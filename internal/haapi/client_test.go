package haapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGetConfig_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"version":"2025.1"}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := string(body); got != `{"version":"2025.1"}` {
		t.Fatalf("body = %q, want %q", got, `{"version":"2025.1"}`)
	}
}

func TestGetConfig_AuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if got != "Bearer test-token" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	_, err := c.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetConfig_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, "bad-token")
	_, err := c.GetConfig(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if want := "401"; !contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err, want)
	}
}

func TestGetErrorLog_Success(t *testing.T) {
	const logText = "2025-01-15 12:00:00 ERROR (MainThread) [homeassistant] something broke"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, logText)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetErrorLog(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := string(body); got != logText {
		t.Fatalf("body = %q, want %q", got, logText)
	}
}

func TestRenderTemplate_Success(t *testing.T) {
	const tpl = "{{ states('sensor.temperature') }}"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/template" {
			t.Errorf("path = %s, want /api/template", r.URL.Path)
		}

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading body: %v", err)
		}
		var payload map[string]string
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("unmarshaling body: %v", err)
		}
		if payload["template"] != tpl {
			t.Errorf("template = %q, want %q", payload["template"], tpl)
		}

		_, _ = fmt.Fprint(w, "42.0")
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	result, err := c.RenderTemplate(context.Background(), tpl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "42.0" {
		t.Fatalf("result = %q, want %q", result, "42.0")
	}
}

func TestRetry_ServerError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry test in short mode")
	}

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := string(body); got != `{"ok":true}` {
		t.Fatalf("body = %q, want %q", got, `{"ok":true}`)
	}
	if n := calls.Load(); n < 2 {
		t.Fatalf("server called %d times, want >= 2", n)
	}
}

func TestRetry_MaxRetriesExceeded(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry test in short mode")
	}

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	_, err := c.GetConfig(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("server called %d times, want 3", n)
	}
}

func TestGetAPIStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/" {
			t.Errorf("path = %q, want /api/", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `{"message":"API running."}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetAPIStatus(context.Background())
	if err != nil {
		t.Fatalf("GetAPIStatus: %v", err)
	}
	if !contains(string(body), "API running") {
		t.Errorf("unexpected body: %q", string(body))
	}
}

func TestGetStates(t *testing.T) {
	statesJSON := `[{"entity_id":"sensor.temp","state":"21.5"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/states" {
			t.Errorf("path = %q, want /api/states", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, statesJSON)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetStates(context.Background())
	if err != nil {
		t.Fatalf("GetStates: %v", err)
	}
	if string(body) != statesJSON {
		t.Errorf("body = %q, want %q", string(body), statesJSON)
	}
}

func TestGetState(t *testing.T) {
	stateJSON := `{"entity_id":"sensor.temp","state":"21.5"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/states/sensor.temp" {
			t.Errorf("path = %q, want /api/states/sensor.temp", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, stateJSON)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetState(context.Background(), "sensor.temp")
	if err != nil {
		t.Fatalf("GetState: %v", err)
	}
	if string(body) != stateJSON {
		t.Errorf("body = %q, want %q", string(body), stateJSON)
	}
}

func TestGetConfigEntries(t *testing.T) {
	entriesJSON := `[{"entry_id":"e1","domain":"mqtt"}]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, entriesJSON)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetConfigEntries(context.Background())
	if err != nil {
		t.Fatalf("GetConfigEntries: %v", err)
	}
	if string(body) != entriesJSON {
		t.Errorf("body = %q, want %q", string(body), entriesJSON)
	}
}

func TestGetAutomationConfig(t *testing.T) {
	configJSON := `{"id":"climate_schedule","alias":"Climate Schedule"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config/automation/config/climate_schedule" {
			t.Errorf("path = %q, want /api/config/automation/config/climate_schedule", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, configJSON)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetAutomationConfig(context.Background(), "climate_schedule")
	if err != nil {
		t.Fatalf("GetAutomationConfig: %v", err)
	}
	if string(body) != configJSON {
		t.Errorf("body = %q, want %q", string(body), configJSON)
	}
}

func TestUpdateAutomationConfig(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/config/automation/config/climate_schedule" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	config := map[string]any{"id": "climate_schedule", "alias": "Updated"}
	if err := c.UpdateAutomationConfig(context.Background(), "climate_schedule", config); err != nil {
		t.Fatalf("UpdateAutomationConfig: %v", err)
	}
}

func TestCallService(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/services/automation/reload" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	if err := c.CallService(context.Background(), "automation", "reload", nil); err != nil {
		t.Fatalf("CallService: %v", err)
	}
}

func TestGetIssues(t *testing.T) {
	issuesJSON := `{"issues":[]}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/repairs/issues" {
			t.Errorf("path = %q, want /api/repairs/issues", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, issuesJSON)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetIssues(context.Background())
	if err != nil {
		t.Fatalf("GetIssues: %v", err)
	}
	if string(body) != issuesJSON {
		t.Errorf("body = %q, want %q", string(body), issuesJSON)
	}
}

func TestGetEvents(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/events" {
			t.Errorf("path = %q, want /api/events", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `[{"event_type":"state_changed"}]`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetEvents(context.Background())
	if err != nil {
		t.Fatalf("GetEvents: %v", err)
	}
	if len(body) == 0 {
		t.Error("GetEvents returned empty body")
	}
}

func TestGetLogbook(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !contains(r.URL.Path, "/api/logbook/") {
			t.Errorf("path = %q, want to contain /api/logbook/", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetLogbook(context.Background(), "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z")
	if err != nil {
		t.Fatalf("GetLogbook: %v", err)
	}
	if string(body) != "[]" {
		t.Errorf("body = %q, want []", string(body))
	}
}

// --- Flow operations ---

func TestStartOptionsFlow(t *testing.T) {
	flowJSON := `{"flow_id":"opt1","type":"form","step_id":"init","handler":"mqtt"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/config/config_entries/options/flow" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, flowJSON)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.StartOptionsFlow(context.Background(), "entry-123")
	if err != nil {
		t.Fatalf("StartOptionsFlow: %v", err)
	}
	if string(body) != flowJSON {
		t.Errorf("body = %q, want %q", string(body), flowJSON)
	}
}

func TestStartConfigFlow(t *testing.T) {
	flowJSON := `{"flow_id":"cfg1","type":"form","step_id":"init","handler":"mqtt"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/config/config_entries/flow" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, flowJSON)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.StartConfigFlow(context.Background(), "mqtt")
	if err != nil {
		t.Fatalf("StartConfigFlow: %v", err)
	}
	if string(body) != flowJSON {
		t.Errorf("body = %q, want %q", string(body), flowJSON)
	}
}

func TestStartConfigFlowOnce(t *testing.T) {
	flowJSON := `{"flow_id":"once1","type":"abort","handler":"mqtt"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, flowJSON)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.StartConfigFlowOnce(context.Background(), "mqtt")
	if err != nil {
		t.Fatalf("StartConfigFlowOnce: %v", err)
	}
	if string(body) != flowJSON {
		t.Errorf("body = %q, want %q", string(body), flowJSON)
	}
}

func TestStepFlow(t *testing.T) {
	flowJSON := `{"flow_id":"f1","type":"create_entry"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/config/config_entries/flow/f1" {
			t.Errorf("path = %q", r.URL.Path)
		}
		_, _ = fmt.Fprint(w, flowJSON)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.StepFlow(context.Background(), "f1", false, nil)
	if err != nil {
		t.Fatalf("StepFlow: %v", err)
	}
	if string(body) != flowJSON {
		t.Errorf("body = %q, want %q", string(body), flowJSON)
	}
}

func TestInspectFlow(t *testing.T) {
	flowJSON := `{"flow_id":"f1","type":"form","step_id":"init"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		_, _ = fmt.Fprint(w, flowJSON)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.InspectFlow(context.Background(), "f1", false)
	if err != nil {
		t.Fatalf("InspectFlow: %v", err)
	}
	if string(body) != flowJSON {
		t.Errorf("body = %q, want %q", string(body), flowJSON)
	}
}

// contains reports whether s contains substr.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
