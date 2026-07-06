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
	text := manual.Claim(stateCacheDir(), manual.SessionKey(), mode, family, time.Now())
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
		manual.MarkDelivered(stateCacheDir(), manual.SessionKey(), time.Now(), scopes...)
	}
}

// stateCacheDir locates the per-instance cache dir for session state. It
// re-resolves rather than trusting config.ResolvedDir(), which commands like
// rtfm never set. May create ~/.hactl/default/cache before setup ever ran —
// harmless, setup uses the same directory. "" (unresolvable) makes delivery
// stateless (fail-open).
func stateCacheDir() string {
	dir := config.BestEffortDir(flagDir)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "cache")
}
