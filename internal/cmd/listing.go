package cmd

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// ---------------------------------------------------------------------------
// What a listing says when it has nothing to show.
//
// `helper ls --pattern zzz` printed "no helpers" on an instance holding 220 of
// them (live-fire #28), and `device ls --pattern zzz` said "no devices" on one
// holding 307. The message was a literal, chosen once per command, and it was
// the SAME literal for every reason a result can be empty: a pattern that
// matched nothing, a domain that does not exist, and an instance that genuinely
// holds none. Under the manual's stop-at-the-first-miss rule that is not a
// missing answer, it is a wrong one — a caller (or an agent) reads "no helpers"
// as an inventory fact and stops.
//
// The other four listings were mute rather than wrong: `ent ls --pattern zzz`
// printed a bare table header. Honest, and it still leaves the caller to guess
// whether the filter or the instance is empty.
//
// One rule for both: an empty listing states what narrowed it. The narrowings
// are read from the command's own flag set rather than passed in, so a listing
// that grows a sixth filter flag cannot forget to mention it — an enumeration
// per call site is how the four filters inside filterDevices came to disagree
// in the first place (dev/surfaces/README.md).
// ---------------------------------------------------------------------------

// narrowingPrefixes are the two ways a flag declares that it removes rows from
// a listing: `--pattern` "filter by helper id", `--failing` "show only
// automations with recent errors". Every narrowing flag in the tree opens its
// help with one of them, and no other flag does.
//
// Widening and reshaping flags are outside both: `--all` includes MORE (issues
// ls), `--unique` deduplicates what is already there, `--top`/`--full`/`--json`
// decide how the answer renders rather than what it contains.
var narrowingPrefixes = []string{"filter", "show only"}

// narrowsListing reports whether a flag narrows what a listing answers with,
// as opposed to shaping how the answer is rendered (--top, --json, --full),
// widening it, or naming a target.
//
// The signal is the flag's own declared purpose rather than its name. That is
// a convention, so TestNarrowingFlagsDeclareThemselves holds the tree to it: a
// flag whose help says it filters somewhere other than at the front fails the
// build rather than silently leaving the surface.
//
// Deriving it beats naming it. The case pole's set used to be four names typed
// into a test — pattern, name, area, label — and the three `--domain` filters
// added later were case-sensitive against a decided case-insensitive pole (D-2)
// for as long as that list existed, because a flag nobody had listed was
// indistinguishable from a flag nobody needed to list.
func narrowsListing(f *pflag.Flag) bool {
	usage := strings.ToLower(f.Usage)
	for _, p := range narrowingPrefixes {
		if strings.HasPrefix(usage, p) {
			return true
		}
	}
	return false
}

// narrowingFlags returns the narrowing flags declared on c, in the order pflag
// sorts them, whether or not the caller supplied a value.
func narrowingFlags(c *cobra.Command) []*pflag.Flag {
	var out []*pflag.Flag
	c.Flags().VisitAll(func(f *pflag.Flag) {
		if narrowsListing(f) {
			out = append(out, f)
		}
	})
	return out
}

// activeNarrowings renders the narrowings the caller actually applied, as the
// `--flag "value"` pairs they typed.
//
// The values are read back off the flag objects rather than off the package
// variables the commands bind: the variables are what the filter functions use,
// and reading them here would mean this message and those functions could
// disagree about which flag carried which value. pflag holds one value per
// flag, so there is nothing to keep in sync.
//
// A flag holding its default is not a narrowing — pflag's Changed cannot be
// used for that, because it survives an in-process invocation (see
// RunWithOutputContext, which resets the persistent flags for exactly this
// reason) and one `--pattern x` would make every later run in the same process
// claim a filter the caller never typed.
func activeNarrowings(c *cobra.Command) []string {
	var out []string
	for _, f := range narrowingFlags(c) {
		v := f.Value.String()
		switch {
		case v == "" || v == f.DefValue:
			// Holding its default: the caller did not narrow by this.
		case f.Value.Type() == "bool":
			// A switch has no value worth echoing — `--failing "true"` reads
			// like a value the caller typed and never is one.
			out = append(out, "--"+f.Name)
		default:
			out = append(out, fmt.Sprintf("--%s %q", f.Name, v))
		}
	}
	return out
}

// emptyListing prints why a listing has no rows: the narrowings that emptied
// it and how much they were applied to, or the plain noun when nothing was
// narrowed and the instance really does hold none.
//
// `total` is how many records existed before the narrowing ran. It is the
// number that turns "no helpers" from a claim into a measurement, and it is the
// one thing the caller cannot get from the message otherwise — the natural next
// command, the same listing unfiltered, is exactly what they would have to run
// to learn it.
//
// Under --json this is still the empty array format.Table renders for zero
// rows: the reason a listing is empty is prose for a reader, and H-10's machine
// contract is that the document parses as the same shape whatever the answer.
// `hints` are extra lines a particular listing can offer about where the answer
// might be instead — `auto ls --failing`'s pointer at the error log. They are
// additions to the sentence above, never replacements for it: the hint used to
// BE the whole message there, so `auto ls --pattern zzz --failing` reported on
// one of the two narrowings the caller had typed.
func emptyListing(c *cobra.Command, w io.Writer, subject string, total int, hints ...string) error {
	lines := make([]string, 0, 1+len(hints))
	lines = append(lines, emptyListingReason(c, subject, total))
	lines = append(lines, hints...)
	return emitEmptyList(w, strings.Join(lines, "\n"))
}

// unknownTotal is `total` for a listing narrowed somewhere hactl cannot count
// — `companion logs`, whose --component and --level are applied by the add-on
// before the records cross the wire. The narrowings are still named; only the
// measurement is missing, and inventing one would be worse than omitting it.
const unknownTotal = -1

func emptyListingReason(c *cobra.Command, subject string, total int) string {
	narrowings := activeNarrowings(c)
	if len(narrowings) == 0 || total == 0 {
		return "no " + subject
	}
	if total == unknownTotal {
		return fmt.Sprintf("no %s match %s", subject, strings.Join(narrowings, " "))
	}
	return fmt.Sprintf("no %s match %s (%d %s on this instance, none matching)",
		subject, strings.Join(narrowings, " "), total, subject)
}
