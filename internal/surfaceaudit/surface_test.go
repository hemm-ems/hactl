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

// TestTransportSurfaceIsClosed — every place a connection's bounds are decided
// declares what decides them.
//
// The flag was documented and two of three transports honoured it. The third —
// the WebSocket, in a package neither of the others is in — was a 5s constant
// dial attempted twice behind a 10s constant handshake, so `companion status
// --timeout 1s` came back after 10.02s (#73). Nothing enumerated the set of
// transports, so a transport that ignored the flag looked exactly like one that
// did not exist.
func TestTransportSurfaceIsClosed(t *testing.T) {
	s, err := surfaceaudit.TransportSurface(repoRoot(t))
	runGate(t, s, err)
}

// TestPartialScopeSurfaceIsClosed — every command body that reads a source
// which can come back incomplete says what it does about a short read.
//
// D-7 is the law, and it has been written twice over a set that was prose in
// one command's doc comment. Both times the set was one source short: first the
// entity registry beneath the dashboards, then the whole config half of
// `ref scan`, which returned three of twenty-four references at exit 0 with the
// failure at slog.Warn (#34).
func TestPartialScopeSurfaceIsClosed(t *testing.T) {
	s, err := surfaceaudit.PartialScopeSurface(repoRoot(t))
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

// TestDecodeSurfaceIsClosed — every decode site the H-14 sweep cannot see is
// dispositioned.
//
// H-7 states the law as a universal — a decode that yields nothing never
// renders as success — and enforced it with three named tests on the trace
// renderer. TestSweep_EveryDecodeSiteIsChecked derives every json.Unmarshal in
// degeneracy.WirePackages; this gate derives everything that sweep cannot see
// (yaml, decoder constructions, websocket ReadJSON, json decodes outside the
// wire packages or in shapes the sweep cannot record). internal/writer sat in
// the gap: it decoded the live automation config from HA for years, and an
// empty decode rendered as a fictitious full-file diff and as a backup of
// nothing standing in for the user's only undo.
func TestDecodeSurfaceIsClosed(t *testing.T) {
	s, err := surfaceaudit.DecodeSurface(repoRoot(t))
	runGate(t, s, err)
}

// TestDomainDecodeSurfaceIsClosed — every place a domain-specific attribute
// schema can meet a Home Assistant states payload is dispositioned.
//
// H-21 states the law as a universal: the set of entities whose attributes a
// command decodes into a domain-specific schema is a subset of the set it
// renders. `auto ls` and `script ls` decoded all of /api/states into their own
// attribute struct and filtered afterwards, and both died on a live instance
// over an entity neither of them lists. Neither H-7 nor the H-14 sweep governs
// that: both are about decodes that silently yield nothing, and this one fails
// loudly on data it should never have read.
//
// The rule is a conjunction — a domain schema applied to an unfiltered payload
// — so the gate derives all three legs: the attribute schemas, the functions
// that read the whole document, and the functions where a schema meets a
// record. The spec's own census said "exactly two sites — not a guess, a
// derived count"; it was neither, which is what this derives instead.
func TestDomainDecodeSurfaceIsClosed(t *testing.T) {
	s, err := surfaceaudit.DomainDecodeSurface(repoRoot(t))
	runGate(t, s, err)
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

// TestAutomationRefSurfaceIsClosed — every command entrypoint that takes an
// automation reference hands it to the one shared resolver, or is
// dispositioned for not doing so.
//
// D-1 (docs/decisions.md): an automation is addressed by its config `id:`, its
// alias, or its entity_id, everywhere; the config id is the canonical printed
// form. The resolver that makes all forms equivalent is resolveAutomation, and
// a command that looks its target up some narrower way is the mechanism behind
// both prior half-fixes: `auto diff`/`auto apply` refusing the id `auto ls`
// prints (issue #94), and `auto rollback` matching the raw reference against
// backup filenames keyed by config id (R2's sibling, found by this gate —
// watched red on internal/cmd/rollback.go:runRollback before the fix).
func TestAutomationRefSurfaceIsClosed(t *testing.T) {
	s, err := surfaceaudit.AutomationRefSurface(repoRoot(t))
	runGate(t, s, err)
}

// TestResultSurfaceIsClosed — every --confirm-gated command declares whether
// its confirmed OUTCOME is machine-readable.
//
// TestPreviewSurfaceIsClosed above is the same law one branch over, and the two
// being separate tests is the point: the preview half was closed by a fix whose
// scope was the word "preview", and the identical omission on the confirmed
// path survived it in fourteen commands, including the flagship write. The
// dry-run/confirm pair is one surface with two branches; a gate on one of them
// is a gate on half a law.
func TestResultSurfaceIsClosed(t *testing.T) {
	s, err := surfaceaudit.ResultSurface(repoRoot(t))
	runGate(t, s, err)
}

// TestTruncationSurfaceIsClosed — every place that shortens a string for a
// reader is dispositioned.
//
// Finding #14 named one of six such sites. The other five did the same thing in
// the same shape, and two of them put the result straight into `--json`:
// `ent ls` reported `"state": "2026-07-31T03:13:..."` for 76 of the reference
// instance's 4486 entities, and `trace show`'s condensed step carried the last
// forty characters of a failure. Nothing could have said how many there were.
func TestTruncationSurfaceIsClosed(t *testing.T) {
	s, err := surfaceaudit.TruncationSurface(repoRoot(t))
	runGate(t, s, err)
}
