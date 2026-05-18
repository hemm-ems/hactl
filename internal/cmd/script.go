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

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/pkg/ids"
)

var flagScriptPattern string
var flagScriptLabel string
var flagScriptFailing bool
var flagScriptFile string
var flagScriptConfirm bool

var scriptCmd = &cobra.Command{
	Use:   "script",
	Short: "Inspect HA scripts",
	Long:  "List and inspect Home Assistant scripts and their traces.",
}

var scriptLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List scripts",
	Long:  "Show scripts table with state, run counts, and error info.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runScriptLs(cmd.Context(), cmd.OutOrStdout())
	},
}

var scriptShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show script details and recent traces",
	Long:  "Display script summary and the last 5 trace runs.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runScriptShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var scriptRunCmd = &cobra.Command{
	Use:   "run <id>",
	Short: "Execute a script",
	Long:  "Run a Home Assistant script via service call script.turn_on.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runScriptRun(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var scriptCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a new script from YAML (dry-run by default)",
	Long:  "Create a new script from a local YAML file via the companion. Use --confirm to apply.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runScriptCreate(cmd.Context(), cmd.OutOrStdout())
	},
}

var scriptDeleteCmd = &cobra.Command{
	Use:   "delete <id>",
	Short: "Delete a script (dry-run by default)",
	Long:  "Delete a script from HA via the companion. Use --confirm to apply.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runScriptDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	scriptLsCmd.Flags().StringVar(&flagScriptPattern, "pattern", "", "filter by name (substring or glob, e.g. kino)")
	scriptLsCmd.Flags().StringVar(&flagScriptLabel, "label", "", "filter scripts by label (substring, e.g. ess)")
	scriptLsCmd.Flags().BoolVar(&flagScriptFailing, "failing", false, "show only scripts with recent errors")
	scriptCreateCmd.Flags().StringVarP(&flagScriptFile, "file", "f", "", "local YAML file for the new script")
	scriptCreateCmd.Flags().BoolVar(&flagScriptConfirm, "confirm", false, "actually create (default is dry-run)")
	scriptDeleteCmd.Flags().BoolVar(&flagScriptConfirm, "confirm", false, "actually delete (default is dry-run)")
	scriptCmd.AddCommand(scriptLsCmd, scriptShowCmd, scriptRunCmd, scriptCreateCmd, scriptDeleteCmd)
	rootCmd.AddCommand(scriptCmd)
}

// scriptEntity is a script from /api/states.
type scriptEntity struct {
	EntityID   string           `json:"entity_id"`
	State      string           `json:"state"`
	Attributes scriptAttributes `json:"attributes"`
}

type scriptAttributes struct {
	FriendlyName  string `json:"friendly_name"`
	LastTriggered string `json:"last_triggered"`
	Mode          string `json:"mode"`
	Current       int    `json:"current"`
}

// scriptRow holds combined state+trace data for one script.
type scriptRow struct {
	id      string
	state   string
	lastErr string
	area    string
	labels  string
	runs    int
	errors  int
}

func runScriptLs(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	scripts, err := fetchScripts(ctx, client)
	if err != nil {
		return err
	}

	// Fetch traces via WebSocket
	traces, wsErr := fetchScriptTraceList(ctx, cfg)
	if wsErr != nil {
		slog.Warn("could not fetch script traces, showing basic info only", "error", wsErr)
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

	rows := buildScriptRows(scripts, traces, cutoff)

	// Enrich with area/labels from registry
	if rc != nil {
		for i := range rows {
			entityID := "script." + rows[i].id
			rows[i].area = rc.areaName(entityID)
			rows[i].labels = rc.labelNames(entityID)
		}
	}

	if flagScriptPattern != "" {
		rows = filterScriptsByPattern(rows, flagScriptPattern)
	}

	if flagScriptLabel != "" {
		rows = filterScriptsByLabel(rows, flagScriptLabel)
	}

	if flagScriptFailing {
		rows = filterScriptsFailing(rows)
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
			r.labels,
			strconv.Itoa(r.runs),
			strconv.Itoa(r.errors),
			r.lastErr,
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func runScriptShow(ctx context.Context, w io.Writer, scriptID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)

	entityID := scriptID
	if !strings.HasPrefix(entityID, "script.") {
		entityID = "script." + scriptID
	}

	stateData, err := client.GetState(ctx, entityID)
	if err != nil {
		return fmt.Errorf("fetching script state: %w", err)
	}
	var ent scriptEntity
	if err := json.Unmarshal(stateData, &ent); err != nil {
		return fmt.Errorf("parsing script state: %w", err)
	}

	_, _ = fmt.Fprintf(w, "%s  state=%s  mode=%s  last_triggered=%s\n",
		ent.EntityID, ent.State,
		ent.Attributes.Mode,
		formatShortTime(ent.Attributes.LastTriggered))

	// Fetch traces
	traces, wsErr := fetchScriptTraceList(ctx, cfg)
	if wsErr != nil {
		_, _ = fmt.Fprintf(w, "traces: unavailable (%v)\n", wsErr)
		return nil
	}

	scriptTraces := traces[entityID]
	if len(scriptTraces) == 0 {
		_, _ = fmt.Fprintln(w, "traces: none")
		return nil
	}

	idsPath := filepath.Join(cfg.Dir, "cache", "ids.json")
	reg := ids.NewRegistry(idsPath)
	if loadErr := reg.Load(); loadErr != nil {
		slog.Warn("could not load ids registry", "error", loadErr)
	}

	limit := min(5, len(scriptTraces))
	recent := scriptTraces[:limit]

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

func fetchScripts(ctx context.Context, client *haapi.Client) ([]scriptEntity, error) {
	data, err := client.GetStates(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching states: %w", err)
	}

	var allStates []scriptEntity
	if err := json.Unmarshal(data, &allStates); err != nil {
		return nil, fmt.Errorf("parsing states: %w", err)
	}

	scripts := make([]scriptEntity, 0, len(allStates))
	for _, s := range allStates {
		if strings.HasPrefix(s.EntityID, "script.") {
			scripts = append(scripts, s)
		}
	}
	return scripts, nil
}

func fetchScriptTraceList(ctx context.Context, cfg *config.Config) (haapi.TraceListResult, error) {
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		return nil, fmt.Errorf("websocket connect: %w", err)
	}
	defer func() { _ = ws.Close() }()

	result, err := ws.TraceList(ctx, "script")
	if err != nil {
		return nil, fmt.Errorf("fetching script traces: %w", err)
	}
	return result, nil
}

func buildScriptRows(scripts []scriptEntity, traces haapi.TraceListResult, cutoff time.Time) []scriptRow {
	rows := make([]scriptRow, 0, len(scripts))
	for _, s := range scripts {
		id := strings.TrimPrefix(s.EntityID, "script.")

		row := scriptRow{
			id:    id,
			state: s.State,
		}

		key := s.EntityID
		if ts, ok := traces[key]; ok {
			for _, tr := range ts {
				t, err := time.Parse(time.RFC3339Nano, tr.Timestamp.Start)
				if err != nil {
					continue
				}
				if t.After(cutoff) {
					row.runs++
					if isTraceError(tr) {
						row.errors++
						if row.lastErr == "" {
							row.lastErr = formatShortTime(tr.Timestamp.Start) + " " + shortenStep(tr.LastStep)
						}
					}
				}
			}
		}

		rows = append(rows, row)
	}
	return rows
}

func filterScriptsByPattern(rows []scriptRow, pattern string) []scriptRow {
	result := make([]scriptRow, 0, len(rows))
	for _, r := range rows {
		if matchPattern(r.id, pattern) || matchPattern("script."+r.id, pattern) {
			result = append(result, r)
		}
	}
	return result
}

func filterScriptsByLabel(rows []scriptRow, label string) []scriptRow {
	result := make([]scriptRow, 0, len(rows))
	for _, r := range rows {
		if strings.Contains(strings.ToLower(r.labels), strings.ToLower(label)) {
			result = append(result, r)
		}
	}
	return result
}

func filterScriptsFailing(rows []scriptRow) []scriptRow {
	result := make([]scriptRow, 0, len(rows))
	for _, r := range rows {
		if r.errors > 0 {
			result = append(result, r)
		}
	}
	return result
}

func runScriptRun(ctx context.Context, w io.Writer, scriptID string) error {
	entityID := scriptID
	if !strings.HasPrefix(entityID, "script.") {
		entityID = "script." + scriptID
	}

	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)

	// Verify the script entity exists before calling the service.
	if _, err := client.GetState(ctx, entityID); err != nil {
		return fmt.Errorf("script not found: %s", entityID)
	}

	if err := client.CallService(ctx, "script", "turn_on", map[string]any{
		"entity_id": entityID,
	}); err != nil {
		return fmt.Errorf("running script %s: %w", entityID, err)
	}

	_, _ = fmt.Fprintf(w, "executed %s\n", entityID)
	return nil
}

func runScriptCreate(ctx context.Context, w io.Writer) error {
	if flagScriptFile == "" {
		return errors.New("--file / -f is required for create")
	}

	data, err := os.ReadFile(flagScriptFile) //nolint:gosec // file path provided by user via CLI flag
	if err != nil {
		return fmt.Errorf("reading file: %w", err)
	}
	content := string(data)

	if !flagScriptConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would create script")
		_, _ = fmt.Fprintf(w, "  file: %s\n", flagScriptFile)
		_, _ = fmt.Fprintf(w, "  size: %d bytes\n", len(data))
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	resp, err := cc.CreateScriptDef(ctx, content)
	if err != nil {
		return fmt.Errorf("creating script: %w", err)
	}

	_, _ = fmt.Fprintf(w, "created script %q\n", resp.ID)
	return nil
}

func runScriptDelete(ctx context.Context, w io.Writer, scriptID string) error {
	if !flagScriptConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would delete script")
		_, _ = fmt.Fprintf(w, "  id: %s\n", scriptID)
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}

	if _, err := cc.DeleteScriptDef(ctx, scriptID); err != nil {
		return fmt.Errorf("deleting script: %w", err)
	}

	_, _ = fmt.Fprintf(w, "deleted script %q\n", scriptID)
	return nil
}
