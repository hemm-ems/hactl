package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"
)

// Set via -ldflags at build time.
var (
	version  = "dev"
	commit   = "none"
	date     = "unknown"
	testedHA = "" // comma-separated HA versions tested against (e.g. "2026.4, 2026.3")
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print hactl version",
	Run: func(cmd *cobra.Command, args []string) {
		printVersion(cmd.OutOrStdout())
	},
}

func printVersion(w io.Writer) {
	_, _ = fmt.Fprintf(w, "hactl %s (commit %s, built %s)\n", version, commit, date)
	if testedHA != "" {
		_, _ = fmt.Fprintf(w, "tested: HA %s\n", testedHA)
	}
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
