package cmd

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"
)

// brokenAutoYAML is the issue #68 repro: unparseable Jinja in a condition
// template plus a dead entity ref. HA's validate_config rejects the condition.
const brokenAutoYAML = `id: test_broken
alias: "test broken"
triggers: [{trigger: state, entity_id: sensor.does_not_exist}]
conditions: [{condition: template, value_template: "{{ broken"}]
actions: [{action: logbook.log, data: {name: x, message: y}}]
`

const validAutoYAML = `id: test_valid
alias: "test valid"
triggers: [{trigger: time, at: "06:00:00"}]
conditions: []
actions: [{delay: "00:00:01"}]
`

// startFakeValidateWS stands up a fake HA WebSocket endpoint that completes the
// auth handshake and answers a validate_config command with the given result.
// Connections that send no command (e.g. the companion ingress-auth WS) are
// simply held open until the server closes.
func startFakeValidateWS(t *testing.T, result map[string]any) *httptest.Server {
	t.Helper()
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()

		_ = c.WriteJSON(map[string]string{"type": "auth_required", "ha_version": "2026.4"})
		var authMsg map[string]string
		_ = c.ReadJSON(&authMsg)
		_ = c.WriteJSON(map[string]string{"type": "auth_ok", "ha_version": "2026.4"})

		for {
			var cmd map[string]any
			if readErr := c.ReadJSON(&cmd); readErr != nil {
				return
			}
			if cmd["type"] != "validate_config" {
				continue
			}
			_ = c.WriteJSON(map[string]any{
				"id":      cmd["id"],
				"type":    "result",
				"success": true,
				"result":  result,
			})
		}
	}))
}

// fakeCompanionWriteServer records whether the automation write endpoint was
// hit, so tests can prove a refused create never reaches the companion.
type fakeCompanionWriteServer struct {
	*httptest.Server

	writeHit atomic.Bool
}

func startFakeCompanionWrite(t *testing.T) *fakeCompanionWriteServer {
	t.Helper()
	f := &fakeCompanionWriteServer{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/config/automation") {
			f.writeHit.Store(true)
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"status":"created","id":"test_valid","entity_id":"automation.test_valid","reloaded":true}`)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{}`)
	}))
	return f
}

// writeValidationEnv writes a .env wiring HA_URL to the fake WS server and
// COMPANION_URL to the fake companion server.
func writeValidationEnv(t *testing.T, dir, haURL, companionURL string) {
	t.Helper()
	content := "HA_URL=" + haURL + "\nHA_TOKEN=test\nCOMPANION_URL=" + companionURL + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// setAutoCreateFlags points the create flags at a file/confirm mode and restores
// them (plus flagDir) when the test ends.
func setAutoCreateFlags(t *testing.T, dir, file string, confirm bool) {
	t.Helper()
	oldDir, oldFile, oldConfirm := flagDir, flagAutoFile, flagAutoConfirm
	flagDir, flagAutoFile, flagAutoConfirm = dir, file, confirm
	t.Cleanup(func() { flagDir, flagAutoFile, flagAutoConfirm = oldDir, oldFile, oldConfirm })
}

// TestAutoCreate_InvalidRefused_DryRun proves a candidate HA rejects is refused
// in dry-run mode, before any companion write.
func TestAutoCreate_InvalidRefused_DryRun(t *testing.T) {
	ws := startFakeValidateWS(t, map[string]any{
		"conditions": map[string]any{"valid": false, "error": "invalid template"},
	})
	defer ws.Close()
	cc := startFakeCompanionWrite(t)
	defer cc.Close()

	dir := t.TempDir()
	writeValidationEnv(t, dir, ws.URL, cc.URL)
	yamlFile := writeYAML(t, dir, "broken.yaml", brokenAutoYAML)
	setAutoCreateFlags(t, dir, yamlFile, false)

	var out bytes.Buffer
	err := runAutoCreate(context.Background(), &out)
	if err == nil {
		t.Fatalf("expected refusal for invalid candidate, got nil; output: %s", out.String())
	}
	if !strings.Contains(err.Error(), "HA rejected") {
		t.Errorf("error should report HA rejection, got: %v", err)
	}
	if cc.writeHit.Load() {
		t.Error("companion write endpoint was hit on a refused dry-run create")
	}
	if strings.Contains(out.String(), "would create automation") {
		t.Errorf("refused dry-run must not print 'would create automation', got: %s", out.String())
	}
}

// TestAutoCreate_InvalidRefused_Confirm proves the same in --confirm mode:
// nothing is written to the companion.
func TestAutoCreate_InvalidRefused_Confirm(t *testing.T) {
	ws := startFakeValidateWS(t, map[string]any{
		"conditions": map[string]any{"valid": false, "error": "invalid template"},
	})
	defer ws.Close()
	cc := startFakeCompanionWrite(t)
	defer cc.Close()

	dir := t.TempDir()
	writeValidationEnv(t, dir, ws.URL, cc.URL)
	yamlFile := writeYAML(t, dir, "broken.yaml", brokenAutoYAML)
	setAutoCreateFlags(t, dir, yamlFile, true)

	var out bytes.Buffer
	err := runAutoCreate(context.Background(), &out)
	if err == nil {
		t.Fatalf("expected refusal for invalid candidate, got nil; output: %s", out.String())
	}
	if !strings.Contains(err.Error(), "HA rejected") {
		t.Errorf("error should report HA rejection, got: %v", err)
	}
	if cc.writeHit.Load() {
		t.Error("companion write endpoint was hit on a refused confirmed create")
	}
	if strings.Contains(out.String(), "created automation") {
		t.Errorf("refused confirm must not print 'created automation', got: %s", out.String())
	}
}

// TestAutoCreate_ValidCreates_Confirm proves a candidate that passes validation
// still creates: validation status line printed and the companion write is hit.
func TestAutoCreate_ValidCreates_Confirm(t *testing.T) {
	ws := startFakeValidateWS(t, map[string]any{
		"triggers":   map[string]any{"valid": true, "error": nil},
		"conditions": map[string]any{"valid": true, "error": nil},
		"actions":    map[string]any{"valid": true, "error": nil},
	})
	defer ws.Close()
	cc := startFakeCompanionWrite(t)
	defer cc.Close()

	dir := t.TempDir()
	writeValidationEnv(t, dir, ws.URL, cc.URL)
	yamlFile := writeYAML(t, dir, "valid.yaml", validAutoYAML)
	setAutoCreateFlags(t, dir, yamlFile, true)

	var out bytes.Buffer
	err := runAutoCreate(context.Background(), &out)
	if err != nil {
		t.Fatalf("valid candidate should create, got error: %v; output: %s", err, out.String())
	}
	if !cc.writeHit.Load() {
		t.Error("companion write endpoint was not hit for a valid confirmed create")
	}
	if !strings.Contains(out.String(), "validation: ok (HA validate_config)") {
		t.Errorf("expected validation ok line, got: %s", out.String())
	}
	if !strings.Contains(out.String(), "created automation") {
		t.Errorf("expected 'created automation' for valid create, got: %s", out.String())
	}
}
