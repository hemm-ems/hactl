package writer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/degeneracy"
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

		// A backup belongs to exactly one automation. Matching a trailing
		// underscore-delimited segment makes `auto rollback door` select
		// bathroom_light_on_door's backup and then write it back under the id
		// the user asked for — one automation's config restored over another's.
		// Underscore-suffixed ids are ordinary in real HA configs.
		{"2026-04-17T09-42-05_bathroom_light_on_door.yaml", "door", false},
		{"2026-04-17T09-42-05_bathroom_light_on_door.yaml", "on_door", false},
		{"2026-04-17T09-42-05_bathroom_light_on_door.yaml", "light_on_door", false},
		{"2026-04-17T09-42-05_bathroom_light_on_door.yaml", "bathroom_light_on_door", true},
		{"2026-04-17T09-42-05_climate_schedule.yaml", "schedule", false},

		// The id must own the whole name, not a prefix of it either.
		{"2026-04-17T09-42-05_climate_schedule_night.yaml", "climate_schedule", false},
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
	w := New(client, nil, nil, "/tmp/backups")
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
	w := New(haapi.New("http://localhost", "tok"), nil, nil, "")
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

	w := New(haapi.New("http://localhost", "tok"), ws, nil, "")
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

	w := New(haapi.New("http://localhost", "tok"), ws, nil, "")
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

// makeWriterServer creates an httptest server standing in for the companion's
// single-entry automation route — the one every write here goes through.
//
// remoteEntry is YAML TEXT, because that is what the route serves and what the
// diff compares: a stub returning HA's JSON would let a test pass against a
// Writer that had gone back to normalizing both sides through a map.
func makeWriterServer(t *testing.T, _ string, remoteEntry string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/v1/config/automation"):
			w.Header().Set("Content-Type", "application/json")
			body, _ := json.Marshal(map[string]string{
				"id":      r.URL.Query().Get("id"),
				"content": remoteEntry,
			})
			_, _ = w.Write(body)
		case r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/v1/config/automation"):
			body, _ := io.ReadAll(r.Body)
			if len(body) == 0 {
				http.Error(w, "empty body", http.StatusBadRequest)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Query().Get("dry_run") == "true" {
				_, _ = fmt.Fprint(w, `{"status":"dry_run","diff":""}`)
				return
			}
			_, _ = fmt.Fprint(w, `{"status":"applied","reloaded":true}`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/api/services/"):
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `[]`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
}

// writerFor builds a Writer whose HA client and companion client both point at
// the stub, which is what the command layer does with the real two. The WS
// client is nil throughout: validate_config is exercised by the
// TestValidateCandidate_* cases, which build their own Writer.
func writerFor(srv *httptest.Server, backupDir string) *Writer {
	return New(haapi.New(srv.URL, "tok"), nil, companion.New(srv.URL, "tok"), backupDir)
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

	w := writerFor(srv, t.TempDir())

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

	w := writerFor(srv, t.TempDir())

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

	backupDir := t.TempDir()
	w := writerFor(srv, backupDir)

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

	backupDir := t.TempDir()
	w := writerFor(srv, backupDir)

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
	// Records every path HA is asked for, so the test can prove the refusal
	// happened before any of them.
	var mu sync.Mutex
	var calls []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"alias":"Old"}`)
	}))
	defer srv.Close()

	localDir := t.TempDir()
	localFile := filepath.Join(localDir, "test_auto.yaml")
	// Invalid YAML
	if err := os.WriteFile(localFile, []byte("{ not: valid: yaml: }:"), 0o600); err != nil {
		t.Fatal(err)
	}

	backupDir := t.TempDir()
	w := writerFor(srv, backupDir)

	result, err := w.Apply(context.Background(), "test_auto", localFile, false)

	// An unparseable local file is a refusal, not a dry run: Apply has to name
	// the parse as the reason and hand back no result for the caller to print.
	if err == nil {
		t.Fatalf("Apply on unparseable YAML returned no error (result = %+v)", result)
	}
	if !strings.Contains(err.Error(), "parsing local YAML") {
		t.Errorf("error = %v, want it to name the local YAML parse", err)
	}
	if result != nil {
		t.Errorf("result = %+v, want nil alongside the error", result)
	}
	// And it must refuse before touching HA or the backup dir: a file hactl
	// cannot parse is never a reason to validate, back up or write anything.
	mu.Lock()
	got := append([]string(nil), calls...)
	mu.Unlock()
	if len(got) != 0 {
		t.Errorf("HA was called %v on an unparseable local file, want no calls", got)
	}
	entries, readErr := os.ReadDir(backupDir)
	if readErr != nil {
		t.Fatalf("reading backup dir: %v", readErr)
	}
	if len(entries) != 0 {
		t.Errorf("backup dir has %d entries after a refused apply, want 0", len(entries))
	}
}

func TestWriter_Apply_MissingFile(t *testing.T) {
	srv := makeWriterServer(t, "test_auto", `{}`)
	defer srv.Close()

	w := writerFor(srv, t.TempDir())

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

	w := writerFor(srv, backupDir)

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

	w := writerFor(srv, backupDir)

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

	w := writerFor(srv, backupDir)

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
	w := writerFor(srv, backupDir)

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

// TestWriter_Apply_BackupFailureAborts enforces H-5: a failed backup must abort
// the write, not warn and proceed. Without the backup the previous config is
// unrecoverable, so `auto rollback` would have nothing to restore — and the
// warning it used to emit is hidden whenever HACTL_LOG_LEVEL is above warn.
//
// The load-bearing assertion is that no POST reached HA: an error return alone
// would not prove the write was prevented.
func TestWriter_Apply_BackupFailureAborts(t *testing.T) {
	var posted bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/api/config/automation/config/"):
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"alias":"Old","trigger":[],"condition":[],"action":[]}`)
		case r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/api/config/automation/config/"):
			posted = true
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		default:
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()

	localFile := filepath.Join(t.TempDir(), "test_auto.yaml")
	if err := os.WriteFile(localFile, []byte("alias: New\ntrigger: []\ncondition: []\naction: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Point the backup dir at a path under a regular file so MkdirAll fails.
	blocker := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := writerFor(srv, filepath.Join(blocker, "backups"))

	_, err := w.Apply(context.Background(), "test_auto", localFile, true)
	if err == nil {
		t.Fatal("Apply succeeded despite an unwritable backup dir; want an error")
	}
	if !strings.Contains(err.Error(), "backup") {
		t.Errorf("error should name the backup as the cause, got: %v", err)
	}
	if posted {
		t.Error("Apply wrote the automation to HA even though the backup failed")
	}
}

// TestWriter_Rollback_ReportsAFailedReload — the restored config being on disk
// is not the same as Home Assistant running it.
//
// Rollback used to return Reloaded: true unconditionally, with the reload error
// going only to a WARN, so `auto rollback --confirm` printed "reload: ok" while
// HA kept running the broken configuration. Apply, in the same file, had always
// reported the field correctly; nothing compared them.
func TestWriter_Rollback_ReportsAFailedReload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPut && strings.Contains(r.URL.Path, "/v1/config/automation") {
			// The companion writes the entry and reports that HA never read it,
			// which is the whole point of the field.
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"status":"applied","reloaded":false,"reload_error":"500: reload blew up"}`)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	backupDir := t.TempDir()
	backupFile := filepath.Join(backupDir, "2026-01-01T09-00-00_test_auto.yaml")
	if err := os.WriteFile(backupFile, []byte("alias: Backup Version\ntrigger: []\ncondition: []\naction: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	result, err := writerFor(srv, backupDir).
		Rollback(context.Background(), "test_auto")
	if err != nil {
		t.Fatalf("a failed reload must not fail the rollback itself: %v", err)
	}
	if result.Reloaded {
		t.Error("Reloaded is true although the reload answered 500 — the caller prints \"reload: ok\" off this field while HA is still running the previous config")
	}
	if result.ReloadError == "" {
		t.Error("the companion said why the reload failed and the result dropped it — a bare reloaded:false sends an operator hunting for a reason hactl already had")
	}
}

// The three tests below are H-7 at the writer's seam: GET
// /api/config/automation/config/<id> and the backup file are the only wire/
// artifact documents this package decodes, and both decode into a bare
// map[string]any — no struct tag can drift, but the *document itself* can
// decode to nothing (`{}`, `null`, an empty file) without an error. A real
// automation config is never empty (HA's schema requires triggers and
// actions), so an empty decode is a changed wire shape, and rendering it
// would produce a fictitious full-file diff, a backup of nothing standing in
// for the user's only undo, or a rollback that overwrites the live config
// with an empty document.

func TestWriter_Diff_EmptyRemoteConfigIsUnparsed(t *testing.T) {
	for _, remote := range []string{`{}`, `null`} {
		t.Run(remote, func(t *testing.T) {
			srv := makeWriterServer(t, "test_auto", remote)
			defer srv.Close()

			localFile := filepath.Join(t.TempDir(), "test_auto.yaml")
			if err := os.WriteFile(localFile, []byte("alias: Test\ntrigger: []\ncondition: []\naction: []\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			_, err := writerFor(srv, t.TempDir()).
				Diff(context.Background(), "test_auto", localFile)
			if err == nil {
				t.Fatal("Diff rendered a diff against a remote config that decoded to nothing — every local line would show as an addition")
			}
			if !strings.Contains(err.Error(), degeneracy.Marker) {
				t.Errorf("error %q does not carry the %s marker the harness scans for", err, degeneracy.Marker)
			}
			if !errors.Is(err, degeneracy.ErrDegenerate) {
				t.Errorf("error is not identifiable with errors.Is(err, degeneracy.ErrDegenerate): %v", err)
			}
		})
	}
}

func TestWriter_Backup_RefusesEmptyRemoteConfig(t *testing.T) {
	srv := makeWriterServer(t, "my_auto", `null`)
	defer srv.Close()

	backupDir := t.TempDir()
	w := writerFor(srv, backupDir)

	if _, err := w.backup(context.Background(), "my_auto"); err == nil {
		t.Fatal("backup of a remote config that decoded to nothing succeeded — an empty file would stand in for the user's only undo")
	} else if !strings.Contains(err.Error(), degeneracy.Marker) {
		t.Errorf("error %q does not carry the %s marker", err, degeneracy.Marker)
	}

	entries, readErr := os.ReadDir(backupDir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("a backup file was written anyway: %v", entries)
	}
}

func TestWriter_Rollback_RefusesEmptyBackup(t *testing.T) {
	posted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/api/config/automation/config/") {
			posted = true
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	backupDir := t.TempDir()
	backupFile := filepath.Join(backupDir, "2026-01-01T09-00-00_test_auto.yaml")
	if err := os.WriteFile(backupFile, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := writerFor(srv, backupDir).
		Rollback(context.Background(), "test_auto")
	if err == nil {
		t.Fatal("Rollback restored a backup that decoded to nothing — the live config would be overwritten with an empty document")
	}
	if !strings.Contains(err.Error(), degeneracy.Marker) {
		t.Errorf("error %q does not carry the %s marker", err, degeneracy.Marker)
	}
	if posted {
		t.Error("the empty config was POSTed to HA before the guard fired — the error alone does not prove the write was prevented")
	}
}

// TestWriter_ApplyWritesTheBytesTheDiffShowed is finding #93 as a property.
//
// The old path sent the parsed config through `encoding/json.Marshal`, which
// sorts keys, and compared two `yaml.Marshal`ed maps, which sorts them too. So
// a confirmed apply alphabetized the entry's nested keys on disk —
// `(platform, entity_id, to)` becoming `(entity_id, platform, to)` — while the
// diff showed those lines as UNCHANGED, because both of its sides had been put
// through the same normalization. The tool could not see what it was doing.
//
// The assertion is the contract, not the mechanism: every line the diff called
// unchanged is a line the write leaves alone, and the bytes that land are the
// bytes the caller wrote.
func TestWriter_ApplyWritesTheBytesTheDiffShowed(t *testing.T) {
	// Deliberately NOT alphabetical, and not the order a map marshal produces.
	const remote = `id: pg_probe
alias: PG Probe
triggers:
- platform: state
  entity_id: input_boolean.pg_probe
  to: 'on'
conditions: []
actions:
- target:
    entity_id: input_boolean.pg_probe
  action: input_boolean.turn_off
mode: single
`
	local := strings.Replace(remote, "alias: PG Probe", "alias: PG Probe edited", 1)

	var mu sync.Mutex
	var written string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodGet:
			body, _ := json.Marshal(map[string]string{"id": "pg_probe", "content": remote})
			_, _ = w.Write(body)
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			if r.URL.Query().Get("dry_run") != "true" {
				mu.Lock()
				written = string(body)
				mu.Unlock()
			}
			_, _ = fmt.Fprint(w, `{"status":"applied","reloaded":true}`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer srv.Close()

	localFile := filepath.Join(t.TempDir(), "pg_probe.yaml")
	if err := os.WriteFile(localFile, []byte(local), 0o600); err != nil {
		t.Fatal(err)
	}

	wr := writerFor(srv, t.TempDir())
	diff, err := wr.Diff(context.Background(), "pg_probe", localFile)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if got := diff.ChangedLines(); got != 2 {
		t.Errorf("a one-line edit reports changed_lines = %d, want 2 (one removed, one added):\n%s",
			got, strings.Join(diff.Lines, "\n"))
	}

	if _, err := wr.Apply(context.Background(), "pg_probe", localFile, true); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	mu.Lock()
	got := written
	mu.Unlock()

	if got != local {
		t.Errorf("the write did not send the file the caller wrote.\n--- sent:\n%s\n--- file:\n%s", got, local)
	}
	// The property, stated as the diff's own claim: a line shown with a leading
	// space is a line that does not move.
	for _, line := range diff.Lines {
		if !strings.HasPrefix(line, " ") {
			continue
		}
		if unchanged := line[1:]; unchanged != "" && !strings.Contains(got, unchanged) {
			t.Errorf("the diff showed %q as unchanged and the write does not contain it", unchanged)
		}
	}
}

// TestChangedLinesCountsChangesNotContext — finding #94, at the one function
// every reporter now goes through.
func TestChangedLinesCountsChangesNotContext(t *testing.T) {
	lines := []string{
		" id: pg_probe",
		"-alias: Old",
		"+alias: New",
		" conditions: []",
		"… 5 unchanged lines …",
		" mode: single",
	}
	if got := ChangedLines(lines); got != 2 {
		t.Errorf("ChangedLines = %d, want 2 — context lines and the collapsed-run marker are not changes", got)
	}
	if !HasChanges(lines) {
		t.Error("HasChanges = false on a diff carrying a +/- pair")
	}
	if HasChanges([]string{" a", " b", "… 3 unchanged lines …"}) {
		t.Error("HasChanges = true on a diff of context only")
	}
}
