package cmd

// Blind spec tests for B-16 (live test 2026-11-01, F-14), written from the
// contract only — docs/decisions.md D-131 and the manual's "What `executed`
// means" under Scripts — without reading the `script run` implementation.
//
// The oracle (TestOracleScriptRunMissingTarget) found that Home Assistant says a
// script step aimed at a missing entity in one place only: a WARNING from
// logger homeassistant.helpers.service in the system log, "Referenced entities
// <id> are missing or not currently available", which does not name the
// script. `script run --confirm` reads the system log once after the call and
// passes every such line logged during the run through verbatim, as
// `warning: Home Assistant logged during this run: <HA's sentence>` and in the
// --json `warnings`; exit 0. "During the run" is measured on HA's clock (the
// run response's or the trace's own timestamps), never the local one. A log it
// cannot read is said, never taken as "nothing missing".
//
// The rig's clock is a fixed morning in the past, so an implementation that
// compared against the local clock would drop the line and go red here.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

const (
	missingTargetLine   = "Referenced entities input_boolean.pg_core_flag_b are missing or not currently available"
	staleMissingLine    = "Referenced entities input_boolean.pg_stale_flag are missing or not currently available"
	unrelatedWarnLine   = "Something unrelated went sideways"
	missingTargetPrefix = "warning: Home Assistant logged during this run: "
)

// runStart is the moment the rig's script run begins, on HA's clock.
var runStart = time.Date(2026, 11, 1, 10, 0, 0, 50_000_000, time.UTC)

func haTime(d time.Duration) string {
	return runStart.Add(d).Format("2006-01-02T15:04:05.000000+00:00")
}

func haEpoch(d time.Duration) float64 {
	return float64(runStart.Add(d).UnixNano()) / float64(time.Second)
}

// scriptRunRig is a Home Assistant holding script.pg_core_script. The run
// response carries the script's own on/off pair (what the oracle observed),
// the trace list holds that run, and — when log is non-nil — system_log/list
// answers with log; otherwise the system log cannot be read at all.
func scriptRunRig(t *testing.T, log []map[string]any) *cmdTestServer {
	t.Helper()
	ws := map[string]any{
		"trace/list": []map[string]any{{
			"run_id": "r1", "domain": "script", "item_id": "pg_core_script",
			"timestamp":        map[string]any{"start": haTime(0), "finish": haTime(40 * time.Millisecond)},
			"state":            "stopped",
			"script_execution": "finished",
			"last_step":        "sequence/0",
		}},
	}
	if log != nil {
		ws["system_log/list"] = log
	}
	state := map[string]any{"entity_id": "script.pg_core_script", "state": "off", "attributes": map[string]any{"friendly_name": "PG Core Script"},
		"last_changed": haTime(-time.Hour), "last_updated": haTime(-time.Hour)}
	return startCmdServer(t, ws, map[string]http.HandlerFunc{
		"/api/states/script.pg_core_script": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(state)
		},
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]any{state})
		},
		"/api/services/script/turn_on": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `[{"entity_id":"script.pg_core_script","state":"on","attributes":{},"last_changed":%q,"last_updated":%q},`+
				`{"entity_id":"script.pg_core_script","state":"off","attributes":{},"last_changed":%q,"last_updated":%q}]`,
				haTime(time.Millisecond), haTime(time.Millisecond), haTime(40*time.Millisecond), haTime(40*time.Millisecond))
		},
		// The text log is not the system log; an implementation that falls back
		// to it finds nothing readable here either.
		"/api/error_log": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "unavailable", http.StatusInternalServerError)
		},
	})
}

// missingTargetLog holds, in HA's system_log/list shape: the warning this run
// caused (logged 10 ms into the run), the same kind of warning last logged an
// hour before the run (a different entity), and an unrelated warning from
// another logger during the run.
func missingTargetLog() []map[string]any {
	return []map[string]any{
		{"name": "homeassistant.helpers.service", "level": "WARNING", "message": []string{missingTargetLine},
			"source": []any{"helpers/service.py", 300}, "timestamp": haEpoch(10 * time.Millisecond),
			"first_occurred": haEpoch(10 * time.Millisecond), "count": 1, "exception": ""},
		{"name": "homeassistant.helpers.service", "level": "WARNING", "message": []string{staleMissingLine},
			"source": []any{"helpers/service.py", 300}, "timestamp": haEpoch(-time.Hour),
			"first_occurred": haEpoch(-2 * time.Hour), "count": 3, "exception": ""},
		{"name": "homeassistant.components.foo", "level": "WARNING", "message": []string{unrelatedWarnLine},
			"source": []any{"components/foo/__init__.py", 12}, "timestamp": haEpoch(20 * time.Millisecond),
			"first_occurred": haEpoch(20 * time.Millisecond), "count": 1, "exception": ""},
	}
}

// TestScriptRunPassesHAsMissingTargetWarningThrough — D-131: the line this run
// caused is passed through verbatim, in text and in --json warnings; the line
// last logged before the run began and the unrelated logger's line are not;
// exit 0 — HA ran the script.
func TestScriptRunPassesHAsMissingTargetWarningThrough(t *testing.T) {
	ts := scriptRunRig(t, missingTargetLog())

	out, err := runCLI(t, "script", "run", "pg_core_script", "--confirm", "--dir", ts.dir)
	if err != nil {
		t.Fatalf("script run --confirm: %v (exit 0 expected — HA ran the script)\n%s", err, out)
	}
	if !strings.Contains(out, "executed") {
		t.Errorf("the run no longer says executed:\n%s", out)
	}
	if want := missingTargetPrefix + missingTargetLine; !strings.Contains(out, want) {
		t.Errorf("the run does not pass HA's warning through as %q:\n%s", want, out)
	}
	if strings.Contains(out, "pg_stale_flag") {
		t.Errorf("a warning last logged an hour before the run began was attributed to it:\n%s", out)
	}
	if strings.Contains(out, unrelatedWarnLine) {
		t.Errorf("a warning from another logger was passed through:\n%s", out)
	}
	if ts.commandCount("system_log/list") != 1 {
		t.Errorf("system_log/list asked %d times, want once", ts.commandCount("system_log/list"))
	}

	raw, err := runCLI(t, "script", "run", "pg_core_script", "--confirm", "--json", "--dir", ts.dir)
	if err != nil {
		t.Fatalf("script run --confirm --json: %v\n%s", err, raw)
	}
	var doc struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &doc); err != nil {
		t.Fatalf("--json is not one JSON object: %v\n%s", err, raw)
	}
	found := false
	for _, w := range doc.Warnings {
		if strings.Contains(w, missingTargetLine) {
			found = true
		}
		if strings.Contains(w, "pg_stale_flag") || strings.Contains(w, unrelatedWarnLine) {
			t.Errorf("--json warnings carry a line that is not this run's: %q", w)
		}
	}
	if !found {
		t.Errorf("--json warnings %q do not carry HA's sentence %q", doc.Warnings, missingTargetLine)
	}
}

// TestScriptRunWithoutAMissingTargetWarnsNothing — the control: a log with no
// line from this run adds no warning, so the warning means something.
func TestScriptRunWithoutAMissingTargetWarnsNothing(t *testing.T) {
	log := missingTargetLog()[1:] // the stale line and the unrelated one only
	ts := scriptRunRig(t, log)
	out, err := runCLI(t, "script", "run", "pg_core_script", "--confirm", "--dir", ts.dir)
	if err != nil {
		t.Fatalf("script run --confirm: %v\n%s", err, out)
	}
	if strings.Contains(out, "warning:") {
		t.Errorf("a run that caused no missing-target line warned:\n%s", out)
	}
	if ts.commandCount("system_log/list") != 1 {
		t.Errorf("system_log/list asked %d times, want once — the check runs on every confirmed run", ts.commandCount("system_log/list"))
	}
}

// TestScriptRunSaysWhenItCouldNotReadTheLog — D-131 / H-7: an unreadable log
// is reported, never taken as "nothing missing"; the run still exits 0.
func TestScriptRunSaysWhenItCouldNotReadTheLog(t *testing.T) {
	ts := scriptRunRig(t, nil)
	out, err := runCLI(t, "script", "run", "pg_core_script", "--confirm", "--dir", ts.dir)
	if err != nil {
		t.Fatalf("script run --confirm: %v (the script ran; exit 0)\n%s", err, out)
	}
	if want := "warning: could not read Home Assistant's log to check for missing targets: "; !strings.Contains(out, want) {
		t.Errorf("an unreadable log is not reported as %q:\n%s", want, out)
	}
}

// TestScriptRunDryRunReadsNoLog — "The dry run runs nothing and cannot know."
func TestScriptRunDryRunReadsNoLog(t *testing.T) {
	ts := scriptRunRig(t, missingTargetLog())
	out, err := runCLI(t, "script", "run", "pg_core_script", "--dir", ts.dir)
	if err != nil {
		t.Fatalf("script run (dry run): %v\n%s", err, out)
	}
	if strings.Contains(out, missingTargetLine) || ts.commandCount("system_log/list") != 0 {
		t.Errorf("the dry run read the system log (%d asks):\n%s", ts.commandCount("system_log/list"), out)
	}
}
