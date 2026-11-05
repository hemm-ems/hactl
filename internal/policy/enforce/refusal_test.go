package enforce_test

import (
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// TestDeniedErrorHintAndLine (D-118, D-119): the hint is built from the
// denials' causes in the contract's order, and the stderr line is a count
// that repeats no denial — for each kind of cause, alone and mixed, at both
// layers.
func TestDeniedErrorHintAndLine(t *testing.T) {
	deny := func(res string, line int) enforce.Denial {
		return enforce.Denial{Resource: res, Verb: "edit", Rule: `deny "` + res + `" [edit]`, Line: line}
	}
	deflt := func(res string) enforce.Denial {
		return enforce.Denial{Resource: res, Verb: "control", Rule: policy.RuleDefaultDeny}
	}
	failClosed := func(res, reason string) enforce.Denial {
		return enforce.Denial{Resource: res, Verb: "control", Rule: "fail-closed: " + reason}
	}
	const tail = " — report it; do not edit policy.yaml"
	for _, tc := range []struct {
		name     string
		ref      enforce.DeniedError
		hint     string
		stderrLn string
	}{
		{
			name: "deny lines are distinct and ascending",
			ref: enforce.DeniedError{Layer: enforce.LayerCommand, Checked: 3, Profile: "agent",
				Denied: []enforce.Denial{deny("entity:b", 9), deny("entity:a", 4), deny("entity:c", 9)}},
			hint:     "the owner would change the rule on line 4, 9" + tail,
			stderrLn: "policy_denied: 3 of 3 resources denied (profile agent)",
		},
		{
			name: "every cause, in the contract's order",
			ref: enforce.DeniedError{Layer: enforce.LayerCommand, Checked: 6, Profile: "agent", PreviewPassed: true,
				Denied: []enforce.Denial{
					failClosed("service:light.turn_on", "reach unknown: timeout"),
					deflt("entity:light.a"), deny("entity:lock.x", 12), deflt("entity:light.b"),
					failClosed("entity:z", "second"),
				}},
			hint: "the preview had passed, and something it depends on changed since (the policy, an area or label, a service call's reach); " +
				"the owner would change the rule on line 12; " +
				"nothing allows entity:light.a (and 1 more); the owner would add an allow; " +
				"hactl could not check service:light.turn_on (reach unknown: timeout), so it refused; " +
				"allowed: 1 of 6 checked resources, but the command is refused as a whole" + tail,
			stderrLn: "policy_denied: 5 of 6 resources denied (profile agent)",
		},
		{
			name: "everything denied: no allowed part",
			ref: enforce.DeniedError{Layer: enforce.LayerCommand, Checked: 1, Profile: "p",
				Denied: []enforce.Denial{deflt("label:*")}},
			hint:     "nothing allows label:*; the owner would add an allow" + tail,
			stderrLn: "policy_denied: 1 of 1 resources denied (profile p)",
		},
		{
			name: "a request refused whole counts as one checked",
			ref: enforce.DeniedError{Layer: enforce.LayerRequest, Profile: "agent", Written: []string{},
				Denied: []enforce.Denial{{Resource: "ws foo/bar", Rule: "fail-closed: unmapped request ws foo/bar"}}},
			hint:     "hactl could not check ws foo/bar (unmapped request ws foo/bar), so it refused" + tail,
			stderrLn: "policy_denied: 1 of 1 resources denied (profile agent) — refused at the request layer; already written: nothing",
		},
		{
			name: "a request the allowed plan missed is a hactl bug",
			ref: enforce.DeniedError{Layer: enforce.LayerRequest, Checked: 2, Profile: "agent", PlanAllowed: true,
				Written: []string{"edit file:a.yaml"}, Denied: []enforce.Denial{deny("file:b.yaml", 7)}},
			hint: "hactl's own plan missed this request — a hactl bug to report; do not edit policy.yaml",
			stderrLn: "policy_denied: 1 of 2 resources denied (profile agent) — refused at the request layer although the " +
				"command's plan allowed it (a hactl bug to report); already written: edit file:a.yaml",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.ref.Hint(); got != tc.hint {
				t.Errorf("hint\n got: %q\nwant: %q", got, tc.hint)
			}
			if got := tc.ref.Error(); got != tc.stderrLn {
				t.Errorf("stderr line\n got: %q\nwant: %q", got, tc.stderrLn)
			}
		})
	}
}
