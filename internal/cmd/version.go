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

// flagVersion backs the root's own `--version`; see init for why cobra's is not
// used.
var flagVersion bool

// Canonical project URLs. Printed by `hactl version` and the root help so
// agents and users can find the issue tracker without inferring it from
// local remotes or forks (hemm-ems/hactl#43).
const (
	projectURL = "https://github.com/hemm-ems/hactl"
	issuesURL  = projectURL + "/issues"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Args:  takesNone(),
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

	// `--version` and `hactl version` are two spellings of one question, so
	// they go through one function.
	//
	// They used to go through two. Cobra answers its built-in `--version` from
	// a template string, before PersistentPreRun and before any Run, so
	// `hactl --version --json` printed the plain banner while `hactl version
	// --json` printed JSON — at exit 0, in either flag order, with nothing
	// saying --json had been dropped (H-25, #13). A template cannot read a
	// flag, so the fix is to stop using one: leaving rootCmd.Version empty
	// keeps cobra from installing its flag OR its handler, and the root's own
	// RunE answers instead.
	//
	// The flag is local to the root, exactly as cobra's was, so `ent ls
	// --version` stays an unknown flag — and flagErrorHelp now answers it with
	// where --version does live.
	rootCmd.Flags().BoolVarP(&flagVersion, "version", "v", false, "print version information and exit")
	helpForRoot := rootCmd.RunE
	rootCmd.RunE = func(c *cobra.Command, args []string) error {
		if flagVersion {
			printVersion(c.OutOrStdout())
			return nil
		}
		return helpForRoot(c, args)
	}
}
