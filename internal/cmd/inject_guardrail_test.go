package cmd

import (
	"fmt"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/docs"
	"github.com/hemm-ems/hactl/internal/manual"
	"github.com/spf13/cobra"
)

// Every visible top-level command must be covered by the manual taxonomy —
// mapped as a family, aliased to one, or explicitly exempt — so a new command
// cannot ship without a progressive-manual decision. Fix a failure in
// internal/manual/families.go.
func TestTopLevelCommandsHaveManualCoverage(t *testing.T) {
	rootCmd.InitDefaultHelpCmd()
	rootCmd.InitDefaultCompletionCmd()
	for _, c := range rootCmd.Commands() {
		if c.Hidden {
			continue // hidden commands (cobra internals) are auto-exempt
		}
		name := c.Name()
		if _, ok := manual.FamilyFor(name); ok {
			continue
		}
		if manual.Exempt[name] {
			continue
		}
		t.Errorf("top-level command %q has no manual coverage: add it to FamilySections, Aliases, or Exempt in internal/manual/families.go", name)
	}
}

// Every runnable command must appear in the manual as a real `hactl <path>`
// usage line, not merely as a slot in the compressed "Full command set" list.
// The compressed list names a command; only prose tells a caller what it does,
// and the manual is the contract for LLM callers — a command listed but never
// explained reads as an invitation to guess. Fix a failure by writing prose in
// docs/manual.md, not by relaxing this test.
func TestEveryCommandHasManualProse(t *testing.T) {
	rootCmd.InitDefaultHelpCmd()
	rootCmd.InitDefaultCompletionCmd()

	// The compressed listing is a table of contents, not prose. Strip it so a
	// command cannot satisfy this gate by being mentioned there.
	body := docs.Manual
	if i := strings.Index(body, "Full command set"); i >= 0 {
		if j := strings.Index(body[i:], "No other commands exist"); j >= 0 {
			body = body[:i] + body[i+j:]
		}
	}

	for _, path := range runnableCommandPaths(rootCmd) {
		if manual.Exempt[strings.Fields(path)[0]] {
			continue
		}
		if !strings.Contains(body, "hactl "+path) {
			t.Errorf("command %q has no manual prose: add a `hactl %s` usage line to docs/manual.md", path, path)
		}
	}
}

// runnableCommandPaths returns the space-joined path of every visible command
// that actually runs (a pure grouping command such as `auto` has no RunE and is
// covered by its subcommands).
func runnableCommandPaths(root *cobra.Command) []string {
	var out []string
	var walk func(c *cobra.Command, prefix string)
	walk = func(c *cobra.Command, prefix string) {
		for _, sub := range c.Commands() {
			if sub.Hidden || sub.Name() == "help" || sub.Name() == "completion" {
				continue
			}
			// Deprecated aliases are the one kind of command the manual must
			// NOT teach: documenting one is an invitation to keep using it.
			// `hactl rollback` marks itself this way; cobra's own Deprecated
			// field counts too, for anything that adopts it later.
			if sub.Deprecated != "" || strings.HasPrefix(sub.Short, "Deprecated:") {
				continue
			}
			path := strings.TrimSpace(fmt.Sprintf("%s %s", prefix, sub.Name()))
			if sub.Runnable() {
				out = append(out, path)
			}
			walk(sub, path)
		}
	}
	walk(root, "")
	return out
}
