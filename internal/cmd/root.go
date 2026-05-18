package cmd

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

var (
	flagDir       string
	flagSince     string
	flagTop       int
	flagFull      bool
	flagJSON      bool
	flagColor     bool
	flagStats     bool
	flagTokensMax int
)

var rootCmd = &cobra.Command{
	Use:   "hactl",
	Short: "CLI for Home Assistant analysis & development",
	Long:  "hactl – LLM-friendly CLI for Home Assistant analysis, debugging, and controlled automation management.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagDir, "dir", "", "instance directory (overrides HACTL_DIR and auto-discovery)")
	rootCmd.PersistentFlags().StringVar(&flagSince, "since", "24h", "time range for queries (e.g. 24h, 7d)")
	rootCmd.PersistentFlags().IntVar(&flagTop, "top", 10, "max items to display")
	rootCmd.PersistentFlags().BoolVar(&flagFull, "full", false, "show full/raw output")
	rootCmd.PersistentFlags().BoolVar(&flagJSON, "json", false, "output as JSON")
	rootCmd.PersistentFlags().BoolVar(&flagColor, "color", false, "enable colored output")
	rootCmd.PersistentFlags().BoolVar(&flagStats, "stats", false, "show response size and estimated token count")
	rootCmd.PersistentFlags().IntVar(&flagTokensMax, "tokensmax", 500, "cap output at N tokens (0 = no cap)")
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

// applyTokenPolicy writes data to dst prefixed with a token-estimate header.
// When flagTokensMax > 0 and the estimated tokens exceed the limit, output is
// truncated at a UTF-8 safe byte boundary and a hint is appended.
// JSON mode skips the header so output remains valid JSON.
func applyTokenPolicy(dst io.Writer, data []byte, cmdPath string) {
	if flagJSON {
		_, _ = dst.Write(data)
		return
	}
	tokens := estimateTokens(int64(len(data)))
	_, _ = fmt.Fprintf(dst, "[~%d tok]\n", tokens)
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
		return "try --component <name>, --errors, or --unique to reduce output"
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
	var capBuf bytes.Buffer
	rootCmd.SetOut(&capBuf)
	defer rootCmd.SetOut(nil)

	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}

	applyTokenPolicy(os.Stdout, capBuf.Bytes(), rootCmd.CommandPath())

	if flagStats {
		writeStats(os.Stderr, int64(capBuf.Len()))
	}
	return nil
}

// RunWithOutput executes the command with the given args and captures output to w.
// Used by integration tests to run hactl commands programmatically.
func RunWithOutput(args []string, w io.Writer) error {
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
		flagTokensMax = 500
		resetSubcommandFlags()
	}()

	err := rootCmd.Execute()

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
	flagTplFile = ""
	flagEntPattern = ""
	flagEntDomain = ""
	flagEntResample = ""
	flagEntAttr = ""
	flagEntArea = ""
	flagEntLabel = ""
	flagCCLogsUnique = false
	flagSvcData = "{}"
	flagScriptPattern = ""
	flagScriptLabel = ""
	flagScriptFailing = false
	flagLogErrors = false
	flagLogUnique = false
	flagLogComponent = ""
	flagLabelColor = ""
	flagLabelIcon = ""
	flagLabelDesc = ""
	flagDashView = ""
	flagDashRaw = false
	flagDashFile = ""
	flagDashConfirm = false
	flagDashTitle = ""
	flagDashURLPath = ""
	flagDashIcon = ""
	flagDashSidebar = true
	flagDashAdmin = false
	// Reset all cobra internal flags (including --help) on every command
	// to prevent stale flag state between repeated Execute() calls.
	resetCobraFlags(rootCmd)
}

// resetCobraFlags recursively resets all flags on a command and its children
// back to their default values. This is critical for cobra's built-in --help
// flag which, once set to true, causes all subsequent calls to print help.
func resetCobraFlags(cmd *cobra.Command) {
	cmd.Flags().VisitAll(func(f *pflag.Flag) {
		_ = f.Value.Set(f.DefValue)
		f.Changed = false
	})
	for _, sub := range cmd.Commands() {
		resetCobraFlags(sub)
	}
}
