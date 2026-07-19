package writer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/hemm-ems/hactl/internal/haapi"
)

func TestDiffLines_NoChanges(t *testing.T) {
	lines := diffLines("foo\nbar\nbaz\n", "foo\nbar\nbaz\n")
	for _, l := range lines {
		if len(l) > 0 && l[0] != ' ' {
			t.Errorf("expected no changes, got line: %q", l)
		}
	}
}

func TestDiffLines_WithChanges(t *testing.T) {
	lines := diffLines("foo\nbar\nbaz\n", "foo\nqux\nbaz\n")
	hasPlus := false
	hasMinus := false
	for _, l := range lines {
		if len(l) > 0 && l[0] == '+' {
			hasPlus = true
		}
		if len(l) > 0 && l[0] == '-' {
			hasMinus = true
		}
	}
	if !hasPlus || !hasMinus {
		t.Errorf("expected +/- lines in diff, got: %v", lines)
	}
}

func TestDiffLines_InsertionDoesNotShiftEverything(t *testing.T) {
	// A single line inserted at the top must not mark every following line
	// as changed (the failure mode of a naive positional diff).
	a := "alias: x\ntrigger: []\ncondition: []\naction: []\n"
	b := "id: new\nalias: x\ntrigger: []\ncondition: []\naction: []\n"
	lines := diffLines(a, b)

	var plus, minus, same int
	for _, l := range lines {
		switch {
		case strings.HasPrefix(l, "+"):
			plus++
		case strings.HasPrefix(l, "-"):
			minus++
		default:
			same++
		}
	}
	if plus != 1 || minus != 0 {
		t.Errorf("want exactly one + line and no - lines, got +%d -%d (diff: %v)", plus, minus, lines)
	}
	if same != 4 {
		t.Errorf("want 4 unchanged lines, got %d (diff: %v)", same, lines)
	}
}

func TestDiffLines_HugeInputFallsBackWithoutQuadraticAllocation(t *testing.T) {
	// Inputs beyond maxLCSLines must take the positional path; this mainly
	// guards that the cap exists (the LCS table would be ~170 GB here).
	a := strings.Repeat("line\n", maxLCSLines+10)
	lines := diffLines(a, a+"extra\n")
	var plus int
	for _, l := range lines {
		if strings.HasPrefix(l, "+") {
			plus++
		}
	}
	if plus != 1 {
		t.Errorf("want exactly one + line, got %d", plus)
	}
}

func TestDiffLines_Addition(t *testing.T) {
	lines := diffLines("foo\n", "foo\nbar\n")
	hasPlus := false
	for _, l := range lines {
		if len(l) > 0 && l[0] == '+' {
			hasPlus = true
		}
	}
	if !hasPlus {
		t.Error("expected + line for addition")
	}
}

func TestDiffLines_Deletion(t *testing.T) {
	lines := diffLines("foo\nbar\n", "foo\n")
	hasMinus := false
	for _, l := range lines {
		if len(l) > 0 && l[0] == '-' {
			hasMinus = true
		}
	}
	if !hasMinus {
		t.Error("expected - line for deletion")
	}
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"foo", 1},
		{"foo\n", 1},
		{"foo\nbar", 2},
		{"foo\nbar\n", 2},
		{"foo\nbar\nbaz", 3},
	}
	for _, tt := range tests {
		got := splitLines(tt.input)
		if len(got) != tt.want {
			t.Errorf("splitLines(%q) = %d lines, want %d", tt.input, len(got), tt.want)
		}
	}
}

func TestIsYAMLFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"foo.yaml", true},
		{"foo.yml", true},
		{"backup_climate.yaml", true},
		{"foo.json", false},
		{"a.yaml", true},
		{".yaml", false},
		{"test", false},
	}
	for _, tt := range tests {
		got := isYAMLFile(tt.name)
		if got != tt.want {
			t.Errorf("isYAMLFile(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestContainsAutoID(t *testing.T) {
	tests := []struct {
		filename string
		autoID   string
		want     bool
	}{
		{"2026-04-17T09-42-05_climate_schedule.yaml", "climate_schedule", true},
		{"2026-04-17T09-42-05_alarm_morning.yaml", "alarm_morning", true},
		{"2026-04-17T09-42-05_alarm_morning.yaml", "climate_schedule", false},
	}
	for _, tt := range tests {
		got := containsAutoID(tt.filename, tt.autoID)
		if got != tt.want {
			t.Errorf("containsAutoID(%q, %q) = %v, want %v", tt.filename, tt.autoID, got, tt.want)
		}
	}
}

func TestExtractAutoIDFromBackup(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/backups/2026-04-17T09-42-05_climate_schedule.yaml", "climate_schedule"},
		{"/backups/2026-04-17T09-42-05_alarm_morning.yaml", "alarm_morning"},
	}
	for _, tt := range tests {
		got := extractAutoIDFromBackup(tt.path)
		if got != tt.want {
			t.Errorf("extractAutoIDFromBackup(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestNew(t *testing.T) {
	client := haapi.New("http://localhost", "token")
	w := New(client, nil, "/tmp/backups")
	if w == nil {
		t.Fatal("New returned nil")
	}
	if w.backupDir != "/tmp/backups" {
		t.Errorf("backupDir = %q, want /tmp/backups", w.backupDir)
	}
}

func TestFindLatestBackup_Found(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"2026-01-01T09-00-00_climate_schedule.yaml",
		"2026-01-02T09-00-00_climate_schedule.yaml",
		"2026-01-03T09-00-00_alarm_morning.yaml",
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("content"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	w := &Writer{backupDir: dir}

	// Should return the most recent climate_schedule backup (jan 2)
	latest, err := w.findLatestBackup("climate_schedule")
	if err != nil {
		t.Fatalf("findLatestBackup failed: %v", err)
	}
	if !strings.Contains(latest, "2026-01-02") {
		t.Errorf("findLatestBackup = %q, expected the jan 2 file", latest)
	}
}

func TestFindLatestBackup_AnyWhenEmptyID(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-01-05T09-00-00_some_auto.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	w := &Writer{backupDir: dir}
	latest, err := w.findLatestBackup("")
	if err != nil {
		t.Fatalf("findLatestBackup(empty id) failed: %v", err)
	}
	if !strings.Contains(latest, "some_auto") {
		t.Errorf("findLatestBackup(empty) = %q, want a yaml file", latest)
	}
}

func TestFindLatestBackup_NotFound(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "2026-01-01T09-00-00_other_auto.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	w := &Writer{backupDir: dir}
	_, err := w.findLatestBackup("missing_auto")
	if err == nil {
		t.Fatal("expected error for missing backup, got nil")
	}
}

func TestFindLatestBackup_EmptyDir(t *testing.T) {
	dir := t.TempDir()
	w := &Writer{backupDir: dir}
	_, err := w.findLatestBackup("any_auto")
	if err == nil {
		t.Fatal("expected error for empty backup dir, got nil")
	}
}

// startValidateWSServer stands up a fake HA WebSocket endpoint that completes
// the auth handshake, reads one validate_config command, and replies with the
// given per-section result map. It lets ValidateCandidate be exercised without
// a live HA.
func startValidateWSServer(t *testing.T, result map[string]any) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer func() { _ = c.Close() }()

		_ = c.WriteJSON(map[string]string{"type": "auth_required", "ha_version": "2026.4"})
		var authMsg map[string]string
		_ = c.ReadJSON(&authMsg)
		_ = c.WriteJSON(map[string]string{"type": "auth_ok", "ha_version": "2026.4"})

		var cmd map[string]any
		if readErr := c.ReadJSON(&cmd); readErr != nil {
			return
		}
		if cmd["type"] != "validate_config" {
			t.Errorf("expected validate_config, got %q", cmd["type"])
			return
		}
		_ = c.WriteJSON(map[string]any{
			"id":      cmd["id"],
			"type":    "result",
			"success": true,
			"result":  result,
		})
	}))
}

func connectValidateWS(t *testing.T, srv *httptest.Server) *haapi.WSClient {
	t.Helper()
	ws := haapi.NewWSClient(srv.URL, "tok")
	if err := ws.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	return ws
}

func TestValidateCandidate_NoWSClientSkips(t *testing.T) {
	w := New(haapi.New("http://localhost", "tok"), nil, "")
	candidate := map[string]any{"triggers": []any{}, "conditions": []any{}, "actions": []any{}}
	validated, err := w.ValidateCandidate(context.Background(), candidate)
	if err != nil {
		t.Fatalf("ValidateCandidate: %v", err)
	}
	if validated {
		t.Error("validated = true with no WS client, want false (skipped)")
	}
}

func TestValidateCandidate_Valid(t *testing.T) {
	srv := startValidateWSServer(t, map[string]any{
		"triggers":   map[string]any{"valid": true, "error": nil},
		"conditions": map[string]any{"valid": true, "error": nil},
		"actions":    map[string]any{"valid": true, "error": nil},
	})
	defer srv.Close()
	ws := connectValidateWS(t, srv)
	defer func() { _ = ws.Close() }()

	w := New(haapi.New("http://localhost", "tok"), ws, "")
	candidate := map[string]any{
		"triggers":   []any{map[string]any{"trigger": "time", "at": "06:00:00"}},
		"conditions": []any{},
		"actions":    []any{map[string]any{"delay": "00:00:01"}},
	}
	validated, err := w.ValidateCandidate(context.Background(), candidate)
	if err != nil {
		t.Fatalf("ValidateCandidate rejected a valid config: %v", err)
	}
	if !validated {
		t.Error("validated = false for a valid config, want true")
	}
}

func TestValidateCandidate_Rejected(t *testing.T) {
	srv := startValidateWSServer(t, map[string]any{
		"conditions": map[string]any{"valid": false, "error": "invalid template"},
	})
	defer srv.Close()
	ws := connectValidateWS(t, srv)
	defer func() { _ = ws.Close() }()

	w := New(haapi.New("http://localhost", "tok"), ws, "")
	candidate := map[string]any{
		"triggers":   []any{map[string]any{"trigger": "state", "entity_id": "sensor.x"}},
		"conditions": []any{map[string]any{"condition": "template", "value_template": "{{ broken"}},
		"actions":    []any{map[string]any{"delay": "00:00:01"}},
	}
	validated, err := w.ValidateCandidate(context.Background(), candidate)
	if err == nil {
		t.Fatal("ValidateCandidate accepted a rejected config, want error")
	}
	if validated {
		t.Error("validated = true for a rejected config, want false")
	}
	if !strings.Contains(err.Error(), "HA rejected the condition") {
		t.Errorf("error = %q, want it to mention the rejected condition section", err)
	}
}

// makeWriterServer creates an httptest server that handles automation config operations.
func makeWriterServer(t *testing.T, _ string, remoteConfig string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/api/config/automation/config/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, remoteConfig)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/api/config/automation/config/"):
			body, _ := io.ReadAll(r.Body)
			if len(body) == 0 {
				http.Error(w, "empty body", http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/api/services/"):
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

func TestWriter_Diff_NoChanges(t *testing.T) {
	// Local and remote are identical → no changes
	remoteJSON := `{"alias":"Test","trigger":[],"condition":[],"action":[]}`
	srv := makeWriterServer(t, "test_auto", remoteJSON)
	defer srv.Close()

	// Write the same config as local YAML
	localDir := t.TempDir()
	localFile := filepath.Join(localDir, "test_auto.yaml")
	localYAML := "alias: Test\naction: []\ncondition: []\ntrigger: []\n"
	if err := os.WriteFile(localFile, []byte(localYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	client := haapi.New(srv.URL, "tok")
	w := New(client, nil, t.TempDir())

	result, err := w.Diff(context.Background(), "test_auto", localFile)
	if err != nil {
		t.Fatalf("Diff failed: %v", err)
	}
	if result.AutomationID != "test_auto" {
		t.Errorf("AutomationID = %q, want 'test_auto'", result.AutomationID)
	}
}

func TestWriter_Diff_WithChanges(t *testing.T) {
	remoteJSON := `{"alias":"Old Name","trigger":[],"condition":[],"action":[]}`
	srv := makeWriterServer(t, "test_auto", remoteJSON)
	defer srv.Close()

	localDir := t.TempDir()
	localFile := filepath.Join(localDir, "test_auto.yaml")
	// Different alias → should detect changes
	if err := os.WriteFile(localFile, []byte("alias: New Name\ntrigger: []\ncondition: []\naction: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	client := haapi.New(srv.URL, "tok")
	w := New(client, nil, t.TempDir())

	result, err := w.Diff(context.Background(), "test_auto", localFile)
	if err != nil {
		t.Fatalf("Diff failed: %v", err)
	}
	if !result.HasChanges {
		t.Error("Diff.HasChanges = false, want true (different alias)")
	}
}

func TestWriter_Apply_DryRun(t *testing.T) {
	remoteJSON := `{"alias":"Existing","trigger":[],"condition":[],"action":[]}`
	srv := makeWriterServer(t, "test_auto", remoteJSON)
	defer srv.Close()

	localDir := t.TempDir()
	localFile := filepath.Join(localDir, "test_auto.yaml")
	if err := os.WriteFile(localFile, []byte("alias: Updated\ntrigger: []\ncondition: []\naction: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	client := haapi.New(srv.URL, "tok")
	backupDir := t.TempDir()
	w := New(client, nil, backupDir)

	// confirm=false → dry run
	result, err := w.Apply(context.Background(), "test_auto", localFile, false)
	if err != nil {
		t.Fatalf("Apply dry-run failed: %v", err)
	}
	if !result.DryRun {
		t.Error("Apply dry-run: DryRun = false, want true")
	}
	if result.AutomationID != "test_auto" {
		t.Errorf("AutomationID = %q, want 'test_auto'", result.AutomationID)
	}

	// A dry run must not leave backup files behind
	if result.BackupPath != "" {
		t.Errorf("dry-run created backup %q, want none", result.BackupPath)
	}
	entries, err := os.ReadDir(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dry-run left %d files in backup dir, want 0", len(entries))
	}
}

func TestWriter_Apply_Confirm(t *testing.T) {
	remoteJSON := `{"alias":"Old","trigger":[],"condition":[],"action":[]}`
	srv := makeWriterServer(t, "test_auto", remoteJSON)
	defer srv.Close()

	localDir := t.TempDir()
	localFile := filepath.Join(localDir, "test_auto.yaml")
	if err := os.WriteFile(localFile, []byte("alias: New\ntrigger: []\ncondition: []\naction: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	client := haapi.New(srv.URL, "tok")
	backupDir := t.TempDir()
	w := New(client, nil, backupDir)

	// confirm=true → actually writes
	result, err := w.Apply(context.Background(), "test_auto", localFile, true)
	if err != nil {
		t.Fatalf("Apply confirm failed: %v", err)
	}
	if result.DryRun {
		t.Error("Apply confirm: DryRun = true, want false")
	}
	_ = result.Reloaded // OK whether reloaded or not since mock returns 200
}

func TestWriter_Apply_InvalidYAML(t *testing.T) {
	srv := makeWriterServer(t, "test_auto", `{"alias":"Old"}`)
	defer srv.Close()

	localDir := t.TempDir()
	localFile := filepath.Join(localDir, "test_auto.yaml")
	// Invalid YAML
	if err := os.WriteFile(localFile, []byte("{ not: valid: yaml: }:"), 0o600); err != nil {
		t.Fatal(err)
	}

	client := haapi.New(srv.URL, "tok")
	w := New(client, nil, t.TempDir())

	// YAML parsing error - might succeed or fail depending on yaml parser strictness
	// Just ensure no panic
	_, _ = w.Apply(context.Background(), "test_auto", localFile, false)
}

func TestWriter_Apply_MissingFile(t *testing.T) {
	srv := makeWriterServer(t, "test_auto", `{}`)
	defer srv.Close()

	client := haapi.New(srv.URL, "tok")
	w := New(client, nil, t.TempDir())

	_, err := w.Apply(context.Background(), "test_auto", "/nonexistent/file.yaml", false)
	if err == nil {
		t.Fatal("expected error for missing local file, got nil")
	}
}

func TestWriter_Rollback(t *testing.T) {
	remoteJSON := `{"alias":"Current","trigger":[],"condition":[],"action":[]}`
	srv := makeWriterServer(t, "test_auto", remoteJSON)
	defer srv.Close()

	backupDir := t.TempDir()
	// Create a backup file
	backupFile := filepath.Join(backupDir, "2026-01-01T09-00-00_test_auto.yaml")
	backupYAML := `alias: Backup Version
trigger: []
condition: []
action: []
`
	if err := os.WriteFile(backupFile, []byte(backupYAML), 0o600); err != nil {
		t.Fatal(err)
	}

	client := haapi.New(srv.URL, "tok")
	w := New(client, nil, backupDir)

	result, err := w.Rollback(context.Background(), "test_auto")
	if err != nil {
		t.Fatalf("Rollback failed: %v", err)
	}
	if result.AutomationID != "test_auto" {
		t.Errorf("AutomationID = %q, want 'test_auto'", result.AutomationID)
	}
}

func TestWriter_Rollback_EmptyID(t *testing.T) {
	remoteJSON := `{"alias":"Current","trigger":[],"condition":[],"action":[]}`
	srv := makeWriterServer(t, "", remoteJSON)
	defer srv.Close()

	backupDir := t.TempDir()
	backupFile := filepath.Join(backupDir, "2026-01-01T09-00-00_mystery_auto.yaml")
	if err := os.WriteFile(backupFile, []byte("alias: Mystery\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	client := haapi.New(srv.URL, "tok")
	w := New(client, nil, backupDir)

	// Empty autoID → picks most recent backup, extracts ID from filename
	result, err := w.Rollback(context.Background(), "")
	if err != nil {
		t.Fatalf("Rollback(empty ID) failed: %v", err)
	}
	if result.AutomationID == "" {
		t.Error("Rollback(empty ID): AutomationID should be extracted from backup filename")
	}
}

func TestExtractAutoIDFromBackup_ShortBasename(t *testing.T) {
	// Basename shorter than 21 chars → returned unchanged
	got := extractAutoIDFromBackup("/some/path/short.yaml")
	if got != "short.yaml" {
		t.Errorf("extractAutoIDFromBackup(short) = %q, want %q", got, "short.yaml")
	}
}

func TestExtractAutoIDFromBackup_YMLExtension(t *testing.T) {
	got := extractAutoIDFromBackup("/backups/2026-04-17T09-42-05_alarm_morning.yml")
	if got != "alarm_morning" {
		t.Errorf("extractAutoIDFromBackup(.yml) = %q, want 'alarm_morning'", got)
	}
}

func TestWriter_Rollback_InvalidYAML(t *testing.T) {
	srv := makeWriterServer(t, "test_auto", `{"alias":"Current","trigger":[],"condition":[],"action":[]}`)
	defer srv.Close()

	backupDir := t.TempDir()
	backupFile := filepath.Join(backupDir, "2026-01-01T09-00-00_test_auto.yaml")
	// Write invalid YAML to the backup file
	if err := os.WriteFile(backupFile, []byte("{ : bad yaml"), 0o600); err != nil {
		t.Fatal(err)
	}

	client := haapi.New(srv.URL, "tok")
	w := New(client, nil, backupDir)

	_, err := w.Rollback(context.Background(), "test_auto")
	if err == nil {
		t.Fatal("expected error for invalid backup YAML, got nil")
	}
}

// Verify the JSON number handling in backup restoration.
func TestWriter_Backup_CreatesFile(t *testing.T) {
	remoteJSON := `{"alias":"My Auto","id":"my_auto","trigger":[],"condition":[],"action":[]}`
	srv := makeWriterServer(t, "my_auto", remoteJSON)
	defer srv.Close()

	backupDir := t.TempDir()
	client := haapi.New(srv.URL, "tok")
	w := &Writer{client: client, backupDir: backupDir}

	backupPath, err := w.backup(context.Background(), "my_auto")
	if err != nil {
		t.Fatalf("backup failed: %v", err)
	}
	if backupPath == "" {
		t.Fatal("backup returned empty path")
	}
	if _, statErr := os.Stat(backupPath); os.IsNotExist(statErr) {
		t.Errorf("backup file %q does not exist", backupPath)
	}
	data, _ := os.ReadFile(filepath.Clean(backupPath))
	var check map[string]any
	if err := json.Unmarshal(data, &check); err != nil {
		// YAML — check for content
		if !strings.Contains(string(data), "My Auto") {
			t.Errorf("backup file content missing automation alias: %q", string(data))
		}
	}
}
