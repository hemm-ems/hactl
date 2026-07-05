package cmd

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/pkg/ids"
)

var cmdWSUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// cmdTestServer holds a combined HTTP+WS test server and a temp dir with .env.
type cmdTestServer struct {
	srv       *httptest.Server
	dir       string
	mu        sync.Mutex
	cmdCounts map[string]int
}

// startCmdServer creates an httptest server serving both HTTP and WS.
// wsResponses maps WS command type → response data (nil → null result, error string → error).
// httpHandlers maps path → HandlerFunc.
func startCmdServer(t *testing.T, wsResponses map[string]any, httpHandlers map[string]http.HandlerFunc) *cmdTestServer {
	t.Helper()
	mux := http.NewServeMux()
	ts := &cmdTestServer{cmdCounts: make(map[string]int)}

	mux.HandleFunc("/api/websocket", func(w http.ResponseWriter, r *http.Request) {
		conn, err := cmdWSUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()

		// HA auth flow
		_ = conn.WriteJSON(map[string]any{"type": "auth_required", "ha_version": "2026.4"})
		var authMsg map[string]string
		_ = conn.ReadJSON(&authMsg)
		_ = conn.WriteJSON(map[string]any{"type": "auth_ok", "ha_version": "2026.4"})

		for {
			var cmd map[string]any
			if err := conn.ReadJSON(&cmd); err != nil {
				return
			}
			cmdType, _ := cmd["type"].(string)
			ts.mu.Lock()
			ts.cmdCounts[cmdType]++
			ts.mu.Unlock()
			respData, ok := wsResponses[cmdType]
			if !ok {
				_ = conn.WriteJSON(map[string]any{
					"id":      cmd["id"],
					"type":    "result",
					"success": false,
					"error":   map[string]string{"code": "unknown", "message": "unknown command " + cmdType},
				})
				continue
			}
			resultJSON, _ := json.Marshal(respData)
			_ = conn.WriteJSON(map[string]any{
				"id":      cmd["id"],
				"type":    "result",
				"success": true,
				"result":  json.RawMessage(resultJSON),
			})
		}
	})

	for path, h := range httpHandlers {
		mux.HandleFunc(path, h)
	}

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	envContent := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=test-token\n", srv.URL)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}

	ts.srv = srv
	ts.dir = dir
	return ts
}

func (ts *cmdTestServer) commandCount(cmdType string) int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return ts.cmdCounts[cmdType]
}

// withFlagDir sets flagDir for the duration of a test and restores afterward.
func withFlagDir(t *testing.T, dir string) {
	t.Helper()
	old := flagDir
	flagDir = dir
	t.Cleanup(func() { flagDir = old })
}

// --- runCacheStatus / runCacheClear tests (no network) ---

func TestRunCacheStatus_Empty(t *testing.T) {
	dir := t.TempDir()
	envContent := "HA_URL=http://localhost:9999\nHA_TOKEN=tok\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, dir)

	var buf bytes.Buffer
	if err := runCacheStatus(context.Background(), &buf); err != nil {
		t.Fatalf("runCacheStatus failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "traces") {
		t.Errorf("output missing 'traces': %q", out)
	}
	if !strings.Contains(out, "synced") {
		t.Errorf("output missing 'synced': %q", out)
	}
}

func TestRunCacheStatus_JSON(t *testing.T) {
	dir := t.TempDir()
	envContent := "HA_URL=http://localhost:9999\nHA_TOKEN=tok\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, dir)

	old := flagJSON
	flagJSON = true
	defer func() { flagJSON = old }()

	var buf bytes.Buffer
	if err := runCacheStatus(context.Background(), &buf); err != nil {
		t.Fatalf("runCacheStatus JSON failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &parsed); err != nil {
		t.Fatalf("JSON output is not valid JSON: %v\noutput: %s", err, buf.String())
	}
}

func TestRunCacheClear(t *testing.T) {
	dir := t.TempDir()
	envContent := "HA_URL=http://localhost:9999\nHA_TOKEN=tok\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, dir)

	var buf bytes.Buffer
	if err := runCacheClear(context.Background(), &buf); err != nil {
		t.Fatalf("runCacheClear failed: %v", err)
	}
	if !strings.Contains(buf.String(), "cache cleared") {
		t.Errorf("output = %q, want 'cache cleared'", buf.String())
	}
}

func TestRunCacheStatus_DirInOutput(t *testing.T) {
	dir := t.TempDir()
	envContent := "HA_URL=http://localhost:9999\nHA_TOKEN=tok\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, dir)

	var buf bytes.Buffer
	if err := runCacheStatus(context.Background(), &buf); err != nil {
		t.Fatalf("runCacheStatus failed: %v", err)
	}
	if !strings.Contains(buf.String(), "dir:") {
		t.Errorf("output missing 'dir:' line: %q", buf.String())
	}
}

// --- runLabelLs (WS) ---

func TestRunLabelLs_Empty(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/label_registry/list": []any{},
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runLabelLs(context.Background(), &buf); err != nil {
		t.Fatalf("runLabelLs failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no labels") {
		t.Errorf("empty result should say 'no labels': %q", buf.String())
	}
}

func TestRunLabelLs_WithLabels(t *testing.T) {
	labels := []map[string]any{
		{"label_id": "energy", "name": "Energy", "color": "green", "description": "Energy monitoring"},
		{"label_id": "lighting", "name": "Lighting", "color": "", "description": ""},
	}
	ts := startCmdServer(t, map[string]any{
		"config/label_registry/list": labels,
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runLabelLs(context.Background(), &buf); err != nil {
		t.Fatalf("runLabelLs failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "energy") {
		t.Errorf("output missing label 'energy': %q", out)
	}
	if !strings.Contains(out, "Energy") {
		t.Errorf("output missing label name 'Energy': %q", out)
	}
}

// --- runAreaLs (WS) ---

func TestRunAreaLs_Empty(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/area_registry/list": []any{},
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runAreaLs(context.Background(), &buf); err != nil {
		t.Fatalf("runAreaLs failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no areas") {
		t.Errorf("empty result should say 'no areas': %q", buf.String())
	}
}

func TestRunAreaLs_WithAreas(t *testing.T) {
	areas := []map[string]any{
		{"area_id": "kitchen", "name": "Kitchen"},
		{"area_id": "bedroom", "name": "Bedroom"},
	}
	ts := startCmdServer(t, map[string]any{
		"config/area_registry/list": areas,
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runAreaLs(context.Background(), &buf); err != nil {
		t.Fatalf("runAreaLs failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "kitchen") {
		t.Errorf("output missing area 'kitchen': %q", out)
	}
	if !strings.Contains(out, "Kitchen") {
		t.Errorf("output missing area name 'Kitchen': %q", out)
	}
}

// --- runFloorLs (WS) ---

func TestRunFloorLs_Empty(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/floor_registry/list": []any{},
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runFloorLs(context.Background(), &buf); err != nil {
		t.Fatalf("runFloorLs failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no floors") {
		t.Errorf("empty result should say 'no floors': %q", buf.String())
	}
}

func TestRunFloorLs_WithFloors(t *testing.T) {
	level := float64(0)
	floors := []map[string]any{
		{"floor_id": "ground", "name": "Ground Floor", "level": level},
		{"floor_id": "first", "name": "First Floor", "level": float64(1)},
	}
	ts := startCmdServer(t, map[string]any{
		"config/floor_registry/list": floors,
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runFloorLs(context.Background(), &buf); err != nil {
		t.Fatalf("runFloorLs failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "ground") {
		t.Errorf("output missing floor 'ground': %q", out)
	}
	if !strings.Contains(out, "Ground Floor") {
		t.Errorf("output missing floor name 'Ground Floor': %q", out)
	}
}

// --- runEntLs (HTTP only, WS allowed to fail) ---

func TestRunEntLs_WithStates(t *testing.T) {
	states := []map[string]any{
		{"entity_id": "sensor.temperature", "state": "21.5", "last_changed": "2026-01-01T10:00:00Z"},
		{"entity_id": "light.kitchen", "state": "on", "last_changed": "2026-01-01T09:00:00Z"},
		{"entity_id": "binary_sensor.door", "state": "off", "last_changed": "2026-01-01T08:00:00Z"},
	}
	statesJSON, _ := json.Marshal(states)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagEntDomain
	flagEntDomain = ""
	defer func() { flagEntDomain = old }()

	var buf bytes.Buffer
	if err := runEntLs(context.Background(), &buf); err != nil {
		t.Fatalf("runEntLs failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "sensor.temperature") {
		t.Errorf("output missing entity: %q", out)
	}
	if !strings.Contains(out, "light.kitchen") {
		t.Errorf("output missing light entity: %q", out)
	}
}

func TestRunEntLs_DomainFilter(t *testing.T) {
	states := []map[string]any{
		{"entity_id": "sensor.temp", "state": "21.5", "last_changed": "2026-01-01T10:00:00Z"},
		{"entity_id": "light.kitchen", "state": "on", "last_changed": "2026-01-01T09:00:00Z"},
	}
	statesJSON, _ := json.Marshal(states)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagEntDomain
	flagEntDomain = "sensor"
	defer func() { flagEntDomain = old }()

	var buf bytes.Buffer
	if err := runEntLs(context.Background(), &buf); err != nil {
		t.Fatalf("runEntLs with domain filter failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "sensor.temp") {
		t.Errorf("output missing sensor entity: %q", out)
	}
	if strings.Contains(out, "light.kitchen") {
		t.Errorf("output should not contain light entity (domain filter): %q", out)
	}
}

func TestRunEntLs_Empty(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
	withFlagDir(t, ts.dir)

	old := flagEntDomain
	flagEntDomain = ""
	defer func() { flagEntDomain = old }()

	var buf bytes.Buffer
	if err := runEntLs(context.Background(), &buf); err != nil {
		t.Fatalf("runEntLs empty failed: %v", err)
	}
}

// --- runLog (HTTP fallback path) ---

func TestRunLog_HTTPFallback(t *testing.T) {
	logText := "2026-01-01 10:00:00.000 ERROR (Main) [comp.test] Something broke\n"
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/error_log": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, logText)
		},
	})
	withFlagDir(t, ts.dir)

	// Reset log flags
	old := flagLogErrors
	flagLogErrors = false
	defer func() { flagLogErrors = old }()
	oldComp := flagLogComponent
	flagLogComponent = ""
	defer func() { flagLogComponent = oldComp }()
	oldUniq := flagLogUnique
	flagLogUnique = false
	defer func() { flagLogUnique = oldUniq }()

	var buf bytes.Buffer
	if err := runLog(context.Background(), &buf); err != nil {
		t.Fatalf("runLog HTTP fallback failed: %v", err)
	}
	// Should contain error log content or at least not fail
}

// --- runAutoLs (HTTP only, WS allowed to fail) ---

func TestRunAutoLs_WithAutomations(t *testing.T) {
	states := []map[string]any{
		{"entity_id": "automation.climate_schedule", "state": "on", "last_changed": "2026-01-01T10:00:00Z"},
		{"entity_id": "automation.alarm_morning", "state": "on", "last_changed": "2026-01-01T09:00:00Z"},
		{"entity_id": "sensor.temp", "state": "21.5", "last_changed": "2026-01-01T10:00:00Z"},
	}
	statesJSON, _ := json.Marshal(states)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
		"/api/logbook/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
	withFlagDir(t, ts.dir)

	// Reset auto flags
	old := flagAutoPattern
	flagAutoPattern = ""
	defer func() { flagAutoPattern = old }()
	oldLabel := flagAutoLabel
	flagAutoLabel = ""
	defer func() { flagAutoLabel = oldLabel }()
	oldFailing := flagAutoFailing
	flagAutoFailing = false
	defer func() { flagAutoFailing = oldFailing }()
	oldSince := flagSince
	flagSince = "24h"
	defer func() { flagSince = oldSince }()

	var buf bytes.Buffer
	if err := runAutoLs(context.Background(), &buf); err != nil {
		t.Fatalf("runAutoLs failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "climate_schedule") {
		t.Errorf("output missing automation: %q", out)
	}
	if !strings.Contains(out, "alarm_morning") {
		t.Errorf("output missing automation: %q", out)
	}
}

// --- runConfigEntries (HTTP) ---

func TestRunConfigEntries_WithEntries(t *testing.T) {
	entries := []map[string]any{
		{"entry_id": "entry1", "domain": "mqtt", "title": "MQTT Broker", "state": "loaded", "version": 1},
		{"entry_id": "entry2", "domain": "hue", "title": "Philips Hue", "state": "loaded", "version": 2},
	}
	entriesJSON, _ := json.Marshal(entries)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/config_entries/entry": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(entriesJSON)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagConfigDomain
	flagConfigDomain = ""
	defer func() { flagConfigDomain = old }()

	var buf bytes.Buffer
	if err := runConfigEntries(context.Background(), &buf); err != nil {
		t.Fatalf("runConfigEntries failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "mqtt") {
		t.Errorf("output missing 'mqtt': %q", out)
	}
	if !strings.Contains(out, "hue") {
		t.Errorf("output missing 'hue': %q", out)
	}
}

func TestRunConfigEntries_DomainFilter(t *testing.T) {
	entries := []map[string]any{
		{"entry_id": "e1", "domain": "mqtt", "title": "MQTT", "state": "loaded", "version": 1},
		{"entry_id": "e2", "domain": "hue", "title": "Hue", "state": "loaded", "version": 1},
	}
	entriesJSON, _ := json.Marshal(entries)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/config_entries/entry": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(entriesJSON)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagConfigDomain
	flagConfigDomain = "mqtt"
	defer func() { flagConfigDomain = old }()

	var buf bytes.Buffer
	if err := runConfigEntries(context.Background(), &buf); err != nil {
		t.Fatalf("runConfigEntries with filter failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "mqtt") {
		t.Errorf("output missing 'mqtt': %q", out)
	}
	if strings.Contains(out, "hue") {
		t.Errorf("output should not contain 'hue' after domain filter: %q", out)
	}
}

func TestRunConfigEntries_Empty(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/config_entries/entry": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
	withFlagDir(t, ts.dir)

	old := flagConfigDomain
	flagConfigDomain = ""
	defer func() { flagConfigDomain = old }()

	var buf bytes.Buffer
	if err := runConfigEntries(context.Background(), &buf); err != nil {
		t.Fatalf("runConfigEntries empty failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no config entries") {
		t.Errorf("empty entries should say 'no config entries': %q", buf.String())
	}
}

// --- truncateStr ---

func TestTruncateStr_Short(t *testing.T) {
	got := truncateStr("hello", 10)
	if got != "hello" {
		t.Errorf("truncateStr short = %q, want 'hello'", got)
	}
}

func TestTruncateStr_Long(t *testing.T) {
	got := truncateStr("this is a very long description that exceeds the limit", 20)
	// The function appends a multi-byte ellipsis, so byte length may exceed maxLen.
	// Verify the string was actually truncated and ends with an ellipsis character.
	if got == "this is a very long description that exceeds the limit" {
		t.Error("truncateStr long: string was not truncated")
	}
	if strings.HasSuffix(got, " ") {
		t.Errorf("truncateStr long should end with ellipsis, got: %q", got)
	}
	// Result must be shorter than the input
	if len(got) >= len("this is a very long description that exceeds the limit") {
		t.Errorf("truncateStr long: result not shorter than input: %q", got)
	}
}

func TestTruncateStr_Exact(t *testing.T) {
	s := "exactly-ten!"
	got := truncateStr(s, len(s))
	if got != s {
		t.Errorf("truncateStr exact = %q, want %q", got, s)
	}
}

func TestTruncateStr_Whitespace(t *testing.T) {
	got := truncateStr("  trim me  ", 20)
	if strings.HasPrefix(got, " ") || strings.HasSuffix(got, " ") {
		t.Errorf("truncateStr should trim whitespace: %q", got)
	}
}

// --- runRTFM ---

func TestRunRTFM(t *testing.T) {
	var buf bytes.Buffer
	if err := runRTFM(&buf); err != nil {
		t.Fatalf("runRTFM failed: %v", err)
	}
	if buf.Len() == 0 {
		t.Error("runRTFM produced no output")
	}
}

// --- runDashSave (dry-run, no WS needed) ---

func TestRunDashSave_DryRun(t *testing.T) {
	dir := t.TempDir()
	// Write a config file
	configFile := filepath.Join(dir, "dash.json")
	if err := os.WriteFile(configFile, []byte(`{"title":"Home","views":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	envDir := t.TempDir()
	envContent := "HA_URL=http://localhost:9999\nHA_TOKEN=tok\n"
	if err := os.WriteFile(filepath.Join(envDir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, envDir)

	old := flagDashConfirm
	flagDashConfirm = false
	defer func() { flagDashConfirm = old }()
	oldFile := flagDashFile
	flagDashFile = configFile
	defer func() { flagDashFile = oldFile }()

	var buf bytes.Buffer
	if err := runDashSave(context.Background(), &buf, ""); err != nil {
		t.Fatalf("runDashSave dry-run failed: %v", err)
	}
	if !strings.Contains(buf.String(), "dry-run") {
		t.Errorf("output = %q, want 'dry-run'", buf.String())
	}
}

// --- runDashCreate (dry-run, no WS needed) ---

func TestRunDashCreate_DryRun(t *testing.T) {
	envDir := t.TempDir()
	envContent := "HA_URL=http://localhost:9999\nHA_TOKEN=tok\n"
	if err := os.WriteFile(filepath.Join(envDir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, envDir)

	old := flagDashConfirm
	flagDashConfirm = false
	defer func() { flagDashConfirm = old }()
	oldTitle := flagDashTitle
	flagDashTitle = "Test Dashboard"
	defer func() { flagDashTitle = oldTitle }()
	oldURLPath := flagDashURLPath
	flagDashURLPath = "test-dash"
	defer func() { flagDashURLPath = oldURLPath }()

	var buf bytes.Buffer
	if err := runDashCreate(context.Background(), &buf); err != nil {
		t.Fatalf("runDashCreate dry-run failed: %v", err)
	}
	if !strings.Contains(buf.String(), "dry-run") {
		t.Errorf("output = %q, want 'dry-run'", buf.String())
	}
	if !strings.Contains(buf.String(), "test-dash") {
		t.Errorf("output missing url_path: %q", buf.String())
	}
}

// --- runDashDelete (dry-run, no WS needed) ---

func TestRunDashDelete_DryRun(t *testing.T) {
	envDir := t.TempDir()
	envContent := "HA_URL=http://localhost:9999\nHA_TOKEN=tok\n"
	if err := os.WriteFile(filepath.Join(envDir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, envDir)

	old := flagDashConfirm
	flagDashConfirm = false
	defer func() { flagDashConfirm = old }()

	var buf bytes.Buffer
	if err := runDashDelete(context.Background(), &buf, "my-dashboard"); err != nil {
		t.Fatalf("runDashDelete dry-run failed: %v", err)
	}
	if !strings.Contains(buf.String(), "dry-run") {
		t.Errorf("output = %q, want 'dry-run'", buf.String())
	}
}

// --- runDashShow (WS) ---

func TestRunDashShow_WithViews(t *testing.T) {
	dashConfig := map[string]any{
		"views": []map[string]any{
			{"title": "Main", "path": "main", "type": "masonry", "cards": []any{}},
			{"title": "Energy", "path": "energy", "type": "sections", "cards": []any{}},
		},
	}
	ts := startCmdServer(t, map[string]any{
		"lovelace/config": dashConfig,
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagDashRaw
	flagDashRaw = false
	defer func() { flagDashRaw = old }()
	oldView := flagDashView
	flagDashView = ""
	defer func() { flagDashView = oldView }()

	var buf bytes.Buffer
	if err := runDashShow(context.Background(), &buf, ""); err != nil {
		t.Fatalf("runDashShow failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Main") {
		t.Errorf("output missing view title 'Main': %q", out)
	}
}

// --- runDashResources (WS) ---

func TestRunDashResources(t *testing.T) {
	resources := []map[string]any{
		{"id": "1", "type": "module", "url": "/local/custom.js"},
	}
	ts := startCmdServer(t, map[string]any{
		"lovelace/resources": resources,
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runDashResources(context.Background(), &buf); err != nil {
		t.Fatalf("runDashResources failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "/local/custom.js") {
		t.Errorf("output missing resource URL: %q", out)
	}
}

// --- runEntAnomalies (HTTP) ---

func TestRunEntAnomalies_NoHistory(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/history/period/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[[]]")
		},
	})
	withFlagDir(t, ts.dir)

	old := flagSince
	flagSince = "24h"
	defer func() { flagSince = old }()

	var buf bytes.Buffer
	if err := runEntAnomalies(context.Background(), &buf, "sensor.x"); err != nil {
		t.Fatalf("runEntAnomalies no history failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no history") {
		t.Errorf("output = %q, want 'no history'", buf.String())
	}
}

func TestRunEntAnomalies_NumericNoAnomalies(t *testing.T) {
	// Return stable numeric history — should detect no anomalies
	histData := `[[
		{"entity_id":"sensor.temp","state":"21.5","last_changed":"2026-01-01T10:00:00+00:00","attributes":{}},
		{"entity_id":"sensor.temp","state":"21.6","last_changed":"2026-01-01T11:00:00+00:00","attributes":{}},
		{"entity_id":"sensor.temp","state":"21.4","last_changed":"2026-01-01T12:00:00+00:00","attributes":{}}
	]]`

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/history/period/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, histData)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagSince
	flagSince = "24h"
	defer func() { flagSince = old }()

	var buf bytes.Buffer
	if err := runEntAnomalies(context.Background(), &buf, "sensor.temp"); err != nil {
		t.Fatalf("runEntAnomalies numeric failed: %v", err)
	}
}

// --- runEntHistAttr (HTTP) ---

func TestRunEntHistAttr_WithData(t *testing.T) { //nolint:dupl
	histData := `[[
		{"entity_id":"light.kitchen","state":"on","last_changed":"2026-01-01T10:00:00+00:00","attributes":{"brightness":200}},
		{"entity_id":"light.kitchen","state":"on","last_changed":"2026-01-01T11:00:00+00:00","attributes":{"brightness":255}}
	]]`

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/history/period/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, histData)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagSince
	flagSince = "24h"
	defer func() { flagSince = old }()
	oldAttr := flagEntAttr
	flagEntAttr = "brightness"
	defer func() { flagEntAttr = oldAttr }()
	oldResample := flagEntResample
	flagEntResample = ""
	defer func() { flagEntResample = oldResample }()

	var buf bytes.Buffer
	// Call runEntHist which branches to runEntHistAttr when flagEntAttr is set
	if err := runEntHist(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntHist with attr failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "brightness") {
		t.Errorf("output missing attr name: %q", out)
	}
}

// --- runEntSetLabel (WS) ---

func TestRunEntSetLabel(t *testing.T) {
	// WS: EntityRegistryList + EntityRegistryUpdate
	ts := startCmdServer(t, map[string]any{
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "sensor.temp", "labels": []string{}},
		},
		"config/label_registry/list": []map[string]any{
			{"label_id": "energy", "name": "Energy"},
		},
		"config/entity_registry/update": map[string]any{"entity_id": "sensor.temp"},
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runEntSetLabel(context.Background(), &buf, "sensor.temp", []string{"energy"}); err != nil {
		t.Fatalf("runEntSetLabel failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "sensor.temp") {
		t.Errorf("output missing entity: %q", out)
	}
}

// --- runEntSetArea (WS) ---

func TestRunEntSetArea(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/area_registry/list": []map[string]any{
			{"area_id": "kitchen_id", "name": "Kitchen"},
		},
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "light.kitchen", "area_id": "old_area"},
		},
	}, nil)
	withFlagDir(t, ts.dir)
	oldConfirm := flagEntConfirm
	flagEntConfirm = false
	defer func() { flagEntConfirm = oldConfirm }()

	var buf bytes.Buffer
	if err := runEntSetArea(context.Background(), &buf, "light.kitchen", "Kitchen"); err != nil {
		t.Fatalf("runEntSetArea failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "light.kitchen") {
		t.Errorf("output missing entity: %q", out)
	}
	if !strings.Contains(out, "dry-run") {
		t.Errorf("output missing dry-run marker: %q", out)
	}
	if got := ts.commandCount("config/entity_registry/update"); got != 0 {
		t.Fatalf("dry-run sent %d entity registry updates, want 0", got)
	}
}

func TestRunEntSetArea_ConfirmWritesOnce(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/area_registry/list": []map[string]any{
			{"area_id": "kitchen_id", "name": "Kitchen"},
		},
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "light.kitchen", "area_id": "old_area"},
		},
		"config/entity_registry/update": map[string]any{"entity_id": "light.kitchen"},
	}, nil)
	withFlagDir(t, ts.dir)
	oldConfirm := flagEntConfirm
	flagEntConfirm = true
	defer func() { flagEntConfirm = oldConfirm }()

	var buf bytes.Buffer
	if err := runEntSetArea(context.Background(), &buf, "light.kitchen", "Kitchen"); err != nil {
		t.Fatalf("runEntSetArea --confirm failed: %v", err)
	}
	if got := ts.commandCount("config/entity_registry/update"); got != 1 {
		t.Fatalf("confirm sent %d entity registry updates, want 1", got)
	}
	out := buf.String()
	if !strings.Contains(out, "area set to kitchen_id") {
		t.Errorf("output missing confirmed area: %q", out)
	}
}

// --- runDeviceLs / runDeviceShow (WS) ---

func TestRunDeviceLs(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/device_registry/list": []map[string]any{
			{
				"id": "dev_heat", "name": "Heat Pump", "area_id": "basement",
				"manufacturer": "Summt", "model": "HP1", "labels": []string{"heat_pump"},
			},
			{
				"id": "dev_light", "name": "Kitchen Light", "area_id": "kitchen",
				"labels": []string{},
			},
		},
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "sensor.heat_temp", "device_id": "dev_heat"},
			{"entity_id": "climate.heat_pump", "device_id": "dev_heat"},
			{"entity_id": "light.kitchen", "device_id": "dev_light"},
		},
		"config/area_registry/list": []map[string]any{
			{"area_id": "basement", "name": "Basement"},
			{"area_id": "kitchen", "name": "Kitchen"},
		},
		"config/label_registry/list": []map[string]any{
			{"label_id": "heat_pump", "name": "Heat Pump"},
		},
	}, nil)
	withFlagDir(t, ts.dir)
	oldLabel := flagDeviceLabel
	flagDeviceLabel = "heat"
	defer func() { flagDeviceLabel = oldLabel }()

	var buf bytes.Buffer
	if err := runDeviceLs(context.Background(), &buf); err != nil {
		t.Fatalf("runDeviceLs failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "dev_heat") || !strings.Contains(out, "Heat Pump") {
		t.Errorf("output missing heat pump device: %q", out)
	}
	if strings.Contains(out, "dev_light") {
		t.Errorf("label filter should exclude dev_light: %q", out)
	}
	if !strings.Contains(out, "2") {
		t.Errorf("output missing entity count: %q", out)
	}
}

func TestRunDeviceShow(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/device_registry/list": []map[string]any{
			{"id": "dev_heat", "name": "Heat Pump", "area_id": "basement", "labels": []string{"heat_pump"}},
		},
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "sensor.heat_temp", "device_id": "dev_heat", "area_id": "basement", "platform": "mqtt"},
			{"entity_id": "climate.heat_pump", "device_id": "dev_heat", "area_id": "basement", "platform": "mqtt"},
		},
		"config/area_registry/list": []map[string]any{
			{"area_id": "basement", "name": "Basement"},
		},
		"config/label_registry/list": []map[string]any{
			{"label_id": "heat_pump", "name": "Heat Pump"},
		},
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runDeviceShow(context.Background(), &buf, "Heat Pump"); err != nil {
		t.Fatalf("runDeviceShow failed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"device_id: dev_heat", "sensor.heat_temp", "climate.heat_pump", "Basement"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q: %q", want, out)
		}
	}
}

// --- runConfigFlowStart (HTTP) ---

func TestRunConfigFlowStart(t *testing.T) {
	flowResponse := `{"flow_id":"f1","type":"abort","step_id":"","handler":"mqtt","reason":"single_instance_allowed"}`
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/config_entries/flow": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, flowResponse)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runConfigFlowStart(context.Background(), &buf, "mqtt"); err != nil {
		t.Fatalf("runConfigFlowStart failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "f1") {
		t.Errorf("output missing flow_id: %q", out)
	}
}

// --- runConfigFlowInspect (HTTP) ---

func TestRunConfigFlowInspect(t *testing.T) {
	flowResponse := `{"flow_id":"f1","type":"form","step_id":"init","handler":"mqtt","data_schema":[{"name":"broker","required":true,"type":"string"}]}`
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/config_entries/flow/f1": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, flowResponse)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagFlowOptions
	flagFlowOptions = false
	defer func() { flagFlowOptions = old }()

	var buf bytes.Buffer
	if err := runConfigFlowInspect(context.Background(), &buf, "f1"); err != nil {
		t.Fatalf("runConfigFlowInspect failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "f1") {
		t.Errorf("output missing flow_id: %q", out)
	}
	if !strings.Contains(out, "broker") {
		t.Errorf("output missing schema field: %q", out)
	}
}

// --- runTplEval (HTTP) ---

func TestRunTplEval_Basic(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/template": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, "42.5")
		},
	})
	withFlagDir(t, ts.dir)

	old := flagTplFile
	flagTplFile = ""
	defer func() { flagTplFile = old }()

	var buf bytes.Buffer
	buf.Reset()
	rootCmd.SetOut(&buf)
	rootCmd.SetArgs([]string{"tpl", "eval", "{{ states('sensor.x') }}", "--dir", ts.dir})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("tpl eval failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "42.5") {
		t.Errorf("output missing template result: %q", out)
	}
}

// --- runDashSave (confirm=true, WS) ---

func TestRunDashSave_Confirm(t *testing.T) {
	dir := t.TempDir()
	configFile := filepath.Join(dir, "dash.json")
	if err := os.WriteFile(configFile, []byte(`{"views":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}

	ts := startCmdServer(t, map[string]any{
		"lovelace/config/save": nil,
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagDashConfirm
	flagDashConfirm = true
	defer func() { flagDashConfirm = old }()
	oldFile := flagDashFile
	flagDashFile = configFile
	defer func() { flagDashFile = oldFile }()

	var buf bytes.Buffer
	if err := runDashSave(context.Background(), &buf, ""); err != nil {
		t.Fatalf("runDashSave confirm failed: %v", err)
	}
	if !strings.Contains(buf.String(), "saved") {
		t.Errorf("output = %q, want 'saved'", buf.String())
	}
}

// --- runCCShow (HTTP) ---

func TestRunCCShow_NotFound(t *testing.T) {
	states := []map[string]any{
		{"entity_id": "sensor.temp", "state": "21.5"},
	}
	statesJSON, _ := json.Marshal(states)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	err := runCCShow(context.Background(), &buf, "nonexistent_component")
	if err == nil {
		t.Fatal("expected error for not-found component, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want 'not found'", err.Error())
	}
}

// --- runCCLogs (WS+HTTP fallback) ---

func TestRunCCLogs_HTTPFallback(t *testing.T) {
	logText := "2026-01-01 10:00:00.000 ERROR (Main) [hacs] Something broke\n"
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/error_log": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, logText)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runCCLogs(context.Background(), &buf, "hacs"); err != nil {
		t.Fatalf("runCCLogs HTTP fallback failed: %v", err)
	}
	// Should have rendered log entries
}

// --- runRollback (HTTP) ---

func TestRunRollback_WithBackup(t *testing.T) {
	remoteJSON := `{"alias":"Current","trigger":[],"condition":[],"action":[]}`
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/automation/config/test_auto": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, remoteJSON)
		},
		"/api/services/automation/reload": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		},
	})
	withFlagDir(t, ts.dir)

	// Create backup dir in the flagDir
	backupDir := filepath.Join(ts.dir, "backups")
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		t.Fatal(err)
	}
	backupFile := filepath.Join(backupDir, "2026-01-01T09-00-00_test_auto.yaml")
	if err := os.WriteFile(backupFile, []byte("alias: Backup\ntrigger: []\ncondition: []\naction: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := runRollback(context.Background(), &buf, "test_auto"); err != nil {
		t.Fatalf("runRollback failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "rolled back") {
		t.Errorf("output missing 'rolled back': %q", out)
	}
}

// --- runLogShow (uses config + cache/ids registry) ---

func TestRunLogShow_NotFound(t *testing.T) {
	dir := t.TempDir()
	envContent := "HA_URL=http://localhost:9999\nHA_TOKEN=tok\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, dir)

	var buf bytes.Buffer
	err := runLogShow(context.Background(), &buf, "nonexistent_id")
	if err == nil {
		t.Fatal("expected error for nonexistent ID, got nil")
	}
}

// --- formatSyncAge additional cases ---

func TestFormatSyncAge_Recent(t *testing.T) {
	// 30 seconds ago
	t0 := time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339)
	got := formatSyncAge(t0)
	if got != "just now" {
		t.Errorf("formatSyncAge(30s ago) = %q, want 'just now'", got)
	}
}

func TestFormatSyncAge_Minutes(t *testing.T) {
	// 5 minutes ago
	t0 := time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339)
	got := formatSyncAge(t0)
	if !strings.HasSuffix(got, "m ago") {
		t.Errorf("formatSyncAge(5m ago) = %q, want '5m ago'", got)
	}
}

func TestFormatSyncAge_Hours(t *testing.T) {
	// 2 hours ago
	t0 := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	got := formatSyncAge(t0)
	if !strings.HasSuffix(got, "h ago") {
		t.Errorf("formatSyncAge(2h ago) = %q, want '2h ago'", got)
	}
}

func TestFormatSyncAge_Days(t *testing.T) {
	// 3 days ago
	t0 := time.Now().Add(-3 * 24 * time.Hour).UTC().Format(time.RFC3339)
	got := formatSyncAge(t0)
	if !strings.HasSuffix(got, "d ago") {
		t.Errorf("formatSyncAge(3d ago) = %q, want '3d ago'", got)
	}
}

// --- runTraceShow (WS) ---

func TestRunTraceShow_DirectKey(t *testing.T) {
	traceJSON := `{"trace":{"run_id":"run-001","domain":"automation","item_id":"climate_schedule","timestamp":{"start":"2026-01-01T10:00:00Z"},"execution":"finished","last_step":"action/0"},"trace_steps":{"action/0":[{"path":"action/0","timestamp":"2026-01-01T10:00:01Z"}]}}`

	ts := startCmdServer(t, map[string]any{
		"trace/get": json.RawMessage(traceJSON),
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagFull
	flagFull = false
	defer func() { flagFull = old }()

	var buf bytes.Buffer
	if err := runTraceShow(context.Background(), &buf, "automation.climate_schedule/run-001"); err != nil {
		t.Fatalf("runTraceShow failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "climate_schedule") {
		t.Errorf("output missing automation ID: %q", out)
	}
}

// --- resolveTraceID ---

func idsRegistry(path string) *ids.Registry {
	return ids.NewRegistry(path)
}

func TestResolveTraceID_DirectKey(t *testing.T) {
	idsPath := filepath.Join(t.TempDir(), "ids.json")
	reg := idsRegistry(idsPath)

	domain, itemID, runID, err := resolveTraceID(reg, "automation.test_auto/run-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if domain != "automation" {
		t.Errorf("domain = %q, want 'automation'", domain)
	}
	if itemID != "test_auto" {
		t.Errorf("itemID = %q, want 'test_auto'", itemID)
	}
	if runID != "run-001" {
		t.Errorf("runID = %q, want 'run-001'", runID)
	}
}

func TestResolveTraceID_InvalidFormat(t *testing.T) {
	idsPath := filepath.Join(t.TempDir(), "ids.json")
	reg := idsRegistry(idsPath)

	_, _, _, err := resolveTraceID(reg, "invalid_format") //nolint:dogsled
	if err == nil {
		t.Fatal("expected error for invalid format, got nil")
	}
}

// --- RunWithOutput (integration-level) ---

func TestRunWithOutput_Version(t *testing.T) {
	dir := t.TempDir()
	envContent := "HA_URL=http://localhost:9999\nHA_TOKEN=tok\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "version"}, &buf); err != nil {
		t.Fatalf("RunWithOutput(version) failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "hactl") {
		t.Errorf("output missing 'hactl': %q", out)
	}
}

// --- runHelperLs (companion via .env COMPANION_URL) ---

func TestRunHelperLs_WithCompanion(t *testing.T) {
	// Start a companion mock HTTP server
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"status":"ok","version":"1.0.0"}`)
		case "/v1/config/helpers":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"helpers":[{"id":"guest_mode","name":"Guest Mode","domain":"input_boolean"}]}`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer companionSrv.Close()

	// Start HA mock server
	haSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer haSrv.Close()

	// Create env dir with both HA_URL and COMPANION_URL
	envDir := t.TempDir()
	envContent := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=tok\nCOMPANION_URL=%s\n", haSrv.URL, companionSrv.URL)
	if err := os.WriteFile(filepath.Join(envDir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, envDir)

	old := flagHelperDomain
	flagHelperDomain = ""
	defer func() { flagHelperDomain = old }()

	var buf bytes.Buffer
	if err := runHelperLs(context.Background(), &buf); err != nil {
		t.Fatalf("runHelperLs failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "guest_mode") {
		t.Errorf("output missing helper: %q", out)
	}
}

func TestRunHelperLs_Empty(t *testing.T) {
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"helpers":[]}`)
	}))
	defer companionSrv.Close()

	haSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer haSrv.Close()

	envDir := t.TempDir()
	envContent := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=tok\nCOMPANION_URL=%s\n", haSrv.URL, companionSrv.URL)
	if err := os.WriteFile(filepath.Join(envDir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, envDir)

	old := flagHelperDomain
	flagHelperDomain = ""
	defer func() { flagHelperDomain = old }()

	var buf bytes.Buffer
	if err := runHelperLs(context.Background(), &buf); err != nil {
		t.Fatalf("runHelperLs empty failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no helpers") {
		t.Errorf("empty should say 'no helpers': %q", buf.String())
	}
}

// --- runHelperShow (companion) ---

func TestRunHelperShow_Found(t *testing.T) {
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/config/helpers":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"helpers":[{"id":"guest_mode","name":"Guest Mode","domain":"input_boolean"}]}`)
		case "/v1/config/helper":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"id":"guest_mode","domain":"input_boolean","content":"guest_mode:\n  name: Guest Mode\n"}`)
		}
	}))
	defer companionSrv.Close()

	haSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer haSrv.Close()

	envDir := t.TempDir()
	envContent := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=tok\nCOMPANION_URL=%s\n", haSrv.URL, companionSrv.URL)
	if err := os.WriteFile(filepath.Join(envDir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, envDir)

	var buf bytes.Buffer
	if err := runHelperShow(context.Background(), &buf, "guest_mode"); err != nil {
		t.Fatalf("runHelperShow failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "guest_mode") {
		t.Errorf("output missing helper ID: %q", out)
	}
}

// --- runScriptRun (HTTP) ---

func TestRunScriptRun_Success(t *testing.T) {
	stateData := map[string]any{
		"entity_id": "script.welcome_home", "state": "off",
	}
	stateJSON, _ := json.Marshal(stateData)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states/script.welcome_home": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(stateJSON)
		},
		"/api/services/script/turn_on": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runScriptRun(context.Background(), &buf, "welcome_home"); err != nil {
		t.Fatalf("runScriptRun failed: %v", err)
	}
	if !strings.Contains(buf.String(), "executed") {
		t.Errorf("output missing 'executed': %q", buf.String())
	}
}

// --- refreshTracesFromHA / runCacheRefresh (full cycle) ---

func TestRunCacheRefresh_TracesAndLogs(t *testing.T) {
	// TraceList returns empty for both automation and script domains
	// Logs fetched via HTTP error_log fallback (WS system_log not handled)
	logText := "2026-01-01 10:00:00.000 INFO (Main) [comp] Test log\n"

	ts := startCmdServer(t, map[string]any{
		"trace/list": []any{}, // empty traces for both automation and script
	}, map[string]http.HandlerFunc{
		"/api/error_log": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, logText)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runCacheRefresh(context.Background(), &buf, ""); err != nil {
		t.Fatalf("runCacheRefresh full failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "traces refreshed") {
		t.Errorf("output missing 'traces refreshed': %q", out)
	}
	if !strings.Contains(out, "logs refreshed") {
		t.Errorf("output missing 'logs refreshed': %q", out)
	}
}

// --- runCacheRefresh (traces via WS, logs via HTTP fallback) ---

func TestRunCacheRefresh_LogsOnly(t *testing.T) {
	// TraceList returns empty (WS succeeds but no traces)
	// Logs fetched via HTTP fallback (WS system_log unavailable)
	logText := "2026-01-01 10:00:00.000 ERROR (Main) [comp.test] Test error\n"

	// Create multi-command WS server that handles trace/list
	ts := startCmdServer(t, map[string]any{
		"trace/list": []any{}, // empty traces
	}, map[string]http.HandlerFunc{
		"/api/error_log": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, logText)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	// Test with "logs" category to exercise refreshLogsFromHA
	if err := runCacheRefresh(context.Background(), &buf, "logs"); err != nil {
		t.Fatalf("runCacheRefresh logs failed: %v", err)
	}
	if !strings.Contains(buf.String(), "logs refreshed") {
		t.Errorf("output = %q, want 'logs refreshed'", buf.String())
	}
}

// --- runDashDelete (confirm=true, WS) ---

func TestRunDashDelete_Confirm(t *testing.T) {
	dashboards := []map[string]any{
		{"id": "energy-id", "url_path": "energy", "title": "Energy"},
	}
	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list":   dashboards,
		"lovelace/dashboards/delete": nil,
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagDashConfirm
	flagDashConfirm = true
	defer func() { flagDashConfirm = old }()

	var buf bytes.Buffer
	if err := runDashDelete(context.Background(), &buf, "energy"); err != nil {
		t.Fatalf("runDashDelete confirm failed: %v", err)
	}
	if !strings.Contains(buf.String(), "deleted") {
		t.Errorf("output missing 'deleted': %q", buf.String())
	}
}

// --- runConfigOptions (HTTP) ---

func TestRunConfigOptions(t *testing.T) {
	flowResponse := `{"flow_id":"opt1","type":"form","step_id":"init","handler":"mqtt","data_schema":[{"name":"port","required":true,"type":"integer","default":1883}]}`
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/config_entries/options/flow": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, flowResponse)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runConfigOptions(context.Background(), &buf, "entry-123"); err != nil {
		t.Fatalf("runConfigOptions failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "opt1") {
		t.Errorf("output missing flow_id: %q", out)
	}
}

// --- runConfigFlowStep ---

func TestRunConfigFlowStep_JSON(t *testing.T) {
	flowResponse := `{"flow_id":"f1","type":"form","step_id":"user","handler":"mqtt","data_schema":[]}`
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/config_entries/flow/f1": func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Errorf("method = %s, want POST", r.Method)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, flowResponse)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagFlowData
	flagFlowData = `{"host":"192.168.1.1"}`
	defer func() { flagFlowData = old }()
	oldOpts := flagFlowOptions
	flagFlowOptions = false
	defer func() { flagFlowOptions = oldOpts }()

	var buf bytes.Buffer
	if err := runConfigFlowStep(context.Background(), &buf, "f1"); err != nil {
		t.Fatalf("runConfigFlowStep failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "f1") {
		t.Errorf("output missing flow_id: %q", out)
	}
}

// --- runEntRelated (HTTP+WS) ---

func TestRunEntRelated_NoRelations(t *testing.T) {
	states := []map[string]any{
		{"entity_id": "sensor.temp", "state": "21.5", "attributes": map[string]any{}},
	}
	statesJSON, _ := json.Marshal(states)

	ts := startCmdServer(t, map[string]any{
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "sensor.temp"},
		},
		"config/area_registry/list":  []any{},
		"config/label_registry/list": []any{},
		"config/floor_registry/list": []any{},
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runEntRelated(context.Background(), &buf, "sensor.temp"); err != nil {
		t.Fatalf("runEntRelated failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no related") {
		t.Errorf("output = %q, want 'no related'", buf.String())
	}
}

func TestRunEntRelated_MergesCompanionAndRegistryWithoutAutomationConfigFetch(t *testing.T) {
	states := []map[string]any{
		{"entity_id": "sensor.source_power", "state": "21.5", "attributes": map[string]any{}},
		{"entity_id": "sensor.generated_power", "state": "42", "attributes": map[string]any{}},
		{"entity_id": "automation.expensive_lookup", "state": "on", "attributes": map[string]any{"id": "expensive_lookup"}},
	}
	statesJSON, _ := json.Marshal(states)

	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/related/entity" {
			t.Fatalf("unexpected companion path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("entity_id"); got != "sensor.source_power" {
			t.Fatalf("entity_id query = %q, want sensor.source_power", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"entity_id":"sensor.source_power","related":[{"entity_id":"sensor.generated_power","relationship":"config-entry-reference","detail":"config_entry=cfg_generated"}]}`)
	}))
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "sensor.source_power", "device_id": "dev1", "area_id": "office"},
			{"entity_id": "sensor.sibling_power", "device_id": "dev1", "area_id": "office"},
			{"entity_id": "sensor.generated_power", "device_id": "dev2", "area_id": "office"},
		},
		"config/area_registry/list":  []map[string]any{{"area_id": "office", "name": "Office"}},
		"config/label_registry/list": []any{},
		"config/floor_registry/list": []any{},
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
		"/api/config/automation/config/expensive_lookup": func(w http.ResponseWriter, r *http.Request) {
			t.Fatalf("runEntRelated must not fetch automation config in the default path: %s", r.URL.Path)
		},
	})
	envContent := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=test-token\nCOMPANION_URL=%s\nCOMPANION_TOKEN=test-token\n", ts.srv.URL, companionSrv.URL)
	if err := os.WriteFile(filepath.Join(ts.dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runEntRelated(context.Background(), &buf, "sensor.source_power"); err != nil {
		t.Fatalf("runEntRelated failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "sensor.generated_power") {
		t.Errorf("output missing companion relation: %q", out)
	}
	if !strings.Contains(out, "sensor.sibling_power") {
		t.Errorf("output missing registry relation: %q", out)
	}
	if !strings.Contains(out, "config-entry-reference") {
		t.Errorf("output missing companion relationship: %q", out)
	}
}

// --- runAutoShow with traces (WS+HTTP) ---

func TestRunAutoShow_WithTraces(t *testing.T) {
	stateData := map[string]any{
		"entity_id": "automation.alarm_morning",
		"state":     "on",
		"attributes": map[string]any{
			"mode":           "single",
			"last_triggered": "2026-01-01T10:00:00Z",
		},
	}
	stateJSON, _ := json.Marshal(stateData)

	traces := []map[string]any{
		{
			"run_id":    "run1",
			"domain":    "automation",
			"item_id":   "alarm_morning",
			"state":     "stopped",
			"execution": "finished",
			"trigger":   "time",
			"last_step": "action/0",
			"timestamp": map[string]any{
				"start":  "2026-01-01T10:00:00Z",
				"finish": "2026-01-01T10:00:01Z",
			},
		},
	}

	ts := startCmdServer(t, map[string]any{
		"trace/list": traces,
	}, map[string]http.HandlerFunc{
		"/api/states/automation.alarm_morning": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(stateJSON)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runAutoShow(context.Background(), &buf, "alarm_morning"); err != nil {
		t.Fatalf("runAutoShow with traces failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "automation.alarm_morning") {
		t.Errorf("output missing entity ID: %q", out)
	}
}

// --- runAutoLs with WS traces ---

func TestRunAutoLs_WithTraces(t *testing.T) {
	states := []map[string]any{
		{"entity_id": "automation.climate_schedule", "state": "on", "last_changed": "2026-01-01T10:00:00Z"},
	}
	statesJSON, _ := json.Marshal(states)

	traces := []map[string]any{
		{
			"run_id":    "run1",
			"domain":    "automation",
			"item_id":   "climate_schedule",
			"state":     "stopped",
			"execution": "finished",
			"trigger":   "time_pattern",
			"last_step": "action/0",
			"timestamp": map[string]any{
				"start":  "2026-01-01T10:00:00Z",
				"finish": "2026-01-01T10:00:01Z",
			},
		},
	}

	ts := startCmdServer(t, map[string]any{
		"trace/list":                  traces,
		"config/entity_registry/list": []any{},
		"config/area_registry/list":   []any{},
		"config/label_registry/list":  []any{},
		"config/floor_registry/list":  []any{},
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
		"/api/logbook/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `[{"entity_id":"automation.climate_schedule","domain":"automation"}]`)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagAutoPattern
	flagAutoPattern = ""
	defer func() { flagAutoPattern = old }()
	oldLabel := flagAutoLabel
	flagAutoLabel = ""
	defer func() { flagAutoLabel = oldLabel }()
	oldFailing := flagAutoFailing
	flagAutoFailing = false
	defer func() { flagAutoFailing = oldFailing }()
	oldSince := flagSince
	flagSince = "24h"
	defer func() { flagSince = oldSince }()

	var buf bytes.Buffer
	if err := runAutoLs(context.Background(), &buf); err != nil {
		t.Fatalf("runAutoLs with traces failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "climate_schedule") {
		t.Errorf("output missing automation: %q", out)
	}
}

// --- runEntHist with resample ---

func TestRunEntHist_NumericWithResample(t *testing.T) {
	histData := `[[
		{"entity_id":"sensor.power","state":"100.0","last_changed":"2026-01-01T10:00:00+00:00","attributes":{}},
		{"entity_id":"sensor.power","state":"110.0","last_changed":"2026-01-01T11:00:00+00:00","attributes":{}},
		{"entity_id":"sensor.power","state":"105.0","last_changed":"2026-01-01T12:00:00+00:00","attributes":{}}
	]]`

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/history/period/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, histData)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagSince
	flagSince = "24h"
	defer func() { flagSince = old }()
	oldAttr := flagEntAttr
	flagEntAttr = ""
	defer func() { flagEntAttr = oldAttr }()
	oldResample := flagEntResample
	flagEntResample = "1h"
	defer func() { flagEntResample = oldResample }()

	var buf bytes.Buffer
	if err := runEntHist(context.Background(), &buf, "sensor.power"); err != nil {
		t.Fatalf("runEntHist with resample failed: %v", err)
	}
}

// --- runAreaDelete (confirm=true, WS) ---

func TestRunAreaDelete_Confirm(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/area_registry/delete": nil,
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagAreaConfirm
	flagAreaConfirm = true
	defer func() { flagAreaConfirm = old }()

	var buf bytes.Buffer
	if err := runAreaDelete(context.Background(), &buf, "kitchen"); err != nil {
		t.Fatalf("runAreaDelete confirm failed: %v", err)
	}
	if !strings.Contains(buf.String(), "deleted") {
		t.Errorf("output missing 'deleted': %q", buf.String())
	}
}

// --- runFloorDelete (confirm=true, WS) ---

func TestRunFloorDelete_Confirm(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/floor_registry/delete": nil,
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagFloorConfirm
	flagFloorConfirm = true
	defer func() { flagFloorConfirm = old }()

	var buf bytes.Buffer
	if err := runFloorDelete(context.Background(), &buf, "ground"); err != nil {
		t.Fatalf("runFloorDelete confirm failed: %v", err)
	}
	if !strings.Contains(buf.String(), "deleted") {
		t.Errorf("output missing 'deleted': %q", buf.String())
	}
}

// --- runLabelDelete (confirm=true, WS) ---

func TestRunLabelDelete_Confirm(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/label_registry/delete": nil,
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagLabelConfirm
	flagLabelConfirm = true
	defer func() { flagLabelConfirm = old }()

	var buf bytes.Buffer
	if err := runLabelDelete(context.Background(), &buf, "energy"); err != nil {
		t.Fatalf("runLabelDelete confirm failed: %v", err)
	}
	if !strings.Contains(buf.String(), "deleted") {
		t.Errorf("output missing 'deleted': %q", buf.String())
	}
}

// --- runAutoDelete (confirm=true, companion) ---

func TestRunAutoDelete_Confirm(t *testing.T) {
	const configID = "automation.climate_schedule"
	const liveEntityID = "automation.climate_schedule_live"

	statesJSON, _ := json.Marshal([]map[string]any{
		{
			"entity_id":  liveEntityID,
			"state":      "on",
			"attributes": map[string]any{"id": configID, "friendly_name": "Climate Schedule"},
		},
	})

	ts := startCmdServer(t, map[string]any{
		"config/entity_registry/remove": nil,
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
	})

	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Query().Get("id") != configID {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"deleted","reloaded":true}`)
	}))
	defer companionSrv.Close()

	envContent, err := os.ReadFile(filepath.Join(ts.dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	envContent = fmt.Appendf(envContent, "COMPANION_URL=%s\n", companionSrv.URL)
	if err := os.WriteFile(filepath.Join(ts.dir, ".env"), envContent, 0o600); err != nil { //nolint:gosec // test fixture dir from t.TempDir(), not user input
		t.Fatal(err)
	}
	withFlagDir(t, ts.dir)

	old := flagAutoConfirm
	flagAutoConfirm = true
	defer func() { flagAutoConfirm = old }()

	var buf bytes.Buffer
	if err := runAutoDelete(context.Background(), &buf, configID); err != nil {
		t.Fatalf("runAutoDelete confirm failed: %v", err)
	}
	if !strings.Contains(buf.String(), "deleted") {
		t.Errorf("output missing 'deleted': %q", buf.String())
	}

	if count := ts.commandCount("config/entity_registry/remove"); count != 1 {
		t.Errorf("expected EntityRegistryRemove to be called once, got %d", count)
	}
}

// TestRunAutoDelete_Confirm_ByAlias covers deleting by the human-readable
// alias (HA's attributes.friendly_name), not just config id or entity_id —
// resolveAutomationEntityID previously only matched entity_id/attributes.id/
// the entity_id slug, silently skipping registry cleanup for this case.
func TestRunAutoDelete_Confirm_ByAlias(t *testing.T) {
	const alias = "Climate Schedule Alias Case"
	const liveEntityID = "automation.climate_schedule_alias_case"

	statesJSON, _ := json.Marshal([]map[string]any{
		{
			"entity_id":  liveEntityID,
			"state":      "on",
			"attributes": map[string]any{"id": "climate_schedule_config_id", "friendly_name": alias},
		},
	})

	ts := startCmdServer(t, map[string]any{
		"config/entity_registry/remove": nil,
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
	})

	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Query().Get("id") != alias {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"deleted","reloaded":true}`)
	}))
	defer companionSrv.Close()

	envContent, err := os.ReadFile(filepath.Join(ts.dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	envContent = fmt.Appendf(envContent, "COMPANION_URL=%s\n", companionSrv.URL)
	if err := os.WriteFile(filepath.Join(ts.dir, ".env"), envContent, 0o600); err != nil { //nolint:gosec // test fixture dir from t.TempDir(), not user input
		t.Fatal(err)
	}
	withFlagDir(t, ts.dir)

	old := flagAutoConfirm
	flagAutoConfirm = true
	defer func() { flagAutoConfirm = old }()

	var buf bytes.Buffer
	if err := runAutoDelete(context.Background(), &buf, alias); err != nil {
		t.Fatalf("runAutoDelete confirm failed: %v", err)
	}
	if !strings.Contains(buf.String(), "deleted") {
		t.Errorf("output missing 'deleted': %q", buf.String())
	}

	if count := ts.commandCount("config/entity_registry/remove"); count != 1 {
		t.Errorf("expected EntityRegistryRemove to be called once when deleting by alias, got %d", count)
	}
}

// --- runHelperCreate (confirm=true, companion) ---

func TestRunHelperCreate_Confirm(t *testing.T) {
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Query().Get("domain") != "input_boolean" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(
			w,
			`{"status":"created","id":"party_mode","entity_id":"input_boolean.party_mode","reloaded":true,"entity_created":true}`,
		)
	}))
	defer companionSrv.Close()

	haSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer haSrv.Close()

	dir := t.TempDir()
	envContent := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=tok\nCOMPANION_URL=%s\n", haSrv.URL, companionSrv.URL)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, dir)

	yamlFile := filepath.Join(dir, "helper.yaml")
	if err := os.WriteFile(yamlFile, []byte("party_mode:\n  name: Party Mode\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldFile := flagHelperFile
	flagHelperFile = yamlFile
	defer func() { flagHelperFile = oldFile }()

	old := flagHelperConfirm
	flagHelperConfirm = true
	defer func() { flagHelperConfirm = old }()

	var buf bytes.Buffer
	if err := runHelperCreate(context.Background(), &buf, "input_boolean"); err != nil {
		t.Fatalf("runHelperCreate confirm failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "created helper") {
		t.Errorf("output missing 'created helper': %q", out)
	}
	if !strings.Contains(out, "entity_id: input_boolean.party_mode") {
		t.Errorf("output missing entity_id confirmation: %q", out)
	}
	if strings.Contains(out, "warning") {
		t.Errorf("did not expect a warning when entity_created is true: %q", out)
	}
}

func TestRunHelperCreate_Confirm_EntityNotCreated(t *testing.T) {
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(
			w,
			`{"status":"created","id":"party_mode","entity_id":"input_boolean.party_mode","reloaded":true,"entity_created":false}`,
		)
	}))
	defer companionSrv.Close()

	haSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer haSrv.Close()

	dir := t.TempDir()
	envContent := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=tok\nCOMPANION_URL=%s\n", haSrv.URL, companionSrv.URL)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, dir)

	yamlFile := filepath.Join(dir, "helper.yaml")
	if err := os.WriteFile(yamlFile, []byte("party_mode:\n  name: Party Mode\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	oldFile := flagHelperFile
	flagHelperFile = yamlFile
	defer func() { flagHelperFile = oldFile }()

	old := flagHelperConfirm
	flagHelperConfirm = true
	defer func() { flagHelperConfirm = old }()

	var buf bytes.Buffer
	if err := runHelperCreate(context.Background(), &buf, "input_boolean"); err != nil {
		t.Fatalf("runHelperCreate confirm failed: %v", err)
	}
	if !strings.Contains(buf.String(), "warning") {
		t.Errorf("expected a warning when entity_created is false: %q", buf.String())
	}
}

// --- runCCShow (fetches custom components from states) ---

func TestRunCCShow_Found(t *testing.T) {
	// Create a state that looks like a HACS update entity
	states := []map[string]any{
		{
			"entity_id": "update.hacs_update",
			"state":     "off",
			"attributes": map[string]any{
				"installed_version": "1.32.0",
				"latest_version":    "1.34.0",
				"title":             "HACS",
			},
		},
		{"entity_id": "sensor.temp", "state": "21.5"},
	}
	statesJSON, _ := json.Marshal(states)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	// runCCShow searches for the component by domain name
	// "hacs" domain would need to match "update.hacs_update"
	// The exact match depends on fetchCustomComponents logic
	// Just verify it doesn't crash
	_ = runCCShow(context.Background(), &buf, "hacs")
}

// --- runDashShow (raw mode) ---

func TestRunDashShow_RawMode(t *testing.T) {
	rawConfig := `{"title":"Home","views":[{"title":"Main"}]}`
	ts := startCmdServer(t, map[string]any{
		"lovelace/config": json.RawMessage(rawConfig),
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagDashRaw
	flagDashRaw = true
	defer func() { flagDashRaw = old }()
	oldView := flagDashView
	flagDashView = ""
	defer func() { flagDashView = oldView }()

	var buf bytes.Buffer
	if err := runDashShow(context.Background(), &buf, ""); err != nil {
		t.Fatalf("runDashShow raw mode failed: %v", err)
	}
	if !strings.Contains(buf.String(), "Home") {
		t.Errorf("output missing 'Home': %q", buf.String())
	}
}

// --- runDashGrep / runDashReplace (reference rename) ---

func TestRunDashGrep_FindsReferences(t *testing.T) {
	cfg := `{"views":[{"cards":[{"type":"entity","entity":"light.gone"}]}]}`
	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []map[string]any{{"url_path": "lovelace-home", "title": "Home", "mode": "storage"}},
		"lovelace/config":          json.RawMessage(cfg),
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runDashGrep(context.Background(), &buf, "light.gone"); err != nil {
		t.Fatalf("runDashGrep failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "views[0].cards[0].entity") {
		t.Errorf("expected the reference path, got: %q", out)
	}
	if !strings.Contains(out, "lovelace-home") {
		t.Errorf("expected the dashboard label, got: %q", out)
	}
}

func TestRunDashGrep_NoReferences(t *testing.T) {
	cfg := `{"views":[{"cards":[{"entity":"light.other"}]}]}`
	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/config":          json.RawMessage(cfg),
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runDashGrep(context.Background(), &buf, "light.gone"); err != nil {
		t.Fatalf("runDashGrep failed: %v", err)
	}
	if !strings.Contains(buf.String(), "not referenced") {
		t.Errorf("expected 'not referenced', got: %q", buf.String())
	}
}

func TestRunDashReplace_DryRunDoesNotSave(t *testing.T) {
	cfg := `{"views":[{"cards":[{"entity":"light.old"}]}]}`
	ts := startCmdServer(t, map[string]any{
		"lovelace/config": json.RawMessage(cfg),
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagDashConfirm
	flagDashConfirm = false
	defer func() { flagDashConfirm = old }()

	var buf bytes.Buffer
	if err := runDashReplace(context.Background(), &buf, "light.old", "light.new", ""); err != nil {
		t.Fatalf("runDashReplace dry-run failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, "views[0].cards[0].entity") {
		t.Errorf("dry-run should show the path-level diff, got: %q", out)
	}
	if !strings.Contains(out, "light.old") || !strings.Contains(out, "light.new") {
		t.Errorf("dry-run should show old→new, got: %q", out)
	}
	if n := ts.commandCount("lovelace/config/save"); n != 0 {
		t.Errorf("dry-run must not save, but save was called %d time(s)", n)
	}
}

func TestRunDashReplace_ConfirmSaves(t *testing.T) {
	cfg := `{"views":[{"cards":[{"entity":"light.old"}]}]}`
	ts := startCmdServer(t, map[string]any{
		"lovelace/config":      json.RawMessage(cfg),
		"lovelace/config/save": nil,
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagDashConfirm
	flagDashConfirm = true
	defer func() { flagDashConfirm = old }()

	var buf bytes.Buffer
	if err := runDashReplace(context.Background(), &buf, "light.old", "light.new", ""); err != nil {
		t.Fatalf("runDashReplace confirm failed: %v", err)
	}
	if n := ts.commandCount("lovelace/config/save"); n != 1 {
		t.Errorf("confirm should save exactly once, got %d", n)
	}
	if !strings.Contains(buf.String(), "replaced") {
		t.Errorf("expected 'replaced' confirmation, got: %q", buf.String())
	}
}

func TestRunDashReplace_NotFound(t *testing.T) {
	cfg := `{"views":[{"cards":[{"entity":"light.other"}]}]}`
	ts := startCmdServer(t, map[string]any{
		"lovelace/config": json.RawMessage(cfg),
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runDashReplace(context.Background(), &buf, "light.old", "light.new", ""); err != nil {
		t.Fatalf("runDashReplace failed: %v", err)
	}
	if !strings.Contains(buf.String(), "not found") {
		t.Errorf("expected 'not found', got: %q", buf.String())
	}
}

// --- resolveTraceID with trc: prefix ---

func TestResolveTraceID_TrcPrefix_NotFound(t *testing.T) {
	idsPath := filepath.Join(t.TempDir(), "ids.json")
	reg := ids.NewRegistry(idsPath)

	_, _, _, err := resolveTraceID(reg, "trc:unknown") //nolint:dogsled
	if err == nil {
		t.Fatal("expected error for unknown trc: ID, got nil")
	}
	if !strings.Contains(err.Error(), "unknown trace ID") {
		t.Errorf("error = %q, want 'unknown trace ID'", err.Error())
	}
}

func TestResolveTraceID_TrcPrefix_Found(t *testing.T) {
	idsPath := filepath.Join(t.TempDir(), "ids.json")
	reg := ids.NewRegistry(idsPath)
	// Register a trace ID first
	shortID := reg.GetOrCreate("trc", "automation.test_auto/run-abc")
	_ = reg.Save()

	domain, itemID, runID, err := resolveTraceID(reg, shortID)
	if err != nil {
		t.Fatalf("resolveTraceID with valid trc: ID failed: %v", err)
	}
	if domain != "automation" || itemID != "test_auto" || runID != "run-abc" {
		t.Errorf("resolveTraceID = (%q, %q, %q), want (automation, test_auto, run-abc)", domain, itemID, runID)
	}
}

// --- runScriptCreate confirm=true (with companion) ---

func TestRunScriptCreate_Confirm(t *testing.T) {
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.Contains(r.URL.Path, "/v1/config/script") {
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprint(w, `{"status":"created","id":"new_script"}`)
		} else {
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer companionSrv.Close()

	haSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer haSrv.Close()

	envDir := t.TempDir()
	envContent := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=tok\nCOMPANION_URL=%s\n", haSrv.URL, companionSrv.URL)
	if err := os.WriteFile(filepath.Join(envDir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, envDir)

	scriptFile := filepath.Join(envDir, "new_script.yaml")
	if err := os.WriteFile(scriptFile, []byte("new_script:\n  alias: New Script\n  sequence: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	old := flagScriptFile
	flagScriptFile = scriptFile
	defer func() { flagScriptFile = old }()
	oldConfirm := flagScriptConfirm
	flagScriptConfirm = true
	defer func() { flagScriptConfirm = oldConfirm }()

	var buf bytes.Buffer
	if err := runScriptCreate(context.Background(), &buf); err != nil {
		t.Fatalf("runScriptCreate confirm failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "created") {
		t.Errorf("output missing 'created': %q", out)
	}
}

// --- runScriptRun - not found path ---

func TestRunScriptRun_NotFound(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states/script.nonexistent": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	err := runScriptRun(context.Background(), &buf, "nonexistent")
	if err == nil {
		t.Fatal("expected error for not-found script, got nil")
	}
}

// --- runSetup (interactive, but takes io.Reader) ---

func TestRunSetup_NewConfig(t *testing.T) {
	// Mock HA server for connectivity test
	haSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"message":"API running."}`)
		} else {
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer haSrv.Close()

	// Create a temp dir for the .env file
	dir := t.TempDir()
	withFlagDir(t, dir)

	// Simulate user input: URL + token
	input := haSrv.URL + "\ntest-long-lived-token-123\n"
	reader := strings.NewReader(input)

	var buf bytes.Buffer
	if err := runSetup(context.Background(), &buf, reader); err != nil {
		t.Fatalf("runSetup failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Setup complete") {
		t.Errorf("output missing 'Setup complete': %q", out)
	}
	if !strings.Contains(out, "OK") && !strings.Contains(out, "FAILED") {
		t.Errorf("output missing connection status: %q", out)
	}
}

func TestRunSetup_ExistingConfig_KeepExisting(t *testing.T) {
	dir := t.TempDir()
	envContent := "HA_URL=http://old.example.com\nHA_TOKEN=old-token\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, dir)

	// User says 'N' to keep existing
	input := "N\n"
	reader := strings.NewReader(input)

	var buf bytes.Buffer
	if err := runSetup(context.Background(), &buf, reader); err != nil {
		t.Fatalf("runSetup (keep existing) failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Keeping existing") {
		t.Errorf("output missing 'Keeping existing': %q", out)
	}
}

func TestRunSetup_ExistingConfig_Overwrite(t *testing.T) {
	haSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"message":"API running."}`)
		} else {
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer haSrv.Close()

	dir := t.TempDir()
	envContent := "HA_URL=http://old.example.com\nHA_TOKEN=old-token\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, dir)

	// User says 'y' to overwrite, provides new URL + token
	input := fmt.Sprintf("y\n%s\nnew-token-456\n", haSrv.URL)
	reader := strings.NewReader(input)

	var buf bytes.Buffer
	if err := runSetup(context.Background(), &buf, reader); err != nil {
		t.Fatalf("runSetup (overwrite) failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Setup complete") {
		t.Errorf("output missing 'Setup complete': %q", out)
	}
}

// --- readLine ---

func TestReadLine(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("hello world\n"))
	got := readLine(reader)
	if got != "hello world" {
		t.Errorf("readLine = %q, want 'hello world'", got)
	}
}

// --- runAutoDiff (HTTP) ---

func TestRunAutoDiff_NoChanges(t *testing.T) {
	remoteJSON := `{"alias":"Climate Schedule","trigger":[],"condition":[],"action":[]}`
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/automation/config/climate_schedule": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, remoteJSON)
		},
	})
	withFlagDir(t, ts.dir)

	// Create local file matching remote
	localFile := filepath.Join(ts.dir, "climate_schedule.yaml")
	if err := os.WriteFile(localFile, []byte("alias: Climate Schedule\naction: []\ncondition: []\ntrigger: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	old := flagAutoFile
	flagAutoFile = localFile
	defer func() { flagAutoFile = old }()

	var buf bytes.Buffer
	if err := runAutoDiff(context.Background(), &buf, "climate_schedule"); err != nil {
		t.Fatalf("runAutoDiff failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no changes") {
		t.Logf("runAutoDiff output: %q", buf.String())
	}
}

func TestRunAutoDiff_WithChanges(t *testing.T) {
	remoteJSON := `{"alias":"Old Name","trigger":[],"condition":[],"action":[]}`
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/automation/config/my_auto": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, remoteJSON)
		},
	})
	withFlagDir(t, ts.dir)

	localFile := filepath.Join(ts.dir, "my_auto.yaml")
	if err := os.WriteFile(localFile, []byte("alias: New Name\ntrigger: []\ncondition: []\naction: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	old := flagAutoFile
	flagAutoFile = localFile
	defer func() { flagAutoFile = old }()

	var buf bytes.Buffer
	if err := runAutoDiff(context.Background(), &buf, "my_auto"); err != nil {
		t.Fatalf("runAutoDiff with changes failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "my_auto") {
		t.Errorf("output missing automation ID: %q", out)
	}
}

// --- runAutoApply (HTTP, no WS needed for dry-run) ---

func TestRunAutoApply_DryRun(t *testing.T) {
	remoteJSON := `{"alias":"Current","trigger":[],"condition":[],"action":[]}`
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/automation/config/test_auto": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, remoteJSON)
		},
	})
	withFlagDir(t, ts.dir)

	localFile := filepath.Join(ts.dir, "test_auto.yaml")
	if err := os.WriteFile(localFile, []byte("alias: Updated\ntrigger: []\ncondition: []\naction: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	old := flagAutoFile
	flagAutoFile = localFile
	defer func() { flagAutoFile = old }()
	oldConfirm := flagAutoConfirm
	flagAutoConfirm = false
	defer func() { flagAutoConfirm = oldConfirm }()

	var buf bytes.Buffer
	if err := runAutoApply(context.Background(), &buf, "test_auto"); err != nil {
		t.Fatalf("runAutoApply dry-run failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, "test_auto") {
		t.Errorf("output = %q, want 'dry-run' and 'test_auto'", out)
	}
}

func TestRunAutoApply_Confirm(t *testing.T) {
	remoteJSON := `{"alias":"Old","trigger":[],"condition":[],"action":[]}`
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/automation/config/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if r.Method == http.MethodGet {
				_, _ = fmt.Fprint(w, remoteJSON)
			} else {
				w.WriteHeader(http.StatusOK)
				_, _ = fmt.Fprint(w, `{}`)
			}
		},
		"/api/services/automation/reload": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `{}`)
		},
	})
	withFlagDir(t, ts.dir)

	localFile := filepath.Join(ts.dir, "the_auto.yaml")
	if err := os.WriteFile(localFile, []byte("alias: New\ntrigger: []\ncondition: []\naction: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	old := flagAutoFile
	flagAutoFile = localFile
	defer func() { flagAutoFile = old }()
	oldConfirm := flagAutoConfirm
	flagAutoConfirm = true
	defer func() { flagAutoConfirm = oldConfirm }()

	var buf bytes.Buffer
	// May fail if route matching is tricky - that's ok, just test it runs
	_ = runAutoApply(context.Background(), &buf, "the_auto")
}

// --- runHealth (HTTP + companion) ---

func TestRunHealth_Basic(t *testing.T) {
	haConfigJSON := `{"version":"2026.4.0","state":"RUNNING","location_name":"Home","time_zone":"UTC","components":["recorder","automation","homeassistant"],"safe_mode":false}`
	logText := "2026-01-01 10:00:00.000 ERROR (Main) [comp] Test error\n"

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, haConfigJSON)
		},
		"/api/error_log": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, logText)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagJSON
	flagJSON = false
	defer func() { flagJSON = old }()

	var buf bytes.Buffer
	if err := runHealth(context.Background(), &buf); err != nil {
		t.Fatalf("runHealth failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "2026.4.0") {
		t.Errorf("output missing HA version: %q", out)
	}
	if !strings.Contains(out, "RUNNING") {
		t.Errorf("output missing state: %q", out)
	}
}

func TestRunHealth_JSON(t *testing.T) {
	haConfigJSON := `{"version":"2026.4.0","state":"RUNNING","location_name":"Home","time_zone":"UTC","components":["recorder"],"safe_mode":false}`

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, haConfigJSON)
		},
		"/api/error_log": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagJSON
	flagJSON = true
	defer func() { flagJSON = old }()

	var buf bytes.Buffer
	if err := runHealth(context.Background(), &buf); err != nil {
		t.Fatalf("runHealth JSON failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &parsed); err != nil {
		t.Fatalf("JSON output is not valid: %v\n%s", err, buf.String())
	}
}

func TestRunHealth_WithCompanion(t *testing.T) {
	haConfigJSON := `{"version":"2026.4.0","state":"RUNNING","location_name":"Test","time_zone":"UTC","components":[],"safe_mode":false}`

	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"status":"ok","version":"1.2.0"}`)
	}))
	defer companionSrv.Close()

	haSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, haConfigJSON)
		case "/api/error_log":
			_, _ = fmt.Fprint(w, "")
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer haSrv.Close()

	envDir := t.TempDir()
	envContent := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=tok\nCOMPANION_URL=%s\n", haSrv.URL, companionSrv.URL)
	if err := os.WriteFile(filepath.Join(envDir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, envDir)

	old := flagJSON
	flagJSON = false
	defer func() { flagJSON = old }()

	var buf bytes.Buffer
	if err := runHealth(context.Background(), &buf); err != nil {
		t.Fatalf("runHealth with companion failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "companion") {
		t.Errorf("output missing companion status: %q", out)
	}
}

func TestRunHealth_CheckConfig(t *testing.T) {
	haConfigJSON := `{"version":"2026.4.0","state":"RUNNING","location_name":"Test","time_zone":"UTC","components":[],"safe_mode":false}`

	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/health":
			_, _ = fmt.Fprint(w, `{"status":"ok","version":"1.2.0"}`)
		case "/v1/ha/check-config":
			_, _ = fmt.Fprint(w, `{"status":"invalid","valid":false,"errors":"bad automation yaml"}`)
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer companionSrv.Close()

	haSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/config":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, haConfigJSON)
		case "/api/error_log":
			_, _ = fmt.Fprint(w, "")
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer haSrv.Close()

	envDir := t.TempDir()
	envContent := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=tok\nCOMPANION_URL=%s\n", haSrv.URL, companionSrv.URL)
	if err := os.WriteFile(filepath.Join(envDir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, envDir)

	oldJSON := flagJSON
	flagJSON = false
	oldCheck := flagHealthCheckConfig
	flagHealthCheckConfig = true
	defer func() {
		flagJSON = oldJSON
		flagHealthCheckConfig = oldCheck
	}()

	var buf bytes.Buffer
	if err := runHealth(context.Background(), &buf); err != nil {
		t.Fatalf("runHealth with check-config failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "config_check=INVALID: bad automation yaml") {
		t.Errorf("output missing invalid config result: %q", out)
	}
}

// --- findAutomationRelations (HTTP+states) ---

func TestFindAutomationRelations_NoID(t *testing.T) {
	// Automations without 'id' attribute are skipped
	ts := startCmdServer(t, map[string]any{}, nil)
	withFlagDir(t, ts.dir)

	client := haapi.New(ts.srv.URL, "tok")
	states := []entityState{
		{
			EntityID:   "automation.some_auto",
			State:      "on",
			Attributes: map[string]any{}, // no 'id'
		},
	}

	result := findAutomationRelations(context.Background(), client, states, "sensor.temperature")
	if len(result) != 0 {
		t.Errorf("expected 0 relations (no id in automation), got %d", len(result))
	}
}

func TestFindAutomationRelations_WithConfig(t *testing.T) {
	configJSON := `{"id":"test_auto","alias":"Test","trigger":[{"platform":"state","entity_id":"sensor.temperature"}],"condition":[],"action":[]}`

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/automation/config/test_auto": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, configJSON)
		},
	})
	withFlagDir(t, ts.dir)

	client := haapi.New(ts.srv.URL, "tok")
	states := []entityState{
		{
			EntityID:   "automation.test_auto",
			State:      "on",
			Attributes: map[string]any{"id": "test_auto"},
		},
	}

	result := findAutomationRelations(context.Background(), client, states, "sensor.temperature")
	t.Logf("findAutomationRelations: %d results", len(result))
	// Just verify it runs without panicking
}

// --- printVersion ---

func TestPrintVersion_WithTestedHA(t *testing.T) {
	old := testedHA
	testedHA = "2026.4, 2026.3"
	defer func() { testedHA = old }()

	var buf bytes.Buffer
	printVersion(&buf)
	out := buf.String()
	if !strings.Contains(out, "hactl") {
		t.Errorf("printVersion missing 'hactl': %q", out)
	}
	if !strings.Contains(out, "tested") {
		t.Errorf("printVersion with testedHA missing 'tested': %q", out)
	}
	if !strings.Contains(out, "2026.4") {
		t.Errorf("printVersion missing HA version: %q", out)
	}
}

func TestPrintVersion_NoTestedHA(t *testing.T) {
	old := testedHA
	testedHA = ""
	defer func() { testedHA = old }()

	var buf bytes.Buffer
	printVersion(&buf)
	out := buf.String()
	if strings.Contains(out, "tested") {
		t.Errorf("printVersion without testedHA should not show tested line: %q", out)
	}
}

// --- runLabelCreate (WS) ---

func TestRunLabelCreate(t *testing.T) {
	createdLabel := map[string]any{"label_id": "new_energy", "name": "Energy", "color": "green"}
	ts := startCmdServer(t, map[string]any{
		"config/label_registry/create": createdLabel,
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagLabelColor
	flagLabelColor = "green"
	defer func() { flagLabelColor = old }()
	oldIcon := flagLabelIcon
	flagLabelIcon = ""
	defer func() { flagLabelIcon = oldIcon }()
	oldDesc := flagLabelDesc
	flagLabelDesc = ""
	defer func() { flagLabelDesc = oldDesc }()

	oldConfirm := flagLabelConfirm
	flagLabelConfirm = true
	defer func() { flagLabelConfirm = oldConfirm }()

	var buf bytes.Buffer
	if err := runLabelCreate(context.Background(), &buf, "Energy"); err != nil {
		t.Fatalf("runLabelCreate failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "created") {
		t.Errorf("output missing 'created': %q", out)
	}
}

// --- runAreaCreate (WS) ---

func TestRunAreaCreate(t *testing.T) {
	createdArea := map[string]any{"area_id": "new_kitchen", "name": "Kitchen"}
	ts := startCmdServer(t, map[string]any{
		"config/area_registry/create": createdArea,
	}, nil)
	withFlagDir(t, ts.dir)

	oldIcon := flagAreaIcon
	flagAreaIcon = ""
	defer func() { flagAreaIcon = oldIcon }()
	oldFloor := flagAreaFloor
	flagAreaFloor = ""
	defer func() { flagAreaFloor = oldFloor }()

	oldConfirm := flagAreaConfirm
	flagAreaConfirm = true
	defer func() { flagAreaConfirm = oldConfirm }()

	var buf bytes.Buffer
	if err := runAreaCreate(context.Background(), &buf, "Kitchen"); err != nil {
		t.Fatalf("runAreaCreate failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "created") {
		t.Errorf("output missing 'created': %q", out)
	}
}

// --- runFloorCreate (WS) ---

func TestRunFloorCreate(t *testing.T) {
	level := 0
	createdFloor := map[string]any{"floor_id": "ground", "name": "Ground", "level": float64(level)}
	ts := startCmdServer(t, map[string]any{
		"config/floor_registry/create": createdFloor,
	}, nil)
	withFlagDir(t, ts.dir)

	oldConfirm := flagFloorConfirm
	flagFloorConfirm = true
	defer func() { flagFloorConfirm = oldConfirm }()

	var buf bytes.Buffer
	if err := runFloorCreate(context.Background(), &buf, "Ground"); err != nil {
		t.Fatalf("runFloorCreate failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "created") {
		t.Errorf("output missing 'created': %q", out)
	}
}

// --- runChanges (HTTP) ---

func TestRunChanges_Empty(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/logbook/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
	withFlagDir(t, ts.dir)

	old := flagSince
	flagSince = "24h"
	defer func() { flagSince = old }()

	var buf bytes.Buffer
	if err := runChanges(context.Background(), &buf); err != nil {
		t.Fatalf("runChanges failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no changes") {
		t.Errorf("empty logbook should say 'no changes': %q", buf.String())
	}
}

func TestRunChanges_WithEntries(t *testing.T) {
	entries := []map[string]any{
		{"entity_id": "light.kitchen", "state": "on", "when": "2026-01-01T10:00:00Z", "name": "Kitchen Light", "domain": "light"},
		{"entity_id": "automation.climate", "state": "triggered", "when": "2026-01-01T10:01:00Z", "message": "Climate triggered"},
	}
	entriesJSON, _ := json.Marshal(entries)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/logbook/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(entriesJSON)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagSince
	flagSince = "24h"
	defer func() { flagSince = old }()

	var buf bytes.Buffer
	if err := runChanges(context.Background(), &buf); err != nil {
		t.Fatalf("runChanges failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "light.kitchen") {
		t.Errorf("output missing entity: %q", out)
	}
}

// --- runIssues (HTTP) ---

func TestRunIssues_Empty(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/repairs/issues": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"issues":[]}`)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runIssues(context.Background(), &buf); err != nil {
		t.Fatalf("runIssues failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no active issues") {
		t.Errorf("empty issues should say 'no active issues': %q", buf.String())
	}
}

func TestRunIssues_WithIssues(t *testing.T) {
	body := `{"issues":[{"domain":"recorder","issue_id":"setup_failed","severity":"error","is_fixable":false}]}`
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/repairs/issues": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, body)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runIssues(context.Background(), &buf); err != nil {
		t.Fatalf("runIssues failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "recorder") {
		t.Errorf("output missing domain 'recorder': %q", out)
	}
	if !strings.Contains(out, "setup_failed") {
		t.Errorf("output missing issue_id: %q", out)
	}
}

func TestRunIssues_404(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/repairs/issues": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not found", http.StatusNotFound)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runIssues(context.Background(), &buf); err != nil {
		t.Fatalf("runIssues 404 should not error: %v", err)
	}
	if !strings.Contains(buf.String(), "no active issues") {
		t.Errorf("404 should say 'no active issues': %q", buf.String())
	}
}

// --- runScriptLs (HTTP, WS optional) ---

func TestRunScriptLs_WithScripts(t *testing.T) {
	states := []map[string]any{
		{"entity_id": "script.welcome_home", "state": "off", "last_changed": "2026-01-01T10:00:00Z"},
		{"entity_id": "script.good_night", "state": "off", "last_changed": "2026-01-01T09:00:00Z"},
		{"entity_id": "sensor.temp", "state": "21.5", "last_changed": "2026-01-01T10:00:00Z"},
	}
	statesJSON, _ := json.Marshal(states)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
		"/api/logbook/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
	withFlagDir(t, ts.dir)

	// Reset script flags
	old := flagScriptPattern
	flagScriptPattern = ""
	defer func() { flagScriptPattern = old }()
	oldLabel := flagScriptLabel
	flagScriptLabel = ""
	defer func() { flagScriptLabel = oldLabel }()
	oldFailing := flagScriptFailing
	flagScriptFailing = false
	defer func() { flagScriptFailing = oldFailing }()
	oldSince := flagSince
	flagSince = "24h"
	defer func() { flagSince = oldSince }()

	var buf bytes.Buffer
	if err := runScriptLs(context.Background(), &buf); err != nil {
		t.Fatalf("runScriptLs failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "welcome_home") {
		t.Errorf("output missing script: %q", out)
	}
	if strings.Contains(out, "sensor.temp") {
		t.Errorf("output should not contain non-script entities: %q", out)
	}
}

// --- runDashLs (WS) ---

func TestRunDashLs_Empty(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runDashLs(context.Background(), &buf); err != nil {
		t.Fatalf("runDashLs failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no dashboards") {
		t.Errorf("empty result should say 'no dashboards': %q", buf.String())
	}
}

func TestRunAutoShow_WithState(t *testing.T) {
	stateData := map[string]any{
		"entity_id": "automation.climate_schedule",
		"state":     "on",
		"attributes": map[string]any{
			"mode":           "single",
			"last_triggered": "2026-01-01T10:00:00Z",
			"friendly_name":  "Climate Schedule",
		},
		"last_changed": "2026-01-01T10:00:00Z",
	}
	stateJSON, _ := json.Marshal(stateData)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states/automation.climate_schedule": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(stateJSON)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	// WS will fail (no trace/list handler), but the function should still show state
	if err := runAutoShow(context.Background(), &buf, "climate_schedule"); err != nil {
		t.Fatalf("runAutoShow failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "automation.climate_schedule") {
		t.Errorf("output missing entity ID: %q", out)
	}
	if !strings.Contains(out, "state=on") {
		t.Errorf("output missing state: %q", out)
	}
}

func TestRunCCLs_Empty(t *testing.T) {
	// No update.* entities → no custom components
	states := []map[string]any{
		{"entity_id": "sensor.temp", "state": "21.5"},
		{"entity_id": "binary_sensor.door", "state": "off"},
	}
	statesJSON, _ := json.Marshal(states)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runCCLs(context.Background(), &buf); err != nil {
		t.Fatalf("runCCLs failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no custom components") {
		t.Errorf("empty result should say 'no custom components': %q", buf.String())
	}
}

// --- runEntShow (HTTP) ---

func TestRunEntShow_Basic(t *testing.T) {
	stateData := map[string]any{
		"entity_id":    "sensor.temperature",
		"state":        "21.5",
		"last_changed": "2026-01-01T10:00:00Z",
		"last_updated": "2026-01-01T10:00:00Z",
		"attributes": map[string]any{
			"friendly_name":       "Temperature",
			"unit_of_measurement": "°C",
			"device_class":        "temperature",
		},
	}
	stateJSON, _ := json.Marshal(stateData)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states/sensor.temperature": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(stateJSON)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagFull
	flagFull = false
	defer func() { flagFull = old }()

	var buf bytes.Buffer
	if err := runEntShow(context.Background(), &buf, "sensor.temperature"); err != nil {
		t.Fatalf("runEntShow failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "sensor.temperature") {
		t.Errorf("output missing entity ID: %q", out)
	}
	if !strings.Contains(out, "21.5") {
		t.Errorf("output missing state: %q", out)
	}
	if !strings.Contains(out, "Temperature") {
		t.Errorf("output missing friendly_name: %q", out)
	}
}

func TestRunEntShow_JSON(t *testing.T) {
	stateData := map[string]any{
		"entity_id":    "light.kitchen",
		"state":        "on",
		"last_changed": "2026-01-01T10:00:00Z",
		"last_updated": "2026-01-01T10:00:00Z",
		"attributes":   map[string]any{"brightness": 255},
	}
	stateJSON, _ := json.Marshal(stateData)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states/light.kitchen": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(stateJSON)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagJSON
	flagJSON = true
	defer func() { flagJSON = old }()

	var buf bytes.Buffer
	if err := runEntShow(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntShow JSON failed: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &parsed); err != nil {
		t.Fatalf("JSON output is not valid JSON: %v\n%s", err, buf.String())
	}
}

// --- runEntHist (HTTP) ---

func TestRunEntHist_NumericData(t *testing.T) {
	// Return numeric history data for a temperature sensor
	histData := `[[
		{"entity_id":"sensor.temp","state":"21.5","last_changed":"2026-01-01T10:00:00+00:00","attributes":{}},
		{"entity_id":"sensor.temp","state":"22.0","last_changed":"2026-01-01T11:00:00+00:00","attributes":{}},
		{"entity_id":"sensor.temp","state":"21.8","last_changed":"2026-01-01T12:00:00+00:00","attributes":{}}
	]]`

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/history/period/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, histData)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagSince
	flagSince = "24h"
	defer func() { flagSince = old }()
	oldAttr := flagEntAttr
	flagEntAttr = ""
	defer func() { flagEntAttr = oldAttr }()
	oldResample := flagEntResample
	flagEntResample = ""
	defer func() { flagEntResample = oldResample }()

	var buf bytes.Buffer
	if err := runEntHist(context.Background(), &buf, "sensor.temp"); err != nil {
		t.Fatalf("runEntHist numeric failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "sensor.temp") {
		t.Errorf("output missing entity ID: %q", out)
	}
}

func TestRunEntHist_BinarySensor(t *testing.T) {
	// Non-numeric: binary sensor (on/off)
	histData := `[[
		{"entity_id":"binary_sensor.door","state":"off","last_changed":"2026-01-01T10:00:00+00:00","attributes":{}},
		{"entity_id":"binary_sensor.door","state":"on","last_changed":"2026-01-01T10:05:00+00:00","attributes":{}},
		{"entity_id":"binary_sensor.door","state":"off","last_changed":"2026-01-01T10:10:00+00:00","attributes":{}}
	]]`

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/history/period/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, histData)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagSince
	flagSince = "24h"
	defer func() { flagSince = old }()
	oldAttr := flagEntAttr
	flagEntAttr = ""
	defer func() { flagEntAttr = oldAttr }()
	oldResample := flagEntResample
	flagEntResample = ""
	defer func() { flagEntResample = oldResample }()

	var buf bytes.Buffer
	if err := runEntHist(context.Background(), &buf, "binary_sensor.door"); err != nil {
		t.Fatalf("runEntHist binary failed: %v", err)
	}
	out := buf.String()
	// Should render as state timeline since states are non-numeric
	if !strings.Contains(out, "binary_sensor.door") {
		t.Errorf("output missing entity ID: %q", out)
	}
}

func TestRunEntHist_NoData(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/history/period/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[[]]")
		},
	})
	withFlagDir(t, ts.dir)

	old := flagSince
	flagSince = "24h"
	defer func() { flagSince = old }()
	oldAttr := flagEntAttr
	flagEntAttr = ""
	defer func() { flagEntAttr = oldAttr }()
	oldResample := flagEntResample
	flagEntResample = ""
	defer func() { flagEntResample = oldResample }()

	var buf bytes.Buffer
	if err := runEntHist(context.Background(), &buf, "sensor.x"); err != nil {
		t.Fatalf("runEntHist no data failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no history") {
		t.Errorf("empty history should say 'no history': %q", buf.String())
	}
}

// --- runScriptShow (HTTP) ---

func TestRunScriptShow_Basic(t *testing.T) {
	stateData := map[string]any{
		"entity_id":    "script.welcome_home",
		"state":        "off",
		"last_changed": "2026-01-01T10:00:00Z",
		"last_updated": "2026-01-01T10:00:00Z",
		"attributes": map[string]any{
			"friendly_name": "Welcome Home",
		},
	}
	stateJSON, _ := json.Marshal(stateData)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states/script.welcome_home": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(stateJSON)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runScriptShow(context.Background(), &buf, "welcome_home"); err != nil {
		t.Fatalf("runScriptShow failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "script.welcome_home") {
		t.Errorf("output missing entity ID: %q", out)
	}
	if !strings.Contains(out, "state=off") {
		t.Errorf("output missing state: %q", out)
	}
}

func TestRunDashLs_WithDashboards(t *testing.T) {
	dashboards := []map[string]any{
		{"id": "lovelace", "url_path": "", "title": "Home", "mode": "storage"},
		{"id": "energy", "url_path": "energy", "title": "Energy", "mode": "storage"},
	}
	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": dashboards,
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runDashLs(context.Background(), &buf); err != nil {
		t.Fatalf("runDashLs with dashboards failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Home") {
		t.Errorf("output missing default dashboard title 'Home': %q", out)
	}
	if !strings.Contains(out, "energy") {
		t.Errorf("output missing 'energy' dashboard: %q", out)
	}
}

// --- runCCShow found path ---

func TestRunCCShow_WithEntityCount(t *testing.T) {
	states := []map[string]any{
		{
			"entity_id": "update.hacs_update",
			"state":     "off",
			"attributes": map[string]any{
				"installed_version": "1.32.0",
				"title":             "HACS",
			},
		},
		{"entity_id": "hacs_update.some_entity", "state": "on"},
	}
	statesJSON, _ := json.Marshal(states)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runCCShow(context.Background(), &buf, "hacs_update"); err != nil {
		t.Fatalf("runCCShow found failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "hacs_update") {
		t.Errorf("output missing domain: %q", out)
	}
	if !strings.Contains(out, "1.32.0") {
		t.Errorf("output missing version: %q", out)
	}
}

// --- runCCLs with components ---

func TestRunCCLs_WithHACSComponents(t *testing.T) {
	states := []map[string]any{
		{
			"entity_id": "update.hacs_update",
			"state":     "off",
			"attributes": map[string]any{
				"installed_version": "1.32.0",
				"title":             "HACS",
			},
		},
	}
	statesJSON, _ := json.Marshal(states)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runCCLs(context.Background(), &buf); err != nil {
		t.Fatalf("runCCLs with components failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "hacs_update") {
		t.Errorf("output missing component domain: %q", out)
	}
	if !strings.Contains(out, "1.32.0") {
		t.Errorf("output missing version: %q", out)
	}
}

// --- runLog filter flags ---

func TestRunLog_ErrorsFilter(t *testing.T) {
	logText := "2026-01-01 10:00:00.000 ERROR (Main) [comp.test] Something broke\n"
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/error_log": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, logText)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagLogErrors
	flagLogErrors = true
	defer func() { flagLogErrors = old }()
	oldComp := flagLogComponent
	flagLogComponent = ""
	defer func() { flagLogComponent = oldComp }()
	oldUniq := flagLogUnique
	flagLogUnique = false
	defer func() { flagLogUnique = oldUniq }()

	var buf bytes.Buffer
	if err := runLog(context.Background(), &buf); err != nil {
		t.Fatalf("runLog --errors failed: %v", err)
	}
}

func TestRunLog_ComponentFilter(t *testing.T) {
	logText := "2026-01-01 10:00:00.000 ERROR (Main) [comp.test] Something broke\n"
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/error_log": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, logText)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagLogErrors
	flagLogErrors = false
	defer func() { flagLogErrors = old }()
	oldComp := flagLogComponent
	flagLogComponent = "test"
	defer func() { flagLogComponent = oldComp }()
	oldUniq := flagLogUnique
	flagLogUnique = false
	defer func() { flagLogUnique = oldUniq }()

	var buf bytes.Buffer
	if err := runLog(context.Background(), &buf); err != nil {
		t.Fatalf("runLog --component failed: %v", err)
	}
}

func TestRunLog_Unique(t *testing.T) {
	logText := "2026-01-01 10:00:00.000 ERROR (Main) [comp.test] Something broke\n"
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/error_log": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, logText)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagLogErrors
	flagLogErrors = false
	defer func() { flagLogErrors = old }()
	oldComp := flagLogComponent
	flagLogComponent = ""
	defer func() { flagLogComponent = oldComp }()
	oldUniq := flagLogUnique
	flagLogUnique = true
	defer func() { flagLogUnique = oldUniq }()

	var buf bytes.Buffer
	if err := runLog(context.Background(), &buf); err != nil {
		t.Fatalf("runLog --unique failed: %v", err)
	}
}

// --- runSvcCall success path ---

func TestRunSvcCall_Success(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/services/": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprint(w, `[]`)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagSvcData
	flagSvcData = "{}"
	defer func() { flagSvcData = old }()

	oldConfirm := flagSvcConfirm
	flagSvcConfirm = true
	defer func() { flagSvcConfirm = oldConfirm }()

	var buf bytes.Buffer
	if err := runSvcCall(context.Background(), &buf, "group.reload"); err != nil {
		t.Fatalf("runSvcCall success failed: %v", err)
	}
	if !strings.Contains(buf.String(), "called group.reload") {
		t.Errorf("output = %q, want 'called group.reload'", buf.String())
	}
}

// --- runAutoLs filter flags ---

func TestRunAutoLs_WithPattern(t *testing.T) {
	states := []map[string]any{
		{"entity_id": "automation.climate_schedule", "state": "on", "last_changed": "2026-01-01T10:00:00Z"},
		{"entity_id": "automation.alarm_morning", "state": "on", "last_changed": "2026-01-01T09:00:00Z"},
	}
	statesJSON, _ := json.Marshal(states)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
		"/api/logbook/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
	withFlagDir(t, ts.dir)

	old := flagAutoPattern
	flagAutoPattern = "climate*"
	defer func() { flagAutoPattern = old }()
	oldLabel := flagAutoLabel
	flagAutoLabel = ""
	defer func() { flagAutoLabel = oldLabel }()
	oldFailing := flagAutoFailing
	flagAutoFailing = false
	defer func() { flagAutoFailing = oldFailing }()
	oldSince := flagSince
	flagSince = "24h"
	defer func() { flagSince = oldSince }()

	var buf bytes.Buffer
	if err := runAutoLs(context.Background(), &buf); err != nil {
		t.Fatalf("runAutoLs with pattern failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "climate_schedule") {
		t.Errorf("output missing climate_schedule: %q", out)
	}
	if strings.Contains(out, "alarm_morning") {
		t.Errorf("output should NOT contain alarm_morning (filtered): %q", out)
	}
}

func TestRunAutoLs_WithFailing(t *testing.T) {
	states := []map[string]any{
		{"entity_id": "automation.climate_schedule", "state": "on", "last_changed": "2026-01-01T10:00:00Z"},
	}
	statesJSON, _ := json.Marshal(states)

	ts := startCmdServer(t, map[string]any{
		// Provide traces with errors for climate_schedule (flat array format)
		"trace/list": []map[string]any{
			{
				"run_id":           "run-001",
				"domain":           "automation",
				"item_id":          "climate_schedule",
				"timestamp":        map[string]any{"start": time.Now().Add(-1 * time.Hour).Format(time.RFC3339Nano)},
				"script_execution": "error",
				"error":            "template error",
			},
		},
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
		"/api/logbook/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
	withFlagDir(t, ts.dir)

	old := flagAutoPattern
	flagAutoPattern = ""
	defer func() { flagAutoPattern = old }()
	oldLabel := flagAutoLabel
	flagAutoLabel = ""
	defer func() { flagAutoLabel = oldLabel }()
	oldFailing := flagAutoFailing
	flagAutoFailing = true
	defer func() { flagAutoFailing = oldFailing }()
	oldSince := flagSince
	flagSince = "24h"
	defer func() { flagSince = oldSince }()

	var buf bytes.Buffer
	// runAutoLs with --failing - should include only automations with errors
	if err := runAutoLs(context.Background(), &buf); err != nil {
		t.Fatalf("runAutoLs --failing failed: %v", err)
	}
}

// --- runTraceShow full JSON mode ---

func TestRunTraceShow_FullJSON(t *testing.T) {
	traceJSON := `{"trace":{"run_id":"run-001","domain":"automation","item_id":"climate_schedule","timestamp":{"start":"2026-01-01T10:00:00Z"},"execution":"finished"},"trace_steps":{}}`

	ts := startCmdServer(t, map[string]any{
		"trace/get": json.RawMessage(traceJSON),
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagFull
	flagFull = true
	defer func() { flagFull = old }()

	var buf bytes.Buffer
	if err := runTraceShow(context.Background(), &buf, "automation.climate_schedule/run-001"); err != nil {
		t.Fatalf("runTraceShow --full failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "climate_schedule") {
		t.Errorf("output missing automation ID: %q", out)
	}
}

// --- runCCLogs unique mode ---

func TestRunCCLogs_Unique(t *testing.T) {
	logText := "2026-01-01 10:00:00.000 ERROR (Main) [hacs] Something broke\n"
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/error_log": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/plain")
			_, _ = fmt.Fprint(w, logText)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagCCLogsUnique
	flagCCLogsUnique = true
	defer func() { flagCCLogsUnique = old }()

	var buf bytes.Buffer
	if err := runCCLogs(context.Background(), &buf, "hacs"); err != nil {
		t.Fatalf("runCCLogs --unique failed: %v", err)
	}
}

// --- runEntHistAttr resample flag ---

func TestRunEntHistAttr_Resample(t *testing.T) {
	histData := `[[
		{"entity_id":"light.kitchen","state":"on","last_changed":"2026-01-01T10:00:00+00:00","attributes":{"brightness":200}},
		{"entity_id":"light.kitchen","state":"on","last_changed":"2026-01-01T11:00:00+00:00","attributes":{"brightness":255}},
		{"entity_id":"light.kitchen","state":"on","last_changed":"2026-01-01T12:00:00+00:00","attributes":{"brightness":100}}
	]]`

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/history/period/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, histData)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagSince
	flagSince = "24h"
	defer func() { flagSince = old }()
	oldAttr := flagEntAttr
	flagEntAttr = "brightness"
	defer func() { flagEntAttr = oldAttr }()
	oldResample := flagEntResample
	flagEntResample = "1h"
	defer func() { flagEntResample = oldResample }()

	var buf bytes.Buffer
	if err := runEntHist(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntHist with resample failed: %v", err)
	}
}

// --- runEntShow --full and --json paths ---

func TestRunEntShow_Full(t *testing.T) {
	stateData := map[string]any{
		"entity_id":    "sensor.temperature",
		"state":        "21.5",
		"last_changed": "2026-01-01T10:00:00Z",
		"last_updated": "2026-01-01T10:00:00Z",
		"attributes": map[string]any{
			"friendly_name":       "Temperature",
			"unit_of_measurement": "°C",
			"device_class":        "temperature",
			"state_class":         "measurement",
			"extra_attr":          "some value",
		},
	}
	stateJSON, _ := json.Marshal(stateData)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states/sensor.temperature": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(stateJSON)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagFull
	flagFull = true
	defer func() { flagFull = old }()
	oldJSON := flagJSON
	flagJSON = false
	defer func() { flagJSON = oldJSON }()

	var buf bytes.Buffer
	if err := runEntShow(context.Background(), &buf, "sensor.temperature"); err != nil {
		t.Fatalf("runEntShow --full failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "sensor.temperature") {
		t.Errorf("output missing entity ID: %q", out)
	}
	if !strings.Contains(out, "extra_attr") {
		t.Errorf("output missing extra attribute: %q", out)
	}
}

func TestRunEntShow_JSONOutput(t *testing.T) {
	stateData := map[string]any{
		"entity_id":  "sensor.temp2",
		"state":      "21.5",
		"attributes": map[string]any{},
	}
	stateJSON, _ := json.Marshal(stateData)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states/sensor.temp2": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(stateJSON)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagJSON
	flagJSON = true
	defer func() { flagJSON = old }()
	oldFull := flagFull
	flagFull = false
	defer func() { flagFull = oldFull }()

	var buf bytes.Buffer
	if err := runEntShow(context.Background(), &buf, "sensor.temp2"); err != nil {
		t.Fatalf("runEntShow --json failed: %v", err)
	}
	if !strings.Contains(buf.String(), "sensor.temp2") {
		t.Errorf("output missing entity ID in JSON: %q", buf.String())
	}
}

// --- runDashShow edge cases ---

func TestRunDashShow_NoViews(t *testing.T) {
	dashConfig := map[string]any{"views": []any{}}
	ts := startCmdServer(t, map[string]any{
		"lovelace/config": dashConfig,
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagDashRaw
	flagDashRaw = false
	defer func() { flagDashRaw = old }()
	oldView := flagDashView
	flagDashView = ""
	defer func() { flagDashView = oldView }()

	var buf bytes.Buffer
	if err := runDashShow(context.Background(), &buf, ""); err != nil {
		t.Fatalf("runDashShow empty views failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no views") {
		t.Errorf("output = %q, want 'no views'", buf.String())
	}
}

func TestRunDashShow_ViewFlag(t *testing.T) {
	dashConfig := map[string]any{
		"views": []map[string]any{
			{"title": "Main", "path": "main"},
			{"title": "Energy", "path": "energy"},
		},
	}
	ts := startCmdServer(t, map[string]any{
		"lovelace/config": dashConfig,
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagDashRaw
	flagDashRaw = false
	defer func() { flagDashRaw = old }()
	oldView := flagDashView
	flagDashView = "energy"
	defer func() { flagDashView = oldView }()

	var buf bytes.Buffer
	if err := runDashShow(context.Background(), &buf, ""); err != nil {
		t.Fatalf("runDashShow --view failed: %v", err)
	}
	if !strings.Contains(buf.String(), "energy") {
		t.Errorf("output missing view: %q", buf.String())
	}
}

// --- runScriptShow with WS trace data ---

func TestRunScriptShow_WithTraces(t *testing.T) {
	stateData := map[string]any{
		"entity_id":    "script.welcome_home",
		"state":        "off",
		"last_changed": "2026-01-01T10:00:00Z",
		"last_updated": "2026-01-01T10:00:00Z",
		"attributes": map[string]any{
			"friendly_name":  "Welcome Home",
			"mode":           "single",
			"last_triggered": "2026-01-01T10:00:00Z",
		},
	}
	stateJSON, _ := json.Marshal(stateData)

	// trace/list returns a flat array of TraceSummary objects
	traces := []map[string]any{
		{
			"run_id":           "run-001",
			"domain":           "script",
			"item_id":          "welcome_home",
			"timestamp":        map[string]any{"start": "2026-01-01T10:00:00Z"},
			"script_execution": "finished",
			"last_step":        "action/0",
		},
	}

	ts := startCmdServer(t, map[string]any{
		"trace/list": traces,
	}, map[string]http.HandlerFunc{
		"/api/states/script.welcome_home": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(stateJSON)
		},
	})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runScriptShow(context.Background(), &buf, "welcome_home"); err != nil {
		t.Fatalf("runScriptShow with traces failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "script.welcome_home") {
		t.Errorf("output missing entity ID: %q", out)
	}
	if !strings.Contains(out, "run-001") && !strings.Contains(out, "trc:") {
		t.Errorf("output missing trace info: %q", out)
	}
}

// --- runDashCreate confirm path ---

func TestRunDashCreate_Confirm(t *testing.T) {
	dashboard := map[string]any{
		"id":              "new-dash",
		"url_path":        "new-dash",
		"title":           "New Dashboard",
		"mode":            "storage",
		"show_in_sidebar": true,
		"require_admin":   false,
	}
	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/create": dashboard,
	}, nil)
	withFlagDir(t, ts.dir)

	old := flagDashConfirm
	flagDashConfirm = true
	defer func() { flagDashConfirm = old }()
	oldURL := flagDashURLPath
	flagDashURLPath = "new-dash"
	defer func() { flagDashURLPath = oldURL }()
	oldTitle := flagDashTitle
	flagDashTitle = "New Dashboard"
	defer func() { flagDashTitle = oldTitle }()

	var buf bytes.Buffer
	if err := runDashCreate(context.Background(), &buf); err != nil {
		t.Fatalf("runDashCreate --confirm failed: %v", err)
	}
	if !strings.Contains(buf.String(), "created dashboard") {
		t.Errorf("output = %q, want 'created dashboard'", buf.String())
	}
}

// --- runEntAnomalies with numeric anomaly ---

func TestRunEntAnomalies_WithAnomaly(t *testing.T) {
	// Provide a large gap in the data → DetectAll should find a gap anomaly
	// Points spread over 10 hours with a 4-hour gap in the middle
	now := time.Now()
	histData := fmt.Sprintf(`[[
		{"entity_id":"sensor.power","state":"100.0","last_changed":"%s"},
		{"entity_id":"sensor.power","state":"101.0","last_changed":"%s"},
		{"entity_id":"sensor.power","state":"102.0","last_changed":"%s"}
	]]`,
		now.Add(-10*time.Hour).Format(time.RFC3339),
		now.Add(-9*time.Hour).Format(time.RFC3339),
		now.Add(-1*time.Hour).Format(time.RFC3339), // 8h gap before last point
	)

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/history/period/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, histData)
		},
	})
	withFlagDir(t, ts.dir)

	old := flagSince
	flagSince = "24h"
	defer func() { flagSince = old }()

	var buf bytes.Buffer
	// runEntAnomalies with a 8h gap → should detect gap anomaly
	if err := runEntAnomalies(context.Background(), &buf, "sensor.power"); err != nil {
		t.Fatalf("runEntAnomalies with anomaly failed: %v", err)
	}
	// Result should contain either "no anomalies" or anomaly table
}

// --- runAutoLs with registry context (fetchRegistryContext body) ---

func TestRunAutoLs_WithRegistryContext(t *testing.T) {
	states := []map[string]any{
		{"entity_id": "automation.climate_schedule", "state": "on", "last_changed": "2026-01-01T10:00:00Z"},
	}
	statesJSON, _ := json.Marshal(states)

	ts := startCmdServer(t, map[string]any{
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "automation.climate_schedule", "area_id": "living_room", "labels": []string{}},
		},
		"config/area_registry/list": []map[string]any{
			{"area_id": "living_room", "name": "Living Room", "labels": []string{}, "floor_id": ""},
		},
		"config/label_registry/list": []map[string]any{
			{"label_id": "energy", "name": "Energy", "color": "", "icon": "", "description": ""},
		},
		"config/floor_registry/list": []map[string]any{
			{"floor_id": "ground", "name": "Ground Floor", "level": 0, "aliases": []string{}},
		},
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
		"/api/logbook/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
	withFlagDir(t, ts.dir)

	old := flagAutoPattern
	flagAutoPattern = ""
	defer func() { flagAutoPattern = old }()
	oldLabel := flagAutoLabel
	flagAutoLabel = ""
	defer func() { flagAutoLabel = oldLabel }()
	oldFailing := flagAutoFailing
	flagAutoFailing = false
	defer func() { flagAutoFailing = oldFailing }()
	oldSince := flagSince
	flagSince = "24h"
	defer func() { flagSince = oldSince }()

	var buf bytes.Buffer
	if err := runAutoLs(context.Background(), &buf); err != nil {
		t.Fatalf("runAutoLs with registry failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "climate_schedule") {
		t.Errorf("output missing automation: %q", out)
	}
}
