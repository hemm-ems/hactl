package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// lintDraftLocks gives lint every reason to name a lock: a deny on lock.*, and
// a narrowed allow on lock.* the agent could satisfy itself (self-grant
// names an example entity).
const lintDraftLocks = `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:lock.*": [control]
      "entity:*/registry/labels": [edit]
    deny:
      "entity:lock.*/attr/battery_level": [read]
    where:
      "entity:lock.*": { label: energy }
`

// lintRun runs `policy lint --file d.yaml` in dir, returning stdout and stderr.
func lintRun(t *testing.T, dir string, extra ...string) (string, string, error) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldErr := os.Stderr
	os.Stderr = w
	out, runErr := runPolicyCLI(t, dir, append([]string{"policy", "lint", "--file", "d.yaml"}, extra...)...)
	os.Stderr = oldErr
	_ = w.Close()
	stderr, _ := io.ReadAll(r)
	_ = r.Close()
	return out, string(stderr), runErr
}

func lintRig(t *testing.T, active string) string {
	t.Helper()
	ts := policyRig(t, active)
	if err := os.WriteFile(filepath.Join(ts.dir, "d.yaml"), []byte(lintDraftLocks), 0o600); err != nil {
		t.Fatal(err)
	}
	return ts.dir
}

// D-173: the owner at a terminal lints against the whole instance — the
// findings name the lock the draft would let the agent relabel.
func TestPolicyLintOwnerNamesWhatTheInstanceHolds(t *testing.T) {
	withOwnerTerminal(t)
	dir := lintRig(t, hidesLocks)
	out, _, _ := lintRun(t, dir, "--json")
	if !strings.Contains(out, "lock.front_door") || !strings.Contains(out, `"inventory": "instance"`) {
		t.Errorf("the owner's lint does not name the lock it found:\n%s", out)
	}
}

// D-173: without a terminal and with a profile in effect that hides lock.*,
// lint's inventory is what that profile reads: no finding names a lock, a
// no-match says it is about what the profile can read, and the answer says
// what it checked against.
func TestPolicyLintWithoutATerminalNamesNothingTheProfileHides(t *testing.T) {
	withoutOwnerTerminal(t)
	dir := lintRig(t, hidesLocks)
	for _, args := range [][]string{nil, {"--json"}} {
		out, stderr, _ := lintRun(t, dir, args...)
		if strings.Contains(out+stderr, "lock.front_door") {
			t.Errorf("lint %v names an entity the active profile hides:\n%s\n%s", args, out, stderr)
		}
		if args == nil && (!strings.Contains(out, "inventory: what the active profile can read") ||
			!strings.Contains(out, "matches no entity that this profile can read")) {
			t.Errorf("lint does not say it checked only what the profile reads:\n%s", out)
		}
		if args != nil && !strings.Contains(out, `"inventory": "readable"`) {
			t.Errorf("lint --json inventory:\n%s", out)
		}
	}
}

// D-173: an instance that requires a policy and has none — or a broken one —
// gives lint no profile to filter by: the inventory is not checked at all,
// one line says so, and the text-level findings still print.
func TestPolicyLintWithoutAProfileWhereOneIsRequired(t *testing.T) {
	withoutOwnerTerminal(t)
	for name, active := range map[string]string{"missing": "", "broken": "version: 1\nactive: nobody\n"} {
		t.Run(name, func(t *testing.T) {
			dir := lintRig(t, active)
			env := readFile(t, filepath.Join(dir, ".env")) + "HACTL_POLICY=required\n"
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
				t.Fatal(err)
			}
			draft := lintDraftLocks + "  spare:\n    allow:\n      \"entity:light.*\": [control]\n"
			if err := os.WriteFile(filepath.Join(dir, "d.yaml"), []byte(draft), 0o600); err != nil {
				t.Fatal(err)
			}
			out, stderr, err := lintRun(t, dir)
			if strings.Contains(out+stderr, "lock.front_door") || strings.Contains(out, "no-match") {
				t.Errorf("lint used the instance without a profile to filter by:\n%s", out)
			}
			if !strings.Contains(out, "inventory not checked: no profile in effect\n") {
				t.Errorf("lint does not say the inventory was not checked:\n%s", out)
			}
			if !strings.Contains(out, "redundant-allow") {
				t.Errorf("a text-level finding (redundant-allow in profile spare) is missing:\n%s", out)
			}
			if cliExitCode(err) != 1 {
				t.Errorf("exit %d, want 1 (not clean)", cliExitCode(err))
			}
		})
	}
}

// D-173: with no policy at all and none required, nothing is hidden from
// anyone (D-58), so lint checks the whole instance.
func TestPolicyLintWithoutAPolicyChecksTheInstance(t *testing.T) {
	withoutOwnerTerminal(t)
	dir := lintRig(t, "")
	out, _, _ := lintRun(t, dir, "--json")
	if !strings.Contains(out, `"inventory": "instance"`) || !strings.Contains(out, "lock.front_door") {
		t.Errorf("lint without any policy:\n%s", out)
	}
}
