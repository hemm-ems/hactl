package cmd

import (
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
	"github.com/hemm-ems/hactl/internal/testaudit"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// confirmSurface is every command that can write, derived from the live cobra
// tree rather than from a list.
//
// The tree is the only honest source. H-2 states "a preview fails exactly where
// the confirmed run would" as a universal, and enforces it by naming the
// thirteen commands that were fixed when it was written. The tree carries
// thirty-one. Nothing connected the two numbers, so eighteen commands were
// neither proven nor knowingly excluded — they were simply not in the sentence.
// H-2 has TWO halves, and this surface used to carry one site per command —
// so a command could be `proven` for the half somebody happened to test.
//
// It shipped that way, twice, and an outsider found both. `svc call` was
// proven by TestSvcCallDryRunRefusesAServiceHADoesNotHave, which resolves the
// SERVICE; `--data '{"target":{…}}'` went to HA unexamined and answered 400.
// `ent rename` was proven by a case whose one malformed id was `nodomain`;
// five other shapes HA refuses previewed as "would rename … references: 2" at
// exit 0. Neither manifest row was false. Both were answers to one of the two
// questions, filed under a key that asked both at once.
//
// So the surface asks them separately. Every `--confirm` command yields two
// sites, and a proof of one is no longer a disposition of the other.
func confirmSurface(t *testing.T) surfaceaudit.Surface {
	t.Helper()
	s := surfaceaudit.Surface{
		Name: "confirm",
		Rule: "a preview fails exactly where --confirm would — asked twice: does the TARGET resolve, and is every caller-supplied VALUE judged, before a plan is printed",
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Flags().Lookup("confirm") != nil {
			s.Sites = append(s.Sites,
				surfaceaudit.Site{
					Key:  c.CommandPath() + " [target]",
					File: "cobra tree",
					Note: "resolves the identifier it writes to (" + positionalContract(c) + ")",
				},
				surfaceaudit.Site{
					Key:  c.CommandPath() + " [value]",
					File: "cobra tree",
					Note: "judges what the caller supplies: " + positionalContract(c) + ", flags " + callerValues(c),
				})
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	return s
}

// positionalContract is the command's own `Use` line minus the command name —
// what a caller types as the target.
func positionalContract(c *cobra.Command) string {
	_, args, _ := strings.Cut(c.Use, " ")
	if args == "" {
		return "none declared"
	}
	return args
}

// callerValues lists the flags that carry a value into the write. Booleans are
// left out: they select behaviour, they are not data the server judges. Which
// POSITIONAL is the target and which is data the target does not decide —
// `ent rename <old> <new>` writes to the first and is judged on the second —
// so both sites carry the whole Use line and the disposition says which half
// it is answering.
func callerValues(c *cobra.Command) string {
	var names []string
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Value.Type() == "bool" {
			return
		}
		names = append(names, "--"+f.Name)
	})
	sort.Strings(names)
	if len(names) == 0 {
		return "none beyond the positional"
	}
	return strings.Join(names, " ")
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
