package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/writer"
	"github.com/hemm-ems/hactl/pkg/ids"
)

var flagAutoFailing bool
var flagAutoPattern string
var flagAutoLabel string
var flagAutoFile string
var flagAutoConfirm bool

var autoCmd = &cobra.Command{
	Use:   "auto",
	Short: "Manage and inspect automations",
	Long:  "List, filter, inspect, diff, and apply Home Assistant automations.",
}

var autoLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List automations",
	Long:  "Show automations table with state, run counts, and error info.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAutoLs(cmd.Context(), cmd.OutOrStdout())
	},
}

var autoShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show automation details and recent traces",
	Long:  "Display automation summary and the last 5 trace runs.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAutoShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var autoDiffCmd = &cobra.Command{
	Use:   "diff <id>",
	Short: "Show diff between local YAML and remote automation config",
	Long:  "Compare a local YAML file (-f) against the current HA automation config.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAutoDiff(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var autoApplyCmd = &cobra.Command{
	Use:   "apply <id>",
	Short: "Apply a local YAML config to HA (dry-run by default)",
	Long:  "Validate and write automation config. Use --confirm to actually write + reload.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAutoApply(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var autoCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new automation from YAML (dry-run by default)",
	Long:  "Create a new automation from a local YAML file via the companion. Use --confirm to apply.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAutoCreate(cmd.Context(), cmd.OutOrStdout())
	},
}

var autoDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete an automation (dry-run by default)",
	Long:  "Delete an automation from HA via the companion. Use --confirm to apply.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAutoDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	autoLsCmd.Flags().BoolVar(&flagAutoFailing, "failing", false, "show only automations with recent errors")
	autoLsCmd.Flags().StringVar(&flagAutoPattern, "pattern", "", "filter by name (substring or glob, e.g. ess_*)")
	autoLsCmd.Flags().StringVar(&flagAutoLabel, "label", "", "filter automations by label (substring, e.g. ess)")
	autoDiffCmd.Flags().StringVarP(&flagAutoFile, "file", "f", "", "local YAML file to diff/apply")
	autoApplyCmd.Flags().StringVarP(&flagAutoFile, "file", "f", "", "local YAML file to apply")
	autoApplyCmd.Flags().BoolVar(&flagAutoConfirm, "confirm", false, "actually write + reload (default is dry-run)")
	autoCreateCmd.Flags().StringVarP(&flagAutoFile, "file", "f", "", "local YAML file for the new automation")
	autoCreateCmd.Flags().BoolVar(&flagAutoConfirm, "confirm", false, "actually create (default is dry-run)")
	autoDeleteCmd.Flags().BoolVar(&flagAutoConfirm, "confirm", false, "actually delete (default is dry-run)")
	autoCmd.AddCommand(autoLsCmd, autoShowCmd, autoDiffCmd, autoApplyCmd, autoCreateCmd, autoDeleteCmd)
	rootCmd.AddCommand(autoCmd)
}

// automationEntity is an automation from /api/states.
type automationEntity struct {
	EntityID   string               `json:"entity_id"`
	State      string               `json:"state"`
	Attributes automationAttributes `json:"attributes"`
}

type automationAttributes struct {
	FriendlyName  string   `json:"friendly_name"`
	LastTriggered string   `json:"last_triggered"`
	ID            string   `json:"id"`
	Mode          string   `json:"mode"`
	Labels        []string `json:"labels"`
	Current       int      `json:"current"`
}

// autoRow holds combined state+trace data for one automation.
type autoRow struct {
	id      string
	state   string
	lastErr string
	area    string
	traces  []haapi.TraceSummary
	labels  []string
	runs    int
	errors  int
}

func runAutoLs(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)

	// Fetch all states and filter automations
	autos, err := fetchAutomations(ctx, client)
	if err != nil {
		return err
	}

	// Fetch traces via WebSocket
	traces, wsErr := fetchTraceList(ctx, cfg)
	if wsErr != nil {
		slog.Warn("could not fetch traces, showing basic info only", "error", wsErr)
	}

	// Fetch registry for area/label enrichment
	var rc *registryContext
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		slog.Warn("could not fetch registry context", "error", connErr)
	} else {
		defer func() { _ = ws.Close() }()
		rc, _ = fetchRegistryContext(ctx, ws)
	}

	sinceDur, err := parseSince(flagSince)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-sinceDur)

	// Fetch fire counts from logbook (traces are bounded per automation, so they
	// undercount runs_24h dramatically for high-fire rules).
	fires, fErr := fetchAutomationFireCounts(ctx, client, sinceDur)
	if fErr != nil {
		slog.Warn("could not fetch logbook fire counts; runs_24h will fall back to trace count", "error", fErr)
	}

	rows := buildAutoRows(autos, traces, fires, cutoff)

	// Enrich with area from registry
	if rc != nil {
		for i := range rows {
			rows[i].area = rc.areaName("automation." + rows[i].id)
		}
	}

	if flagAutoPattern != "" {
		rows = filterAutosByPattern(rows, flagAutoPattern)
	}

	if flagAutoLabel != "" {
		rows = filterAutosByTag(rows, flagAutoLabel)
	}

	if flagAutoFailing {
		rows = filterFailing(rows)
		if len(rows) == 0 {
			_, _ = fmt.Fprintln(w, failingEmptyHint())
			return nil
		}
	}

	tbl := &format.Table{
		Headers: []string{"id", "state", "area", "labels", "runs_24h", "errors", "last_err"},
		Rows:    make([][]string, len(rows)),
	}
	for i, r := range rows {
		tbl.Rows[i] = []string{
			r.id,
			r.state,
			r.area,
			strings.Join(r.labels, ", "),
			strconv.Itoa(r.runs),
			strconv.Itoa(r.errors),
			r.lastErr,
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:      flagTop,
		Full:     flagFull,
		JSON:     flagJSON,
		Compact:  true,
		MoreHint: "try --pattern '<glob>', --label <l>, --failing, or --top N",
	})
}

func runAutoShow(ctx context.Context, w io.Writer, autoID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)

	// Resolve the entity
	entityID := autoID
	if !strings.HasPrefix(entityID, "automation.") {
		entityID = "automation." + autoID
	}

	stateData, err := client.GetState(ctx, entityID)
	if err != nil {
		return fmt.Errorf("fetching automation state: %w", err)
	}
	var ent automationEntity
	if err := json.Unmarshal(stateData, &ent); err != nil {
		return fmt.Errorf("parsing automation state: %w", err)
	}

	// Summary line
	_, _ = fmt.Fprintf(w, "%s  state=%s  mode=%s  last_triggered=%s\n",
		ent.EntityID, ent.State,
		ent.Attributes.Mode,
		formatShortTime(ent.Attributes.LastTriggered))

	// Fetch traces
	traces, wsErr := fetchTraceList(ctx, cfg)
	if wsErr != nil {
		_, _ = fmt.Fprintf(w, "traces: unavailable (%v)\n", wsErr)
		return nil
	}

	key := entityID
	autoTraces := traces[key]

	if len(autoTraces) == 0 {
		_, _ = fmt.Fprintln(w, "traces: none")
		return nil
	}

	// Setup IDs registry
	idsPath := filepath.Join(cfg.Dir, "cache", "ids.json")
	reg := ids.NewRegistry(idsPath)
	if loadErr := reg.Load(); loadErr != nil {
		slog.Warn("could not load ids registry", "error", loadErr)
	}

	// Show last 5 traces
	limit := min(5, len(autoTraces))
	recent := autoTraces[:limit]

	_, _ = fmt.Fprintf(w, "traces (last %d):\n", limit)

	tbl := &format.Table{
		Headers: []string{"id", "time", "result", "last_step"},
		Rows:    make([][]string, len(recent)),
	}
	for i, tr := range recent {
		traceKey := tr.Domain + "." + tr.ItemID + "/" + tr.RunID
		shortID := reg.GetOrCreate("trc", traceKey)

		tbl.Rows[i] = []string{
			shortID,
			formatShortTime(tr.Timestamp.Start),
			traceResult(tr),
			tr.LastStep,
		}
	}

	if saveErr := reg.Save(); saveErr != nil {
		slog.Warn("could not save ids registry", "error", saveErr)
	}

	return tbl.Render(w, format.RenderOpts{Full: true})
}

func fetchAutomations(ctx context.Context, client *haapi.Client) ([]automationEntity, error) {
	data, err := client.GetStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching states: %w", err)
	}

	var allStates []automationEntity
	if err := json.Unmarshal(data, &allStates); err != nil {
		return nil, fmt.Errorf("parsing states: %w", err)
	}

	autos := make([]automationEntity, 0, len(allStates))
	for _, s := range allStates {
		if strings.HasPrefix(s.EntityID, "automation.") {
			autos = append(autos, s)
		}
	}
	return autos, nil
}

// fetchAutomationFireCounts pulls a window of logbook entries via REST and buckets
// them by entity_id where domain == "automation". One logbook entry per fire
// (HA records "Foo triggered by ..." for every actual run), so the count reflects
// actual fires rather than the bounded trace storage.
func fetchAutomationFireCounts(ctx context.Context, client *haapi.Client, since time.Duration) (map[string]int, error) {
	now := time.Now()
	startTime := now.Add(-since)
	data, err := client.GetLogbook(ctx,
		startTime.Format(time.RFC3339),
		now.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("fetching logbook: %w", err)
	}

	var entries []logbookEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing logbook: %w", err)
	}

	counts := make(map[string]int)
	for _, e := range entries {
		if e.Domain == "automation" && e.EntityID != "" {
			counts[e.EntityID]++
		}
	}
	return counts, nil
}

func fetchTraceList(ctx context.Context, cfg *config.Config) (haapi.TraceListResult, error) {
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		return nil, fmt.Errorf("websocket connect: %w", err)
	}
	defer func() { _ = ws.Close() }()

	result, err := ws.TraceList(ctx, "automation")
	if err != nil {
		return nil, fmt.Errorf("fetching traces: %w", err)
	}
	return result, nil
}

// buildAutoRows combines per-automation state, fire counts, and trace data.
//
// Fire counts come from the logbook (fires map): one entry per actual run within
// the cutoff window. If the logbook lookup failed (fires == nil or missing key),
// runs falls back to the count of in-window traces.
//
// Traces still drive errors/lastErr because they carry execution status, but they
// are bounded per automation by HA's stored_traces setting (default 5) and so
// must not be used to derive runs.
func buildAutoRows(autos []automationEntity, traces haapi.TraceListResult, fires map[string]int, cutoff time.Time) []autoRow {
	rows := make([]autoRow, 0, len(autos))
	for _, a := range autos {
		// Use entity_id suffix as the display ID — this is what auto show/diff/apply accept.
		id := strings.TrimPrefix(a.EntityID, "automation.")

		row := autoRow{
			id:     id,
			state:  a.State,
			labels: a.Attributes.Labels,
		}

		key := a.EntityID
		traceRunsInWindow := 0
		if ts, ok := traces[key]; ok {
			row.traces = ts
			for _, tr := range ts {
				t, err := time.Parse(time.RFC3339Nano, tr.Timestamp.Start)
				if err != nil {
					continue
				}
				if t.After(cutoff) {
					traceRunsInWindow++
					if isTraceError(tr) {
						row.errors++
						if row.lastErr == "" {
							row.lastErr = formatShortTime(tr.Timestamp.Start) + " " + shortenStep(tr.LastStep)
						}
					}
				}
			}
		}

		if n, ok := fires[key]; ok {
			row.runs = n
		} else {
			row.runs = traceRunsInWindow
		}

		rows = append(rows, row)
	}
	return rows
}

func filterAutosByPattern(rows []autoRow, pattern string) []autoRow {
	result := make([]autoRow, 0, len(rows))
	for _, r := range rows {
		if matchPattern(r.id, pattern) || matchPattern("automation."+r.id, pattern) {
			result = append(result, r)
		}
	}
	return result
}

func filterAutosByTag(rows []autoRow, tag string) []autoRow {
	result := make([]autoRow, 0, len(rows))
	for _, r := range rows {
		for _, l := range r.labels {
			if strings.EqualFold(l, tag) || strings.Contains(strings.ToLower(l), strings.ToLower(tag)) {
				result = append(result, r)
				break
			}
		}
	}
	return result
}

// failingEmptyHint returns the hint printed when --failing yields no rows.
func failingEmptyHint() string {
	return "# no failing automations in recent traces\n" +
		"# (try: hactl log --errors --unique to check the error log)"
}

func filterFailing(rows []autoRow) []autoRow {
	result := make([]autoRow, 0, len(rows))
	for _, r := range rows {
		if r.errors > 0 {
			result = append(result, r)
		}
	}
	return result
}

func isTraceError(tr haapi.TraceSummary) bool {
	return tr.Execution == "error" || tr.Error != ""
}

func traceResult(tr haapi.TraceSummary) string {
	if isTraceError(tr) {
		return "error"
	}
	if tr.Execution == "" {
		return tr.State
	}
	return tr.Execution
}

func shortenStep(step string) string {
	if step == "" {
		return ""
	}
	parts := strings.Split(step, "/")
	if len(parts) >= 2 {
		return parts[0]
	}
	return step
}

func formatShortTime(isoTime string) string {
	if isoTime == "" {
		return "-"
	}
	t, err := time.Parse(time.RFC3339Nano, isoTime)
	if err != nil {
		// Try without nanoseconds
		t, err = time.Parse(time.RFC3339, isoTime)
		if err != nil {
			return isoTime
		}
	}
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("01-02 15:04")
}

// parseSince converts a duration string like "24h" or "7d" to time.Duration.
func parseSince(s string) (time.Duration, error) {
	if after, found := strings.CutSuffix(s, "d"); found {
		days, err := strconv.Atoi(after)
		if err != nil {
			return 0, fmt.Errorf("invalid duration: %s", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}
	return d, nil
}

func runAutoDiff(ctx context.Context, w io.Writer, autoID string) error {
	if flagAutoFile == "" {
		return errors.New("--file / -f is required for diff")
	}

	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	backupDir := filepath.Join(cfg.Dir, "backups")
	wr := writer.New(client, nil, backupDir)

	diff, err := wr.Diff(ctx, autoID, flagAutoFile)
	if err != nil {
		return err
	}

	if !diff.HasChanges {
		_, _ = fmt.Fprintf(w, "%s: no changes\n", autoID)
		return nil
	}

	_, _ = fmt.Fprintf(w, "%s: diff\n", autoID)
	for _, line := range diff.Lines {
		_, _ = fmt.Fprintln(w, line)
	}
	return nil
}

func runAutoApply(ctx context.Context, w io.Writer, autoID string) error {
	if flagAutoFile == "" {
		return errors.New("--file / -f is required for apply")
	}

	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	backupDir := filepath.Join(cfg.Dir, "backups")

	// Connect WebSocket for validation
	var wsClient *haapi.WSClient
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connectErr := ws.Connect(ctx); connectErr != nil {
		slog.Warn("could not connect WebSocket for validation", "error", connectErr)
	} else {
		wsClient = ws
		defer func() { _ = ws.Close() }()
	}

	wr := writer.New(client, wsClient, backupDir)

	// Show diff first
	diff, diffErr := wr.Diff(ctx, autoID, flagAutoFile)
	switch {
	case diffErr != nil:
		slog.Warn("could not generate diff", "error", diffErr)
	case diff.HasChanges:
		_, _ = fmt.Fprintf(w, "diff:\n")
		for _, line := range diff.Lines {
			_, _ = fmt.Fprintln(w, line)
		}
	default:
		_, _ = fmt.Fprintf(w, "no changes detected\n")
		return nil
	}

	result, err := wr.Apply(ctx, autoID, flagAutoFile, flagAutoConfirm)
	if err != nil {
		return err
	}

	if result.DryRun {
		_, _ = fmt.Fprintf(w, "\ndry-run: no changes written (use --confirm to apply)\n")
		if result.BackupPath != "" {
			_, _ = fmt.Fprintf(w, "backup: %s\n", result.BackupPath)
		}
		return nil
	}

	_, _ = fmt.Fprintf(w, "\napplied: %s\n", autoID)
	if result.BackupPath != "" {
		_, _ = fmt.Fprintf(w, "backup:  %s\n", result.BackupPath)
	}
	if result.Reloaded {
		_, _ = fmt.Fprintf(w, "reload:  ok\n")
	}
	return nil
}

func runAutoCreate(ctx context.Context, w io.Writer) error {
	if flagAutoFile == "" {
		return errors.New("--file / -f is required for create")
	}

	data, err := os.ReadFile(flagAutoFile) //nolint:gosec // file path provided by user via CLI flag
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}
	content := string(data)

	if !flagAutoConfirm {
		if _, connErr := connectCompanion(ctx); connErr != nil {
			return connErr
		}
		_, _ = fmt.Fprintln(w, "dry-run: would create automation")
		_, _ = fmt.Fprintf(w, "  file: %s\n", flagAutoFile)
		_, _ = fmt.Fprintf(w, "  size: %d bytes\n", len(data))
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	resp, err := cc.CreateAutomationDef(ctx, content)
	if err != nil {
		return fmt.Errorf("creating automation: %w", err)
	}

	_, _ = fmt.Fprintf(w, "created automation %q\n", resp.ID)
	return nil
}

func runAutoDelete(ctx context.Context, w io.Writer, autoID string) error {
	if !flagAutoConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would delete automation")
		_, _ = fmt.Fprintf(w, "  id: %s\n", autoID)
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	if _, err := cc.DeleteAutomationDef(ctx, autoID); err != nil {
		return fmt.Errorf("deleting automation: %w", err)
	}

	_, _ = fmt.Fprintf(w, "deleted automation %q\n", autoID)
	return nil
}

// connectCompanion discovers and connects to the hactl-companion.
func connectCompanion(ctx context.Context) (*companion.Client, error) {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return nil, err
	}

	var wsClient *haapi.WSClient
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connectErr := ws.Connect(ctx); connectErr != nil {
		slog.Debug("could not connect WebSocket for companion discovery", "error", connectErr)
	} else {
		wsClient = ws
		// Intentionally leak the WS connection: the returned client uses it
		// for IngressSession on every Companion call. The OS closes it when
		// the CLI process exits.
	}

	companionURL, err := companion.Discover(ctx, cfg, wsClient)
	if err != nil {
		return nil, fmt.Errorf("companion discovery: %w", err)
	}

	cc := companion.New(companionURL, cfg.CompanionToken)
	if wsClient != nil {
		cc = cc.WithIngressAuth(wsClient)
	}
	return cc, nil
}
