package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var issuesCmd = &cobra.Command{
	Use:   "issues",
	Short: "Show active HA issues and repairs",
	Long:  "Display currently active Home Assistant issues from the repairs integration.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runIssues(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	rootCmd.AddCommand(issuesCmd)
}

// haIssue holds one repair issue from the HA API.
type haIssue struct {
	Domain       string `json:"domain"`
	IssueID      string `json:"issue_id"`
	Severity     string `json:"severity"`
	TranslateKey string `json:"translation_key"`
	IsFixable    bool   `json:"is_fixable"`
}

// issuesResponse wraps the issues list from HA repairs API.
type issuesResponse struct {
	Issues []haIssue `json:"issues"`
}

func runIssues(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.GetIssues(ctx)
	if err != nil {
		// The /api/repairs/issues endpoint may not exist in all HA versions
		if strings.Contains(err.Error(), "404") {
			_, _ = fmt.Fprintln(w, "no active issues")
			return nil
		}
		return fmt.Errorf("fetching issues: %w", err)
	}

	// HA repairs API may return {"issues": [...]} or just an array
	var resp issuesResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		// Try plain array fallback
		var issues []haIssue
		if arrErr := json.Unmarshal(data, &issues); arrErr != nil {
			return fmt.Errorf("parsing issues: %w", err)
		}
		resp.Issues = issues
	}

	if len(resp.Issues) == 0 {
		_, _ = fmt.Fprintln(w, "no active issues")
		return nil
	}

	tbl := &format.Table{
		Headers: []string{"domain", "issue_id", "severity", "fixable"},
		Rows:    make([][]string, len(resp.Issues)),
	}
	for i, issue := range resp.Issues {
		fixable := "no"
		if issue.IsFixable {
			fixable = "yes"
		}
		tbl.Rows[i] = []string{
			issue.Domain,
			issue.IssueID,
			issue.Severity,
			fixable,
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}
