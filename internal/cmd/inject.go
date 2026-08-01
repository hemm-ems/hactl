package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/manual"
)

// isTerminal is a var so tests can force the non-TTY (agent) path.
var isTerminal = func(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// maybeInjectManual is the plain-CLI counterpart of the MCP server's manual
// delivery: when an agent runs hactl through a shell (both stdout and stderr
// captured), the progressive manual goes to stderr with the first command of
// a session — stdout stays byte-identical for pipes, --json, and goldens.
// Runs only from Execute(); RunWithOutputContext (MCP, tests) never injects.
func maybeInjectManual(executed *cobra.Command, rawArgs []string) {
	mode := manual.ModeFromEnv()
	stdoutTTY, stderrTTY := isTerminal(os.Stdout), isTerminal(os.Stderr)
	top := topCommandName(executed)

	if top == "rtfm" {
		// rtfm prints manual content on stdout itself; record what it
		// covered (same gating) so the hook doesn't deliver it again.
		if mode != manual.ModeOff && !stdoutTTY && !stderrTTY {
			markRTFMDelivered()
		}
		return
	}
	if !shouldInject(mode, stdoutTTY, stderrTTY, top, len(rawArgs) == 0) {
		return
	}

	family, _ := manual.FamilyFor(top) // unknown command ⇒ "" ⇒ core only
	text := manual.Claim(stateCacheDir(flagDir), manual.SessionKey(), mode, family, time.Now())
	if text == "" {
		return
	}
	// The trailing marker reproduces the tuned manual-before-result layout in
	// any merged (2>&1 or stderr-then-stdout) capture; the note prefixes and
	// this marker are parsed by dev/tuning/inject_tokens.py.
	fmt.Fprintf(os.Stderr, "%s\n\n=== RESULT of hactl %s ===\n", text, strings.Join(rawArgs, " "))
}

// shouldInject implements the gating table: delivery is on by default but
// only for agent-shaped invocations — a TTY on either stream means a human
// is watching, a bare invocation is just the help screen, and exempt
// commands handle the manual themselves or must stay clean (mcp, setup,
// completion machinery).
//
// Delivery is decided by who is listening, never by what shape the answer takes
// (H-25). `--json` used to be a fourth condition here, and the cost was
// measured on a real instance: with a brand-new HACTL_SESSION, `health --json`
// and `device ls --json` wrote ZERO bytes to stderr and recorded no session at
// all, while the same commands without the flag delivered 10 262 and 11 742
// bytes — so an agent that reads only structured output, the exact caller this
// manual is written for, never received the routing table, the confirm
// convention or any family how-to, with nothing saying anything had been
// skipped (#50).
//
// hactl already disagreed with itself about this: confirmGuard delivers the
// how-to under `--json` (12 447 bytes, measured in the same run), because its
// refusal is meaningless without it. The argument for the exemption was that
// agent harnesses merge stdout and stderr and prose would corrupt the JSON
// stream — but that is already true of every error hactl prints and of the
// slog warnings it emits mid-command, both of which go to stderr under `--json`
// today. `--json` is a promise about STDOUT, and the manual has never been on
// stdout: docs/manual.md's own delivery section says so in as many words.
func shouldInject(mode manual.Mode, stdoutTTY, stderrTTY bool, top string, bareInvocation bool) bool {
	if mode == manual.ModeOff || stdoutTTY || stderrTTY || bareInvocation {
		return false
	}
	return !manual.Exempt[top]
}

// topCommandName returns the name of the top-level command an execution
// resolved to, or "" for the root itself (bare call, unknown command).
func topCommandName(c *cobra.Command) string {
	if c == nil || c == rootCmd {
		return ""
	}
	for c.Parent() != nil && c.Parent() != rootCmd {
		c = c.Parent()
	}
	return c.Name()
}

// markRTFMDelivered records which manual parts an rtfm invocation printed.
func markRTFMDelivered() {
	var scopes []string
	switch {
	case flagRtfmFamilies:
		return // listing only — no manual content was shown
	case flagRtfmCore || len(flagRtfmFamily) > 0:
		if flagRtfmCore {
			scopes = append(scopes, "core")
		}
		for _, name := range flagRtfmFamily {
			if f, ok := manual.FamilyFor(name); ok {
				scopes = append(scopes, f)
			}
		}
	default:
		scopes = []string{"all"}
	}
	if len(scopes) > 0 {
		manual.MarkDelivered(stateCacheDir(flagDir), manual.SessionKey(), time.Now(), scopes...)
	}
}

// confirmGuard refuses a --confirm write that no dry-run of the same target
// preceded, in an agent-shaped invocation: nothing showed the caller what the
// write would change, so nothing informed it (the measured F4 shape —
// dev/tuning e08). The refusal delivers core + how-to in the usual layout and
// exits 1, making the retry an informed one; the documented
// dry-run→present→confirm sequence never triggers it, because the dry-run
// records the witness. Agent-shaped scripts that intend blind writes opt out
// with HACTL_MANUAL_MODE=off.
//
// It used to ask whether the family how-to had reached the session, and that
// question is answered out of state every process sharing the instance
// directory can write — see witness.go for the live measurement of one
// process switching the guard off for another (#61). It was also weaker than
// it read: `auto ls` delivers the automation how-to, so `auto ls` followed by
// `auto apply --confirm` passed a guard whose whole subject is writes nobody
// previewed.
//
// cmd is the command cobra resolved and args its positional arguments, so the
// target this checks is the target that would be written — not a string
// scanned out of unparsed argv.
func confirmGuard(cmd *cobra.Command, args []string, rawArgs []string) error {
	mode := manual.ModeFromEnv()
	if mode == manual.ModeOff || isTerminal(os.Stdout) || isTerminal(os.Stderr) {
		return nil
	}
	if cmd == nil || cmd.Flags().Lookup("confirm") == nil {
		return nil
	}
	if confirmed, err := cmd.Flags().GetBool("confirm"); err != nil || !confirmed {
		//nolint:nilerr // a --confirm that does not parse is cobra's error to report, not this guard's
		return nil
	}
	// Stateless delivery (cacheDir "") cannot record a preview, so it cannot
	// tell "never previewed" from "cannot remember" — and refusing forever
	// would brick every caller with no resolvable instance directory. Fail
	// open, like delivery itself does.
	cacheDir := stateCacheDir(flagDir)
	if cacheDir == "" {
		return nil
	}
	if hasWitness(cacheDir, manual.SessionKey(), cmd.CommandPath(), args, time.Now()) {
		return nil
	}

	top := topCommandName(cmd)
	if family, ok := manual.FamilyFor(top); ok && len(manual.FamilySections[family]) > 0 {
		if text := manual.Claim(cacheDir, manual.SessionKey(), mode, family, time.Now()); text != "" {
			fmt.Fprintf(os.Stderr, "%s\n\n=== RESULT of hactl %s ===\n", text, strings.Join(rawArgs, " "))
		}
	}
	// Name the dry-run to run, verbatim. A refusal a caller cannot act on is a
	// worse answer than the silence it replaced (H-25's third lesson), and the
	// dry-run form of any write is the same command with --confirm removed.
	return fmt.Errorf("--confirm refused: no dry-run of %q was recorded for this instance in the last %s, "+
		"so nothing has shown what this write would change — run `%s` without --confirm, present the plan "+
		"to the user, and repeat with --confirm only after the user explicitly confirms "+
		"(scripts: HACTL_MANUAL_MODE=off)",
		dryRunForm(cmd, args), witnessTTL, dryRunForm(cmd, args))
}

// dryRunForm renders the preview a caller has to run before this write: the
// resolved command and its target, which is exactly what witnessKey records.
func dryRunForm(cmd *cobra.Command, args []string) string {
	return strings.TrimSpace(cmd.CommandPath() + " " + strings.Join(args, " "))
}

// noteDryRun records a successful preview so the matching --confirm is
// allowed. It is the other half of confirmGuard and deliberately sits beside
// it: the two must agree on what "the same write" means, and they agree by
// calling one witnessKey.
func noteDryRun(cmd *cobra.Command, args []string) {
	if cmd == nil || cmd.Flags().Lookup("confirm") == nil {
		return
	}
	if confirmed, err := cmd.Flags().GetBool("confirm"); err != nil || confirmed {
		return
	}
	recordWitness(stateCacheDir(flagDir), manual.SessionKey(), cmd.CommandPath(), args, time.Now())
}

// stateCacheDir locates the per-instance cache dir for session state. It
// re-resolves rather than trusting config.ResolvedDir(), which commands like
// rtfm never set. May create ~/.hactl/default/cache before setup ever ran —
// harmless, setup uses the same directory. "" (unresolvable) makes delivery
// stateless (fail-open).
func stateCacheDir(dirFlag string) string {
	dir := config.BestEffortDir(dirFlag)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "cache")
}
