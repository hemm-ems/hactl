package cmd

import (
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
	"github.com/hemm-ems/hactl/internal/testaudit"
	"github.com/spf13/cobra"
)

// setAssignmentSites is the extractor for H-27: every leaf command in the live
// cobra tree named `set-<something>` — an assignment by construction, since
// that is the one verb hactl uses for "make this entry's field equal this
// value" (`ent set-label`, `ent set-area`, `device set-label`,
// `device set-area`; `hactl setup` does not match, its last path segment is
// `setup` not `set-`, and it configures the local `.env` rather than a
// registry field).
//
// Finding #81: `ent set-label` could only ever grow an entity's label set, and
// `ent set-area` rejected `""`/`none` outright — so the class this command
// verb names had no way to undo what it made, short of `label delete`/`area
// delete`, which strip the value from every instance-wide holder rather than
// the one a caller named. The set is derived from the tree rather than typed
// out so a fifth `set-*` command inherits the question automatically instead
// of silently landing outside it, which is exactly how `device ls --pattern`
// fell out of step with its siblings (D-2).
func setAssignmentSites(root *cobra.Command) []surfaceaudit.Site {
	var sites []surfaceaudit.Site
	walkCommandTree(root, func(c *cobra.Command) {
		if c.HasSubCommands() {
			return
		}
		segments := strings.Fields(c.Use)
		if len(segments) == 0 || !strings.HasPrefix(segments[0], "set-") {
			return
		}
		sites = append(sites, surfaceaudit.Site{
			Key:  c.CommandPath(),
			File: "cobra tree",
			Note: "assigns a registry field; H-27 asks whether it can also unmake what it assigned",
		})
	})
	sort.Slice(sites, func(i, j int) bool { return sites[i].Key < sites[j].Key })
	return sites
}

func unmakeSurface(root *cobra.Command) surfaceaudit.Surface {
	return surfaceaudit.Surface{
		Name:  "unmake",
		Rule:  "every assignment a command can make, it can also unmake (INVARIANTS.md H-27)",
		Sites: setAssignmentSites(root),
	}
}

// TestUnmakeSurfaceIsClosed — a `set-*` command that only ever grows what it
// touches, with no flag or sibling command to shrink it again, is finding #81
// waiting to be re-reported against the next such command.
func TestUnmakeSurfaceIsClosed(t *testing.T) {
	root := surfaceRepoRoot(t)
	s := unmakeSurface(rootCmd)
	if len(s.Sites) == 0 {
		t.Fatal("no set-* command found in the live tree — the walk has stopped matching")
	}
	m, err := surfaceaudit.LoadManifest(root, s.Name)
	if err != nil {
		t.Fatalf("loading the unmake manifest: %v", err)
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

// TestSetAssignmentExtractorSeesAndIgnoresTheRightCommands guards the
// extractor itself: without a fixture, a refactor that made the prefix check
// always answer false would leave this surface green with nothing behind it,
// the same failure mode TestSharedFlagExtractorSeesASecondDeclaration exists
// to catch on flagcontract.
func TestSetAssignmentExtractorSeesAndIgnoresTheRightCommands(t *testing.T) {
	fake := &cobra.Command{Use: "hactl"}
	group := &cobra.Command{Use: "widget", Run: func(*cobra.Command, []string) {}}
	assign := &cobra.Command{Use: "set-color <widget> <color>", Run: func(*cobra.Command, []string) {}}
	setupLike := &cobra.Command{Use: "setup", Run: func(*cobra.Command, []string) {}}
	other := &cobra.Command{Use: "ls", Run: func(*cobra.Command, []string) {}}
	group.AddCommand(assign, other)
	fake.AddCommand(group, setupLike)

	got := map[string]bool{}
	for _, s := range setAssignmentSites(fake) {
		got[s.Key] = true
	}
	if !got["hactl widget set-color"] {
		t.Error("extractor missed a leaf command named set-<something>")
	}
	if got["hactl setup"] {
		t.Error("extractor flagged `setup`, which is not `set-<something>`")
	}
	if got["hactl widget ls"] {
		t.Error("extractor flagged a command whose name does not start with set-")
	}
	if got["hactl widget"] {
		t.Error("extractor flagged a group command (HasSubCommands), which is not an assignment leaf")
	}
}
