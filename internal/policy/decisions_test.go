package policy

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

// Implementation tests for D-63..D-68 and the implementation-level fixes of
// the second phase-1 review. The binding spec tests live in spec_test.go and
// the conformance vectors; these pin the implementation's own reading.

const profileHead = "version: 1\nactive: p\nprofiles:\n  p:\n    default: deny\n"

// --- D-67: file ids ---------------------------------------------------------

func TestFileIDsRefuseDotSegmentsAndAnAmbiguousBlock(t *testing.T) {
	refused := map[string]string{
		"file:./x.yaml":             `"." segment`,
		"file:a/../x.yaml":          `".." segment`,
		"file:..":                   `".." segment`,
		"file:a/block":              "ambiguous with the block sub-resource",
		"file:a/block/b/c.yaml":     "ambiguous with the block sub-resource",
		"file:block/x":              "ambiguous with the block sub-resource",
		"file:a/Block/x":            "ambiguous with the block sub-resource",
		"file:BLOCK.yaml/block/x":   "", // a segment merely containing the word is fine
		"file:a.yaml/block/block":   "ambiguous with the block sub-resource",
		"file:pkgs/BLOCK/x.yaml":    "ambiguous with the block sub-resource",
		"file:a.yaml/block/climate": "",
		"file:a/b/c.yaml":           "",
	}
	for addr, want := range refused {
		_, err := ParseResource(addr)
		switch {
		case want == "" && err != nil:
			t.Errorf("ParseResource(%q) = %v, want valid", addr, err)
		case want != "" && (err == nil || !strings.Contains(err.Error(), want)):
			t.Errorf("ParseResource(%q) = %v, want an error containing %q", addr, err, want)
		}
	}
	r := mustResource(t, "file:a.yaml/block/climate")
	if r.ID != "a.yaml" || r.Sub != "block/climate" {
		t.Errorf("file:a.yaml/block/climate = %+v", r)
	}

	// The same addresses are broken patterns, each naming its line.
	ps := loadProblems(t, profileHead+"    deny:\n      \"file:./secrets.yaml\": [read]\n      \"file:**/../x\": [read]\n      \"file:a/block\": [read]\n")
	for line, sub := range map[int]string{7: `"." segment`, 8: `".." segment`, 9: "ambiguous"} {
		if !hasProblem(ps, line, sub) {
			t.Errorf("no problem at line %d containing %q: %+v", line, sub, ps)
		}
	}
}

// --- D-68: the id charset ---------------------------------------------------

func TestIDCharsetPerKind(t *testing.T) {
	restricted := []string{"entity:lock.front_door", "service:lock.unlock", "helper:input_boolean/guest", "area:wohnzimmer", "floor:eg", "label:agent_ok"}
	bad := map[string]string{" ": "U+0020", "\t": "U+0009", "-": `'-'`, "ö": `'ö'`, "\u00a0": "U+00A0"}
	for _, ok := range restricted {
		if _, err := ParseResource(ok); err != nil {
			t.Errorf("ParseResource(%q) = %v", ok, err)
		}
		for ch, named := range bad {
			addr := ok + ch
			if strings.HasPrefix(ok, "helper:") {
				addr = "helper:input_boolean/gu" + ch + "est"
			}
			_, err := ParseResource(addr)
			if err == nil || !strings.Contains(err.Error(), named) || !strings.Contains(err.Error(), "D-68") {
				t.Errorf("ParseResource(%q) = %v, want a D-68 refusal naming %s", addr, err, named)
			}
		}
	}
	// Letters in any case (D-2).
	if _, err := ParseResource("entity:Lock.Front_Door"); err != nil {
		t.Errorf("upper case refused: %v", err)
	}
	// Every other kind: only white space and control characters are refused.
	for _, ok := range []string{"automation:pg-heating.v2", "dashboard:dashboard-main", "file:packages/küche.yaml", "template:ä-1", "config_entry:01J-X"} {
		if _, err := ParseResource(ok); err != nil {
			t.Errorf("ParseResource(%q) = %v, want valid", ok, err)
		}
	}
	for _, addr := range []string{"automation:pg heating", "file:a /b.yaml", "dashboard:x\u00a0", "template:a\x01", "script:a\u2028"} {
		if _, err := ParseResource(addr); err == nil || !strings.Contains(err.Error(), "D-68") {
			t.Errorf("ParseResource(%q) = %v, want a D-68 refusal", addr, err)
		}
	}
	// Free sub segments are not restricted by the charset.
	if _, err := ParseResource("entity:light.x/attr/friendly-name"); err != nil {
		t.Errorf("a free attr segment was charset-checked: %v", err)
	}
	// Patterns: wildcards are fine, the rest is a broken file at its line.
	ps := loadProblems(t, profileHead+"    deny:\n      \"entity:lock.* \": [read]\n      \"entity:lоck.*\": [read]\n      \"area:wohn-*\": [read]\n      \"entity:lock.?*\": [read]\n")
	for line, sub := range map[int]string{7: "U+0020", 8: "U+043E", 9: `'-'`} {
		if !hasProblem(ps, line, sub) {
			t.Errorf("no problem at line %d naming %s: %+v", line, sub, ps)
		}
	}
	if hasProblem(ps, 10, "") {
		t.Errorf("a wildcard in a restricted id was refused: %+v", ps)
	}
}

// --- D-52: line, then column ------------------------------------------------

func TestSameLineRulesBreakTiesByColumn(t *testing.T) {
	p := mustLoad(t, profileHead+
		"    deny: {\"entity:lock.*\": [write], \"entity:*\": [control]}\n"+
		"    allow: {\"entity:z*\": [read], \"entity:a*\": [read], \"entity:*\": [read]}\n")
	v := p.Decide(mustResource(t, "entity:lock.front_door"), Control, nil)
	if v.Rule != `deny "entity:lock.*" [write]` {
		t.Errorf("rule named = %q, want the leftmost of line 6", v.Rule)
	}
	got := make([]string, 0, len(p.Rules()))
	for _, r := range p.Rules() {
		got = append(got, r.Pattern)
	}
	want := []string{"entity:lock.*", "entity:*", "entity:z*", "entity:a*", "entity:*"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Rules() order = %v, want file order %v", got, want)
	}
}

// --- D-55: a selector is evaluated only while it can change the answer ----

// countingLive counts every question and fails each of them.
type countingLive struct{ n int }

func (c *countingLive) EntityArea(string) (Ref, bool, error) {
	c.n++
	return Ref{}, false, errors.New("unreachable")
}

func (c *countingLive) EntityLabels(string) ([]Ref, error) {
	c.n++
	return nil, errors.New("unreachable")
}

func (c *countingLive) DeviceArea(string) (Ref, bool, error) {
	c.n++
	return Ref{}, false, errors.New("unreachable")
}

func (c *countingLive) DeviceLabels(string) ([]Ref, error) {
	c.n++
	return nil, errors.New("unreachable")
}

func TestSelectorIsTriedOnlyWhileItCanChangeTheVerdict(t *testing.T) {
	p := mustLoad(t, profileHead+
		"    allow:\n"+
		"      \"entity:*\": [control]\n"+ // line 7, plain
		"      \"entity:light.*\": [control]\n"+ // line 8, narrowed — after a plain allow
		"      \"entity:switch.*\": [control]\n"+ // line 9, narrowed
		"      \"entity:switch.k*\": [control]\n"+ // line 10, narrowed
		"    deny:\n"+
		"      \"entity:lock.*\": [control]\n"+
		"    where:\n"+
		"      \"entity:light.*\": {area: x}\n"+
		"      \"entity:switch.*\": {area: x}\n"+
		"      \"entity:switch.k*\": {area: x}\n")
	live := &countingLive{}
	v := p.Decide(mustResource(t, "entity:light.sofa"), Control, live)
	if !v.Allowed || v.Line != 7 || live.n != 0 || len(v.Notes) != 0 {
		t.Errorf("plain allow first: %+v, live asked %d times — a selector placed after a granting allow is never tried", v, live.n)
	}
	v = p.Decide(mustResource(t, "entity:lock.x"), Control, live)
	if v.Allowed || live.n != 0 || len(v.Notes) != 0 {
		t.Errorf("deny: %+v, live asked %d times — a deny decides without HA", v, live.n)
	}

	// Without the plain allow, the narrowed allows are tried in file order,
	// each failure noted, and the verdict falls to the default.
	q := mustLoad(t, profileHead+
		"    allow:\n"+
		"      \"entity:switch.*\": [control]\n"+
		"      \"entity:switch.k*\": [control]\n"+
		"      \"entity:*\": [control]\n"+
		"    where:\n"+
		"      \"entity:switch.*\": {area: x}\n"+
		"      \"entity:switch.k*\": {area: x}\n")
	live = &countingLive{}
	v = q.Decide(mustResource(t, "entity:switch.kaffee"), Control, live)
	if !v.Allowed || v.Line != 9 || live.n != 2 || len(v.Notes) != 2 ||
		!strings.Contains(v.Notes[0], `"entity:switch.*"`) || !strings.Contains(v.Notes[1], `"entity:switch.k*"`) {
		t.Errorf("narrowed allows tried in file order: %+v, live asked %d times", v, live.n)
	}
}

// --- D-66: the partial verdict ----------------------------------------------

func TestPartialVerdictImpl(t *testing.T) {
	p := mustLoad(t, profileHead+
		"    allow:\n"+
		"      \"entity:*\": [read, edit]\n"+
		"      \"file:**\": [edit]\n"+
		"    deny:\n"+
		"      \"entity:*/registry/entity_id\": [edit]\n"+ // line 10
		"      \"entity:device_tracker.*/attr/latitude\": [read]\n"+ // line 11
		"      \"entity:device_tracker.*/attr/lon*\": [read]\n"+ // line 12
		"      \"file:pkgs/x.yaml\": [edit]\n"+ // line 13
		"      \"file:a.yaml/block/b\": [edit]\n") // line 14
	cases := []struct {
		res      string
		verb     Verb
		allowed  bool
		partial  bool
		line     int
		denyLine []int
	}{
		{"entity:light.x", Edit, false, true, 10, []int{10}},
		{"entity:light.x/registry", Edit, false, true, 10, []int{10}},
		{"entity:light.x/registry/entity_id", Edit, false, false, 10, nil}, // the deny itself: step 1
		{"entity:light.x/registry/labels", Edit, true, false, 7, nil},
		{"entity:light.x/attr", Edit, true, false, 7, nil},
		{"entity:light.x", Read, true, false, 7, nil},
		{"entity:device_tracker.jan", Read, true, true, 7, []int{11, 12}},
		{"entity:device_tracker.jan/attr", Read, true, true, 7, []int{11, 12}},
		{"entity:device_tracker.jan/attr/latitude", Read, false, false, 11, nil},
		{"file:pkgs", Edit, true, false, 8, nil}, // a path is not a descendant of another path
		{"file:a.yaml", Edit, false, true, 14, []int{14}},
	}
	for _, c := range cases {
		v := p.Decide(mustResource(t, c.res), c.verb, nil)
		var lines []int
		for _, d := range v.PartialDenies {
			lines = append(lines, d.Line)
		}
		if v.Allowed != c.allowed || v.Partial != c.partial || v.Line != c.line || !reflect.DeepEqual(lines, c.denyLine) {
			t.Errorf("%s %s = allowed %v partial %v line %d denies %v, want %v %v %d %v (rule %q)",
				c.res, c.verb, v.Allowed, v.Partial, v.Line, lines, c.allowed, c.partial, c.line, c.denyLine, v.Rule)
		}
		if v.PartialDenies == nil {
			t.Errorf("%s: PartialDenies is nil, want []", c.res)
		}
	}
	// A default-deny verdict is never partial; neither is allow-all.
	if v := p.Decide(mustResource(t, "entity:light.x"), Control, nil); v.Partial || v.Rule != RuleDefaultDeny {
		t.Errorf("default deny: %+v", v)
	}
	none, _ := Load(t.TempDir())
	if v := none.Decide(mustResource(t, "entity:light.x"), Edit, nil); v.Partial || !v.Allowed {
		t.Errorf("no file: %+v", v)
	}
}

// --- lint: unreachable treats a glob sub path as a set ---------------------

func TestLintUnreachableReadsAGlobSubAsASet(t *testing.T) {
	cases := []struct {
		allow, deny string
		want        []string
	}{
		{"entity:lock.*/attr/*", "entity:lock.*/attr/?", nil},
		{"entity:device_tracker.jan/attr/l*", "entity:device_tracker.jan/attr/l?", nil},
		{"entity:lock.*/attr/*", "entity:lock.*/attr/*", []string{"unreachable@p:7"}},
		{"entity:lock.*/attr/*", "entity:lock.*/attr", []string{"unreachable@p:7"}},
		{"entity:lock.*/attr/*", "entity:lock.*", []string{"unreachable@p:7"}},
		{"entity:lock.*/attr/battery", "entity:lock.*/attr/b*", []string{"unreachable@p:7"}},
		{"entity:lock.*/attr/*", "entity:lock.*/attr/b*", nil},
	}
	inv := attrInventory{fakeInventory: fakeInventory{ids: map[Kind][]string{
		KindEntity: {"lock.front_door", "device_tracker.jan"},
	}}, attrs: map[string][]string{"lock.front_door": {"battery", "x"}, "device_tracker.jan": {"latitude", "la"}}}
	for _, c := range cases {
		p := mustLoad(t, profileHead+"    allow:\n      \""+c.allow+"\": [read]\n    deny:\n      \""+c.deny+"\": [read]\n")
		got := codes(Lint(p, inv))
		if len(got) == 0 {
			got = nil
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("allow %s, deny %s: findings %v, want %v", c.allow, c.deny, got, c.want)
		}
	}
	// The reachable verdict is Decide's: the allow does decide attr/battery.
	p := mustLoad(t, profileHead+"    allow:\n      \"entity:lock.*/attr/*\": [read]\n    deny:\n      \"entity:lock.*/attr/?\": [read]\n")
	if v := p.Decide(mustResource(t, "entity:lock.front_door/attr/battery"), Read, nil); !v.Allowed || v.Line != 7 {
		t.Errorf("Decide(attr/battery) = %+v, want allowed by line 7", v)
	}
}

// --- D-65: lint below the id -----------------------------------------------

type attrInventory struct {
	fakeInventory

	attrs map[string][]string
	err   error
}

func (a attrInventory) Attributes() (map[string][]string, error) { return a.attrs, a.err }

func TestLintChecksAttributeNamesAgainstLiveState(t *testing.T) {
	inv := attrInventory{fakeInventory: liveFixture, attrs: map[string][]string{
		"device_tracker.jan": {"latitude", "longitude", "gps_accuracy"},
		"light.sofa":         {"brightness"},
	}}
	p := mustLoad(t, profileHead+
		"    allow:\n      \"entity:*\": [read]\n"+
		"    deny:\n"+
		"      \"entity:device_tracker.*/attr/latitdue\": [read]\n"+ // 9: typo
		"      \"entity:device_tracker.*/attr/latitude\": [read]\n"+ // 10
		"      \"entity:*/attr/BRIGHT*\": [read]\n"+ // 11: glob, case-insensitive
		"      \"entity:lokc.*/attr/battery\": [read]\n"+ // 12: id matches nothing: one no-match
		"      \"entity:light.sofa/attr/latitude\": [read]\n"+ // 13: another entity has it
		"      \"entity:light.sofa/attr\": [read]\n") // 14: stops at attr
	rep := Lint(p, inv)
	if got := codes(rep); !reflect.DeepEqual(got, []string{"no-match@p:9", "no-match@p:12", "no-match@p:13"}) {
		t.Errorf("findings = %v", got)
	}
	if len(rep.Unchecked) != 0 {
		t.Errorf("unchecked = %+v, want none", rep.Unchecked)
	}

	// An inventory that cannot list attributes, or fails to: the attr rules
	// are unchecked, one entry per rule, never clean.
	for _, c := range []struct {
		inv    Inventory
		reason string
	}{
		{liveFixture, ReasonAttrsNotEnumerated},
		{attrInventory{fakeInventory: liveFixture, err: errors.New("connecting to HA: refused")}, "connecting to HA: refused"},
	} {
		rep := Lint(p, c.inv)
		lines := make([]int, 0, len(rep.Unchecked))
		for _, u := range rep.Unchecked {
			if u.Kind != KindEntity || u.Reason != c.reason || u.Profile != "p" || u.Rule == "" {
				t.Errorf("unchecked entry %+v, want entity/%q naming its rule", u, c.reason)
			}
			lines = append(lines, u.Line)
		}
		if !reflect.DeepEqual(lines, []int{9, 10, 11, 13}) {
			t.Errorf("%s: unchecked lines %v, want [9 10 11 13] (12 is a no-match, 14 has no name)", c.reason, lines)
		}
	}
}

func TestLintListsEveryOtherFreeSubSegmentAsUnchecked(t *testing.T) {
	inv := fakeInventory{ids: map[Kind][]string{
		KindDashboard: {"", "main"},
		KindSystem:    {"logs", "wireguard", "reload", "check_config", "cache"},
	}}
	p := mustLoad(t, profileHead+
		"    deny:\n"+
		"      \"system:reload/automaton\": [control]\n"+ // 7
		"      \"dashboard:main/views/secrte\": [read]\n"+ // 8
		"      \"file:x.yaml/block/clmate\": [edit]\n"+ // 9
		"      \"dashboard:main/views\": [edit]\n"+ // 10: no free segment
		"      \"dashboard:*/views/a\": [edit]\n"+ // 11
		"      \"file:y.yaml\": [edit]\n") // 12
	rep := Lint(p, inv)
	want := []Unchecked{
		{Kind: KindDashboard, Reason: ReasonSubNotChecked, Profile: "p", Rule: `deny "dashboard:main/views/secrte" [read]`, Line: 8},
		{Kind: KindDashboard, Reason: ReasonSubNotChecked, Profile: "p", Rule: `deny "dashboard:*/views/a" [edit]`, Line: 11},
		{Kind: KindFile, Reason: ReasonNotEnumerated},
		{Kind: KindFile, Reason: ReasonSubNotChecked, Profile: "p", Rule: `deny "file:x.yaml/block/clmate" [edit]`, Line: 9},
		{Kind: KindSystem, Reason: ReasonSubNotChecked, Profile: "p", Rule: `deny "system:reload/automaton" [control]`, Line: 7},
	}
	if !reflect.DeepEqual(rep.Unchecked, want) {
		t.Errorf("unchecked =\n%+v\nwant\n%+v", rep.Unchecked, want)
	}
	if len(rep.Findings) != 0 {
		t.Errorf("findings = %+v", rep.Findings)
	}
}
