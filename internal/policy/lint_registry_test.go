package policy

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// house is a registry fixture for the checks that look below an id:
//
//   - automation.morning: config id 1700000000000 (state `id` attribute);
//   - script.kino: registry unique_id kino_key (D-84);
//   - input_boolean.guest: helper platform, unique_id guest_uid (renamed, so
//     the object id differs);
//   - sensor.tpl: template platform, unique_id blk-tpl_temp (a block's own
//     unique_id prefixes it);
//   - light.hall: no own area, on device dev1 (area hall); light.kitchen: own
//     area kitchen, label energy.
func house() fakeInventory {
	return fakeInventory{
		ids: map[Kind][]string{
			KindEntity: {
				"automation.morning", "script.kino", "input_boolean.guest", "sensor.tpl",
				"light.hall", "light.kitchen",
			},
			KindAutomation: {"1700000000000"},
			KindScript:     {"kino"},
			KindHelper:     {"input_boolean/guest"},
			KindTemplate:   {"tpl_temp"},
			KindDevice:     {"dev1"},
		},
		refs: map[Kind][]Ref{
			KindArea:  {{ID: "hall", Name: "Hall"}, {ID: "kitchen", Name: "Kitchen"}},
			KindLabel: {{ID: "energy", Name: "Energy"}},
		},
		reg: &Registries{
			Entities: []RegistryEntity{
				{EntityID: "automation.morning", Platform: "automation", UniqueID: "1700000000000"},
				{EntityID: "script.kino", Platform: "script", UniqueID: "kino_key"},
				{EntityID: "input_boolean.guest", Platform: "input_boolean", UniqueID: "guest_uid"},
				{EntityID: "sensor.tpl", Platform: "template", UniqueID: "blk-tpl_temp"},
				{EntityID: "light.hall", DeviceID: "dev1"},
				{EntityID: "light.kitchen", DeviceID: "dev1", AreaID: "kitchen", Labels: []string{"energy"}},
			},
			Devices:       []RegistryDevice{{ID: "dev1", AreaID: "hall"}},
			AutomationIDs: map[string]string{"automation.morning": "1700000000000"},
		},
	}
}

func findingsOf(rep LintReport, code string) []Finding {
	var out []Finding
	for _, f := range rep.Findings {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

func agent(rules string) string {
	return "version: 1\nactive: agent\nprofiles:\n  agent:\n" + rules
}

// D-115: each of the four kinds, both directions, resolved the way the
// service-call check resolves typed addresses (D-107).
func TestLintHalfProtectedEveryKind(t *testing.T) {
	for _, tc := range []struct {
		name, deny string
		want       string // message prefix up to the counterpart; "" = no finding
	}{
		{"automation config", `"automation:1700000000000": [write]`,
			`deny "automation:1700000000000" [write] protects the automation's config, but its entity automation.morning can still be changed (edit entity:automation.morning/registry/name is allowed) — to protect both, also deny "entity:automation.morning": [write]`},
		{"automation config sub", `"automation:1700000000000/config": [edit]`,
			`deny "automation:1700000000000/config" [edit] protects the automation's config, but its entity automation.morning`},
		{"script by its object id", `"script:kino": [delete]`, ""}, // kino is the object id; the script's key is kino_key
		{"script by its key", `"script:kino_key": [delete]`, ""},  // not live per script ls: no-match, nothing to resolve
		{"helper", `"helper:input_boolean/guest": [delete]`,
			`deny "helper:input_boolean/guest" [delete] protects the helper's config, but its entity input_boolean.guest can still be changed`},
		{"template suffix", `"template:tpl_temp": [write]`,
			`deny "template:tpl_temp" [write] protects the template's config, but its entity sensor.tpl can still be changed`},
		{"switching only", `"automation:1700000000000": [control]`, ""},
		{"automation entity", `"entity:automation.morning": [write]`,
			`deny "entity:automation.morning" [write] protects the entity, but its config automation:1700000000000 can still be changed (edit automation:1700000000000/config is allowed) — to protect both, also deny "automation:1700000000000": [write]`},
		{"script entity", `"entity:script.kino": [edit]`,
			`deny "entity:script.kino" [edit] protects the entity, but its config script:kino_key can still be changed (edit script:kino_key/config is allowed)`},
		{"helper entity", `"entity:input_boolean.guest": [write]`,
			`deny "entity:input_boolean.guest" [write] protects the entity, but its config helper:input_boolean/guest_uid can still be changed (delete helper:input_boolean/guest_uid is allowed)`},
		{"template entity", `"entity:sensor.tpl": [write]`,
			`deny "entity:sensor.tpl" [write] protects the entity, but its config template:blk-tpl_temp can still be changed (edit template:blk-tpl_temp/config is allowed)`},
		{"unowned entity", `"entity:light.hall": [write]`, ""},
		{"entity without edit", `"entity:automation.morning": [control, delete]`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := Lint(mustLoad(t, agent("    deny:\n      "+tc.deny+"\n")), house())
			got := findingsOf(rep, CodeHalfProtected)
			if tc.want == "" {
				if len(got) != 0 {
					t.Errorf("half-protected %+v, want none", got)
				}
				return
			}
			if len(got) != 1 || got[0].Line != 6 || !strings.HasPrefix(got[0].Message, tc.want) {
				t.Errorf("half-protected %+v, want one on line 6 starting %q", got, tc.want)
			}
		})
	}
}

// D-115: a counterpart protected by another deny — on the entity, on any one
// form of a helper's or template's id — is no finding; the helper's object-id
// form counts like its unique_id.
func TestLintHalfProtectedCounterpartDenied(t *testing.T) {
	for name, rules := range map[string]string{
		"automation both":      `      "automation:1700000000000": [write]` + "\n" + `      "entity:automation.morning": [write]`,
		"helper by object id":  `      "entity:input_boolean.guest": [write]` + "\n" + `      "helper:input_boolean/guest": [delete]`,
		"template by suffix":   `      "entity:sensor.tpl": [write]` + "\n" + `      "template:tpl_temp": [write]`,
		"entity fields denied": `      "automation:1700000000000": [delete]` + "\n" + `      "entity:automation.morning/registry": [edit]`,
	} {
		t.Run(name, func(t *testing.T) {
			rep := Lint(mustLoad(t, agent("    deny:\n"+rules+"\n")), house())
			if got := findingsOf(rep, CodeHalfProtected); len(got) != 0 {
				t.Errorf("half-protected %+v, want none", got)
			}
		})
	}
}

// D-115: one finding per rule, the first counterpart named and the rest
// counted; under default: deny nothing is editable, so nothing is half.
func TestLintHalfProtectedCountsAndDefaultDeny(t *testing.T) {
	inv := house()
	inv.ids[KindAutomation] = []string{"1700000000000", "1700000000001"}
	inv.ids[KindEntity] = append(inv.ids[KindEntity], "automation.evening")
	inv.reg.AutomationIDs = map[string]string{"automation.morning": "1700000000000", "automation.evening": "1700000000001"}
	rep := Lint(mustLoad(t, agent("    deny:\n      \"automation:17*\": [write]\n")), inv)
	got := findingsOf(rep, CodeHalfProtected)
	if len(got) != 1 || !strings.Contains(got[0].Message, "its entity automation.morning") || !strings.HasSuffix(got[0].Message, " (1 more like it)") {
		t.Errorf("half-protected %+v, want one naming automation.morning (its config id sorts first) and 1 more", got)
	}
	rep = Lint(mustLoad(t, agent("    default: deny\n    deny:\n      \"automation:17*\": [write]\n")), inv)
	if got := findingsOf(rep, CodeHalfProtected); len(got) != 0 {
		t.Errorf("half-protected under default: deny %+v, want none", got)
	}
}

// D-115: registries lint could not read list the rule unchecked —
// `counterpart not resolved` — unless its own kind is unchecked already.
func TestLintHalfProtectedUnresolved(t *testing.T) {
	inv := house()
	inv.regErr = errors.New("entity registry: connection refused")
	rep := Lint(mustLoad(t, agent("    deny:\n      \"automation:1700000000000\": [write]\n      \"device:dev1\": [delete]\n")), inv)
	want := []Unchecked{{Kind: KindAutomation, Reason: ReasonCounterpartNotResolved, Profile: "agent",
		Rule: `deny "automation:1700000000000" [write]`, Line: 6}}
	if !reflect.DeepEqual(rep.Unchecked, want) {
		t.Errorf("unchecked %+v, want %+v", rep.Unchecked, want)
	}

	inv = house()
	inv.regErr = errors.New("refused")
	inv.errs = map[Kind]error{KindAutomation: errors.New("refused")}
	rep = Lint(mustLoad(t, agent("    deny:\n      \"automation:1700000000000\": [write]\n")), inv)
	want = []Unchecked{{Kind: KindAutomation, Reason: "refused"}}
	if !reflect.DeepEqual(rep.Unchecked, want) {
		t.Errorf("unchecked %+v, want only the kind's own entry %+v", rep.Unchecked, want)
	}
}

// D-116: a partial list (helper without the companion) lists the kind
// unchecked, reports no rule as no-match or unreachable against it, and still
// resolves a real match for half-protected.
func TestLintPartialInventory(t *testing.T) {
	inv := house()
	inv.errs = map[Kind]error{}
	partial := partialInventory{fakeInventory: inv, kind: KindHelper, err: errors.New("companion: unreachable")}
	rep := Lint(mustLoad(t, agent(`    default: deny
    allow:
      "helper:input_boolean/*": [delete]
    deny:
      "helper:input_boolean/guest": [delete]
      "helper:input_boolean/yaml_only": [delete]
      "entity:input_boolean.guest/registry": [edit]
`)), partial)
	if got := findingsOf(rep, CodeNoMatch); len(got) != 0 {
		t.Errorf("no-match against a partial list: %+v", got)
	}
	if got := findingsOf(rep, CodeUnreachable); len(got) != 0 {
		t.Errorf("unreachable against a partial list: %+v", got)
	}
	want := []Unchecked{{Kind: KindHelper, Reason: "companion: unreachable"}}
	if !reflect.DeepEqual(rep.Unchecked, want) {
		t.Errorf("unchecked %+v, want %+v", rep.Unchecked, want)
	}

	rep = Lint(mustLoad(t, agent("    deny:\n      \"helper:input_boolean/guest\": [delete]\n")), partial)
	if got := findingsOf(rep, CodeHalfProtected); len(got) != 1 {
		t.Errorf("half-protected from a partial list %+v, want the real match's finding", got)
	}
}

type partialInventory struct {
	fakeInventory

	kind Kind
	err  error
}

func (p partialInventory) IDs(k Kind) ([]string, error) {
	ids, err := p.fakeInventory.IDs(k)
	if k == p.kind && err == nil {
		return ids, &PartialError{Err: p.err}
	}
	return ids, err
}

// D-113: values by id, by name, by glob and `*` are checked; a miss is
// no-match on the where: line; an unreadable registry lists its kind once.
func TestLintWhereValues(t *testing.T) {
	body := agent(`    default: deny
    allow:
      "entity:light.*": [control]
      "device:*": [delete]
    where:
      "entity:light.*": { area: [hall, Kitch*, nowhere], label: "*" }
      "device:*": { label: [Energy, enrgy] }
`)
	rep := Lint(mustLoad(t, body), house())
	want := []Finding{
		{Code: CodeNoMatch, Profile: "agent", Rule: `allow "entity:light.*" [control] where area=hall|Kitch*|nowhere label=*`, Line: 10,
			Message: `where: area "nowhere" matches no area on this instance (by id or name)`},
		{Code: CodeNoMatch, Profile: "agent", Rule: `allow "device:*" [delete] where label=Energy|enrgy`, Line: 11,
			Message: `where: label "enrgy" matches no label on this instance (by id or name)`},
	}
	if got := findingsOf(rep, CodeNoMatch); !reflect.DeepEqual(got, want) {
		t.Errorf("no-match %+v\nwant %+v", got, want)
	}

	inv := house()
	inv.errs = map[Kind]error{KindArea: errors.New("area registry: timeout")}
	rep = Lint(mustLoad(t, body), inv)
	if got := findingsOf(rep, CodeNoMatch); len(got) != 1 || got[0].Line != 11 {
		t.Errorf("no-match %+v, want only the label miss", got)
	}
	wantU := []Unchecked{{Kind: KindArea, Reason: "area registry: timeout"}}
	if !reflect.DeepEqual(rep.Unchecked, wantU) {
		t.Errorf("unchecked %+v, want %+v", rep.Unchecked, wantU)
	}
}

// D-114: a self-grant needs the narrowed allow's matches in the registry and
// an allow (never the narrowed one itself) that grants the edit; an
// unreadable registry lists the narrowed allow unchecked.
func TestLintSelfGrantEdges(t *testing.T) {
	// The narrowed allow grants edit of the labels itself: only where its
	// label already holds, so the agent gains nothing.
	rep := Lint(mustLoad(t, agent(`    default: deny
    allow:
      "entity:light.*": [control, edit]
    where:
      "entity:light.*": { label: energy }
`)), house())
	if got := findingsOf(rep, CodeSelfGrant); len(got) != 0 {
		t.Errorf("self-grant by the narrowed allow itself: %+v", got)
	}

	// A granting allow narrowed by its own where: counts only where it holds.
	rep = Lint(mustLoad(t, agent(`    default: deny
    allow:
      "entity:light.*": [control]
      "entity:*": [edit]
    where:
      "entity:light.*": { label: energy }
      "entity:*": { area: kitchen }
`)), house())
	got := findingsOf(rep, CodeSelfGrant)
	if len(got) != 1 || !strings.Contains(got[0].Message, "lets it change labels on 1 of the entities this allow matches (e.g. light.kitchen)") {
		t.Errorf("self-grant %+v, want one counting light.kitchen only", got)
	}

	inv := house()
	inv.regErr = errors.New("refused")
	rep = Lint(mustLoad(t, agent(`    default: deny
    allow:
      "entity:light.*": [control]
      "entity:*/registry/labels": [edit]
    where:
      "entity:light.*": { label: energy }
`)), inv)
	want := []Unchecked{{Kind: KindEntity, Reason: ReasonSelfGrantNotResolved, Profile: "agent",
		Rule: `allow "entity:light.*" [control] where label=energy`, Line: 7}}
	if !reflect.DeepEqual(rep.Unchecked, want) {
		t.Errorf("unchecked %+v, want %+v", rep.Unchecked, want)
	}
}

// D-111: control by name on an automation, helper or template address is
// no-effect; `write`, a sub-resource, a script and a deny are not.
func TestLintNoEffect(t *testing.T) {
	rep := Lint(mustLoad(t, agent(`    default: deny
    allow:
      "automation:a": [control]
      "helper:input_*/x": [read, control]
      "template:t": [control]
      "automation:b": [write]
      "automation:c/config": [edit]
      "script:s": [control]
    deny:
      "automation:d": [control]
`)), nil)
	lines := make([]int, 0, 3)
	for _, f := range findingsOf(rep, CodeNoEffect) {
		lines = append(lines, f.Line)
	}
	if !reflect.DeepEqual(lines, []int{7, 8, 9}) {
		t.Errorf("no-effect on lines %v, want [7 8 9]", lines)
	}
	if f := findingsOf(rep, CodeNoEffect)[1]; !strings.Contains(f.Message, `allow "entity:*.*": [control]`) {
		t.Errorf("a wildcard helper domain suggests %q", f.Message)
	}
}
