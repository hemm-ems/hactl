package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/manual"
)

// F-8: a witness identifies the write, and a flag is part of what a write
// writes. The key used to hold the positional arguments only, so a dry-run of
// `svc call light.turn_on --data '{"entity_id":"light.a"}'` authorized the
// --confirm of the same call on light.b — the service is the argument, the
// entity it acts on is the flag.
func TestExecute_ConfirmGuardKeysOnTheFlags(t *testing.T) {
	setupGuardEnv(t, "guard-flags-"+filepath.Base(t.TempDir()))
	dataA, dataB := `{"entity_id":"light.a"}`, `{"entity_id":"light.b"}`

	// The preview warns (HA is unreachable) and succeeds: a witness is recorded.
	if _, _, err := executeCapture(t, "svc", "call", "light.turn_on", "--data", dataA); err != nil {
		t.Fatalf("dry-run should succeed against an unreachable HA (it warns): %v", err)
	}

	_, _, execErr := executeCapture(t, "svc", "call", "light.turn_on", "--data", dataB, "--confirm")
	if execErr == nil || !strings.Contains(execErr.Error(), "--confirm refused") {
		t.Fatalf("a dry-run with --data %s must not authorize --confirm with --data %s; got: %v", dataA, dataB, execErr)
	}
	// The refusal names a dry-run that works: the same command with the
	// flags it was given, quoted so it can be pasted.
	wantForm := `hactl svc call light.turn_on --data '{"entity_id":"light.b"}'`
	if !strings.Contains(execErr.Error(), "run `"+wantForm+"` without --confirm") {
		t.Errorf("the refusal does not name the dry-run with its flags (%s):\n%v", wantForm, execErr)
	}

	// The previewed call itself passes the guard (it then fails at the dead
	// HA address, which is not the guard's refusal).
	_, _, execErr = executeCapture(t, "svc", "call", "light.turn_on", "--data", dataA, "--confirm")
	if execErr != nil && strings.Contains(execErr.Error(), "--confirm refused") {
		t.Fatalf("the previewed --data must pass the guard: %v", execErr)
	}
	// A global output flag on either side does not change the write.
	_, _, execErr = executeCapture(t, "svc", "call", "light.turn_on", "--data", dataA, "--json", "--confirm")
	if execErr != nil && strings.Contains(execErr.Error(), "--confirm refused") {
		t.Fatalf("--json is an output flag and must not invalidate the preview: %v", execErr)
	}
}

// -f is keyed on the path as typed: a preview of one candidate file does not
// authorize writing another.
func TestExecute_ConfirmGuardKeysOnTheFileFlag(t *testing.T) {
	dir := setupGuardEnv(t, "guard-file-"+filepath.Base(t.TempDir()))
	other := filepath.Join(dir, "other.yaml")
	if err := os.WriteFile(other, []byte("id: guard_other\nalias: Other\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := executeCapture(t, "auto", "create", "-f", filepath.Join(dir, "cand.yaml")); err != nil {
		t.Fatalf("dry-run: %v", err)
	}
	_, _, execErr := executeCapture(t, "auto", "create", "-f", other, "--confirm")
	if execErr == nil || !strings.Contains(execErr.Error(), "--confirm refused") {
		t.Fatalf("a preview of cand.yaml must not authorize creating other.yaml; got: %v", execErr)
	}
	if !strings.Contains(execErr.Error(), "hactl auto create --file "+shellQuote(other)) {
		t.Errorf("the refusal does not name the dry-run with its file: %v", execErr)
	}
}

// The words a witness records: positional arguments first, then the flags the
// caller set sorted by name, without --confirm and without global flags; the
// D-215 policy witness reads the same words.
func TestWitnessArgsRule(t *testing.T) {
	resetSubcommandFlags()
	t.Cleanup(resetSubcommandFlags)
	if err := svcCallCmd.ParseFlags([]string{"--return", "--json", "--data", `{"a": 1}`, "--confirm", "--top", "3"}); err != nil {
		t.Fatal(err)
	}
	got := strings.Join(witnessArgs(svcCallCmd, []string{"light.turn_on"}), " ")
	if want := `light.turn_on --data '{"a": 1}' --return`; got != want {
		t.Errorf("witnessArgs = %s, want %s", got, want)
	}

	dir := t.TempDir()
	now := time.Date(2026, 11, 26, 10, 0, 0, 0, time.UTC)
	words := witnessArgs(svcCallCmd, []string{"light.turn_on"})
	recordPolicyWitness(dir, "s", "hactl svc call", words, "p@h", now)
	if hasPolicyWitness(dir, "s", "hactl svc call", []string{"light.turn_on"}, "p@h", now) {
		t.Error("a policy witness recorded with flags must not answer for the same call without them")
	}
	if !hasPolicyWitness(dir, "s", "hactl svc call", words, "p@h", now) {
		t.Error("the policy witness must answer for the words it was recorded with")
	}
}

func TestShellQuote(t *testing.T) {
	for in, want := range map[string]string{
		"light.a":        "light.a",
		"-":              "-",
		"@data.json":     "@data.json",
		"":               "''",
		"a b":            "'a b'",
		`{"x":1}`:        `'{"x":1}'`,
		"it's":           `'it'\''s'`,
		"/tmp/x/y.yaml":  "/tmp/x/y.yaml",
		"mdi:flash,--=+": "mdi:flash,--=+",
	} {
		if got := shellQuote(in); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", in, got, want)
		}
	}
}

// D-109's path: over MCP the dry-run records, and the --confirm checks, the
// same words — a dry-run with one flag value does not authorize another.
func TestMCPConfirmKeysOnTheFlags(t *testing.T) {
	ts := startCmdServer(t, map[string]any{"config/label_registry/create": map[string]any{"label_id": "garden", "name": "Garden"}}, nil)
	t.Setenv("HACTL_MANUAL_MODE", "progressive")
	t.Setenv("HACTL_SESSION", "mcp-flags-"+filepath.Base(ts.dir))
	oldTerm, oldEntry := isTerminal, cliEntry
	isTerminal = func(*os.File) bool { return false }
	cliEntry = true // `hactl mcp` serves its calls from inside its own Execute()
	defer func() { isTerminal, cliEntry = oldTerm, oldEntry }()
	// Each MCP call leaves its (by then cancelled) context on rootCmd and on
	// the command it ran; a later rootCmd.Execute() in this package would
	// inherit it and fail with "operation was canceled".
	t.Cleanup(func() { resetCommandContexts(rootCmd) })

	call := mcpCaller(t, ts.dir, true)
	if text, isErr := call("label create Garden --icon mdi:flower"); isErr {
		t.Fatalf("dry run over MCP: %s", text)
	}
	if !hasWitness(stateCacheDir(ts.dir), manual.SessionKey(), "hactl label create", []string{"Garden", "--icon", "mdi:flower"}, time.Now()) {
		t.Error("the MCP dry run did not record its flags in the witness")
	}
	text, isErr := call("label create Garden --icon mdi:tree --confirm")
	if !isErr || !strings.Contains(text, "--confirm refused") {
		t.Fatalf("an MCP --confirm with another --icon than its dry run must be refused: %v\n%s", isErr, text)
	}
	if text, isErr := call("label create Garden --icon mdi:flower --confirm"); isErr {
		t.Fatalf("the previewed --confirm over MCP was refused: %s", text)
	}
}

// resetCommandContexts gives every command of the tree a live context again.
func resetCommandContexts(c *cobra.Command) {
	c.SetContext(context.Background())
	for _, sub := range c.Commands() {
		resetCommandContexts(sub)
	}
}
