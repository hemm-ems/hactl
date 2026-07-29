package cmd

import (
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
	"github.com/hemm-ems/hactl/internal/testaudit"
	"github.com/spf13/cobra"
)

// lsFilterSurface is every listing command in the live cobra tree — every
// leaf named "ls" — regardless of whether it carries a filter flag.
//
// The existing filter gates close over flags that EXIST:
// TestFilterSurfaceIsClosed demands a probe per present filter flag, and
// TestFilterFlagsAgreeOnCase holds each probe to the D-2 pole. A listing with
// NO filter never enters either walk, so a missing --pattern is structurally
// invisible — which is how `helper ls` shipped without one while all four
// siblings had it (#108), and how nobody had decided whether `area ls` or
// `dash ls` lacking filters is fine. This surface makes the absence a
// disposition someone signed instead of a silence (#109).
func lsFilterSurface(t *testing.T) surfaceaudit.Surface {
	t.Helper()
	s := surfaceaudit.Surface{
		Name: "lsfilter",
		Rule: "a listing narrows by an identifier filter (--pattern, D-1), or its row states why there is nothing to narrow",
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Name() == "ls" && !c.HasSubCommands() {
			note := "has --pattern"
			if c.Flags().Lookup("pattern") == nil {
				note = "no --pattern"
			}
			s.Sites = append(s.Sites, surfaceaudit.Site{
				Key:  c.CommandPath(),
				File: "cobra tree",
				Note: note,
			})
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	return s
}

// TestLsFilterSurfaceIsClosed — every listing is proven filterable or
// knowingly exempt; a new `ls` command fails the build until somebody says
// which.
func TestLsFilterSurfaceIsClosed(t *testing.T) {
	root := surfaceRepoRoot(t)
	s := lsFilterSurface(t)
	if len(s.Sites) == 0 {
		t.Fatal("no command named ls in the tree — the walk has stopped matching")
	}
	m, err := surfaceaudit.LoadManifest(root, s.Name)
	if err != nil {
		t.Fatalf("loading the lsfilter manifest: %v", err)
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

// TestLsFilterProvenRowsCarryPatternFlags — a `proven` row in the lsfilter
// manifest claims the flag exists; this pins the claim to the tree, so a
// removed flag turns the row stale loudly rather than staying "proven".
func TestLsFilterProvenRowsCarryPatternFlags(t *testing.T) {
	for _, site := range lsFilterSurface(t).Sites {
		if site.Note == "no --pattern" {
			continue
		}
		key := site.Key + "/pattern"
		if probes[key] == nil {
			t.Errorf("%s has --pattern but no probe under %q — TestFilterSurfaceIsClosed should have caught this first", site.Key, key)
		}
	}
}
