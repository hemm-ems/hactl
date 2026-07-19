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
	"github.com/hemm-ems/hactl/internal/haapi"
)

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
)

var rootCmd = &cobra.Command{
	Use:   "hactl",
	Short: "CLI for Home Assistant analysis & development",
	Long: "hactl – LLM-friendly CLI for Home Assistant analysis, debugging, and controlled automation management.\n\n" +
		"project: " + projectURL + "\n" +
		"issues:  " + issuesURL,
	SilenceUsage:  true,
	SilenceErrors: true,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		haapi.DefaultTimeout = flagTimeout
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagDir, "dir", "", "instance directory (overrides HACTL_DIR and auto-discovery)")
	rootCmd.PersistentFlags().StringVar(&flagSince, "since", "24h", "time range for queries (e.g. 24h, 7d)")
	rootCmd.PersistentFlags().IntVar(&flagTop, "top", 10, "max items to display")
	rootCmd.PersistentFlags().BoolVar(&flagFull, "full", false, "show full/raw output")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "output as JSON")
	rootCmd.PersistentFlags().BoolVar(&flagColor, "color", false, "enable colored output")
	rootCmd.PersistentFlags().BoolVar(&flagStats, "stats", false, "show response size and estimated token count")
	rootCmd.PersistentFlags().BoolVar(&flagTokens, "tokens", false, "show compact token estimate")
	rootCmd.PersistentFlags().IntVar(&flagTokensMax, "tokensmax", 500, "cap output at N tokens (0 = no cap)")
	rootCmd.PersistentFlags().DurationVar(&flagTimeout, "timeout", 30*time.Second, "per-request timeout for HA/companion API calls")
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

// applyTokenPolicy writes data to dst and applies the output token cap.
// When flagTokens is set, text output gets a compact token-estimate header.
// When flagTokensMax > 0 and the estimated tokens exceed the limit, output is
// truncated at a UTF-8 safe byte boundary and a hint is appended.
// JSON mode skips the header and the cap so output remains valid JSON; when
// flagTokens is set, the compact token estimate goes to stderr instead.
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
	if flagTokensMax > 0 && tokens > int64(flagTokensMax) {
		limit := min(flagTokensMax*4, len(data))
		// Walk backward to a valid UTF-8 boundary
		for limit > 0 && !utf8.Valid(data[:limit]) {
			limit--
		}
		_, _ = dst.Write(data[:limit])
		hint := truncationHint(cmdPath)
		_, _ = fmt.Fprintf(dst, "\n\u2026output capped at %d tok; %s\n", flagTokensMax, hint)
	} else {
		_, _ = dst.Write(data)
	}
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
	// Manual delivery goes to stderr first, so a merged capture reads
	// manual → marker → result/error (the layout the tuning evals measured);
	// injection happens on errors too — that's when the agent needs it most.
	maybeInjectManual(executed, os.Args[1:])
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

	if capBuf.Len() > 0 {
		cmdPath := rootCmd.CommandPath()
		if executed != nil {
			// The leaf path, so truncationHint can give command-specific
			// advice (rootCmd.CommandPath() is always just "hactl").
			cmdPath = executed.CommandPath()
		}
		applyTokenPolicy(os.Stdout, capBuf.Bytes(), cmdPath)
	}

	if flagStats {
		writeStats(os.Stderr, int64(capBuf.Len()))
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
		flagSince = "24h"
		flagTop = 10
		flagFull = false
		flagJSON = false
		flagColor = false
		flagStats = false
		flagTokens = false
		flagTokensMax = 500
		resetSubcommandFlags()
	}()

	// Set the context on the target command explicitly: cobra only
	// propagates the root context to a subcommand whose ctx is still nil,
	// so a re-run command would otherwise keep the (long cancelled)
	// context of its previous invocation.
	if target, _, findErr := rootCmd.Find(args[1:]); findErr == nil {
		target.SetContext(ctx)
	}
	err := rootCmd.ExecuteContext(ctx)

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
	flagEntStale = false
	flagDevicePattern = ""
	flagDeviceName = ""
	flagDeviceArea = ""
	flagDeviceLabel = ""
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
