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
// that actually runs. A pure grouping command such as `auto` is covered by its
// subcommands: it is runnable only because `family` gives it a RunE that prints
// its own help, which is what makes cobra validate its arguments at all (H-22,
// args.go) — the annotation, not Runnable(), is what says whether a command is
// one a caller invokes for an answer.
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
			if sub.Runnable() && !isFamilyGroup(sub) {
				out = append(out, path)
			}
			walk(sub, path)
		}
	}
	walk(root, "")
	return out
}

// The reverse of the two gates above: every command the manual NAMES exists.
// The core's "Full command set" tells every caller "No other commands exist —
// never invent one", and its quick-routing table says what to run; a
// manual-first contract commit put `policy show|check|lint` in both while the
// binary answered `unknown command "policy"` (exit 1 — which the routed
// command's own contract reads as "denied"). Both directions were needed: the
// tree→manual gates were green throughout. Every FamilySections key must also
// be a real top-level command, or the family's prose is delivered for nothing.
func TestEveryCommandTheManualNamesExists(t *testing.T) {
	core := docs.Manual
	start := strings.Index(core, "Full command set")
	end := strings.Index(core, "No other commands exist")
	qs := strings.Index(core, "## Quick routing")
	if start < 0 || end < start || qs < 0 || qs > start {
		t.Fatal("the manual core has no quick-routing table followed by a \"Full command set\" … \"No other commands exist\" block — this gate has nothing to check")
	}

	listed := 0
	for _, span := range backtickSpans(core[start:end]) {
		for _, path := range commandSetPaths(span) {
			assertManualPathExists(t, path, "Full command set")
			listed++
		}
	}
	if listed < 50 {
		t.Fatalf("parsed only %d command paths from the Full command set — the parser has stopped matching the manual", listed)
	}

	routed := 0
	for _, span := range quickRoutingSpans(core[qs:start]) {
		words := leadingCommandWords(span)
		if len(words) == 0 {
			continue
		}
		found, _, err := rootCmd.Find(words)
		if err != nil || found == rootCmd || isFamilyGroup(found) {
			t.Errorf("quick routing names `%s`, which does not resolve to a command (err=%v)", span, err)
		}
		routed++
	}
	if routed < 10 {
		t.Fatalf("parsed only %d routed commands from the quick-routing table — the parser has stopped matching the manual", routed)
	}

	for _, fam := range manual.Families() {
		assertManualPathExists(t, []string{fam}, "internal/manual FamilySections")
	}
}

// commandSetPaths expands one "Full command set" entry — `label|area|floor
// ls|create|delete`, `log [show <id>]`, `grep` — into command paths.
func commandSetPaths(span string) [][]string {
	words := strings.Fields(span)
	if len(words) == 0 {
		return nil
	}
	var subs []string
	for _, w := range words[1:] {
		w = strings.Trim(w, "[]")
		if w == "" || strings.HasPrefix(w, "<") || strings.HasSuffix(w, ">") {
			continue
		}
		subs = strings.Split(w, "|")
		break
	}
	var out [][]string
	for fam := range strings.SplitSeq(words[0], "|") {
		if len(subs) == 0 {
			out = append(out, []string{fam})
			continue
		}
		for _, sub := range subs {
			out = append(out, []string{fam, sub})
		}
	}
	return out
}

// quickRoutingSpans returns the backtick spans of the "Run, in order" column.
func quickRoutingSpans(table string) []string {
	var out []string
	for line := range strings.SplitSeq(table, "\n") {
		cells := strings.Split(line, "|")
		if len(cells) < 4 || strings.HasPrefix(strings.TrimSpace(cells[2]), "---") || strings.Contains(cells[2], "Run, in order") {
			continue
		}
		out = append(out, backtickSpans(cells[2])...)
	}
	return out
}

// leadingCommandWords is the command path a routed invocation starts with:
// the lowercase words before its first flag, placeholder or argument.
func leadingCommandWords(span string) []string {
	var words []string
	for w := range strings.FieldsSeq(span) {
		if !isCommandWord(w) {
			break
		}
		words = append(words, w)
	}
	return words
}

func assertManualPathExists(t *testing.T, path []string, where string) {
	t.Helper()
	found, rest, err := rootCmd.Find(path)
	if err != nil || len(rest) != 0 || found.CommandPath() != "hactl "+strings.Join(path, " ") {
		t.Errorf("%s names `hactl %s`, which is not a command (err=%v)", where, strings.Join(path, " "), err)
	}
}

func backtickSpans(s string) []string {
	parts := strings.Split(s, "`")
	var out []string
	for i := 1; i < len(parts); i += 2 {
		out = append(out, parts[i])
	}
	return out
}

func isCommandWord(w string) bool {
	if w == "" {
		return false
	}
	for _, r := range w {
		if (r < 'a' || r > 'z') && r != '-' {
			return false
		}
	}
	return !strings.HasPrefix(w, "-")
}
