package cmd

import (
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
	"github.com/hemm-ems/hactl/internal/testaudit"
	"github.com/spf13/cobra"
)

// confirmSurface is every command that can write, derived from the live cobra
// tree rather than from a list.
//
// The tree is the only honest source. H-2 states "a preview fails exactly where
// the confirmed run would" as a universal, and enforces it by naming the
// thirteen commands that were fixed when it was written. The tree carries
// thirty-one. Nothing connected the two numbers, so eighteen commands were
// neither proven nor knowingly excluded — they were simply not in the sentence.
func confirmSurface(t *testing.T) surfaceaudit.Surface {
	t.Helper()
	s := surfaceaudit.Surface{
		Name: "confirm",
		Rule: "a preview fails exactly where --confirm would: the target resolves and the input parses before a plan is printed",
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Flags().Lookup("confirm") != nil {
			s.Sites = append(s.Sites, surfaceaudit.Site{
				Key:  c.CommandPath(),
				File: "cobra tree",
				Note: "writes; --confirm gated",
			})
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	return s
}

// TestConfirmSurfaceIsClosed — every write command declares how its preview is
// proven honest, or records that it is not.
//
// This gate exists because the fix that was supposed to establish the property
// scoped itself by grepping the symptom. The thirteen commands it reached were
// the ones printing `dry-run: would …`; `auto apply` prints `dry-run: no
// changes written to …` and was invisible to the search. The E2E table that
// would have caught it lists `script apply` and four deletes — five rows typed
// by hand, and the sixth row was the defect. A list cannot notice its own
// omissions; a tree walk can.
func TestConfirmSurfaceIsClosed(t *testing.T) {
	root := surfaceRepoRoot(t)
	s := confirmSurface(t)
	if len(s.Sites) == 0 {
		t.Fatal("no command in the tree carries --confirm — the walk has stopped matching")
	}
	m, err := surfaceaudit.LoadManifest(root, s.Name)
	if err != nil {
		t.Fatalf("loading the confirm manifest: %v", err)
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
