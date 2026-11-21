package policy_test

// D-125, from the §3.5 bypass lens: a deny on a part refuses every verb that
// changes the part, wherever the verb is asked — create and delete of the
// whole, control on an entity (its state and attributes), edit of a file (its
// blocks). An allow on a part still grants only the part.

import (
	"slices"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
)

// denyUnderAllowAll is the shape the bypass lens used: everything allowed by
// rule, one deny on line 8.
func denyUnderAllowAll(pattern, verbs string) string {
	return "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    allow:\n      \"*\": [read, write]\n    deny:\n      \"" +
		pattern + "\": " + verbs + "\n"
}

// TestADenyOnAPartRefusesDeletingAndCreatingItsOwner — deleting (or creating)
// an entry deletes (or creates) its config, options or registry entry, so a
// deny there holding the verb — by `write` or by name — refuses it, partly,
// naming the deny: in a profile of its own and under a default-deny profile
// that allows everything by rule.
func TestADenyOnAPartRefusesDeletingAndCreatingItsOwner(t *testing.T) {
	for _, tc := range []struct{ pattern, verbs, owner string }{
		{"automation:heating/config", "[write]", "automation:heating"},
		{"automation:heating/config", "[delete]", "automation:heating"},
		{"script:wakeup/config", "[write]", "script:wakeup"},
		{"template:room_temp/config", "[write]", "template:room_temp"},
		{"helper:input_boolean/guest_mode/config", "[write]", "helper:input_boolean/guest_mode"},
		{"config_entry:ce1/options", "[write]", "config_entry:ce1"},
		{"entity:lock.front/registry", "[write]", "entity:lock.front"},
		{"entity:*/registry/entity_id", "[write]", "entity:lock.front"},
		{"device:dev1/registry", "[write]", "device:dev1"},
	} {
		for name, body := range map[string]string{
			"one deny":                oneDeny(tc.pattern, tc.verbs),
			"default deny, allow all": denyUnderAllowAll(tc.pattern, tc.verbs),
		} {
			p := mustLoad(t, writePolicy(t, body))
			owner := mustResource(t, tc.owner)
			verbs := []policy.Verb{policy.Delete}
			// A device is never created by a command; a deny holding delete
			// by name holds no create.
			if owner.Kind != policy.KindDevice && tc.verbs == "[write]" {
				verbs = append(verbs, policy.Create)
			}
			for _, v := range verbs {
				got := p.Decide(owner, v, nil)
				want := `deny "` + tc.pattern + `" ` + tc.verbs[:len(tc.verbs)-1] + "]"
				if got.Allowed || !got.Partial || got.Rule != want {
					t.Errorf("%s: %s %s under deny %s %s = allowed %v partial %v (%s); want denied, partial, by %s",
						name, v, tc.owner, tc.pattern, tc.verbs, got.Allowed, got.Partial, got.Rule, want)
				}
			}
		}
	}
}

// TestADenyOnAPartRefusesEveryVerbThatChangesIt — a service call changes an
// entity's state and attributes, and an edit of a file rewrites its blocks:
// a deny on either refuses the parent's check, for the deny-only service-call
// check too.
func TestADenyOnAPartRefusesEveryVerbThatChangesIt(t *testing.T) {
	for _, tc := range []struct {
		pattern, verbs, res string
		v                   policy.Verb
	}{
		{"entity:light.a/state", "[write]", "entity:light.a", policy.Control},
		{"entity:light.a/state", "[control]", "entity:light.a", policy.Control},
		{"entity:light.a/attr/brightness", "[control]", "entity:light.a", policy.Control},
		{"entity:light.a/attr", "[write]", "entity:light.a", policy.Control},
		{"entity:lock.*/state", "[write]", "entity:lock.front_door", policy.Control},
		{"file:automations.yaml/block/prod_heating", "[edit]", "file:automations.yaml", policy.Edit},
		{"file:automations.yaml/block/prod_heating", "[write]", "file:automations.yaml", policy.Edit},
	} {
		p := mustLoad(t, writePolicy(t, oneDeny(tc.pattern, tc.verbs)))
		want := `deny "` + tc.pattern + `" ` + tc.verbs[:len(tc.verbs)-1] + "]"
		if got := p.Decide(mustResource(t, tc.res), tc.v, nil); got.Allowed || !got.Partial || got.Rule != want {
			t.Errorf("%s %s under deny %s %s = allowed %v partial %v (%s); want denied, partial, by %s",
				tc.v, tc.res, tc.pattern, tc.verbs, got.Allowed, got.Partial, got.Rule, want)
		}
		if tc.v != policy.Control {
			continue
		}
		if got := p.DecideDenyOnly(mustResource(t, tc.res), tc.v); got.Allowed {
			t.Errorf("deny-only %s %s under deny %s %s = allowed (%s); want denied", tc.v, tc.res, tc.pattern, tc.verbs, got.Rule)
		}
		if got := p.DecideKindWide(policy.KindEntity, "", tc.v); got.Allowed {
			t.Errorf("kind-wide %s entity:* under deny %s %s = allowed (%s); want denied", tc.v, tc.pattern, tc.verbs, got.Rule)
		}
	}
	// What no command's control changes stays narrowed: F-1.
	p := mustLoad(t, writePolicy(t, oneDeny("entity:*/registry/labels", "[control]")))
	if got := p.Decide(mustResource(t, "entity:light.a"), policy.Control, nil); !got.Allowed || got.Partial {
		t.Errorf("control entity:light.a under deny registry/labels [control] = allowed %v partial %v (%s); want allowed whole",
			got.Allowed, got.Partial, got.Rule)
	}
}

// TestAnAllowOnAPartGrantsOnlyThePart — D-125 widens denies only: an allow on
// a part never grants a verb asked above it, and its `write` keeps D-110's
// expansion, as `policy show` lists it.
func TestAnAllowOnAPartGrantsOnlyThePart(t *testing.T) {
	p := mustLoad(t, writePolicy(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*/registry/labels": [write]
      "automation:x/config": [write]
      "entity:light.a/state": [write]
    deny:
      "entity:*/registry/entity_id": [write]
`))
	want := map[string][]policy.Verb{
		"entity:*/registry/labels":    {policy.Edit},
		"automation:x/config":         {policy.Edit},
		"entity:light.a/state":        {},
		"entity:*/registry/entity_id": {policy.Edit, policy.Create, policy.Delete},
	}
	for _, r := range p.Rules() {
		if got := r.Verbs; !slices.Equal(got, want[r.Pattern]) {
			t.Errorf("%s %s: expanded %v, want %v", r.Effect, r.Pattern, got, want[r.Pattern])
		}
	}
	for _, tc := range []struct {
		res string
		v   policy.Verb
	}{
		{"entity:light.a", policy.Delete},
		{"automation:x", policy.Delete},
		{"entity:light.a", policy.Control},
	} {
		if got := p.Decide(mustResource(t, tc.res), tc.v, nil); got.Allowed {
			t.Errorf("%s %s = allowed by %s; an allow on a part grants nothing above it", tc.v, tc.res, got.Rule)
		}
	}
}
