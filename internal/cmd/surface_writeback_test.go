package cmd

import (
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
	"github.com/hemm-ems/hactl/internal/testaudit"
)

// TestWriteBackSurfaceIsClosed — every command that mutates Home Assistant
// declares whether a test reads the write back from HA, or records that none
// does.
//
// H-12 says "a write is proven by reading it back from Home Assistant" and then
// enforces it with an "Enforced by:" list of the families somebody wrote a
// round-trip test for. The tree carries thirty-four write commands. Nothing
// connected the law's universal to that number, so a family with no round-trip
// test was indistinguishable from a family that did not exist — which is how
// `dash save` reached a release able to be replaced by a stub with no test
// failing, and how the six `area`/`floor`/`label` writes are still checked by
// reading them back through `hactl … ls`, the one thing the law says does not
// count.
//
// The set comes from the confirm census — one cobra walk, shared with
// TestConfirmSurfaceIsClosed, because H-2 makes `--confirm` the definition of a
// mutating command and a scope derived twice is a scope that can disagree with
// itself. See [surfaceaudit.WriteBackSurface] for the granularity argument and
// for the blind spot this does not close.
func TestWriteBackSurfaceIsClosed(t *testing.T) {
	root := surfaceRepoRoot(t)
	s, err := surfaceaudit.WriteBackSurface(confirmSurface(t).Sites)
	if err != nil {
		t.Fatalf("deriving the write-back surface: %v", err)
	}
	// Belt and braces with the same check inside the derivation: an extractor
	// that has stopped matching otherwise passes forever while proving nothing,
	// and this surface is a census — in any tree that still contains the
	// product it cannot legitimately be empty.
	if len(s.Sites) == 0 {
		t.Fatal("the writeback surface is empty — the --confirm walk has stopped matching the cobra tree")
	}
	m, err := surfaceaudit.LoadManifest(root, s.Name)
	if err != nil {
		t.Fatalf("loading the writeback manifest: %v", err)
	}
	// The proofs that matter here are the ones that talk to a real Home
	// Assistant, so the index spans every tier including the Docker-gated ones:
	// a lookup that only saw the untagged build would call every read-back
	// citation a phantom.
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
