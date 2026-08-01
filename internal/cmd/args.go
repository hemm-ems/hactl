package cmd

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// Positional-argument contracts (INVARIANTS.md H-22).
//
// # The defect this exists for
//
// A live-fire run against a real instance found three symptoms that are one
// defect: hactl's positional contract was implicit, so cobra's defaults decided
// it.
//
//   - `hactl auto show ''` and `hactl auto delete ''` resolved to a real,
//     unrelated restored automation. `resolveAutomation` compares
//     `a.Attributes.ID == ref` (and three sibling equality checks), a ghost's
//     own config id is `""`, and `"" == ""` matches. The dry run of
//     `auto delete ''` printed a plan to delete that automation; one `--confirm`
//     away from deleting an object nobody named. `device show ''` returned an
//     arbitrary device by the same mechanism, and `area create ''` previewed
//     creating an area with an empty name.
//   - `hactl ent ls sensor` printed the same unfiltered listing as `ent ls`.
//     A leaf command with `Args == nil` gets cobra's ArbitraryArgs, which
//     accepts every positional and hands it to a Run that never reads `args`.
//   - `hactl helper set`, `hactl dash frobnicate` and ~12 more exited 0 with the
//     family's help on stdout. Cobra's `legacyArgs` refuses an unknown command
//     only for the root; for any other group it returns nil, and a group with no
//     Run is then help-only — `execute` returns `flag.ErrHelp` *before*
//     `ValidateArgs` ever runs, so an `Args` validator on a help-only group is
//     dead code.
//
// # The contract
//
// Every command in the tree declares its arity here, and every declaration
// refuses the same three things before the command body runs: a blank
// identifier, a positional the command does not take, and — on a command that
// groups subcommands — an unknown subcommand. `family` is the group half: it
// makes the group runnable (printing its own help, which is what a bare
// `hactl auto` always did) purely so that cobra reaches ValidateArgs at all.
//
// The set is closed by TestPositionalSurfaceIsClosed, which walks the live
// cobra tree and flags any command whose `Args` is not one of the five
// constructors below — so a new command is red until its contract is written,
// rather than silently inheriting ArbitraryArgs.

// errPositional is what every refusal below reports as. It exists so the
// closure gates can assert that a command refused *for the contract's reason*
// rather than for the next reason down (an unconfigured instance, a 404), which
// is the only way a universal over the whole tree can be more than "it errored".
var errPositional = errors.New("invalid positional argument")

// positionalError carries the human-readable refusal while still answering
// errors.Is(err, errPositional). The message is not prefixed with the sentinel:
// a caller reads "argument 1 (<id>) is blank", not a wrapped chain.
type positionalError struct{ msg string }

func (e *positionalError) Error() string { return e.msg }

func (e *positionalError) Is(target error) bool { return target == errPositional }

// takesNone is the contract for a command with no positional arguments: every
// listing, every command whose input arrives through flags, and every family
// group. On a command that has subcommands, an argument is a subcommand the
// caller got wrong, and the refusal says so.
func takesNone() cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error { return checkPositionals(cmd, args, 0, 0) }
}

// takes is the contract for a command that requires exactly n identifiers.
func takes(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error { return checkPositionals(cmd, args, n, n) }
}

// takesAtLeast is the contract for a command that requires n identifiers and
// accepts more (`ent set-label <entity_id> <label>...`).
func takesAtLeast(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error { return checkPositionals(cmd, args, n, -1) }
}

// takesAtMost is the contract for a command whose identifier is optional
// (`dash show [url_path]`, `auto rollback [automation-id]`). An omitted
// argument keeps its documented default; an argument that is present but blank
// is still refused, because a caller who passed something meant something.
//
// today; the arity stays a parameter because these five constructors are the
// whole vocabulary a command's contract is written in, and one of them silently
// meaning "at most 1" is how the next two-optional-argument command gets the
// wrong contract.
//
//nolint:unparam // every command with an optional identifier takes exactly one
func takesAtMost(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error { return checkPositionals(cmd, args, 0, n) }
}

// takesBetween is the contract for a command with a required pair and an
// optional third (`dash replace <old> <new> [url_path]`).
func takesBetween(lo, hi int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error { return checkPositionals(cmd, args, lo, hi) }
}

// checkPositionals is the one body all five contracts share. max < 0 means
// unbounded.
//
// The arity messages are cobra's own wording, verbatim, because the arity half
// was never the defect and a caller who has seen `accepts 1 arg(s), received 2`
// from hactl before should keep seeing it.
func checkPositionals(cmd *cobra.Command, args []string, minN, maxN int) error {
	switch {
	case maxN == 0 && len(args) > 0:
		return unexpectedPositional(cmd, args[0])
	case len(args) < minN && minN == maxN:
		return &positionalError{fmt.Sprintf("accepts %d arg(s), received %d", minN, len(args))}
	case len(args) < minN:
		return &positionalError{fmt.Sprintf("requires at least %d arg(s), only received %d", minN, len(args))}
	case maxN >= 0 && len(args) > maxN && minN == maxN:
		return &positionalError{fmt.Sprintf("accepts %d arg(s), received %d", maxN, len(args))}
	case maxN >= 0 && len(args) > maxN && minN == 0:
		return &positionalError{fmt.Sprintf("accepts at most %d arg(s), received %d", maxN, len(args))}
	case maxN >= 0 && len(args) > maxN:
		return &positionalError{fmt.Sprintf("accepts between %d and %d arg(s), received %d", minN, maxN, len(args))}
	}
	for i, a := range args {
		if strings.TrimSpace(a) == "" {
			return blankPositional(cmd, i)
		}
	}
	return nil
}

// blankPositional refuses an empty or whitespace-only identifier.
//
// It names the placeholder from the command's own Use line, so the message
// points at the argument the caller has to fix rather than at a position.
func blankPositional(cmd *cobra.Command, i int) error {
	where := fmt.Sprintf("argument %d", i+1)
	fix := ""
	if p := positionalPlaceholder(cmd, i); p != "" {
		where += " (" + p + ")"
		fix = "; pass a real " + p
	}
	return &positionalError{fmt.Sprintf(
		"%s: %s is blank — an empty string names nothing and is not a wildcard%s",
		cmd.CommandPath(), where, fix)}
}

// unexpectedPositional refuses an argument the command has no place for.
//
// On a group it is a mistyped subcommand. On a leaf it is nearly always a
// filter the caller expected to be positional — `ent ls sensor` for
// `ent ls --domain sensor` — so the refusal offers the command's own
// value-taking flags with the argument already spliced in.
func unexpectedPositional(cmd *cobra.Command, arg string) error {
	if cmd.HasSubCommands() {
		return unknownSubcommand(cmd, arg)
	}
	msg := fmt.Sprintf("%s: unexpected argument %q — this command takes no positional arguments",
		cmd.CommandPath(), arg)
	switch hints := valueFlagSuggestions(cmd, arg); len(hints) {
	case 0:
	case 1:
		msg += fmt.Sprintf("\ndid you mean %s?", hints[0])
	default:
		msg += fmt.Sprintf("\ndid you mean one of: %s?", strings.Join(hints, ", "))
	}
	return &positionalError{msg}
}

// unknownSubcommand mirrors what the root command has always done for a
// mistyped top-level command — the behaviour every family had lost — including
// cobra's own suggestion list.
func unknownSubcommand(cmd *cobra.Command, name string) error {
	msg := fmt.Sprintf("unknown command %q for %q", name, cmd.CommandPath())
	// cobra's own findSuggestions defaults the distance before asking, and
	// SuggestionsFor with the zero value answers nothing — which is how the
	// root's did-you-mean works and a hand-rolled call silently does not.
	if !cmd.DisableSuggestions {
		if cmd.SuggestionsMinimumDistance <= 0 {
			cmd.SuggestionsMinimumDistance = 2
		}
		if s := cmd.SuggestionsFor(name); len(s) > 0 {
			msg += "\n\nDid you mean this?\n\t" + strings.Join(s, "\n\t")
		}
	}
	msg += fmt.Sprintf("\n\nRun '%s --help' for the subcommands.", cmd.CommandPath())
	return &positionalError{msg}
}

// valueFlagSuggestions renders the command's own value-taking flags with arg
// spliced in. Boolean flags are excluded (they take no value), as are the
// global flags every command inherits — suggesting `--json sensor` would be
// noise, and the flag the caller wanted is always one this command declared.
func valueFlagSuggestions(cmd *cobra.Command, arg string) []string {
	var names []string
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Hidden || f.Name == "help" || f.Value.Type() == "bool" {
			return
		}
		if cmd.InheritedFlags().Lookup(f.Name) != nil {
			return
		}
		names = append(names, f.Name)
	})
	sort.Strings(names)
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "--"+n+" "+arg)
	}
	return out
}

// positionalPlaceholder reads the i-th placeholder out of a command's Use line
// ("set-area <device> <area>" -> "<area>" for i=1), so the blank-argument
// message can name what was blank. It is derived rather than declared for the
// same reason the surfaces are: a second list of argument names would drift
// from the first one the day somebody renames a placeholder.
func positionalPlaceholder(cmd *cobra.Command, i int) string {
	fields := strings.Fields(cmd.Use)
	var places []string
	for _, f := range fields[min(1, len(fields)):] {
		if strings.HasPrefix(f, "<") || strings.HasPrefix(f, "[") {
			places = append(places, f)
		}
	}
	if i < len(places) {
		return places[i]
	}
	return ""
}

// familyAnnotation marks a command that exists only to group subcommands.
//
// It is an annotation on the command rather than a list, because three walkers
// in this package ask "is this a real command?" via Runnable() — the MCP gate's
// exhaustiveness check, the --json contract sweep and the manual-prose
// guardrail — and `family` makes a group runnable for cobra's benefit alone.
// Without a mark, every group would become a command those three have to
// classify, document and answer --json for.
const familyAnnotation = "hactl.family-group"

// family installs the group contract: an unknown subcommand is an error with a
// non-zero exit, and a bare invocation prints the group's help exactly as it
// always did.
//
// The RunE is what makes this work at all. Cobra returns flag.ErrHelp for a
// command with no Run *before* it validates arguments, and ExecuteC turns that
// into "print help, exit 0" — which is precisely the reported defect, and the
// reason setting Args alone on a help-only group changes nothing.
func family(cmd *cobra.Command) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[familyAnnotation] = "true"
	cmd.Args = takesNone()
	cmd.RunE = func(c *cobra.Command, _ []string) error { return c.Help() }
	// Runnable commands get a "<path> [flags]" line in their usage block. A
	// group takes no flags of its own and does nothing but print this very
	// help, so the line would advertise an invocation that exists only as a
	// side effect of the fix.
	cmd.DisableFlagsInUseLine = true
	return cmd
}

// isFamilyGroup reports whether a command only groups subcommands. See
// familyAnnotation for why the question is asked at all.
func isFamilyGroup(cmd *cobra.Command) bool {
	return cmd != nil && cmd.Annotations[familyAnnotation] == "true"
}
