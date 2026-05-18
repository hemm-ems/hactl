package companion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealth(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/health" {
			t.Errorf("path = %q, want /v1/health", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok", Version: "0.2.0"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if h.Status != "ok" {
		t.Errorf("status = %q, want ok", h.Status)
	}
	if h.Version != "0.2.0" {
		t.Errorf("version = %q, want 0.2.0", h.Version)
	}
}

func TestListConfigFiles(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(ConfigFilesResponse{Files: []string{"configuration.yaml", "automations.yaml"}})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.ListConfigFiles(context.Background())
	if err != nil {
		t.Fatalf("ListConfigFiles: %v", err)
	}
	if len(r.Files) != 2 {
		t.Errorf("files count = %d, want 2", len(r.Files))
	}
}

func TestReadConfigFile(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("path"); got != "configuration.yaml" {
			t.Errorf("path param = %q, want configuration.yaml", got)
		}
		if got := r.URL.Query().Get("resolve"); got != "true" { //nolint:goconst
			t.Errorf("resolve param = %q, want true", got)
		}
		_ = json.NewEncoder(w).Encode(ConfigFileResponse{Path: "configuration.yaml", Content: "homeassistant:\n"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	f, err := c.ReadConfigFile(context.Background(), "configuration.yaml")
	if err != nil {
		t.Fatalf("ReadConfigFile: %v", err)
	}
	if f.Content == "" {
		t.Error("content is empty")
	}
}

func TestReadConfigFileRaw(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("resolve"); got != "false" { //nolint:goconst
			t.Errorf("resolve param = %q, want false", got)
		}
		_ = json.NewEncoder(w).Encode(ConfigFileResponse{Path: "template.yaml", Content: "!include sensors.yaml\n"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	f, err := c.ReadConfigFileRaw(context.Background(), "template.yaml")
	if err != nil {
		t.Fatalf("ReadConfigFileRaw: %v", err)
	}
	if f.Content == "" {
		t.Error("content is empty")
	}
}

func TestReadConfigFile_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "File not found", http.StatusNotFound)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	_, err := c.ReadConfigFile(context.Background(), "nonexistent.yaml")
	if err == nil {
		t.Error("expected error for 404")
	}
}

func TestWriteConfigFile_DryRun(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %q, want PUT", r.Method)
		}
		if got := r.URL.Query().Get("dry_run"); got != "true" {
			t.Errorf("dry_run = %q, want true", got)
		}
		_ = json.NewEncoder(w).Encode(ConfigWriteResponse{Status: "dry_run", Diff: "--- a/test.yaml\n+++ b/test.yaml\n"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	wr, err := c.WriteConfigFile(context.Background(), "test.yaml", "key: value\n", true)
	if err != nil {
		t.Fatalf("WriteConfigFile dry_run: %v", err)
	}
	if wr.Status != "dry_run" { //nolint:goconst
		t.Errorf("status = %q, want dry_run", wr.Status)
	}
}

func TestWriteConfigFile_Apply(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("dry_run"); got != "false" {
			t.Errorf("dry_run = %q, want false", got)
		}
		_ = json.NewEncoder(w).Encode(ConfigWriteResponse{Status: "applied", Backup: "test.yaml.bak.20260428T120000"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	wr, err := c.WriteConfigFile(context.Background(), "test.yaml", "key: value\n", false)
	if err != nil {
		t.Fatalf("WriteConfigFile apply: %v", err)
	}
	if wr.Status != "applied" {
		t.Errorf("status = %q, want applied", wr.Status)
	}
	if wr.Backup == "" {
		t.Error("backup is empty")
	}
}

func TestBearerTokenSent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer my-secret-token" {
			t.Errorf("Authorization = %q, want Bearer my-secret-token", auth)
		}
		_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok", Version: "0.2.0"})
	}))
	defer srv.Close()

	c := New(srv.URL, "my-secret-token")
	_, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
}

// --- Template CRUD tests ---

func TestListTemplates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/config/templates" {
			t.Errorf("path = %q, want /v1/config/templates", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(TemplatesResponse{Templates: []TemplateDefinition{
			{UniqueID: "temp_power", Name: "Power Sensor", Domain: "sensor", State: "{{ states('sensor.raw') | float }}"},
		}})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.ListTemplates(context.Background())
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(r.Templates) != 1 {
		t.Fatalf("templates count = %d, want 1", len(r.Templates))
	}
	if r.Templates[0].UniqueID != "temp_power" { //nolint:goconst
		t.Errorf("unique_id = %q, want temp_power", r.Templates[0].UniqueID)
	}
}

func TestGetTemplate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("id"); got != "temp_power" {
			t.Errorf("id = %q, want temp_power", got)
		}
		_ = json.NewEncoder(w).Encode(TemplateResponse{UniqueID: "temp_power", Content: "name: Power\nstate: '{{ 1 }}'\n"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.GetTemplate(context.Background(), "temp_power")
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if r.UniqueID != "temp_power" {
		t.Errorf("unique_id = %q, want temp_power", r.UniqueID)
	}
	if r.Content == "" {
		t.Error("content is empty")
	}
}

func TestWriteTemplate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %q, want PUT", r.Method)
		}
		if got := r.URL.Query().Get("id"); got != "temp_power" {
			t.Errorf("id = %q, want temp_power", got)
		}
		if got := r.URL.Query().Get("dry_run"); got != "true" {
			t.Errorf("dry_run = %q, want true", got)
		}
		_ = json.NewEncoder(w).Encode(ConfigDeleteResponse{Status: "dry_run"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.WriteTemplate(context.Background(), "temp_power", "state: '{{ 2 }}'\n", true)
	if err != nil {
		t.Fatalf("WriteTemplate: %v", err)
	}
	if r.Status != "dry_run" {
		t.Errorf("status = %q, want dry_run", r.Status)
	}
}

func TestCreateTemplate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		if got := r.URL.Query().Get("domain"); got != "sensor" {
			t.Errorf("domain = %q, want sensor", got)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(TemplateCreateResponse{Status: "created", UniqueID: "new_sensor_1"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.CreateTemplate(context.Background(), "name: Test\nstate: '{{ 1 }}'\n", "sensor")
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if r.Status != "created" { //nolint:goconst
		t.Errorf("status = %q, want created", r.Status)
	}
	if r.UniqueID == "" {
		t.Error("unique_id is empty")
	}
}

func TestDeleteTemplate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %q, want DELETE", r.Method)
		}
		if got := r.URL.Query().Get("id"); got != "temp_power" {
			t.Errorf("id = %q, want temp_power", got)
		}
		_ = json.NewEncoder(w).Encode(ConfigDeleteResponse{Status: "deleted"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.DeleteTemplate(context.Background(), "temp_power")
	if err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if r.Status != "deleted" { //nolint:goconst
		t.Errorf("status = %q, want deleted", r.Status)
	}
}

// --- Script CRUD tests ---

func TestListScriptDefs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/config/scripts" {
			t.Errorf("path = %q, want /v1/config/scripts", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(ScriptsResponse{Scripts: []ScriptDefinition{
			{ID: "notify_all", Alias: "Notify All", Mode: "single"},
		}})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.ListScriptDefs(context.Background())
	if err != nil {
		t.Fatalf("ListScriptDefs: %v", err)
	}
	if len(r.Scripts) != 1 {
		t.Fatalf("scripts count = %d, want 1", len(r.Scripts))
	}
	if r.Scripts[0].ID != "notify_all" { //nolint:goconst
		t.Errorf("id = %q, want notify_all", r.Scripts[0].ID)
	}
}

func TestGetScriptDef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("id"); got != "notify_all" {
			t.Errorf("id = %q, want notify_all", got)
		}
		_ = json.NewEncoder(w).Encode(ScriptResponse{ID: "notify_all", Content: "alias: Notify All\nmode: single\n"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.GetScriptDef(context.Background(), "notify_all")
	if err != nil {
		t.Fatalf("GetScriptDef: %v", err)
	}
	if r.ID != "notify_all" {
		t.Errorf("id = %q, want notify_all", r.ID)
	}
}

func TestWriteScriptDef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %q, want PUT", r.Method)
		}
		if got := r.URL.Query().Get("dry_run"); got != "false" {
			t.Errorf("dry_run = %q, want false", got)
		}
		_ = json.NewEncoder(w).Encode(ConfigDeleteResponse{Status: "applied"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.WriteScriptDef(context.Background(), "notify_all", "alias: Notify\n", false)
	if err != nil {
		t.Fatalf("WriteScriptDef: %v", err)
	}
	if r.Status != "applied" {
		t.Errorf("status = %q, want applied", r.Status)
	}
}

func TestCreateScriptDef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(ScriptCreateResponse{Status: "created", ID: "new_script"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.CreateScriptDef(context.Background(), "alias: New\nmode: single\n")
	if err != nil {
		t.Fatalf("CreateScriptDef: %v", err)
	}
	if r.Status != "created" {
		t.Errorf("status = %q, want created", r.Status)
	}
}

func TestDeleteScriptDef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %q, want DELETE", r.Method)
		}
		_ = json.NewEncoder(w).Encode(ConfigDeleteResponse{Status: "deleted"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.DeleteScriptDef(context.Background(), "notify_all")
	if err != nil {
		t.Fatalf("DeleteScriptDef: %v", err)
	}
	if r.Status != "deleted" {
		t.Errorf("status = %q, want deleted", r.Status)
	}
}

// --- Automation CRUD tests ---

func TestListAutomationDefs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/config/automations" {
			t.Errorf("path = %q, want /v1/config/automations", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(AutomationsResponse{Automations: []AutomationDefinition{
			{ID: "auto_1", Alias: "Motion Light", Mode: "single"},
		}})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.ListAutomationDefs(context.Background())
	if err != nil {
		t.Fatalf("ListAutomationDefs: %v", err)
	}
	if len(r.Automations) != 1 {
		t.Fatalf("automations count = %d, want 1", len(r.Automations))
	}
}

func TestGetAutomationDef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("id"); got != "auto_1" {
			t.Errorf("id = %q, want auto_1", got)
		}
		_ = json.NewEncoder(w).Encode(AutomationResponse{ID: "auto_1", Content: "alias: Motion Light\ntrigger:\n"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.GetAutomationDef(context.Background(), "auto_1")
	if err != nil {
		t.Fatalf("GetAutomationDef: %v", err)
	}
	if r.ID != "auto_1" {
		t.Errorf("id = %q, want auto_1", r.ID)
	}
}

func TestWriteAutomationDef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("method = %q, want PUT", r.Method)
		}
		if got := r.URL.Query().Get("dry_run"); got != "true" {
			t.Errorf("dry_run = %q, want true", got)
		}
		_ = json.NewEncoder(w).Encode(ConfigDeleteResponse{Status: "dry_run"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.WriteAutomationDef(context.Background(), "auto_1", "alias: Updated\n", true)
	if err != nil {
		t.Fatalf("WriteAutomationDef: %v", err)
	}
	if r.Status != "dry_run" {
		t.Errorf("status = %q, want dry_run", r.Status)
	}
}

func TestCreateAutomationDef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %q, want POST", r.Method)
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(AutomationCreateResponse{Status: "created", ID: "new_auto"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.CreateAutomationDef(context.Background(), "alias: New Auto\ntrigger:\n")
	if err != nil {
		t.Fatalf("CreateAutomationDef: %v", err)
	}
	if r.Status != "created" {
		t.Errorf("status = %q, want created", r.Status)
	}
}

func TestDeleteAutomationDef(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Errorf("method = %q, want DELETE", r.Method)
		}
		_ = json.NewEncoder(w).Encode(ConfigDeleteResponse{Status: "deleted"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	r, err := c.DeleteAutomationDef(context.Background(), "auto_1")
	if err != nil {
		t.Fatalf("DeleteAutomationDef: %v", err)
	}
	if r.Status != "deleted" {
		t.Errorf("status = %q, want deleted", r.Status)
	}
}

func TestRetryOn5xx(t *testing.T) {
	attempts := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts++
		if attempts < 3 {
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
		_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok", Version: "0.2.0"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health after retries: %v", err)
	}
	if h.Status != "ok" {
		t.Errorf("status = %q, want ok", h.Status)
	}
	if attempts != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
}
