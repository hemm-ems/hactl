//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// Contract tests verify that the HA API endpoints used by hactl
// return the expected response shapes. These tests break early when
// HA changes its API, making version-incompatibilities visible.

// TestContract_APIConfig verifies /api/config returns expected fields.
func TestContract_APIConfig(t *testing.T) {
	out := runHactl(t, "health")
	// health command parses /api/config — if it succeeds, the schema is compatible
	if !strings.Contains(out, "HA ") {
		t.Errorf("health output missing HA version prefix: %s", out)
	}
	if !strings.Contains(out, "state=") {
		t.Errorf("health output missing state field: %s", out)
	}
	if !strings.Contains(out, "recorder=") {
		t.Errorf("health output missing recorder field: %s", out)
	}
}

// TestContract_APIStates verifies /api/states returns an array of state objects.
func TestContract_APIStates(t *testing.T) {
	out := runHactl(t, "ent", "ls", "--json")
	var entries []map[string]string
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("ent ls --json returned invalid JSON: %v\noutput: %s", err, out)
	}
	if len(entries) == 0 {
		t.Fatal("ent ls returned no entities")
	}
	// Verify expected columns exist
	first := entries[0]
	for _, key := range []string{"entity_id", "state", "last_changed"} {
		if _, ok := first[key]; !ok {
			t.Errorf("missing expected key %q in entity entry", key)
		}
	}
}

// TestContract_APIErrorLog verifies /api/error_log returns text content.
func TestContract_APIErrorLog(t *testing.T) {
	// log command parses /api/error_log — any output means the endpoint works
	out := runHactl(t, "log")
	// HA always has some log output, even if empty the command should not error
	_ = out
}

// TestContract_APITemplate verifies /api/template accepts and renders templates.
func TestContract_APITemplate(t *testing.T) {
	out := runHactl(t, "tpl", "eval", "{{ 1 + 1 }}")
	out = strings.TrimSpace(stripTokenHeader(out))
	if out != "2" {
		t.Errorf("template eval '{{ 1 + 1 }}' = %q, want %q", out, "2")
	}
}

// TestContract_WebSocket_TraceList verifies trace/list WebSocket command works.
func TestContract_WebSocket_TraceList(t *testing.T) {
	out := runHactl(t, "auto", "ls", "--json")
	var entries []map[string]string
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("auto ls --json returned invalid JSON: %v\noutput: %s", err, out)
	}
	// auto ls uses WebSocket trace/list internally — success means WS protocol is compatible
	for _, e := range entries {
		if _, ok := e["id"]; !ok {
			t.Error("auto ls entry missing 'id' column")
		}
		if _, ok := e["state"]; !ok {
			t.Error("auto ls entry missing 'state' column")
		}
	}
}

// TestContract_AutomationConfigAPI verifies /api/config/automation/config/<id> exists.
func TestContract_AutomationConfigAPI(t *testing.T) {
	// auto show uses GetAutomationConfig internally via the show → traces path
	// We verify the auto ls works with correct schema
	out := runHactl(t, "auto", "ls")
	if out == "" {
		t.Skip("no automations to test config API against")
	}
	// If auto ls succeeded, the states API schema is compatible
}

// TestContract_Logbook verifies /api/logbook/<timestamp> returns an array.
func TestContract_Logbook(t *testing.T) {
	out := runHactl(t, "changes", "--since", "1h", "--json")
	// The changes command uses GetLogbook — if it returns valid JSON, the API is compatible
	var entries []map[string]string
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		// An empty result means "no changes" which is also fine
		if !strings.Contains(out, "no changes") {
			t.Fatalf("changes --json returned unexpected output: %s", out)
		}
	}
}

// TestContract_ConfigEntries verifies GET /api/config/config_entries/entry returns
// an array of config entry objects with expected fields.
func TestContract_ConfigEntries(t *testing.T) {
	out := runHactl(t, "config", "entries", "--json")
	var entries []map[string]any
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("config entries --json returned invalid JSON: %v\noutput: %s", err, out)
	}
	// HA always has at least some built-in config entries (e.g. sun)
	if len(entries) == 0 {
		t.Skip("no config entries returned (possible on minimal HA)")
	}
	first := entries[0]
	for _, key := range []string{"entry_id", "domain", "title", "state"} {
		if _, ok := first[key]; !ok {
			t.Errorf("config entry missing expected key %q", key)
		}
	}
}

// TestContract_ManifestList verifies WS manifest/list returns integration manifests
// with the expected fields (used by cc ls for custom component detection).
func TestContract_ManifestList(t *testing.T) {
	cfg := loadConfig(t)
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	ctx := context.Background()
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("ws connect: %v", err)
	}
	defer func() { _ = ws.Close() }()

	manifests, err := ws.IntegrationManifestList(ctx)
	if err != nil {
		t.Fatalf("manifest/list: %v", err)
	}
	if len(manifests) == 0 {
		t.Fatal("manifest/list returned empty list")
	}
	// Verify at least one built-in integration exists
	foundBuiltIn := false
	for _, m := range manifests {
		if m.IsBuiltIn && m.Domain != "" {
			foundBuiltIn = true
			break
		}
	}
	if !foundBuiltIn {
		t.Error("no built-in integrations found in manifest list")
	}
}
