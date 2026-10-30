package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// The wire shapes below are the ones TestOracleStatistics* captured from HA
// 2026.10.4 (internal/integration/statistics_oracle_test.go): metadata lists
// only known ids, rows carry start/end as epoch milliseconds, a mean statistic
// carries mean/min/max and a sum statistic state/sum/change.

const statsHour = int64(3600 * 1000)

func meanMetadata(id string) []any {
	return []any{map[string]any{
		"statistic_id": id, "source": "recorder", "name": nil,
		"display_unit_of_measurement": "°C", "statistics_unit_of_measurement": "°C",
		"has_mean": true, "mean_type": 1, "has_sum": false, "unit_class": "temperature",
	}}
}

func sumMetadata(id string) []any {
	return []any{map[string]any{
		"statistic_id": id, "source": "recorder", "name": "Meter",
		"display_unit_of_measurement": "kWh", "statistics_unit_of_measurement": "kWh",
		"has_mean": false, "mean_type": 0, "has_sum": true, "unit_class": "energy",
	}}
}

func statsRigState(entityID string) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/api/states/" + entityID: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `{"entity_id":%q,"state":"21.5","attributes":{},`+
				`"last_changed":"2026-10-29T10:00:00+00:00","last_updated":"2026-10-29T10:00:00+00:00",`+
				`"context":{"id":"c","parent_id":null,"user_id":null}}`, entityID)
		},
	}
}

func withEntHistFlags(t *testing.T, since string, stats bool, period string) {
	t.Helper()
	oldSince, oldStats, oldPeriod, oldJSON := flagSince, flagEntStats, flagEntPeriod, flagJSON
	flagSince, flagEntStats, flagEntPeriod = since, stats, period
	t.Cleanup(func() {
		flagSince, flagEntStats, flagEntPeriod, flagJSON = oldSince, oldStats, oldPeriod, oldJSON
	})
}

func TestAutoStatisticPeriodKeepsAboutFiftyRows(t *testing.T) {
	day := 24 * time.Hour
	for _, tc := range []struct {
		window time.Duration
		want   string
	}{
		{24 * time.Hour, "hour"},
		{50 * time.Hour, "hour"},
		{51 * time.Hour, "day"},
		{7 * day, "day"},
		{30 * day, "day"},
		{50 * day, "day"},
		{90 * day, "week"},
		{180 * day, "week"},
		{350 * day, "week"},
		{365 * day, "month"},
		{730 * day, "month"},
	} {
		if got := autoStatisticPeriod(tc.window); got != tc.want {
			t.Errorf("autoStatisticPeriod(%s) = %s, want %s", tc.window, got, tc.want)
		}
	}
}

func TestEntHistStatsFlagContract(t *testing.T) {
	for _, tc := range []struct {
		name   string
		stats  bool
		period string
		id     string
		want   string
	}{
		{"period without stats", false, "day", "sensor.x", "add --stats"},
		{"unknown period", true, "fortnight", "sensor.x", "invalid --period"},
		{"external id without stats", false, "", "tibber:energy_x", "--stats"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withEntHistFlags(t, "24h", tc.stats, tc.period)
			// No server: the refusal must come before any request.
			withFlagDir(t, t.TempDir())
			err := runEntHist(context.Background(), io.Discard, io.Discard, tc.id)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
		})
	}
}

// TestEntHistStatsRefusesAttrAndResample drives the real entry point: the
// exclusion is cobra's, so it only exists there.
func TestEntHistStatsRefusesAttrAndResample(t *testing.T) {
	for _, extra := range [][]string{{"--attr", "brightness"}, {"--resample", "1h"}} {
		args := append([]string{"hactl", "ent", "hist", "sensor.x", "--stats"}, extra...)
		var buf bytes.Buffer
		err := RunWithOutputContext(context.Background(), args, &buf)
		if err == nil || !strings.Contains(err.Error(), "stats") {
			t.Errorf("%v: err = %v, want a refusal naming --stats", args, err)
		}
	}
}

func TestEntHistStatsRendersMeanStatistic(t *testing.T) {
	start := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC).UnixMilli()
	ts := startCmdServer(t, map[string]any{
		"recorder/get_statistics_metadata": meanMetadata("sensor.temp"),
		"recorder/statistics_during_period": map[string]any{"sensor.temp": []any{
			map[string]any{"start": start, "end": start + 24*statsHour, "mean": 21.5, "min": 19.0, "max": 24.25, "last_reset": nil},
			map[string]any{"start": start + 24*statsHour, "end": start + 48*statsHour, "mean": 22.0, "min": 20.0, "max": 25.0, "last_reset": nil},
		}},
	}, nil)
	withFlagDir(t, ts.dir)
	withEntHistFlags(t, "30d", true, "")

	var buf bytes.Buffer
	if err := runEntHist(context.Background(), &buf, io.Discard, "sensor.temp"); err != nil {
		t.Fatalf("runEntHist --stats: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"long-term statistics (mean), period day, 2 rows (°C)", "mean", "min", "max", "21.50", "19.00", "24.25"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "change") {
		t.Errorf("a mean statistic rendered sum columns:\n%s", out)
	}
	params := ts.lastParams("recorder/statistics_during_period")
	if params["period"] != "day" {
		t.Errorf("auto period for 30d sent %v, want day", params["period"])
	}
	if ids, _ := params["statistic_ids"].([]any); len(ids) != 1 || ids[0] != "sensor.temp" {
		t.Errorf("statistic_ids sent %v", params["statistic_ids"])
	}
}

func TestEntHistStatsRendersSumStatisticAsJSON(t *testing.T) {
	start := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixMilli()
	ts := startCmdServer(t, map[string]any{
		"recorder/get_statistics_metadata": sumMetadata("sensor.meter"),
		"recorder/statistics_during_period": map[string]any{"sensor.meter": []any{
			map[string]any{"start": start, "end": start + 720*statsHour, "state": 1071.0, "sum": 71.0, "change": 71.0, "last_reset": nil},
		}},
	}, nil)
	withFlagDir(t, ts.dir)
	withEntHistFlags(t, "365d", true, "month")
	flagJSON = true

	var buf bytes.Buffer
	if err := runEntHist(context.Background(), &buf, io.Discard, "sensor.meter"); err != nil {
		t.Fatalf("runEntHist --stats --json: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("--json is not a JSON array: %v\n%s", err, buf.String())
	}
	if len(rows) != 1 {
		t.Fatalf("got %d rows, want 1: %s", len(rows), buf.String())
	}
	row := rows[0]
	if row["change"] != 71.0 || row["state"] != 1071.0 || row["sum"] != 71.0 {
		t.Errorf("sum fields lost: %v", row)
	}
	// Every field is present, null where the statistic has none (H-10).
	for _, k := range []string{"mean", "min", "max"} {
		if v, ok := row[k]; !ok || v != nil {
			t.Errorf("%s = %v (present %v), want an explicit null", k, v, ok)
		}
	}
	got, err := time.Parse(time.RFC3339Nano, fmt.Sprint(row["start"]))
	if err != nil || !got.Equal(time.UnixMilli(start)) {
		t.Errorf("start = %v, want the instant %s", row["start"], time.UnixMilli(start).UTC())
	}
	if ts.lastParams("recorder/statistics_during_period")["period"] != "month" {
		t.Errorf("--period month was not sent")
	}
}

func TestEntHistStatsSaysWhyThereAreNone(t *testing.T) {
	for _, tc := range []struct {
		name  string
		id    string
		known bool
		want  string
	}{
		{"real entity without state_class", "sensor.temp", true, "has no long-term statistics"},
		{"no such entity", "sensor.nope", false, "no live state and no long-term statistics"},
		{"unknown external id", "tibber:nope", false, "energy show"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var handlers map[string]http.HandlerFunc
			if tc.known {
				handlers = statsRigState(tc.id)
			}
			ts := startCmdServer(t, map[string]any{"recorder/get_statistics_metadata": []any{}}, handlers)
			withFlagDir(t, ts.dir)
			withEntHistFlags(t, "24h", true, "")

			err := runEntHist(context.Background(), io.Discard, io.Discard, tc.id)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want one containing %q", err, tc.want)
			}
			if n := ts.commandCount("recorder/statistics_during_period"); n != 0 {
				t.Errorf("asked for rows of a statistic the metadata says does not exist (%d calls)", n)
			}
		})
	}
}

// TestEntHistStatsEmptyWindowIsAnEmptyList: statistics_during_period answers
// `{}` both for an unknown id and for an empty window; the metadata decides
// which, and a known statistic with no rows is a fact, not an error (H-7).
func TestEntHistStatsEmptyWindowIsAnEmptyList(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"recorder/get_statistics_metadata":  meanMetadata("sensor.temp"),
		"recorder/statistics_during_period": map[string]any{},
	}, nil)
	withFlagDir(t, ts.dir)
	withEntHistFlags(t, "24h", true, "")

	var buf bytes.Buffer
	if err := runEntHist(context.Background(), &buf, io.Discard, "sensor.temp"); err != nil {
		t.Fatalf("an empty window on a known statistic errored: %v", err)
	}
	if !strings.Contains(buf.String(), "no long-term statistics in the last 24h") {
		t.Errorf("output = %q", buf.String())
	}
}

// historyFrom serves sensor.temp's /api/history/period with its first numeric
// row at first.
func historyFrom(first time.Time) map[string]http.HandlerFunc {
	const entityID = "sensor.temp"
	h := statsRigState(entityID)
	h["/api/history/period/"] = func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprintf(w, `[[{"entity_id":%q,"state":"21.0","last_changed":%q},`+
			`{"entity_id":%q,"state":"22.0","last_changed":%q}]]`,
			entityID, first.UTC().Format(time.RFC3339Nano),
			entityID, first.Add(time.Hour).UTC().Format(time.RFC3339Nano))
	}
	return h
}

func TestEntHistNotesStatisticsReachingFurther(t *testing.T) {
	historyStart := time.Now().Add(-10 * 24 * time.Hour)
	statsStart := time.Now().Add(-89 * 24 * time.Hour).Truncate(24 * time.Hour)
	ts := startCmdServer(t, map[string]any{
		"recorder/get_statistics_metadata": meanMetadata("sensor.temp"),
		"recorder/statistics_during_period": map[string]any{"sensor.temp": []any{
			map[string]any{"start": statsStart.UnixMilli(), "end": statsStart.Add(24 * time.Hour).UnixMilli(), "mean": 20.0, "min": 19.0, "max": 21.0},
		}},
	}, historyFrom(historyStart))
	withFlagDir(t, ts.dir)
	withEntHistFlags(t, "90d", false, "")

	var out, errOut bytes.Buffer
	if err := runEntHist(context.Background(), &out, &errOut, "sensor.temp"); err != nil {
		t.Fatalf("runEntHist: %v", err)
	}
	for _, want := range []string{"note: recorder history starts", "long-term statistics start ",
		"hactl ent hist sensor.temp --since 90d --stats"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("text output missing %q:\n%s", want, out.String())
		}
	}
	if errOut.Len() != 0 {
		t.Errorf("text mode wrote to stderr: %q", errOut.String())
	}
	// The probe asks only for the part of the window the history misses.
	params := ts.lastParams("recorder/statistics_during_period")
	if params["period"] != "day" {
		t.Errorf("coverage probe used period %v, want day", params["period"])
	}
	end, _ := time.Parse(time.RFC3339, fmt.Sprint(params["end_time"]))
	if end.Sub(historyStart).Abs() > time.Second {
		t.Errorf("coverage probe ended at %v, want the history start %v", end, historyStart)
	}

	// --json: the document is unchanged, the note moves to stderr.
	out.Reset()
	errOut.Reset()
	flagJSON = true
	if err := runEntHist(context.Background(), &out, &errOut, "sensor.temp"); err != nil {
		t.Fatalf("runEntHist --json: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rows); err != nil {
		t.Errorf("--json stdout is not one JSON document: %v\n%s", err, out.String())
	}
	if !strings.Contains(errOut.String(), "--stats") {
		t.Errorf("--json did not carry the note on stderr: %q", errOut.String())
	}
}

// TestEntHistCoveredWindowAsksNothing: when the first history row is the
// clamped window start, the history covers the window and no statistics
// request is made.
func TestEntHistCoveredWindowAsksNothing(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"recorder/get_statistics_metadata": meanMetadata("sensor.temp"),
	}, historyFrom(time.Now().Add(-25*time.Hour)))
	withFlagDir(t, ts.dir)
	withEntHistFlags(t, "24h", false, "")

	var out bytes.Buffer
	if err := runEntHist(context.Background(), &out, io.Discard, "sensor.temp"); err != nil {
		t.Fatalf("runEntHist: %v", err)
	}
	if strings.Contains(out.String(), "note:") {
		t.Errorf("covered window produced a note:\n%s", out.String())
	}
	if n := ts.commandCount("recorder/get_statistics_metadata"); n != 0 {
		t.Errorf("covered window still asked for statistics metadata (%d calls)", n)
	}
}

// TestEntHistNoteSaysWhenItCouldNotCheck: "no note" is documented to mean
// nothing further to find, so a failed check must not be silent.
func TestEntHistNoteSaysWhenItCouldNotCheck(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"recorder/get_statistics_metadata": wsErrorResponse{Code: "unknown_command", Message: "Unknown command."},
	}, historyFrom(time.Now().Add(-10*24*time.Hour)))
	withFlagDir(t, ts.dir)
	withEntHistFlags(t, "90d", false, "")

	var out bytes.Buffer
	if err := runEntHist(context.Background(), &out, io.Discard, "sensor.temp"); err != nil {
		t.Fatalf("runEntHist: %v", err)
	}
	if !strings.Contains(out.String(), "could not check long-term statistics") {
		t.Errorf("failed check was silent:\n%s", out.String())
	}
}

func TestEntShowNamesStatistics(t *testing.T) {
	for _, tc := range []struct {
		name     string
		metadata any
		wantText string
		wantJSON func(map[string]any) error
	}{
		{"mean", meanMetadata("light.kitchen"), "statistics:   mean °C (ent hist --stats)", func(m map[string]any) error {
			s, ok := m["statistics"].(map[string]any)
			if !ok || s["kind"] != "mean" || s["unit"] != "°C" {
				return fmt.Errorf("statistics = %v", m["statistics"])
			}
			return nil
		}},
		{"none", []any{}, "statistics:   none", func(m map[string]any) error {
			if v, ok := m["statistics"]; !ok || v != nil {
				return fmt.Errorf("statistics = %v (present %v), want null", v, ok)
			}
			return nil
		}},
		{"unknown", wsErrorResponse{Code: "unknown_command", Message: "Unknown command."}, "statistics:   unknown (", func(m map[string]any) error {
			if _, ok := m["statistics"]; ok {
				return errors.New("a failed check rendered a statistics field — it would read as an answer")
			}
			if _, ok := m["statistics_error"]; !ok {
				return errors.New("statistics_error missing")
			}
			return nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := startCmdServer(t, map[string]any{
				"recorder/get_statistics_metadata": tc.metadata,
				"config/entity_registry/list":      []any{},
				"config/area_registry/list":        []any{},
				"config/label_registry/list":       []any{},
				"config/floor_registry/list":       []any{},
				"config/auth/list":                 []map[string]any{},
			}, map[string]http.HandlerFunc{
				"/api/states/light.kitchen": func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(stateJSON("")))
				},
			})
			withFlagDir(t, ts.dir)
			oldJSON := flagJSON
			t.Cleanup(func() { flagJSON = oldJSON })

			flagJSON = false
			var buf bytes.Buffer
			if err := runEntShow(context.Background(), &buf, "light.kitchen"); err != nil {
				t.Fatalf("runEntShow: %v", err)
			}
			if !strings.Contains(buf.String(), tc.wantText) {
				t.Errorf("text missing %q:\n%s", tc.wantText, buf.String())
			}

			flagJSON = true
			buf.Reset()
			if err := runEntShow(context.Background(), &buf, "light.kitchen"); err != nil {
				t.Fatalf("runEntShow --json: %v", err)
			}
			var m map[string]any
			if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
				t.Fatalf("--json: %v", err)
			}
			if err := tc.wantJSON(m); err != nil {
				t.Error(err)
			}
		})
	}
}

// TestEntHistNoteNeverClaimsBeyondTheWindow: the coverage probe is bounded by
// the window, so a first daily row at or before the window start means the
// window is covered — not that statistics "reach back to" that day (the
// first live run printed the window's own start date as if it were a finding).
func TestEntHistNoteNeverClaimsBeyondTheWindow(t *testing.T) {
	dayStart := time.Now().Add(-91 * 24 * time.Hour).Truncate(24 * time.Hour)
	ts := startCmdServer(t, map[string]any{
		"recorder/get_statistics_metadata": meanMetadata("sensor.temp"),
		"recorder/statistics_during_period": map[string]any{"sensor.temp": []any{
			map[string]any{"start": dayStart.UnixMilli(), "end": dayStart.Add(24 * time.Hour).UnixMilli(), "mean": 20.0, "min": 19.0, "max": 21.0},
		}},
	}, historyFrom(time.Now().Add(-10*24*time.Hour)))
	withFlagDir(t, ts.dir)
	withEntHistFlags(t, "90d", false, "")

	var out bytes.Buffer
	if err := runEntHist(context.Background(), &out, io.Discard, "sensor.temp"); err != nil {
		t.Fatalf("runEntHist: %v", err)
	}
	if !strings.Contains(out.String(), "long-term statistics cover the whole window") {
		t.Errorf("note does not say the window is covered:\n%s", out.String())
	}
}

// --- cases from the live run against a real instance (2026-10-29) ---

// TestEntHistStatsNamesTheRealCauseOfMissingStatistics: the live
// sensor.balkon_illuminance HAS state_class measurement but no statistics (the
// recorder holds no rows for it), and its old statistics live under
// sensor.balkon_illuminance_2. The answer used to say "HA keeps them only for
// entities with a state_class" — false for this entity.
func TestEntHistStatsNamesTheRealCauseOfMissingStatistics(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"recorder/get_statistics_metadata": []any{},
		"recorder/list_statistic_ids": append(append(meanMetadata("sensor.lux_2"),
			meanMetadata("sensor.cell_7")...), meanMetadata("sensor.cell_8")...),
	}, map[string]http.HandlerFunc{
		"/api/states/sensor.lux": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `{"entity_id":"sensor.lux","state":"695.7","attributes":{"state_class":"measurement"},`+
				`"last_changed":"2026-10-29T10:00:00+00:00","last_updated":"2026-10-29T10:00:00+00:00",`+
				`"context":{"id":"c","parent_id":null,"user_id":null}}`)
		},
	})
	withFlagDir(t, ts.dir)
	withEntHistFlags(t, "24h", true, "")

	err := runEntHist(context.Background(), io.Discard, io.Discard, "sensor.lux")
	if err == nil {
		t.Fatal("no statistics answered nil")
	}
	msg := err.Error()
	if strings.Contains(msg, "only for entities with a state_class") {
		t.Errorf("claims the entity lacks a state_class it has: %s", msg)
	}
	for _, want := range []string{"state_class measurement", "sensor.lux_2"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message missing %q: %s", want, msg)
		}
	}

	// Siblings sharing a numbered stem are not renames.
	err = runEntHist(context.Background(), io.Discard, io.Discard, "sensor.cell_9")
	if err == nil || strings.Contains(err.Error(), "cell_7") || strings.Contains(err.Error(), "cell_8") {
		t.Errorf("sibling statistics named as a rename: %v", err)
	}
}

// TestEntHistStatsLowercasesTheID: HA's state API is case-insensitive, its
// statistics API is not; `SENSOR.X --stats` found the entity and then denied
// it had statistics.
func TestEntHistStatsLowercasesTheID(t *testing.T) {
	start := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC).UnixMilli()
	ts := startCmdServer(t, map[string]any{
		"recorder/get_statistics_metadata": meanMetadata("sensor.temp"),
		"recorder/statistics_during_period": map[string]any{"sensor.temp": []any{
			map[string]any{"start": start, "end": start + statsHour, "mean": 21.5, "min": 19.0, "max": 24.25},
		}},
	}, nil)
	withFlagDir(t, ts.dir)
	withEntHistFlags(t, "24h", true, "")

	var buf bytes.Buffer
	if err := runEntHist(context.Background(), &buf, io.Discard, "SENSOR.Temp"); err != nil {
		t.Fatalf("uppercase id: %v", err)
	}
	if ids, _ := ts.lastParams("recorder/get_statistics_metadata")["statistic_ids"].([]any); len(ids) != 1 || ids[0] != "sensor.temp" {
		t.Errorf("metadata asked for %v, want [sensor.temp]", ids)
	}
}

// TestEntHistStatsEmptyWindowNamesTheNewestRow: a deleted entity's statistic
// keeps its rows forever; the live one ended in 2022 and the answer was only
// "nothing in the last 365d".
func TestEntHistStatsEmptyWindowNamesTheNewestRow(t *testing.T) {
	// The mock answers every period with the same rows, so it cannot serve an
	// empty window and a non-empty history at once; this drives the helper
	// the empty-window path calls. 2022-10-31 22:00 → 2026-10-29 12:00 is
	// 1458.6 days, so the smallest covering window is 1459d.
	month := time.Date(2022, 10, 1, 0, 0, 0, 0, time.UTC)
	lastHour := time.Date(2022, 10, 31, 22, 0, 0, 0, time.UTC)
	ts := startCmdServer(t, map[string]any{
		"recorder/statistics_during_period": map[string]any{"sensor.old": []any{
			map[string]any{"start": month.UnixMilli(), "end": month.AddDate(0, 1, 0).UnixMilli(), "mean": 44.5},
			map[string]any{"start": lastHour.UnixMilli(), "end": lastHour.Add(time.Hour).UnixMilli(), "mean": 0.0},
		}},
	}, nil)
	withFlagDir(t, ts.dir)
	withEntHistFlags(t, "365d", true, "")

	ws := haapi.NewWSClient(ts.srv.URL, "test-token")
	if err := ws.Connect(context.Background()); err != nil {
		t.Fatalf("connecting to the rig: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	now := time.Date(2026, 10, 29, 12, 0, 0, 0, time.UTC)
	hint := newestStatisticHint(context.Background(), ws, "sensor.old", now)
	if !strings.Contains(hint, "hactl ent hist sensor.old --stats --since 1459d") {
		t.Errorf("hint = %q, want a --since reaching the newest row", hint)
	}
	if ts.lastParams("recorder/statistics_during_period")["period"] != "hour" {
		t.Errorf("the newest row was not narrowed to the hour")
	}
}

// TestEntHistUnknownEntityPointsAtItsStatistics: `ent hist` on a deleted
// entity said "no live state, and no recorded history" while years of its
// statistics were one flag away.
func TestEntHistUnknownEntityPointsAtItsStatistics(t *testing.T) {
	end := time.Now().Add(-400 * 24 * time.Hour).Truncate(time.Hour)
	ts := startCmdServer(t, map[string]any{
		"recorder/get_statistics_metadata": meanMetadata("sensor.gone"),
		"recorder/statistics_during_period": map[string]any{"sensor.gone": []any{
			map[string]any{"start": end.Add(-time.Hour).UnixMilli(), "end": end.UnixMilli(), "mean": 80.0},
		}},
	}, map[string]http.HandlerFunc{
		"/api/history/period/": func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, "[]") },
	})
	withFlagDir(t, ts.dir)
	withEntHistFlags(t, "30d", false, "")

	err := runEntHist(context.Background(), io.Discard, io.Discard, "sensor.gone")
	if err == nil {
		t.Fatal("unknown entity answered nil")
	}
	for _, want := range []string{"no live state", "it has long-term statistics", "hactl ent hist sensor.gone --stats --since 401d"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q: %v", want, err)
		}
	}
}

// TestEntHistFailedHistoryPointsAtStatistics: a 60d window over a busy sensor
// answered 504 three times in 272s on the live instance; --stats answered the
// same window in 0.1s.
func TestEntHistFailedHistoryPointsAtStatistics(t *testing.T) {
	start := time.Now().Add(-2 * time.Hour).Truncate(time.Hour)
	ts := startCmdServer(t, map[string]any{
		"recorder/get_statistics_metadata": meanMetadata("sensor.busy"),
		"recorder/statistics_during_period": map[string]any{"sensor.busy": []any{
			map[string]any{"start": start.UnixMilli(), "end": start.Add(time.Hour).UnixMilli(), "mean": 5.0},
		}},
	}, map[string]http.HandlerFunc{
		"/api/history/period/": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "upstream timed out", http.StatusBadRequest)
		},
	})
	withFlagDir(t, ts.dir)
	withEntHistFlags(t, "60d", false, "")

	err := runEntHist(context.Background(), io.Discard, io.Discard, "sensor.busy")
	if err == nil || !strings.Contains(err.Error(), "hactl ent hist sensor.busy --stats --since 60d") {
		t.Errorf("err = %v, want a pointer to --stats over the same window", err)
	}
}
