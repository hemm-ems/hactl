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

// lsColumnsStates is two items of one domain: one renamed in the UI and run,
// one that never ran (HA's null last_triggered).
func lsColumnsStates(domain string) []map[string]any {
	return []map[string]any{
		{"entity_id": domain + ".ran", "state": "on", "last_changed": "2026-11-27T06:00:00Z",
			"attributes": map[string]any{"friendly_name": "Flur Licht", "last_triggered": "2026-11-27T06:31:28.650000+00:00", "id": "cfg_ran"}},
		{"entity_id": domain + ".never", "state": "off", "last_changed": "2026-11-27T06:00:00Z",
			"attributes": map[string]any{"friendly_name": "Never Ran", "last_triggered": nil, "id": "cfg_never"}},
	}
}

func lsColumnsServer(t *testing.T, domain string) *cmdTestServer {
	t.Helper()
	statesJSON, _ := json.Marshal(lsColumnsStates(domain))
	return startCmdServer(t, map[string]any{"trace/list": []any{}}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
		"/api/logbook/": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, "[]")
		},
	})
}

// assertNameAndLastTriggered checks both columns in both renderings (D-235):
// the text table names them, and JSON carries the instant or null.
func assertNameAndLastTriggered(t *testing.T, run func(*bytes.Buffer) error) {
	t.Helper()
	var text bytes.Buffer
	if err := run(&text); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"last_triggered", "name", "Flur Licht", "Never Ran"} {
		if !strings.Contains(text.String(), want) {
			t.Errorf("text listing lacks %q:\n%s", want, text.String())
		}
	}

	withRefFlag(t, &flagJSON)
	var js bytes.Buffer
	if err := run(&js); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err := json.Unmarshal(js.Bytes(), &rows); err != nil {
		t.Fatalf("--json: %v\n%s", err, js.String())
	}
	byID := map[string]map[string]any{}
	for _, r := range rows {
		byID[fmt.Sprint(r["id"])] = r
	}
	ran, never := byID["ran"], byID["never"]
	if ran == nil || never == nil {
		t.Fatalf("rows missing: %s", js.String())
	}
	if ran["name"] != "Flur Licht" || never["name"] != "Never Ran" {
		t.Errorf("name = %v / %v, want HA's friendly_name", ran["name"], never["name"])
	}
	if lt, ok := ran["last_triggered"].(string); !ok || !strings.HasPrefix(lt, "2026-11-27T") {
		t.Errorf("last_triggered = %#v, want the instant", ran["last_triggered"])
	}
	if v, present := never["last_triggered"]; !present || v != nil {
		t.Errorf("last_triggered of a never-run item = %#v (present=%v), want null", v, present)
	}
}

func TestAutoLsCarriesNameAndLastTriggered(t *testing.T) {
	ts := lsColumnsServer(t, "automation")
	withFlagDir(t, ts.dir)
	assertNameAndLastTriggered(t, func(b *bytes.Buffer) error {
		return runAutoLs(listingCmd(context.Background(), "auto", "ls"), b)
	})
}

func TestScriptLsCarriesNameAndLastTriggered(t *testing.T) {
	ts := lsColumnsServer(t, "script")
	withFlagDir(t, ts.dir)
	assertNameAndLastTriggered(t, func(b *bytes.Buffer) error {
		return runScriptLs(listingCmd(context.Background(), "script", "ls"), b)
	})
}
