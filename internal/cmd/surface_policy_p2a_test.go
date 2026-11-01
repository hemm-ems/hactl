// Stage-2a: the H-29 closure gate over dev/surfaces/policy.manifest, and the
// derived write-command census it is fed (D-80, R-17), written from the
// contract before either exists.
//
// WIRING (integrated 2026-11-01). Untagged now: surfaceaudit.PolicySurface
// and the derived policyCommandCensus (surface_policy_test.go) are real.
// History: built only with `-tags p2a_contract`. It needs two things the
// integrator builds: surfaceaudit.PolicySurface (stubbed in
// internal/surfaceaudit/policy_stub_p2a.go) and policyCommandCensus (stubbed in
// surface_policy_stub_p2a_test.go). Until then:
//
//	go test -tags 'p2a_contract p2a_stub' ./internal/cmd/ -run 'PolicySurface|PolicyCommandCensus'
//
// compiles and fails. The integrator deletes both stubs and this build line;
// policyCommandCensus must then be DERIVED — every --confirm command of the
// live tree plus every entrypoint that reaches a mapped write request or a
// system:cache write without one, plus mcp and setup — never a list (D-80).
package cmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
	"github.com/hemm-ems/hactl/internal/testaudit"
)

// TestPolicySurfaceIsClosed — every request hactl sends and every write
// command carries a policy disposition (INVARIANTS.md H-29).
//
// The request set is derived from the source and the command set from the live
// cobra tree, so a websocket type, a REST path, a companion operation or a
// write command added tomorrow without a row turns this red as unclassified; a
// row whose site is gone turns it red as stale. Watched to fail by
// internal/surfaceaudit's TestPolicyGateFailsOnANewSite, which injects each
// kind of site into a fixture tree.
func TestPolicySurfaceIsClosed(t *testing.T) {
	root := surfaceRepoRoot(t)
	s, err := surfaceaudit.PolicySurface(root, policyCommandCensus(t))
	if err != nil {
		t.Fatalf("deriving the policy surface: %v", err)
	}
	// A census surface: in any tree that still holds the product it cannot be
	// empty, so emptiness means the extractor stopped matching (manifest
	// header: "the gate, once built, must fail when the extractor returns
	// nothing").
	if len(s.Sites) == 0 {
		t.Fatal("the policy surface is empty — the extractor has stopped matching the code it audits")
	}
	m, err := surfaceaudit.LoadManifest(root, s.Name)
	if err != nil {
		t.Fatalf("loading the policy manifest: %v", err)
	}
	tests, err := testaudit.ScanRepo(root)
	if err != nil {
		t.Fatalf("indexing the test corpus: %v", err)
	}
	byName := make(map[string]bool, len(tests))
	for _, tc := range tests {
		byName[tc.Name] = true
	}
	res := surfaceaudit.Check(s, m, func(name string) bool { return byName[name] })
	if res.Failed() {
		t.Error(res.Report())
		return
	}
	t.Log(res.Report())
}

// TestPolicyCommandCensusFindsEveryWriter — the command half of the surface is
// derived, and the derivation reaches what a --confirm walk alone cannot.
//
// Every --confirm command is in it; so are the writers without --confirm the
// manifest names (config show's probe, config flow-inspect, cache clear and
// refresh — D-79, D-80) and the two that dispatch or bootstrap (mcp, setup).
// The read commands whose requests are POSTs that read — tpl eval's
// /api/template, health's check-config — are not: a census that took every
// POST for a write would put them here, and a census that took only --confirm
// would miss the four above.
func TestPolicyCommandCensusFindsEveryWriter(t *testing.T) {
	census := policyCommandCensus(t)
	keys := make([]string, 0, len(census))
	for _, s := range census {
		keys = append(keys, s.Key)
	}
	if len(keys) == 0 {
		t.Fatal("the policy command census is empty")
	}
	for _, site := range confirmSurface(t).Sites {
		k := strings.TrimSuffix(strings.TrimSuffix(site.Key, " [target]"), " [value]")
		if !slices.Contains(keys, k) {
			t.Errorf("--confirm command %q is missing from the policy command census", k)
		}
	}
	for _, k := range []string{
		"hactl config show", "hactl config flow-inspect", "hactl cache clear", "hactl cache refresh",
		"hactl mcp", "hactl setup",
	} {
		if !slices.Contains(keys, k) {
			t.Errorf("%q writes (or dispatches/bootstraps) without --confirm and is missing from the census", k)
		}
	}
	for _, k := range []string{
		"hactl tpl eval", "hactl health", "hactl ent ls", "hactl policy show", "hactl policy check", "hactl auto ls",
	} {
		if slices.Contains(keys, k) {
			t.Errorf("%q sends no write request and is no write site", k)
		}
	}
}
