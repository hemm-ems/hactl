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

// confirmGuard refuses a --confirm write fired as the first command of its
// family in an agent-shaped session: the family how-to is only delivered
// *with* that command's result, so nothing informed the write (the measured
// F4 shape — dev/tuning e08). The refusal delivers core + how-to in the
// usual layout and exits 1, making the retry an informed one; a proper
// dry-run→confirm sequence never triggers it because the dry-run call
// delivers the how-to first. Agent-shaped scripts that intend blind writes
// opt out with HACTL_MANUAL_MODE=off.
func confirmGuard(rawArgs []string) error {
	if !hasConfirmArg(rawArgs) {
		return nil
	}
	mode := manual.ModeFromEnv()
	if mode == manual.ModeOff || isTerminal(os.Stdout) || isTerminal(os.Stderr) {
		return nil
	}
	cmd, _, err := rootCmd.Find(rawArgs)
	if err != nil || cmd == nil || cmd.Flags().Lookup("confirm") == nil {
		//nolint:nilerr // fail-open by design: not a write command, or an unknown command that must error in cobra, not here
		return nil
	}
	top := topCommandName(cmd)
	family, ok := manual.FamilyFor(top)
	if !ok || len(manual.FamilySections[family]) == 0 {
		return nil
	}
	// The guard runs before cobra parses flags, so --dir must come from the
	// raw args; stateless delivery (cacheDir "") cannot track "seen" and the
	// guard would refuse forever — fail open like delivery itself does.
	cacheDir := stateCacheDir(dirFromArgs(rawArgs))
	if cacheDir == "" {
		return nil
	}
	if !manual.HowToPending(cacheDir, manual.SessionKey(), mode, family, time.Now()) {
		return nil
	}
	if text := manual.Claim(cacheDir, manual.SessionKey(), mode, family, time.Now()); text != "" {
		fmt.Fprintf(os.Stderr, "%s\n\n=== RESULT of hactl %s ===\n", text, strings.Join(rawArgs, " "))
	}
	// Name the command the caller actually ran, and the family by every command
	// it covers. Interpolating the internal family key told an agent that had
	// only ever run `area create` that this was its "first label command" —
	// false about its session, and actionable in the wrong direction.
	return fmt.Errorf("--confirm refused: the how-to for hactl's %s commands (delivered above) had not "+
		"reached this session, so it could not have informed this %q call — run the dry-run form, "+
		"present the plan to the user, and repeat with --confirm only after the user explicitly "+
		"confirms (scripts: HACTL_MANUAL_MODE=off)", manual.FamilyLabel(family), top)
}

// hasConfirmArg scans unparsed args for the --confirm flag.
func hasConfirmArg(args []string) bool {
	for _, a := range args {
		if a == "--confirm" || a == "--confirm=true" {
			return true
		}
	}
	return false
}

// dirFromArgs extracts a --dir value from unparsed args.
func dirFromArgs(args []string) string {
	for i, a := range args {
		if a == "--dir" && i+1 < len(args) {
			return args[i+1]
		}
		if v, ok := strings.CutPrefix(a, "--dir="); ok {
			return v
		}
	}
	return ""
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
