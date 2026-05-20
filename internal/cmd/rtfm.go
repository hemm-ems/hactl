package cmd

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/docs"
)

var rtfmCmd = &cobra.Command{
	Use:   "rtfm",
	Short: "Print the full hactl manual",
	Long:  "Display the embedded hactl manual. Intended for LLM self-teaching.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// rtfm IS the manual — cap it only when the user explicitly asks.
		if !rootCmd.PersistentFlags().Changed("tokensmax") {
			flagTokensMax = 0
		}
		return runRTFM(cmd.OutOrStdout())
	},
}

func init() {
	rootCmd.AddCommand(rtfmCmd)
}

func runRTFM(w io.Writer) error {
	_, err := fmt.Fprint(w, docs.Manual)
	return err
}
