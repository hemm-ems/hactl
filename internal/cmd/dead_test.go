package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
)

type fakeDeadHistory struct {
	answer  map[string][]haapi.MinimalHistoryRow
	batches [][]string
	failAt  int // 1-based batch that fails; 0 = never
}

func (f *fakeDeadHistory) GetHistoryMinimal(_ context.Context, _, _ string, ids []string) (map[string][]haapi.MinimalHistoryRow, error) {
	f.batches = append(f.batches, ids)
	if f.failAt == len(f.batches) {
		return nil, errors.New("boom")
	}
	out := map[string][]haapi.MinimalHistoryRow{}
	for _, id := range ids {
		if rows, ok := f.answer[id]; ok {
			out[id] = rows
		}
	}
	return out, nil
}

func TestValidStateKnowsTimestampDomains(t *testing.T) {
	for _, tc := range []struct {
		id, state string
		want      bool
	}{
		{"sensor.x", "21", true},
		{"sensor.x", "unknown", false},
		{"sensor.x", "unavailable", false},
		{"button.restart", "unknown", true}, // never pressed is healthy
		{"input_button.x", "unknown", true},
		{"scene.x", "unknown", true},
		{"event.doorbell", "unknown", true},
		{"notify.x", "unknown", true},
		{"button.restart", "unavailable", false}, // unavailable is never valid
	} {
		if got := validState(tc.id, tc.state); got != tc.want {
			t.Errorf("validState(%s, %s) = %v, want %v", tc.id, tc.state, got, tc.want)
		}
	}
}

// TestJudgeDeadDecidesFromWhatItHoldsFirst: alive, ghost and
// unchanged-since-before-the-window need no recorder request; only the rest
// is asked about, and the answer is read per entity_id.
func TestJudgeDeadDecidesFromWhatItHoldsFirst(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	window := 7 * 24 * time.Hour
	start := now.Add(-window)
	at := func(d time.Duration) string { return start.Add(d).Format(time.RFC3339Nano) }
	states := []entityState{
		{EntityID: "sensor.alive", State: "21", LastChanged: at(time.Hour)},
		{EntityID: "sensor.ghost", State: "unavailable", LastChanged: at(time.Hour), Attributes: wireAttributes{"restored": true}},
		{EntityID: "sensor.old_dead", State: "unavailable", LastChanged: at(-time.Hour)},
		{EntityID: "sensor.flapped", State: "unavailable", LastChanged: at(2 * time.Hour)},
		{EntityID: "sensor.dead_since_restart", State: "unavailable", LastChanged: at(3 * time.Hour)},
		{EntityID: "sensor.unrecorded", State: "unknown", LastChanged: at(time.Hour)},
		{EntityID: "sensor.new", State: "unavailable", LastChanged: at(5 * time.Hour)},
		{EntityID: "button.never", State: "unknown", LastChanged: at(time.Hour)},
	}
	f := &fakeDeadHistory{answer: map[string][]haapi.MinimalHistoryRow{
		"sensor.flapped": {
			{EntityID: "sensor.flapped", State: "unavailable", LastChanged: at(0)},
			{State: "3", LastChanged: at(time.Hour)},
			{State: "unavailable", LastChanged: at(2 * time.Hour)},
		},
		"sensor.dead_since_restart": {
			{EntityID: "sensor.dead_since_restart", State: "unavailable", LastChanged: at(0)},
			{State: "unknown", LastChanged: at(3 * time.Hour)},
		},
		// First row after the window opens: the recorder does not reach back.
		"sensor.new": {{EntityID: "sensor.new", State: "unavailable", LastChanged: at(5 * time.Hour)}},
	}}
	got, err := judgeDead(context.Background(), f, states, now, window)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]deadVerdict{
		"sensor.ghost":              {reason: deadGhost, dead: true},
		"sensor.old_dead":           {reason: deadUnchanged, dead: true},
		"sensor.dead_since_restart": {reason: deadNoValid, dead: true},
		"sensor.unrecorded":         {reason: deadUnrecorded},
		"sensor.new":                {reason: deadPartlyKnown},
	}
	if len(got) != len(want) {
		t.Errorf("verdicts = %v, want %v", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s = %+v, want %+v", id, got[id], w)
		}
	}
	asked := strings.Join(f.batches[0], ",")
	if asked != "sensor.flapped,sensor.dead_since_restart,sensor.unrecorded,sensor.new" || len(f.batches) != 1 {
		t.Errorf("asked the recorder about %v, want only the four it could not decide", f.batches)
	}
}

func TestHistoryBatchesStayWithinBudget(t *testing.T) {
	ids := make([]string, 0, 501)
	for i := range 500 {
		ids = append(ids, "sensor.some_rather_long_entity_name_"+strings.Repeat("x", i%7)+"_"+string(rune('a'+i%26)))
	}
	ids = append(ids, "sensor."+strings.Repeat("y", deadHistoryBatchBytes+10))
	batches := historyBatches(ids)
	n := 0
	for _, b := range batches {
		n += len(b)
		if j := strings.Join(b, ","); len(j) > deadHistoryBatchBytes && len(b) > 1 {
			t.Errorf("batch of %d ids is %d bytes", len(b), len(j))
		}
	}
	if n != len(ids) || len(batches) < 2 {
		t.Errorf("batches carry %d of %d ids in %d batches", n, len(ids), len(batches))
	}
}

func TestJudgeDeadStopsAtFirstFailedBatch(t *testing.T) {
	now := time.Now().UTC()
	states := make([]entityState, 0, 400)
	for i := range 400 {
		states = append(states, entityState{
			EntityID:    "sensor.candidate_with_a_long_name_" + strings.Repeat("z", 20) + string(rune('a'+i%26)) + string(rune('a'+i/26)),
			State:       "unavailable",
			LastChanged: now.Add(-time.Hour).Format(time.RFC3339Nano),
		})
	}
	f := &fakeDeadHistory{failAt: 2}
	_, err := judgeDead(context.Background(), f, states, now, 24*time.Hour)
	if err == nil || !strings.Contains(err.Error(), "batch 2 of") {
		t.Fatalf("err = %v, want the failing batch named", err)
	}
	if len(f.batches) != 2 {
		t.Errorf("sent %d batches after a failure, want to stop at 2", len(f.batches))
	}
}

func TestParseDeadWindow(t *testing.T) {
	for raw, ok := range map[string]bool{"7d": true, "30d": true, "36h": true, "31d": false, "0h": false, "-1d": false, "x": false} {
		if _, err := parseDeadWindow(raw); (err == nil) != ok {
			t.Errorf("parseDeadWindow(%q) err = %v, want ok=%v", raw, err, ok)
		}
	}
}

func TestJudgeDevice(t *testing.T) {
	ents := func(ids ...string) []haapi.EntityRegistryEntry {
		out := make([]haapi.EntityRegistryEntry, 0, len(ids))
		for _, id := range ids {
			e := haapi.EntityRegistryEntry{EntityID: id}
			if strings.HasPrefix(id, "disabled.") {
				e.DisabledBy = "user"
			}
			out = append(out, e)
		}
		return out
	}
	has := map[string]bool{"sensor.a": true, "sensor.b": true, "button.r": true}
	dead := deadVerdict{reason: deadNoValid, dead: true}
	undecided := deadVerdict{reason: deadUnrecorded}
	for _, tc := range []struct {
		name      string
		entities  []haapi.EntityRegistryEntry
		verdicts  map[string]deadVerdict
		wantAlive bool
		want      deadVerdict
	}{
		{"all dead", ents("sensor.a", "sensor.b", "disabled.x"), map[string]deadVerdict{"sensor.a": dead, "sensor.b": dead}, false, deadVerdict{reason: deadAllEntities, dead: true}},
		{"one alive", ents("sensor.a", "button.r"), map[string]deadVerdict{"sensor.a": dead}, true, deadVerdict{}},
		{"undecided", ents("sensor.a", "sensor.b"), map[string]deadVerdict{"sensor.a": dead, "sensor.b": undecided}, false, deadVerdict{reason: deadPartlyDecided}},
		{"nothing to judge", ents("disabled.x", "sensor.stateless"), nil, false, deadVerdict{reason: deadNoStateful}},
	} {
		got, alive := judgeDevice(tc.entities, has, tc.verdicts)
		if alive != tc.wantAlive || got != tc.want {
			t.Errorf("%s: got %+v alive=%v, want %+v alive=%v", tc.name, got, alive, tc.want, tc.wantAlive)
		}
	}
}

// TestDeadMeansOneWindowEverywhere: every command offering --dead takes the
// same value type, so one parse, one cap and one refusal hold for all of
// them (D-238), and each one acts on it.
func TestDeadMeansOneWindowEverywhere(t *testing.T) {
	var seen []string
	for _, leaf := range leafCommands(rootCmd) {
		f := leaf.Flags().Lookup("dead")
		if f == nil {
			continue
		}
		seen = append(seen, leaf.CommandPath())
		if _, ok := f.Value.(*deadWindow); !ok {
			t.Errorf("%s --dead is a %T, want the shared *deadWindow", leaf.CommandPath(), f.Value)
		}
		if f.Value.Type() != "duration" {
			t.Errorf("%s --dead has type %q, want duration", leaf.CommandPath(), f.Value.Type())
		}
	}
	if strings.Join(seen, ",") != "hactl device ls,hactl ent ls" {
		t.Errorf("--dead is offered by %v; update D-238 and this test together", seen)
	}
	for _, args := range [][]string{{"ent", "ls", "--dead", "31d"}, {"device", "ls", "--dead", "31d"}} {
		err := RunWithOutput(append([]string{"hactl", "--dir", t.TempDir()}, args...), &strings.Builder{})
		if err == nil || !strings.Contains(err.Error(), "longer than 30d") {
			t.Errorf("hactl %s: err = %v, want the cap refusal before anything else", strings.Join(args, " "), err)
		}
	}
}

// TestDeviceLsDeadJudgesByEntities drives `device ls --dead` end to end
// against a fake HA: each device's verdict comes from its entities', and the
// recorder is asked only about the one entity nothing else decides.
func TestDeviceLsDeadJudgesByEntities(t *testing.T) {
	recent := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)
	var asked []string
	ts := startCmdServer(t, map[string]any{
		"config/device_registry/list": []map[string]any{
			{"id": "dev_dead", "name": "Dead"},
			{"id": "dev_alive", "name": "Alive"},
			{"id": "dev_mixed", "name": "Mixed"},
			{"id": "dev_off", "name": "Off"},
		},
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "sensor.a", "device_id": "dev_dead"},
			{"entity_id": "sensor.b", "device_id": "dev_alive"},
			{"entity_id": "sensor.a2", "device_id": "dev_alive"},
			{"entity_id": "sensor.c", "device_id": "dev_mixed"},
			{"entity_id": "sensor.d", "device_id": "dev_mixed"},
			{"entity_id": "sensor.e", "device_id": "dev_off", "disabled_by": "user"},
		},
		"config/area_registry/list":  []map[string]any{},
		"config/label_registry/list": []map[string]any{},
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprintf(w, `[
				{"entity_id":"sensor.a","state":"unavailable","last_changed":"2020-01-01T00:00:00+00:00","attributes":{}},
				{"entity_id":"sensor.a2","state":"unavailable","last_changed":"2020-01-01T00:00:00+00:00","attributes":{}},
				{"entity_id":"sensor.b","state":"21","last_changed":"2020-01-01T00:00:00+00:00","attributes":{}},
				{"entity_id":"sensor.c","state":"unknown","last_changed":"2020-01-01T00:00:00+00:00","attributes":{}},
				{"entity_id":"sensor.d","state":"unavailable","last_changed":%q,"attributes":{}}
			]`, recent)
		},
		"/api/history/period/": func(w http.ResponseWriter, r *http.Request) {
			asked = append(asked, r.URL.Query().Get("filter_entity_id"))
			_, _ = fmt.Fprint(w, `[]`)
		},
	})
	withFlagDir(t, ts.dir)
	oldDead, oldJSON := flagDeviceDead, flagJSON
	defer func() { flagDeviceDead, flagJSON = oldDead, oldJSON }()
	if err := flagDeviceDead.Set("7d"); err != nil {
		t.Fatal(err)
	}
	flagJSON = true

	var buf bytes.Buffer
	if err := runDeviceLs(listingCmd(context.Background(), "device", "ls"), &buf); err != nil {
		t.Fatalf("device ls --dead: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("%v\n%s", err, buf.String())
	}
	got := map[string][2]any{}
	for _, r := range rows {
		id, _ := r["device_id"].(string)
		got[id] = [2]any{r["dead"], r["dead_reason"]}
	}
	want := map[string][2]any{
		"dev_dead":  {true, deadAllEntities},
		"dev_mixed": {nil, deadPartlyDecided},
		"dev_off":   {nil, deadNoStateful},
	}
	if len(got) != len(want) {
		t.Errorf("listed %v, want %v (dev_alive has a valid entity)", got, want)
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("%s = %v, want %v", id, got[id], w)
		}
	}
	if strings.Join(asked, "|") != "sensor.d" {
		t.Errorf("asked the recorder about %q, want only sensor.d", asked)
	}
}
