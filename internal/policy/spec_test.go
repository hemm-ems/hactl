package policy_test

// Spec tests for the policy model that a static vector cannot express:
// permutation (D-52 order independence), the what-if view's immutability
// (D-62), the environment's irrelevance (D-62), a live selector re-read on
// every check (D-55), the resource and verb grammar, and the manual's
// resources table against KindSpecs. Authored from the contract only.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
)

// writePolicy creates an instance directory holding policy.yaml with body.
func writePolicy(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, policy.FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func mustLoad(t *testing.T, dir string) *policy.Policy {
	t.Helper()
	p, err := policy.Load(dir)
	if err != nil {
		t.Fatalf("Load(%s): %v", dir, err)
	}
	if p == nil {
		t.Fatalf("Load(%s) returned nil and no error", dir)
	}
	return p
}

func mustResource(t *testing.T, s string) policy.Resource {
	t.Helper()
	r, err := policy.ParseResource(s)
	if err != nil {
		t.Fatalf("ParseResource(%q): %v", s, err)
	}
	return r
}

// manualExample is the file the manual's "Policy file format" section shows,
// verbatim. Its lines are the ones the manual's --json example quotes
// (deny "entity:lock.*" on 16, allow "entity:light.*" on 12).
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

// --- D-52: order independence ------------------------------------------------

type permRule struct {
	pattern string
	verbs   string // YAML flow list body, e.g. "read, write"
}

func permutations[T any](xs []T) [][]T {
	if len(xs) <= 1 {
		return [][]T{slices.Clone(xs)}
	}
	var out [][]T
	for i := range xs {
		rest := make([]T, 0, len(xs)-1)
		rest = append(rest, xs[:i]...)
		rest = append(rest, xs[i+1:]...)
		for _, p := range permutations(rest) {
			out = append(out, append([]T{xs[i]}, p...))
		}
	}
	return out
}

// permQuestion is one question the permutation test asks of every file.
type permQuestion struct {
	resource, verb string
	allowed        bool
	rule           string // exact Text of the deciding rule, or a default text
	pattern        string // pattern whose line must be reported ("" for a default)
}

// renderPermutation writes one ordering of the profile and records the line
// each pattern landed on.
func renderPermutation(allows, denies []permRule, blocks []string, where string) (string, map[string]int) {
	lines := []string{"version: 1", "active: agent", "profiles:", "  agent:", "    default: deny"}
	lineOf := map[string]int{}
	emit := func(rules []permRule) {
		for _, r := range rules {
			lines = append(lines, fmt.Sprintf("      %s: [%s]", r.pattern, r.verbs))
			lineOf[r.pattern] = len(lines)
		}
	}
	for _, b := range blocks {
		lines = append(lines, "    "+b+":")
		switch b {
		case "allow":
			emit(allows)
		case "deny":
			emit(denies)
		case "where":
			lines = append(lines, where)
		}
	}
	return strings.Join(lines, "\n") + "\n", lineOf
}

// checkPermutation asks every question of one rendered file.
func checkPermutation(t *testing.T, n int, body string, lineOf map[string]int, questions []permQuestion, live policy.Live) {
	t.Helper()
	p, err := policy.Load(writePolicy(t, body))
	if err != nil {
		t.Fatalf("permutation %d does not load: %v\n%s", n, err, body)
	}
	for _, q := range questions {
		v, err := policy.ParseVerb(q.verb)
		if err != nil {
			t.Fatalf("ParseVerb(%q): %v", q.verb, err)
		}
		got := p.Decide(mustResource(t, q.resource), v, live)
		if got.Allowed != q.allowed || got.Rule != q.rule {
			t.Errorf("permutation %d, %s %s: got (allowed=%v, %q), want (allowed=%v, %q)\n%s",
				n, q.resource, q.verb, got.Allowed, got.Rule, q.allowed, q.rule, body)
			continue
		}
		wantLine := 0
		if q.pattern != "" {
			wantLine = lineOf[q.pattern]
		}
		if got.Line != wantLine {
			t.Errorf("permutation %d, %s %s: Line = %d, want %d (where %s sits in this file)\n%s",
				n, q.resource, q.verb, got.Line, wantLine, q.pattern, body)
		}
	}
}

// TestDecideIsIndependentOfRuleOrder renders the same profile in every order
// of its allow entries, its deny entries and its three blocks (allow, deny,
// where) — 3!·2!·3! = 72 files — and requires every file to give the same
// verdict, decided by the same rule, for every question (D-52: rule order
// carries no meaning; H-16: the answer never depends on order). Each question
// is chosen so that exactly one rule of the deciding effect matches, so the
// named rule is fully determined; its Line must follow it to wherever the
// permutation put it.
func TestDecideIsIndependentOfRuleOrder(t *testing.T) {
	allows := []permRule{
		{`"entity:*"`, "read"},
		{`"entity:light.*"`, "control"},
		{`"automation:pg_*"`, "read, edit"},
	}
	denies := []permRule{
		{`"entity:lock.*"`, "read, write"},
		{`"automation:pg_secret*"`, "edit"},
	}
	where := `      "entity:light.*": { area: wohnzimmer }`
	live := fixtureLive{f: &liveFixture{Entities: map[string]liveObj{
		"light.wz": {Area: &refYAML{ID: "wohnzimmer", Name: "Wohnzimmer"}},
		"light.k":  {Area: &refYAML{ID: "kueche", Name: "Küche"}},
	}}}
	questions := []permQuestion{
		{"entity:sensor.t", "read", true, `allow "entity:*" [read]`, `"entity:*"`},
		{"entity:lock.front", "read", false, `deny "entity:lock.*" [read write]`, `"entity:lock.*"`},
		{"entity:lock.front", "control", false, `deny "entity:lock.*" [read write]`, `"entity:lock.*"`},
		{"entity:light.wz", "control", true, `allow "entity:light.*" [control] where area=wohnzimmer`, `"entity:light.*"`},
		{"entity:light.k", "control", false, "default: deny", ""},
		{"automation:pg_secret_x", "edit", false, `deny "automation:pg_secret*" [edit]`, `"automation:pg_secret*"`},
		{"automation:pg_heat", "edit", true, `allow "automation:pg_*" [read edit]`, `"automation:pg_*"`},
		{"automation:pg_secret_x", "read", true, `allow "automation:pg_*" [read edit]`, `"automation:pg_*"`},
		{"automation:other", "read", false, "default: deny", ""},
	}

	files := 0
	for _, ap := range permutations(allows) {
		for _, dp := range permutations(denies) {
			for _, blocks := range permutations([]string{"allow", "deny", "where"}) {
				files++
				body, lineOf := renderPermutation(ap, dp, blocks, where)
				checkPermutation(t, files, body, lineOf, questions, live)
			}
		}
	}
	if files != 72 {
		t.Fatalf("rendered %d permutations, want 72", files)
	}
}

// --- D-62: the what-if view ---------------------------------------------------

// TestAsIsAWhatIfAndNeverChangesTheReceiver: As returns a new view; the
// effective policy keeps its profile, selection and verdicts (D-62).
func TestAsIsAWhatIfAndNeverChangesTheReceiver(t *testing.T) {
	p := mustLoad(t, writePolicy(t, manualExample))
	r := mustResource(t, "entity:lock.front_door")

	before := p.Decide(r, policy.Read, nil)
	if before.Allowed || before.Profile != "agent" || before.WhatIf {
		t.Fatalf("effective verdict before As = %+v, want denied by profile agent, not a what-if", before)
	}

	view, err := p.As("default")
	if err != nil {
		t.Fatalf("As(default): %v", err)
	}
	if view == p {
		t.Errorf("As returned its receiver; it must return a new view")
	}
	if view.Active() != "default" || view.Selection() != policy.SelectedByWhatIf || !view.WhatIf() {
		t.Errorf("view: Active=%q Selection=%q WhatIf=%v, want default/what-if/true",
			view.Active(), view.Selection(), view.WhatIf())
	}
	got := view.Decide(r, policy.Read, nil)
	if !got.Allowed || got.Profile != "default" || !got.WhatIf || got.Rule != policy.RuleDefaultAllow {
		t.Errorf("what-if verdict = %+v, want allowed by %q in profile default, WhatIf set", got, policy.RuleDefaultAllow)
	}

	// The receiver is unchanged in every observable.
	if p.Active() != "agent" || p.Selection() != policy.SelectedByActive || p.WhatIf() {
		t.Errorf("receiver after As: Active=%q Selection=%q WhatIf=%v, want agent/active/false",
			p.Active(), p.Selection(), p.WhatIf())
	}
	after := p.Decide(r, policy.Read, nil)
	if after.Allowed != before.Allowed || after.Rule != before.Rule || after.Profile != before.Profile || after.WhatIf {
		t.Errorf("receiver's verdict changed after As: before %+v, after %+v", before, after)
	}
}

// TestLoadNeverReadsTheProfileFromTheEnvironment: only the file selects the
// profile (D-62) — there is no HACTL_PROFILE. Set it to a declared profile
// that is not `active:`, and to one that is not declared at all; neither may
// change the selection or fail the load.
func TestLoadNeverReadsTheProfileFromTheEnvironment(t *testing.T) {
	dir := writePolicy(t, manualExample)
	for _, val := range []string{"default", "nosuch"} {
		t.Run(val, func(t *testing.T) {
			t.Setenv("HACTL_PROFILE", val)
			p := mustLoad(t, dir)
			if p.Active() != "agent" || p.Selection() != policy.SelectedByActive {
				t.Errorf("HACTL_PROFILE=%s: Active=%q Selection=%q, want agent selected by active:", val, p.Active(), p.Selection())
			}
			got := p.Decide(mustResource(t, "entity:lock.front_door"), policy.Read, nil)
			if got.Profile != "agent" || got.Allowed {
				t.Errorf("HACTL_PROFILE=%s: verdict %+v, want denied in profile agent", val, got)
			}
		})
	}
}

// --- Load edges ------------------------------------------------------------

// TestLoadWithoutAFileReportsThePathItLookedAt: D-58 — no file is not an
// error, and Source names the policy.yaml Load looked for.
func TestLoadWithoutAFileReportsThePathItLookedAt(t *testing.T) {
	dir := t.TempDir()
	p := mustLoad(t, dir)
	if p.Exists() {
		t.Errorf("Exists() = true for an empty instance directory")
	}
	if want := filepath.Join(dir, policy.FileName); p.Source() != want {
		t.Errorf("Source() = %q, want %q", p.Source(), want)
	}
	if !p.AllowAll() || p.Selection() != policy.SelectedByNoFile {
		t.Errorf("AllowAll=%v Selection=%q, want true/no-file", p.AllowAll(), p.Selection())
	}
	if len(p.Rules()) != 0 || len(p.Profiles()) != 0 {
		t.Errorf("Rules=%v Profiles=%v, want both empty for no file", p.Rules(), p.Profiles())
	}

	withFile := writePolicy(t, manualExample)
	q := mustLoad(t, withFile)
	if want := filepath.Join(withFile, policy.FileName); q.Source() != want || !q.Exists() {
		t.Errorf("Source()=%q Exists()=%v, want %q/true", q.Source(), q.Exists(), want)
	}
}

// TestLoadRefusesAPolicyPathThatIsNotARegularFile: a policy.yaml that exists
// but is a directory is an error, never "no policy" (Load's doc; D-60's spirit).
func TestLoadRefusesAPolicyPathThatIsNotARegularFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, policy.FileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if p, err := policy.Load(dir); err == nil {
		t.Fatalf("Load succeeded on a policy.yaml directory (policy %+v); want an error, never \"no policy\"", p)
	}
}

// --- D-55: live, on every check ---------------------------------------------

// TestWhereIsEvaluatedLiveOnEveryCheck: a relabel takes effect at once (D-55,
// E-4) — the same Policy, asked twice, consults the Live each time.
func TestWhereIsEvaluatedLiveOnEveryCheck(t *testing.T) {
	p := mustLoad(t, writePolicy(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:switch.*": [control]
    where:
      "entity:switch.*": { label: agent_ok }
`))
	fx := &liveFixture{Entities: map[string]liveObj{"switch.kaffee": {}}}
	live := fixtureLive{f: fx}
	r := mustResource(t, "entity:switch.kaffee")

	if got := p.Decide(r, policy.Control, live); got.Allowed {
		t.Fatalf("before the relabel: %+v, want denied", got)
	}
	fx.Entities["switch.kaffee"] = liveObj{Labels: []refYAML{{ID: "agent_ok", Name: "Agent OK"}}}
	if got := p.Decide(r, policy.Control, live); !got.Allowed || got.Line != 7 {
		t.Errorf("after the relabel: %+v, want allowed by the narrowed allow on line 7", got)
	}
	fx.Entities["switch.kaffee"] = liveObj{}
	if got := p.Decide(r, policy.Control, live); got.Allowed {
		t.Errorf("after removing the label again: %+v, want denied", got)
	}
}

// --- Rules(), Profiles(), Default() ---------------------------------------

// TestRulesListDenyFirstThenAllowInFileOrder: Rules() is the effective
// profile's rules, denies first then allows, each by line; Pattern and Written
// as written, Verbs expanded in canonical order, Where only on the narrowed
// allow (manual "policy show", Rule's doc).
func TestRulesListDenyFirstThenAllowInFileOrder(t *testing.T) {
	p := mustLoad(t, writePolicy(t, manualExample))
	if p.Default() != policy.Deny {
		t.Errorf("Default() = %q, want deny", p.Default())
	}
	if got := p.Profiles(); !slices.Equal(got, []string{"agent", "default"}) {
		t.Errorf("Profiles() = %q, want [agent default]", got)
	}
	all := policy.AllVerbs()
	write := []policy.Verb{policy.Control, policy.Edit, policy.Create, policy.Delete}
	want := []policy.Rule{
		{Effect: policy.Deny, Pattern: "entity:lock.*", Written: []string{"read", "write"}, Verbs: all, Line: 16},
		{Effect: policy.Deny, Pattern: "entity:device_tracker.*/attr/latitude", Written: []string{"read"}, Verbs: []policy.Verb{policy.Read}, Line: 17},
		{Effect: policy.Deny, Pattern: "file:**", Written: []string{"write"}, Verbs: write, Line: 18},
		{Effect: policy.Allow, Pattern: "entity:*", Written: []string{"read"}, Verbs: []policy.Verb{policy.Read}, Line: 11},
		{Effect: policy.Allow, Pattern: "entity:light.*", Written: []string{"control"}, Verbs: []policy.Verb{policy.Control},
			Where: &policy.Where{Area: []string{"wohnzimmer"}}, Line: 12},
		{Effect: policy.Allow, Pattern: "automation:pg_*", Written: []string{"read", "edit", "create"},
			Verbs: []policy.Verb{policy.Read, policy.Edit, policy.Create}, Line: 13},
		{Effect: policy.Allow, Pattern: "entity:*/registry/labels", Written: []string{"edit"}, Verbs: []policy.Verb{policy.Edit}, Line: 14},
	}
	got := p.Rules()
	if len(got) != len(want) {
		t.Fatalf("Rules() has %d rules, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		g, w := got[i], want[i]
		if g.Effect != w.Effect || g.Pattern != w.Pattern || g.Line != w.Line ||
			!slices.Equal(g.Written, w.Written) || !slices.Equal(g.Verbs, w.Verbs) {
			t.Errorf("rule %d = %+v, want %+v", i, g, w)
		}
		switch {
		case w.Where == nil && g.Where != nil:
			t.Errorf("rule %d (%s) carries a where %+v, want none", i, w.Pattern, *g.Where)
		case w.Where != nil && g.Where == nil:
			t.Errorf("rule %d (%s) lost its where", i, w.Pattern)
		case w.Where != nil && (!slices.Equal(g.Where.Area, w.Where.Area) || len(g.Where.Label) != 0):
			t.Errorf("rule %d where = %+v, want %+v", i, *g.Where, *w.Where)
		}
	}
	if got[4].Text() != `allow "entity:light.*" [control] where area=wohnzimmer` {
		t.Errorf("narrowed allow renders as %q", got[4].Text())
	}

	// The default profile, through a what-if, declares no `default:` → allow.
	view, err := p.As("default")
	if err != nil {
		t.Fatalf("As(default): %v", err)
	}
	if view.Default() != policy.Allow {
		t.Errorf("default profile's Default() = %q, want allow (omitted means allow)", view.Default())
	}
	if r := view.Rules(); len(r) != 1 || r[0].Line != 7 || r[0].Pattern != "entity:lock.*" {
		t.Errorf("default profile's Rules() = %+v, want the one deny on line 7", r)
	}
}

// --- the resource grammar -----------------------------------------------------

// TestParseResourceReadsTheManualsGrammar: every row of the manual's
// resources table parses into (Kind, ID, Sub), and String() gives back the
// input (ParseResource(r.String()) == r).
func TestParseResourceReadsTheManualsGrammar(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want policy.Resource
	}{
		{"entity:light.kitchen", policy.Resource{Kind: policy.KindEntity, ID: "light.kitchen"}},
		{"entity:light.kitchen/state", policy.Resource{Kind: policy.KindEntity, ID: "light.kitchen", Sub: "state"}},
		{"entity:device_tracker.jan/attr/latitude", policy.Resource{Kind: policy.KindEntity, ID: "device_tracker.jan", Sub: "attr/latitude"}},
		{"entity:light.kitchen/attr", policy.Resource{Kind: policy.KindEntity, ID: "light.kitchen", Sub: "attr"}},
		{"entity:light.kitchen/registry/labels", policy.Resource{Kind: policy.KindEntity, ID: "light.kitchen", Sub: "registry/labels"}},
		{"entity:light.kitchen/registry", policy.Resource{Kind: policy.KindEntity, ID: "light.kitchen", Sub: "registry"}},
		{"entity:light.kitchen/history", policy.Resource{Kind: policy.KindEntity, ID: "light.kitchen", Sub: "history"}},
		{"entity:light.kitchen/stats", policy.Resource{Kind: policy.KindEntity, ID: "light.kitchen", Sub: "stats"}},
		{"device:0123abcd/registry/name_by_user", policy.Resource{Kind: policy.KindDevice, ID: "0123abcd", Sub: "registry/name_by_user"}},
		{"area:kitchen", policy.Resource{Kind: policy.KindArea, ID: "kitchen"}},
		{"floor:ground", policy.Resource{Kind: policy.KindFloor, ID: "ground"}},
		{"label:agent_ok", policy.Resource{Kind: policy.KindLabel, ID: "agent_ok"}},
		{"automation:pg_heating/trace", policy.Resource{Kind: policy.KindAutomation, ID: "pg_heating", Sub: "trace"}},
		{"script:morning/run", policy.Resource{Kind: policy.KindScript, ID: "morning", Sub: "run"}},
		{"template:my_unique_id/config", policy.Resource{Kind: policy.KindTemplate, ID: "my_unique_id", Sub: "config"}},
		{"helper:input_boolean/guest_mode", policy.Resource{Kind: policy.KindHelper, ID: "input_boolean/guest_mode"}},
		{"helper:input_boolean/guest_mode/config", policy.Resource{Kind: policy.KindHelper, ID: "input_boolean/guest_mode", Sub: "config"}},
		{"dashboard:", policy.Resource{Kind: policy.KindDashboard, ID: ""}},
		{"dashboard:/views/home", policy.Resource{Kind: policy.KindDashboard, ID: "", Sub: "views/home"}},
		{"dashboard:energy-main/resources", policy.Resource{Kind: policy.KindDashboard, ID: "energy-main", Sub: "resources"}},
		{"config_entry:01JABC/options", policy.Resource{Kind: policy.KindConfigEntry, ID: "01JABC", Sub: "options"}},
		{"config_entry:01JABC/flow", policy.Resource{Kind: policy.KindConfigEntry, ID: "01JABC", Sub: "flow"}},
		{"file:configuration.yaml", policy.Resource{Kind: policy.KindFile, ID: "configuration.yaml"}},
		{"file:packages/heating.yaml", policy.Resource{Kind: policy.KindFile, ID: "packages/heating.yaml"}},
		{"file:packages/heating.yaml/block/climate_schedule", policy.Resource{Kind: policy.KindFile, ID: "packages/heating.yaml", Sub: "block/climate_schedule"}},
		{"service:light.turn_on", policy.Resource{Kind: policy.KindService, ID: "light.turn_on"}},
		{"system:logs", policy.Resource{Kind: policy.KindSystem, ID: "logs"}},
		{"system:reload/automation", policy.Resource{Kind: policy.KindSystem, ID: "reload", Sub: "automation"}},
		{"system:check_config", policy.Resource{Kind: policy.KindSystem, ID: "check_config"}},
	} {
		got, err := policy.ParseResource(tc.in)
		if err != nil {
			t.Errorf("ParseResource(%q) = %v, want %+v", tc.in, err, tc.want)
			continue
		}
		if got != tc.want {
			t.Errorf("ParseResource(%q) = %+v, want %+v", tc.in, got, tc.want)
		}
		if got.String() != tc.in {
			t.Errorf("ParseResource(%q).String() = %q, want the input back", tc.in, got.String())
		}
	}
}

// TestParseResourceRefusesWhatTheGrammarDoesNot: every refusal ParseResource's
// doc and the manual's resources section list.
func TestParseResourceRefusesWhatTheGrammarDoesNot(t *testing.T) {
	for _, tc := range []struct{ in, why string }{
		{"light.kitchen", "missing `:`"},
		{"", "empty"},
		{"entiy:light.kitchen", "unknown kind"},
		{"Entity:light.kitchen", "kinds are lowercase exactly as written"},
		{"entity:light.*", "a resource holds no `*`"},
		{"entity:light.kitch?n", "a resource holds no `?`"},
		{"file:packages/**", "a resource holds no `**`"},
		{"entity:", "empty id"},
		{"automation:", "empty id"},
		{"file:a//b.yaml", "empty segment"},
		{"entity:light.kitchen/", "empty trailing segment"},
		{"helper:input_boolean", "helper needs <domain>/<id>"},
		{"system:nope", "system id outside its set"},
		{"system:logs/x", "only reload has a sub below system"},
		{"entity:light.kitchen/attrs/x", "unknown sub-resource word"},
		{"entity:light.kitchen/ATTR/x", "sub-resource words are lowercase exactly as written"},
		{"automation:x/run", "run is a script sub-resource, not an automation one"},
		{"entity:light.kitchen/registry/lables", "registry field outside the closed list"},
		{"device:abc/registry/icon", "icon is an entity registry field, not a device one"},
		{"entity:light.kitchen/state/x", "state takes no parameter"},
		{"entity:light.kitchen/attr/a/b", "more sub segments than the grammar allows"},
		{"service:light.turn_on/x", "service has no sub-resources"},
		{"area:kitchen/x", "area has no sub-resources"},
	} {
		if r, err := policy.ParseResource(tc.in); err == nil {
			t.Errorf("ParseResource(%q) = %+v, want a refusal (%s)", tc.in, r, tc.why)
		}
	}
}

// TestDecideAnswersForEveryKind: Decide never errors or panics and every
// verdict carries Level "A" (D-51), for one valid resource of every kind and
// every verb, with no Live.
func TestDecideAnswersForEveryKind(t *testing.T) {
	p := mustLoad(t, writePolicy(t, "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n"))
	examples := map[policy.Kind]string{
		policy.KindEntity: "entity:light.k", policy.KindDevice: "device:d1", policy.KindArea: "area:a",
		policy.KindFloor: "floor:f", policy.KindLabel: "label:l", policy.KindAutomation: "automation:x",
		policy.KindScript: "script:s", policy.KindTemplate: "template:u", policy.KindHelper: "helper:input_boolean/x",
		policy.KindDashboard: "dashboard:", policy.KindConfigEntry: "config_entry:e", policy.KindFile: "file:x.yaml",
		policy.KindService: "service:light.turn_on", policy.KindSystem: "system:cache",
	}
	for _, k := range policy.Kinds() {
		r := mustResource(t, examples[k])
		for _, v := range policy.AllVerbs() {
			got := p.Decide(r, v, nil)
			if got.Allowed || got.Rule != policy.RuleDefaultDeny || got.Level != "A" || got.Profile != "agent" {
				t.Errorf("%s %s = %+v, want denied by %q in agent at level A", r, v, got, policy.RuleDefaultDeny)
			}
		}
	}
}

// --- verbs (D-53) ---------------------------------------------------------------

func TestParseVerbAcceptsExactlyTheFiveVerbs(t *testing.T) {
	for _, v := range policy.AllVerbs() {
		got, err := policy.ParseVerb(v.String())
		if err != nil || got != v {
			t.Errorf("ParseVerb(%q) = (%v, %v), want %v", v.String(), got, err, v)
		}
	}
	if names := fmt.Sprint(policy.AllVerbs()); names != "[read control edit create delete]" {
		t.Errorf("AllVerbs() = %s, want the canonical order read control edit create delete", names)
	}

	_, err := policy.ParseVerb("write")
	if err == nil {
		t.Fatal(`ParseVerb("write") succeeded: write is a rule-only alias, a check asks about one verb (manual)`)
	}
	for _, w := range []string{"control", "edit", "create", "delete"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("ParseVerb(write) error %q does not name %q (the four verbs it stands for)", err, w)
		}
	}
	for _, bad := range []string{"wirte", "Read", "READ", " read", "read ", "", "explain"} {
		if v, err := policy.ParseVerb(bad); err == nil {
			t.Errorf("ParseVerb(%q) = %v, want a refusal (no trimming, no case folding)", bad, v)
		}
	}
}

func TestParseVerbsExpandsWriteAndCanonicalises(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want []policy.Verb
	}{
		{[]string{"write"}, []policy.Verb{policy.Control, policy.Edit, policy.Create, policy.Delete}},
		{[]string{"read", "write"}, policy.AllVerbs()},
		{[]string{"delete", "read"}, []policy.Verb{policy.Read, policy.Delete}},
		{[]string{"edit", "write", "edit"}, []policy.Verb{policy.Control, policy.Edit, policy.Create, policy.Delete}},
		{[]string{"read", "read"}, []policy.Verb{policy.Read}},
	} {
		got, err := policy.ParseVerbs(tc.in)
		if err != nil || !slices.Equal(got, tc.want) {
			t.Errorf("ParseVerbs(%q) = (%v, %v), want %v", tc.in, got, err, tc.want)
		}
	}
	if _, err := policy.ParseVerbs(nil); err == nil {
		t.Error("ParseVerbs(nil) succeeded; an empty list is an error")
	}
	if _, err := policy.ParseVerbs([]string{}); err == nil {
		t.Error("ParseVerbs([]) succeeded; an empty list is an error")
	}
	_, err := policy.ParseVerbs([]string{"read", "wirte"})
	if err == nil || !strings.Contains(err.Error(), "wirte") {
		t.Errorf("ParseVerbs([read wirte]) error = %v, want one naming %q", err, "wirte")
	}
}

// --- the manual's resources table vs KindSpecs ----------------------------------

var backticked = regexp.MustCompile("`([a-z_]+)`")

// manualResourcesSection returns the manual's "#### Resources" section.
func manualResourcesSection(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../../docs/manual.md")
	if err != nil {
		t.Fatal(err)
	}
	_, section, ok := strings.Cut(string(raw), "#### Resources")
	if !ok {
		t.Fatal("manual has no \"#### Resources\" section")
	}
	if before, _, found := strings.Cut(section, "\n#### "); found {
		section = before
	}
	return section
}

// manualKinds reads the kinds column of the resources table, in order.
func manualKinds(section string) []policy.Kind {
	var kinds []policy.Kind
	for line := range strings.SplitSeq(section, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		first := strings.Split(line, "|")[1]
		for _, m := range backticked.FindAllStringSubmatch(first, -1) {
			kinds = append(kinds, policy.Kind(m[1]))
		}
	}
	return kinds
}

// manualRegistryFields reads `field one of ...` from the table row for kind.
func manualRegistryFields(t *testing.T, section, row string) []string {
	t.Helper()
	_, line, ok := strings.Cut(section, row)
	if !ok {
		t.Fatalf("manual resources table has no row %q", row)
	}
	line, _, _ = strings.Cut(line, "\n")
	_, rest, ok := strings.Cut(line, "field one of")
	if !ok {
		t.Fatalf("row %q has no `field one of`", row)
	}
	rest, _, _ = strings.Cut(rest, "·")
	matches := backticked.FindAllStringSubmatch(rest, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

func specRegistryFields(k policy.Kind) []string {
	for _, s := range policy.KindSpecs() {
		if s.Kind != k {
			continue
		}
		for _, sub := range s.Subs {
			if sub.Word == "registry" {
				return sub.ParamValues
			}
		}
	}
	return nil
}

// TestKindsTableMatchesTheManual: the manual's resources table and KindSpecs
// are the same grammar (resource.go: "the two must agree"). Checks the kinds
// and their order, and the closed registry field lists.
func TestKindsTableMatchesTheManual(t *testing.T) {
	section := manualResourcesSection(t)
	if kinds := manualKinds(section); !slices.Equal(kinds, policy.Kinds()) {
		t.Errorf("manual kinds %q != Kinds() %q", kinds, policy.Kinds())
	}
	if m, s := manualRegistryFields(t, section, "| `entity` |"), specRegistryFields(policy.KindEntity); !slices.Equal(m, s) {
		t.Errorf("entity registry fields: manual %q, KindSpecs %q", m, s)
	}
	if m, s := manualRegistryFields(t, section, "| `device` |"), specRegistryFields(policy.KindDevice); !slices.Equal(m, s) {
		t.Errorf("device registry fields: manual %q, KindSpecs %q", m, s)
	}
}
