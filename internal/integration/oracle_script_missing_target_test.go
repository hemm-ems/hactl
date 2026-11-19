//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// A script whose target does not exist — the oracle behind B-16 / D-131.
//
// The live test of 2026-11-01 (F-14) ran `script run pg_core_script --confirm`
// on a real instance: the script toggles input_boolean.pg_core_flag_b, which
// does not exist, and hactl answered "executed". The question is not whether
// hactl should know better — it is whether Home Assistant says so anywhere
// hactl can read: in the response to the call, in the run's trace, or in the
// log. These tests ask all three and record each answer, so the contract at
// `script run` rests on what HA reported rather than on what sounded likely.
//
// Observed (HA 2026.10.x, the `stable` image): the call answers 200 with the
// script's own on/off states and nothing else; the trace records the step's
// target and a finished, error-free run; only the system log says it, as a
// WARNING from homeassistant.helpers.service. That one depends on the logger
// letting warnings through — the oracle fixture's `logger: default: warning`
// does; an instance configured above warning hears nothing at all.
// ============================================================================

const (
	missingTargetScript = "script.oracle_script_missing_target"
	missingTargetEntity = "input_boolean.oracle_no_such_flag"
)

func TestOracleScriptRunMissingTarget(t *testing.T) {
	inst, _ := getOracleHA(t)
	ctx := context.Background()
	client := haapi.New(inst.URL(), inst.Token())

	if _, err := client.GetState(ctx, missingTargetScript); err != nil {
		t.Fatalf("precondition: %s is not loaded: %v", missingTargetScript, err)
	}
	if _, err := client.GetState(ctx, missingTargetEntity); err == nil {
		t.Fatalf("precondition: %s exists, so the script's target is not missing and the rig proves nothing",
			missingTargetEntity)
	}
	if raw, err := client.GetConfig(ctx); err == nil {
		var cfg struct {
			Version string `json:"version"`
		}
		if json.Unmarshal(raw, &cfg) == nil {
			t.Logf("Home Assistant %s", cfg.Version)
		}
	}

	// The call hactl's `script run --confirm` makes, through the same client.
	changed, err := client.CallService(ctx, "script", "turn_on", map[string]any{"entity_id": missingTargetScript})
	if err != nil {
		t.Fatalf("script.turn_on %s: HA refused the call (%v) — the run response DOES report the missing "+
			"target now, and D-131's contract needs a fresh look", missingTargetScript, err)
	}
	run := waitForScriptTrace(t, inst, "oracle_script_missing_target")

	t.Run("run_response_is_silent", func(t *testing.T) { assertRunResponseSilent(t, changed) })
	t.Run("trace_calls_the_run_a_success", func(t *testing.T) { assertTraceCallsRunASuccess(t, inst, run) })
	t.Run("system_log_says_it", func(t *testing.T) { assertSystemLogSaysIt(t, inst, run) })
}

// assertRunResponseSilent: the response to script.turn_on lists the states the
// call changed. For this script that is its own on/off pair and nothing else —
// with an existing target the toggled entity is listed as well, so a missing
// one is visible only as an absence the response never names.
func assertRunResponseSilent(t *testing.T, changed []haapi.ServiceStateChange) {
	t.Helper()
	for _, c := range changed {
		if c.EntityID != missingTargetScript {
			t.Errorf("the run response names %s (state %q): HA now reports more than the script's own "+
				"state, and D-131's contract needs a fresh look", c.EntityID, c.State)
		}
	}
	if len(changed) == 0 {
		t.Errorf("the run response carries no state at all; observed was the script's own on/off pair")
	}
}

// assertTraceCallsRunASuccess: the trace records what the step aimed at and
// calls the run finished, with no error on the run or on the step.
func assertTraceCallsRunASuccess(t *testing.T, inst *hatest.Instance, run haapi.TraceSummary) {
	t.Helper()
	if run.Error != "" || run.Execution != "finished" {
		t.Errorf("trace summary: error=%q script_execution=%q — HA now marks the run, and the trace is a "+
			"better source than the log for D-131", run.Error, run.Execution)
	}
	raw, err := oracleWS(t, inst).TraceGet(context.Background(), "script", "oracle_script_missing_target", run.RunID)
	if err != nil {
		t.Fatalf("trace/get: %v", err)
	}
	var detail struct {
		Trace map[string][]struct {
			Error  string `json:"error"`
			Result struct {
				Params struct {
					Target struct {
						EntityID []string `json:"entity_id"`
					} `json:"target"`
				} `json:"params"`
			} `json:"result"`
		} `json:"trace"`
	}
	if err := json.Unmarshal(raw, &detail); err != nil {
		t.Fatalf("parsing trace/get: %v", err)
	}
	steps := detail.Trace["sequence/0"]
	if len(steps) != 1 {
		t.Fatalf("trace step sequence/0: %d records, want 1 (%s)", len(steps), raw)
	}
	if steps[0].Error != "" {
		t.Errorf("the step now carries an error (%q): the trace reports the missing target", steps[0].Error)
	}
	if !slices.Contains(steps[0].Result.Params.Target.EntityID, missingTargetEntity) {
		t.Errorf("the step's target %v does not name %s: the trace's record of what the step aimed at moved",
			steps[0].Result.Params.Target.EntityID, missingTargetEntity)
	}
}

// assertSystemLogSaysIt: the one source that does — a WARNING from
// homeassistant.helpers.service that names the entity but not the script.
func assertSystemLogSaysIt(t *testing.T, inst *hatest.Instance, run haapi.TraceSummary) {
	t.Helper()
	start, err := time.Parse(time.RFC3339Nano, run.Timestamp.Start)
	if err != nil {
		t.Fatalf("parsing the run's start %q: %v", run.Timestamp.Start, err)
	}
	entries, err := oracleWS(t, inst).SystemLogList(context.Background())
	if err != nil {
		t.Fatalf("system_log/list: %v", err)
	}
	want := "Referenced entities " + missingTargetEntity + " are missing or not currently available"
	i := slices.IndexFunc(entries, func(e haapi.SystemLogEntry) bool { return slices.Contains(e.Message, want) })
	if i < 0 {
		seen := make([]string, 0, len(entries))
		for _, e := range entries {
			seen = append(seen, e.Name+": "+strings.Join(e.Message, " / "))
		}
		t.Fatalf("no system_log entry %q after the run — HA no longer says it anywhere hactl can read, and "+
			"D-131's warning has nothing to report. Entries:\n%s", want, strings.Join(seen, "\n"))
	}
	e := entries[i]
	if e.Name != "homeassistant.helpers.service" || e.Level != "WARNING" {
		t.Errorf("the warning moved: logger %q level %q, observed homeassistant.helpers.service WARNING",
			e.Name, e.Level)
	}
	// system_log folds repeats into one entry and moves `timestamp` to the
	// latest; on HA's own clock it is never before this run began.
	if logged := time.Unix(0, int64(e.Timestamp*float64(time.Second))); logged.Before(start) {
		t.Errorf("the warning was last logged at %s, before the run began at %s", logged, start)
	}
}

// waitForScriptTrace returns the newest trace HA holds for one script, polling
// briefly: the trace is stored when the run ends, which script.turn_on does
// not wait for.
func waitForScriptTrace(t *testing.T, inst *hatest.Instance, itemID string) haapi.TraceSummary {
	t.Helper()
	ws := oracleWS(t, inst)
	deadline := time.Now().Add(10 * time.Second)
	for {
		res, err := ws.TraceList(context.Background(), "script")
		if err != nil {
			t.Fatalf("trace/list script: %v", err)
		}
		if runs := res["script."+itemID]; len(runs) > 0 && runs[0].Timestamp.Finish != "" {
			newest := runs[0]
			for _, r := range runs[1:] {
				if r.Timestamp.Start > newest.Timestamp.Start {
					newest = r
				}
			}
			return newest
		}
		if time.Now().After(deadline) {
			t.Fatalf("no trace for script.%s within 10s of running it", itemID)
		}
		time.Sleep(250 * time.Millisecond)
	}
}
