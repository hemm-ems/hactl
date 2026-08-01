package cmd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// defaultTimeout is the bound every connection carries when the caller names
// none. It is a constant rather than a literal in the flag declaration so the
// per-invocation reset and the declaration cannot drift.
const defaultTimeout = 30 * time.Second

var (
	flagDir       string
	flagSince     string
	flagTop       int
	flagFull      bool
	flagJSON      bool
	flagColor     bool
	flagStats     bool
	flagTokens    bool
	flagTokensMax int
	flagTimeout   time.Duration

	// helpRendered is set by the HelpFunc wrapper (see init) whenever cobra
	// actually rendered help this invocation — --help, -h, a bare non-runnable
	// command, or the built-in "help" subcommand all funnel through it. It is
	// read (never truncate) and reset (see RunWithOutputContext) by the
	// applyTokenPolicy pipeline.
	helpRendered bool

	// structuredOutput is set by a command that wrote a DOCUMENT rather than
	// prose — see markStructuredOutput. Read and reset by the same pipeline
	// helpRendered is.
	structuredOutput bool
)

var rootCmd = family(&cobra.Command{
	Use:   "hactl",
	Short: "CLI for Home Assistant analysis & development",
	Long: "hactl – LLM-friendly CLI for Home Assistant analysis, debugging, and controlled automation management.\n\n" +
		"project: " + projectURL + "\n" +
		"issues:  " + issuesURL,
	SilenceUsage:  true,
	SilenceErrors: true,
	// The domain check runs before the value is installed anywhere, so a
	// duration that cannot bound a connection never reaches one: `--timeout
	// -1s` used to arrive at net.Dialer as a deadline already in the past and
	// come back as `dial tcp: lookup <host>: i/o timeout` — a network failure
	// invented out of a flag value (H-25, #56).
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		if err := checkGlobalFlagDomains(); err != nil {
			return err
		}
		haapi.DefaultTimeout = flagTimeout
		return nil
	},
})

func init() {
	rootCmd.PersistentFlags().StringVar(&flagDir, "dir", "", "instance directory (overrides HACTL_DIR and auto-discovery)")
	// `--since` is deliberately NOT here: it is declared on the nine commands
	// that read it (see sinceCommands in since.go), because a flag on a command
	// that cannot act on it is the defect H-25 exists for.
	rootCmd.PersistentFlags().IntVar(&flagTop, "top", 10, "max items to display in tables (0 = every row); never truncates --json")
	rootCmd.PersistentFlags().BoolVar(&flagFull, "full", false, "show full/raw output")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "output as JSON")
	// --color is not yet implemented: no command emits ANSI escapes. The flag
	// is kept (removing it would be a breaking CLI change for anyone already
	// passing it) but it is currently a no-op; see H-10 notes / manual.md.
	rootCmd.PersistentFlags().BoolVar(&flagColor, "color", false, "enable colored output (currently a no-op; reserved for future use)")
	rootCmd.PersistentFlags().BoolVar(&flagStats, "stats", false, "show response size and estimated token count")
	rootCmd.PersistentFlags().BoolVar(&flagTokens, "tokens", false, "show compact token estimate")
	rootCmd.PersistentFlags().IntVar(&flagTokensMax, "tokensmax", 500,
		"cap output at N tokens (0 = no cap); never applied to --json or to a verbatim/raw document, "+
			"which would be truncated into something that no longer parses")
	// "per-request" was always the documented meaning and it was true of the
	// REST client; the WebSocket transport read a constant instead, so
	// `companion status --timeout 1s` returned after 10.02s while `health
	// --timeout 1s` returned after 1.01s. The wording is unchanged because it was
	// never wrong — H-23 is the code catching up with it — and the parenthesis
	// names the set, which is what a caller bounding worst-case latency needs.
	rootCmd.PersistentFlags().DurationVar(&flagTimeout, "timeout", defaultTimeout,
		"per-request timeout for HA/companion API calls (bounds every connection: REST, WebSocket, companion; must be positive)")

	// Cobra's built-in help output must never go through the --tokensmax cap
	// (defect C): wrap the default HelpFunc purely to record that help was
	// rendered this invocation, then delegate unchanged. helpRendered is
	// checked by applyTokenPolicy, the same place the --json exemption lives.
	defaultHelpFunc := rootCmd.HelpFunc()
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		helpRendered = true
		defaultHelpFunc(cmd, args)
	})

	// Cobra consults the nearest FlagErrorFunc up the parent chain, so one
	// installed here answers for every command in the tree. See flagErrorHelp:
	// a mistyped flag gets the help a mistyped command has always got, and a
	// flag that belongs to a different command is answered with its address.
	rootCmd.SetFlagErrorFunc(flagErrorHelp)
}

// statsWriter wraps an io.Writer and counts bytes written.
type statsWriter struct {
	inner io.Writer
	bytes int64
}

func (sw *statsWriter) Write(p []byte) (int, error) {
	n, err := sw.inner.Write(p)
	sw.bytes += int64(n)
	return n, err
}

// estimateTokens estimates token count from byte count.
// Approximation: ~4 characters per token for English text.
func estimateTokens(bytes int64) int64 {
	return (bytes + 3) / 4
}

// writeStats writes the stats footer to the given writer.
func writeStats(w io.Writer, byteCount int64) {
	tokens := estimateTokens(byteCount)
	_, _ = fmt.Fprintf(w, "---\nstats: %d bytes, ~%d tokens\n", byteCount, tokens)
}

// markStructuredOutput declares that what this command wrote is a DOCUMENT —
// JSON, YAML, a shell script, a config file — rather than prose a reader
// skims.
//
// The token cap chops at a byte boundary, walking back only far enough to keep
// the bytes valid UTF-8. On prose that is a truncated sentence; on a document
// it is a syntax error, delivered at exit 0 with a plain-English notice
// appended into the middle of the stream. `dash show lovelace-dev --raw`
// emitted 2 096 bytes of a 91 541-byte config that way and reported success,
// and `--raw`'s own help says it exists "for LLM round-trip editing" — the
// caller most likely to pipe it straight into a parser.
//
// The pole is the one --json already sets and docs/manual.md already
// documents: output whose contract is "this parses" is never capped, and the
// caller narrows it with filters instead. Truncating it is not a smaller
// answer, it is a broken one — and a loud refusal would be worse still, since
// the command CAN answer and the caller asked it to.
func markStructuredOutput() { structuredOutput = true }

// markGeneratedScript marks cobra's `completion <shell>` output as a document.
//
// It cannot be done where the other sites do it, because that command's body is
// cobra's, not this package's — and cobra adds it lazily during ExecuteC, so an
// init()-time wrapper would not always find it. The consequence was the same
// class of defect one layer over: `hactl completion bash > /etc/…`, the exact
// line the command's own --help prints, produced a shell script cut off
// mid-identifier at line 60 of 212, and `bash -n` on it exits 2.
func markGeneratedScript(c *cobra.Command) {
	for cur := c; cur != nil; cur = cur.Parent() {
		if cur.Name() == "completion" && cur.Parent() != nil && cur.Parent().Parent() == nil {
			markStructuredOutput()
			return
		}
	}
}

// applyTokenPolicy writes data to dst and applies the output token cap.
// When flagTokens is set, text output gets a compact token-estimate header.
// When flagTokensMax > 0 and the estimated tokens exceed the limit, output is
// truncated at a UTF-8 safe byte boundary and a hint is appended.
// JSON mode skips the header and the cap so output remains valid JSON; when
// flagTokens is set, the compact token estimate goes to stderr instead.
// Cobra help output (helpRendered) skips only the cap — never truncated,
// mid-word or otherwise — since it's the same documentation regardless of
// --tokensmax and a chopped help screen is worse than a long one. Structured
// output (structuredOutput) skips it for the same reason one step further on:
// a capped document does not parse at all — see markStructuredOutput.
func applyTokenPolicy(dst io.Writer, data []byte, cmdPath string) {
	if flagJSON {
		if flagTokens {
			fmt.Fprintf(os.Stderr, "[~%d tok]\n", estimateTokens(int64(len(data))))
		}
		_, _ = dst.Write(data)
		return
	}
	tokens := estimateTokens(int64(len(data)))
	if flagTokens {
		_, _ = fmt.Fprintf(dst, "[~%d tok]\n", tokens)
	}
	maxTok := effectiveTokensMax()
	if !helpRendered && !structuredOutput && maxTok > 0 && tokens > int64(maxTok) {
		limit := min(maxTok*4, len(data))
		// Walk backward to a valid UTF-8 boundary
		for limit > 0 && !utf8.Valid(data[:limit]) {
			limit--
		}
		_, _ = dst.Write(data[:limit])
		hint := truncationHint(cmdPath)
		_, _ = fmt.Fprintf(dst, "\n\u2026output capped at %d tok; %s\n", maxTok, hint)
	} else {
		_, _ = dst.Write(data)
	}
}

// effectiveTokensMax is the cap applyTokenPolicy enforces, after --full.
//
// `--full` is documented globally as "show full/raw output", and it did that
// for exactly one thing: format.Table's --top row cap. The 500-token prose cap
// sat behind it untouched, so the flag ranged from useless to actively
// harmful depending on which side of the table the command was on
// (finding #21, re-measured live 2026-07-31):
//
//   - `config entries --full` dropped the 10-row cap, produced 213 rows, and
//     was then chopped mid-row by the token cap at SEVEN \u2014 three rows FEWER
//     than the same command without the flag, and the last one cut in half.
//   - `config show <entry> --full` on an entry large enough to truncate was
//     byte-identical to `config show <entry>`: no rows to uncap, so the flag
//     had nothing to do and said nothing about it.
//
// One rule rather than a per-command wiring, because a flag that means
// something different in each of 60 commands is the defect and not the fix:
// --full lifts every cap on the answer's size, which is the only reading of
// "full" that both commands above satisfy at once.
//
// An explicit --tokensmax still wins. `--full --tokensmax 200` is a caller
// asking for every row and a bound on the bytes, and there is no reading under
// which a flag should silently discard a number the caller typed.
func effectiveTokensMax() int {
	if flagFull && !tokensMaxWasGiven() {
		return 0
	}
	return flagTokensMax
}

// tokensMaxWasGiven reports whether --tokensmax appeared on the command line
// this invocation, as opposed to holding its default.
func tokensMaxWasGiven() bool {
	f := rootCmd.PersistentFlags().Lookup("tokensmax")
	return f != nil && f.Changed
}

// emitEmptyList prints prose for humans when a listing has no results, or —
// with --json active — the same empty JSON array format.Table.Render would
// produce for zero rows ("[]"), never bare prose. Call sites whose non-json
// message varies (e.g. by the reason the result is empty) should call
// writeEmptyJSONArray directly for the --json branch instead.
func emitEmptyList(w io.Writer, prose string) error {
	if flagJSON {
		return writeEmptyJSONArray(w)
	}
	_, _ = fmt.Fprintln(w, prose)
	return nil
}

// writeEmptyJSONArray writes the empty-array JSON document ("[]") that
// format.Table.Render produces for zero rows, so --json output stays valid
// JSON even when a listing has nothing to show.
func writeEmptyJSONArray(w io.Writer) error {
	return (&format.Table{}).Render(w, format.RenderOpts{JSON: true})
}

// truncationHint returns a command-specific suggestion for reducing output.
func truncationHint(cmdPath string) string {
	switch {
	case strings.HasSuffix(cmdPath, " log"):
		return "try --component <name>, --errors, --warnings, or --unique to reduce output"
	case strings.HasSuffix(cmdPath, " ent ls"):
		return "try --domain <d>, --area <a>, --label <l>, or --pattern <glob> to reduce output"
	case strings.HasSuffix(cmdPath, " auto ls"):
		return "try --pattern <glob>, --label <l>, or --failing to reduce output"
	case strings.HasSuffix(cmdPath, " script ls"):
		return "try --pattern <glob>, --label <l>, or --failing to reduce output"
	case strings.Contains(cmdPath, " ent show"):
		if flagFull {
			return "try removing --full to see summary only"
		}
		return "use --tokensmax=0 to remove cap or apply filters to reduce output"
	default:
		return "use --tokensmax=0 to remove cap or apply filters to reduce output"
	}
}

// Execute runs the root command.
func Execute() error {
	// Before anything runs: a first-of-family --confirm from an agent-shaped
	// caller is refused (see confirmGuard) — the write must not execute.
	if err := confirmGuard(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}

	var capBuf bytes.Buffer
	rootCmd.SetOut(&capBuf)
	defer rootCmd.SetOut(nil)

	executed, err := rootCmd.ExecuteC()
	markGeneratedScript(executed)
	// Manual delivery goes to stderr first, so a merged capture reads
	// manual → marker → result/error (the layout the tuning evals measured);
	// injection happens on errors too — that's when the agent needs it most.
	maybeInjectManual(executed, os.Args[1:])

	// --stats is documented as printing "after any command", and used to be
	// skipped on the error path — the run whose cost a caller most wants to
	// know, because it is the one they are about to retry.
	if flagStats {
		writeStats(os.Stderr, int64(capBuf.Len()))
	}

	// An error ENDS the command; it does not ERASE what the command already
	// printed. This flush used to sit on the success path only, so every site
	// that renders an answer and then returns an error lost the answer: `ref
	// validate --exit-code` (the verdict IS the error — a real instance with 429
	// dangling references printed one line of stderr and zero bytes of stdout),
	// and `ref replace`'s two post-report refusals, whose own comments say the
	// report is rendered first "so the caller sees what is stuck".
	//
	// The manual's promise for bad input — exit 1, stderr, empty stdout — is
	// unaffected, because a refusal refuses BEFORE it renders: there is nothing
	// in the buffer to flush. It was never the entry point deleting output that
	// kept that promise. RunWithOutputContext, the path the MCP server and the
	// integration tier take, has always flushed on both paths, so the CLI was
	// the one of the two entry points that answered differently.
	if capBuf.Len() > 0 {
		cmdPath := rootCmd.CommandPath()
		if executed != nil {
			// The leaf path, so truncationHint can give command-specific
			// advice (rootCmd.CommandPath() is always just "hactl").
			cmdPath = executed.CommandPath()
		}
		applyTokenPolicy(os.Stdout, capBuf.Bytes(), cmdPath)
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		// Name the instance the failing command was talking to — with
		// multi-instance discovery the target is otherwise invisible.
		var nf *config.ConfigNotFoundError
		if dir := config.ResolvedDir(); dir != "" && !errors.As(err, &nf) {
			fmt.Fprintf(os.Stderr, "instance: %s\n", dir)
		}
		return err
	}

	return nil
}

// RunWithOutput executes the command with the given args and captures output to w.
// Used by integration tests to run hactl commands programmatically.
func RunWithOutput(args []string, w io.Writer) error {
	return RunWithOutputContext(context.Background(), args, w)
}

// RunWithOutputContext is RunWithOutput with a caller-supplied context. The
// MCP server uses it so that a client cancelling a tool call aborts the
// in-flight HA requests instead of leaving the command running.
func RunWithOutputContext(ctx context.Context, args []string, w io.Writer) error {
	var capBuf bytes.Buffer
	rootCmd.SetOut(&capBuf)
	rootCmd.SetArgs(args[1:]) // skip "hactl" binary name
	defer func() {
		rootCmd.SetOut(nil)
		rootCmd.SetArgs(nil)
		// Reset flags to defaults for next invocation
		flagDir = ""
		flagSince = defaultSinceWindow
		flagTop = 10
		flagFull = false
		flagJSON = false
		flagColor = false
		flagStats = false
		flagTokens = false
		flagTokensMax = 500
		flagTimeout = defaultTimeout
		helpRendered = false
		structuredOutput = false
		// pflag records `Changed` on the flag object, which outlives an
		// invocation exactly as the variables above do. Leaving it set would
		// make one `--tokensmax 200` look, to every later in-process run, like
		// a number the caller had typed — see effectiveTokensMax.
		rootCmd.PersistentFlags().VisitAll(func(f *pflag.Flag) { f.Changed = false })
		resetSubcommandFlags()
	}()

	// Cleared BEFORE the run, not after it like the flag values above: this one
	// is an observation of the invocation rather than state carried into it, and
	// a reset in the deferred block would erase the answer before the caller
	// could read it. TestEveryCommandDeclaringSinceReadsIt is that caller.
	sinceWasRead = false

	// Set the context on the target command explicitly: cobra only
	// propagates the root context to a subcommand whose ctx is still nil,
	// so a re-run command would otherwise keep the (long cancelled)
	// context of its previous invocation.
	if target, _, findErr := rootCmd.Find(args[1:]); findErr == nil {
		target.SetContext(ctx)
	}
	err := rootCmd.ExecuteContext(ctx)

	// After, not before: cobra adds the `completion` subtree lazily during
	// ExecuteC, so a Find ahead of the run cannot resolve it and the mark would
	// never land. Execute() marks off ExecuteC's own return value for the same
	// reason.
	if target, _, findErr := rootCmd.Find(args[1:]); findErr == nil {
		markGeneratedScript(target)
	}

	cmdPath := "hactl " + strings.Join(args[1:], " ")
	applyTokenPolicy(w, capBuf.Bytes(), cmdPath)

	if flagStats {
		writeStats(w, int64(capBuf.Len()))
	}

	return err
}

// resetSubcommandFlags resets all subcommand-specific flags to their defaults.
// This prevents flag value leakage between consecutive RunWithOutput calls in tests.
func resetSubcommandFlags() {
	flagAutoFailing = false
	flagAutoPattern = ""
	flagAutoLabel = ""
	flagAutoFile = ""
	flagAutoConfirm = false
	flagRtfmCore = false
	flagRtfmFamily = nil
	flagRtfmFamilies = false
	flagTplFile = ""
	flagEntPattern = ""
	flagEntDomain = ""
	flagEntResample = ""
	flagEntAttr = ""
	flagEntArea = ""
	flagEntLabel = ""
	flagEntConfirm = false
	flagEntRenameAllowPartial = false
	flagEntStale = false
	flagDevicePattern = ""
	flagDeviceName = ""
	flagDeviceArea = ""
	flagDeviceLabel = ""
	flagDeviceConfirm = false
	flagHelperPattern = ""
	flagHelperName = ""
	flagCCLogsUnique = false
	flagSvcData = "{}"
	flagSvcReturn = false
	flagSvcConfirm = false
	flagScriptPattern = ""
	flagScriptLabel = ""
	flagScriptFailing = false
	flagScriptFile = ""
	flagScriptConfirm = false
	flagLogErrors = false
	flagLogUnique = false
	flagLogComponent = ""
	flagLabelColor = ""
	flagLabelIcon = ""
	flagLabelDesc = ""
	flagLabelConfirm = false
	flagSetupURL = ""
	flagSetupToken = ""
	flagSetupForce = false
	flagAreaConfirm = false
	flagFloorConfirm = false
	flagDashView = ""
	flagDashRaw = false
	flagDashYAML = false
	flagConfigFileRaw = false
	flagDashFile = ""
	flagDashConfirm = false
	flagDashTitle = ""
	flagDashURLPath = ""
	flagDashIcon = ""
	flagDashSidebar = true
	flagDashAdmin = false
	flagRefConfirm = false
	flagRefExitCode = false
	flagRefAllowPartial = false
	flagDashAllowPartial = false
	// Reset all cobra internal flags (including --help) on every command
	// to prevent stale flag state between repeated Execute() calls.
	resetCobraFlags(rootCmd)
}

// resetCobraFlags recursively resets all flags on a command and its children
// back to their default values. This is critical for cobra's built-in --help
// flag which, once set to true, causes all subsequent calls to print help.
func resetCobraFlags(cmd *cobra.Command) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		// Slice flags append on Set, so Set(DefValue) would grow them with a
		// literal "[]" element instead of clearing.
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	})
	for _, sub := range cmd.Commands() {
		resetCobraFlags(sub)
	}
}
