package cmd

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var (
	flagGrepRegex         bool
	flagGrepCaseSensitive bool
	flagGrepAllowPartial  bool
)

// grepHalfCap is the most hit rows one half of a grep answer carries (D-48,
// oracle resolution 1). The companion caps the config half at the same number;
// the dashboard half is capped here. Each half's total is counted before the
// cap and always stated, so a truncated answer is never a silently short one.
const grepHalfCap = 1000

// grepValueWidth is how wide a hit's value renders in the text table. A leaf
// can be 2,303 characters (measured); the whole value is in --json and --full.
const grepValueWidth = 72

var grepCmd = &cobra.Command{
	Use:   "grep <term>",
	Short: "Find where a term appears across config files and dashboards (read-only)",
	Long: "Search every YAML config file (via the companion, following !include) and every dashboard " +
		"for a term, and report each place it appears: config-file string values and mapping keys, " +
		"dashboard string values. The match is a substring, case-insensitive by the simple Unicode " +
		"lowercase on both halves (Küche finds KÜCHE; straße does not find STRASSE); --case-sensitive " +
		"compares as written and --regex reads the term as a pattern from the subset Python and Go read " +
		"the same way (\\w \\d \\s \\b, lookaround and backreferences are refused). A literal term needs at " +
		"least 2 characters.\n\n" +
		"The answer opens with exact hit counts per source, then the hit rows. Each half returns at most " +
		"1000 rows and says so when it is truncated, with its exact total. grep never writes; to rename an " +
		"id everywhere use `hactl ref replace`. For an exact entity_id, `hactl ref scan` and " +
		"`hactl dash grep` match whole values.",
	Args: takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runGrep(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	grepCmd.Flags().BoolVar(&flagGrepRegex, "regex", false,
		"read the term as a regular expression (the Python/Go common subset; see `hactl rtfm`, \"Term search\")")
	grepCmd.Flags().BoolVar(&flagGrepCaseSensitive, "case-sensitive", false,
		"compare as written instead of case-insensitively")
	grepCmd.Flags().BoolVar(&flagGrepAllowPartial, "allow-partial", false,
		"search whatever could be read when a source is unavailable — the config files (or some of them), "+
			"or a dashboard whose config cannot be fetched. Without it, plain text still answers and states "+
			"the scope above the hits, while --json refuses")
	rootCmd.AddCommand(grepCmd)
}

// grepRow is one hit, from either half.
type grepRow struct {
	Source   string `json:"source"`   // "config" | "dashboard"
	Location string `json:"location"` // file name or dashboard
	Path     string `json:"path"`
	Key      string `json:"key"`  // nearest enclosing mapping key
	Kind     string `json:"kind"` // "key" | "value"
	Value    string `json:"value"`
}

// grepSource is one file's or dashboard's exact hit counts, before any cap.
type grepSource struct {
	Source    string `json:"source"`
	Location  string `json:"location"`
	KeyHits   int    `json:"key_hits"`
	ValueHits int    `json:"value_hits"`
}

// grepHalf is what one half returned against what it holds.
type grepHalf struct {
	Total     int  `json:"total"`
	Returned  int  `json:"returned"`
	Truncated bool `json:"truncated"`
}

// grepUnread is one source the sweep could not read.
type grepUnread struct {
	Source   string `json:"source"`
	Location string `json:"location"`
	Reason   string `json:"reason"`
}

// grepAnswer is `grep --json`'s document, and the one value both renderings
// are built from so the two cannot disagree about a count.
type grepAnswer struct {
	Term          string       `json:"term"`
	Regex         bool         `json:"regex"`
	CaseSensitive bool         `json:"case_sensitive"`
	Total         int          `json:"total"`
	Truncated     bool         `json:"truncated"`
	Config        grepHalf     `json:"config"`
	Dashboards    grepHalf     `json:"dashboards"`
	Sources       []grepSource `json:"sources"`
	Hits          []grepRow    `json:"hits"`
	Partial       bool         `json:"partial"`
	Unread        []grepUnread `json:"unread"`
}

func runGrep(ctx context.Context, w io.Writer, term string) error {
	// The term is the caller's input: a degenerate one, or a pattern the two
	// halves would read differently, ends the command before anything is
	// contacted (AC-5, AC-6).
	match, err := validateGrepTerm(term, flagGrepRegex, flagGrepCaseSensitive)
	if err != nil {
		return err
	}

	src, err := connectRefSources(ctx)
	if err != nil {
		return err
	}
	defer src.close()

	// Empty lists, not nil: a miss is a search's commonest answer, and the manual
	// documents sources and hits as lists a consumer can range over.
	ans := grepAnswer{Term: term, Regex: flagGrepRegex, CaseSensitive: flagGrepCaseSensitive,
		Sources: []grepSource{}, Hits: []grepRow{}}
	var scope sweepScope

	// Config half (companion). A 400 is the companion refusing the term or
	// pattern — the caller's input, exactly like the refusals above — so it
	// ends the command instead of becoming "the config files could not be
	// read" beside an answer.
	resp, cfgErr := src.cc.RefGrep(ctx, term, flagGrepRegex, flagGrepCaseSensitive)
	switch status, isStatus := haapi.HTTPStatus(cfgErr); {
	case cfgErr == nil:
		scope.configSkipped = unreadConfigFiles(resp.Skipped)
		addConfigHalf(&ans, resp)
	case isStatus && status == http.StatusBadRequest:
		return fmt.Errorf("the companion refused this search, so nothing was searched: %w", cfgErr)
	default:
		scope.configErr = cfgErr
	}

	// Dashboard half (WS), through the one shared walk.
	dashboards, err := src.ws.DashboardList(ctx)
	if err != nil {
		return fmt.Errorf("listing dashboards: %w", err)
	}
	targets := dashboardScanTargets(dashboards)
	matches, dashScope := matchDashboards(ctx, src.ws, targets, match)
	scope.dash = dashScope
	addDashboardHalf(&ans, targets, matches)

	// D-31: a search answers over what it could read and says so in the body;
	// under --json it refuses unless the caller acknowledged a partial answer.
	if gateErr := searchScopeGateError(scope, flagGrepAllowPartial,
		"a --json consumer can take its hits for the whole answer without reading `partial`"); gateErr != nil {
		return gateErr
	}
	ans.Partial = scope.partial()
	ans.Unread = grepUnreadSources(scope)
	ans.Total = ans.Config.Total + ans.Dashboards.Total
	ans.Truncated = ans.Config.Truncated || ans.Dashboards.Truncated

	if flagJSON {
		reportSweepScope(w, scope)
		return writeJSON(w, ans)
	}
	reportSweepScope(w, scope)
	return renderGrepText(w, ans)
}

// addConfigHalf folds the companion's answer into ans.
func addConfigHalf(ans *grepAnswer, resp *companion.RefGrepResponse) {
	ans.Config = grepHalf{Total: resp.Total, Returned: len(resp.Hits), Truncated: resp.Truncated}
	for _, s := range resp.Sources {
		ans.Sources = append(ans.Sources, grepSource{
			Source: "config", Location: s.Location, KeyHits: s.KeyHits, ValueHits: s.ValueHits,
		})
	}
	for _, h := range resp.Hits {
		ans.Hits = append(ans.Hits, grepRow{
			Source: "config", Location: h.Location, Path: h.Path, Key: h.Key, Kind: h.Kind, Value: h.MatchedValue,
		})
	}
}

// addDashboardHalf counts every dashboard match, then keeps the first
// grepHalfCap as rows: the per-dashboard counts are exact whatever the cap cut.
func addDashboardHalf(ans *grepAnswer, targets []dashScanTarget, matches []dashMatch) {
	perDashboard := make(map[string]int, len(targets))
	for _, m := range matches {
		perDashboard[m.dashboard]++
	}
	for _, t := range targets {
		if n := perDashboard[t.label]; n > 0 {
			ans.Sources = append(ans.Sources, grepSource{Source: "dashboard", Location: t.label, ValueHits: n})
		}
	}
	kept := matches
	if len(kept) > grepHalfCap {
		kept = kept[:grepHalfCap]
	}
	ans.Dashboards = grepHalf{Total: len(matches), Returned: len(kept), Truncated: len(kept) < len(matches)}
	for _, m := range kept {
		ans.Hits = append(ans.Hits, grepRow{
			Source: "dashboard", Location: m.dashboard, Path: m.path, Key: m.key, Kind: companion.GrepKindValue, Value: m.value,
		})
	}
}

// grepUnreadSources is the scope as rows a document can carry.
func grepUnreadSources(scope sweepScope) []grepUnread {
	out := []grepUnread{}
	if scope.configErr != nil {
		out = append(out, grepUnread{Source: "config", Location: "(all config files)", Reason: scope.configErr.Error()})
	}
	for _, f := range scope.configSkipped {
		out = append(out, grepUnread{Source: "config", Location: f.Location, Reason: f.Reason})
	}
	for _, u := range scope.dash.unscanned {
		label, reason, _ := strings.Cut(u, ": ")
		out = append(out, grepUnread{Source: "dashboard", Location: label, Reason: reason})
	}
	return out
}

// grepModeWords names the question the answer is to.
func grepModeWords(ans grepAnswer) string {
	kind := "substring"
	if ans.Regex {
		kind = "regex"
	}
	if ans.CaseSensitive {
		return kind + ", case-sensitive"
	}
	return kind + ", case-insensitive"
}

// renderGrepText writes the plain answer: the headline, the complete
// per-source summary, any truncation, then the hit rows under --top.
func renderGrepText(w io.Writer, ans grepAnswer) error {
	if ans.Total == 0 {
		if ans.Partial {
			_, _ = fmt.Fprintf(w, "no hit for %q in what could be searched (%s; see the partial-sweep note "+
				"above — the rest was not searched)\n", ans.Term, grepModeWords(ans))
			return nil
		}
		_, _ = fmt.Fprintf(w, "no hit for %q in any config file or dashboard (%s)\n", ans.Term, grepModeWords(ans))
		return nil
	}

	_, _ = fmt.Fprintf(w, "grep %q (%s): %d hit(s) in %d source(s) — config files %d, dashboards %d\n",
		ans.Term, grepModeWords(ans), ans.Total, len(ans.Sources), ans.Config.Total, ans.Dashboards.Total)

	summary := &format.Table{Headers: []string{"source", "location", "keys", "values"}}
	for _, s := range ans.Sources {
		keys := strconv.Itoa(s.KeyHits)
		if s.Source == "dashboard" {
			keys = "-" // dashboard keys are not searched (D-48)
		}
		summary.Rows = append(summary.Rows, []string{s.Source, s.Location, keys, strconv.Itoa(s.ValueHits)})
	}
	if err := summary.Render(w, format.RenderOpts{Full: true}); err != nil {
		return err
	}
	writeGrepTruncation(w, "config half", ans.Config)
	writeGrepTruncation(w, "dashboard half", ans.Dashboards)
	_, _ = fmt.Fprintln(w)

	hits := &format.Table{Headers: []string{"source", "location", "path", "key", "value"}}
	for _, h := range ans.Hits {
		hits.Rows = append(hits.Rows, []string{h.Source, h.Location, h.Path, h.Key, h.Value})
	}
	hits.SetWidth("value", grepValueWidth)
	return hits.Render(w, format.RenderOpts{
		Top:      flagTop,
		Full:     flagFull,
		MoreHint: "the counts above say where they are; narrow the term, or --top 0 / --json for every returned row",
	})
}

// writeGrepTruncation states a capped half, with its exact total.
func writeGrepTruncation(w io.Writer, half string, h grepHalf) {
	if !h.Truncated {
		return
	}
	_, _ = fmt.Fprintf(w, "truncated: the %s holds %d hit(s) and returns the first %d; the counts above are "+
		"exact — narrow the term to see the rest\n", half, h.Total, h.Returned)
}
