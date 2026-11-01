package policy_test

// Blind spec tests for B-1 (live test 2026-11-01, F-1), written from the
// contract only — docs/decisions.md D-110 and the manual's "Verbs" table and
// "Decision order" step 4 — without reading Decide, DecideKindWide or the
// evaluation code.
//
// `deny "entity:*/registry/entity_id": [write]`, the obvious "no renames" rule,
// denied `control` on every entity: `write` expanded to control on a registry
// field, where no command ever asks for it, and the partial step then made
// every entity partly denied. D-110: a verb applies at an address only if some
// command asks for it there; `write` expands, per rule, to the write verbs that
// apply at the rule's pattern (its own address or one below it); a verb written
// by name is kept as written; and the partial step counts a descendant deny for
// a verb only if that verb applies at the deny's own address — in Decide,
// DecideKindWide and DecideKindWideDenyOnly alike.

import (
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
)

var writeVerbs = []policy.Verb{policy.Control, policy.Edit, policy.Create, policy.Delete}

// applicabilityRows is the manual's applicability table (D-110), one sample
// address per row and per sub-resource it lists, with the write verbs that
// apply exactly there. The "every other address" row is the entries with no
// verbs.
var applicabilityRows = []struct {
	res string
	at  []policy.Verb
}{
	{"entity:light.x", []policy.Verb{policy.Control, policy.Create, policy.Delete}},
	{"entity:light.x/registry/entity_id", []policy.Verb{policy.Edit}},
	{"entity:light.x/registry/labels", []policy.Verb{policy.Edit}},
	{"entity:light.x/registry/area_id", []policy.Verb{policy.Edit}},
	{"entity:light.x/state", nil},
	{"entity:light.x/attr/brightness", nil},
	{"entity:light.x/history", nil},
	{"entity:light.x/stats", nil},
	{"device:dev1", []policy.Verb{policy.Delete}},
	{"device:dev1/registry/area_id", []policy.Verb{policy.Edit}},
	{"area:hall", []policy.Verb{policy.Create, policy.Edit, policy.Delete}},
	{"floor:ground", []policy.Verb{policy.Create, policy.Delete}},
	{"label:night", []policy.Verb{policy.Create, policy.Delete}},
	{"automation:a1", []policy.Verb{policy.Control, policy.Create, policy.Delete}},
	{"automation:a1/config", []policy.Verb{policy.Edit}},
	{"automation:a1/trace", nil},
	{"template:t1", []policy.Verb{policy.Control, policy.Create, policy.Delete}},
	{"template:t1/config", []policy.Verb{policy.Edit}},
	{"helper:input_boolean/h1", []policy.Verb{policy.Control, policy.Create, policy.Delete}},
	{"helper:input_boolean/h1/config", nil},
	{"script:s1", []policy.Verb{policy.Control, policy.Create, policy.Delete}},
	{"script:s1/config", []policy.Verb{policy.Edit}},
	{"script:s1/run", []policy.Verb{policy.Control}},
	{"script:s1/trace", nil},
	{"dashboard:main", []policy.Verb{policy.Create, policy.Edit, policy.Delete}},
	{"dashboard:main/views/v1", []policy.Verb{policy.Create, policy.Edit, policy.Delete}},
	{"dashboard:main/resources", nil},
	{"config_entry:e1", []policy.Verb{policy.Create, policy.Edit, policy.Delete}},
	{"config_entry:e1/options", []policy.Verb{policy.Edit}},
	{"config_entry:e1/flow", nil},
	{"file:packages/x.yaml", []policy.Verb{policy.Edit}},
	{"file:packages/x.yaml/block/b1", nil},
	{"service:light.turn_on", []policy.Verb{policy.Control}},
	{"system:wireguard", []policy.Verb{policy.Control, policy.Edit}},
	{"system:cache", []policy.Verb{policy.Edit, policy.Delete}},
	{"system:logs", nil},
	{"system:check_config", nil},
	{"system:reload", nil},
	{"system:reload/light", nil},
}

// appliesAtOrBelow is the union of the table's verbs at res and at every row
// below it — what `write` on a pattern that is exactly res expands to.
func appliesAtOrBelow(res string) []policy.Verb {
	var out []policy.Verb
	for _, row := range applicabilityRows {
		if row.res == res || strings.HasPrefix(row.res, res+"/") {
			for _, v := range row.at {
				if !slices.Contains(out, v) {
					out = append(out, v)
				}
			}
		}
	}
	return out
}

// parentOf is the row's resource without its sub-resource ("" when it has
// none). A system id is not a sub-resource: system:reload/light's parent is
// system:reload.
func parentOf(t *testing.T, res string) string {
	t.Helper()
	r := mustResource(t, res)
	if r.Sub == "" {
		return ""
	}
	return policy.Resource{Kind: r.Kind, ID: r.ID}.String()
}

func oneDeny(pattern, verbs string) string {
	return "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"" + pattern + "\": " + verbs + "\n"
}

// TestTheLiveTestRuleNoLongerDeniesControl is F-1 verbatim: the "no renames"
// rule leaves switching alone and still refuses the rename — at the entity, at
// every entity (kind-wide), and under the shape the live test used (a
// default-deny profile that allows entities).
func TestTheLiveTestRuleNoLongerDeniesControl(t *testing.T) {
	for name, body := range map[string]string{
		"profile without default": oneDeny("entity:*/registry/entity_id", "[write]"),
		"default deny with an allow": `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*": [write]
    deny:
      "entity:*/registry/entity_id": [write]
`,
	} {
		t.Run(name, func(t *testing.T) {
			p := mustLoad(t, writePolicy(t, body))
			if v := p.Decide(mustResource(t, "entity:light.x"), policy.Control, nil); !v.Allowed || v.Partial {
				t.Errorf("control entity:light.x = allowed %v partial %v (%s, line %d); want allowed — control does not apply at registry/entity_id",
					v.Allowed, v.Partial, v.Rule, v.Line)
			}
			if v := p.DecideKindWide(policy.KindEntity, "", policy.Control); !v.Allowed || v.Partial {
				t.Errorf("control entity:* (kind-wide) = allowed %v partial %v (%s); want allowed", v.Allowed, v.Partial, v.Rule)
			}
			if v := p.Decide(mustResource(t, "entity:light.x/registry/entity_id"), policy.Edit, nil); v.Allowed || v.Rule != `deny "entity:*/registry/entity_id" [write]` {
				t.Errorf("edit entity:light.x/registry/entity_id = allowed %v by %q; want denied by the rule", v.Allowed, v.Rule)
			}
			// Edit on the entity as a whole is still partly denied: edit applies
			// where the deny sits.
			if v := p.Decide(mustResource(t, "entity:light.x"), policy.Edit, nil); v.Allowed || !v.Partial {
				t.Errorf("edit entity:light.x = allowed %v partial %v; want denied, partial — edit applies at registry/entity_id", v.Allowed, v.Partial)
			}
		})
	}
}

// TestWriteExpandsToTheVerbsThatApplyAtTheRulesAddress — D-110 (1): for every
// row of the table, `deny "<address>": [write]` denies exactly the write verbs
// that apply at that address or below it, and nothing else; a rule on a
// no-write address denies no write verb at all.
func TestWriteExpandsToTheVerbsThatApplyAtTheRulesAddress(t *testing.T) {
	for _, row := range applicabilityRows {
		t.Run(row.res, func(t *testing.T) {
			p := mustLoad(t, writePolicy(t, oneDeny(row.res, "[write]")))
			want := appliesAtOrBelow(row.res)
			r := mustResource(t, row.res)
			for _, v := range writeVerbs {
				got := p.Decide(r, v, nil)
				if wantDenied := slices.Contains(want, v); got.Allowed == wantDenied {
					t.Errorf("%s %s under deny [write]: allowed %v (%s, line %d); want denied=%v (write there means %v)",
						v, row.res, got.Allowed, got.Rule, got.Line, wantDenied, want)
				}
			}
			if got := p.Decide(r, policy.Read, nil); !got.Allowed {
				t.Errorf("read %s denied by a [write] deny (%s)", row.res, got.Rule)
			}
		})
	}
}

// TestPartialCountsOnlyDeniesWhoseVerbAppliesWhereTheDenySits — D-110 (2) and
// (3): a deny on a sub-resource, with every write verb written out by name,
// still matches a direct check of exactly that address for each of them (kept
// as written), but makes its parent partial only for the verbs that apply at
// the deny's own address.
func TestPartialCountsOnlyDeniesWhoseVerbAppliesWhereTheDenySits(t *testing.T) {
	for _, row := range applicabilityRows {
		parent := parentOf(t, row.res)
		if parent == "" {
			continue
		}
		t.Run(row.res, func(t *testing.T) {
			p := mustLoad(t, writePolicy(t, oneDeny(row.res, "[control, edit, create, delete]")))
			leaf, top := mustResource(t, row.res), mustResource(t, parent)
			for _, v := range writeVerbs {
				if got := p.Decide(leaf, v, nil); got.Allowed {
					t.Errorf("%s %s = allowed (%s); a verb written by name still matches a direct check of its address", v, row.res, got.Rule)
				}
				got := p.Decide(top, v, nil)
				if applies := slices.Contains(row.at, v); got.Allowed == applies || got.Partial != applies {
					t.Errorf("%s %s with a deny on %s: allowed %v partial %v (%s); want partial-denied=%v (%s applies at %s: %v)",
						v, parent, row.res, got.Allowed, got.Partial, got.Rule, applies, v, row.res, applies)
				}
			}
		})
	}
}

// TestKindWideChecksIgnoreInapplicableDescendantDenies — D-110 (3) names all
// three evaluators. A deny on script:*/config holding [write] holds edit only,
// so control on every script — a plain kind-wide check and the deny-only one a
// service call makes (D-107) — stays allowed.
func TestKindWideChecksIgnoreInapplicableDescendantDenies(t *testing.T) {
	p := mustLoad(t, writePolicy(t, oneDeny("script:*/config", "[write]")))
	if v := p.DecideKindWide(policy.KindScript, "", policy.Control); !v.Allowed {
		t.Errorf("control script:* (kind-wide) denied by %s (line %d); control does not apply at /config", v.Rule, v.Line)
	}
	if v := p.DecideKindWideDenyOnly(policy.KindScript, "", policy.Control); !v.Allowed {
		t.Errorf("deny-only control script:* denied by %s (line %d); control does not apply at /config", v.Rule, v.Line)
	}
	if v := p.DecideDenyOnly(mustResource(t, "script:s1"), policy.Control); !v.Allowed {
		t.Errorf("deny-only control script:s1 denied by %s (line %d)", v.Rule, v.Line)
	}
	if v := p.DecideKindWide(policy.KindScript, "", policy.Edit); v.Allowed {
		t.Errorf("edit script:* (kind-wide) allowed; edit applies at script:<id>/config, so it is partly denied")
	}
}

// TestADenyOnANoWritePartRefusesNoParentWrite — the manual's two examples of
// what narrows: an explicit `deny "entity:lock.*/state" [control]` refuses no
// service call (control is checked on the entity itself), and a block deny
// no longer refuses an edit of its file.
func TestADenyOnANoWritePartRefusesNoParentWrite(t *testing.T) {
	p := mustLoad(t, writePolicy(t, oneDeny("entity:lock.*/state", "[control]")))
	if v := p.Decide(mustResource(t, "entity:lock.front_door"), policy.Control, nil); !v.Allowed {
		t.Errorf("control entity:lock.front_door denied by %s; control does not apply at /state", v.Rule)
	}
	if v := p.Decide(mustResource(t, "entity:lock.front_door/state"), policy.Control, nil); v.Allowed {
		t.Errorf("control entity:lock.front_door/state allowed; a verb written by name is kept as written")
	}

	p = mustLoad(t, writePolicy(t, oneDeny("file:x.yaml/block/b", "[edit]")))
	if v := p.Decide(mustResource(t, "file:x.yaml"), policy.Edit, nil); !v.Allowed {
		t.Errorf("edit file:x.yaml denied by %s; no command writes a block on its own, so the block deny decides nothing for the file", v.Rule)
	}
}
