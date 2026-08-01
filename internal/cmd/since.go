package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// defaultSinceWindow is the look-back every command that takes --since starts
// from.
const defaultSinceWindow = "24h"

// sinceCommands is every command that reads the window — which, since H-25, is
// also every command that DECLARES the flag.
//
// `--since` used to be a root persistent flag: declared on all 112 commands,
// read by these nine. `area ls --since garbage-value-xyz` exited 0 with output
// byte-identical to `area ls`, and six more families did the same, while `log`
// and `changes` refused the identical value — so whether a mistyped window was
// an error depended on which command the caller happened to be running
// (#54). Every one of those help screens advertised the flag.
//
// Declaring it here rather than gating it at the root is what makes the fix
// structural: cobra refuses an undeclared flag on its own, `area ls --help`
// stops promising an effect that does not exist, and there is no registry of
// "commands exempt from --since" to fall out of step with the code. What the
// caller gets instead is the flag's address — flagErrorHelp answers `area ls
// --since 1h` by naming the commands below.
func sinceCommands() []*cobra.Command {
	return []*cobra.Command{
		autoLsCmd, ccLogsCmd, changesCmd, companionLogsCmd,
		entAnomaliesCmd, entHistCmd, entWhoCmd, logCmd, scriptLsCmd,
	}
}

func init() {
	for _, c := range sinceCommands() {
		c.Flags().StringVar(&flagSince, "since", defaultSinceWindow, "time range for queries (e.g. 24h, 7d)")
	}
}

// sinceWasRead records that the running command consulted the window. It is
// reset per invocation alongside the flag values (RunWithOutputContext).
//
// It exists so that sinceCommands can be PROVEN to be the consumption set
// rather than asserted to be: TestEveryCommandDeclaringSinceReadsIt drives each
// of the nine through the real entry point and fails on one that declares the
// flag and never looks at it — the exact state the whole tree was in.
var sinceWasRead bool

// sinceWindow is the only read of the --since flag. Every consumer goes through
// it, so the instrumentation above cannot be bypassed by a new call site.
func sinceWindow() string {
	sinceWasRead = true
	return flagSince
}

// parseSince converts a duration string like "24h" or "7d" to time.Duration.
// Shared by every command that honors the --since flag.
//
// --since names how far BACK to look, so a negative value is always a mistake.
// Accepting one silently inverted the query window (since > until), which HA
// answers with an empty result — indistinguishable from "nothing happened".
// Under the manual's "stop at the first miss" rule that reads as a confident
// negative answer, so this rejects rather than guessing at the intent.
func parseSince(s string) (time.Duration, error) {
	d, err := parseSinceRaw(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid duration: %s (--since is a look-back window; it cannot be negative)", s)
	}
	return d, nil
}

// runsColumn is the name of the run-count column, for the window the caller
// actually asked for.
//
// The column was called `runs_24h` whatever `--since` said, so `auto ls
// --pattern x --since 1h --json` answered `"runs_24h": "0"` for a count that
// covered one hour (#72). A name is not a label beside the value, it is a claim
// ABOUT the value, and a JSON consumer that trusts the key over the invocation
// that produced it reads the wrong number — which is the whole of H-11 one
// layer up from the count itself.
//
// Derived from the flag rather than from a table, so it cannot fall out of step
// with it: the default `--since 24h` still produces exactly `runs_24h`, and no
// caller who did not touch the flag sees any change. A window hactl cannot name
// falls back to `runs`, because an unnamed count is honest and a misnamed one is
// not.
func runsColumn(since string) string {
	if !validWindowName(since) {
		return "runs"
	}
	return "runs_" + strings.ToLower(since)
}

// validWindowName reports whether a --since value can be spelled inside a
// column name: letters and digits only, so the key stays a plain identifier for
// whatever reads the JSON.
func validWindowName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		default:
			return false
		}
	}
	return true
}

func parseSinceRaw(s string) (time.Duration, error) {
	if after, found := strings.CutSuffix(s, "d"); found {
		days, err := strconv.Atoi(after)
		if err != nil {
			return 0, fmt.Errorf("invalid duration: %s", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}
	return d, nil
}
