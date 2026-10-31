package policy_test

// Spec tests for D-63..D-68, the 2026-10-31 confirmations of D-52, D-55 and
// D-60, and the review findings of the phase-1 pass (_wt/p1-findings.json)
// that a static vector cannot express. Authored from docs/decisions.md, the
// manual's "Policy (permission guardrail)" section and the findings file; no
// implementation was read beyond exported signatures and doc comments.
//
// Fields the decisions add to exported types (Verdict.Partial,
// Verdict.PartialDenies, Unchecked.Profile/Rule/Line) are read by reflection
// (vectors_test.go), so this file compiles before they exist and fails, naming
// the missing field, until they do.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
)

// --- D-60 (confirmed): a policy.yaml that exists but cannot be read -------------

// TestLoadRefusesADanglingPolicySymlink — finding 0 (bug), D-60 confirmation:
// a policy.yaml (or policy.yml) entry that exists but whose target is gone is
// a *LoadError naming the path, never "no policy file" (allow-all). Red today:
// Load stats through the link, gets ErrNotExist and returns the no-file
// allow-all Policy.
func TestLoadRefusesADanglingPolicySymlink(t *testing.T) {
	for _, name := range []string{policy.FileName, "policy.yml"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			link := filepath.Join(dir, name)
			if err := os.Symlink(filepath.Join(dir, "moved-away", "policy.yaml"), link); err != nil {
				t.Skipf("symlinks unavailable: %v", err)
			}
			p, err := policy.Load(dir)
			if err == nil {
				t.Fatalf("Load with a dangling %s succeeded (Exists=%v Selection=%q AllowAll=%v); want a load error naming the path, never \"no policy file\"",
					name, p.Exists(), p.Selection(), p.AllowAll())
			}
			var le *policy.LoadError
			if !errors.As(err, &le) {
				t.Errorf("Load error is %T (%v), want a *policy.LoadError (D-60: a load error)", err, err)
			}
			if !strings.Contains(err.Error(), name) {
				t.Errorf("Load error %q does not name the path %s", err, link)
			}
			if p != nil {
				t.Errorf("Load returned a Policy beside its error")
			}
		})
	}
}

// TestLoadRefusesAnUnreadablePolicyFile — D-60 confirmation ("no permission"):
// a policy.yaml hactl may not read is an error naming the path, never no file.
func TestLoadRefusesAnUnreadablePolicyFile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads a mode-000 file")
	}
	dir := writePolicy(t, "version: 1\n")
	path := filepath.Join(dir, policy.FileName)
	if err := os.Chmod(path, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	p, err := policy.Load(dir)
	if err == nil {
		t.Fatalf("Load of an unreadable policy.yaml succeeded (Exists=%v AllowAll=%v); want an error naming the path", p.Exists(), p.AllowAll())
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("Load error %q does not name %s", err, path)
	}
}

// --- D-52 (confirmed): line, then column ------------------------------------------

// TestRulesAndVerdictsBreakSameLineTiesByColumn — finding 9 (bug), D-52
// confirmation: two rules of one flow mapping share a line; the first in the
// file is the leftmost. Both the named rule (vector e23) and Rules()' order —
// what `policy show` lists — follow the column, not the pattern text. Red
// today: ties on Line break by Pattern, so `entity:*` sorts before
// `entity:lock.*` and `entity:a.*` before `entity:z.*`.
func TestRulesAndVerdictsBreakSameLineTiesByColumn(t *testing.T) {
	p := mustLoad(t, writePolicy(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    deny: {"entity:lock.*": [write], "entity:*": [control]}
    allow: {"entity:zone.*": [read], "entity:all.*": [read], "entity:*": [read]}
`))
	got := make([]string, 0, len(p.Rules()))
	for _, r := range p.Rules() {
		got = append(got, string(r.Effect)+" "+r.Pattern)
	}
	want := []string{"deny entity:lock.*", "deny entity:*", "allow entity:zone.*", "allow entity:all.*", "allow entity:*"}
	if !slices.Equal(got, want) {
		t.Errorf("Rules() = %q, want %q (file order: line, then column)", got, want)
	}
	v := p.Decide(mustResource(t, "entity:lock.front_door"), policy.Control, nil)
	if v.Rule != `deny "entity:lock.*" [write]` || v.Line != 6 {
		t.Errorf("verdict names %q (line %d), want the leftmost deny `deny \"entity:lock.*\" [write]` (line 6)", v.Rule, v.Line)
	}
	v = p.Decide(mustResource(t, "entity:zone.home"), policy.Read, nil)
	if v.Rule != `allow "entity:zone.*" [read]` {
		t.Errorf("verdict names %q, want the leftmost allow `allow \"entity:zone.*\" [read]`", v.Rule)
	}
}

// --- D-55 (confirmed): a selector is evaluated only while it can matter ------------

// countingLive answers from fixed data, can fail every call, and counts every
// call per method.
type countingLive struct {
	mu     sync.Mutex
	fail   bool
	areas  map[string]policy.Ref
	labels map[string][]policy.Ref
	calls  map[string]int
}

func (l *countingLive) EntityArea(id string) (policy.Ref, bool, error) {
	l.count("EntityArea")
	if l.fail {
		return policy.Ref{}, false, errors.New("live: unreachable")
	}
	r, ok := l.areas[id]
	return r, ok, nil
}

func (l *countingLive) EntityLabels(id string) ([]policy.Ref, error) {
	l.count("EntityLabels")
	if l.fail {
		return nil, errors.New("live: unreachable")
	}
	return l.labels[id], nil
}

func (l *countingLive) DeviceArea(id string) (policy.Ref, bool, error) {
	l.count("DeviceArea")
	if l.fail {
		return policy.Ref{}, false, errors.New("live: unreachable")
	}
	r, ok := l.areas[id]
	return r, ok, nil
}

func (l *countingLive) DeviceLabels(id string) ([]policy.Ref, error) {
	l.count("DeviceLabels")
	if l.fail {
		return nil, errors.New("live: unreachable")
	}
	return l.labels[id], nil
}

func (l *countingLive) count(m string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.calls == nil {
		l.calls = map[string]int{}
	}
	l.calls[m]++
}

func (l *countingLive) n(m string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.calls[m]
}

func (l *countingLive) total() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, c := range l.calls {
		n += c
	}
	return n
}

// TestSelectorIsTriedOnlyWhileItCanChangeTheVerdict — finding 13 (decided:
// D-55 confirmation), the Decide half of "policy check contacts Home Assistant
// only when a where: selector can still change the verdict": never when a deny
// matches, never for a narrowed allow placed after a matching plain allow or
// after a narrowed allow that already granted; a tried selector that fails is
// noted and the next allow is tried; one never tried is neither evaluated nor
// noted. Red today: Decide evaluates every covering allow's where after one has
// already granted (and notes it).
func TestSelectorIsTriedOnlyWhileItCanChangeTheVerdict(t *testing.T) {
	plainFirst := mustLoad(t, writePolicy(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*": [control]
      "entity:light.*": [control]
    deny:
      "entity:lock.*": [control]
    where:
      "entity:light.*": { area: wohnzimmer }
`))
	t.Run("a plain allow on an earlier line makes the selector moot", func(t *testing.T) {
		for _, fail := range []bool{false, true} {
			live := &countingLive{fail: fail}
			v := plainFirst.Decide(mustResource(t, "entity:light.sofa"), policy.Control, live)
			if !v.Allowed || v.Line != 7 {
				t.Errorf("fail=%v: verdict %+v, want allowed by the plain allow on line 7", fail, v)
			}
			if live.total() != 0 {
				t.Errorf("fail=%v: the moot selector consulted Live %d time(s) (%v), want 0", fail, live.total(), live.calls)
			}
			if len(v.Notes) != 0 {
				t.Errorf("fail=%v: notes %q for a selector never tried, want none", fail, v.Notes)
			}
		}
	})
	t.Run("a matching deny decides before any selector", func(t *testing.T) {
		live := &countingLive{fail: true}
		v := plainFirst.Decide(mustResource(t, "entity:lock.door"), policy.Control, live)
		if v.Allowed || v.Line != 10 || live.total() != 0 || len(v.Notes) != 0 {
			t.Errorf("verdict %+v with %d Live call(s), want denied by line 10, no Live call, no note", v, live.total())
		}
	})
}

// TestNarrowedAllowsAreTriedInFileOrderUntilOneGrants — finding 13 / D-55
// confirmation, continued: narrowed allows are tried in file order; the walk
// stops at the first allow that grants (narrowed or plain), a tried selector
// that fails is noted, one never tried is neither evaluated nor noted. Red
// today: every covering narrowed allow is evaluated and noted.
func TestNarrowedAllowsAreTriedInFileOrderUntilOneGrants(t *testing.T) {
	narrowedFirst := mustLoad(t, writePolicy(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.*": [control]
      "entity:light.s*": [control]
      "entity:*": [control]
      "entity:light.so*": [control]
    where:
      "entity:light.*": { area: wohnzimmer }
      "entity:light.s*": { label: agent_ok }
      "entity:light.so*": { label: never_tried }
`))
	t.Run("a narrowed allow that grants stops the walk", func(t *testing.T) {
		live := &countingLive{areas: map[string]policy.Ref{"light.sofa": {ID: "wohnzimmer", Name: "Wohnzimmer"}}}
		v := narrowedFirst.Decide(mustResource(t, "entity:light.sofa"), policy.Control, live)
		if !v.Allowed || v.Line != 7 {
			t.Errorf("verdict %+v, want allowed by the narrowed allow on line 7", v)
		}
		if n := live.n("EntityLabels"); n != 0 {
			t.Errorf("label selectors after a granting allow consulted Live %d time(s), want 0", n)
		}
	})
	t.Run("a tried selector that fails is noted; the walk stops at the plain allow", func(t *testing.T) {
		live := &countingLive{fail: true}
		v := narrowedFirst.Decide(mustResource(t, "entity:light.sofa"), policy.Control, live)
		if !v.Allowed || v.Line != 9 {
			t.Errorf("verdict %+v, want allowed by the plain allow on line 9", v)
		}
		if len(v.Notes) != 2 {
			t.Fatalf("notes %q, want 2 (lines 7 and 8 tried and failed; line 10 never tried)", v.Notes)
		}
		if !strings.Contains(v.Notes[0], `"entity:light.*"`) || !strings.Contains(v.Notes[1], `"entity:light.s*"`) {
			t.Errorf("notes %q, want line 7's then line 8's allow named", v.Notes)
		}
		for _, n := range v.Notes {
			if strings.Contains(n, "never_tried") || strings.Contains(n, `"entity:light.so*"`) {
				t.Errorf("note %q names the selector placed after the plain allow", n)
			}
		}
	})
	t.Run("a selector that does not hold passes to the next allow", func(t *testing.T) {
		live := &countingLive{
			areas:  map[string]policy.Ref{"light.sofa": {ID: "kueche", Name: "Küche"}},
			labels: map[string][]policy.Ref{"light.sofa": {{ID: "agent_ok", Name: "Agent OK"}}},
		}
		v := narrowedFirst.Decide(mustResource(t, "entity:light.sofa"), policy.Control, live)
		if !v.Allowed || v.Line != 8 || len(v.Notes) != 0 {
			t.Errorf("verdict %+v, want allowed by line 8's label selector, no note", v)
		}
	})
}

// --- D-63: the full id only -------------------------------------------------------

// TestPolicyPatternsMatchTheFullIDOnly — D-63 (findings 1, 12, 19): the
// second anchor is the filters' (glob.MatchQualified), never the policy's.
// A dotless literal entity/service pattern is a broken file; a dotless glob is
// valid but matches only at the domain. Red today: `entity:front*` grants
// lock.front_door through the tail anchor, and the dotless literals load.
func TestPolicyPatternsMatchTheFullIDOnly(t *testing.T) {
	p := mustLoad(t, writePolicy(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:front*": [control]
      "entity:*.front_door": [read]
      "service:*.unlock": [control]
`))
	for _, tc := range []struct {
		res     string
		verb    policy.Verb
		allowed bool
	}{
		{"entity:lock.front_door", policy.Control, false},
		{"entity:lock.front_door", policy.Read, true},
		{"entity:cover.front_door", policy.Read, true},
		{"entity:lock.front_door_2", policy.Read, false},
		{"service:lock.unlock", policy.Control, true},
		{"service:lock.unlock_all", policy.Control, false},
	} {
		if v := p.Decide(mustResource(t, tc.res), tc.verb, nil); v.Allowed != tc.allowed {
			t.Errorf("%s %s = %+v, want allowed=%v (D-63: the full id only)", tc.res, tc.verb, v, tc.allowed)
		}
	}

	for _, pat := range []string{"entity:boiler", "service:turn_on", "entity:Front_Door"} {
		_, err := policy.Load(writePolicy(t, fmt.Sprintf("version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    allow:\n      %q: [control]\n", pat)))
		var le *policy.LoadError
		if !errors.As(err, &le) {
			t.Errorf("pattern %q loaded (err %v); D-63: a dotless literal entity/service id is a broken file", pat, err)
			continue
		}
		if len(le.Problems) != 1 || le.Problems[0].Line != 7 {
			t.Errorf("pattern %q: problems %+v, want one on line 7", pat, le.Problems)
		}
	}
}

// --- D-66: partial ----------------------------------------------------------------

// TestPartialVerdict — D-66 (finding 7): every write verb treats a partial as
// denied, naming the FIRST descendant deny (file order) and listing all of
// them; read stays allowed by its allow and lists the read denies; a resource
// with no descendant deny for the verb is whole. Red today: the parent is
// allowed outright and Verdict has no Partial/PartialDenies.
func TestPartialVerdict(t *testing.T) {
	p := mustLoad(t, writePolicy(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*": [read, write]
    deny:
      "entity:*/registry/entity_id": [write]
      "entity:*/attr/secret": [write, read]
`))
	first := `deny "entity:*/registry/entity_id" [write]`
	second := `deny "entity:*/attr/secret" [write read]`
	r := mustResource(t, "entity:light.x")
	for _, v := range []policy.Verb{policy.Control, policy.Edit, policy.Create, policy.Delete} {
		got := p.Decide(r, v, nil)
		if got.Allowed || got.Rule != first || got.Line != 9 {
			t.Errorf("%s %s = allowed=%v rule %q line %d, want denied by the first descendant deny %q (line 9)", r, v, got.Allowed, got.Rule, got.Line, first)
		}
		partial, denies, ok, why := verdictPartial(got)
		if !ok {
			t.Errorf("%s %s: %s", r, v, why)
			continue
		}
		want := []ruleRefExpect{{first, 9}, {second, 10}}
		if !partial || !slices.Equal(denies, want) {
			t.Errorf("%s %s: partial=%v denies=%+v, want true %+v", r, v, partial, denies, want)
		}
	}
	got := p.Decide(r, policy.Read, nil)
	if !got.Allowed || got.Rule != `allow "entity:*" [read write]` || got.Line != 7 {
		t.Errorf("%s read = %+v, want allowed by the allow on line 7", r, got)
	}
	if partial, denies, ok, why := verdictPartial(got); !ok {
		t.Errorf("%s read: %s", r, why)
	} else if !partial || !slices.Equal(denies, []ruleRefExpect{{second, 10}}) {
		t.Errorf("%s read: partial=%v denies=%+v, want true [%s line 10]", r, partial, denies, second)
	}
	// A resource below every descendant deny's branch is whole.
	whole := p.Decide(mustResource(t, "entity:light.x/state"), policy.Edit, nil)
	if !whole.Allowed {
		t.Errorf("entity:light.x/state edit = %+v, want allowed (no deny below it)", whole)
	}
	if partial, _, ok, why := verdictPartial(whole); !ok {
		t.Errorf("entity:light.x/state edit: %s", why)
	} else if partial {
		t.Errorf("entity:light.x/state edit is partial; no deny covers a descendant of it")
	}
}

// --- D-67: file ids ------------------------------------------------------------------

// TestParseResourceRefusesDotSegmentsAndAmbiguousBlock — D-67 (finding 4):
// `.`/`..` segments are refused, never canonicalised; a `block` segment (any
// case) is accepted only second to last with a path before it. Red today:
// every refused address but `file:block/x` parses.
func TestParseResourceRefusesDotSegmentsAndAmbiguousBlock(t *testing.T) {
	for _, in := range []string{
		"file:./secrets.yaml",
		"file:packages/../secrets.yaml",
		"file:packages/./x.yaml",
		"file:..",
		"file:.",
		"file:a/..",
		"file:a/block",
		"file:a/BLOCK",
		"file:a/block/b/c.yaml",
		"file:block/x",
		"file:Block/x",
		"file:block",
	} {
		if r, err := policy.ParseResource(in); err == nil {
			t.Errorf("ParseResource(%q) = %+v, want a refusal (D-67)", in, r)
		}
	}
	for in, want := range map[string]policy.Resource{
		"file:.storage/core.json":       {Kind: policy.KindFile, ID: ".storage/core.json"},
		"file:a..b.yaml":                {Kind: policy.KindFile, ID: "a..b.yaml"},
		"file:a/block/b":                {Kind: policy.KindFile, ID: "a", Sub: "block/b"},
		"file:pkgs/x.yaml/block/clmate": {Kind: policy.KindFile, ID: "pkgs/x.yaml", Sub: "block/clmate"},
		"file:blocks/x.yaml":            {Kind: policy.KindFile, ID: "blocks/x.yaml"},
	} {
		got, err := policy.ParseResource(in)
		if err != nil || got != want {
			t.Errorf("ParseResource(%q) = %+v, %v; want %+v", in, got, err, want)
		}
	}
	// The refusal says what is wrong.
	if _, err := policy.ParseResource("file:a/block/b/c.yaml"); err != nil && !strings.Contains(err.Error(), "block") {
		t.Errorf("refusal %q does not say the segment is ambiguous with the block sub-resource", err)
	}
}

// --- D-68: the id charset ----------------------------------------------------------

// charsetCase is an id template with one %s where the offending character goes.
type charsetCase struct {
	kind, tmpl string
}

// TestIDCharset — D-68 (finding 6, refined 2026-10-31): each restricted kind
// × a space, a tab, a `-`, a non-ASCII letter, a no-break space and a control
// character inside the id; each other kind × a control character and a
// format character (Cf) inside the id — an interior space is no longer
// refused there (TestInteriorSpaces); resources refused, patterns broken with
// their line. Then the characters that stay legal. The edges (leading and
// trailing white space) and the free sub segments are id_edges_spec_test.go.
func TestIDCharset(t *testing.T) {
	restricted := []charsetCase{
		{"entity", "light.kit%schen"},
		{"service", "light.turn%son"},
		{"helper", "input_boolean/guest%smode"},
		{"area", "wohn%szimmer"},
		{"floor", "erd%sgeschoss"},
		{"label", "agent%sok"},
	}
	other := []charsetCase{
		{"device", "abc%sdef"},
		{"automation", "pg%sheating"},
		{"script", "mor%sning"},
		{"template", "uid%s1"},
		{"dashboard", "main%sview"},
		{"config_entry", "01J%sABC"},
		{"file", "packages/my%sfile.yaml"},
	}
	check := func(c charsetCase, bad string) {
		t.Helper()
		id := fmt.Sprintf(c.tmpl, bad)
		res := c.kind + ":" + id
		if r, err := policy.ParseResource(res); err == nil {
			t.Errorf("ParseResource(%q) = %+v, want a refusal (D-68)", res, r)
		}
		body := fmt.Sprintf("version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    deny:\n      %q: [read]\n", res)
		_, err := policy.Load(writePolicy(t, body))
		var le *policy.LoadError
		if !errors.As(err, &le) {
			t.Errorf("pattern %q loaded (err %v), want a broken file (D-68)", res, err)
			return
		}
		if !slices.ContainsFunc(le.Problems, func(p policy.Problem) bool { return p.Line == 7 }) {
			t.Errorf("pattern %q: problems %+v, want one on line 7", res, le.Problems)
		}
	}
	for _, c := range restricted {
		for _, bad := range []string{" ", "\t", "-", "ü", "\u00a0", "\x01"} {
			check(c, bad)
		}
	}
	for _, c := range other {
		for _, bad := range []string{"\x01", "\x00", "\u200b", "\u202e"} {
			check(c, bad)
		}
	}
	// A trailing space on a concrete resource slips past an exact deny today.
	if r, err := policy.ParseResource("entity:lock.front_door "); err == nil {
		t.Errorf(`ParseResource("entity:lock.front_door ") = %+v, want a refusal`, r)
	}

	for _, ok := range []string{
		"entity:Light.Kitchen_2",
		"service:LIGHT.turn_on",
		"helper:input_boolean/Guest_Mode",
		"area:Wohnzimmer_1",
		"floor:EG",
		"label:agent_ok",
		"automation:Pg-Heizung_ü",
		"dashboard:energy-main",
		"device:0123abcd",
		"file:packages/küche.yaml",
		"template:sensor-uid.1",
	} {
		if _, err := policy.ParseResource(ok); err != nil {
			t.Errorf("ParseResource(%q) = %v, want it accepted (D-68 restricts only HA's own charsets and white space/control)", ok, err)
		}
	}
	// Patterns add * and ? to the restricted charsets.
	mustLoad(t, writePolicy(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.k?tchen*": [read]
      "service:*.turn_?n": [control]
      "helper:input_*/guest_?ode": [read]
      "area:wohn*": [read]
      "floor:e?": [read]
      "label:agent_*": [read]
`))
}

// --- lint: unreachable treats an allow's sub path as a set ---------------------------

func lintCodes(rep policy.LintReport, code string) []string {
	var out []string
	for _, f := range rep.Findings {
		if f.Code == code {
			out = append(out, fmt.Sprintf("%s@%d", f.Rule, f.Line))
		}
	}
	return out
}

func entityInventory(ids ...string) policy.Inventory {
	return fixtureInventory{e: &lintExpect{Inventory: map[string][]string{"entity": ids}}}
}

// TestLintUnreachableTreatsAGlobSubAsASet — findings 10 and 18 (bugs): an
// allow whose glob sub path still decides for some live sub-resource is not
// unreachable. Only the `unreachable` code is asserted, so whatever D-65 adds
// for attr names does not interfere. Red today: allDenied appends `attr/*`
// (`attr/l*`) as a literal sub and glob-matches the deny's `?` (`l?`) against
// it.
func TestLintUnreachableTreatsAGlobSubAsASet(t *testing.T) {
	for _, tc := range []struct {
		name, allow, deny string
	}{
		{"attr/* vs attr/?", `"entity:lock.*/attr/*"`, `"entity:lock.*/attr/?"`},
		{"attr/l* vs attr/l?", `"entity:device_tracker.jan/attr/l*"`, `"entity:device_tracker.jan/attr/l?"`},
		{"attr/* vs attr/lat*", `"entity:*/attr/*"`, `"entity:*/attr/lat*"`},
		{"attr (stops) vs attr/latitude", `"entity:*/attr"`, `"entity:*/attr/latitude"`},
		{"attr/latitude vs attr/longitude", `"entity:*/attr/latitude"`, `"entity:*/attr/longitude"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mustLoad(t, writePolicy(t, fmt.Sprintf(
				"version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    allow:\n      %s: [read]\n    deny:\n      %s: [read]\n", tc.allow, tc.deny)))
			rep := policy.Lint(p, entityInventory("lock.front_door", "device_tracker.jan"))
			if got := lintCodes(rep, policy.CodeUnreachable); len(got) != 0 {
				t.Errorf("unreachable %v, want none: the allow %s still decides for a sub-resource the deny %s leaves", got, tc.allow, tc.deny)
			}
		})
	}
}

// TestLintUnreachableHonoursTheAllowsSubPath — findings 28 and 15/M11
// (test-gaps): an allow on a sub path that a deny of the same profile covers
// for every live match is unreachable — the deny reaches it through a literal
// segment its glob matches, through `*` facing the allow's `*`, or by stopping
// above it. Goes red under the mutation `sub := ""` in allDenied (the allow's
// sub path ignored: the bare entity is never covered by a sub-path deny).
func TestLintUnreachableHonoursTheAllowsSubPath(t *testing.T) {
	for _, tc := range []struct {
		name, allow, deny string
	}{
		{"literal sub matched by the deny's glob", `"entity:*/attr/latitude"`, `"entity:*/attr/lat*"`},
		{"wildcard sub facing *", `"entity:*/attr/*"`, `"entity:*/attr/*"`},
		{"deny stops above the allow's sub", `"entity:lock.*/attr/*"`, `"entity:*/attr"`},
		{"registry field", `"entity:*/registry/labels"`, `"entity:*/registry/labels"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := mustLoad(t, writePolicy(t, fmt.Sprintf(
				"version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    allow:\n      %s: [edit]\n    deny:\n      %s: [edit]\n", tc.allow, tc.deny)))
			rep := policy.Lint(p, entityInventory("lock.front_door", "device_tracker.jan"))
			want := []string{fmt.Sprintf("allow %s [edit]@7", tc.allow)}
			if got := lintCodes(rep, policy.CodeUnreachable); !slices.Equal(got, want) {
				t.Errorf("unreachable %v, want %v (every live match's sub path is denied edit)", got, want)
			}
		})
	}
}
