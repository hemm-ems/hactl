package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
	"github.com/hemm-ems/hactl/internal/testaudit"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// outputFormatSurface is every command that declares a format flag of its own
// beside the global `--json`.
//
// `--json` alone is not a site: it is the flag every command has, and a command
// with only that one has nothing to conflict with. A command that adds `--raw`
// or `--yaml` has created a question — "which of the two do I honour?" — and
// `dash show` answered it by the order of its if-statements, silently, at exit
// 0 (finding #60).
//
// The vocabulary lives in outputformat.go, not here, so the extractor and the
// gate cannot drift: adding `--toml` to that list puts every command declaring
// one on this surface the same day.
func outputFormatSurface(t *testing.T) surfaceaudit.Surface {
	t.Helper()
	s := surfaceaudit.Surface{
		Name: "outputformat",
		Rule: "a command declaring an output-format flag beside --json refuses the combination rather than silently picking a winner",
	}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		var local []string
		c.LocalFlags().VisitAll(func(f *pflag.Flag) {
			for _, name := range outputFormatFlagNames {
				if f.Name == name && name != "json" {
					local = append(local, "--"+f.Name)
				}
			}
		})
		if len(local) > 0 {
			s.Sites = append(s.Sites, surfaceaudit.Site{
				Key:  c.CommandPath(),
				File: "cobra tree",
				Note: "declares " + strings.Join(local, " ") + " beside --json",
			})
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	return s
}

// TestOutputFormatSurfaceIsClosed — a command that grows a second way to spell
// its output format is a site somebody has to disposition, not a silent
// precedence rule.
func TestOutputFormatSurfaceIsClosed(t *testing.T) {
	root := surfaceRepoRoot(t)
	s := outputFormatSurface(t)
	if len(s.Sites) == 0 {
		t.Fatal("no command declares a format flag of its own — the walk has stopped matching")
	}
	m, err := surfaceaudit.LoadManifest(root, s.Name)
	if err != nil {
		t.Fatalf("loading the outputformat manifest: %v", err)
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

// TestDashShowRefusesConflictingOutputFormats runs the real command through
// cobra, because what the rule is about is which flags the CALLER passed and
// only cobra knows that. Reading the package's flag variables would pass
// against a `dash show --raw` whose --yaml was left true by an earlier test.
//
// The refusal happens before any instance is contacted, so no server is needed
// — and that is itself the assertion for the last case: a valid single-format
// invocation gets past the gate and fails on the connection instead.
func TestDashShowRefusesConflictingOutputFormats(t *testing.T) {
	for _, flags := range [][]string{
		{"--raw", "--yaml"},
		{"--raw", "--json"},
		{"--yaml", "--json"},
		{"--raw", "--yaml", "--json"},
	} {
		out, err := runDashShowArgs(t, flags...)
		if err == nil {
			t.Errorf("`dash show %v` was accepted; one format was silently discarded:\n%s", flags, out)
			continue
		}
		if !strings.Contains(err.Error(), "only one can be honoured") {
			t.Errorf("`dash show %v` failed for another reason: %v", flags, err)
		}
		if out != "" {
			t.Errorf("`dash show %v` wrote to stdout before refusing: %q", flags, out)
		}
	}
	// The control. Without it, "refuse two" is satisfied by refusing one.
	for _, flag := range []string{"--raw", "--yaml", "--json"} {
		_, err := runDashShowArgs(t, flag)
		if err != nil && strings.Contains(err.Error(), "only one can be honoured") {
			t.Errorf("`dash show %s` alone was refused as a conflict", flag)
		}
	}
}

// runDashShowArgs executes `dash show <flags>` through cobra with the flag
// state reset, and returns what reached stdout.
func runDashShowArgs(t *testing.T, flags ...string) (string, error) {
	t.Helper()
	setFlagForTest(t, &flagDashRaw, false)
	setFlagForTest(t, &flagDashYAML, false)
	setFlagForTest(t, &flagJSON, false)
	dashShowCmd.Flags().Visit(func(f *pflag.Flag) { f.Changed = false })
	rootCmd.PersistentFlags().Lookup("json").Changed = false

	var buf bytes.Buffer
	dashShowCmd.SetOut(&buf)
	dashShowCmd.SetErr(&buf)
	t.Cleanup(func() { dashShowCmd.SetOut(nil); dashShowCmd.SetErr(nil) })

	if err := dashShowCmd.ParseFlags(flags); err != nil {
		t.Fatalf("parsing %v: %v", flags, err)
	}
	err := dashShowCmd.RunE(dashShowCmd, nil)
	return buf.String(), err
}
