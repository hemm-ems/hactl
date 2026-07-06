package cmd

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/docs"
	"github.com/hemm-ems/hactl/internal/manual"
)

var (
	flagRtfmCore     bool
	flagRtfmFamily   []string
	flagRtfmFamilies bool
)

var rtfmCmd = &cobra.Command{
	Use:   "rtfm",
	Short: "Print the full hactl manual",
	Long: "Display the embedded hactl manual. Intended for LLM self-teaching.\n" +
		"Use --core / --family for a token-frugal subset, --families to see the split.",
	RunE: func(cmd *cobra.Command, args []string) error {
		// rtfm IS the manual — cap it only when the user explicitly asks.
		if !rootCmd.PersistentFlags().Changed("tokensmax") {
			flagTokensMax = 0
		}
		w := cmd.OutOrStdout()
		switch {
		case flagRtfmFamilies:
			return runRTFMFamilies(w)
		case flagRtfmCore || len(flagRtfmFamily) > 0:
			return runRTFMSections(w, flagRtfmCore, flagRtfmFamily)
		}
		return runRTFM(w)
	},
}

func init() {
	rtfmCmd.Flags().BoolVar(&flagRtfmCore, "core", false,
		"print only the core manual (routing table, conventions, global flags)")
	rtfmCmd.Flags().StringArrayVar(&flagRtfmFamily, "family", nil,
		"print only this command family's how-to sections (repeatable, alias-aware)")
	rtfmCmd.Flags().BoolVar(&flagRtfmFamilies, "families", false,
		"list command families, their aliases and section sizes")
	rtfmCmd.MarkFlagsMutuallyExclusive("families", "core")
	rtfmCmd.MarkFlagsMutuallyExclusive("families", "family")
	rootCmd.AddCommand(rtfmCmd)
}

func runRTFM(w io.Writer) error {
	_, err := fmt.Fprint(w, docs.Manual)
	return err
}

func runRTFMSections(w io.Writer, core bool, families []string) error {
	var blocks []string
	if core {
		blocks = append(blocks, manual.CoreText())
	}
	seen := map[string]bool{}      // family dedup (e.g. --family trace --family auto)
	delivered := map[string]bool{} // heading dedup across overlapping families
	for _, name := range families {
		family, ok := manual.FamilyFor(name)
		if !ok {
			return fmt.Errorf("unknown family %q; valid: %s", name,
				strings.Join(manual.Families(), ", "))
		}
		if seen[family] {
			continue
		}
		seen[family] = true
		text, headings := manual.FamilyText(family, delivered)
		for _, h := range headings {
			delivered[h] = true
		}
		if text == "" {
			text = fmt.Sprintf("(no manual section for '%s'; see hactl %s --help)", family, family)
		}
		blocks = append(blocks, text)
	}
	_, err := fmt.Fprintln(w, strings.Join(blocks, "\n\n"))
	return err
}

func runRTFMFamilies(w io.Writer) error {
	aliases := map[string][]string{}
	for alias, family := range manual.Aliases {
		aliases[family] = append(aliases[family], alias)
	}
	_, _ = fmt.Fprintf(w, "core%28s~%d tok  (always delivered first)\n", "",
		estimateTokens(int64(len(manual.CoreText()))))
	for _, family := range manual.Families() {
		text, headings := manual.FamilyText(family, nil)
		a := aliases[family]
		sort.Strings(a)
		aliasNote := ""
		if len(a) > 0 {
			aliasNote = " (aliases: " + strings.Join(a, ", ") + ")"
		}
		_, _ = fmt.Fprintf(w, "%-12s%d sections  ~%d tok%s\n",
			family, len(headings), estimateTokens(int64(len(text))), aliasNote)
	}
	return nil
}
