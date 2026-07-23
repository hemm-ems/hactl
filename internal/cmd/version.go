package cmd

import (
	"encoding/json"
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

// Canonical project URLs. Printed by `hactl version` and the root help so
// agents and users can find the issue tracker without inferring it from
// local remotes or forks (hemm-ems/hactl#43).
const (
	projectURL = "https://github.com/hemm-ems/hactl"
	issuesURL  = projectURL + "/issues"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print hactl version",
	Run: func(cmd *cobra.Command, args []string) {
		printVersion(cmd.OutOrStdout())
	},
}

// versionInfo is the structured form of `hactl version`, used verbatim for
// --json output.
type versionInfo struct {
	Version  string `json:"version"`
	Commit   string `json:"commit"`
	Date     string `json:"date"`
	TestedHA string `json:"tested_ha,omitempty"`
	Project  string `json:"project"`
	Issues   string `json:"issues"`
}

func printVersion(w io.Writer) {
	if flagJSON {
		info := versionInfo{
			Version:  version,
			Commit:   commit,
			Date:     date,
			TestedHA: testedHA,
			Project:  projectURL,
			Issues:   issuesURL,
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		_ = enc.Encode(info)
		return
	}

	_, _ = fmt.Fprintf(w, "hactl %s (commit %s, built %s)\n", version, commit, date)
	if testedHA != "" {
		_, _ = fmt.Fprintf(w, "tested: HA %s\n", testedHA)
	}
	_, _ = fmt.Fprintf(w, "project: %s\n", projectURL)
	_, _ = fmt.Fprintf(w, "issues:  %s\n", issuesURL)
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
