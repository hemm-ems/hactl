package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Enforcement at the CLI entry points: what the audit log's `command` holds,
// what Execute() prints on stderr for a policy refusal, and the confirm guard's
// witness over MCP.

// TestAuditCommandNeverCarriesContent (D-71): the audit log's `command` is the
// command path and its positional arguments — never a content-carrying flag
// value: not the --data of a service call, not the file -f names, not its
// contents.
func TestAuditCommandNeverCarriesContent(t *testing.T) {
	rig := startSvcRig(t, reachOf("lock.door"))
	writePolicyFile(t, rig.dir, policyAllowEverything)
	if out, err := runPolicyCLI(t, rig.dir, "svc", "call", "scene.apply", "--data", `{"entities":{"lock.door":"unlocked"},"note":"hunter2"}`); err != nil {
		t.Fatalf("svc call: %v\n%s", err, out)
	}
	typed := newTypedRig(t)
	writePolicyFile(t, typed.ts.dir, policyAllowEverything)
	file := typed.write(t, "candidate-hunter2.yaml", "id: heating\nalias: Heating hunter2\ntriggers: []\nactions: []\n")
	if out, err := runPolicyCLI(t, typed.ts.dir, "auto", "apply", "heating", "-f", file); err != nil {
		t.Fatalf("auto apply: %v\n%s", err, out)
	}
	for dir, want := range map[string]string{rig.dir: "hactl svc call scene.apply", typed.ts.dir: "hactl auto apply heating"} {
		lines := readAudit(t, dir)
		if len(lines) == 0 {
			t.Fatalf("%s: no audit line", want)
		}
		for _, l := range lines {
			if l["command"] != want {
				t.Errorf("audit command = %q, want exactly %q", l["command"], want)
			}
		}
	}
}

// TestExecuteStderrIsThePolicyLineAlone (D-70): through the CLI entry point a
// denial is one stderr line, and a broken file one line per problem — no
// `instance:` line after either, which every other error gets.
func TestExecuteStderrIsThePolicyLineAlone(t *testing.T) {
	t.Setenv("HACTL_MANUAL_MODE", "off")
	dir := policyInstance(t, closedHAURL(t), policyDenySecretLabel)
	stdout, stderr, err := executeCapture(t, "label", "create", "Garden", "--dir", dir)
	if cliExitCode(err) != 1 || policyErrorClass(err) != "policy_denied" {
		t.Fatalf("exit %d (%v)", cliExitCode(err), err)
	}
	if want := "policy_denied: create label:* — deny \"label:secret\" [create] (line 6)\n"; stderr != want {
		t.Errorf("denial stderr\n got: %q\nwant: %q", stderr, want)
	}
	if !strings.HasPrefix(stdout, "policy_denied: would create label") {
		t.Errorf("denial stdout:\n%s", stdout)
	}

	broken := policyInstance(t, closedHAURL(t), "version: 1\nactive: nobody\n")
	stdout, stderr, err = executeCapture(t, "area", "ls", "--dir", broken)
	if cliExitCode(err) != 1 || policyErrorClass(err) != "policy_broken" {
		t.Fatalf("broken: exit %d (%v)", cliExitCode(err), err)
	}
	want := err.Error() + "\n"
	if stderr != want || stdout != "" || !strings.HasPrefix(stderr, "policy_broken: "+filepath.Join(broken, "policy.yaml")+":") {
		t.Errorf("broken stderr\n got: %q\nwant: %q (stdout %q)", stderr, want, stdout)
	}
}

// TestMCPConfirmAfterItsDryRun (D-80, D-89; manual "MCP server": the dry-run +
// --confirm write path still applies): a tool call runs inside `hactl mcp`'s
// own CLI invocation, so the confirm guard is active for it. A dry run over
// MCP records the witness its --confirm needs — the confirm is accepted — and
// a --confirm the policy refuses after its preview passed says so.
func TestMCPConfirmAfterItsDryRun(t *testing.T) {
	ts := startCmdServer(t, map[string]any{"config/label_registry/create": map[string]any{"label_id": "garden", "name": "Garden"}}, nil)
	writePolicyFile(t, ts.dir, policyAllowEverything)
	t.Setenv("HACTL_MANUAL_MODE", "progressive")
	t.Setenv("HACTL_SESSION", "mcp-witness-"+filepath.Base(ts.dir))
	oldTerm, oldEntry := isTerminal, cliEntry
	isTerminal = func(*os.File) bool { return false }
	cliEntry = true // `hactl mcp` serves its calls from inside its own Execute()
	defer func() { isTerminal, cliEntry = oldTerm, oldEntry }()

	call := mcpCaller(t, ts.dir, true)
	if text, isErr := call("label create Garden"); isErr {
		t.Fatalf("dry run over MCP: %s", text)
	}
	if text, isErr := call("label create Garden --confirm"); isErr {
		t.Fatalf("--confirm after its dry run over MCP was refused: %s", text)
	}
	ts.mu.Lock()
	sent := ts.cmdCounts["config/label_registry/create"]
	ts.mu.Unlock()
	if sent != 1 {
		t.Errorf("label creates sent = %d, want 1", sent)
	}

	if text, isErr := call("label create Garden"); isErr {
		t.Fatalf("second dry run: %s", text)
	}
	writePolicyFile(t, ts.dir, policyDenySecretLabel)
	text, isErr := call("label create Garden --confirm")
	if !isErr || !strings.Contains(text, "policy_denied") || !strings.Contains(text, "note: the preview had passed") {
		t.Errorf("a --confirm refused after its preview passed, over MCP: error %v\n%s", isErr, text)
	}
}
