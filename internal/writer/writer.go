package writer

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/backupfile"
	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// Writer handles automation config writes with backup, validation, and rollback.
//
// The write goes through the companion's single-entry route, not Home
// Assistant's `POST /api/config/automation/config/<id>`. Two defects came out
// of that endpoint and neither was fixable behind it (D-14, issue #128):
//
//   - HA's storage collection loads the whole automations.yaml, applies the one
//     change and re-dumps the file with its own serializer, so a confirmed
//     apply on one automation came back having reformatted every other one.
//   - hactl reached it through `map[string]any` and `encoding/json.Marshal`,
//     which sorts keys — so a confirmed write also silently alphabetized the
//     edited automation's nested keys, `(trigger, entity_id, to)` becoming
//     `(entity_id, to, trigger)` on disk. The diff could not show it, because
//     BOTH sides of the comparison went through the same normalization: the
//     change was invisible to the tool performing it (finding #93).
//
// The companion takes the entry as YAML TEXT and splices it into the file, so
// what a caller previewed is what lands, byte for byte. Both halves of this
// type therefore work on text: the diff compares the file the caller edited
// against the companion's rendering of the stored entry, which is the same
// document the write replaces.
type Writer struct {
	client    *haapi.Client
	wsClient  *haapi.WSClient
	cc        *companion.Client
	backupDir string
}

// New creates a Writer for the given HA instance. cc may be nil for the paths
// that never write (PlanRollback, ValidateCandidate); every write path returns
// ErrNoCompanion without it rather than falling back to HA's endpoint, because
// a silent fallback is exactly the whole-file rewrite this route exists to
// stop.
func New(client *haapi.Client, wsClient *haapi.WSClient, cc *companion.Client, backupDir string) *Writer {
	return &Writer{
		client:    client,
		wsClient:  wsClient,
		cc:        cc,
		backupDir: backupDir,
	}
}

// ErrNoCompanion is returned by the write paths when no companion is
// configured. `auto create` and `auto delete` have had this dependency since
// they moved to the same route; apply and rollback are the family's last two.
var ErrNoCompanion = errors.New("this command writes through hactl-companion, which is not configured for this instance " +
	"(hactl companion status); Home Assistant's own config endpoint re-serializes the whole automations.yaml, " +
	"so hactl does not fall back to it")

// DiffResult holds the result of comparing local vs remote automation config.
type DiffResult struct {
	AutomationID string
	// Lines holds unified-diff-style lines (prefixed with +/-/space).
	Lines      []string
	HasChanges bool
}

// ChangedLines counts the lines of d that actually change.
func (d *DiffResult) ChangedLines() int { return ChangedLines(d.Lines) }

// isChange reports whether a diff line is an addition or a removal. Every
// question this package answers about a diff — does it change anything, how
// many lines change — goes through this one predicate, because it was written
// out three times and one of the three counted something else.
func isChange(line string) bool {
	return len(line) > 0 && (line[0] == '+' || line[0] == '-')
}

// ChangedLines counts the `+`/`-` lines of a diff.
//
// It exists because `changed_lines` was `len(diff.Lines)` — the whole diff,
// context lines and "… N unchanged lines …" markers included — so a one-line
// alias edit reported `changed_lines: 14` (finding #94). A field's name is a
// claim about what it counts, and both `auto apply` and `script apply` made it.
func ChangedLines(lines []string) int {
	n := 0
	for _, line := range lines {
		if isChange(line) {
			n++
		}
	}
	return n
}

// unifiedDiffChangedLines counts the real changes in a unified diff — the
// `+`/`-` lines that are not the `---`/`+++` file headers, which start with the
// same characters and are not changes to anything.
func unifiedDiffChangedLines(diff string) int {
	n := 0
	for line := range strings.SplitSeq(diff, "\n") {
		if strings.HasPrefix(line, "---") || strings.HasPrefix(line, "+++") {
			continue
		}
		if isChange(line) {
			n++
		}
	}
	return n
}

// HasChanges reports whether a diff contains any change at all.
func HasChanges(lines []string) bool {
	return slices.ContainsFunc(lines, isChange)
}

// ApplyResult holds the result of applying a config change.
type ApplyResult struct {
	BackupPath   string
	AutomationID string
	// ReloadError carries the companion's reason when Reloaded is false.
	ReloadError string
	Reloaded    bool
	DryRun      bool
	// Validated is true when the candidate config passed HA's
	// validate_config check (false when validation was unavailable).
	Validated bool
	// WriterChangedLines is how many lines the WRITER's own dry run says will
	// change — the companion diffs its serialization of the stored entry
	// against its serialization of the candidate, which is what lands on disk.
	// It can exceed the diff hactl shows: the candidate's own indentation and
	// quoting are the caller's, and the entry is written in the companion's
	// canonical style. Zero when the companion answered no diff.
	WriterChangedLines int
	// Reformatted is true when the companion could not splice this entry and
	// re-serialized the whole file instead, so formatting elsewhere may have
	// changed (companion C-14). Surfaced rather than swallowed: the difference
	// between "your entry changed" and "the file was rewritten" is the whole
	// point of routing the write here.
	Reformatted bool
}

// Diff compares a local YAML file against the stored automation entry.
//
// Both sides are TEXT: the file as the caller wrote it, and the companion's
// rendering of the entry as it sits in automations.yaml — which is the same
// document `auto cat` prints and the same one the write replaces. The previous
// implementation marshalled both sides from `map[string]any`, which sorts, so
// every ordering difference was normalized away on both sides at once and a
// confirmed write could change lines the diff had shown as unchanged.
func (w *Writer) Diff(ctx context.Context, automationID string, localPath string) (*DiffResult, error) {
	localText, _, err := readLocalAutomation(localPath)
	if err != nil {
		return nil, err
	}

	remoteText, err := w.remoteEntry(ctx, automationID)
	if err != nil {
		return nil, err
	}

	lines := diffLines(remoteText, localText)

	return &DiffResult{
		AutomationID: automationID,
		HasChanges:   HasChanges(lines),
		Lines:        lines,
	}, nil
}

// Apply writes an automation entry through the companion. If confirm is false
// it validates the candidate and asks the companion to rehearse the same write
// (H-2: a preview fails exactly where --confirm would).
func (w *Writer) Apply(ctx context.Context, automationID, localPath string, confirm bool) (*ApplyResult, error) {
	localText, localConfig, err := readLocalAutomation(localPath)
	if err != nil {
		return nil, err
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

	if w.cc == nil {
		return nil, ErrNoCompanion
	}

	if !confirm {
		// The same route, rehearsed: the companion resolves the entry and
		// parses the body exactly as it would on the real write, so a preview
		// cannot succeed where --confirm fails.
		resp, dryErr := w.cc.WriteAutomationDef(ctx, automationID, localText, true)
		if dryErr != nil {
			return nil, fmt.Errorf("dry-run automation write check: %w", dryErr)
		}
		result.WriterChangedLines = unifiedDiffChangedLines(resp.Diff)
		return result, nil
	}

	// Backup current config before the write. A failed backup is fatal: without
	// it the previous config is unrecoverable, so `auto rollback` would have
	// nothing to restore. Warning and writing anyway silently traded the user's
	// only undo for a log line they never see.
	backupPath, backupErr := w.backup(ctx, automationID)
	if backupErr != nil {
		return nil, fmt.Errorf("refusing to write without a backup: %w", backupErr)
	}
	result.BackupPath = backupPath

	resp, writeErr := w.cc.WriteAutomationDef(ctx, automationID, localText, false)
	if writeErr != nil {
		return nil, fmt.Errorf("writing automation config: %w", writeErr)
	}
	// The companion reloads the domain itself and reports what happened, so
	// there is no second reload call to disagree with it.
	result.Reloaded = resp.Reloaded
	result.ReloadError = resp.ReloadError
	result.Reformatted = resp.Reformatted
	if !resp.Reloaded {
		slog.Warn("the entry was written but Home Assistant did not confirm the reload",
			"automation", automationID, "reason", resp.ReloadError)
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
	if parseErr := yaml.Unmarshal(data, &config); parseErr != nil {
		return nil, fmt.Errorf("parsing backup YAML: %w", parseErr)
	}
	if len(config) == 0 {
		// backup() can no longer write an empty file, so an empty decode here
		// is a truncated or foreign file — restoring it would overwrite the
		// live config with nothing, which is worse than any state it could fix.
		return nil, fmt.Errorf(
			"backup %s decoded to %s data: an automation config is never empty, refusing to restore it: %w",
			backupFile, degeneracy.Marker, degeneracy.ErrDegenerate)
	}

	// Extract automation ID from filename if not provided
	if automationID == "" {
		automationID = extractAutoIDFromBackup(backupFile)
	}

	if w.cc == nil {
		return nil, ErrNoCompanion
	}
	// The backup's own bytes go back, so an undo restores what was taken —
	// which is only true while the write path is byte-preserving in both
	// directions. Restoring the parsed map through HA's endpoint would have
	// rewritten the file the rollback exists to put back.
	resp, err := w.cc.WriteAutomationDef(ctx, automationID, string(data), false)
	if err != nil {
		return nil, fmt.Errorf("restoring config: %w", err)
	}

	// Reloaded reports what happened, not what was attempted. This field used
	// to be hardcoded true while the reload error went to a WARN, so a rollback
	// whose reload failed printed "reload: ok" — telling the operator the old
	// config was live at the exact moment Home Assistant was still running the
	// broken one. Apply, forty lines up, always reported it correctly.
	if !resp.Reloaded {
		slog.Warn("reload failed after rollback; the restored config is on disk but HA has not read it",
			"automation", automationID, "reason", resp.ReloadError)
	}

	return &ApplyResult{
		AutomationID: automationID,
		BackupPath:   backupFile,
		Reloaded:     resp.Reloaded,
		ReloadError:  resp.ReloadError,
		Reformatted:  resp.Reformatted,
	}, nil
}

// PlanRollback resolves which backup Rollback would restore, without applying
// it — the dry-run preview for `hactl rollback`. It needs neither the HA client
// nor the WS connection, so a Writer built with nil clients is sufficient.
func (w *Writer) PlanRollback(automationID string) (*ApplyResult, error) {
	backupFile, err := w.findLatestBackup(automationID)
	if err != nil {
		return nil, err
	}
	id := automationID
	if id == "" {
		id = extractAutoIDFromBackup(backupFile)
	}
	return &ApplyResult{
		AutomationID: id,
		BackupPath:   backupFile,
		DryRun:       true,
	}, nil
}

// ValidateCandidate validates a parsed automation config against HA's schema
// without writing anything. It mirrors the check Apply runs so the create path
// (which writes via the companion, not the Config API) can refuse the same
// broken configs Apply refuses. Returns whether validation actually ran (false
// when no WS connection is available; HA still validates on write) and an error
// when HA rejects a section.
func (w *Writer) ValidateCandidate(ctx context.Context, cfg map[string]any) (bool, error) {
	return w.validateCandidate(ctx, cfg)
}

// remoteEntry fetches the stored entry as the companion renders it, refusing a
// document that decodes to nothing (H-7: an automation config is never empty,
// so an empty one means the route's shape moved, and diffing against it would
// render as a fictitious whole-file change).
func (w *Writer) remoteEntry(ctx context.Context, automationID string) (string, error) {
	if w.cc == nil {
		return "", ErrNoCompanion
	}
	resp, err := w.cc.GetAutomationDef(ctx, automationID)
	if err != nil {
		return "", fmt.Errorf("fetching remote config: %w", err)
	}
	var stored map[string]any
	if unmarshalErr := yaml.Unmarshal([]byte(resp.Content), &stored); unmarshalErr != nil {
		return "", fmt.Errorf("parsing remote config: %w", unmarshalErr)
	}
	if len(stored) == 0 {
		return "", fmt.Errorf(
			"GET /v1/config/automation?id=%s returned %s data: the document decoded to nothing, "+
				"which a real automation config never is (HA's schema requires triggers and actions): %w",
			automationID, degeneracy.Marker, degeneracy.ErrDegenerate)
	}
	return resp.Content, nil
}

// readLocalAutomation reads the candidate the caller wrote, returning both its
// exact text (what will be written) and its parsed form (what is validated).
func readLocalAutomation(localPath string) (string, map[string]any, error) {
	localData, err := os.ReadFile(filepath.Clean(localPath))
	if err != nil {
		return "", nil, fmt.Errorf("reading local file: %w", err)
	}
	var localConfig map[string]any
	if unmarshalErr := yaml.Unmarshal(localData, &localConfig); unmarshalErr != nil {
		return "", nil, fmt.Errorf("parsing local YAML: %w", unmarshalErr)
	}
	return string(localData), localConfig, nil
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

// backup saves the current entry to the backups directory, as the bytes that
// are on disk rather than as a re-serialization of them. A backup that
// normalizes what it saves cannot restore what it took.
//
// The name comes from backupfile, which will not write over a name that is
// taken (H-26). It used to be `<second>_<id>.yaml` written with os.WriteFile,
// so two confirmed writes inside one second destroyed one of the two recovery
// points and told both callers theirs was safe (#101).
func (w *Writer) backup(ctx context.Context, automationID string) (string, error) {
	remoteText, err := w.remoteEntry(ctx, automationID)
	if err != nil {
		return "", fmt.Errorf("fetching current config for backup: %w", err)
	}

	backupPath, err := backupfile.Write(w.backupDir, 0o600, []byte(remoteText),
		func(stamp string) string { return fmt.Sprintf("%s_%s.yaml", stamp, automationID) })
	if err != nil {
		return "", err
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

// DiffLines exposes the package diff implementation for command paths that
// need the same compact line diff without using the automation Writer.
func DiffLines(a, b string) []string {
	return diffLines(a, b)
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

// containsAutoID reports whether a backup file belongs to automationID.
//
// A backup belongs to exactly one automation, so the id must be the whole name
// after the timestamp — not a trailing underscore-delimited segment of it.
// Matching a segment made `auto rollback door` select bathroom_light_on_door's
// backup and then write that config back under the id the user asked for.
func containsAutoID(filename, automationID string) bool {
	return automationID != "" && extractAutoIDFromBackup(filename) == automationID
}

// extractAutoIDFromBackup returns the automation id a backup file belongs to.
//
// The name is `<stamp>_<id>.yaml` and the stamp contains no underscore — it
// separates its own fields with `-`, `T` and `.` — so the id is everything
// after the FIRST underscore, whatever width the stamp has. It used to be
// `base[20:]`, a hard-coded skip measured off `2026-04-17T09-42-05`, which
// silently returned the wrong id the moment the stamp grew microseconds to
// stop backups overwriting each other (H-26). A parser that encodes the
// length of the thing it is skipping is a second copy of that format.
func extractAutoIDFromBackup(path string) string {
	base := filepath.Base(path)
	_, rest, ok := strings.Cut(base, "_")
	if !ok {
		return base
	}
	// Strip extension
	if idx := len(rest) - 5; idx > 0 && rest[idx:] == extYAML {
		rest = rest[:idx]
	} else if idx := len(rest) - 4; idx > 0 && rest[idx:] == extYML {
		rest = rest[:idx]
	}
	return rest
}
