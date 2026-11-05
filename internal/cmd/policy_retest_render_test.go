package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
)

// The live re-test's rendering findings (2026-11-01, D-128): what `policy
// show` and `policy check` print must not hide an expansion or contradict
// itself.

// TestPolicyShowTextShowsWhatWriteExpandsTo (D-128 (1)): a rule whose verbs
// hold `write` shows what it expands to at its address, deny and allow alike,
// so the same `[write]` on a registry field and on a whole entity read
// differently; a list without `write` shows nothing more.
func TestPolicyShowTextShowsWhatWriteExpandsTo(t *testing.T) {
	body := `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*/registry/labels": [write]
      "entity:light.*": [control]
    deny:
      "entity:*/registry/entity_id": [write]
      "entity:cover.*": [write]
      "system:reload/*": [read, write]
`
	dir := policyInstance(t, closedHAURL(t), body)
	out, err := runPolicyCLI(t, dir, "policy", "show")
	if cliExitCode(err) != 0 {
		t.Fatalf("policy show: exit %d (%v)\n%s", cliExitCode(err), err, out)
	}
	want := "file: " + filepath.Join(dir, policy.FileName) + "\n" +
		policy.Disclaimer + "\n" +
		"level: A\n" +
		"enforced: writes and reads\n" +
		"profile: agent (selected by active)\n" +
		"default: deny\n" +
		"rules:\n" +
		"  line 10   deny \"entity:*/registry/entity_id\" [write] → [edit create delete]\n" +
		"  line 11   deny \"entity:cover.*\" [write] → [control edit create delete]\n" +
		"  line 12   deny \"system:reload/*\" [read write] → [read]\n" +
		"  line 7    allow \"entity:*/registry/labels\" [write] → [edit]\n" +
		"  line 8    allow \"entity:light.*\" [control]\n" +
		"profiles: agent\n"
	if out != want {
		t.Errorf("policy show\n got: %q\nwant: %q", out, want)
	}
}

// checkRetestBody is the profile the write-check answers below are decided
// under: a typed control the default denies, and a script allow.
const checkRetestBody = `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "automation:pg_*": [edit, create, delete]
      "script:pg_s": [control, edit, create, delete]
      "file:automations.yaml": [edit]
`

// TestPolicyCheckWriteIgnoresADenialNoCommandEnforces (D-128 (2), N-3): the
// deny-only control the default decided on an automation is shown denied with
// its `not enforced` line, but no command enforces it, so it does not make
// the write check exit 1 — the top line and the JSON `allowed` follow the
// exit code, and the verdict object keeps its own denial.
func TestPolicyCheckWriteIgnoresADenialNoCommandEnforces(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), checkRetestBody)
	out, err := runPolicyCLI(t, dir, "policy", "check", "automation:pg_x", "write")
	want := "allowed: automation:pg_x write\n" +
		"control: denied — default: deny\n" +
		"  not enforced: control on automation:<id> is checked for its denies only — the default decides on the entity a service call reaches\n" +
		"edit: allowed — allow \"automation:pg_*\" [edit create delete] (line 7)\n" +
		"create: allowed — allow \"automation:pg_*\" [edit create delete] (line 7)\n" +
		"delete: allowed — allow \"automation:pg_*\" [edit create delete] (line 7)\n" +
		"profile: agent (selected by active)\n" +
		"enforced: a write command refuses what this verdict denies\n"
	if cliExitCode(err) != 0 {
		t.Errorf("exit %d (%v), want 0", cliExitCode(err), err)
	}
	if out != want {
		t.Errorf("answer\n got: %q\nwant: %q", out, want)
	}

	out, err = runPolicyCLI(t, dir, "policy", "check", "automation:pg_x", "write", "--json")
	if cliExitCode(err) != 0 {
		t.Errorf("--json: exit %d (%v), want 0", cliExitCode(err), err)
	}
	m := decodeObject(t, out)
	wantField(t, "write --json", m, "allowed", true)
	verdicts, _ := m["verdicts"].([]any)
	if len(verdicts) != 4 {
		t.Fatalf("verdicts %v, want 4", verdicts)
	}
	control, _ := verdicts[0].(map[string]any)
	wantField(t, "write --json control", control, "verb", "control")
	wantField(t, "write --json control", control, "allowed", false)
	wantField(t, "write --json control", control, "enforced", false)
}

// TestPolicyCheckWriteScriptAllowHasNoGrantsNothingNote (D-128 (3), N-4): a
// `script:<id>` allow also grants the script's `run`, so its control verdict
// carries no "this allow grants nothing" note.
func TestPolicyCheckWriteScriptAllowHasNoGrantsNothingNote(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), checkRetestBody)
	out, err := runPolicyCLI(t, dir, "policy", "check", "script:pg_s", "write")
	rule := "allow \"script:pg_s\" [control edit create delete] (line 8)"
	want := "allowed: script:pg_s write\n" +
		"control: allowed — " + rule + "\n" +
		"edit: allowed — " + rule + "\n" +
		"create: allowed — " + rule + "\n" +
		"delete: allowed — " + rule + "\n" +
		"profile: agent (selected by active)\n" +
		"enforced: a write command refuses what this verdict denies\n"
	if cliExitCode(err) != 0 || out != want {
		t.Errorf("exit %d (%v)\n got: %q\nwant: %q", cliExitCode(err), err, out, want)
	}
}

// TestPolicyCheckInapplicableVerbNoteSaysItDecidesNothing (D-128 (4), N-21):
// a verb no command asks for at the resource still matches its rule (D-110),
// and the note says the verdict decides no command, so "allowed" and the note
// no longer read as a contradiction.
func TestPolicyCheckInapplicableVerbNoteSaysItDecidesNothing(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), checkRetestBody)
	out, err := runPolicyCLI(t, dir, "policy", "check", "file:automations.yaml/block/x", "edit")
	want := "allowed: file:automations.yaml/block/x edit\n" +
		"rule: allow \"file:automations.yaml\" [edit] (line 9)\n" +
		"profile: agent (selected by active)\n" +
		"note: edit does not apply at file:automations.yaml/block/x: no command asks for it there, so this verdict decides no command\n" +
		"enforced: a write command refuses what this verdict denies\n"
	if cliExitCode(err) != 0 || out != want {
		t.Errorf("exit %d (%v)\n got: %q\nwant: %q", cliExitCode(err), err, out, want)
	}
}

// TestPolicyCheckWriteScriptCountsTheRunTheDefaultDenies (D-128 (2), corrected): on a
// script the default that decides the deny-only `control script:<id>` also
// decides `control script:<id>/run`, which `script run` enforces in full — so
// the write check counts that control verdict whenever the run is denied, and
// says the run decides it, never "the default decides on the entity". When
// the run is allowed, the deny-only exemption holds and the line names the
// run instead.
func TestPolicyCheckWriteScriptCountsTheRunTheDefaultDenies(t *testing.T) {
	const base = "    default: deny\n    allow:\n" +
		"      \"script:morning\": [edit, create, delete]\n" +
		"      \"entity:script.morning_renamed\": [control, read]\n" + // read: script run finds the script it runs (W-2)
		"      \"service:script.turn_on\": [control]\n"
	rig := startSvcRig(t, reachOf("script.morning_renamed"))
	writePolicyFile(t, rig.dir, agentPolicy(base))

	out, err := runPolicyCLI(t, rig.dir, "policy", "check", "script:morning", "write")
	want := "denied: script:morning write\n" +
		"control: denied — default: deny\n" +
		"  note: a run is checked as control script:morning/run, denied — default: deny: script run and a service call starting the script refuse it\n" +
		"edit: allowed — allow \"script:morning\" [edit create delete] (line 7)\n" +
		"create: allowed — allow \"script:morning\" [edit create delete] (line 7)\n" +
		"delete: allowed — allow \"script:morning\" [edit create delete] (line 7)\n" +
		"profile: agent (selected by active)\n" +
		"enforced: a write command refuses what this verdict denies\n"
	if cliExitCode(err) != 1 || out != want {
		t.Errorf("run denied: exit %d (%v)\n got: %q\nwant: %q", cliExitCode(err), err, out, want)
	}
	out, err = runPolicyCLI(t, rig.dir, "policy", "check", "script:morning", "write", "--json")
	if cliExitCode(err) != 1 {
		t.Errorf("run denied --json: exit %d (%v), want 1", cliExitCode(err), err)
	}
	m := decodeObject(t, out)
	wantField(t, "run denied --json", m, "allowed", false)
	verdicts, _ := m["verdicts"].([]any)
	control, _ := verdicts[0].(map[string]any)
	wantField(t, "run denied --json control", control, "verb", "control")
	wantField(t, "run denied --json control", control, "enforced", true)
	out, err = runPolicyCLI(t, rig.dir, "policy", "check", "script:morning", "control")
	if cliExitCode(err) != 1 || strings.Contains(out, "not enforced") || !strings.Contains(out, "enforced: a write command refuses what this verdict denies\n") {
		t.Errorf("run denied, control alone: exit %d (%v)\n%s", cliExitCode(err), err, out)
	}
	out, err = runPolicyCLI(t, rig.dir, "script", "run", "morning_renamed", "--confirm")
	wantRefused(t, "the run the check calls denied", out, err, "control script:morning/run — default: deny")

	writePolicyFile(t, rig.dir, agentPolicy(base+"      \"script:morning/run\": [control]\n"))
	out, err = runPolicyCLI(t, rig.dir, "policy", "check", "script:morning", "write")
	want = "allowed: script:morning write\n" +
		"control: denied — default: deny\n" +
		"  not enforced: control on script:<id> is checked for its denies only — a run is decided by control script:morning/run, which is allowed\n" +
		"edit: allowed — allow \"script:morning\" [edit create delete] (line 7)\n" +
		"create: allowed — allow \"script:morning\" [edit create delete] (line 7)\n" +
		"delete: allowed — allow \"script:morning\" [edit create delete] (line 7)\n" +
		"profile: agent (selected by active)\n" +
		"enforced: a write command refuses what this verdict denies\n"
	if cliExitCode(err) != 0 || out != want {
		t.Errorf("run allowed: exit %d (%v)\n got: %q\nwant: %q", cliExitCode(err), err, out, want)
	}
	if out, err = runPolicyCLI(t, rig.dir, "script", "run", "morning_renamed", "--confirm"); err != nil {
		t.Errorf("the run the check calls allowed was refused: %v\n%s", err, out)
	}
}
