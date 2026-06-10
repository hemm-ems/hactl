package writer

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// Writer handles automation config writes with backup, validation, and rollback.
type Writer struct {
	client    *haapi.Client
	wsClient  *haapi.WSClient
	backupDir string
}

// New creates a Writer for the given HA instance.
func New(client *haapi.Client, wsClient *haapi.WSClient, backupDir string) *Writer {
	return &Writer{
		client:    client,
		wsClient:  wsClient,
		backupDir: backupDir,
	}
}

// DiffResult holds the result of comparing local vs remote automation config.
type DiffResult struct {
	AutomationID string
	// Lines holds unified-diff-style lines (prefixed with +/-/space).
	Lines      []string
	HasChanges bool
}

// ApplyResult holds the result of applying a config change.
type ApplyResult struct {
	BackupPath   string
	AutomationID string
	Reloaded     bool
	DryRun       bool
	// Validated is true when the candidate config passed HA's
	// validate_config check (false when validation was unavailable).
	Validated bool
}

// Diff compares a local YAML file against the current HA automation config.
func (w *Writer) Diff(ctx context.Context, automationID string, localPath string) (*DiffResult, error) {
	localData, err := os.ReadFile(filepath.Clean(localPath))
	if err != nil {
		return nil, fmt.Errorf("reading local file: %w", err)
	}

	var localConfig map[string]any
	if unmarshalErr := yaml.Unmarshal(localData, &localConfig); unmarshalErr != nil {
		return nil, fmt.Errorf("parsing local YAML: %w", unmarshalErr)
	}

	remoteData, err := w.client.GetAutomationConfig(ctx, automationID)
	if err != nil {
		return nil, fmt.Errorf("fetching remote config: %w", err)
	}

	var remoteConfig map[string]any
	if err := json.Unmarshal(remoteData, &remoteConfig); err != nil {
		return nil, fmt.Errorf("parsing remote config: %w", err)
	}

	localYAML, _ := yaml.Marshal(localConfig)
	remoteYAML, _ := yaml.Marshal(remoteConfig)

	lines := diffLines(string(remoteYAML), string(localYAML))

	hasChanges := false
	for _, l := range lines {
		if len(l) > 0 && (l[0] == '+' || l[0] == '-') {
			hasChanges = true
			break
		}
	}

	return &DiffResult{
		AutomationID: automationID,
		HasChanges:   hasChanges,
		Lines:        lines,
	}, nil
}

// Apply writes an automation config to HA. If confirm is false, only validates and shows diff (dry-run).
func (w *Writer) Apply(ctx context.Context, automationID, localPath string, confirm bool) (*ApplyResult, error) {
	localData, err := os.ReadFile(filepath.Clean(localPath))
	if err != nil {
		return nil, fmt.Errorf("reading local file: %w", err)
	}

	var localConfig map[string]any
	if unmarshalErr := yaml.Unmarshal(localData, &localConfig); unmarshalErr != nil {
		return nil, fmt.Errorf("parsing local YAML: %w", unmarshalErr)
	}

	result := &ApplyResult{
		AutomationID: automationID,
		DryRun:       !confirm,
	}

	// Validate the candidate config against HA's schema before anything else.
	validated, validateErr := w.validateCandidate(ctx, localConfig)
	if validateErr != nil {
		return nil, validateErr
	}
	result.Validated = validated

	if !confirm {
		return result, nil
	}

	// Backup current config before the write
	backupPath, backupErr := w.backup(ctx, automationID)
	if backupErr != nil {
		slog.Warn("could not create backup", "error", backupErr)
	} else {
		result.BackupPath = backupPath
	}

	// Write via Config API
	if err := w.client.UpdateAutomationConfig(ctx, automationID, localConfig); err != nil {
		return nil, fmt.Errorf("writing automation config: %w", err)
	}

	// Reload automations
	if reloadErr := w.client.CallService(ctx, "automation", "reload", nil); reloadErr != nil {
		slog.Warn("reload failed, config was written but not activated", "error", reloadErr)
	} else {
		result.Reloaded = true
	}

	return result, nil
}

// Rollback restores the most recent backup for the given automation.
// If automationID is empty, restores the most recent backup regardless of automation.
func (w *Writer) Rollback(ctx context.Context, automationID string) (*ApplyResult, error) {
	backupFile, err := w.findLatestBackup(automationID)
	if err != nil {
		return nil, err
	}

	data, err := os.ReadFile(filepath.Clean(backupFile))
	if err != nil {
		return nil, fmt.Errorf("reading backup: %w", err)
	}

	var config map[string]any
	if err := yaml.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("parsing backup YAML: %w", err)
	}

	// Extract automation ID from filename if not provided
	if automationID == "" {
		automationID = extractAutoIDFromBackup(backupFile)
	}

	if err := w.client.UpdateAutomationConfig(ctx, automationID, config); err != nil {
		return nil, fmt.Errorf("restoring config: %w", err)
	}

	if reloadErr := w.client.CallService(ctx, "automation", "reload", nil); reloadErr != nil {
		slog.Warn("reload failed after rollback", "error", reloadErr)
	}

	return &ApplyResult{
		AutomationID: automationID,
		BackupPath:   backupFile,
		Reloaded:     true,
	}, nil
}

// validateCandidate checks the automation's trigger/condition/action blocks
// against HA's real config schema via WS validate_config — this validates
// the *candidate* config, not what is already installed. Returns whether
// validation actually ran (it is skipped when no WS connection is available;
// HA's Config API still validates on write) and an error when a section is
// rejected.
func (w *Writer) validateCandidate(ctx context.Context, cfg map[string]any) (bool, error) {
	if w.wsClient == nil {
		return false, nil
	}

	// Automations use legacy singular or modern plural keys; accept both.
	pick := func(singular, plural string) any {
		if v, ok := cfg[singular]; ok {
			return v
		}
		return cfg[plural]
	}
	triggers := pick("trigger", "triggers")
	conditions := pick("condition", "conditions")
	actions := pick("action", "actions")
	if triggers == nil && conditions == nil && actions == nil {
		return false, nil
	}

	results, err := w.wsClient.ValidateConfig(ctx, triggers, conditions, actions)
	if err != nil {
		slog.Warn("config validation unavailable", "error", err)
		return false, nil
	}
	for _, section := range []string{"triggers", "conditions", "actions"} {
		if r, ok := results[section]; ok && !r.Valid {
			return false, fmt.Errorf("HA rejected the %s config: %s", strings.TrimSuffix(section, "s"), r.Error)
		}
	}
	return true, nil
}

// backup saves the current remote config to the backups directory.
func (w *Writer) backup(ctx context.Context, automationID string) (string, error) {
	if err := os.MkdirAll(w.backupDir, 0o750); err != nil {
		return "", fmt.Errorf("creating backup dir: %w", err)
	}

	remoteData, err := w.client.GetAutomationConfig(ctx, automationID)
	if err != nil {
		return "", fmt.Errorf("fetching current config for backup: %w", err)
	}

	var remoteConfig map[string]any
	if unmarshalErr := json.Unmarshal(remoteData, &remoteConfig); unmarshalErr != nil {
		return "", fmt.Errorf("parsing remote config: %w", unmarshalErr)
	}

	yamlData, err := yaml.Marshal(remoteConfig)
	if err != nil {
		return "", fmt.Errorf("marshaling backup: %w", err)
	}

	ts := time.Now().Format("2006-01-02T15-04-05")
	filename := fmt.Sprintf("%s_%s.yaml", ts, automationID)
	backupPath := filepath.Join(w.backupDir, filename)

	if err := os.WriteFile(backupPath, yamlData, 0o600); err != nil {
		return "", fmt.Errorf("writing backup: %w", err)
	}

	slog.Info("backup created", "path", backupPath)
	return backupPath, nil
}

// findLatestBackup returns the path to the most recent backup file.
func (w *Writer) findLatestBackup(automationID string) (string, error) {
	entries, err := os.ReadDir(w.backupDir)
	if err != nil {
		return "", fmt.Errorf("reading backup dir: %w", err)
	}

	var latest string
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isYAMLFile(name) {
			continue
		}
		if automationID == "" || containsAutoID(name, automationID) {
			latest = filepath.Join(w.backupDir, name)
			break
		}
	}

	if latest == "" {
		return "", fmt.Errorf("no backup found for automation %q", automationID)
	}
	return latest, nil
}

// maxLCSLines bounds the O(n·m) LCS table in diffLines: 4096² ints ≈ 128 MB
// worst case, far beyond any automation config. Larger inputs fall back to a
// positional diff instead of allocating quadratically.
const maxLCSLines = 4096

// diffLines produces a unified-diff-style line diff between two strings,
// aligned on the longest common subsequence — an inserted or deleted line
// doesn't mark everything after it as changed.
func diffLines(a, b string) []string {
	aLines := splitLines(a)
	bLines := splitLines(b)
	n, m := len(aLines), len(bLines)
	if n > maxLCSLines || m > maxLCSLines {
		return diffLinesPositional(aLines, bLines)
	}

	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if aLines[i] == bLines[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	var result []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case aLines[i] == bLines[j]:
			result = append(result, " "+aLines[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			result = append(result, "-"+aLines[i])
			i++
		default:
			result = append(result, "+"+bLines[j])
			j++
		}
	}
	for ; i < n; i++ {
		result = append(result, "-"+aLines[i])
	}
	for ; j < m; j++ {
		result = append(result, "+"+bLines[j])
	}
	return result
}

// diffLinesPositional is the line-by-line fallback for inputs too large for
// the LCS table; an insertion shifts everything after it, but output stays
// correct as a diff.
func diffLinesPositional(aLines, bLines []string) []string {
	var result []string
	for i := range max(len(aLines), len(bLines)) {
		var aLine, bLine string
		if i < len(aLines) {
			aLine = aLines[i]
		}
		if i < len(bLines) {
			bLine = bLines[i]
		}
		if aLine == bLine {
			result = append(result, " "+aLine)
			continue
		}
		if i < len(aLines) {
			result = append(result, "-"+aLine)
		}
		if i < len(bLines) {
			result = append(result, "+"+bLine)
		}
	}
	return result
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := range len(s) {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

const (
	extYAML = ".yaml"
	extYML  = ".yml"
)

func isYAMLFile(name string) bool {
	return len(name) > 5 && (name[len(name)-5:] == extYAML || name[len(name)-4:] == extYML)
}

func containsAutoID(filename, automationID string) bool {
	// Backup filenames are like "2026-04-17T09-42-05_climate_schedule.yaml"
	// The automation ID follows the timestamp underscore.
	for i := range len(filename) {
		if i > 0 && filename[i-1] == '_' {
			rest := filename[i:]
			// Strip .yaml/.yml extension
			if idx := len(rest) - 5; idx > 0 && rest[idx:] == extYAML {
				rest = rest[:idx]
			} else if idx := len(rest) - 4; idx > 0 && rest[idx:] == extYML {
				rest = rest[:idx]
			}
			if rest == automationID {
				return true
			}
		}
	}
	return false
}

func extractAutoIDFromBackup(path string) string {
	base := filepath.Base(path)
	// Format: "2026-04-17T09-42-05_climate_schedule.yaml"
	// Find first underscore after timestamp (20 chars: 2026-04-17T09-42-05)
	if len(base) < 21 {
		return base
	}
	rest := base[20:] // skip "2026-04-17T09-42-05_"
	// Strip extension
	if idx := len(rest) - 5; idx > 0 && rest[idx:] == extYAML {
		rest = rest[:idx]
	} else if idx := len(rest) - 4; idx > 0 && rest[idx:] == extYML {
		rest = rest[:idx]
	}
	return rest
}
