package testaudit_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/testaudit"
)

// repoRoot walks up from the test's working directory to the module root, so
// the gate keeps working if this package is ever moved.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("no go.mod above %s", dir)
		}
		dir = parent
	}
}

// tiers are the tiers the gate must see. Naming them is the point: the tests
// that talk to a real Home Assistant live behind build tags, and a scan that
// only saw what the untagged build compiles would be blind to exactly the tier
// where a liveness-only test does the most damage.
var tiers = []string{"unit", "integration", "companion", "discovery"}

// TestAssertionFloor is the gate. Every test function in the repository, in
// every tier, must be able to fail for a reason other than the process dying.
//
// See the package doc for what counts as an assertion and what this
// deliberately does not judge.
func TestAssertionFloor(t *testing.T) {
	tests, err := testaudit.ScanRepo(repoRoot(t))
	if err != nil {
		t.Fatalf("scanning repository: %v", err)
	}

	type tally struct{ total, asserting, exempt int }
	byTier := map[string]*tally{}
	for _, name := range tiers {
		byTier[name] = &tally{}
	}

	var offenders, staleExemptions, thinReasons []string
	for _, tc := range tests {
		c, ok := byTier[tc.Tier()]
		if !ok {
			t.Fatalf("%s:%d %s: unknown tier %q — a new build tag needs a tier name in the gate",
				tc.File, tc.Line, tc.Name, tc.Tier())
		}
		c.total++
		switch {
		case tc.Asserts:
			c.asserting++
			if tc.ExemptReason != "" {
				staleExemptions = append(staleExemptions,
					entryf(tc, "asserts, so its "+testaudit.ExemptDirective+" directive is stale — delete it"))
			}
		case tc.ExemptReason != "":
			c.exempt++
			if testaudit.ExemptReasonTooShort(tc.ExemptReason) {
				thinReasons = append(thinReasons, entryf(tc, "exemption reason is too thin to review: "+tc.ExemptReason))
			}
		default:
			offenders = append(offenders, entryf(tc, "reaches no failure site that observes a value"))
		}
	}

	// The tally is printed unconditionally: it is the number this gate exists
	// to hold at zero, and a gate whose output you never see is one you stop
	// believing. `-v` on the make target puts it in the build log.
	for _, name := range tiers {
		c := byTier[name]
		t.Logf("tier=%-11s tests=%-4d asserting=%-4d exempt=%-2d missing=%d",
			name, c.total, c.asserting, c.exempt, c.total-c.asserting-c.exempt)
	}

	// A scan that found nothing would report a perfect score. Refuse to pass
	// on an empty or lopsided corpus: each tier is known to be non-trivial, so
	// a walker that stopped early or a build tag that stopped matching shows up
	// here rather than as a silent green (TC-7 — derived, never hand-counted,
	// but the derivation itself has to be alive).
	for _, name := range tiers {
		if byTier[name].total < 5 {
			t.Errorf("tier %q has only %d test functions — the scan is broken, not the tier",
				name, byTier[name].total)
		}
	}

	report := func(what string, list []string) {
		if len(list) == 0 {
			return
		}
		sort.Strings(list)
		t.Errorf("%d test function(s) %s:\n%s", len(list), what, strings.Join(list, "\n"))
	}
	report("run a command and then discard the answer", offenders)
	report("carry a stale exemption", staleExemptions)
	report("carry an unreviewable exemption", thinReasons)

	if len(offenders) > 0 {
		t.Logf("A test must be able to go red for a reason other than the process dying.\n"+
			"Assert what the command was supposed to answer. If — and only if — there is\n"+
			"genuinely nothing to observe, say so in the test's doc comment:\n"+
			"\n\t%s <why this test can only prove liveness>\n", testaudit.ExemptDirective)
	}
}

func entryf(tc testaudit.Test, why string) string {
	return fmt.Sprintf("\t%s:%d %s (%s) — %s", tc.File, tc.Line, tc.Name, tc.Tier(), why)
}

// ---------------------------------------------------------------------------
// the classifier's own tests
//
// The gate is only worth its build time if its verdict is the one the package
// doc claims. These cases pin the two decisions everything else rests on: an
// `err != nil` check is not an assertion, and a helper that hands a value back
// is not an assertion helper however many times it calls t.Fatalf inside.
// ---------------------------------------------------------------------------

// fixtureTest is the name every classifier fixture below declares. The
// fixtures are one function each, so the name carries no information and is
// fixed here rather than repeated at five call sites.
const fixtureTest = "TestX"

// classify parses one throwaway package and returns the verdict on fixtureTest.
func classify(t *testing.T, src string) testaudit.Test {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte("package p\n\nimport \"testing\"\n\n"+src), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	tests, err := testaudit.ScanRepo(dir)
	if err != nil {
		t.Fatalf("scanning fixture: %v", err)
	}
	for _, tc := range tests {
		if tc.Name == fixtureTest {
			return tc
		}
	}
	t.Fatalf("fixture defines no test named %q (parsed %d)", fixtureTest, len(tests))
	return testaudit.Test{}
}

func TestClassifierVerdicts(t *testing.T) {
	cases := []struct {
		name string
		want bool
		src  string
	}{{
		name: "discarded output",
		want: false,
		src: `func TestX(t *testing.T) {
	out := run(t, "ls")
	_ = out
}`,
	}, {
		// Renaming the variable is the obvious way to beat a grep. It must not
		// beat this.
		name: "output consumed by a Logf",
		want: false,
		src: `func TestX(t *testing.T) {
	answer := run(t, "ls")
	t.Logf("got %s", answer)
}`,
	}, {
		name: "error check only",
		want: false,
		src: `func TestX(t *testing.T) {
	if err := do(); err != nil {
		t.Fatalf("do: %v", err)
	}
}`,
	}, {
		name: "two error checks and a named error variable",
		want: false,
		src: `func TestX(t *testing.T) {
	out, callErr := run()
	if callErr != nil {
		t.Fatalf("run: %v", callErr)
	}
	if closeErr := out.Close(); closeErr != nil {
		t.Errorf("close: %v", closeErr)
	}
}`,
	}, {
		name: "skip is not an assertion",
		want: false,
		src: `func TestX(t *testing.T) {
	out := run(t)
	if out == "" {
		t.Skip("nothing to check")
	}
}`,
	}, {
		name: "content check",
		want: true,
		src: `func TestX(t *testing.T) {
	out := run(t, "ls")
	if !strings.Contains(out, "entity_id") {
		t.Errorf("no header: %s", out)
	}
}`,
	}, {
		name: "required refusal",
		want: true,
		src: `func TestX(t *testing.T) {
	_, err := do()
	if err == nil {
		t.Fatal("expected a refusal")
	}
}`,
	}, {
		name: "search loop with a bare Fatal",
		want: true,
		src: `func TestX(t *testing.T) {
	for _, row := range rows(t) {
		if row.ID == "want" {
			return
		}
	}
	t.Fatal("row not found")
}`,
	}, {
		name: "assertion inside a subtest closure",
		want: true,
		src: `func TestX(t *testing.T) {
	t.Run("sub", func(t *testing.T) {
		if got != want {
			t.Errorf("got %v want %v", got, want)
		}
	})
}`,
	}, {
		name: "empty subtest closure proves nothing",
		want: false,
		src: `func TestX(t *testing.T) {
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			// nothing here
		})
	}
}`,
	}, {
		name: "same-package assertion helper counts",
		want: true,
		src: `func assertContains(t *testing.T, s, sub string) {
	if !strings.Contains(s, sub) {
		t.Errorf("missing %q", sub)
	}
}

func TestX(t *testing.T) {
	assertContains(t, run(t), "ok")
}`,
	}, {
		// This is the rule that keeps the gate from marking every test in a
		// tier as asserting: runHactl fails the test when the command exits
		// non-zero, but it does that for every caller, so it distinguishes
		// none of them.
		name: "value-returning invocation helper does not count",
		want: false,
		src: `func runHactl(t *testing.T, args ...string) string {
	out, err := exec(args)
	if err != nil {
		t.Fatalf("hactl %v: %v", args, err)
	}
	if strings.Contains(out, "UNPARSED") {
		t.Fatalf("degenerate output: %s", out)
	}
	return out
}

func TestX(t *testing.T) {
	out := runHactl(t, "ent", "ls")
	_ = out
}`,
	}, {
		name: "helper without a testing.T does not count",
		want: false,
		src: `func mustFail(msg string) {
	panic(msg)
}

func TestX(t *testing.T) {
	mustFail("nope")
}`,
	}, {
		name: "assertion helper reached through another assertion helper",
		want: true,
		src: `func assertContains(t *testing.T, s, sub string) {
	if !strings.Contains(s, sub) {
		t.Errorf("missing %q", sub)
	}
}

func assertLooksLikeTable(t *testing.T, s string) {
	assertContains(t, s, "entity_id")
}

func TestX(t *testing.T) {
	assertLooksLikeTable(t, run(t))
}`,
	}, {
		name: "mutually recursive helpers terminate and do not count",
		want: false,
		src: `func a(t *testing.T) { b(t) }
func b(t *testing.T) { a(t) }

func TestX(t *testing.T) {
	a(t)
}`,
	}, {
		name: "value check in the else branch of an error check",
		want: true,
		src: `func TestX(t *testing.T) {
	out, err := run()
	if err != nil {
		t.Fatalf("run: %v", err)
	} else if out != "ok" {
		t.Errorf("got %q", out)
	}
}`,
	}, {
		name: "tagless switch on error only",
		want: false,
		src: `func TestX(t *testing.T) {
	switch {
	case err != nil:
		t.Fatalf("run: %v", err)
	}
}`,
	}, {
		name: "tagless switch on a value",
		want: true,
		src: `func TestX(t *testing.T) {
	switch {
	case out == "":
		t.Fatal("empty answer")
	}
}`,
	}}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(t, tc.src).Asserts; got != tc.want {
				t.Errorf("Asserts = %v, want %v for:\n%s", got, tc.want, tc.src)
			}
		})
	}
}

// TestCorpusIsTestFunctionsOnly pins what the gate holds to the floor. A tier's
// harness (TestMain), its helpers and its benchmarks have no answer of their own
// to assert on; only the functions `go test` reports as tests do.
func TestCorpusIsTestFunctionsOnly(t *testing.T) {
	dir := t.TempDir()
	src := `package p

import (
	"os"
	"testing"
)

func TestMain(m *testing.M)      { os.Exit(m.Run()) }
func testHelper(t *testing.T)    { _ = t }
func TestNotATest()              {}
func TestWrongArg(s string)      { _ = s }
func BenchmarkThing(b *testing.B) { _ = b }

func TestReal(t *testing.T) {
	if got != want {
		t.Errorf("no")
	}
}
`
	if err := os.WriteFile(filepath.Join(dir, "x_test.go"), []byte(src), 0o600); err != nil {
		t.Fatalf("writing fixture: %v", err)
	}
	tests, err := testaudit.ScanRepo(dir)
	if err != nil {
		t.Fatalf("scanning fixture: %v", err)
	}
	if len(tests) != 1 || tests[0].Name != "TestReal" {
		t.Fatalf("want only TestReal in the corpus, got %v", names(tests))
	}
}

func names(tests []testaudit.Test) []string {
	out := make([]string, 0, len(tests))
	for _, tc := range tests {
		out = append(out, tc.Name)
	}
	return out
}

func TestExemptionRequiresAReason(t *testing.T) {
	bare := classify(t, `//test:no-assert
func TestX(t *testing.T) {
	_ = run(t)
}`)
	if bare.ExemptReason != "" {
		t.Errorf("a bare directive should carry no reason, got %q", bare.ExemptReason)
	}
	if !testaudit.ExemptReasonTooShort(bare.ExemptReason) {
		t.Error("an empty reason must be rejected as unreviewable")
	}

	token := classify(t, `//test:no-assert n/a
func TestX(t *testing.T) {
	_ = run(t)
}`)
	if !testaudit.ExemptReasonTooShort(token.ExemptReason) {
		t.Errorf("%q must be rejected: a token is not a decision anyone can review", token.ExemptReason)
	}

	written := classify(t, `//test:no-assert the fake supervisor answers nothing this test could read back
func TestX(t *testing.T) {
	_ = run(t)
}`)
	if testaudit.ExemptReasonTooShort(written.ExemptReason) {
		t.Errorf("a written reason must be accepted, got %q", written.ExemptReason)
	}
	if written.Asserts {
		t.Error("the exempt test must still be classified as non-asserting")
	}

	// A near-miss spelling must not be mistaken for the directive.
	near := classify(t, `//test:no-assertions-here because reasons and more reasons
func TestX(t *testing.T) {
	_ = run(t)
}`)
	if near.ExemptReason != "" {
		t.Errorf("%q is not the directive, got reason %q", "//test:no-assertions-here", near.ExemptReason)
	}
}

func TestTierIsDerivedFromBuildTags(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []struct{ name, tag string }{
		{"a_test.go", "//go:build integration\n"},
		{"b_test.go", "//go:build companion\n"},
		{"c_test.go", "//go:build companion_discovery\n"},
		{"d_test.go", ""},
	} {
		src := f.tag + "\npackage p\n\nimport \"testing\"\n\nfunc Test" + strings.ToUpper(f.name[:1]) + `(t *testing.T) {
	if got != want {
		t.Error("no")
	}
}
`
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(src), 0o600); err != nil {
			t.Fatalf("writing %s: %v", f.name, err)
		}
	}
	tests, err := testaudit.ScanRepo(dir)
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	got := map[string]string{}
	for _, tc := range tests {
		got[tc.Name] = tc.Tier()
	}
	want := map[string]string{"TestA": "integration", "TestB": "companion", "TestC": "discovery", "TestD": "unit"}
	for name, wantTier := range want {
		if got[name] != wantTier {
			t.Errorf("%s: tier = %q, want %q", name, got[name], wantTier)
		}
	}
}
