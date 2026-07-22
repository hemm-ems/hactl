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
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

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
var flagAutoRestored bool

var autoCmd = &cobra.Command{
	Use:        "auto",
	SuggestFor: []string{"automation", "automations"},
	Short:      "Manage and inspect automations",
	Long:       "List, filter, inspect, diff, and apply Home Assistant automations.",
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

var autoCatCmd = &cobra.Command{
	Use:   "cat <id>",
	Short: "Print an automation's remote config as YAML",
	Long:  "Fetch and print the current remote YAML definition of an automation (via the companion).",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAutoCat(cmd.Context(), cmd.OutOrStdout(), args[0])
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
	autoLsCmd.Flags().BoolVar(&flagAutoRestored, "restored", false, "show only restored 'ghost' automations (registry entry with no live config — deleted or re-authored under a new id)")
	autoDiffCmd.Flags().StringVarP(&flagAutoFile, "file", "f", "", "local YAML file to diff/apply")
	autoApplyCmd.Flags().StringVarP(&flagAutoFile, "file", "f", "", "local YAML file to apply")
	autoApplyCmd.Flags().BoolVar(&flagAutoConfirm, "confirm", false, "actually write + reload (default is dry-run)")
	autoCreateCmd.Flags().StringVarP(&flagAutoFile, "file", "f", "", "local YAML file for the new automation")
	autoCreateCmd.Flags().BoolVar(&flagAutoConfirm, "confirm", false, "actually create (default is dry-run)")
	autoDeleteCmd.Flags().BoolVar(&flagAutoConfirm, "confirm", false, "actually delete (default is dry-run)")
	autoCmd.AddCommand(autoLsCmd, autoShowCmd, autoCatCmd, autoDiffCmd, autoApplyCmd, autoCreateCmd, autoDeleteCmd)
	rootCmd.AddCommand(autoCmd)
}

// runAutoCat prints an automation's remote YAML config verbatim (pipe-friendly,
// round-trippable with `auto diff -f`). The companion returns the definition as
// YAML text in resp.Content.
//
// The companion's /v1/config/automation route keys on the config id, but
// `auto ls` displays entity object ids and a caller may just as easily be
// holding a full entity_id or the alias (#70). Resolve any of those forms to
// the config id via /api/states before asking the companion; a reference
// that matches no live automation is passed through as-is so a genuinely
// unknown id still 404s with the companion's own message.
func runAutoCat(ctx context.Context, w io.Writer, automationID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	configID := automationID
	restClient := haapi.New(cfg.URL, cfg.Token)
	if a, ok := resolveAutomation(ctx, restClient, automationID); ok && a.Attributes.ID != "" {
		configID = a.Attributes.ID
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	resp, err := cc.GetAutomationDef(ctx, configID)
	if err != nil {
		return fmt.Errorf("fetching automation: %w", err)
	}
	_, _ = fmt.Fprint(w, resp.Content)
	return nil
}

// automationEntity is an automation from /api/states.
type automationEntity struct {
	EntityID   string               `json:"entity_id"`
	State      string               `json:"state"`
	Attributes automationAttributes `json:"attributes"`
}

// automationAttributes mirrors the attribute keys HA actually emits for an
// automation state (current, friendly_name, id, last_triggered, mode — plus
// restored on ghosts). Registry labels are NOT among them; they live in the
// entity registry and are resolved via registryContext.labelNames.
type automationAttributes struct {
	FriendlyName  string `json:"friendly_name"`
	LastTriggered string `json:"last_triggered"`
	ID            string `json:"id"`
	Mode          string `json:"mode"`
	Current       int    `json:"current"`
	Restored      bool   `json:"restored"`
}

// autoRow holds combined state+trace data for one automation.
type autoRow struct {
	id       string
	state    string
	lastErr  string
	area     string
	traces   []haapi.TraceSummary
	labels   string
	runs     int
	errors   int
	restored bool
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

	// Enrich with area/labels from registry. Labels only exist in the entity
	// registry — /api/states never carries them — so --label depends on this.
	if rc != nil {
		for i := range rows {
			entityID := "automation." + rows[i].id
			rows[i].area = rc.areaName(entityID)
			rows[i].labels = rc.labelNames(entityID)
		}
	}

	if flagAutoPattern != "" {
		rows = filterAutosByPattern(rows, flagAutoPattern)
	}

	if flagAutoLabel != "" {
		// Without the registry there are no labels to match, so the filter
		// silently removes everything — say why instead of printing an empty
		// table that looks like "no automation carries this label".
		if rc == nil {
			slog.Warn("entity registry unavailable; --label cannot match any automation", "label", flagAutoLabel)
		}
		rows = filterAutosByTag(rows, flagAutoLabel)
	}

	if flagAutoFailing {
		rows = filterFailing(rows)
		if len(rows) == 0 {
			return emitEmptyList(w, failingEmptyHint())
		}
	}

	if flagAutoRestored {
		rows = filterAutosRestored(rows)
	}

	// #54: HA marks an automation `restored: true` when its state was resurrected
	// from the registry with no live config behind it — a "ghost" from a deleted
	// or re-authored automation. Show the column only when at least one row is a
	// ghost, so the common all-live listing keeps its narrower shape.
	anyRestored := false
	for i := range rows {
		if rows[i].restored {
			anyRestored = true
			break
		}
	}

	headers := []string{"id", "state", "area", "labels", "runs_24h", "errors", "last_err"}
	if anyRestored {
		headers = append(headers, "restored")
	}
	tbl := &format.Table{
		Headers: headers,
		Rows:    make([][]string, len(rows)),
	}
	for i, r := range rows {
		row := []string{
			r.id,
			r.state,
			r.area,
			r.labels,
			strconv.Itoa(r.runs),
			strconv.Itoa(r.errors),
			r.lastErr,
		}
		if anyRestored {
			row = append(row, boolCell(r.restored))
		}
		tbl.Rows[i] = row
	}

	return tbl.Render(w, format.RenderOpts{
		Top:      flagTop,
		Full:     flagFull,
		JSON:     flagJSON,
		Compact:  true,
		MoreHint: "try --pattern '<glob>', --label <l>, --failing, --restored, or --top N",
	})
}

func runAutoShow(ctx context.Context, w io.Writer, autoID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)

	// Resolve the entity. `auto cat`/`diff`/`apply` all key on the config id,
	// but HA derives entity_id from the alias — so a caller working from
	// those commands' output may only have the config id, not the entity_id
	// `show` used to require (#70). Try every interchangeable form first;
	// fall back to the old bare-prefix guess so a genuinely unknown
	// reference still 404s usefully instead of silently swallowing the typo.
	entityID := autoID
	if resolved, ok := resolveAutomationEntityID(ctx, client, autoID); ok {
		entityID = resolved
	} else if !strings.HasPrefix(entityID, "automation.") {
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
	if ent.Attributes.Restored {
		// #54: ghost entry — registry state with no live config; nothing to repair.
		_, _ = fmt.Fprintln(w, "restored=true (ghost: no live config — deleted or re-authored under a new id; nothing to repair)")
	}

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
			id:       id,
			state:    a.State,
			restored: a.Attributes.Restored,
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
	// HA's /api/states response order is not guaranteed to be stable, so sort by
	// id for deterministic output (keeps `auto ls` listings and golden tests
	// reproducible across runs).
	sort.Slice(rows, func(i, j int) bool { return rows[i].id < rows[j].id })
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

// filterAutosByTag keeps rows whose registry label names contain tag
// (case-insensitive substring), mirroring filterScriptsByLabel.
func filterAutosByTag(rows []autoRow, tag string) []autoRow {
	result := make([]autoRow, 0, len(rows))
	for _, r := range rows {
		if r.labels == "" {
			continue
		}
		if strings.Contains(strings.ToLower(r.labels), strings.ToLower(tag)) {
			result = append(result, r)
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

func filterAutosRestored(rows []autoRow) []autoRow {
	result := make([]autoRow, 0, len(rows))
	for _, r := range rows {
		if r.restored {
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

// compactDiffContext is how many unchanged context lines the compact diff
// renderer keeps on each side of a changed hunk. Unchanged runs longer than
// this collapse to a single "… N unchanged lines …" marker so the real +/-
// changes stay visible under the default output token cap — a full-document
// echo pushes changes past the truncation point (Go's YAML marshal sorts map
// keys alphabetically, so a changed trigger lands at the end of the document).
const compactDiffContext = 3

// compactDiff renders full unified-diff lines (each prefixed with ' ', '+' or
// '-') as compact hunks: every changed line is kept, flanked by up to
// compactDiffContext unchanged context lines, and each remaining run of
// unchanged lines collapses to a single "… N unchanged lines …" marker. Hunks
// whose context windows touch or overlap merge into one. A diff with no
// changes is returned unchanged.
func compactDiff(lines []string) []string {
	keep := make([]bool, len(lines))
	changed := false
	for i, l := range lines {
		if len(l) > 0 && (l[0] == '+' || l[0] == '-') {
			changed = true
			lo := max(i-compactDiffContext, 0)
			hi := min(i+compactDiffContext, len(lines)-1)
			for j := lo; j <= hi; j++ {
				keep[j] = true
			}
		}
	}
	if !changed {
		return lines
	}

	var result []string
	for i := 0; i < len(lines); {
		if keep[i] {
			result = append(result, lines[i])
			i++
			continue
		}
		start := i
		for i < len(lines) && !keep[i] {
			i++
		}
		result = append(result, fmt.Sprintf("… %d unchanged lines …", i-start))
	}
	return result
}

// renderAutoDiff writes an automation diff's lines to w in compact hunk form so
// the +/- changes stay visible under the output token cap. Both `auto diff` and
// the `auto apply` dry-run preview render through here.
func renderAutoDiff(w io.Writer, lines []string) {
	for _, line := range compactDiff(lines) {
		_, _ = fmt.Fprintln(w, line)
	}
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
	renderAutoDiff(w, diff.Lines)
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
		renderAutoDiff(w, diff.Lines)
	default:
		_, _ = fmt.Fprintf(w, "no changes detected\n")
		return nil
	}

	result, err := wr.Apply(ctx, autoID, flagAutoFile, flagAutoConfirm)
	if err != nil {
		return err
	}

	if result.Validated {
		_, _ = fmt.Fprintf(w, "\nvalidation: ok (HA validate_config)\n")
	} else {
		_, _ = fmt.Fprintf(w, "\nvalidation: skipped (validate_config unavailable; HA still validates on write)\n")
	}

	if result.DryRun {
		_, _ = fmt.Fprintf(w, "dry-run: no changes written to %s (use --confirm to apply)\n", autoID)
		return nil
	}

	_, _ = fmt.Fprintf(w, "applied: %s\n", autoID)
	if result.BackupPath != "" {
		_, _ = fmt.Fprintf(w, "backup:  %s\n", result.BackupPath)
	}
	if result.Reloaded {
		_, _ = fmt.Fprintf(w, "reload:  ok\n")
	}
	return nil
}

// validateAutoCreateCandidate runs HA's validate_config against a single-automation
// YAML mapping and prints the same `validation:` status line `auto apply` prints.
// It returns an error (refusing the create) when HA rejects a section. A YAML
// parse failure is a hard error, mirroring `auto apply` (writer.Apply returns
// "parsing local YAML: %w" on the same failure) — a config the parser chokes on
// must never be reported as "checked". Inputs that parse cleanly but are not a
// top-level mapping (e.g. a YAML list) are left for the companion to handle,
// matching the pre-validation behavior.
func validateAutoCreateCandidate(ctx context.Context, w io.Writer, data []byte) error {
	var parsed any
	if err := yaml.Unmarshal(data, &parsed); err != nil {
		return fmt.Errorf("parsing local YAML: %w", err)
	}
	candidate, ok := parsed.(map[string]any)
	if !ok {
		return nil
	}

	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	var wsClient *haapi.WSClient
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connectErr := ws.Connect(ctx); connectErr != nil {
		slog.Warn("could not connect WebSocket for validation", "error", connectErr)
	} else {
		wsClient = ws
		defer func() { _ = ws.Close() }()
	}

	validated, err := writer.New(client, wsClient, "").ValidateCandidate(ctx, candidate)
	if err != nil {
		return err
	}
	if validated {
		_, _ = fmt.Fprintf(w, "\nvalidation: ok (HA validate_config)\n")
	} else {
		_, _ = fmt.Fprintf(w, "\nvalidation: skipped (validate_config unavailable; HA still validates on write)\n")
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

	// Validate the candidate against HA's schema before touching the companion —
	// the same validate_config check `auto apply` runs. `auto create` used to skip
	// it, letting a broken config reach the companion and load as `unavailable`. A
	// rejected config is now refused (nothing written) in both dry-run and
	// --confirm mode.
	if valErr := validateAutoCreateCandidate(ctx, w, data); valErr != nil {
		return valErr
	}

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
	switch {
	case !resp.Reloaded:
		_, _ = fmt.Fprintln(w, "warning: automation written but HA did not confirm reload")
	case resp.EntityID == "":
		_, _ = fmt.Fprintln(w, "warning: automation reloaded but its live entity_id could not be confirmed")
	default:
		_, _ = fmt.Fprintf(w, "entity_id: %s\n", resp.EntityID)
		if resp.EntityID != resp.ID {
			_, _ = fmt.Fprintf(w, "note: live entity_id (%s) differs from config id (%s) — HA derives entity_id from alias, not id\n", resp.EntityID, resp.ID)
		}
	}
	return nil
}

// resolveAutomation resolves a config id, entity object id, full entity_id,
// or alias reference to its live automationEntity via /api/states. HA derives
// entity_id from alias (not the config id), so a caller working from
// `hactl auto` output may only have the display identifier — which could be
// the alias itself (HA exposes it as attributes.friendly_name verbatim), the
// config id (`cat`/`diff`/`apply`), or the entity object id (`ls`). Returns
// (automationEntity{}, false) if no live automation matches or the states
// fetch fails.
func resolveAutomation(ctx context.Context, client *haapi.Client, ref string) (automationEntity, bool) {
	autos, err := fetchAutomations(ctx, client)
	if err != nil {
		return automationEntity{}, false
	}
	for _, a := range autos {
		if a.EntityID == ref ||
			a.Attributes.ID == ref ||
			a.Attributes.FriendlyName == ref ||
			strings.TrimPrefix(a.EntityID, "automation.") == ref {
			return a, true
		}
	}
	return automationEntity{}, false
}

// resolveAutomationEntityID is resolveAutomation narrowed to the live
// entity_id, for callers (delete, show) that only need the entity address.
func resolveAutomationEntityID(ctx context.Context, client *haapi.Client, ref string) (string, bool) {
	a, ok := resolveAutomation(ctx, client, ref)
	if !ok {
		return "", false
	}
	return a.EntityID, true
}

func runAutoDelete(ctx context.Context, w io.Writer, autoID string) error {
	if !flagAutoConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would delete automation")
		_, _ = fmt.Fprintf(w, "  id: %s\n", autoID)
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	restClient := haapi.New(cfg.URL, cfg.Token)
	liveEntityID, hadLiveEntity := resolveAutomationEntityID(ctx, restClient, autoID)

	if _, err := cc.DeleteAutomationDef(ctx, autoID); err != nil {
		return fmt.Errorf("deleting automation: %w", err)
	}

	_, _ = fmt.Fprintf(w, "deleted automation %q\n", autoID)

	if hadLiveEntity {
		ws := haapi.NewWSClient(cfg.URL, cfg.Token)
		if connErr := ws.Connect(ctx); connErr != nil {
			slog.Warn("could not connect to HA to clean up entity registry", "entity_id", liveEntityID, "error", connErr)
		} else {
			defer func() { _ = ws.Close() }()
			if rmErr := ws.EntityRegistryRemove(ctx, liveEntityID); rmErr != nil {
				slog.Warn("could not remove orphaned entity registry entry", "entity_id", liveEntityID, "error", rmErr)
			}
		}
	}

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
