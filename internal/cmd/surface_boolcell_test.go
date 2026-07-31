package cmd

import (
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
	"github.com/hemm-ems/hactl/internal/testaudit"
)

// TestBoolCellSurfaceIsClosed — every bool that becomes a table cell is proven
// to reach `--json` as a boolean, or knowingly exempt.
//
// `format.Table.SetMachine` existed, was documented on `yesNo` in the exact
// terms this rule needs ("a consumer writing `if row["fixable"]` reads every
// issue as fixable"), and two commands used it. Four others rendered a bool
// into a cell and stopped there, and nothing in the repository could name them:
// finding #59 reported one of the four, `dash ls`, because that is the command
// its author happened to run.
func TestBoolCellSurfaceIsClosed(t *testing.T) {
	root := surfaceRepoRoot(t)
	s, err := surfaceaudit.BoolCellSurface(root)
	if err != nil {
		t.Fatalf("deriving the boolcell surface: %v", err)
	}
	if len(s.Sites) == 0 {
		t.Fatal("no bool reaches a table cell anywhere — the extractor has stopped matching")
	}
	m, err := surfaceaudit.LoadManifest(root, s.Name)
	if err != nil {
		t.Fatalf("loading the boolcell manifest: %v", err)
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
