package policy

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// writePolicy writes content as <dir>/policy.yaml and returns dir.
func writePolicy(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustLoad(t *testing.T, content string) *Policy {
	t.Helper()
	p, err := Load(writePolicy(t, content))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p
}

// loadProblems loads content and returns its D-60 problems; it fails the test
// when the file loads.
func loadProblems(t *testing.T, content string) []Problem {
	t.Helper()
	_, err := Load(writePolicy(t, content))
	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("Load succeeded or failed without a *LoadError (err=%v); want a broken file:\n%s", err, content)
	}
	return le.Problems
}

func hasProblem(ps []Problem, line int, substr string) bool {
	for _, p := range ps {
		if p.Line == line && strings.Contains(p.Message, substr) {
			return true
		}
	}
	return false
}

// loadErr loads content and returns Load's result as is.
func loadErr(t *testing.T, content string) (*Policy, error) {
	t.Helper()
	return Load(writePolicy(t, content))
}

func mustResource(t *testing.T, s string) Resource {
	t.Helper()
	r, err := ParseResource(s)
	if err != nil {
		t.Fatalf("ParseResource(%q): %v", s, err)
	}
	return r
}

// manualExample is the file of the manual's "Policy file format" section.
const manualExample = `# <instance>/policy.yaml
version: 1
active: agent                     # the profile in effect, for CLI and MCP alike
profiles:
  default:                        # in effect when there is no active:
    deny:
      "entity:lock.*":                  [write]
  agent:
    default: deny
    allow:
      "entity:*":                       [read]
      "entity:light.*":                 [control]
      "automation:pg_*":                [read, edit, create]
      "entity:*/registry/labels":       [edit]
    deny:
      "entity:lock.*":                  [read, write]
      "entity:device_tracker.*/attr/latitude":  [read]
      "file:**":                        [write]
    where:
      "entity:light.*": { area: wohnzimmer }
`

// --- D-58: no file ---------------------------------------------------------

func TestNoFileIsAllowAll(t *testing.T) {
	dir := t.TempDir()
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p.Exists() || p.Selection() != SelectedByNoFile || !p.AllowAll() || p.Active() != "" {
		t.Fatalf("no file: exists=%v selection=%q allowAll=%v active=%q", p.Exists(), p.Selection(), p.AllowAll(), p.Active())
	}
	if p.Source() != filepath.Join(dir, FileName) {
		t.Errorf("Source = %q", p.Source())
	}
	if p.Default() != Allow || len(p.Rules()) != 0 || len(p.Profiles()) != 0 {
		t.Errorf("no file: default=%q rules=%v profiles=%v", p.Default(), p.Rules(), p.Profiles())
	}
	v := p.Decide(mustResource(t, "entity:lock.front_door"), Control, nil)
	if !v.Allowed || v.Rule != RuleNoFile || v.Level != "A" || v.Line != 0 {
		t.Errorf("no file verdict = %+v, want allowed by %q", v, RuleNoFile)
	}
	rep := Lint(p, nil)
	if rep.Findings == nil || rep.Unchecked == nil || len(rep.Findings)+len(rep.Unchecked) != 0 {
		t.Errorf("no file lint = %+v, want empty non-nil slices", rep)
	}
	if _, err := p.As("agent"); err == nil {
		t.Error("As on a missing file must be refused (D-62: --as names a declared profile)")
	}
}

func TestPolicyYmlIsBrokenNotAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "policy.yml"), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir)
	var le *LoadError
	if !errors.As(err, &le) || !strings.Contains(le.Error(), "policy.yml") {
		t.Fatalf("policy.yml alone: err = %v, want a *LoadError naming policy.yml", err)
	}

	// Beside a valid policy.yaml it is still a problem.
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); !errors.As(err, &le) {
		t.Fatalf("policy.yml beside policy.yaml: err = %v, want *LoadError", err)
	}
}

func TestPolicyPathThatIsADirectoryIsAnError(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, FileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(dir); err == nil {
		t.Fatal("a directory named policy.yaml must not load as no policy")
	}
}

// --- D-60: a broken file fails closed, naming file:line --------------------

func TestBrokenFileProblemsNameTheirLine(t *testing.T) {
	cases := []struct {
		name, content string
		line          int
		substr        string
	}{
		{"yaml syntax", "version: 1\nprofiles:\n  a: [\n", 3, "not valid YAML"},
		{"top not mapping", "- a\n- b\n", 1, "top level must be a mapping"},
		{"empty file", "", 0, "version missing"},
		{"comment only", "# nothing\n", 0, "version missing"},
		{"version missing", "profiles: {}\n", 1, "version missing"},
		{"version string", "version: \"1\"\n", 1, "integer 1"},
		{"version two", "version: 2\n", 1, "integer 1"},
		{"unknown top key", "version: 1\nprofile: {}\n", 2, `unknown key "profile"`},
		{"active null", "version: 1\nactive:\nprofiles: {agent: {}}\n", 2, "active"},
		{"active tilde", "version: 1\nactive: ~\nprofiles: {agent: {}}\n", 2, "active"},
		{"active empty string", "version: 1\nactive: \"\"\nprofiles: {agent: {}}\n", 2, "active is empty"},
		{"active number", "version: 1\nactive: 3\nprofiles: {agent: {}}\n", 2, "active must be a string"},
		{"active undeclared", "version: 1\nactive: agnet\nprofiles:\n  agent: {}\n", 2, `active names "agnet"`},
		{"unknown profile key", "version: 1\nprofiles:\n  a:\n    allows: {}\n", 4, `unknown key "allows"`},
		{"bad default", "version: 1\nprofiles:\n  a:\n    default: block\n", 4, "allow or deny"},
		{"null profile", "version: 1\nprofiles:\n  a:\n", 3, "must be a mapping"},
		{"unknown kind", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entitiy:lock.*\": [write]\n", 5, `unknown kind "entitiy"`},
		{"missing colon", "version: 1\nprofiles:\n  a:\n    deny:\n      \"lock.*\": [write]\n", 5, `missing ":"`},
		{"helper one segment", "version: 1\nprofiles:\n  a:\n    deny:\n      \"helper:guest_mode\": [write]\n", 5, "<domain>/<id>"},
		{"unknown sub", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entity:x.y/atr/latitude\": [read]\n", 5, `unknown sub-resource "atr"`},
		{"registry field typo", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entity:*/registry/lables\": [edit]\n", 5, `unknown registry field "lables"`},
		{"wildcard sub word", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entity:*/at*\": [read]\n", 5, "literal"},
		{"too deep sub", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entity:x.y/attr/a/b\": [read]\n", 5, "one segment below"},
		{"no subs kind", "version: 1\nprofiles:\n  a:\n    deny:\n      \"area:kitchen/x\": [read]\n", 5, "no sub-resources"},
		{"system id outside set", "version: 1\nprofiles:\n  a:\n    deny:\n      \"system:restart\": [control]\n", 5, "names none of"},
		{"system glob matches none", "version: 1\nprofiles:\n  a:\n    deny:\n      \"system:x*\": [control]\n", 5, "names none of"},
		{"empty id", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entity:\": [read]\n", 5, "empty entity id"},
		{"empty segment", "version: 1\nprofiles:\n  a:\n    deny:\n      \"file:a//b\": [read]\n", 5, "empty segment"},
		{"verbs not a list", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entity:*\": write\n", 5, "must be a list"},
		{"verbs empty", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entity:*\": []\n", 5, "empty verb list"},
		{"unknown verb", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entity:*\": [wirte]\n", 5, `unknown verb "wirte"`},
		{"uppercase verb", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entity:*\": [Read]\n", 5, `unknown verb "Read"`},
		{"duplicate pattern", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entity:x.y\": [read]\n      \"entity:x.y\": [edit]\n", 6, "first at line 5"},
		{"where not an allow", "version: 1\nprofiles:\n  a:\n    allow:\n      \"entity:light.*\": [control]\n    where:\n      \"entity:light.x*\": {area: k}\n", 7, "names no allow pattern"},
		{"where on wrong kind", "version: 1\nprofiles:\n  a:\n    allow:\n      \"automation:*\": [read]\n    where:\n      \"automation:*\": {area: k}\n", 7, "only an entity or device"},
		{"where on star", "version: 1\nprofiles:\n  a:\n    allow:\n      \"*\": [read]\n    where:\n      \"*\": {area: k}\n", 7, "only an entity or device"},
		{"where empty selector", "version: 1\nprofiles:\n  a:\n    allow:\n      \"entity:*\": [read]\n    where:\n      \"entity:*\": {}\n", 7, "is empty"},
		{"where unknown selector key", "version: 1\nprofiles:\n  a:\n    allow:\n      \"entity:*\": [read]\n    where:\n      \"entity:*\": {floor: k}\n", 7, `unknown key "floor"`},
		{"where empty list", "version: 1\nprofiles:\n  a:\n    allow:\n      \"entity:*\": [read]\n    where:\n      \"entity:*\": {area: []}\n", 7, "empty list"},
		// D-63: a dot-less, wildcard-free entity/service id can never match.
		{"entity without domain", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entity:hallway\": [read]\n", 5, "never the part after the domain"},
		{"service without domain", "version: 1\nprofiles:\n  a:\n    allow:\n      \"service:turn_on\": [control]\n", 5, "never the part after the domain"},
		// D-67: file ids.
		{"file dot segment", "version: 1\nprofiles:\n  a:\n    deny:\n      \"file:./secrets.yaml\": [read]\n", 5, `"." segment`},
		{"file dotdot segment", "version: 1\nprofiles:\n  a:\n    deny:\n      \"file:packages/../secrets.yaml\": [read]\n", 5, `".." segment`},
		{"file ambiguous block", "version: 1\nprofiles:\n  a:\n    deny:\n      \"file:a/block/b/c.yaml\": [read]\n", 5, "ambiguous with the block sub-resource"},
		// D-68: the id charset.
		{"entity trailing space", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entity:lock.* \": [read]\n", 5, "no entity id may hold white space"},
		{"label dash", "version: 1\nprofiles:\n  a:\n    deny:\n      \"label:agent-ok\": [read]\n", 5, "a label id is ASCII letters, digits and _"},
		{"file tab", "version: 1\nprofiles:\n  a:\n    deny:\n      \"file:a\\tb.yaml\": [read]\n", 5, "a control character"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := loadProblems(t, c.content)
			if !hasProblem(ps, c.line, c.substr) {
				t.Errorf("problems = %+v, want one at line %d containing %q", ps, c.line, c.substr)
			}
		})
	}
}

// Duplicate keys at any level — yaml.v3 decodes duplicates into a Node without
// complaint, so a walk that kept the last `deny:` would silently drop the
// first one's rules (verifier finding, holes lens).
func TestDuplicateKeysAtAnyLevelAreBroken(t *testing.T) {
	cases := []struct {
		name, content string
		line          int
	}{
		{"top level active", "version: 1\nactive: a\nactive: b\nprofiles: {a: {}, b: {}}\n", 3},
		{"profile name", "version: 1\nprofiles:\n  agent:\n    deny: {\"entity:lock.*\": [write]}\n  agent:\n    default: deny\n", 5},
		{"deny key", "version: 1\nprofiles:\n  a:\n    deny: {\"entity:lock.*\": [write]}\n    deny: {\"entity:alarm.*\": [write]}\n", 5},
		{"selector key", "version: 1\nprofiles:\n  a:\n    allow: {\"entity:*\": [read]}\n    where:\n      \"entity:*\":\n        area: x\n        area: y\n", 8},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := loadProblems(t, c.content)
			if !hasProblem(ps, c.line, "duplicate key") {
				t.Errorf("problems = %+v, want a duplicate key at line %d", ps, c.line)
			}
		})
	}
}

// Aliases and merge keys reach a node walk as nodes with no content of their
// own; read pairwise they would silently drop the rules they stand for.
func TestAliasesAndMergeKeysAreBroken(t *testing.T) {
	cases := []struct {
		name, content string
		line          int
	}{
		{"aliased deny mapping", "version: 1\nprofiles:\n  base:\n    deny: &d {\"entity:lock.*\": [write]}\n  agent:\n    default: deny\n    deny: *d\n", 7},
		{"aliased verb list", "version: 1\nprofiles:\n  a:\n    deny:\n      \"entity:lock.*\": &v [write]\n      \"entity:alarm.*\": *v\n", 6},
		{"merge key", "version: 1\nprofiles:\n  a:\n    deny:\n      <<: {\"entity:lock.*\": [write]}\n", 5},
		// D-60 (confirmed as built): an anchor is broken even when nothing
		// refers to it — on a scalar, a mapping, a key and a sequence.
		{"unused anchor on a scalar", "version: 1\nactive: &p a\nprofiles:\n  a:\n    default: deny\n", 2},
		{"unused anchor on a mapping", "version: 1\nprofiles:\n  a:\n    default: deny\n    deny: &d\n      \"entity:lock.*\": [write]\n", 5},
		{"unused anchor on a key", "version: 1\nprofiles:\n  a:\n    default: deny\n    deny:\n      &k \"entity:lock.*\": [write]\n", 6},
		{"unused anchor on a verb list", "version: 1\nprofiles:\n  a:\n    default: deny\n    deny:\n      \"entity:lock.*\": &v [write]\n", 6},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ps := loadProblems(t, c.content)
			if !hasProblem(ps, c.line, "aliases and merge keys") {
				t.Errorf("problems = %+v, want an alias/merge problem at line %d", ps, c.line)
			}
		})
	}
}

func TestSecondYAMLDocumentIsBroken(t *testing.T) {
	ps := loadProblems(t, "version: 1\nprofiles:\n  agent:\n    allow: {\"entity:*\": [read]}\n---\nprofiles:\n  agent:\n    deny: {\"entity:lock.*\": [write]}\n")
	if !hasProblem(ps, 5, "more than one YAML document") {
		t.Errorf("problems = %+v, want the second document named at line 5", ps)
	}
}

func TestLoadErrorListsEveryProblemInLineOrder(t *testing.T) {
	dir := writePolicy(t, "version: 2\nprofiles:\n  a:\n    deny:\n      \"x:y\": [read]\n      \"entity:*\": [wirte]\nfoo: 1\n")
	_, err := Load(dir)
	var le *LoadError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v", err)
	}
	lines := make([]int, 0, len(le.Problems))
	for _, p := range le.Problems {
		lines = append(lines, p.Line)
	}
	if !reflect.DeepEqual(lines, []int{1, 5, 6, 7}) {
		t.Errorf("problem lines = %v, want [1 5 6 7]: every problem, by line (%+v)", lines, le.Problems)
	}
	file := filepath.Join(dir, FileName)
	first := strings.SplitN(le.Error(), "\n", 2)[0]
	if !strings.HasPrefix(first, file+":1: ") {
		t.Errorf("Error() first line = %q, want %q prefix", first, file+":1: ")
	}
}

func TestManualExampleLoads(t *testing.T) {
	p := mustLoad(t, manualExample)
	if p.Active() != "agent" || p.Selection() != SelectedByActive || p.AllowAll() || p.Default() != Deny {
		t.Fatalf("active=%q selection=%q allowAll=%v default=%q", p.Active(), p.Selection(), p.AllowAll(), p.Default())
	}
	if !reflect.DeepEqual(p.Profiles(), []string{"agent", "default"}) {
		t.Errorf("Profiles = %v", p.Profiles())
	}
	texts := make([]string, 0, len(p.Rules()))
	for _, r := range p.Rules() {
		texts = append(texts, r.Text())
	}
	want := []string{
		`deny "entity:lock.*" [read write]`,
		`deny "entity:device_tracker.*/attr/latitude" [read]`,
		`deny "file:**" [write]`,
		`allow "entity:*" [read]`,
		`allow "entity:light.*" [control] where area=wohnzimmer`,
		`allow "automation:pg_*" [read edit create]`,
		`allow "entity:*/registry/labels" [edit]`,
	}
	if !reflect.DeepEqual(texts, want) {
		t.Errorf("Rules (deny first, then allow, each by line) =\n%s\nwant\n%s", strings.Join(texts, "\n"), strings.Join(want, "\n"))
	}
	if got := p.Rules()[0].Verbs; !reflect.DeepEqual(got, AllVerbs()) {
		t.Errorf("[read, write] expands to %v, want all five", got)
	}
}

// --- D-61 / D-62: which profile applies ------------------------------------

func TestProfileSelection(t *testing.T) {
	cases := []struct {
		name, content string
		active        string
		sel           Selection
		allowAll      bool
	}{
		{"active", "version: 1\nactive: b\nprofiles: {a: {}, b: {}, default: {}}\n", "b", SelectedByActive, false},
		{"default profile", "version: 1\nprofiles: {a: {}, default: {}}\n", "default", SelectedByDefault, false},
		{"implicit", "version: 1\nprofiles: {a: {default: deny}}\n", "", SelectedByImplicit, true},
		{"implicit no profiles", "version: 1\n", "", SelectedByImplicit, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := mustLoad(t, c.content)
			if p.Active() != c.active || p.Selection() != c.sel || p.AllowAll() != c.allowAll {
				t.Errorf("active=%q selection=%q allowAll=%v, want %q %q %v", p.Active(), p.Selection(), p.AllowAll(), c.active, c.sel, c.allowAll)
			}
		})
	}
	// The implicit default allows everything, under its own rule text.
	p := mustLoad(t, "version: 1\nprofiles: {a: {default: deny}}\n")
	if v := p.Decide(mustResource(t, "entity:lock.x"), Delete, nil); !v.Allowed || v.Rule != RuleImplicit {
		t.Errorf("implicit verdict = %+v", v)
	}
}

// Load takes no profile and reads no environment (D-62): only `active:`
// decides, however the caller's environment is set.
func TestLoadIgnoresTheEnvironmentForTheProfile(t *testing.T) {
	t.Setenv("HACTL_PROFILE", "open")
	p := mustLoad(t, "version: 1\nactive: closed\nprofiles: {open: {}, closed: {default: deny}}\n")
	if p.Active() != "closed" {
		t.Errorf("Active = %q, want the file's active: closed", p.Active())
	}
}

func TestAsIsAReadOnlyWhatIf(t *testing.T) {
	p := mustLoad(t, manualExample)
	w, err := p.As("default")
	if err != nil {
		t.Fatal(err)
	}
	if w.Active() != "default" || w.Selection() != SelectedByWhatIf || !w.WhatIf() {
		t.Errorf("what-if view: active=%q selection=%q", w.Active(), w.Selection())
	}
	if base, sel := w.Effective(); base != "agent" || sel != SelectedByActive {
		t.Errorf("what-if Effective = %q %q, want agent active", base, sel)
	}
	if p.Active() != "agent" || p.Selection() != SelectedByActive {
		t.Errorf("As changed the receiver: active=%q selection=%q", p.Active(), p.Selection())
	}
	lock := mustResource(t, "entity:lock.front_door")
	if v := w.Decide(lock, Read, nil); !v.Allowed || !v.WhatIf || v.Profile != "default" {
		t.Errorf("what-if verdict = %+v, want allowed read under default, WhatIf set", v)
	}
	if v := p.Decide(lock, Read, nil); v.Allowed || v.WhatIf {
		t.Errorf("effective verdict = %+v, want denied, not a what-if", v)
	}
	if _, err := p.As("nope"); err == nil || !strings.Contains(err.Error(), "agent, default") {
		t.Errorf("As(unknown) err = %v, want a refusal naming the declared profiles", err)
	}
}

// --- Matching ---------------------------------------------------------------

func decideIn(t *testing.T, rules, resource string, v Verb) Verdict {
	t.Helper()
	p := mustLoad(t, "version: 1\nactive: p\nprofiles:\n  p:\n    default: deny\n    allow:\n"+rules)
	return p.Decide(mustResource(t, resource), v, nil)
}

func TestMatching(t *testing.T) {
	cases := []struct {
		pattern, resource string
		want              bool
	}{
		{"*", "system:logs", true},
		{"*", "file:a/b/c.yaml", true},
		{"entity:light.kitchen", "entity:light.kitchen", true},
		{"entity:light.kitchen", "entity:LIGHT.Kitchen", true},    // D-2
		{"entity:light.kitchen", "entity:light.kitchen_2", false}, // exact, never substring
		{"entity:kitchen*", "entity:light.kitchen_lamp", false},   // D-63: the full id only, no second anchor
		{"entity:*.kitchen*", "entity:light.kitchen_lamp", true},  // across domains: the domain as a wildcard
		{"entity:*.front_door", "entity:lock.front_door", true},
		{"service:*.turn_on", "service:light.turn_on", true},
		{"file:k?che.yaml", "file:küche.yaml", true}, // ? is one character, not one byte
		{"entity:sensor.*", "entity:binary_sensor.x", false},
		{"entity:light.kitchen", "entity:light.kitchen/attr/brightness", true}, // parent covers child
		{"entity:light.kitchen/attr", "entity:light.kitchen", false},           // child does not cover parent
		{"entity:*/registry/labels", "entity:light.x/registry/labels", true},
		{"entity:*/registry/labels", "entity:light.x/registry/name", false},
		{"entity:*/attr/lat*", "entity:device_tracker.j/attr/latitude", true},
		{"device:abc", "device:abc", true},
		{"device:abc", "entity:abc", false}, // kind must be equal
		{"helper:input_boolean/*", "helper:input_boolean/guest_mode", true},
		{"helper:input_boolean/*", "helper:input_number/guest_mode", false},
		{"file:*", "file:configuration.yaml", true},
		{"file:*", "file:packages/heating.yaml", false}, // * never crosses a /
		{"file:**", "file:packages/heating.yaml", true},
		{"file:packages/**", "file:packages/a/b.yaml", true},
		{"file:packages/**", "file:automations.yaml", false},
		{"file:packages/x.yaml/block/*", "file:packages/x.yaml/block/climate", true},
		{"file:packages/x.yaml", "file:packages/x.yaml/block/climate", true},
		{"dashboard:", "dashboard:", true},
		{"dashboard:", "dashboard:energy", false},
		{"dashboard:*", "dashboard:", true},
		{"dashboard:/views/home", "dashboard:/views/home", true},
		{"service:light.*", "service:light.turn_on", true},
		{"system:reload", "system:reload/automation", true},
		{"system:reload/auto*", "system:reload/automation", true},
		{"system:reload/automation", "system:reload", false},
	}
	for _, c := range cases {
		got := decideIn(t, "      \""+c.pattern+"\": [read]\n", c.resource, Read).Allowed
		if got != c.want {
			t.Errorf("pattern %q vs %q: allowed=%v, want %v", c.pattern, c.resource, got, c.want)
		}
	}
}

// D-63: a policy pattern matches the full id only. A wildcard-free entity or
// service id without a `.` can never match a real id, so it is a broken file
// (D-60) rather than a rule granting that name in every domain. This replaces
// the pending-decision test that pinned the old D-28 second anchor.
func TestUnqualifiedLiteralIsABrokenFile(t *testing.T) {
	for _, pat := range []string{"entity:front_door", "service:turn_on", "entity:Front_Door"} {
		_, err := loadErr(t, "version: 1\nactive: p\nprofiles:\n  p:\n    allow:\n      \""+pat+"\": [control]\n")
		if err == nil || !strings.Contains(err.Error(), ":6: ") || !strings.Contains(err.Error(), "never the part after the domain") {
			t.Errorf("%s: err = %v, want a broken file naming line 6 and why the id can never match", pat, err)
		}
	}
	// With a wildcard or a dot it loads; the wildcard form is the cross-domain one.
	for _, pat := range []string{"entity:front*", "entity:*.front_door", "service:light.turn_on", "entity:front_doo?"} {
		if _, err := loadErr(t, "version: 1\nactive: p\nprofiles:\n  p:\n    allow:\n      \""+pat+"\": [control]\n"); err != nil {
			t.Errorf("%s: %v, want a valid file", pat, err)
		}
	}
	if decideIn(t, "      \"entity:front*\": [control]\n", "entity:lock.front_door", Control).Allowed {
		t.Error("entity:front* granted lock.front_door: a policy pattern has no second anchor (D-63)")
	}
}

// --- Evaluation (D-52) ------------------------------------------------------

func TestEvaluation(t *testing.T) {
	p := mustLoad(t, manualExample)
	cases := []struct {
		resource string
		verb     Verb
		allowed  bool
		rule     string
		line     int
	}{
		{"entity:lock.front_door", Control, false, `deny "entity:lock.*" [read write]`, 16},
		{"entity:lock.front_door", Read, false, `deny "entity:lock.*" [read write]`, 16}, // deny overrides allow entity:*
		{"entity:sensor.t", Read, true, `allow "entity:*" [read]`, 11},
		{"entity:sensor.t", Edit, false, RuleDefaultDeny, 0},
		{"entity:sensor.t/registry/labels", Edit, true, `allow "entity:*/registry/labels" [edit]`, 14},
		{"entity:sensor.t/registry/area_id", Edit, false, RuleDefaultDeny, 0}, // child allow grants only the child
		{"entity:device_tracker.jan/attr/latitude", Read, false, `deny "entity:device_tracker.*/attr/latitude" [read]`, 17},
		{"entity:device_tracker.jan/attr/longitude", Read, true, `allow "entity:*" [read]`, 11}, // a child deny cuts out only the child
		{"automation:pg_heating", Edit, true, `allow "automation:pg_*" [read edit create]`, 13},
		{"automation:pg_heating", Delete, false, RuleDefaultDeny, 0},
		{"file:packages/x.yaml", Edit, false, `deny "file:**" [write]`, 18},
	}
	for _, c := range cases {
		v := p.Decide(mustResource(t, c.resource), c.verb, nil)
		if v.Allowed != c.allowed || v.Rule != c.rule || v.Line != c.line || v.Profile != "agent" || v.Level != Level {
			t.Errorf("%s %s = %+v, want allowed=%v rule=%q line=%d", c.resource, c.verb, v, c.allowed, c.rule, c.line)
		}
	}
}

func TestDefaultAllowProfile(t *testing.T) {
	p := mustLoad(t, "version: 1\nprofiles:\n  default:\n    deny: {\"entity:lock.*\": [write]}\n")
	if v := p.Decide(mustResource(t, "entity:light.x"), Control, nil); !v.Allowed || v.Rule != RuleDefaultAllow {
		t.Errorf("verdict = %+v, want %q", v, RuleDefaultAllow)
	}
}

// When several rules of the deciding effect match, the lowest line is named —
// whatever the order the file lists them in (H-16).
func TestDecidingRuleIsTheFirstByLine(t *testing.T) {
	p := mustLoad(t, "version: 1\nprofiles:\n  default:\n    deny:\n      \"entity:lock.front*\": [write]\n      \"entity:lock.*\": [control]\n")
	v := p.Decide(mustResource(t, "entity:lock.front_door"), Control, nil)
	if v.Line != 5 || v.Rule != `deny "entity:lock.front*" [write]` {
		t.Errorf("verdict = %+v, want the line-5 deny", v)
	}
	// Two rules on one line (a flow mapping): the leftmost is the first in
	// the file, whatever its pattern text sorts as (D-52).
	q := mustLoad(t, "version: 1\nprofiles:\n  default:\n    deny: {\"entity:lock.*\": [write], \"entity:*\": [control]}\n")
	if v := q.Decide(mustResource(t, "entity:lock.front_door"), Control, nil); v.Rule != `deny "entity:lock.*" [write]` {
		t.Errorf("same-line verdict = %+v, want the leftmost rule", v)
	}
}

type fakeLive struct {
	entityArea   map[string]Ref
	entityLabels map[string][]Ref
	deviceArea   map[string]Ref
	err          error
}

func (f fakeLive) EntityArea(id string) (Ref, bool, error) {
	if f.err != nil {
		return Ref{}, false, f.err
	}
	a, ok := f.entityArea[id]
	return a, ok, nil
}

func (f fakeLive) EntityLabels(id string) ([]Ref, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.entityLabels[id], nil
}

func (f fakeLive) DeviceArea(id string) (Ref, bool, error) {
	if f.err != nil {
		return Ref{}, false, f.err
	}
	a, ok := f.deviceArea[id]
	return a, ok, nil
}

func (f fakeLive) DeviceLabels(string) ([]Ref, error) { return nil, f.err }

func TestWhereSelector(t *testing.T) {
	p := mustLoad(t, manualExample)
	live := fakeLive{
		entityArea: map[string]Ref{
			"light.sofa":    {ID: "wohnzimmer", Name: "Wohnzimmer"},
			"light.kitchen": {ID: "kueche", Name: "Küche"},
		},
	}
	sofa := mustResource(t, "entity:light.sofa")
	if v := p.Decide(sofa, Control, live); !v.Allowed || v.Rule != `allow "entity:light.*" [control] where area=wohnzimmer` {
		t.Errorf("sofa = %+v, want allowed by the narrowed allow", v)
	}
	if v := p.Decide(mustResource(t, "entity:light.kitchen"), Control, live); v.Allowed {
		t.Errorf("kitchen = %+v, want denied: the selector does not hold", v)
	}
	// Unevaluable: no live, or a live error, never grants — and says so.
	for _, l := range []Live{nil, fakeLive{err: errors.New("HA unreachable")}} {
		v := p.Decide(sofa, Control, l)
		if v.Allowed || len(v.Notes) != 1 || !strings.HasPrefix(v.Notes[0], `where not evaluated for allow "entity:light.*" [control]: `) {
			t.Errorf("unevaluable selector: %+v, want denied with one note", v)
		}
	}

	// By name, and by label; several values are alternatives; both keys must hold.
	q := mustLoad(t, "version: 1\nactive: p\nprofiles:\n  p:\n    default: deny\n    allow: {\"entity:*\": [control]}\n    where:\n      \"entity:*\": {area: [Küche, garten], label: agent_ok}\n")
	live2 := fakeLive{
		entityArea:   map[string]Ref{"light.a": {ID: "kueche", Name: "Küche"}, "light.b": {ID: "kueche", Name: "Küche"}},
		entityLabels: map[string][]Ref{"light.a": {{ID: "agent_ok", Name: "Agent OK"}}},
	}
	if !q.Decide(mustResource(t, "entity:light.a"), Control, live2).Allowed {
		t.Error("light.a: area matches by name and label by id — want allowed")
	}
	if q.Decide(mustResource(t, "entity:light.b"), Control, live2).Allowed {
		t.Error("light.b: area holds, label does not — want denied")
	}
}

func TestEmptySlicesAreNotNil(t *testing.T) {
	p := mustLoad(t, manualExample)
	if v := p.Decide(mustResource(t, "entity:sensor.x"), Read, nil); v.Notes == nil {
		t.Error("Notes must be an empty slice, not nil (--json renders [])")
	}
	q := mustLoad(t, "version: 1\n")
	if q.Rules() == nil || q.Profiles() == nil {
		t.Error("Rules/Profiles must be empty slices, not nil")
	}
}

// --- ParseResource / ParseVerb ---------------------------------------------

func TestParseResource(t *testing.T) {
	good := map[string]Resource{
		"entity:light.kitchen":                   {Kind: KindEntity, ID: "light.kitchen"},
		"entity:light.kitchen/attr/brightness":   {Kind: KindEntity, ID: "light.kitchen", Sub: "attr/brightness"},
		"helper:input_boolean/guest_mode":        {Kind: KindHelper, ID: "input_boolean/guest_mode"},
		"helper:input_boolean/guest_mode/config": {Kind: KindHelper, ID: "input_boolean/guest_mode", Sub: "config"},
		"file:packages/heating.yaml/block/clim":  {Kind: KindFile, ID: "packages/heating.yaml", Sub: "block/clim"},
		"file:configuration.yaml":                {Kind: KindFile, ID: "configuration.yaml"},
		"dashboard:":                             {Kind: KindDashboard, ID: ""},
		"dashboard:/views/home":                  {Kind: KindDashboard, ID: "", Sub: "views/home"},
		"system:reload/automation":               {Kind: KindSystem, ID: "reload", Sub: "automation"},
	}
	inputs := make([]string, 0, len(good))
	for s := range good {
		inputs = append(inputs, s)
	}
	for _, s := range sortedCopy(inputs) {
		want := good[s]
		got, err := ParseResource(s)
		if err != nil || got != want {
			t.Errorf("ParseResource(%q) = %+v, %v; want %+v", s, got, err, want)
			continue
		}
		// The round-trip holds for every resource ParseResource returns.
		if back, err := ParseResource(got.String()); err != nil || back != got {
			t.Errorf("round-trip %q -> %q -> %+v, %v", s, got.String(), back, err)
		}
	}
	bad := []string{
		"lock.front_door", "entitiy:x.y", "entity:lock.*", "entity:x.?", "*", "entity:",
		"helper:guest_mode", "system:restart", "entity:x.y/registry/lables", "entity:x.y/foo",
		"area:a/b", "file:a//b", "file:block/x", "entity:x.y/state/z",
	}
	for _, s := range bad {
		if _, err := ParseResource(s); err == nil {
			t.Errorf("ParseResource(%q) accepted; want a refusal", s)
		}
	}
}

func sortedCopy(ss []string) []string {
	out := append([]string(nil), ss...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func TestParseVerb(t *testing.T) {
	for _, v := range AllVerbs() {
		if got, err := ParseVerb(v.String()); err != nil || got != v {
			t.Errorf("ParseVerb(%q) = %v, %v", v.String(), got, err)
		}
	}
	if _, err := ParseVerb("write"); err == nil || !strings.Contains(err.Error(), "control, edit, create and delete") {
		t.Errorf("ParseVerb(write) err = %v, want a refusal naming the four verbs", err)
	}
	for _, s := range []string{"Read", " read", "wirte", ""} {
		if _, err := ParseVerb(s); err == nil {
			t.Errorf("ParseVerb(%q) accepted", s)
		}
	}
	got, err := ParseVerbs([]string{"delete", "write", "read", "read"})
	if err != nil || !reflect.DeepEqual(got, AllVerbs()) {
		t.Errorf("ParseVerbs = %v, %v; want all five, canonical order, de-duplicated", got, err)
	}
	if _, err := ParseVerbs(nil); err == nil {
		t.Error("ParseVerbs(empty) accepted")
	}
	if Verb(9).String() != "verb(9)" {
		t.Errorf("out-of-range verb renders %q", Verb(9).String())
	}
}

// D-60: a policy.yaml (or policy.yml) that is there but cannot be read — a
// dangling symlink, a directory, no permission — is a *LoadError naming the
// path, never "no policy file" (allow-all).
func TestUnreadablePolicyPathIsALoadError(t *testing.T) {
	for _, name := range []string{FileName, "policy.yml"} {
		t.Run("dangling symlink "+name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Symlink(filepath.Join(dir, "moved-away.yaml"), filepath.Join(dir, name)); err != nil {
				t.Fatal(err)
			}
			p, err := Load(dir)
			var le *LoadError
			if !errors.As(err, &le) {
				t.Fatalf("Load = (%v, %v), want a *LoadError", p, err)
			}
			if le.File != filepath.Join(dir, name) || !strings.Contains(le.Error(), "moved-away.yaml") {
				t.Errorf("LoadError %q does not name the path and its target", le.Error())
			}
		})
		t.Run("directory "+name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.Mkdir(filepath.Join(dir, name), 0o700); err != nil {
				t.Fatal(err)
			}
			var le *LoadError
			if _, err := Load(dir); !errors.As(err, &le) || !strings.Contains(le.Error(), "a directory") {
				t.Fatalf("Load = %v, want a *LoadError saying it is a directory", err)
			}
		})
	}
	t.Run("no permission", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a 0000 file")
		}
		dir := writePolicy(t, "version: 1\n")
		if err := os.Chmod(filepath.Join(dir, FileName), 0); err != nil {
			t.Fatal(err)
		}
		var le *LoadError
		if _, err := Load(dir); !errors.As(err, &le) || !strings.Contains(le.Error(), "cannot be read") {
			t.Fatalf("Load = %v, want a *LoadError saying it cannot be read", err)
		}
	})
}

// TestDecideDenyOnly (D-107): a deny-only check is decided by the denies of
// the effective profile alone — a deny on the resource or an ancestor, or for
// a write verb a deny on a strict descendant (D-66) — never by `default: deny`
// and never by an allow, which grants nothing extra. Kind-wide, any deny of
// the kind decides. The allow-all views answer as Decide.
func TestDecideDenyOnly(t *testing.T) {
	p := mustLoad(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "automation:pg_*": [control]
    deny:
      "automation:prod_*": [write]
      "helper:input_boolean/alarm_armed/config": [control]
`)
	for _, c := range []struct {
		resource string
		allowed  bool
		rule     string
		line     int
		partial  bool
	}{
		{"automation:prod_heating", false, `deny "automation:prod_*" [write]`, 9, false},
		{"automation:kitchen", true, RuleNoDeny, 0, false}, // default: deny does not decide
		{"automation:pg_test", true, RuleNoDeny, 0, false}, // nor does an allow
		// D-110: control does not apply at a helper's config, so the deny
		// there makes nothing partial.
		{"helper:input_boolean/alarm_armed", true, RuleNoDeny, 0, false},
	} {
		v := p.DecideDenyOnly(mustResource(t, c.resource), Control)
		if v.Allowed != c.allowed || v.Rule != c.rule || v.Line != c.line || v.Partial != c.partial || v.Profile != "agent" {
			t.Errorf("%s = %+v, want allowed=%v rule=%q line=%d partial=%v", c.resource, v, c.allowed, c.rule, c.line, c.partial)
		}
		if full := p.Decide(mustResource(t, c.resource), Control, nil); c.resource == "automation:kitchen" && full.Allowed {
			t.Errorf("Decide allowed %s under default: deny — the case proves nothing", c.resource)
		}
	}
	if v := p.DecideDenyOnly(mustResource(t, "automation:prod_heating"), Read); !v.Allowed || v.Rule != RuleNoDeny {
		t.Errorf("read on a write deny = %+v", v)
	}
	for _, c := range []struct {
		kind    Kind
		allowed bool
		rule    string
	}{
		{KindAutomation, false, `deny "automation:prod_*" [write]`},
		{KindTemplate, true, RuleNoDeny},
		{KindHelper, true, RuleNoDeny}, // a descendant deny whose verb does not apply there (D-110)
	} {
		v := p.DecideKindWideDenyOnly(c.kind, "", Control)
		if v.Allowed != c.allowed || v.Rule != c.rule {
			t.Errorf("%s:* = %+v, want allowed=%v rule=%q", c.kind, v, c.allowed, c.rule)
		}
	}
	none := mustLoad(t, "version: 1\nprofiles:\n  agent:\n    deny: {\"automation:*\": [write]}\n")
	if v := none.DecideDenyOnly(mustResource(t, "automation:x"), Control); !v.Allowed || v.Rule != RuleImplicit {
		t.Errorf("implicit allow-all = %+v", v)
	}
	if v := none.DecideKindWideDenyOnly(KindAutomation, "", Control); !v.Allowed || v.Rule != RuleImplicit {
		t.Errorf("implicit allow-all kind-wide = %+v", v)
	}
}
