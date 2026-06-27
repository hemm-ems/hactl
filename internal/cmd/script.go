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
	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/writer"
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
	Long:  "List, inspect, diff, apply, and run Home Assistant scripts.",
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

var scriptDiffCmd = &cobra.Command{
	Use:   "diff <id>",
	Short: "Show diff between local YAML and remote script config",
	Long:  "Compare a local YAML file (-f) against the current HA script config.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runScriptDiff(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var scriptApplyCmd = &cobra.Command{
	Use:   "apply <id>",
	Short: "Apply a local YAML config to a script (dry-run by default)",
	Long:  "Validate and write script config through the companion. Use --confirm to actually write + reload.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runScriptApply(cmd.Context(), cmd.OutOrStdout(), args[0])
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
	scriptDiffCmd.Flags().StringVarP(&flagScriptFile, "file", "f", "", "local YAML file to diff/apply")
	scriptApplyCmd.Flags().StringVarP(&flagScriptFile, "file", "f", "", "local YAML file to apply")
	scriptApplyCmd.Flags().BoolVar(&flagScriptConfirm, "confirm", false, "actually write + reload (default is dry-run)")
	scriptCreateCmd.Flags().StringVarP(&flagScriptFile, "file", "f", "", "local YAML file for the new script")
	scriptCreateCmd.Flags().BoolVar(&flagScriptConfirm, "confirm", false, "actually create (default is dry-run)")
	scriptDeleteCmd.Flags().BoolVar(&flagScriptConfirm, "confirm", false, "actually delete (default is dry-run)")
	scriptCmd.AddCommand(scriptLsCmd, scriptShowCmd, scriptRunCmd, scriptDiffCmd, scriptApplyCmd, scriptCreateCmd, scriptDeleteCmd)
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

type scriptConfigCandidate struct {
	ID      string
	Config  map[string]any
	Content string
}

func runScriptDiff(ctx context.Context, w io.Writer, scriptID string) error {
	if flagScriptFile == "" {
		return errors.New("--file / -f is required for diff")
	}

	candidate, err := loadScriptCandidate(flagScriptFile, scriptID)
	if err != nil {
		return err
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	remote, err := cc.GetScriptDef(ctx, candidate.ID)
	if err != nil {
		return fmt.Errorf("fetching remote script: %w", err)
	}
	remoteCandidate, err := normalizeScriptYAML([]byte(remote.Content), candidate.ID)
	if err != nil {
		return fmt.Errorf("normalizing remote script: %w", err)
	}

	diff := scriptConfigDiff(remoteCandidate.Content, candidate.Content)
	if !scriptDiffHasChanges(diff) {
		_, _ = fmt.Fprintf(w, "%s: no changes\n", candidate.ID)
		return nil
	}

	_, _ = fmt.Fprintf(w, "%s: diff\n", candidate.ID)
	for _, line := range diff {
		_, _ = fmt.Fprintln(w, line)
	}
	return nil
}

func runScriptApply(ctx context.Context, w io.Writer, scriptID string) error {
	if flagScriptFile == "" {
		return errors.New("--file / -f is required for apply")
	}

	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	candidate, err := loadScriptCandidate(flagScriptFile, scriptID)
	if err != nil {
		return err
	}

	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	remote, err := cc.GetScriptDef(ctx, candidate.ID)
	if err != nil {
		return fmt.Errorf("fetching remote script: %w", err)
	}
	remoteCandidate, err := normalizeScriptYAML([]byte(remote.Content), candidate.ID)
	if err != nil {
		return fmt.Errorf("normalizing remote script: %w", err)
	}

	diff := scriptConfigDiff(remoteCandidate.Content, candidate.Content)
	if scriptDiffHasChanges(diff) {
		_, _ = fmt.Fprintln(w, "diff:")
		for _, line := range diff {
			_, _ = fmt.Fprintln(w, line)
		}
	} else {
		_, _ = fmt.Fprintln(w, "no changes detected")
		return nil
	}

	client := haapi.New(cfg.URL, cfg.Token)
	validated, err := validateScriptCandidate(ctx, cfg, candidate.Config)
	if err != nil {
		return err
	}
	switch {
	case validated:
		_, _ = fmt.Fprintln(w, "\nvalidation: ok (HA validate_config)")
	case !flagScriptConfirm:
		_, _ = fmt.Fprintln(w, "\nvalidation: skipped (validate_config unavailable)")
	default:
		return errors.New("script validation unavailable; refusing confirmed apply")
	}

	if !flagScriptConfirm {
		if _, dryErr := cc.WriteScriptDef(ctx, candidate.ID, candidate.Content, true); dryErr != nil {
			return fmt.Errorf("dry-run script write check: %w", dryErr)
		}
		_, _ = fmt.Fprintf(w, "dry-run: no changes written to %s (use --confirm to apply)\n", candidate.ID)
		return nil
	}

	backupPath, err := backupScriptConfig(cfg.Dir, candidate.ID, remote.Content)
	if err != nil {
		return err
	}

	if _, err := cc.WriteScriptDef(ctx, candidate.ID, candidate.Content, false); err != nil {
		return fmt.Errorf("writing script: %w", err)
	}

	reloaded := false
	if reloadErr := client.CallService(ctx, "script", "reload", nil); reloadErr != nil {
		slog.Warn("script reload failed, config was written but not activated", "error", reloadErr)
	} else {
		reloaded = true
	}

	_, _ = fmt.Fprintf(w, "applied: %s\n", candidate.ID)
	_, _ = fmt.Fprintf(w, "backup:  %s\n", backupPath)
	if reloaded {
		_, _ = fmt.Fprintln(w, "reload:  ok")
	}
	entityID := "script." + candidate.ID
	if stateData, stateErr := client.GetState(ctx, entityID); stateErr == nil {
		var ent scriptEntity
		if jsonErr := json.Unmarshal(stateData, &ent); jsonErr == nil {
			_, _ = fmt.Fprintf(w, "state:   %s %s\n", ent.EntityID, ent.State)
		}
	}
	return nil
}

func loadScriptCandidate(path, scriptID string) (*scriptConfigCandidate, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil, fmt.Errorf("reading local file: %w", err)
	}
	return normalizeScriptYAML(data, scriptID)
}

func normalizeScriptYAML(data []byte, scriptID string) (*scriptConfigCandidate, error) {
	id := normalizeScriptID(scriptID)
	if id == "" {
		return nil, errors.New("script id must not be empty")
	}

	var top map[string]any
	if err := yaml.Unmarshal(data, &top); err != nil {
		return nil, fmt.Errorf("parsing script YAML: %w", err)
	}
	if len(top) == 0 {
		return nil, errors.New("script YAML must be a non-empty mapping")
	}

	cfg := top
	if len(top) == 1 {
		for key, value := range top {
			keyID := normalizeScriptID(key)
			if keyID == id {
				inner, ok := value.(map[string]any)
				if !ok {
					return nil, fmt.Errorf("script %q must be a YAML mapping", key)
				}
				cfg = inner
			} else if !isScriptDefinitionKey(key) {
				return nil, fmt.Errorf("script wrapper id %q does not match target %q", key, id)
			}
		}
	}

	sequence, ok := cfg["sequence"]
	if !ok || sequence == nil {
		return nil, errors.New("script YAML must include sequence")
	}
	if _, ok := sequence.([]any); !ok {
		return nil, errors.New("script sequence must be a list")
	}

	contentBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshaling normalized script YAML: %w", err)
	}
	return &scriptConfigCandidate{
		ID:      id,
		Config:  cfg,
		Content: string(contentBytes),
	}, nil
}

func normalizeScriptID(id string) string {
	return strings.TrimPrefix(strings.TrimSpace(id), "script.")
}

func isScriptDefinitionKey(key string) bool {
	switch key {
	case "alias", "description", "sequence", "mode", "fields", "variables", "icon", "max", "max_exceeded", "trace":
		return true
	default:
		return false
	}
}

func scriptConfigDiff(remote, local string) []string {
	return writer.DiffLines(remote, local)
}

func scriptDiffHasChanges(lines []string) bool {
	for _, line := range lines {
		if len(line) > 0 && (line[0] == '+' || line[0] == '-') {
			return true
		}
	}
	return false
}

func validateScriptCandidate(ctx context.Context, cfg *config.Config, candidate map[string]any) (bool, error) {
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		slog.Warn("could not connect WebSocket for script validation", "error", err)
		return false, nil
	}
	defer func() { _ = ws.Close() }()

	results, err := ws.ValidateConfig(ctx, nil, nil, candidate["sequence"])
	if err != nil {
		slog.Warn("script validation unavailable", "error", err)
		return false, nil
	}
	if r, ok := results["actions"]; ok && !r.Valid {
		return false, fmt.Errorf("HA rejected the script sequence: %s", r.Error)
	}
	return true, nil
}

func backupScriptConfig(dir, scriptID, content string) (string, error) {
	backupDir := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		return "", fmt.Errorf("creating backup dir: %w", err)
	}
	ts := time.Now().Format("2006-01-02T15-04-05")
	filename := fmt.Sprintf("%s_script_%s.yaml", ts, strings.ReplaceAll(scriptID, string(os.PathSeparator), "_"))
	backupPath := filepath.Join(backupDir, filename)
	if err := os.WriteFile(backupPath, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("writing backup: %w", err)
	}
	return backupPath, nil
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
		if _, connErr := connectCompanion(ctx); connErr != nil {
			return connErr
		}
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
