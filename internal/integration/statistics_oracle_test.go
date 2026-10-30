//go:build integration

package integration

// Long-term statistics oracles for `ent hist --stats` (D-47, SPEC-ent-stats.md).
//
// A freshly booted rig has no long-term statistics: HA compiles them hourly,
// and only for entities with a state_class. recorder/import_statistics writes
// past-dated hourly rows through HA's own recorder (no database surgery, unlike
// the history backfill rig), so the input is authored and known in advance.
//
// The assertions have the two legs TestRecorderBackfill has:
//
//	Leg 1 (rig ⇄ HA) — what HA answers over raw WebSocket, hactl nowhere in
//	  the path. These are the facts the implementation was built on.
//	Leg 2 (hactl ⇄ HA) — hactl's answer compared against leg 1's, never
//	  against values restated here from hactl's own model.

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"
)

// statsRigHours is how many hourly rows each imported statistic carries.
const statsRigHours = 72

// statsRigIDs are the statistics this file imports. The entity-shaped ones do
// not exist as entities — import_statistics accepts that (oracle-probed), which
// is what lets the "statistic outlives its entity" case exist at all.
const (
	statsMeanID     = "sensor.oracle_stats_temp"
	statsSumID      = "sensor.oracle_stats_meter"
	statsExternalID = "oracle_stats:meter"
)

// statsRigReady records that the rig's statistics are in the shared instance.
var statsRigReady bool

func importStatisticsRig(t *testing.T) time.Time {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Hour)
	if statsRigReady {
		return now
	}
	ws := dialRawWS(t, ha.URL(), ha.Token())

	var meanRows, sumRows []map[string]any
	for h := statsRigHours; h >= 1; h-- {
		start := now.Add(-time.Duration(h) * time.Hour).Format(time.RFC3339)
		v := float64(20 + h%5)
		meanRows = append(meanRows, map[string]any{"start": start, "mean": v, "min": v - 1, "max": v + 1})
		sumRows = append(sumRows, map[string]any{"start": start, "state": float64(1000 + statsRigHours - h), "sum": float64(statsRigHours - h)})
	}
	imports := []map[string]any{
		{"statistic_id": statsMeanID, "source": "recorder", "name": nil, "unit_of_measurement": "°C", "has_mean": true, "has_sum": false},
		{"statistic_id": statsSumID, "source": "recorder", "name": nil, "unit_of_measurement": "kWh", "has_mean": false, "has_sum": true},
		{"statistic_id": statsExternalID, "source": "oracle_stats", "name": "Oracle meter", "unit_of_measurement": "kWh", "has_mean": false, "has_sum": true},
	}
	for _, meta := range imports {
		rows := sumRows
		if meta["has_mean"] == true {
			rows = meanRows
		}
		ws.mustSucceed(t, map[string]any{"type": "recorder/import_statistics", "metadata": meta, "stats": rows})
	}

	// The import is queued on the recorder thread; poll until HA answers rows
	// for every id rather than sleeping for a guessed interval.
	deadline := time.Now().Add(30 * time.Second)
	for {
		got := statsRaw(t, ws, []string{statsMeanID, statsSumID, statsExternalID}, now.Add(-(statsRigHours+1)*time.Hour), now, "hour")
		if len(got[statsMeanID]) == statsRigHours && len(got[statsSumID]) == statsRigHours && len(got[statsExternalID]) == statsRigHours {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("imported statistics did not land within 30s: %d/%d/%d rows",
				len(got[statsMeanID]), len(got[statsSumID]), len(got[statsExternalID]))
		}
		time.Sleep(250 * time.Millisecond)
	}
	statsRigReady = true
	return now
}

// statsRaw asks statistics_during_period over raw WebSocket (leg 1).
func statsRaw(t *testing.T, ws *rawWS, ids []string, start, end time.Time, period string) map[string][]map[string]any {
	t.Helper()
	msg := map[string]any{
		"type": "recorder/statistics_during_period", "statistic_ids": ids, "period": period,
		"start_time": start.UTC().Format(time.RFC3339), "end_time": end.UTC().Format(time.RFC3339),
	}
	env := ws.mustSucceed(t, msg)
	var out map[string][]map[string]any
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatalf("decoding statistics_during_period: %v\n%s", err, env.Result)
	}
	return out
}

// TestOracleStatisticsMetadataNamesOnlyKnownIds — the fork the implementation
// rests on: statistics_during_period answers `{}` for an unknown id AND for a
// known id with no rows in the window, so it cannot distinguish them; the
// metadata call can, because it lists only known ids.
func TestOracleStatisticsMetadataNamesOnlyKnownIds(t *testing.T) {
	now := importStatisticsRig(t)
	ws := dialRawWS(t, ha.URL(), ha.Token())

	if got := statsRaw(t, ws, []string{"sensor.oracle_stats_nope"}, now.Add(-24*time.Hour), now, "hour"); len(got) != 0 {
		t.Errorf("unknown id answered %v, want {}", got)
	}
	if got := statsRaw(t, ws, []string{statsMeanID}, now.Add(-1000*time.Hour), now.Add(-900*time.Hour), "hour"); len(got) != 0 {
		t.Errorf("known id, empty window answered %v, want {} — if HA now answers an empty list "+
			"for a known id, the metadata call is no longer the only way to tell the two apart", got)
	}

	env := ws.mustSucceed(t, map[string]any{
		"type":          "recorder/get_statistics_metadata",
		"statistic_ids": []string{statsMeanID, "sensor.oracle_stats_nope", statsSumID},
	})
	var meta []map[string]any
	if err := json.Unmarshal(env.Result, &meta); err != nil {
		t.Fatalf("decoding metadata: %v", err)
	}
	seen := map[string]map[string]any{}
	for _, m := range meta {
		id, _ := m["statistic_id"].(string)
		if id == "" {
			t.Errorf("metadata row without a statistic_id: %v", m)
		}
		seen[id] = m
	}
	if _, ok := seen["sensor.oracle_stats_nope"]; ok || len(seen) != 2 {
		t.Errorf("metadata answered %v; want exactly the two known ids", meta)
	}
	if m := seen[statsMeanID]; m["has_mean"] != true || m["has_sum"] != false || m["display_unit_of_measurement"] != "°C" {
		t.Errorf("mean metadata = %v", m)
	}
	if m := seen[statsSumID]; m["has_mean"] != false || m["has_sum"] != true {
		t.Errorf("sum metadata = %v", m)
	}
}

// TestOracleStatisticsPeriodsAccepted pins the period vocabulary hactl offers
// (haapi.StatisticPeriods) against what this HA accepts. It runs in both
// matrix lanes, so the previous HA series answers for itself.
func TestOracleStatisticsPeriodsAccepted(t *testing.T) {
	now := importStatisticsRig(t)
	ws := dialRawWS(t, ha.URL(), ha.Token())
	for _, p := range []string{"5minute", "hour", "day", "week", "month", "year"} {
		env := ws.send(t, map[string]any{
			"type": "recorder/statistics_during_period", "statistic_ids": []string{statsMeanID}, "period": p,
			"start_time": now.Add(-24 * time.Hour).Format(time.RFC3339),
		})
		if !env.Success {
			t.Errorf("period %q refused: %s", p, env.Error)
		}
	}
}

// TestOracleStatisticsRowsMatchHA — leg 2: hactl's `ent hist --stats --json`
// carries exactly the rows HA answers, for a mean, a sum and an external
// statistic.
func TestOracleStatisticsRowsMatchHA(t *testing.T) {
	importStatisticsRig(t)
	ws := dialRawWS(t, ha.URL(), ha.Token())

	for _, tc := range []struct {
		id     string
		since  string
		window time.Duration
		period string
		fields []string
	}{
		{statsMeanID, "80h", 80 * time.Hour, "hour", []string{"mean", "min", "max"}},
		{statsSumID, "5d", 120 * time.Hour, "day", []string{"state", "sum", "change"}},
		{statsExternalID, "5d", 120 * time.Hour, "day", []string{"state", "sum", "change"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			rows := runHactlJSON[[]map[string]any](t, "ent", "hist", tc.id, "--stats", "--since", tc.since, "--period", tc.period)
			// Ask HA for the same window hactl asked for. hactl's window ends at
			// its own "now"; the rows are hour-aligned and the newest imported
			// row ends at the rig's truncated hour, so ending at time.Now()
			// selects the same rows.
			want := statsRaw(t, ws, []string{tc.id}, time.Now().Add(-tc.window), time.Now(), tc.period)[tc.id]

			if len(want) == 0 {
				t.Fatalf("HA has no rows for %s — the rig did not land", tc.id)
			}
			if len(rows) != len(want) {
				t.Fatalf("hactl returned %d rows, HA %d", len(rows), len(want))
			}
			for i := range want {
				for _, f := range tc.fields {
					if fmt.Sprint(rows[i][f]) != fmt.Sprint(want[i][f]) {
						t.Errorf("row %d %s: hactl %v, HA %v", i, f, rows[i][f], want[i][f])
					}
				}
				startMS, _ := want[i]["start"].(float64)
				got, err := time.Parse(time.RFC3339Nano, fmt.Sprint(rows[i]["start"]))
				if err != nil || !got.Equal(time.UnixMilli(int64(startMS))) {
					t.Errorf("row %d start: hactl %v, HA %v ms", i, rows[i]["start"], startMS)
				}
			}
		})
	}
}

// TestStatisticsTextAndErrors drives the text rendering and the three
// "no statistic" answers through the real entry point.
func TestStatisticsTextAndErrors(t *testing.T) {
	importStatisticsRig(t)

	out := runHactl(t, "ent", "hist", statsSumID, "--stats", "--since", "5d")
	for _, want := range []string{"long-term statistics (sum), period day", "(kWh)", "change", "state"} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}

	// sun.sun is real and has no state_class, so HA keeps no statistics.
	_, err := runHactlErr(t, "ent", "hist", "sun.sun", "--stats")
	if err == nil || !strings.Contains(err.Error(), "has no long-term statistics") {
		t.Errorf("sun.sun --stats: err = %v", err)
	}
	_, err = runHactlErr(t, "ent", "hist", "sensor.oracle_stats_nope", "--stats")
	if err == nil || !strings.Contains(err.Error(), "no live state and no long-term statistics") {
		t.Errorf("unknown entity --stats: err = %v", err)
	}
	_, err = runHactlErr(t, "ent", "hist", statsExternalID)
	if err == nil || !strings.Contains(err.Error(), "--stats") {
		t.Errorf("external id without --stats: err = %v", err)
	}
}

// historyRaw reads /api/history/period for one entity, hactl nowhere in the
// path, returning the first row's last_changed (zero when empty).
func historyRaw(t *testing.T, entityID string, start time.Time) time.Time {
	t.Helper()
	path := "/api/history/period/" + url.PathEscape(start.UTC().Format(time.RFC3339)) +
		"?filter_entity_id=" + url.QueryEscape(entityID) +
		"&end_time=" + url.QueryEscape(time.Now().UTC().Format(time.RFC3339))
	code, body := ddRequest(t, ha, "GET", path, nil)
	if code != 200 {
		t.Fatalf("history: HTTP %d %s", code, body)
	}
	var rows [][]struct {
		LastChanged string `json:"last_changed"`
	}
	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("decoding history: %v\n%s", err, body)
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return time.Time{}
	}
	ts, err := time.Parse(time.RFC3339Nano, rows[0][0].LastChanged)
	if err != nil {
		t.Fatalf("parsing last_changed %q: %v", rows[0][0].LastChanged, err)
	}
	return ts
}

// waitForHistory polls until HA's history reaches back to (or before)
// earliest — right after boot the recorder's oldest_ts is unset and every
// window starting before boot answers [] (oracle-probed; a single read races
// the recorder's first commit).
func waitForHistory(t *testing.T, entityID string, from time.Time) time.Time {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		if first := historyRaw(t, entityID, from); !first.IsZero() {
			return first
		}
		if time.Now().After(deadline) {
			t.Fatalf("no history for %s from %s within 30s", entityID, from)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// TestOracleHistoryFirstRowMarksCoverage — the premise of the `ent hist`
// coverage note: a window starting before the oldest recorded state answers a
// first row AFTER the window start (the real oldest row), and a window
// starting inside the recorded range answers a first row clamped to the
// window start. If HA ever pads the start of a window it has no data for,
// the note fires never; if it stops clamping, the note fires always.
func TestOracleHistoryFirstRowMarksCoverage(t *testing.T) {
	before := time.Now().Add(-90 * 24 * time.Hour)
	first := waitForHistory(t, "sun.sun", before)
	if !first.After(before) {
		t.Errorf("window from 90 days before boot answered a first row at %s — HA padded a window it has no data for", first)
	}

	time.Sleep(2 * time.Second)
	inside := time.Now().Add(-time.Second)
	if !inside.After(first) {
		t.Fatalf("rig: %s is not inside the recorded range starting %s", inside, first)
	}
	clamped := historyRaw(t, "sun.sun", inside)
	if clamped.After(inside) {
		t.Errorf("window starting inside the recorded range answered a first row at %s, after the window start %s — "+
			"HA stopped clamping, so `ent hist` would note missing coverage on every window", clamped, inside)
	}
}

// TestStatisticsCoverageNoteEndToEnd — an entity whose history starts at
// rig boot while its long-term statistics reach three days back: `ent hist`
// without --stats says so and names the command; `ent show` names the
// statistic; a window the history covers says nothing.
func TestStatisticsCoverageNoteEndToEnd(t *testing.T) {
	const id = "sensor.oracle_stats_live"
	if code, body := ddRequest(t, ha, "POST", "/api/states/"+id, map[string]any{
		"state": "21.5", "attributes": map[string]any{"unit_of_measurement": "°C"},
	}); code != 200 && code != 201 {
		t.Fatalf("creating %s: HTTP %d %s", id, code, body)
	}

	now := time.Now().UTC().Truncate(time.Hour)
	ws := dialRawWS(t, ha.URL(), ha.Token())
	var rows []map[string]any
	for h := statsRigHours; h >= 2; h-- {
		v := float64(20 + h%3)
		rows = append(rows, map[string]any{"start": now.Add(-time.Duration(h) * time.Hour).Format(time.RFC3339), "mean": v, "min": v, "max": v})
	}
	ws.mustSucceed(t, map[string]any{
		"type":     "recorder/import_statistics",
		"metadata": map[string]any{"statistic_id": id, "source": "recorder", "name": nil, "unit_of_measurement": "°C", "has_mean": true, "has_sum": false},
		"stats":    rows,
	})
	deadline := time.Now().Add(30 * time.Second)
	for len(statsRaw(t, ws, []string{id}, now.Add(-(statsRigHours+1)*time.Hour), now, "hour")[id]) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("imported statistics did not land within 30s")
		}
		time.Sleep(250 * time.Millisecond)
	}
	historyStart := waitForHistory(t, id, time.Now().Add(-5*24*time.Hour))

	out := runHactl(t, "ent", "hist", id, "--since", "5d")
	for _, want := range []string{"note: recorder history starts", "long-term statistics start ",
		"hactl ent hist " + id + " --since 5d --stats"} {
		if !strings.Contains(out, want) {
			t.Errorf("ent hist --since 5d missing %q:\n%s", want, out)
		}
	}

	// A window inside the recorded history is covered: no note.
	time.Sleep(time.Until(historyStart.Add(3 * time.Second)))
	if out := runHactl(t, "ent", "hist", id, "--since", "1s"); strings.Contains(out, "note:") {
		t.Errorf("a covered window produced a note:\n%s", out)
	}

	show := runHactl(t, "ent", "show", id)
	if !strings.Contains(show, "statistics:   mean °C") {
		t.Errorf("ent show does not name the statistic:\n%s", show)
	}
	if show := runHactl(t, "ent", "show", "sun.sun"); !strings.Contains(show, "statistics:   none") {
		t.Errorf("ent show sun.sun:\n%s", show)
	}
}

// TestOracleStatisticIDsAreCaseSensitiveWhereStatesAreNot — the premise of
// `ent hist --stats` lowercasing its id: HA's state API answers an uppercase
// entity id with the lowercase entity, while the statistics API treats the
// uppercase id as unknown. Live, `SENSOR.X --stats` found the entity and then
// denied it had statistics.
func TestOracleStatisticIDsAreCaseSensitiveWhereStatesAreNot(t *testing.T) {
	importStatisticsRig(t)
	if code, body := ddRequest(t, ha, "GET", "/api/states/SUN.SUN", nil); code != 200 || !strings.Contains(body, `"entity_id":"sun.sun"`) {
		t.Errorf("GET /api/states/SUN.SUN: HTTP %d %s — if the state API became case-sensitive, "+
			"errNoStatistic's entity lookup no longer agrees with the lowercased statistics lookup", code, body)
	}
	ws := dialRawWS(t, ha.URL(), ha.Token())
	env := ws.mustSucceed(t, map[string]any{
		"type": "recorder/get_statistics_metadata", "statistic_ids": []string{strings.ToUpper(statsMeanID)},
	})
	if string(env.Result) != "[]" {
		t.Errorf("uppercase statistic id answered %s — the statistics API now folds case; "+
			"the lowercasing in runEntHistStats is harmless but no longer needed", env.Result)
	}

	out := runHactl(t, "ent", "hist", strings.ToUpper(statsMeanID), "--stats", "--since", "80h")
	if !strings.Contains(out, statsMeanID+": long-term statistics") {
		t.Errorf("uppercase id through hactl:\n%s", out)
	}
}

// TestStatisticsEmptyWindowNamesTheNewestRow — the rig's statistics end at
// the last full hour before the import, so a window of the last minute is
// empty and the answer names where the rows end. Also pins that
// statistics_during_period accepts a start at the Unix epoch, which
// newestStatistic sends.
func TestStatisticsEmptyWindowNamesTheNewestRow(t *testing.T) {
	importStatisticsRig(t)
	out := runHactl(t, "ent", "hist", statsMeanID, "--stats", "--since", "1m")
	for _, want := range []string{"no long-term statistics in the last 1m", "note: the newest row ends",
		"hactl ent hist " + statsMeanID + " --stats --since 1d"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q:\n%s", want, out)
		}
	}

	// An id without an entity and without history points at its statistics.
	_, err := runHactlErr(t, "ent", "hist", statsMeanID, "--since", "1h")
	if err == nil || !strings.Contains(err.Error(), "it has long-term statistics") {
		t.Errorf("plain ent hist on a statistic-only id: err = %v", err)
	}
}
