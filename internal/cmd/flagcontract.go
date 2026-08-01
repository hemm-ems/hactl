package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Flag contracts (INVARIANTS.md H-25).
//
// # The defect this exists for
//
// H-22 gave every command a positional contract. The flag half was never
// written, so cobra's defaults decided it, and a live-fire run against a real
// instance found nine symptoms of one rule that did not exist: a flag hactl
// offers is not a flag hactl honours.
//
//   - `--since` was a root persistent flag, declared on all 112 commands and
//     read by nine. `area ls --since garbage-value-xyz` exited 0 with output
//     byte-identical to `area ls`, and `area ls --help` advertised the flag
//     that had just done nothing (#54).
//   - `--top -1`, `--top 0`, `--tokensmax -5` and `--timeout 0s` were each
//     reinterpreted rather than refused. `--top 0` silently meant "no cap",
//     documented for `--tokensmax` and for nothing else (#47, #53); `--timeout
//     0s` disabled the very bound the flag exists to set, and `--timeout -1s`
//     reached net.Dialer as a deadline already in the past, so hactl answered
//     `dial tcp: lookup <host>: i/o timeout` — a network failure that never
//     happened, invented out of a flag value (#56).
//   - `rtfm --json` printed Markdown while the manual's own enumeration of the
//     commands that ignore `--json` did not name it — and did name `tpl eval`,
//     which honours it (#12).
//   - `hactl --version --json` printed the plain banner while `hactl version
//     --json` printed JSON: two spellings of one question, two answers (#13).
//   - `tpl eval "{{ 1+1 }}" -f file.jinja` evaluated the file and discarded the
//     argument, with nothing on either stream (#6).
//   - `ent ls --tpo 5` answered `unknown flag: --tpo` and stopped, while
//     `hactl ento ls` — the same fat-finger one token to the left — got cobra's
//     "Did you mean this?" block (#48).
//   - `--json` decided whether the progressive manual was delivered at all, and
//     decided it differently on the two halves of the product: a read command
//     wrote zero bytes to stderr, a `--confirm` write got the whole how-to from
//     confirmGuard (#50). See shouldInject in inject.go — the flag's reach is
//     stdout, and the manual has never been on stdout.
//
// # The contract
//
// A flag is declared on the commands that act on it, it accepts the values it
// says it accepts, and where two inputs name the same thing, passing both ends
// the command. There is no third state: a flag that is declared and ignored is
// the defect, and documenting the gap is not the fix.
//
// The set is closed by TestFlagContractSurfaceIsClosed, which walks the live
// cobra tree for every flag name more than one command OFFERS — the flags where
// "offered where it is not acted on" is possible at all — and requires each to
// be dispositioned in dev/surfaces/flagcontract.manifest.

// errFlagContract is what every refusal in this file reports as. It exists for
// the same reason errPositional does: a gate that quantifies over the whole
// tree has to be able to say the command refused *for the contract's reason*
// rather than for the next reason down.
var errFlagContract = errors.New("invalid flag")

// flagContractError carries the human-readable refusal while still answering
// errors.Is(err, errFlagContract).
type flagContractError struct{ msg string }

func (e *flagContractError) Error() string { return e.msg }

func (e *flagContractError) Is(target error) bool { return target == errFlagContract }

// globalFlagDomain is one flag's answer to "which values may I be given?".
//
// Only the value-taking global flags need a row: a bool's domain is the two
// values pflag parses, and a flag whose every parseable value is legal states
// that by having no row. TestEveryNumericGlobalFlagStatesItsDomain derives the
// set that DOES need one from the live flag set — every root persistent flag
// whose pflag type counts or measures something — so a new `--depth` is red
// until somebody says what it accepts.
type globalFlagDomain struct {
	Name  string
	Check func() error
}

// globalFlagDomains is the domain of every counting or measuring global flag.
//
// Zero means "no cap" for the two caps and is now documented on both, which is
// the half of #53 that was a documentation gap. It cannot mean the same for
// `--timeout`: H-23 says every connection hactl opens is bounded by the
// caller's `--timeout`, and a bound of zero bounds nothing, so a value that
// makes the law vacuous is refused rather than honoured. That asymmetry is the
// point — a cap and a bound are different promises, and the flag that promises
// a bound may not be talked out of it.
var globalFlagDomains = []globalFlagDomain{
	{Name: "top", Check: func() error {
		if flagTop < 0 {
			return &flagContractError{fmt.Sprintf(
				"invalid --top: %d (--top counts the rows to display, so it cannot be negative; --top 0 shows every row)", flagTop)}
		}
		return nil
	}},
	{Name: "tokensmax", Check: func() error {
		if flagTokensMax < 0 {
			return &flagContractError{fmt.Sprintf(
				"invalid --tokensmax: %d (--tokensmax counts the tokens to keep, so it cannot be negative; --tokensmax 0 removes the cap)", flagTokensMax)}
		}
		return nil
	}},
	{Name: "timeout", Check: func() error {
		if flagTimeout <= 0 {
			return &flagContractError{fmt.Sprintf(
				"invalid --timeout: %s (--timeout bounds every connection hactl opens, and a bound of zero or less bounds nothing; "+
					"pass a positive duration such as --timeout 5s, or leave it unset for the 30s default)", flagTimeout)}
		}
		return nil
	}},
}

// checkGlobalFlagDomains refuses a value no global flag can honour, before the
// command runs and before the value reaches anything that would turn it into a
// different answer.
func checkGlobalFlagDomains() error {
	for _, d := range globalFlagDomains {
		if err := d.Check(); err != nil {
			return err
		}
	}
	return nil
}

// flagErrorHelp is the root's FlagErrorFunc: cobra consults the nearest one up
// the parent chain, so installing it on the root reaches every command.
//
// pflag's answer to a flag it does not know is four words long. Cobra's answer
// to a COMMAND it does not know names the commands it does know — and a caller
// correcting itself needs the same signal for both halves of a command line.
// Two questions are worth asking about an unknown flag, in this order:
//
//  1. Does it exist elsewhere in this tree? Then the caller has the right flag
//     on the wrong command, and the answer is where it lives. This is the
//     branch `--since` takes now that it is declared on the nine commands that
//     read it rather than on all 112.
//  2. Is it a near miss for one this command does take? Then it is a typo, and
//     the answer is the flag itself.
//
// The original message stays as the first line either way: it is what a caller
// matching on hactl's output already sees, and the addition is an addition.
func flagErrorHelp(cmd *cobra.Command, err error) error {
	name, short := unknownFlagName(err)
	if name == "" && short == "" {
		return err
	}
	if where := commandsDeclaringFlag(cmd, name, short); len(where) > 0 {
		return &flagContractError{fmt.Sprintf("%s\n%s does not take %s; it is declared by: %s",
			err, cmd.CommandPath(), spellFlag(name, short), strings.Join(where, ", "))}
	}
	if near := nearestFlagNames(cmd, name); len(near) > 0 {
		if len(near) == 1 {
			return &flagContractError{fmt.Sprintf("%s\ndid you mean --%s?", err, near[0])}
		}
		return &flagContractError{fmt.Sprintf("%s\ndid you mean one of: --%s?", err, strings.Join(near, ", --"))}
	}
	return err
}

// spellFlag renders a flag the way the caller typed it.
func spellFlag(name, short string) string {
	if name != "" {
		return "--" + name
	}
	return "-" + short
}

// unknownFlagName reads the flag out of pflag's own error text.
//
// pflag returns a plain error rather than a typed one, so the message is the
// only carrier; TestUnknownFlagNameReadsPflagsOwnErrors builds both forms
// through pflag itself rather than from string literals, so a change to that
// wording fails the build instead of silently disabling the help.
func unknownFlagName(err error) (name, short string) {
	msg := err.Error()
	if v, ok := strings.CutPrefix(msg, "unknown flag: --"); ok {
		return v, ""
	}
	// "unknown shorthand flag: 'q' in -qxy" — the rune between the quotes.
	if rest, ok := strings.CutPrefix(msg, "unknown shorthand flag: '"); ok {
		if i := strings.Index(rest, "'"); i > 0 {
			return "", rest[:i]
		}
	}
	return "", ""
}

// commandsDeclaringFlag lists the commands that declare the flag `cmd` was
// handed, nearest first: when the caller's own family declares it, those are
// the answer and the rest of the tree is noise.
func commandsDeclaringFlag(cmd *cobra.Command, name, short string) []string {
	var family, all []string
	top := topCommandName(cmd)
	walkCommandTree(rootCmd, func(c *cobra.Command) {
		if c == cmd || !declaresFlag(c, name, short) {
			return
		}
		all = append(all, c.CommandPath())
		if top != "" && topCommandName(c) == top {
			family = append(family, c.CommandPath())
		}
	})
	if len(family) > 0 {
		all = family
	}
	sort.Strings(all)
	// A flag on twenty-five write commands is a fact about hactl, not an
	// answer: the list stops being readable long before it stops being true.
	const listed = 6
	if len(all) > listed {
		rest := len(all) - listed
		all = append(all[:listed:listed], fmt.Sprintf("and %d more", rest))
	}
	return all
}

// declaresFlag reports whether c declares the flag itself, as opposed to
// inheriting it from an ancestor.
//
// LocalFlags is cobra's own answer to that question — it excludes a parent's
// persistent flags while keeping one this command shadows — and it is the
// answer to use rather than `Flags() minus InheritedFlags()`, which looks
// equivalent and is not: cobra merges an ancestor's persistent flags into
// Flags() lazily, and a command's OWN persistent flags live in a third set that
// Flags() does not carry until then. Asking the naive way answered that `hactl
// companion wireguard` does not declare `--tunnel`, so the flag with a group's
// worth of subcommands behind it was the one whose address could not be found.
func declaresFlag(c *cobra.Command, name, short string) bool {
	local := c.LocalFlags()
	var f *pflag.Flag
	if name != "" {
		f = local.Lookup(name)
	} else {
		f = local.ShorthandLookup(short)
	}
	return f != nil && !f.Hidden
}

// nearestFlagNames returns the flags this command takes that are within a
// typo's reach of the one the caller typed.
//
// The distance is cobra's own default for command names (2), so the two halves
// of a command line answer a fat finger with the same tolerance. Containment is
// accepted at any length, which is what catches a truncation (`--jso`) rather
// than a substitution.
func nearestFlagNames(cmd *cobra.Command, typo string) []string {
	if typo == "" {
		return nil
	}
	type candidate struct {
		name string
		dist int
	}
	var found []candidate
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" {
			return
		}
		d := editDistance(typo, f.Name)
		if d <= 2 || strings.Contains(f.Name, typo) {
			found = append(found, candidate{f.Name, d})
		}
	})
	sort.Slice(found, func(i, j int) bool {
		if found[i].dist != found[j].dist {
			return found[i].dist < found[j].dist
		}
		return found[i].name < found[j].name
	})
	names := make([]string, 0, len(found))
	for _, c := range found {
		names = append(names, c.name)
	}
	const suggested = 3
	if len(names) > suggested {
		names = names[:suggested]
	}
	return names
}

// editDistance is Levenshtein distance, the measure cobra uses for command
// names. It is reimplemented here because cobra's is unexported and pflag has
// none — and the alternative, leaving flag typos without the help command
// typos get, is the asymmetry #48 reported.
func editDistance(a, b string) int {
	ar, br := []rune(a), []rune(b)
	prev := make([]int, len(br)+1)
	cur := make([]int, len(br)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ar); i++ {
		cur[0] = i
		for j := 1; j <= len(br); j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			cur[j] = min(min(cur[j-1]+1, prev[j]+1), prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(br)]
}

// walkCommandTree calls fn for every command hactl declares, skipping the two
// cobra generates: `help` and the `completion` subtree carry cobra's flags, not
// this package's, and cobra adds them lazily during ExecuteC — so a walk that
// included them would return a different set depending on what ran before.
func walkCommandTree(root *cobra.Command, fn func(*cobra.Command)) {
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		path := c.CommandPath()
		if path == "hactl help" || path == "hactl completion" || strings.HasPrefix(path, "hactl completion ") {
			return
		}
		fn(c)
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(root)
}
