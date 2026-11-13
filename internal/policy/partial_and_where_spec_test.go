package policy_test

// Spec tests for two edges of Decide the earlier suites leave open, authored
// from the manual's "Decision order" (D-52, D-66) and the where: selector
// (D-55): only a DENY makes a verdict partial, a default-deny verdict is never
// partial, and a resource without an area or labels never satisfies a
// selector, however wide its glob.

import (
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
)

// TestOnlyADescendantDenyMakesAVerdictPartial — D-66 step 4 counts a deny of
// the same profile on a strict descendant. An allow on a child sub-resource
// beside an allow on the parent narrows nothing: the parent stays whole and
// allowed, for a write verb and for read.
func TestOnlyADescendantDenyMakesAVerdictPartial(t *testing.T) {
	p := mustLoad(t, writePolicy(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*": [read, edit]
      "entity:*/registry/labels": [edit, read]
`))
	r := mustResource(t, "entity:light.x")
	for _, v := range []policy.Verb{policy.Edit, policy.Read} {
		got := p.Decide(r, v, nil)
		if !got.Allowed || got.Line != 7 || got.Partial || len(got.PartialDenies) != 0 {
			t.Errorf("%s %s = allowed=%v line %d partial=%v denies=%+v; want allowed by line 7, whole (an allow on a child is not a descendant deny)",
				r, v, got.Allowed, got.Line, got.Partial, got.PartialDenies)
		}
	}
}

// TestADefaultDenyVerdictIsNeverPartial — D-66 applies "if steps 2-3 allowed
// the resource". Under a default-deny profile whose only rule is a descendant
// deny, the parent is denied by `default: deny` itself — not by the
// descendant deny, and not partial.
func TestADefaultDenyVerdictIsNeverPartial(t *testing.T) {
	p := mustLoad(t, writePolicy(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    deny:
      "entity:*/attr/x": [read, edit]
`))
	r := mustResource(t, "entity:light.x")
	for _, v := range []policy.Verb{policy.Read, policy.Edit} {
		got := p.Decide(r, v, nil)
		if got.Allowed || got.Rule != policy.RuleDefaultDeny || got.Line != 0 || got.Partial || len(got.PartialDenies) != 0 {
			t.Errorf("%s %s = allowed=%v rule %q line %d partial=%v denies=%+v; want denied by %q, line 0, not partial",
				r, v, got.Allowed, got.Rule, got.Line, got.Partial, got.PartialDenies, policy.RuleDefaultDeny)
		}
	}
}

// TestASelectorNeverHoldsForAResourceWithoutAreaOrLabels — D-55: a selector
// narrows. An entity or a device that has no area (Live answers ok=false) is
// in no area, so even `area: "*"` / `area: "?*"` does not hold; an empty label
// list never satisfies `label: "*"`. The narrowed allow does not grant and
// default: deny decides.
func TestASelectorNeverHoldsForAResourceWithoutAreaOrLabels(t *testing.T) {
	for _, sel := range []string{`area: "*"`, `area: "?*"`, `label: "*"`, `label: "?*"`} {
		p := mustLoad(t, writePolicy(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.*": [control]
      "device:*": [edit]
    where:
      "entity:light.*": { `+sel+` }
      "device:*": { `+sel+` }
`))
		live := &countingLive{} // no areas, no labels: ok=false / empty
		for _, q := range []struct {
			res  string
			verb policy.Verb
		}{
			{"entity:light.noarea", policy.Control},
			{"device:0123abcd", policy.Edit},
		} {
			got := p.Decide(mustResource(t, q.res), q.verb, live)
			if got.Allowed || got.Rule != policy.RuleDefaultDeny {
				t.Errorf("where {%s}: %s %s = allowed=%v rule %q; want denied by %q (no area / no labels never satisfies a selector)",
					sel, q.res, q.verb, got.Allowed, got.Rule, policy.RuleDefaultDeny)
			}
		}
	}
}
