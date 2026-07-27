package surfaceaudit_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
	"github.com/hemm-ems/hactl/internal/testaudit"
)

// repoRoot walks up until it finds go.mod, so the gates run from any package.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// proofIndex reports whether a test function of a given name is defined
// anywhere in the repository, in any tier.
//
// It spans the build-tag-gated tiers deliberately. The proofs that matter most
// for these surfaces are exactly the ones that talk to a real Home Assistant,
// and a lookup that only saw the untagged build would call every `TestE2E…`
// citation a phantom.
func proofIndex(t *testing.T, root string) func(string) bool {
	t.Helper()
	tests, err := testaudit.ScanRepo(root)
	if err != nil {
		t.Fatalf("indexing the test corpus: %v", err)
	}
	byName := make(map[string]bool, len(tests))
	for _, tc := range tests {
		byName[tc.Name] = true
	}
	return func(name string) bool { return byName[name] }
}

// runGate is the body every surface gate shares.
func runGate(t *testing.T, s surfaceaudit.Surface, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("deriving the %s surface: %v", s.Name, err)
	}
	if len(s.Sites) == 0 && !s.AllowEmpty {
		// An extractor that finds nothing is the failure mode that makes a
		// closure gate worthless: it passes, forever, proving nothing. A census
		// surface is non-empty in any tree that still contains the product, so
		// an empty one means the extractor stopped matching. A violation
		// surface may legitimately be empty and is guarded by a fixture test
		// instead.
		t.Fatalf("the %s surface is empty — the extractor has stopped matching the code it audits", s.Name)
	}
	root := repoRoot(t)
	m, err := surfaceaudit.LoadManifest(root, s.Name)
	if err != nil {
		t.Fatalf("loading the %s manifest: %v", s.Name, err)
	}
	res := surfaceaudit.Check(s, m, proofIndex(t, root))
	if res.Failed() {
		t.Error(res.Report())
		return
	}
	t.Log(res.Report())
}

// TestClockSurfaceIsClosed — every site that renders a wall clock a human reads
// is dispositioned.
//
// The timezone fix of 2026-07-26 converted `formatShortTime` and shipped. Four
// other renderers produce an hour: two more in internal/analyze, two inline in
// internal/cmd. `trace show` was reported as still displaying UTC the same day.
func TestClockSurfaceIsClosed(t *testing.T) {
	s, err := surfaceaudit.ClockSurface(repoRoot(t))
	runGate(t, s, err)
}

// TestTargetSurfaceIsClosed — every command entrypoint that takes an identifier
// from the caller either resolves it or is dispositioned for not resolving it.
//
// H-17 says an identifier hactl prints is an identifier hactl accepts, and
// resolve_target.go says a command must not refuse an identifier its siblings
// display and accept. Neither statement was ever checked against the set of
// commands that take an identifier.
func TestTargetSurfaceIsClosed(t *testing.T) {
	s, err := surfaceaudit.TargetSurface(repoRoot(t))
	runGate(t, s, err)
}

// TestInvariantSurfaceIsClosed — every law in INVARIANTS.md declares whether a
// gate quantifies over its set or whether it is enforced by an enumeration.
func TestInvariantSurfaceIsClosed(t *testing.T) {
	s, err := surfaceaudit.InvariantSurface(repoRoot(t))
	runGate(t, s, err)
}

// TestInvariantCitationsResolve — every test named in an "Enforced by:" list
// exists.
//
// Unlike the surfaces above this is not ratcheted and has no manifest. A
// citation that does not resolve is not debt to be scheduled; it is a claim of
// proof that is false right now, and the document is the only place a reader
// goes to find out whether a rule is enforced.
func TestInvariantCitationsResolve(t *testing.T) {
	root := repoRoot(t)
	invs, err := surfaceaudit.ParseInvariants(root)
	if err != nil {
		t.Fatalf("parsing %s: %v", surfaceaudit.InvariantsFile, err)
	}
	if len(invs) == 0 {
		t.Fatalf("%s parsed to zero invariants — the heading format has changed", surfaceaudit.InvariantsFile)
	}
	var cited int
	for _, inv := range invs {
		cited += len(inv.Cites)
	}
	if cited == 0 {
		t.Fatalf("%s cites no tests at all — the citation format has changed", surfaceaudit.InvariantsFile)
	}
	phantoms := surfaceaudit.PhantomCitations(invs, proofIndex(t, root))
	for _, p := range phantoms {
		t.Errorf("%s", p)
	}
	if len(phantoms) == 0 {
		t.Logf("%d invariants, %d citations, all resolving", len(invs), cited)
	}
}

// TestRetrySurfaceIsClosed — every non-idempotent request site declares whether
// its retry policy can duplicate the request.
//
// H-1 states the rule as a universal and cites one test file, which exercises
// the companion client. The HA client is the other half and was never in the
// sentence.
func TestRetrySurfaceIsClosed(t *testing.T) {
	s, err := surfaceaudit.RetrySurface(repoRoot(t))
	runGate(t, s, err)
}

// TestPreviewSurfaceIsClosed — every --confirm-gated command declares whether
// its preview is machine-readable.
func TestPreviewSurfaceIsClosed(t *testing.T) {
	s, err := surfaceaudit.PreviewSurface(repoRoot(t))
	runGate(t, s, err)
}

// TestMapRangeSurfaceIsClosed — every range over a Go map in the module's
// non-test sources declares whether its iteration order can reach rendered
// output, and how that is prevented.
//
// H-16 states the rule as a universal and cited three commands. The other
// map walks were swept by hand once (2026-07-26), the sweep found the
// `companion wireguard status` defect, and nothing ever re-ran it — so the
// 29th map-range would have arrived carrying the same risk with no gate in
// its way. This is that re-run, standing.
func TestMapRangeSurfaceIsClosed(t *testing.T) {
	s, err := surfaceaudit.MapRangeSurface(repoRoot(t))
	runGate(t, s, err)
}
