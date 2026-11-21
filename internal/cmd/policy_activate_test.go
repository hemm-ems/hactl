package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// A draft with a comment and layout a re-serialiser would lose: activate must
// write these bytes unchanged (D-163).
const activateDraft = `# drafted by the agent — the owner reviews this
version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*":       [read]     # see everything
      "entity:light.*": [control]
`

// withOwnerTerminal makes the in-process tests behave as if stdin and stdout
// were a terminal. The gate itself is proven through a real process below.
func withOwnerTerminal(t *testing.T) {
	t.Helper()
	old := ownerTerminalCheck
	ownerTerminalCheck = func() bool { return true }
	t.Cleanup(func() { ownerTerminalCheck = old })
}

// activateIn runs runPolicyActivate on dir with answer typed at the prompt.
func activateIn(t *testing.T, dir, draft, answer string) (string, error) {
	t.Helper()
	old := flagDir
	flagDir = dir
	defer func() { flagDir = old }()
	var out bytes.Buffer
	err := runPolicyActivate(context.Background(), strings.NewReader(answer), &out, draft)
	return out.String(), err
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a path the test made
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// noTempFiles fails when activate left a temporary file in dir.
func noTempFiles(t *testing.T, dir string) {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("a temporary file was left behind: %s", e.Name())
		}
	}
}

func TestPolicyActivateWritesTheDraftBytesAndKeepsThePrevious(t *testing.T) {
	withOwnerTerminal(t)
	ts := policyRig(t, starterPolicy)
	draft := filepath.Join(ts.dir, "policy.draft.yaml")
	if err := os.WriteFile(draft, []byte(activateDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := activateIn(t, ts.dir, "policy.draft.yaml", "agent\n")
	if err != nil {
		t.Fatalf("activate: %v\n%s", err, out)
	}
	target := filepath.Join(ts.dir, policy.FileName)
	if got := readFile(t, target); got != activateDraft {
		t.Errorf("policy.yaml is not the draft's bytes:\n%s", got)
	}
	if got := readFile(t, target+".prev"); got != starterPolicy {
		t.Errorf("policy.yaml.prev is not the previous file:\n%s", got)
	}
	if got := readFile(t, draft); got != activateDraft {
		t.Error("the draft was changed")
	}
	noTempFiles(t, ts.dir)
	for _, want := range []string{
		"review: " + draft + " (draft, not in effect)\n",
		"  switch: 2 of 8 — light.hall, light.kitchen\n",
		"replaces: " + target + " (profile readonly)\n",
		"type the profile name to activate it (agent), anything else cancels: ",
		"activated: " + target + " — profile agent is in effect from the next command\n",
		"previous: " + target + ".prev\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("activate output lacks %q:\n%s", want, out)
		}
	}
	// The activated file is what every command now loads.
	pol, err := policy.Load(ts.dir)
	if err != nil || pol.Active() != "agent" {
		t.Errorf("after activate, policy.Load = %v, %v", pol, err)
	}
}

func TestPolicyActivateMakesNoPrevWhenThereWasNone(t *testing.T) {
	withOwnerTerminal(t)
	ts := policyRig(t, "")
	if err := os.WriteFile(filepath.Join(ts.dir, "d.yaml"), []byte(activateDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := activateIn(t, ts.dir, "d.yaml", "agent\n")
	if err != nil {
		t.Fatalf("activate: %v\n%s", err, out)
	}
	if !strings.Contains(out, "replaces: nothing (no policy.yaml yet)\n") || strings.Contains(out, "previous:") {
		t.Errorf("output:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(ts.dir, policy.FileName+".prev")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a .prev was made with no previous file: %v", err)
	}
	noTempFiles(t, ts.dir)
}

// Anything but the exact profile name cancels: exit 1, nothing written.
func TestPolicyActivateCancelsOnAnythingButTheProfileName(t *testing.T) {
	withOwnerTerminal(t)
	for _, answer := range []string{"Agent\n", "\n", "", "yes\n", "agent x\n"} {
		ts := policyRig(t, starterPolicy)
		if err := os.WriteFile(filepath.Join(ts.dir, "d.yaml"), []byte(activateDraft), 0o600); err != nil {
			t.Fatal(err)
		}
		out, err := activateIn(t, ts.dir, "d.yaml", answer)
		if cliExitCode(err) != 1 || !strings.Contains(err.Error(), "cancelled — nothing was written") {
			t.Errorf("answer %q: err = %v", answer, err)
		}
		if strings.Contains(out, "activated:") {
			t.Errorf("answer %q: output says activated:\n%s", answer, out)
		}
		if got := readFile(t, filepath.Join(ts.dir, policy.FileName)); got != starterPolicy {
			t.Errorf("answer %q: policy.yaml changed", answer)
		}
		if _, statErr := os.Stat(filepath.Join(ts.dir, policy.FileName+".prev")); statErr == nil {
			t.Errorf("answer %q: a .prev was written on a cancel", answer)
		}
		noTempFiles(t, ts.dir)
	}
}

// D-163: refused before any prompt, nothing written.
func TestPolicyActivateRefusesBeforeThePrompt(t *testing.T) {
	withOwnerTerminal(t)
	cases := []struct {
		name, draftName, draft, wantErr string
		setup                           func(dir string)
		class                           string
	}{
		{name: "broken", draftName: "d.yaml", draft: "version: 1\nactive: agent\nprofiles:\n  agent:\n    allow:\n      \"entity:*\": [wirte]\n", wantErr: "d.yaml:6:", class: enforce.ClassBroken},
		{name: "no profile in effect", draftName: "d.yaml", draft: "version: 1\nprofiles:\n  agent: {}\n", wantErr: "puts no profile in effect"},
		{name: "missing", draftName: "nope.yaml", wantErr: "no draft at"},
		{name: "policy.yaml itself", draftName: "policy.yaml", wantErr: "itself"},
		{name: "policy.yml beside", draftName: "d.yaml", draft: activateDraft, wantErr: "policy.yml",
			setup: func(dir string) { _ = os.WriteFile(filepath.Join(dir, "policy.yml"), []byte("x"), 0o600) }}, //nolint:gosec // the test's own temp dir
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ts := policyRig(t, starterPolicy)
			if tc.draft != "" {
				if err := os.WriteFile(filepath.Join(ts.dir, tc.draftName), []byte(tc.draft), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if tc.setup != nil {
				tc.setup(ts.dir)
			}
			out, err := activateIn(t, ts.dir, tc.draftName, "agent\n")
			if cliExitCode(err) != 1 || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want exit 1 naming %q", err, tc.wantErr)
			}
			if tc.class != "" && policyErrorClass(err) != tc.class {
				t.Errorf("class %q, want %q", policyErrorClass(err), tc.class)
			}
			if strings.Contains(out, "type the profile name") {
				t.Errorf("the owner was prompted before the refusal:\n%s", out)
			}
			if got := readFile(t, filepath.Join(ts.dir, policy.FileName)); got != starterPolicy {
				t.Error("policy.yaml changed")
			}
			noTempFiles(t, ts.dir)
		})
	}
}

// D-163: activate runs without the instance's policy session, so an instance
// that requires a policy and has none — or a broken one — can be given one.
func TestPolicyActivateRepairsAMissingOrBrokenRequiredPolicy(t *testing.T) {
	withOwnerTerminal(t)
	for name, current := range map[string]string{"missing": "", "broken": "version: 1\nactive: nobody\n"} {
		t.Run(name, func(t *testing.T) {
			ts := policyRig(t, current)
			envPath := filepath.Join(ts.dir, ".env")
			env := readFile(t, envPath) + "HACTL_POLICY=required\n"
			if err := os.WriteFile(envPath, []byte(env), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(ts.dir, "d.yaml"), []byte(activateDraft), 0o600); err != nil {
				t.Fatal(err)
			}
			// Through the command tree, so the pre-run (the policy session)
			// runs: stdin is a pipe carrying the answer.
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			_, _ = w.WriteString("agent\n")
			_ = w.Close()
			oldIn, oldOut := os.Stdin, os.Stdout
			devnull, _ := os.Open(os.DevNull)
			os.Stdin, os.Stdout = r, devnull
			defer func() { os.Stdin, os.Stdout = oldIn, oldOut; _ = r.Close(); _ = devnull.Close() }()
			var buf bytes.Buffer
			err = RunWithOutput([]string{"hactl", "--dir", ts.dir, "policy", "activate", "d.yaml"}, &buf)
			os.Stdin, os.Stdout = oldIn, oldOut
			if err != nil {
				t.Fatalf("activate on a %s required policy: %v", name, err)
			}
			if got := readFile(t, filepath.Join(ts.dir, policy.FileName)); got != activateDraft {
				t.Errorf("policy.yaml was not replaced")
			}
		})
	}
}

// --- the terminal gate, through a real process ---------------------------------

// TestHelperProcessHactl is not a test: run as a child with
// HACTL_HELPER_ARGS set, it is hactl's own entry point with real stdin and
// stdout, so the terminal gate sees exactly what the child was given.
//
//test:no-assert the child process is hactl itself; its parent test asserts the exit code, the output and the files it left
func TestHelperProcessHactl(t *testing.T) {
	raw := os.Getenv("HACTL_HELPER_ARGS")
	if raw == "" {
		t.Skip("helper process only")
	}
	var args []string
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		t.Fatalf("HACTL_HELPER_ARGS %q: %v", raw, err)
	}
	os.Args = append([]string{"hactl"}, args...)
	rootCmd.SetArgs(args)
	code := 0
	if err := Execute(); err != nil {
		code = 1
		var ec interface{ ExitCode() int }
		if errors.As(err, &ec) {
			code = ec.ExitCode()
		}
		_, _ = os.Stderr.WriteString("hactl: " + err.Error() + "\n")
	}
	os.Exit(code)
}

// hactlProcess is a child running hactl with args.
func hactlProcess(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	raw, _ := json.Marshal(args)
	// Bounded: a gate that let a child through to its prompt would otherwise
	// wait on a terminal nobody types into — a hang is the one failure a test
	// run cannot report.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	c := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestHelperProcessHactl$") //nolint:gosec // the test binary itself
	c.WaitDelay = 2 * time.Second
	c.Env = append(os.Environ(), "HACTL_HELPER_ARGS="+string(raw), "HACTL_MANUAL_MODE=off")
	return c
}

// D-149: without a terminal on stdin and stdout, activate is refused with the
// owner hint, before it reads anything — an agent's shell has none (M-5).
func TestPolicyActivateRefusedWithoutATerminalCLI(t *testing.T) {
	ts := policyRig(t, starterPolicy)
	if err := os.WriteFile(filepath.Join(ts.dir, "d.yaml"), []byte(activateDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	c := hactlProcess(t, "--dir", ts.dir, "policy", "activate", "d.yaml")
	c.Stdin = strings.NewReader("agent\n")
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 {
		t.Fatalf("activate without a terminal: %v (want exit 1)\nstdout %s\nstderr %s", err, stdout.String(), stderr.String())
	}
	for _, want := range []string{
		"policy activate needs a terminal: only the owner activates a policy",
		"hint: ask the owner; a change goes through a draft and `hactl policy activate`",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("stderr lacks %q:\n%s", want, stderr.String())
		}
	}
	if strings.Contains(stdout.String(), "review:") || strings.Contains(stdout.String(), "type the profile name") {
		t.Errorf("a refused activate printed the review or the prompt:\n%s", stdout.String())
	}
	if got := readFile(t, filepath.Join(ts.dir, policy.FileName)); got != starterPolicy {
		t.Error("policy.yaml changed")
	}
	if ts.cmdCounts["config/entity_registry/list"] != 0 {
		t.Error("a refused activate contacted Home Assistant")
	}
}
