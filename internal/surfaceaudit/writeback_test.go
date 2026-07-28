package surfaceaudit_test

import (
	"errors"
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
)

// TestWriteBackSurfaceRefusesAnEmptyCensus is the guard on the one failure a
// closure gate cannot survive.
//
// The write-back census is handed in by the cobra walk in internal/cmd, so this
// package cannot notice on its own that the walk stopped matching. If an empty
// census produced an empty surface, the gate would find nothing to disposition,
// the manifest would be entirely stale — and staleness is a hard error, so the
// gate would go red. But the derivation must not depend on the ledger being
// non-empty to stay honest, so emptiness is refused at the source.
func TestWriteBackSurfaceRefusesAnEmptyCensus(t *testing.T) {
	if _, err := surfaceaudit.WriteBackSurface(nil); !errors.Is(err, surfaceaudit.ErrNoWriteCommands) {
		t.Errorf("an empty --confirm census must not derive a surface, got err=%v", err)
	}
}

// TestWriteBackSurfaceKeepsTheCommandPaths pins the two things the manifest
// depends on: the keys are the command paths the cobra tree yields, so a site
// reads as prose and the same key names the same command in every ledger, and
// the order is stable so a failure report and a `git diff` do not reshuffle.
func TestWriteBackSurfaceKeepsTheCommandPaths(t *testing.T) {
	in := []surfaceaudit.Site{
		{Key: "hactl tpl create", File: "cobra tree", Note: "writes; --confirm gated"},
		{Key: "hactl auto delete", File: "cobra tree", Note: "writes; --confirm gated"},
	}
	s, err := surfaceaudit.WriteBackSurface(in)
	if err != nil {
		t.Fatalf("deriving the write-back surface: %v", err)
	}
	if s.Name != "writeback" {
		t.Errorf("surface name is %q, want writeback — it is the manifest's basename", s.Name)
	}
	want := []string{"hactl auto delete", "hactl tpl create"}
	if len(s.Sites) != len(want) {
		t.Fatalf("derived %d sites from %d commands: %+v", len(s.Sites), len(in), s.Sites)
	}
	for i, key := range want {
		if s.Sites[i].Key != key {
			t.Errorf("site %d is %q, want %q", i, s.Sites[i].Key, key)
		}
		if s.Sites[i].Note == "" {
			t.Errorf("site %q carries no note; the report explains itself with it", key)
		}
	}
}
