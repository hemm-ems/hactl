package policy_test

// Conformance vectors for the policy model, authored from the contract only:
// docs/manual.md "Policy (permission guardrail)", docs/decisions.md D-2, D-28,
// D-51..D-62, the workspace's SPEC-permissions.md and the doc comments of this
// package's public API. Nothing here was derived from an implementation.
//
// Each directory under testdata/policy/vectors is one vector:
//
//   - policy.yaml / policy.yml — the instance directory's files, loaded as-is
//     (policy.Load is pointed at the vector directory itself, so the line numbers
//     a vector pins are the lines of its own policy.yaml; vector.yaml is not a
//     name Load reads);
//   - vector.yaml — what the contract says must come out (format below).
//
// vector.yaml (YAML; the generated ones are JSON, which is YAML):
//
//	clause:            the contract clause the vector pins (printed on failure)
//	as:                evaluate through Policy.As(<name>) — the --as what-if
//	as_error_contains: As must fail; its error must contain each string
//	load_error:        Load must fail with a *policy.LoadError
//	  file_is_policy_yaml: LoadError.File == <dir>/policy.yaml
//	  count:             exact number of problems (only where the contract pins it)
//	  problems: each must be matched by a distinct actual problem:
//	    line | line_in | line_positive | line_any, contains: [substrings of Message]
//	view:              Exists/Selection/Active/AllowAll/Default/Profiles/WhatIf
//	live:              the Live a case evaluates with (mode: fixture|nil|error)
//	cases:             (resource, verb) → allowed, rule | rule_contains, line,
//	                   profile, notes (count), notes_contains (per note index),
//	                   partial, partial_denies [{rule, line}] (D-66)
//	lint:              policy.Lint over a fixture Inventory (or nil_inventory),
//	                   findings compared in order, unchecked compared in order
//
// Every verdict is additionally held to Level == "A" (D-51) and WhatIf == (as
// set) (D-62). A field a vector omits is not asserted: the vectors pin only
// what the contract specifies.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/policy"
)

const vectorRoot = "../../testdata/policy/vectors"

type vector struct {
	Clause          string       `yaml:"clause"`
	As              string       `yaml:"as"`
	AsErrorContains *[]string    `yaml:"as_error_contains"`
	LoadError       *loadExpect  `yaml:"load_error"`
	View            *viewExpect  `yaml:"view"`
	Live            *liveFixture `yaml:"live"`
	Cases           []caseExpect `yaml:"cases"`
	Lint            *lintExpect  `yaml:"lint"`
}

type loadExpect struct {
	FileIsPolicyYAML bool            `yaml:"file_is_policy_yaml"`
	Count            *int            `yaml:"count"`
	Problems         []problemExpect `yaml:"problems"`
}

type problemExpect struct {
	Line         *int     `yaml:"line"`
	LineIn       []int    `yaml:"line_in"`
	LinePositive bool     `yaml:"line_positive"`
	LineAny      bool     `yaml:"line_any"`
	Contains     []string `yaml:"contains"`
}

type viewExpect struct {
	Exists     *bool     `yaml:"exists"`
	SelectedBy *string   `yaml:"selected_by"`
	Active     *string   `yaml:"active"`
	AllowAll   *bool     `yaml:"allow_all"`
	Default    *string   `yaml:"default"`
	Profiles   *[]string `yaml:"profiles"`
	WhatIf     *bool     `yaml:"what_if"`
}

type caseExpect struct {
	Resource      string       `yaml:"resource"`
	Verb          string       `yaml:"verb"`
	Allowed       *bool        `yaml:"allowed"`
	Rule          *string      `yaml:"rule"`
	RuleContains  []string     `yaml:"rule_contains"`
	Line          *int         `yaml:"line"`
	Profile       *string      `yaml:"profile"`
	Notes         *int         `yaml:"notes"`
	NotesContains [][]string   `yaml:"notes_contains"`
	Live          *liveFixture `yaml:"live"`
	// Partial and PartialDenies pin D-66. The Go fields they are read from
	// (Verdict.Partial, Verdict.PartialDenies) mirror the --json keys the
	// manual fixes (`partial`, `partial_denies`); they are read by reflection
	// so this runner compiles before D-66 is built.
	Partial       *bool            `yaml:"partial"`
	PartialDenies *[]ruleRefExpect `yaml:"partial_denies"`
}

// ruleRefExpect is one entry of a partial verdict's descendant denies.
type ruleRefExpect struct {
	Rule string `yaml:"rule"`
	Line int    `yaml:"line"`
}

type lintExpect struct {
	NilInventory bool                `yaml:"nil_inventory"`
	Inventory    map[string][]string `yaml:"inventory"`
	Errors       map[string]string   `yaml:"errors"`
	Findings     []findingExpect     `yaml:"findings"`
	Unchecked    []uncheckedExpect   `yaml:"unchecked"`
}

type findingExpect struct {
	Code    string `yaml:"code"`
	Profile string `yaml:"profile"`
	Rule    string `yaml:"rule"`
	Line    *int   `yaml:"line"`
}

type uncheckedExpect struct {
	Kind   string `yaml:"kind"`
	Reason string `yaml:"reason"`
	// Profile, Rule and Line pin D-65's per-rule entries (manual "policy
	// lint" --json: `profile`/`rule` "" and `line` 0 for a kind's own entry).
	// Read by reflection from Unchecked's fields of the same names, so the
	// runner compiles before they exist; asserted only when present here.
	Profile *string `yaml:"profile"`
	Rule    *string `yaml:"rule"`
	Line    *int    `yaml:"line"`
}

// --- the Live fixture -------------------------------------------------------

type refYAML struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
}

type liveObj struct {
	Area   *refYAML  `yaml:"area"`
	Labels []refYAML `yaml:"labels"`
}

// liveFixture is the vectors' Live. mode "nil" passes a nil Live (an untyped
// nil interface, not a typed nil pointer), mode "error" fails every call.
type liveFixture struct {
	Mode     string             `yaml:"mode"`
	Error    string             `yaml:"error"`
	Entities map[string]liveObj `yaml:"entities"`
	Devices  map[string]liveObj `yaml:"devices"`
}

type fixtureLive struct{ f *liveFixture }

func toRefs(in []refYAML) []policy.Ref {
	out := make([]policy.Ref, 0, len(in))
	for _, r := range in {
		out = append(out, policy.Ref{ID: r.ID, Name: r.Name})
	}
	return out
}

func (l fixtureLive) EntityArea(id string) (policy.Ref, bool, error) {
	if l.f.Mode == "error" {
		return policy.Ref{}, false, l.fail()
	}
	o, ok := l.f.Entities[id]
	if !ok || o.Area == nil {
		return policy.Ref{}, false, nil
	}
	return policy.Ref{ID: o.Area.ID, Name: o.Area.Name}, true, nil
}

func (l fixtureLive) EntityLabels(id string) ([]policy.Ref, error) {
	if l.f.Mode == "error" {
		return nil, l.fail()
	}
	return toRefs(l.f.Entities[id].Labels), nil
}

func (l fixtureLive) DeviceArea(id string) (policy.Ref, bool, error) {
	if l.f.Mode == "error" {
		return policy.Ref{}, false, l.fail()
	}
	o, ok := l.f.Devices[id]
	if !ok || o.Area == nil {
		return policy.Ref{}, false, nil
	}
	return policy.Ref{ID: o.Area.ID, Name: o.Area.Name}, true, nil
}

func (l fixtureLive) DeviceLabels(id string) ([]policy.Ref, error) {
	if l.f.Mode == "error" {
		return nil, l.fail()
	}
	return toRefs(l.f.Devices[id].Labels), nil
}

func (l fixtureLive) fail() error {
	msg := l.f.Error
	if msg == "" {
		msg = "live fixture: unavailable"
	}
	return errors.New(msg)
}

// liveFor builds the Live a case runs with. A nil fixture and mode "nil" both
// mean "no Live": the untyped nil interface Decide documents.
func liveFor(f *liveFixture) policy.Live {
	if f == nil || f.Mode == "nil" {
		return nil
	}
	return fixtureLive{f: f}
}

// --- the Inventory fixture --------------------------------------------------

type fixtureInventory struct{ e *lintExpect }

func (i fixtureInventory) IDs(kind policy.Kind) ([]string, error) {
	if msg, ok := i.e.Errors[string(kind)]; ok {
		return nil, errors.New(msg)
	}
	if ids, ok := i.e.Inventory[string(kind)]; ok {
		return slices.Clone(ids), nil
	}
	return nil, policy.ErrNotEnumerated
}

// --- the runner -------------------------------------------------------------

func loadVectors(t *testing.T) []string {
	t.Helper()
	entries, err := os.ReadDir(vectorRoot)
	if err != nil {
		t.Fatalf("reading %s: %v", vectorRoot, err)
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() {
			dirs = append(dirs, e.Name())
		}
	}
	// The corpus is known to be large; a walker that found almost nothing
	// must not report a perfect score over nothing (TC-7's posture).
	if len(dirs) < 60 {
		t.Fatalf("found %d vectors under %s, want at least 60 — the corpus did not load", len(dirs), vectorRoot)
	}
	return dirs
}

func readVector(t *testing.T, dir string) vector {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "vector.yaml")) //nolint:gosec // the test reads its own testdata directory
	if err != nil {
		t.Fatalf("reading vector: %v", err)
	}
	var v vector
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&v); err != nil {
		t.Fatalf("vector.yaml does not decode (a typo in a vector must not silently assert nothing): %v", err)
	}
	if v.Clause == "" {
		t.Fatalf("vector has no clause")
	}
	return v
}

// TestPolicyConformanceVectors runs every vector through the public API:
// Load, As, ParseResource, ParseVerb, Decide and Lint.
func TestPolicyConformanceVectors(t *testing.T) {
	for _, name := range loadVectors(t) {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(vectorRoot, name)
			v := readVector(t, dir)
			runVector(t, dir, v)
		})
	}
}

func runVector(t *testing.T, dir string, v vector) {
	t.Helper()
	p, err := policy.Load(dir)

	if v.LoadError != nil {
		checkLoadError(t, dir, v, p, err)
		return
	}
	if err != nil {
		t.Fatalf("[%s]\nLoad(%s) = error %v, want a policy", v.Clause, dir, err)
	}
	if p == nil {
		t.Fatalf("[%s]\nLoad returned a nil policy and no error", v.Clause)
	}

	if v.AsErrorContains != nil {
		_, asErr := p.As(v.As)
		if asErr == nil {
			t.Fatalf("[%s]\nAs(%q) succeeded, want a refusal", v.Clause, v.As)
		}
		for _, s := range *v.AsErrorContains {
			if !strings.Contains(asErr.Error(), s) {
				t.Errorf("[%s]\nAs(%q) error %q does not name %q", v.Clause, v.As, asErr, s)
			}
		}
		return
	}

	whatIf := false
	if v.As != "" {
		view, asErr := p.As(v.As)
		if asErr != nil {
			t.Fatalf("[%s]\nAs(%q) = %v", v.Clause, v.As, asErr)
		}
		if view == nil {
			t.Fatalf("[%s]\nAs(%q) returned nil and no error", v.Clause, v.As)
		}
		p = view
		whatIf = true
	}

	if v.View != nil {
		checkView(t, v, p)
	}
	for _, c := range v.Cases {
		checkCase(t, v, p, c, whatIf)
	}
	if v.Lint != nil {
		checkLint(t, v, p)
	}
}

func checkLoadError(t *testing.T, dir string, v vector, p *policy.Policy, err error) {
	t.Helper()
	if err == nil {
		t.Fatalf("[%s]\nLoad succeeded (policy %+v), want a *policy.LoadError — a broken file must fail closed (D-60)", v.Clause, p)
	}
	var le *policy.LoadError
	if !errors.As(err, &le) {
		t.Fatalf("[%s]\nLoad error is %T (%v), want a *policy.LoadError", v.Clause, err, err)
	}
	if p != nil {
		t.Errorf("[%s]\nLoad returned a Policy beside its LoadError — a broken file never yields a Policy", v.Clause)
	}
	want := filepath.Join(dir, policy.FileName)
	if v.LoadError.FileIsPolicyYAML && le.File != want {
		t.Errorf("[%s]\nLoadError.File = %q, want %q", v.Clause, le.File, want)
	}
	if len(le.Problems) == 0 {
		t.Fatalf("[%s]\nLoadError carries no problems", v.Clause)
	}
	if v.LoadError.Count != nil && len(le.Problems) != *v.LoadError.Count {
		t.Errorf("[%s]\n%d problems, want exactly %d: %+v", v.Clause, len(le.Problems), *v.LoadError.Count, le.Problems)
	}
	// Ordered by Line, then Message — never by map order (H-16).
	sorted := slices.IsSortedFunc(le.Problems, func(a, b policy.Problem) int {
		if a.Line != b.Line {
			return a.Line - b.Line
		}
		return strings.Compare(a.Message, b.Message)
	})
	if !sorted {
		t.Errorf("[%s]\nproblems are not ordered by (Line, Message): %+v", v.Clause, le.Problems)
	}
	used := make([]bool, len(le.Problems))
	for _, want := range v.LoadError.Problems {
		found := -1
		for i, got := range le.Problems {
			if used[i] || !problemMatches(want, got) {
				continue
			}
			found = i
			break
		}
		if found < 0 {
			t.Errorf("[%s]\nno problem matches %+v (line %s); got %+v", v.Clause, want, lineDesc(want), le.Problems)
			continue
		}
		used[found] = true
	}
	// Every problem with a line is rendered `<file>:<line>: <message>`.
	text := le.Error()
	for _, pr := range le.Problems {
		if pr.Message == "" {
			t.Errorf("[%s]\na problem with an empty message: %+v", v.Clause, pr)
		}
		if pr.Line > 0 {
			prefix := le.File + ":" + strconv.Itoa(pr.Line) + ": "
			if !strings.Contains(text, prefix+pr.Message) {
				t.Errorf("[%s]\nError() %q does not carry %q", v.Clause, text, prefix+pr.Message)
			}
		}
	}
}

func lineDesc(p problemExpect) string {
	switch {
	case p.Line != nil:
		return strconv.Itoa(*p.Line)
	case len(p.LineIn) > 0:
		return fmt.Sprintf("one of %v", p.LineIn)
	case p.LineAny:
		return "any"
	default:
		return "> 0"
	}
}

func problemMatches(want problemExpect, got policy.Problem) bool {
	switch {
	case want.Line != nil:
		if got.Line != *want.Line {
			return false
		}
	case len(want.LineIn) > 0:
		if !slices.Contains(want.LineIn, got.Line) {
			return false
		}
	case want.LineAny:
	default: // line_positive, and the default: a content problem names its line
		if got.Line <= 0 {
			return false
		}
	}
	for _, s := range want.Contains {
		if !strings.Contains(got.Message, s) {
			return false
		}
	}
	return true
}

func checkView(t *testing.T, v vector, p *policy.Policy) {
	t.Helper()
	w := v.View
	if w.Exists != nil && p.Exists() != *w.Exists {
		t.Errorf("[%s]\nExists() = %v, want %v", v.Clause, p.Exists(), *w.Exists)
	}
	if w.SelectedBy != nil && string(p.Selection()) != *w.SelectedBy {
		t.Errorf("[%s]\nSelection() = %q, want %q", v.Clause, p.Selection(), *w.SelectedBy)
	}
	if w.Active != nil && p.Active() != *w.Active {
		t.Errorf("[%s]\nActive() = %q, want %q", v.Clause, p.Active(), *w.Active)
	}
	if w.AllowAll != nil && p.AllowAll() != *w.AllowAll {
		t.Errorf("[%s]\nAllowAll() = %v, want %v", v.Clause, p.AllowAll(), *w.AllowAll)
	}
	if w.Default != nil && string(p.Default()) != *w.Default {
		t.Errorf("[%s]\nDefault() = %q, want %q", v.Clause, p.Default(), *w.Default)
	}
	if w.Profiles != nil && !slices.Equal(p.Profiles(), *w.Profiles) {
		t.Errorf("[%s]\nProfiles() = %q, want %q", v.Clause, p.Profiles(), *w.Profiles)
	}
	if w.WhatIf != nil && p.WhatIf() != *w.WhatIf {
		t.Errorf("[%s]\nWhatIf() = %v, want %v", v.Clause, p.WhatIf(), *w.WhatIf)
	}
}

func checkCase(t *testing.T, v vector, p *policy.Policy, c caseExpect, whatIf bool) {
	t.Helper()
	label := "[" + v.Clause + "]\n" + c.Resource + " " + c.Verb
	if c.Allowed == nil {
		t.Fatalf("%s: vector case has no `allowed`", label)
	}
	r, err := policy.ParseResource(c.Resource)
	if err != nil {
		t.Fatalf("%s: ParseResource(%q) = %v — the vector's resource is valid per the manual's grammar", label, c.Resource, err)
	}
	verb, err := policy.ParseVerb(c.Verb)
	if err != nil {
		t.Fatalf("%s: ParseVerb(%q) = %v", label, c.Verb, err)
	}
	if verb.String() != c.Verb {
		t.Errorf("%s: ParseVerb(%q).String() = %q", label, c.Verb, verb.String())
	}
	live := v.Live
	if c.Live != nil {
		live = c.Live
	}
	got := p.Decide(r, verb, liveFor(live))

	if got.Allowed != *c.Allowed {
		t.Errorf("%s: Allowed = %v, want %v (rule %q)", label, got.Allowed, *c.Allowed, got.Rule)
	}
	if c.Rule != nil && got.Rule != *c.Rule {
		t.Errorf("%s: Rule = %q, want %q", label, got.Rule, *c.Rule)
	}
	for _, s := range c.RuleContains {
		if !strings.Contains(got.Rule, s) {
			t.Errorf("%s: Rule %q does not contain %q", label, got.Rule, s)
		}
	}
	if c.Line != nil && got.Line != *c.Line {
		t.Errorf("%s: Line = %d, want %d (rule %q)", label, got.Line, *c.Line, got.Rule)
	}
	if c.Profile != nil && got.Profile != *c.Profile {
		t.Errorf("%s: Profile = %q, want %q", label, got.Profile, *c.Profile)
	}
	if got.Level != "A" {
		t.Errorf("%s: Level = %q, want %q (D-51)", label, got.Level, "A")
	}
	if got.WhatIf != whatIf {
		t.Errorf("%s: WhatIf = %v, want %v (D-62)", label, got.WhatIf, whatIf)
	}
	if c.Notes != nil && len(got.Notes) != *c.Notes {
		t.Errorf("%s: %d notes, want %d: %q", label, len(got.Notes), *c.Notes, got.Notes)
	}
	checkPartial(t, label, got, c)
	for i, want := range c.NotesContains {
		if i >= len(got.Notes) {
			t.Errorf("%s: note %d missing (notes %q)", label, i, got.Notes)
			continue
		}
		for _, s := range want {
			if !strings.Contains(got.Notes[i], s) {
				t.Errorf("%s: note %d %q does not contain %q", label, i, got.Notes[i], s)
			}
		}
	}
}

func checkLint(t *testing.T, v vector, p *policy.Policy) {
	t.Helper()
	var inv policy.Inventory
	if !v.Lint.NilInventory {
		inv = fixtureInventory{e: v.Lint}
	}
	rep := policy.Lint(p, inv)
	if rep.Findings == nil || rep.Unchecked == nil {
		t.Errorf("[%s]\nLintReport has a nil slice (Findings nil=%v, Unchecked nil=%v) — both render as [] in --json",
			v.Clause, rep.Findings == nil, rep.Unchecked == nil)
	}
	if len(rep.Findings) != len(v.Lint.Findings) {
		t.Errorf("[%s]\n%d findings, want %d:\n got %+v\nwant %+v", v.Clause, len(rep.Findings), len(v.Lint.Findings), rep.Findings, v.Lint.Findings)
	} else {
		for i, want := range v.Lint.Findings {
			got := rep.Findings[i]
			if got.Code != want.Code || got.Profile != want.Profile || got.Rule != want.Rule ||
				(want.Line != nil && got.Line != *want.Line) {
				t.Errorf("[%s]\nfinding %d = %+v, want %+v (line %v)", v.Clause, i, got, want, derefLine(want.Line))
			}
			if got.Message == "" {
				t.Errorf("[%s]\nfinding %d has no message", v.Clause, i)
			}
		}
	}
	if len(rep.Unchecked) != len(v.Lint.Unchecked) {
		t.Errorf("[%s]\nunchecked = %+v, want %+v", v.Clause, rep.Unchecked, v.Lint.Unchecked)
		return
	}
	for i, want := range v.Lint.Unchecked {
		got := rep.Unchecked[i]
		if string(got.Kind) != want.Kind || got.Reason != want.Reason {
			t.Errorf("[%s]\nunchecked %d = %+v, want %+v", v.Clause, i, got, want)
		}
		checkUncheckedRule(t, v.Clause, i, got, want)
	}
}

func derefLine(p *int) any {
	if p == nil {
		return "any"
	}
	return *p
}

// --- D-66 / D-65 fields, read by reflection ------------------------------------

// structField returns v's exported field name, or ok=false when the type has
// no such field (the decision is not built yet).
func structField(v any, name string) (reflect.Value, bool) {
	rv := reflect.ValueOf(v)
	for rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if rv.Kind() != reflect.Struct {
		return reflect.Value{}, false
	}
	f := rv.FieldByName(name)
	return f, f.IsValid()
}

// ruleRefOf reads one descendant-deny entry: a struct with a Text() method
// (a policy.Rule) or a Rule string field, and a Line int field.
func ruleRefOf(e reflect.Value) (ruleRefExpect, bool) {
	var out ruleRefExpect
	if e.Kind() == reflect.String {
		out.Rule = e.String()
		out.Line = -1
		return out, true
	}
	if m := e.MethodByName("Text"); m.IsValid() && m.Type().NumIn() == 0 && m.Type().NumOut() == 1 {
		out.Rule = m.Call(nil)[0].String()
	} else if f := e.FieldByName("Rule"); f.IsValid() && f.Kind() == reflect.String {
		out.Rule = f.String()
	} else {
		return out, false
	}
	if f := e.FieldByName("Line"); f.IsValid() && f.CanInt() {
		out.Line = int(f.Int())
	} else {
		out.Line = -1
	}
	return out, true
}

// verdictPartial reads Verdict.Partial and Verdict.PartialDenies (D-66).
// ok is false, with a reason, when the fields do not exist.
func verdictPartial(got policy.Verdict) (partial bool, denies []ruleRefExpect, ok bool, why string) {
	pf, has := structField(got, "Partial")
	if !has || pf.Kind() != reflect.Bool {
		return false, nil, false, "policy.Verdict has no bool field Partial (D-66 not built)"
	}
	df, has := structField(got, "PartialDenies")
	if !has || df.Kind() != reflect.Slice {
		return pf.Bool(), nil, false, "policy.Verdict has no slice field PartialDenies (D-66 not built)"
	}
	for i := range df.Len() {
		r, ok := ruleRefOf(df.Index(i))
		if !ok {
			return pf.Bool(), nil, false, fmt.Sprintf("PartialDenies[%d] (%s) carries no rule text", i, df.Index(i).Type())
		}
		denies = append(denies, r)
	}
	return pf.Bool(), denies, true, ""
}

func checkPartial(t *testing.T, label string, got policy.Verdict, c caseExpect) {
	t.Helper()
	if c.Partial == nil && c.PartialDenies == nil {
		return
	}
	partial, denies, ok, why := verdictPartial(got)
	if !ok {
		t.Errorf("%s: %s", label, why)
		return
	}
	if c.Partial != nil && partial != *c.Partial {
		t.Errorf("%s: Partial = %v, want %v (denies %+v)", label, partial, *c.Partial, denies)
	}
	if c.PartialDenies != nil {
		want := *c.PartialDenies
		if len(denies) != len(want) {
			t.Errorf("%s: PartialDenies = %+v, want %+v", label, denies, want)
			return
		}
		for i := range want {
			if denies[i].Rule != want[i].Rule || (denies[i].Line >= 0 && denies[i].Line != want[i].Line) {
				t.Errorf("%s: PartialDenies[%d] = %+v, want %+v", label, i, denies[i], want[i])
			}
			if denies[i].Line < 0 {
				t.Errorf("%s: PartialDenies[%d] carries no Line", label, i)
			}
		}
	}
}

// checkUncheckedRule asserts the per-rule fields of an Unchecked entry (D-65).
func checkUncheckedRule(t *testing.T, clause string, i int, got policy.Unchecked, want uncheckedExpect) {
	t.Helper()
	if want.Profile == nil && want.Rule == nil && want.Line == nil {
		return
	}
	for _, f := range []struct {
		name string
		want any
	}{{"Profile", want.Profile}, {"Rule", want.Rule}, {"Line", want.Line}} {
		fv, has := structField(got, f.name)
		if !has {
			t.Errorf("[%s]\nunchecked %d: policy.Unchecked has no field %s (D-65 per-rule entries not built)", clause, i, f.name)
			continue
		}
		switch w := f.want.(type) {
		case *string:
			if w != nil && (fv.Kind() != reflect.String || fv.String() != *w) {
				t.Errorf("[%s]\nunchecked %d: %s = %v, want %q", clause, i, f.name, fv, *w)
			}
		case *int:
			if w != nil && (!fv.CanInt() || int(fv.Int()) != *w) {
				t.Errorf("[%s]\nunchecked %d: %s = %v, want %d", clause, i, f.name, fv, *w)
			}
		}
	}
}
