package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/jsonwalk"
)

var flagRefConfirm bool

var refCmd = &cobra.Command{
	Use:   "ref",
	Short: "Find and rename entity references across config files and dashboards",
	Long: "Scan and rewrite literal entity_id references everywhere they appear — YAML config files " +
		"(via the companion, following !include) and Lovelace dashboards (via the WebSocket API) — in one pass.",
}

var refScanCmd = &cobra.Command{
	Use:   "scan <target>",
	Short: "Find every reference to a value across config and dashboards",
	Long: "Scan all YAML config files (following !include) and every dashboard for an exact string " +
		"(typically an entity_id) and report the source, location, and path of each reference.",
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRefScan(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var refReplaceCmd = &cobra.Command{
	Use:   "replace <old> <new>",
	Short: "Rename an entity reference everywhere (dry-run by default)",
	Long: "Replace every exact occurrence of <old> with <new> across all YAML config files and every " +
		"storage-mode dashboard in one pass. Dry-run by default; use --confirm to apply. References in " +
		"non-storage-mode dashboards are reported but not rewritten.",
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runRefReplace(cmd.Context(), cmd.OutOrStdout(), args[0], args[1])
	},
}

func init() {
	refReplaceCmd.Flags().BoolVar(&flagRefConfirm, "confirm", false, "actually apply changes (default is dry-run)")
	refCmd.AddCommand(refScanCmd, refReplaceCmd)
	rootCmd.AddCommand(refCmd)
}

// refSources bundles the connections a ref command needs: a live WS client for
// dashboards and a companion client for config files.
type refSources struct {
	ws *haapi.WSClient
	cc *companion.Client
}

func (s *refSources) close() {
	if s.ws != nil {
		_ = s.ws.Close()
	}
}

// connectRefSources connects to HA over WebSocket and discovers the companion.
func connectRefSources(ctx context.Context) (*refSources, error) {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return nil, err
	}
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return nil, fmt.Errorf("connecting to HA: %w", connErr)
	}
	companionURL, err := companion.Discover(ctx, cfg, ws)
	if err != nil {
		_ = ws.Close()
		return nil, fmt.Errorf("companion discovery: %w", err)
	}
	cc := companion.New(companionURL, cfg.CompanionToken).WithIngressAuth(ws)
	return &refSources{ws: ws, cc: cc}, nil
}

// refRow is one merged reference across sources for `ref scan`.
type refRow struct {
	source   string // "config" | "dashboard"
	location string
	path     string
}

func runRefScan(ctx context.Context, w io.Writer, target string) error {
	src, err := connectRefSources(ctx)
	if err != nil {
		return err
	}
	defer src.close()

	var rows []refRow

	// Config files (companion). A companion failure is surfaced as a warning
	// but does not hide dashboard hits — better a partial answer than none.
	if scan, scanErr := src.cc.RefScan(ctx, target); scanErr != nil {
		slog.Warn("companion config scan failed; config files were not scanned", "error", scanErr)
	} else {
		for _, h := range scan.Hits {
			rows = append(rows, refRow{"config", h.Location, h.Path})
		}
	}

	// Dashboards (WS).
	dashboards, err := src.ws.DashboardList(ctx)
	if err != nil {
		return fmt.Errorf("listing dashboards: %w", err)
	}
	for _, h := range scanDashboards(ctx, src.ws, dashboardScanTargets(dashboards), target) {
		rows = append(rows, refRow{"dashboard", h.dashboard, h.path})
	}

	if len(rows) == 0 {
		_, _ = fmt.Fprintf(w, "%s: not referenced in any config file or dashboard\n", target)
		return nil
	}

	tbl := &format.Table{
		Headers: []string{"source", "location", "path"},
		Rows:    make([][]string, len(rows)),
	}
	for i, r := range rows {
		tbl.Rows[i] = []string{r.source, r.location, r.path}
	}
	return tbl.Render(w, format.RenderOpts{Top: flagTop, Full: true, JSON: flagJSON})
}

// dashReplacePlan is one dashboard's pending rewrite for `ref replace`.
type dashReplacePlan struct {
	label    string
	urlPath  string
	result   any
	changed  []jsonwalk.Path
	writable bool
}

func runRefReplace(ctx context.Context, w io.Writer, oldVal, newVal string) error {
	src, err := connectRefSources(ctx)
	if err != nil {
		return err
	}
	defer src.close()

	confirm := flagRefConfirm

	// --- Config side (companion) ---
	// A write that silently skips config files is exactly the failure this tool
	// exists to prevent, so a companion error aborts before anything is written.
	cfgResp, err := src.cc.RefReplace(ctx, oldVal, newVal, !confirm)
	if err != nil {
		return fmt.Errorf("companion ref replace (use `hactl dash replace` for dashboard-only renames): %w", err)
	}

	// --- Dashboard side (WS) ---
	dashboards, err := src.ws.DashboardList(ctx)
	if err != nil {
		return fmt.Errorf("listing dashboards: %w", err)
	}
	plans := planDashboardReplacements(ctx, src.ws, dashboards, oldVal, newVal)

	total := len(cfgResp.Changes)
	for _, p := range plans {
		total += len(p.changed)
	}
	if total == 0 {
		_, _ = fmt.Fprintf(w, "%q not found in any config file or dashboard\n", oldVal)
		return nil
	}

	if !flagJSON {
		if confirm {
			_, _ = fmt.Fprintf(w, "renamed %q → %q (%d occurrence(s))\n", oldVal, newVal, total)
		} else {
			_, _ = fmt.Fprintf(w, "dry-run: would rename %q → %q (%d occurrence(s))\n", oldVal, newVal, total)
		}
	}

	rows := buildReplaceRows(ctx, src.ws, cfgResp.Changes, plans, confirm)

	tbl := &format.Table{
		Headers: []string{"source", "location", "path", "status"},
		Rows:    rows,
	}
	if err := tbl.Render(w, format.RenderOpts{Full: true, JSON: flagJSON}); err != nil {
		return err
	}

	if !confirm && !flagJSON {
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
	}
	return nil
}

// planDashboardReplacements computes the pending rewrite for every dashboard,
// recording which are writable (storage mode). Dashboards that cannot be fetched
// are skipped. Only dashboards with at least one match are returned.
func planDashboardReplacements(ctx context.Context, ws *haapi.WSClient, dashboards []haapi.LovelaceDashboard, oldVal, newVal string) []dashReplacePlan {
	writable := dashboardWritability(ctx, ws, dashboards)

	var plans []dashReplacePlan
	for _, t := range dashboardScanTargets(dashboards) {
		result, changed, replErr := dashReplaceOne(ctx, ws, t.urlPath, oldVal, newVal)
		if replErr != nil {
			slog.Debug("could not scan dashboard for replace", "dashboard", t.label, "error", replErr)
			continue
		}
		if len(changed) == 0 {
			continue
		}
		plans = append(plans, dashReplacePlan{
			label:    t.label,
			urlPath:  t.urlPath,
			result:   result,
			changed:  changed,
			writable: writable[t.urlPath],
		})
	}
	return plans
}

// dashboardWritability maps each dashboard url_path (plus "" for the default) to
// whether it is in storage mode and can therefore be saved back.
func dashboardWritability(ctx context.Context, ws *haapi.WSClient, dashboards []haapi.LovelaceDashboard) map[string]bool {
	writable := make(map[string]bool, len(dashboards)+1)
	if info, infoErr := ws.LovelaceInfo(ctx); infoErr == nil {
		writable[""] = info.Mode == "storage"
	} else {
		slog.Debug("could not fetch lovelace info for default dashboard mode", "error", infoErr)
	}
	for _, d := range dashboards {
		writable[d.URLPath] = d.Mode == "storage"
	}
	return writable
}

// buildReplaceRows renders the merged config+dashboard change rows, applying the
// dashboard writes when confirm is set. Config changes are already applied (or
// dry-run reported) by the companion before this is called.
func buildReplaceRows(ctx context.Context, ws *haapi.WSClient, configChanges []companion.RefChange, plans []dashReplacePlan, confirm bool) [][]string {
	configStatus := "pending"
	if confirm {
		configStatus = "applied" // the companion already wrote the files
	}
	var rows [][]string
	for _, c := range configChanges {
		rows = append(rows, []string{"config", c.Location, c.Path, configStatus})
	}
	for _, p := range plans {
		status := dashboardStatus(ctx, ws, p, confirm)
		for _, path := range p.changed {
			rows = append(rows, []string{"dashboard", p.label, path.String(), status})
		}
	}
	return rows
}

// dashboardStatus applies a single dashboard's rewrite when confirmed and
// writable, returning the per-dashboard status string for the report.
func dashboardStatus(ctx context.Context, ws *haapi.WSClient, p dashReplacePlan, confirm bool) string {
	if !p.writable {
		return "skipped: not storage-mode"
	}
	if !confirm {
		return "pending"
	}
	out, err := json.Marshal(p.result)
	if err != nil {
		return "error: " + err.Error()
	}
	if err := ws.DashboardConfigSave(ctx, p.urlPath, out); err != nil {
		return "error: " + err.Error()
	}
	return "saved"
}
