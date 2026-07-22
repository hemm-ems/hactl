package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestConfigCommand_NoEnv(t *testing.T) {
	dir := t.TempDir()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"config", "options", "some-entry-id", "--dir", dir})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), ".env") {
		t.Errorf("error = %q, want it to mention .env", err.Error())
	}
}

func TestConfigFlowStep_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"config", "flow-step", "abc", "--data", "not-json", "--dir", dir})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestRenderFlowResult_Form(t *testing.T) {
	raw := `{
		"flow_id": "abc123",
		"type": "form",
		"step_id": "init",
		"handler": "mqtt",
		"data_schema": [
			{"name": "broker", "required": true, "type": "string"},
			{"name": "port", "required": true, "type": "integer", "default": 1883}
		],
		"errors": {}
	}`

	buf := new(bytes.Buffer)
	err := renderFlowResult(buf, []byte(raw))
	if err != nil {
		t.Fatalf("renderFlowResult error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "abc123") {
		t.Errorf("output missing flow_id: %s", out)
	}
	if !strings.Contains(out, "form") {
		t.Errorf("output missing type: %s", out)
	}
	if !strings.Contains(out, "init") {
		t.Errorf("output missing step: %s", out)
	}
	if !strings.Contains(out, "broker") {
		t.Errorf("output missing field 'broker': %s", out)
	}
	if !strings.Contains(out, "port") {
		t.Errorf("output missing field 'port': %s", out)
	}
	if !strings.Contains(out, "1883") {
		t.Errorf("output missing default value '1883': %s", out)
	}
}

func TestRenderFlowResult_CreateEntry(t *testing.T) {
	raw := `{
		"flow_id": "xyz789",
		"type": "create_entry",
		"step_id": "",
		"handler": "mqtt",
		"title": "MQTT",
		"result": {"entry_id": "new-entry-123"}
	}`

	buf := new(bytes.Buffer)
	err := renderFlowResult(buf, []byte(raw))
	if err != nil {
		t.Fatalf("renderFlowResult error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "create_entry") {
		t.Errorf("output missing type create_entry: %s", out)
	}
	if !strings.Contains(out, "new-entry-123") {
		t.Errorf("output missing result entry_id: %s", out)
	}
}

func TestRenderFlowResult_WithErrors(t *testing.T) {
	raw := `{
		"flow_id": "err456",
		"type": "form",
		"step_id": "user",
		"handler": "test",
		"data_schema": [{"name": "host", "required": true, "type": "string"}],
		"errors": {"host": "cannot_connect"}
	}`

	buf := new(bytes.Buffer)
	err := renderFlowResult(buf, []byte(raw))
	if err != nil {
		t.Fatalf("renderFlowResult error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "cannot_connect") {
		t.Errorf("output missing error message: %s", out)
	}
}

func TestConfigEntries_NoEnv(t *testing.T) {
	dir := t.TempDir()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"config", "entries", "--dir", dir})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), ".env") {
		t.Errorf("error = %q, want it to mention .env", err.Error())
	}
}

func TestConfigEntries_DomainFilter(t *testing.T) {
	// Test that --domain flag is registered and accepted
	dir := t.TempDir()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"config", "entries", "--domain", "mqtt", "--dir", dir})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error (no .env), got nil")
	}
	// The error should be about .env, not about unknown flag
	if !strings.Contains(err.Error(), ".env") {
		t.Errorf("error = %q, want .env error not flag error", err.Error())
	}
}

func TestRenderFlowResult_JSON(t *testing.T) {
	raw := `{"flow_id":"j1","type":"form","step_id":"init","handler":"test","data_schema":[]}`

	// Set flagJSON temporarily
	oldJSON := flagJSON
	flagJSON = true
	defer func() { flagJSON = oldJSON }()

	buf := new(bytes.Buffer)
	err := renderFlowResult(buf, []byte(raw))
	if err != nil {
		t.Fatalf("renderFlowResult error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, `"flow_id"`) {
		t.Errorf("JSON output missing flow_id key: %s", out)
	}
}

// Live HA's /api/config/config_entries/entry response carries no "version" key.
// A `version` field on configEntry therefore serialises a fabricated 0 into
// `config show --json`, which reads as "this entry is at schema version 0".
func TestRunConfigShow_JSONHasNoFabricatedVersion(t *testing.T) {
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/config/config_entries/entry": func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, configShowEntries())
		},
		"/api/diagnostics/config_entry/entry1": func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, `{"data":{"options":{"price_entity":"sensor.price"}}}`)
		},
	})
	withFlagDir(t, ts.dir)

	oldJSON := flagJSON
	flagJSON = true
	defer func() { flagJSON = oldJSON }()

	var buf bytes.Buffer
	if err := runConfigShow(context.Background(), &buf, "entry1"); err != nil {
		t.Fatalf("runConfigShow --json failed: %v", err)
	}

	var out struct {
		Entry map[string]json.RawMessage `json:"entry"`
	}
	if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, buf.String())
	}
	if v, ok := out.Entry["version"]; ok {
		t.Errorf("entry.version = %s emitted, but HA sends no version key; --json must not invent one:\n%s", v, buf.String())
	}
}
