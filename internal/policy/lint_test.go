package policy

import (
	"errors"
	"reflect"
	"strconv"
	"testing"
)

type fakeInventory struct {
	ids  map[Kind][]string
	errs map[Kind]error
	// refs are the areas and labels by kind; reg the registry rows (none
	// when nil); regErr fails Registries.
	refs   map[Kind][]Ref
	reg    *Registries
	regErr error
}

func (f fakeInventory) IDs(k Kind) ([]string, error) {
	if err, ok := f.errs[k]; ok {
		return nil, err
	}
	if ids, ok := f.ids[k]; ok {
		return ids, nil
	}
	return nil, ErrNotEnumerated
}

func (f fakeInventory) Refs(k Kind) ([]Ref, error) {
	if err, ok := f.errs[k]; ok {
		return nil, err
	}
	if refs, ok := f.refs[k]; ok {
		return refs, nil
	}
	return nil, ErrNotEnumerated
}

func (f fakeInventory) Registries() (*Registries, error) {
	if f.regErr != nil {
		return nil, f.regErr
	}
	if f.reg == nil {
		return &Registries{}, nil
	}
	return f.reg, nil
}

func codes(rep LintReport) []string {
	out := make([]string, len(rep.Findings))
	for i, f := range rep.Findings {
		out[i] = f.Code + "@" + f.Profile + ":" + strconv.Itoa(f.Line)
	}
	return out
}

var liveFixture = fakeInventory{ids: map[Kind][]string{
	KindEntity:     {"lock.front_door", "light.sofa", "device_tracker.jan", "sensor.t"},
	KindAutomation: {"pg_heating", "1700000000000"},
	KindSystem:     {"logs", "wireguard", "reload", "check_config", "cache", "template"},
}}

// A typo in a deny is a silent hole otherwise: lint must name it.
func TestLintReportsAMisspeltDeny(t *testing.T) {
	p := mustLoad(t, "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    deny:\n      \"entity:lokc.*\": [read, write]\n")
	rep := Lint(p, liveFixture)
	want := []Finding{{
		Code: CodeNoMatch, Profile: "agent", Rule: `deny "entity:lokc.*" [read write]`, Line: 7,
		Message: "matches no entity on this instance",
	}}
	if !reflect.DeepEqual(rep.Findings, want) {
		t.Errorf("findings = %+v, want %+v", rep.Findings, want)
	}
	if len(rep.Unchecked) != 0 {
		t.Errorf("unchecked = %+v, want none", rep.Unchecked)
	}
}

func TestLintChecksEveryProfileNotOnlyTheActiveOne(t *testing.T) {
	p := mustLoad(t, "version: 1\nactive: a\nprofiles:\n  a: {default: deny}\n  b:\n    default: deny\n    deny:\n      \"automation:pg_heatnig\": [edit]\n")
	if got := codes(Lint(p, liveFixture)); !reflect.DeepEqual(got, []string{"no-match@b:8"}) {
		t.Errorf("findings = %v, want the inactive profile's typo", got)
	}
}

func TestLintFindings(t *testing.T) {
	content := "version: 1\nprofiles:\n" +
		"  open:\n" + // line 3: no default: deny
		"    allow:\n" +
		"      \"entity:light.*\": [control]\n" + // 5: redundant-allow
		"  closed:\n" +
		"    default: deny\n" +
		"    allow:\n" +
		"      \"entity:lock.*\": [read, control]\n" + // 9: unreachable for control only
		"      \"*\": [read]\n" + // 10: never no-match
		"    deny:\n" +
		"      \"entity:lock.*\": [control]\n" // 12
	p := mustLoad(t, content)
	rep := Lint(p, liveFixture)
	got := codes(rep)
	want := []string{
		"implicit-allow-all@:0",
		"redundant-allow@open:5",
		"unreachable@closed:9",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("findings = %v, want %v (%+v)", got, want, rep.Findings)
	}
	for _, f := range rep.Findings {
		if f.Code == CodeUnreachable && f.Message != "never decides control: every entity it matches is denied control by a deny of this profile" {
			t.Errorf("unreachable message = %q", f.Message)
		}
	}
}

func TestLintUnchecked(t *testing.T) {
	content := "version: 1\nprofiles:\n  default:\n    deny:\n      \"file:**\": [write]\n      \"entity:lock.*\": [write]\n      \"device:x\": [edit]\n      \"system:logs\": [read]\n"
	p := mustLoad(t, content)

	// No live instance: every named kind is unchecked, in Kinds() order.
	rep := Lint(p, nil)
	want := []Unchecked{
		{Kind: KindEntity, Reason: "no live instance"},
		{Kind: KindDevice, Reason: "no live instance"},
		{Kind: KindFile, Reason: "no live instance"},
		{Kind: KindSystem, Reason: "no live instance"},
	}
	if !reflect.DeepEqual(rep.Unchecked, want) {
		t.Errorf("unchecked (nil inventory) = %+v, want %+v", rep.Unchecked, want)
	}

	// An inventory: not enumerated, an error, and enumerated kinds.
	inv := fakeInventory{
		ids:  map[Kind][]string{KindEntity: {"lock.a"}, KindSystem: liveFixture.ids[KindSystem]},
		errs: map[Kind]error{KindDevice: errors.New("device registry: connection refused")},
	}
	rep = Lint(p, inv)
	want = []Unchecked{
		{Kind: KindDevice, Reason: "device registry: connection refused"},
		{Kind: KindFile, Reason: "not enumerated", Profile: "default", Rule: `deny "file:**" [write]`, Line: 5},
	}
	if !reflect.DeepEqual(rep.Unchecked, want) {
		t.Errorf("unchecked = %+v, want %+v", rep.Unchecked, want)
	}
	if len(rep.Findings) != 0 {
		t.Errorf("findings = %+v, want none", rep.Findings)
	}
}

// TestLintFileIsUncheckedPerRule (D-128): a kind the inventory does not list
// is unchecked once per rule naming it — with the rule's profile, text and
// line, like a free sub segment — and has no entry of its own.
func TestLintFileIsUncheckedPerRule(t *testing.T) {
	p := mustLoad(t, agent("    default: deny\n    allow:\n      \"file:automations.yaml\": [edit]\n"+
		"    deny:\n      \"file:secrets.yaml\": [read, write]\n  other:\n    deny:\n      \"file:**\": [write]\n"))
	rep := Lint(p, fakeInventory{})
	want := []Unchecked{
		{Kind: KindFile, Reason: ReasonNotEnumerated, Profile: "agent", Rule: `allow "file:automations.yaml" [edit]`, Line: 7},
		{Kind: KindFile, Reason: ReasonNotEnumerated, Profile: "agent", Rule: `deny "file:secrets.yaml" [read write]`, Line: 9},
		{Kind: KindFile, Reason: ReasonNotEnumerated, Profile: "other", Rule: `deny "file:**" [write]`, Line: 12},
	}
	if !reflect.DeepEqual(rep.Unchecked, want) {
		t.Errorf("unchecked =\n%+v\nwant\n%+v", rep.Unchecked, want)
	}
}

// The order of findings is (line, code, profile, rule) — never map order.
func TestLintOrderIsTotal(t *testing.T) {
	content := "version: 1\nactive: z\nprofiles:\n" +
		"  z:\n    deny:\n      \"entity:x.nope1\": [read]\n" +
		"  a:\n    deny:\n      \"entity:x.nope2\": [read]\n"
	p := mustLoad(t, content)
	for range 20 {
		if got := codes(Lint(p, liveFixture)); !reflect.DeepEqual(got, []string{"no-match@z:6", "no-match@a:9"}) {
			t.Fatalf("findings = %v", got)
		}
	}
}
