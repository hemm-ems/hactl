//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// deadFlip calls input_boolean.<svc> on one fixture switch and gives the
// recorder (commit_interval 1) time to write it.
func deadFlip(t *testing.T, client *haapi.Client, svc, id string) {
	t.Helper()
	if _, err := client.CallService(context.Background(), "input_boolean", svc, map[string]any{"entity_id": id}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
}

// TestOracleDeadHistoryShape asks HA the three questions D-238 builds on,
// through the same client call --dead uses:
//
//  1. An entity the recorder has no rows for (excluded, or nonexistent) is
//     absent from the answer, not an empty list: absence is the only signal.
//  2. The start-time state of a window is stamped with the window start
//     itself, so "first row later than the start" means the recorder does
//     not reach back that far.
//  3. A filter list of 16 KB is accepted (the batch budget is 4 KB).
func TestOracleDeadHistoryShape(t *testing.T) {
	inst := hatest.Start(t, hatest.WithFixture("dead"))
	waitForRunning(t, inst)
	client := haapi.New(inst.URL(), inst.Token())
	ctx := context.Background()

	time.Sleep(2 * time.Second)
	windowStart := time.Now().UTC().Truncate(time.Millisecond)
	time.Sleep(time.Second)
	deadFlip(t, client, "turn_off", "input_boolean.dead_up_a")
	deadFlip(t, client, "turn_on", "input_boolean.dead_up_unrecorded")
	end := time.Now().UTC()

	ids := []string{"sensor.dead_a", "sensor.dead_b", "sensor.dead_unrecorded", "sensor.does_not_exist"}
	got, err := client.GetHistoryMinimal(ctx, windowStart.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), ids)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"sensor.dead_unrecorded", "sensor.does_not_exist"} {
		if rows, ok := got[id]; ok {
			t.Errorf("%s: HA answered %v; D-238 reads absence as `unrecorded` and relies on it", id, rows)
		}
	}
	for _, id := range []string{"sensor.dead_a", "sensor.dead_b"} {
		rows := got[id]
		if len(rows) == 0 {
			t.Fatalf("%s: no rows for a recorded entity", id)
		}
		for i, r := range rows {
			if r.LastChanged == "" {
				t.Errorf("%s row %d has no last_changed: MinimalHistoryRow's identity rests on it", id, i)
			}
		}
		first, err := time.Parse(time.RFC3339Nano, rows[0].LastChanged)
		if err != nil {
			t.Fatalf("%s first row last_changed %q: %v", id, rows[0].LastChanged, err)
		}
		if d := first.Sub(windowStart); math.Abs(float64(d)) > float64(time.Millisecond) {
			t.Errorf("%s: start-time row stamped %s, window start %s; D-238's partly_recorded test assumes they are equal",
				id, rows[0].LastChanged, windowStart.Format(time.RFC3339Nano))
		}
	}
	if a := got["sensor.dead_a"]; len(a) < 2 || a[0].State != "1" || a[len(a)-1].State != "unavailable" {
		t.Errorf("sensor.dead_a rows = %+v, want 1 then unavailable", a)
	}

	pad := []string{"sensor.dead_a"}
	for i := 0; len(strings.Join(pad, ",")) < 16000; i++ {
		pad = append(pad, fmt.Sprintf("sensor.pad_%05d", i))
	}
	if _, err := client.GetHistoryMinimal(ctx, windowStart.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano), pad); err != nil {
		t.Errorf("a 16 KB filter list was refused: %v", err)
	}
}

// TestEntLsDeadAgainstTheRecorder runs `ent ls --dead` on the dead fixture
// and checks each verdict against what the test made the entity do (D-238).
func TestEntLsDeadAgainstTheRecorder(t *testing.T) {
	inst := hatest.Start(t, hatest.WithFixture("dead"))
	waitForRunning(t, inst)
	client := haapi.New(inst.URL(), inst.Token())

	time.Sleep(3 * time.Second)
	windowStart := time.Now()
	deadFlip(t, client, "turn_off", "input_boolean.dead_up_a")          // was valid in the window
	deadFlip(t, client, "turn_on", "input_boolean.dead_up_c")           // unavailable → unknown
	deadFlip(t, client, "turn_off", "input_boolean.dead_up_c")          // → unavailable
	deadFlip(t, client, "turn_on", "input_boolean.dead_up_unrecorded")  // changes, not recorded
	deadFlip(t, client, "turn_off", "input_boolean.dead_up_unrecorded") //
	window := time.Since(windowStart).Round(time.Second) + time.Second

	verdicts := func(dur string) map[string][2]any {
		var rows []map[string]any
		out := runHactlDir(t, inst.Dir(), "ent", "ls", "--pattern", "*dead*", "--dead", dur, "--json")
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("ent ls --dead %s --json: %v\n%s", dur, err, out)
		}
		m := map[string][2]any{}
		for _, r := range rows {
			id, _ := r["entity_id"].(string)
			m[id] = [2]any{r["dead"], r["dead_reason"]}
		}
		return m
	}

	got := verdicts(fmt.Sprintf("%ds", int(window.Seconds())))
	want := map[string][2]any{
		"sensor.dead_b":          {true, "unchanged"},      // unavailable since boot
		"sensor.dead_c":          {true, "no_valid_state"}, // only the recorder can tell
		"sensor.dead_unrecorded": {nil, "unrecorded"},      // excluded: never dead
	}
	for id, w := range want {
		if got[id] != w {
			t.Errorf("--dead %s: %s = %v, want %v", window, id, got[id], w)
		}
	}
	for _, alive := range []string{"sensor.dead_a", "input_button.dead_never_pressed", "input_boolean.dead_up_a"} {
		if v, listed := got[alive]; listed {
			t.Errorf("--dead %s listed %s as %v; it had a valid state in the window", window, alive, v)
		}
	}

	// A window reaching back before HA booted: the recorder does not cover
	// its start, so nothing it was asked about may be called dead.
	long := verdicts("1h")
	for _, id := range []string{"sensor.dead_b", "sensor.dead_c"} {
		if long[id] != [2]any{nil, "partly_recorded"} {
			t.Errorf("--dead 1h: %s = %v, want undecided partly_recorded", id, long[id])
		}
	}
}
