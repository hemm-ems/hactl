package cmd

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
	"github.com/hemm-ems/hactl/internal/testaudit"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// sharedFlagSites is the extractor: every flag name MORE THAN ONE command in
// the live tree offers — inherited from an ancestor, or declared again lower
// down, or both.
//
// The set is narrowed to shared names on purpose, and the narrowing is the
// design rather than a convenience. A flag one command declares and nothing
// else sees is on the command that reads it by construction: there is nowhere
// for it to be inert and nothing for it to disagree with. Every defect this
// surface exists for needed a second command:
//
//   - `--since` inherited by all 112 commands and read by nine, so a mistyped
//     window was an error on `log` and silence on `area ls` (#54).
//   - `--pattern` on five listings, four case-insensitive and one that stopped
//     being so in a *consistency* commit (D-2).
//   - `--raw` naming a FORMAT on `dash show` and CONTENT on `config file`, one
//     of which conflicts with `--json` and one of which composes with it (D-26).
//   - `--color` a documented global no-op that every command inherits, and a
//     real value-taking flag on `label create` that silently shadows it.
//
// So this is a census of hactl's shared flag vocabulary, and its question is
// "does this spelling mean one thing, and does every command that offers it act
// on it?" — which is H-25 in the only place it can be broken.
//
// Cobra merges an ancestor's persistent flags into a command's set lazily, so
// LocalFlags() is called first purely to force that merge: without it the walk
// answers that no command sees `--json`, which is the shape of extractor
// failure this surface is most exposed to (the sites it would then miss are
// exactly the global flags it exists for).
func sharedFlagSites(root *cobra.Command) []surfaceaudit.Site {
	type reach struct {
		sees     []string
		declares []string
	}
	byName := map[string]*reach{}
	walkCommandTree(root, func(c *cobra.Command) {
		_ = c.LocalFlags() // forces cobra's persistent-flag merge; see above
		c.Flags().VisitAll(func(f *pflag.Flag) {
			// `help` is cobra's, on every command, and hactl neither declares
			// nor honours it.
			if f.Hidden || f.Name == "help" {
				return
			}
			r := byName[f.Name]
			if r == nil {
				r = &reach{}
				byName[f.Name] = r
			}
			r.sees = append(r.sees, c.CommandPath())
			if declaresFlag(c, f.Name, "") {
				r.declares = append(r.declares, c.CommandPath())
			}
		})
	})

	var sites []surfaceaudit.Site
	for name, r := range byName {
		if len(r.sees) < 2 {
			continue
		}
		sort.Strings(r.declares)
		note := fmt.Sprintf("%d commands see it; declared by %s", len(r.sees), listOrCount(r.declares))
		sites = append(sites, surfaceaudit.Site{Key: "--" + name, File: "cobra tree", Note: note})
	}
	sort.Slice(sites, func(i, j int) bool { return sites[i].Key < sites[j].Key })
	return sites
}

// listOrCount names the declaring commands while the list is still something a
// reader takes in, and counts them once it is not.
func listOrCount(where []string) string {
	const listed = 5
	if len(where) <= listed {
		return strings.Join(where, ", ")
	}
	return fmt.Sprintf("%s, and %d more", strings.Join(where[:listed], ", "), len(where)-listed)
}

func flagContractSurface(root *cobra.Command) surfaceaudit.Surface {
	return surfaceaudit.Surface{
		Name:  "flagcontract",
		Rule:  "a flag more than one command offers means one thing in all of them, and every command that offers it acts on it",
		Sites: sharedFlagSites(root),
	}
}

// TestFlagContractSurfaceIsClosed — a flag that spreads to a second command is a
// question somebody has to answer, not a spelling that quietly acquires a second
// meaning or a third command it does nothing on.
func TestFlagContractSurfaceIsClosed(t *testing.T) {
	root := surfaceRepoRoot(t)
	s := flagContractSurface(rootCmd)
	if len(s.Sites) == 0 {
		t.Fatal("no flag is offered by more than one command — the walk has stopped matching")
	}
	m, err := surfaceaudit.LoadManifest(root, s.Name)
	if err != nil {
		t.Fatalf("loading the flagcontract manifest: %v", err)
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

// TestSharedFlagExtractorSeesASecondDeclaration feeds the extractor a tree in
// which one flag is declared twice and one is declared once.
//
// Without it, a refactor that made declaresFlag always answer false would leave
// the gate green forever with nothing behind it — and this surface's site count
// is small enough that the failure would look like an ordinary day.
func TestSharedFlagExtractorSeesASecondDeclaration(t *testing.T) {
	fake := &cobra.Command{Use: "hactl"}
	a := &cobra.Command{Use: "a", Run: func(*cobra.Command, []string) {}}
	b := &cobra.Command{Use: "b", Run: func(*cobra.Command, []string) {}}
	a.Flags().Bool("shared", false, "")
	b.Flags().Bool("shared", false, "")
	a.Flags().Bool("mine", false, "")
	fake.AddCommand(a, b)

	got := map[string]bool{}
	for _, s := range sharedFlagSites(fake) {
		got[s.Key] = true
	}
	if !got["--shared"] {
		t.Error("extractor missed a flag two commands declare")
	}
	if got["--mine"] {
		t.Error("extractor flagged a flag only one command declares")
	}

	// A flag the root declares persistently IS a site — that is the shape
	// `--since` had — and it is one site rather than one per command.
	fake.PersistentFlags().Bool("global", false, "")
	var globals int
	for _, s := range sharedFlagSites(fake) {
		if s.Key == "--global" {
			globals++
		}
	}
	if globals != 1 {
		t.Errorf("an inherited flag produced %d sites, want exactly 1", globals)
	}
}
