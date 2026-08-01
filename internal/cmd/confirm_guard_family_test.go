package cmd

import (
	"strings"
	"testing"
)

// TestConfirmGuardNamesTheCommandTheCallerRan — the refusal must describe the
// call that was refused.
//
// label/area/floor share one manual section, keyed "label" internally, and the
// refusal interpolated that key: `area create --confirm`, run as the literal
// first command of a session, was refused with `this is the session's first
// "label" command` — a claim about a command the caller had never typed, and
// one an agent can act on by running `hactl label ...` to satisfy a gate that
// is about `area`. The same shape sits on every aliased command: `rollback`
// is keyed "auto", `cc` is keyed "log", `changes`/`issues` are keyed "health".
func TestConfirmGuardNamesTheCommandTheCallerRan(t *testing.T) {
	cases := []struct {
		name    string
		wrong   string
		argv    []string
		covered []string
	}{
		{"area", "label", []string{"area", "create", "pg_gate_area", "--confirm"}, []string{"area", "floor", "label"}},
		{"floor", "label", []string{"floor", "create", "pg_gate_floor", "--confirm"}, []string{"area", "floor", "label"}},
		{"rollback", "auto", []string{"rollback", "--confirm"}, []string{"auto", "rollback", "trace"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setupInjectEnv(t, "guard-family-"+tc.name)

			_, errOut, execErr := executeCapture(t, tc.argv...)
			if execErr == nil {
				t.Fatal("first-of-family --confirm should be refused")
			}
			msg := execErr.Error()
			if !strings.Contains(msg, "--confirm refused") {
				t.Fatalf("expected the guard refusal, got: %v", execErr)
			}
			if !strings.Contains(msg, tc.name) {
				t.Errorf("the refusal does not name the command that was refused (%q): %s", tc.name, msg)
			}
			if strings.Contains(msg, `"`+tc.wrong+`"`) && tc.wrong != tc.name {
				t.Errorf("the refusal quotes %q, a command the caller never ran: %s", tc.wrong, msg)
			}
			// The how-to banner delivered with the refusal has the same job:
			// it must name commands the caller can act on.
			for _, member := range tc.covered {
				if !strings.Contains(errOut, member) {
					t.Errorf("the how-to banner does not name %q, which the delivered section covers:\n%s",
						member, firstLine(errOut))
				}
			}
		})
	}
}

// firstLine trims a stderr capture to its banner for a readable failure.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
