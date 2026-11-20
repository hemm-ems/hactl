//go:build integration

package integration

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// How the system log groups "Referenced … are missing" — the oracle behind
// D-132, the half of B-16 that D-131 assumed.
//
// D-131 has `script run --confirm` pass through every such warning "whose last
// occurrence is not before the run began", which presumes each line carries
// its own time. HA's system_log keys an entry by (logger, source line, root
// cause) and folds every later record from the same call site into it: the
// entry's `timestamp` moves to the newest record, `count` grows, and a message
// it has not held yet is appended to `message` (at most five). Every missing
// reference is logged from one call site, so two different missing entities
// end up as two messages of ONE entry, with one time between them — and a
// repeat of a message the entry already holds moves nothing in `message`. A
// reader of system_log/list can therefore tell that SOME such line was logged
// after a moment, and which line only when the entry holds one.
//
// Observed on HA 2026.10.x (`stable`): exactly that. Also recorded: the call's
// answer stamps the script's own states (`last_updated`) before the warning's
// time, on the same clock — the run start `script run` measures against.
// ============================================================================

func TestOracleSystemLogGroupsMissingTargetWarnings(t *testing.T) {
	opts := []hatest.Option{hatest.WithFixture("syslogfold")}
	if img := os.Getenv("HACTL_HA_IMAGE"); img != "" {
		opts = append(opts, hatest.WithImage(img))
	}
	inst := hatest.Start(t, opts...)
	waitForRunning(t, inst)
	client := haapi.New(inst.URL(), inst.Token())
	ws := oracleWS(t, inst)

	const (
		lineA = "Referenced entities input_boolean.fold_no_such_a are missing or not currently available"
		lineB = "Referenced entities input_boolean.fold_no_such_b are missing or not currently available"
	)

	startA := runScriptForStart(t, client, "script.fold_missing_a")
	first := missingRefEntry(t, ws, 1)
	if !slices.Equal(first.Message, []string{lineA}) {
		t.Fatalf("after one missing reference the entry holds %q, want just %q", first.Message, lineA)
	}
	if logStamp(first).Before(startA) {
		t.Errorf("the warning is stamped %s, before the run's own state at %s: the answer's last_updated is "+
			"not a lower bound for the run's log lines", logStamp(first), startA)
	}

	startB := runScriptForStart(t, client, "script.fold_missing_b")
	second := missingRefEntry(t, ws, 2)
	if !slices.Equal(second.Message, []string{lineA, lineB}) {
		t.Errorf("two different missing references: messages %q, want both in one entry, in order of first "+
			"appearance", second.Message)
	}
	if logStamp(second).Before(startB) || second.FirstOccurred != first.FirstOccurred {
		t.Errorf("the entry's time did not move to the second line (timestamp %s, run B began %s; "+
			"first_occurred %v then %v)", logStamp(second), startB, first.FirstOccurred, second.FirstOccurred)
	}

	// The repeat: line A again. The entry's time moves to it, and nothing in
	// `message` says that A — not B — is the line just logged.
	startA2 := runScriptForStart(t, client, "script.fold_missing_a")
	third := missingRefEntry(t, ws, 3)
	if logStamp(third).Before(startA2) {
		t.Errorf("the repeat did not move the entry's time: %s, run began %s", logStamp(third), startA2)
	}
	if !slices.Equal(third.Message, []string{lineA, lineB}) {
		t.Errorf("after repeating A the messages are %q: HA now records which line came last, and "+
			"`script run` could name it (revisit D-132)", third.Message)
	}
}

// runScriptForStart calls the script the way `script run --confirm` does and
// returns the run's start as the call's answer stamps it: the earliest
// last_updated of the script's own states.
func runScriptForStart(t *testing.T, client *haapi.Client, script string) time.Time {
	t.Helper()
	changed, err := client.CallService(context.Background(), "script", "turn_on", map[string]any{"entity_id": script})
	if err != nil {
		t.Fatalf("script.turn_on %s: %v", script, err)
	}
	var start time.Time
	for _, c := range changed {
		if c.EntityID != script {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, c.LastUpdated)
		if err != nil {
			t.Fatalf("%s's state in the call's answer: last_updated %q: %v", script, c.LastUpdated, err)
		}
		if start.IsZero() || at.Before(start) {
			start = at
		}
	}
	if start.IsZero() {
		t.Fatalf("the call's answer carries no state of %s (%+v): there is no run start on HA's clock", script, changed)
	}
	return start
}

// missingRefEntry waits until the helpers.service WARNING records add up to n
// and returns the one entry holding them, failing if there is more than one.
func missingRefEntry(t *testing.T, ws *haapi.WSClient, n int) haapi.SystemLogEntry {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		entries, err := ws.SystemLogList(context.Background())
		if err != nil {
			t.Fatalf("system_log/list: %v", err)
		}
		held := slices.DeleteFunc(entries, func(e haapi.SystemLogEntry) bool {
			return e.Name != "homeassistant.helpers.service" || e.Level != "WARNING"
		})
		total := 0
		for _, e := range held {
			total += e.Count
		}
		switch {
		case total >= n && len(held) != 1:
			t.Fatalf("%d helpers.service WARNING entries after %d missing references, want one — HA no "+
				"longer folds them, and D-132's 'which line' limit may be gone: %+v", len(held), n, held)
		case total >= n:
			return held[0]
		case time.Now().After(deadline):
			t.Fatalf("helpers.service WARNING count %d after 10s, want %d", total, n)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// logStamp is a system_log entry's time: its newest record, on HA's clock.
func logStamp(e haapi.SystemLogEntry) time.Time {
	return time.Unix(0, int64(e.Timestamp*float64(time.Second)))
}
