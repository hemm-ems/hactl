package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var flagIssuesAll bool

var issuesCmd = &cobra.Command{
	Use:   "issues",
	Short: "Show active HA issues and repairs",
	Long: "Display currently active Home Assistant issues from the repairs integration " +
		"(all severities, including WARNING). Ignored issues are hidden unless --all is given.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIssues(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	issuesCmd.Flags().BoolVar(&flagIssuesAll, "all", false, "include ignored (dismissed) issues")
	rootCmd.AddCommand(issuesCmd)
}

// haIssue holds one repair issue from the HA repairs/list_issues WS API.
type haIssue struct {
	Domain            string `json:"domain"`
	IssueID           string `json:"issue_id"`
	Severity          string `json:"severity"`
	TranslateKey      string `json:"translation_key"`
	IsFixable         bool   `json:"is_fixable"`
	Ignored           bool   `json:"ignored"`
	BreaksInHAVersion string `json:"breaks_in_ha_version"`
}

// issuesResponse wraps the issues list from the HA repairs API.
type issuesResponse struct {
	Issues []haIssue `json:"issues"`
}

func runIssues(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	// The repair/issue registry is only exposed over the WebSocket API
	// (repairs/list_issues); there is no REST list endpoint. The handler
	// returns active issues of every severity, so WARNING-level domain repairs
	// show up here alongside ERROR ones.
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return fmt.Errorf("connecting for issues: %w", connErr)
	}
	data, err := ws.ListIssues(ctx)
	_ = ws.Close()
	if err != nil {
		return fmt.Errorf("fetching issues: %w", err)
	}

	var resp issuesResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("parsing issues: %w", err)
	}

	issues := resp.Issues
	if !flagIssuesAll {
		filtered := issues[:0:0]
		for _, issue := range issues {
			if !issue.Ignored {
				filtered = append(filtered, issue)
			}
		}
		issues = filtered
	}

	if len(issues) == 0 {
		if !flagIssuesAll {
			_, _ = fmt.Fprintln(w, "no active issues (use --all to include ignored)")
		} else {
			_, _ = fmt.Fprintln(w, "no active issues")
		}
		return nil
	}

	tbl := &format.Table{
		Headers: []string{"domain", "issue_id", "severity", "fixable", "ignored", "breaks_in"},
		Rows:    make([][]string, len(issues)),
	}
	for i, issue := range issues {
		tbl.Rows[i] = []string{
			issue.Domain,
			issue.IssueID,
			issue.Severity,
			yesNo(issue.IsFixable),
			yesNo(issue.Ignored),
			issue.BreaksInHAVersion,
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
