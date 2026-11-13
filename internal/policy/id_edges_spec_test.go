package policy_test

// Spec tests for Jan's 2026-10-31 refinements of D-68 and D-65, authored from
// docs/decisions.md (D-65, D-68 as refined) and the manual's "Policy
// (permission guardrail)" section; the implementation was not read beyond
// exported signatures and doc comments.
//
// D-68 refined: in every id and every free sub segment (attr/<name>,
// views/<path>, block/<id>, reload/<domain>) leading or trailing white space,
// control characters (NUL included) and Unicode format characters (category
// Cf) are refused. Interior spaces are allowed where Home Assistant allows
// them (automation/script config ids, file paths, dashboard paths, attribute
// names); the HA-charset kinds (entity, service, helper, area, floor, label)
// stay strict. A pattern that breaks it is a broken file (D-60), a resource
// is invalid.
//
// D-65 refined: a free sub segment made only of wildcards counts as checked —
// nothing there can be misspelt — so it is not listed unchecked; a segment
// holding any literal character stays unchecked (or is live-checked, for
// attr/<name>).

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
)

// edgeChar is one refused character and the class it stands for.
type edgeChar struct {
	name, ch string
}

// Leading/trailing white space that is neither a control nor a format
// character: refused at the edges only (an interior one is not asserted here
// — interior spaces are the subject of TestInteriorSpaces).
var edgeWhitespace = []edgeChar{
	{"space", " "},
	{"no-break space U+00A0", "\u00a0"},
	{"em space U+2003", "\u2003"},
	{"ideographic space U+3000", "\u3000"},
}

// Control characters (category Cc): refused anywhere — start, end, middle.
var controlChars = []edgeChar{
	{"NUL", "\x00"},
	{"SOH U+0001", "\x01"},
	{"tab", "\t"},
	{"line feed", "\n"},
	{"carriage return", "\r"},
	{"escape U+001B", "\x1b"},
	{"DEL U+007F", "\x7f"},
	{"NEL U+0085", "\u0085"},
}

// Format characters (category Cf): refused anywhere — start, end, middle.
var formatChars = []edgeChar{
	{"zero width space U+200B", "\u200b"},
	{"zero width non-joiner U+200C", "\u200c"},
	{"zero width joiner U+200D", "\u200d"},
	{"left-to-right mark U+200E", "\u200e"},
	{"word joiner U+2060", "\u2060"},
	{"BOM / zero width no-break space U+FEFF", "\ufeff"},
	{"soft hyphen U+00AD", "\u00ad"},
	{"right-to-left override U+202E", "\u202e"},
	{"left-to-right embedding U+202A", "\u202a"},
	{"left-to-right isolate U+2066", "\u2066"},
	{"arabic letter mark U+061C", "\u061c"},
}

// idSite is one place a refused character can go: the address is
// prefix + value + suffix, and value is the part the character is put at the
// start, the end or the middle of. mid is the byte offset into value for the
// middle position.
type idSite struct {
	name           string
	prefix, value  string
	suffix         string
	mid            int
	interiorSpaces bool // D-68 refined: HA allows interior spaces here
	strict         bool // an HA-charset kind (entity, service, helper, area, floor, label)
}

// idSites is the id of every kind (system's closed set aside: an id outside
// it is refused regardless) and every free sub segment kind.
var idSites = []idSite{
	// ids — HA-charset kinds
	{name: "entity id", prefix: "entity:", value: "light.kitchen", mid: 5, strict: true},
	{name: "service id", prefix: "service:", value: "light.turn_on", mid: 7, strict: true},
	{name: "helper id", prefix: "helper:", value: "input_boolean/guest_mode", mid: 19, strict: true},
	{name: "area id", prefix: "area:", value: "wohnzimmer", mid: 4, strict: true},
	{name: "floor id", prefix: "floor:", value: "erdgeschoss", mid: 3, strict: true},
	{name: "label id", prefix: "label:", value: "agent_ok", mid: 5, strict: true},
	// ids — every other kind
	{name: "device id", prefix: "device:", value: "0123abcd", mid: 4},
	{name: "automation id", prefix: "automation:", value: "pg_heating", mid: 2, interiorSpaces: true},
	{name: "script id", prefix: "script:", value: "good_morning", mid: 4, interiorSpaces: true},
	{name: "template id", prefix: "template:", value: "sensor_uid_1", mid: 6},
	{name: "dashboard id", prefix: "dashboard:", value: "energy-main", mid: 6, interiorSpaces: true},
	{name: "config_entry id", prefix: "config_entry:", value: "01JABCDEF", mid: 3},
	{name: "file id", prefix: "file:", value: "packages/heating.yaml", mid: 12, interiorSpaces: true},
	{name: "file id before a block", prefix: "file:", value: "packages/heating.yaml", suffix: "/block/climate", mid: 12, interiorSpaces: true},
	// free sub segments
	{name: "attr/<name>", prefix: "entity:light.kitchen/attr/", value: "friendly_name", mid: 8, interiorSpaces: true},
	{name: "views/<path>", prefix: "dashboard:energy-main/views/", value: "overview", mid: 4, interiorSpaces: true},
	{name: "views/<path> of the default dashboard", prefix: "dashboard:/views/", value: "overview", mid: 4, interiorSpaces: true},
	{name: "block/<id>", prefix: "file:packages/heating.yaml/block/", value: "climate", mid: 3},
	{name: "reload/<domain>", prefix: "system:reload/", value: "automation", mid: 4},
}

func (s idSite) with(value string) string { return s.prefix + value + s.suffix }

// patternBody is a file whose single deny rule, on line 7, is pattern.
func patternBody(pattern string) string {
	return fmt.Sprintf("version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    deny:\n      %q: [read]\n", pattern)
}

// assertRefused holds addr to D-68 twice: as a resource (ParseResource
// refuses it — `policy check` would print nothing) and as a pattern (Load
// returns a *LoadError with a problem on line 7, the rule's line).
func assertRefused(t *testing.T, addr, why string) {
	t.Helper()
	if r, err := policy.ParseResource(addr); err == nil {
		t.Errorf("ParseResource(%q) = %+v, want an invalid resource (D-68: %s)", addr, r, why)
	}
	_, err := policy.Load(writePolicy(t, patternBody(addr)))
	var le *policy.LoadError
	if !errors.As(err, &le) {
		t.Errorf("pattern %q loaded (err %v), want a broken file (D-60/D-68: %s)", addr, err, why)
		return
	}
	if !slices.ContainsFunc(le.Problems, func(p policy.Problem) bool { return p.Line == 7 }) {
		t.Errorf("pattern %q: problems %+v, want one naming line 7 (D-60/D-68: %s)", addr, le.Problems, why)
	}
}

// assertAccepted holds addr to be both a valid resource and a valid pattern,
// read back as written: the resource's String() and the loaded rule's
// Pattern are addr itself, so no character was dropped on the way in.
func assertAccepted(t *testing.T, addr, why string) {
	t.Helper()
	if r, err := policy.ParseResource(addr); err != nil {
		t.Errorf("ParseResource(%q) = %v, want it accepted (%s)", addr, err, why)
	} else if got := r.String(); got != addr {
		t.Errorf("ParseResource(%q).String() = %q, want the address back as written (%s)", addr, got, why)
	}
	if p, err := policy.Load(writePolicy(t, patternBody(addr))); err != nil {
		t.Errorf("pattern %q refused (%v), want it valid (%s)", addr, err, why)
	} else if rules := p.Rules(); len(rules) != 1 || rules[0].Pattern != addr {
		t.Errorf("pattern %q loaded as rules %+v, want the one rule with that pattern as written (%s)", addr, rules, why)
	}
}

// TestIDSitesAreValidAsWritten is the positive control for every site below:
// without the offending character each address is a valid resource and a
// valid pattern, so a refusal in the tests below is the character's doing.
func TestIDSitesAreValidAsWritten(t *testing.T) {
	for _, s := range idSites {
		assertAccepted(t, s.with(s.value), "the unmodified "+s.name)
	}
}

// TestIDEdgeWhitespaceIsRefused — D-68 refined: leading or trailing white
// space is refused in every id and every free sub segment.
func TestIDEdgeWhitespaceIsRefused(t *testing.T) {
	for _, s := range idSites {
		for _, c := range edgeWhitespace {
			t.Run(s.name+"/"+c.name, func(t *testing.T) {
				assertRefused(t, s.with(c.ch+s.value), "leading "+c.name+" in the "+s.name)
				assertRefused(t, s.with(s.value+c.ch), "trailing "+c.name+" in the "+s.name)
			})
		}
	}
}

// TestIDControlAndFormatCharactersAreRefusedAnywhere — D-68 refined: control
// characters (NUL included) and Unicode format characters (Cf: ZWSP, ZWJ,
// word joiner, BOM, soft hyphen, bidi overrides and isolates…) are refused at
// the start, at the end and in the middle of every id and every free sub
// segment.
func TestIDControlAndFormatCharactersAreRefusedAnywhere(t *testing.T) {
	for _, s := range idSites {
		for _, group := range [][]edgeChar{controlChars, formatChars} {
			for _, c := range group {
				t.Run(s.name+"/"+c.name, func(t *testing.T) {
					assertRefused(t, s.with(c.ch+s.value), "leading "+c.name+" in the "+s.name)
					assertRefused(t, s.with(s.value+c.ch), "trailing "+c.name+" in the "+s.name)
					assertRefused(t, s.with(s.value[:s.mid]+c.ch+s.value[s.mid:]), c.name+" inside the "+s.name)
				})
			}
		}
	}
}

// TestInteriorSpaces — D-68 refined: an interior space is allowed where Home
// Assistant allows one — automation and script config ids, file paths,
// dashboard ids and paths, attribute names — and refused in the HA-charset
// kinds (entity, service, helper, area, floor, label), which stay strict.
// Sites neither list names (device, template, config_entry ids; block/<id>,
// reload/<domain>) are not asserted.
func TestInteriorSpaces(t *testing.T) {
	for _, s := range idSites {
		spaced := s.with(s.value[:s.mid] + " " + s.value[s.mid:])
		switch {
		case s.strict:
			assertRefused(t, spaced, "an interior space in the "+s.name+" (HA's charset has none)")
		case s.interiorSpaces:
			assertAccepted(t, spaced, "an interior space in the "+s.name+" is allowed (D-68 refined)")
		}
	}
	// More than one, and next to a glob, in a pattern.
	for _, ok := range []string{
		"automation:pg heating  morning",
		"script:good morning",
		"file:packages/my heating/climate control.yaml",
		"file:packages/my heating.yaml/block/climate",
		"dashboard:my dash",
		"dashboard:main/views/living room",
		"entity:device_tracker.jan/attr/last seen",
	} {
		assertAccepted(t, ok, "interior spaces where HA allows them (D-68 refined)")
	}
	for _, ok := range []string{
		"automation:pg heat*",
		"file:my packages/**",
		"entity:*/attr/last s*",
		"dashboard:*/views/living ?oom",
	} {
		if _, err := policy.Load(writePolicy(t, patternBody(ok))); err != nil {
			t.Errorf("pattern %q refused (%v), want it valid: interior spaces beside wildcards", ok, err)
		}
	}
}

// TestIDCharsetCheckIsNotFooledByAWildcard — D-68 refined holds for patterns
// whatever wildcards surround the offending character: `*` and `?` are not
// white space, so `automation:* ` still ends in a space.
func TestIDCharsetCheckIsNotFooledByAWildcard(t *testing.T) {
	for _, bad := range []string{
		"automation:* ",
		"automation: *",
		"automation:pg_*\u200b",
		"file:packages/**\t",
		"file:**/ x.yaml ",
		"entity:*/attr/* ",
		"entity:*/attr/\u00a0*",
		"dashboard:*/views/?\u2060",
		"system:reload/* ",
		"file:x.yaml/block/*\u00ad",
		"service:light.*\u202e",
		"label:agent_?\u200d",
	} {
		_, err := policy.Load(writePolicy(t, patternBody(bad)))
		var le *policy.LoadError
		if !errors.As(err, &le) || !slices.ContainsFunc(le.Problems, func(p policy.Problem) bool { return p.Line == 7 }) {
			t.Errorf("pattern %q: err %v, want a broken file naming line 7 (D-68 refined)", bad, err)
		}
	}
}

// TestRefusedCharacterSitesAreBrokenEachWithItsLine — D-60 under D-68
// refined: a file holding several such patterns names every one of them with
// its own line, and a valid rule between them (interior space) is no problem.
func TestRefusedCharacterSitesAreBrokenEachWithItsLine(t *testing.T) {
	body := `version: 1
active: agent
profiles:
  agent:
    default: deny
    deny:
      "entity:lock.front_door ": [read]
      "automation:pg heating": [read]
      "automation:\u200bpg_heating": [read]
      "dashboard:main/views/home\t": [read]
      "entity:light.kitchen/attr/friendly name": [read]
      "file:x.yaml/block/clim\u2060ate": [edit]
      "system:reload/ automation": [control]
`
	_, err := policy.Load(writePolicy(t, body))
	var le *policy.LoadError
	if !errors.As(err, &le) {
		t.Fatalf("Load = %v, want a broken file", err)
	}
	lines := make([]int, 0, len(le.Problems))
	for _, p := range le.Problems {
		lines = append(lines, p.Line)
	}
	if want := []int{7, 9, 10, 12, 13}; !slices.Equal(lines, want) {
		t.Errorf("problem lines %v, want %v (lines 8 and 11 hold interior spaces, which are allowed)\n%s", lines, want, le)
	}
}

// --- D-65 refined: an all-wildcard free sub segment counts as checked ----------

// attrsInventory is an Inventory that can also list attributes.
type attrsInventory struct {
	policy.Inventory

	attrs map[string][]string
}

func (a attrsInventory) Attributes() (map[string][]string, error) { return a.attrs, nil }

func lintInventory() policy.Inventory {
	return fixtureInventory{e: &lintExpect{Inventory: map[string][]string{
		"entity":    {"light.kitchen", "device_tracker.jan"},
		"dashboard": {"", "main"},
		"system":    {"logs", "wireguard", "reload", "check_config", "cache"},
	}}}
}

// ruleUnchecked reports the per-rule Unchecked entries (Rule != "") other
// than a kind's `not enumerated` as "<reason>@<line>".
func ruleUnchecked(rep policy.LintReport) []string {
	var out []string
	for _, u := range rep.Unchecked {
		// A file rule's own `not enumerated` (D-128) is about its kind, not
		// about its free segment, which is what these tests pin (D-65).
		if u.Rule != "" && u.Reason != policy.ReasonNotEnumerated {
			out = append(out, fmt.Sprintf("%s@%d", u.Reason, u.Line))
		}
	}
	return out
}

// TestLintCountsAnAllWildcardFreeSubAsChecked — D-65 refined: a free sub
// segment that is only wildcards (`*`) has nothing to misspell, so lint does
// not list the rule unchecked — for views/<path>, reload/<domain>,
// block/<id> and attr/<name> alike, the last even when the inventory cannot
// list attributes. A segment holding any literal character, mixed with a
// wildcard (`views/a*`) or not, stays unchecked.
func TestLintCountsAnAllWildcardFreeSubAsChecked(t *testing.T) {
	for _, tc := range []struct {
		pattern string
		verb    string
		want    []string // per-rule unchecked entries, "<reason>@7"
	}{
		// all wildcards: checked
		{"dashboard:main/views/*", "read", nil},
		{"dashboard:*/views/*", "read", nil},
		{"dashboard:/views/*", "read", nil},
		{"system:reload/*", "control", nil},
		{"file:x.yaml/block/*", "edit", nil},
		{"entity:*/attr/*", "read", nil},
		{"entity:device_tracker.jan/attr/*", "read", nil},
		// a literal character: unchecked
		{"dashboard:main/views/home", "read", []string{policy.ReasonSubNotChecked + "@7"}},
		{"dashboard:main/views/a*", "read", []string{policy.ReasonSubNotChecked + "@7"}},
		{"dashboard:main/views/*a", "read", []string{policy.ReasonSubNotChecked + "@7"}},
		{"dashboard:*/views/h*e", "read", []string{policy.ReasonSubNotChecked + "@7"}},
		{"system:reload/auto*", "control", []string{policy.ReasonSubNotChecked + "@7"}},
		{"system:reload/automation", "control", []string{policy.ReasonSubNotChecked + "@7"}},
		{"file:x.yaml/block/clim*", "edit", []string{policy.ReasonSubNotChecked + "@7"}},
		{"file:x.yaml/block/climate", "edit", []string{policy.ReasonSubNotChecked + "@7"}},
		// attr with a literal: live-checked when attributes can be listed,
		// otherwise unchecked (plain Inventory here)
		{"entity:*/attr/lat*", "read", []string{policy.ReasonAttrsNotEnumerated + "@7"}},
		{"entity:*/attr/latitude", "read", []string{policy.ReasonAttrsNotEnumerated + "@7"}},
	} {
		t.Run(tc.pattern, func(t *testing.T) {
			body := fmt.Sprintf("version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    deny:\n      %q: [%s]\n", tc.pattern, tc.verb)
			p := mustLoad(t, writePolicy(t, body))
			rep := policy.Lint(p, lintInventory())
			if got := ruleUnchecked(rep); !slices.Equal(got, tc.want) {
				t.Errorf("per-rule unchecked %v, want %v (D-65 refined: only a segment with a literal character is unchecked)\nfull: %+v", got, tc.want, rep.Unchecked)
			}
			if got := lintCodes(rep, policy.CodeNoMatch); len(got) != 0 {
				t.Errorf("no-match %v, want none (every id here matches a live one)", got)
			}
		})
	}
}

// TestLintAllWildcardAttrIsNeverNoMatch — D-65 refined, with attributes
// listable: `attr/*` has nothing to misspell, so it is never a no-match on
// its name and never unchecked; `attr/lat*` and `attr/latitude` are checked
// live (no entry when some matched entity has one, no-match when none has).
func TestLintAllWildcardAttrIsNeverNoMatch(t *testing.T) {
	inv := attrsInventory{
		Inventory: lintInventory(),
		attrs: map[string][]string{
			"light.kitchen":      {"friendly_name", "brightness"},
			"device_tracker.jan": {"latitude", "longitude"},
		},
	}
	body := `version: 1
active: agent
profiles:
  agent:
    default: deny
    deny:
      "entity:*/attr/*": [read]
      "entity:device_tracker.*/attr/*": [read]
      "entity:*/attr/lat*": [read]
      "entity:light.*/attr/lat*": [read]
      "entity:device_tracker.jan/attr/latitude": [read]
`
	rep := policy.Lint(mustLoad(t, writePolicy(t, body)), inv)
	if got, want := lintCodes(rep, policy.CodeNoMatch), []string{`deny "entity:light.*/attr/lat*" [read]@10`}; !slices.Equal(got, want) {
		t.Errorf("no-match %v, want %v", got, want)
	}
	if got := ruleUnchecked(rep); len(got) != 0 {
		t.Errorf("per-rule unchecked %v, want none: attribute names are live-checked, and `*` counts as checked", got)
	}
}

// TestLintAllWildcardFreeSubsLeaveLintClean — D-65 refined end to end: a file
// whose only free sub segments are all wildcards, over enumerated kinds, is
// clean — no finding and nothing unchecked — so lint passes without
// --allow-partial. One literal segment beside them makes it unclean again.
func TestLintAllWildcardFreeSubsLeaveLintClean(t *testing.T) {
	clean := `version: 1
active: agent
profiles:
  agent:
    default: deny
    deny:
      "dashboard:main/views/*": [read]
      "dashboard:/views/*": [edit]
      "system:reload/*": [read]
      "entity:*/attr/*": [read]
`
	rep := policy.Lint(mustLoad(t, writePolicy(t, clean)), lintInventory())
	if len(rep.Findings) != 0 || len(rep.Unchecked) != 0 {
		t.Errorf("lint = findings %+v unchecked %+v, want clean (D-65 refined: an all-wildcard segment counts as checked)", rep.Findings, rep.Unchecked)
	}

	unclean := clean + "      \"dashboard:main/views/a*\": [read]\n"
	rep = policy.Lint(mustLoad(t, writePolicy(t, unclean)), lintInventory())
	want := []string{policy.ReasonSubNotChecked + "@11"}
	if got := ruleUnchecked(rep); !slices.Equal(got, want) {
		t.Errorf("per-rule unchecked %v, want %v (a literal character beside a wildcard stays unchecked)", got, want)
	}
}

// TestLintDoubleStarFreeSubCountsAsChecked — D-65 refined names `**` beside
// `*` as a wildcard-only segment. Whether `**` is a valid pattern in a sub
// segment at all is the grammar's business (the manual gives `**` its
// whole-segment meaning only in a file id); where it loads, it must not be
// listed unchecked.
func TestLintDoubleStarFreeSubCountsAsChecked(t *testing.T) {
	for _, pat := range []string{
		"dashboard:main/views/**",
		"system:reload/**",
		"file:x.yaml/block/**",
		"entity:*/attr/**",
	} {
		p, err := policy.Load(writePolicy(t, patternBody(pat)))
		if err != nil {
			t.Logf("%q is a broken file here (%v); D-65 refined has nothing to say about it", pat, strings.ReplaceAll(err.Error(), "\n", "; "))
			continue
		}
		if got := ruleUnchecked(policy.Lint(p, lintInventory())); len(got) != 0 {
			t.Errorf("%q: per-rule unchecked %v, want none (D-65 refined: `**` is only wildcards)", pat, got)
		}
	}
}
