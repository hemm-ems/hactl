package cmd

import (
	"strings"
	"testing"
)

// The `policy check <resource> write` answer in full (D-120): every line below
// a verb — its partial denies, its narrowed allows not met, its notes, its
// deny-only default — sits indented under that verb, and the closing line says
// whether a write command acts on the verdict. Run on policyRig's registries,
// so a `where:` is evaluated.

// checkWriteBody is the profile the golden answers are decided under.
const checkWriteBody = `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.*": [control, delete]
      "entity:light.k*": [edit]
      "automation:1700000000000": [control]
    deny:
      "entity:light.kitchen/registry/labels": [write]
    where:
      "entity:light.k*": { label: nope }
  other:
    deny:
      "entity:*": [write]
`

func TestPolicyCheckWriteRendersEveryLineUnderItsVerb(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		exit int
		want string
	}{
		{
			// control granted; edit's narrowed allow not met, so the default
			// denies; delete granted, then partly denied by the field deny,
			// which holds delete because deleting the entity deletes the field.
			name: "partial and where not met",
			args: []string{"entity:light.kitchen", "write"},
			exit: 1,
			want: "denied: entity:light.kitchen write\n" +
				"control: allowed — allow \"entity:light.*\" [control delete] (line 7)\n" +
				"edit: denied — default: deny\n" +
				"  where not met: allow \"entity:light.k*\" [edit] where label=nope (line 8) — light.kitchen has no label nope\n" +
				"create: denied — default: deny\n" +
				"delete: denied — deny \"entity:light.kitchen/registry/labels\" [write] (line 11)\n" +
				"  partial: deny \"entity:light.kitchen/registry/labels\" [write] (line 11)\n" +
				"profile: agent (selected by active)\n" +
				"enforced: a write command refuses what this verdict denies\n",
		},
		{
			// A control allow on a typed address grants nothing (D-111): its
			// note sits under control.
			name: "a note under its verb",
			args: []string{"automation:1700000000000", "write"},
			exit: 1,
			want: "denied: automation:1700000000000 write\n" +
				"control: allowed — allow \"automation:1700000000000\" [control] (line 9)\n" +
				"  note: control on automation:1700000000000 is checked for its denies only — this allow grants nothing; a service call is decided by the entity's own verdict\n" +
				"edit: denied — default: deny\n" +
				"create: denied — default: deny\n" +
				"delete: denied — default: deny\n" +
				"profile: agent (selected by active)\n" +
				"enforced: a write command refuses what this verdict denies\n",
		},
		{
			// Without an allow the default denies the typed control, and no
			// command enforces that denial (D-107).
			name: "a deny-only default under control",
			args: []string{"automation:other", "write"},
			exit: 1,
			want: "denied: automation:other write\n" +
				"control: denied — default: deny\n" +
				"  not enforced: control on automation:<id> is checked for its denies only — the default decides on the entity a service call reaches\n" +
				"edit: denied — default: deny\n" +
				"create: denied — default: deny\n" +
				"delete: denied — default: deny\n" +
				"profile: agent (selected by active)\n" +
				"enforced: a write command refuses what this verdict denies\n",
		},
		{
			name: "what-if",
			args: []string{"entity:light.kitchen/registry/labels", "write", "--as", "other"},
			exit: 1,
			want: "what-if: --as other (the effective profile is unchanged)\n" +
				"denied: entity:light.kitchen/registry/labels write\n" +
				"edit: denied — deny \"entity:*\" [write] (line 16)\n" +
				"profile: other (selected by what-if)\n" +
				"enforced: a write command refuses what this verdict denies\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := policyRig(t, checkWriteBody)
			out, err := runPolicyCLI(t, ts.dir, append([]string{"policy", "check"}, tc.args...)...)
			if cliExitCode(err) != tc.exit {
				t.Errorf("exit %d (%v), want %d", cliExitCode(err), err, tc.exit)
			}
			if out != tc.want {
				t.Errorf("answer\n got: %q\nwant: %q", out, tc.want)
			}
		})
	}
}

// TestPolicyCheckWriteSaysWhetherItIsEnforced — with no profile in effect the
// closing line says no command acts on the verdict, and the JSON's enforced is
// false; with one it is true.
func TestPolicyCheckWriteSaysWhetherItIsEnforced(t *testing.T) {
	implicit := "version: 1\nprofiles:\n  agent:\n    deny:\n      \"entity:lock.*\": [write]\n"
	for _, tc := range []struct {
		name, body, last string
		enforced         bool
	}{
		{"implicit", implicit, "not enforced: no profile is in effect, so every command runs as without a policy", false},
		{"active", checkWriteBody, "enforced: a write command refuses what this verdict denies", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := policyRig(t, tc.body)
			out, _ := runPolicyCLI(t, ts.dir, "policy", "check", "entity:light.hall", "write")
			lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
			if lines[len(lines)-1] != tc.last {
				t.Errorf("closing line %q, want %q:\n%s", lines[len(lines)-1], tc.last, out)
			}
			out, _ = runPolicyCLI(t, ts.dir, "policy", "check", "entity:light.hall", "write", "--json")
			wantField(t, tc.name, decodeObject(t, out), "enforced", tc.enforced)
		})
	}
}

// TestPolicyCheckTypedControlNoteNeedsAnAllow — the "this allow grants
// nothing" note (D-111) is for an allow that decided; under a default allow,
// an allow-all profile or no file at all no allow decided, so there is none.
func TestPolicyCheckTypedControlNoteNeedsAnAllow(t *testing.T) {
	for name, body := range map[string]string{
		"default allow": "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"entity:lock.*\": [write]\n",
		"allow-all":     policyAllowEverything,
		"no file":       "",
	} {
		t.Run(name, func(t *testing.T) {
			ts := policyRig(t, body)
			out, err := runPolicyCLI(t, ts.dir, "policy", "check", "automation:x", "control")
			if cliExitCode(err) != 0 || !strings.HasPrefix(out, "allowed: automation:x control\n") {
				t.Errorf("exit %d (%v), want allowed:\n%s", cliExitCode(err), err, out)
			}
			if strings.Contains(out, "this allow grants nothing") {
				t.Errorf("no allow decided, yet the answer notes one:\n%s", out)
			}
		})
	}
}
