package cmd

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// The gaps the B-16 spec tests leave: the log as HA really holds it (D-132:
// one entry for every missing-reference line), and a call answer with no
// clock reading to time the run by. The rig and log shapes are the spec
// test's (script_run_missing_target_prelaunch_spec_test.go).

// TestScriptRunLogWarningsOnAFoldedEntryNameEveryLineAndClaimNone — the shape
// TestOracleSystemLogGroupsMissingTargetWarnings observed: this run's line and
// an hour-old one share an entry stamped during the run. Either could be the
// one just logged, so the warning names both and attributes neither.
func TestScriptRunLogWarningsOnAFoldedEntryNameEveryLineAndClaimNone(t *testing.T) {
	folded := []map[string]any{{
		"name": "homeassistant.helpers.service", "level": "WARNING",
		"message":   []string{staleMissingLine, missingTargetLine},
		"source":    []any{"helpers/target.py", 155},
		"timestamp": haEpoch(10 * time.Millisecond), "first_occurred": haEpoch(-2 * time.Hour), "count": 4, "exception": "",
	}}
	ts := scriptRunRig(t, folded)
	out, err := runCLI(t, "script", "run", "pg_core_script", "--confirm", "--dir", ts.dir)
	if err != nil {
		t.Fatalf("script run --confirm: %v\n%s", err, out)
	}
	want := "warning: Home Assistant logged during this run one of these lines, and its log does not say which: " +
		staleMissingLine + "; " + missingTargetLine
	if !strings.Contains(out, want) {
		t.Errorf("a folded entry is not reported as %q:\n%s", want, out)
	}
	if strings.Contains(out, missingTargetPrefix) {
		t.Errorf("a line of a folded entry was attributed to the run as if the log said so:\n%s", out)
	}
}

// TestScriptRunLogWarningsNeedAClockReading — an answer carrying no state of
// the script has no run start on HA's clock; the run says so and reads no log
// rather than measuring from the local clock.
func TestScriptRunLogWarningsNeedAClockReading(t *testing.T) {
	ts := startCmdServer(t, map[string]any{"system_log/list": missingTargetLog()}, map[string]http.HandlerFunc{
		"/api/states/script.pg_core_script": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"entity_id": "script.pg_core_script", "state": "off"})
		},
		"/api/services/script/turn_on": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		},
	})
	out, err := runCLI(t, "script", "run", "pg_core_script", "--confirm", "--dir", ts.dir)
	if err != nil {
		t.Fatalf("script run --confirm: %v\n%s", err, out)
	}
	want := "warning: could not check Home Assistant's log for missing targets: " +
		"its answer to the call carries no state of script.pg_core_script to time the run by"
	if !strings.Contains(out, want) {
		t.Errorf("a run with no start on HA's clock does not say %q:\n%s", want, out)
	}
	if n := ts.commandCount("system_log/list"); n != 0 {
		t.Errorf("system_log/list asked %d times with nothing to measure its lines against", n)
	}
}

// TestScriptRunStartIsTheEarliestOwnState — the start is the script's own
// earliest last_updated; another entity's earlier state and an unparseable
// stamp do not move it.
func TestScriptRunStartIsTheEarliestOwnState(t *testing.T) {
	changed := []haapi.ServiceStateChange{
		{EntityID: "script.x", State: "off", LastUpdated: haTime(40 * time.Millisecond)},
		{EntityID: "light.other", State: "on", LastUpdated: haTime(-time.Hour)},
		{EntityID: "script.x", State: "on", LastUpdated: haTime(time.Millisecond)},
		{EntityID: "script.x", State: "on", LastUpdated: "not a time"},
	}
	start, ok := scriptRunStart(changed, "script.x")
	if !ok || !start.Equal(runStart.Add(time.Millisecond)) {
		t.Errorf("start = %s (%v), want %s", start, ok, runStart.Add(time.Millisecond))
	}
	if _, ok := scriptRunStart(changed[1:2], "script.x"); ok {
		t.Error("a start was found in an answer holding no state of the script")
	}
}

// TestMissingTargetWarningsKeyOnLoggerLevelAndFrame — the matcher's edges: an
// entry stamped exactly at the start counts (not before it), another level of
// the same logger does not, and a line of the same logger outside the frame
// is not passed through.
func TestMissingTargetWarningsKeyOnLoggerLevelAndFrame(t *testing.T) {
	entry := func(level, msg string, at time.Duration) haapi.SystemLogEntry {
		return haapi.SystemLogEntry{Name: missingTargetLogger, Level: level, Message: []string{msg}, Timestamp: haEpoch(at), Count: 1}
	}
	got := missingTargetWarnings([]haapi.SystemLogEntry{
		entry("WARNING", missingTargetLine, 0),
		entry("ERROR", staleMissingLine, time.Millisecond),
		entry("WARNING", "Unable to find referenced service", time.Millisecond),
	}, runStart)
	want := []string{"Home Assistant logged during this run: " + missingTargetLine}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("warnings = %q, want %q", got, want)
	}
}
