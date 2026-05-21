package haapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// startWSTestServer creates a test WS server that handles auth and delegates command handling.
func startWSTestServer(t *testing.T, handler func(c *websocket.Conn, cmd map[string]any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer func() { _ = c.Close() }()

		// Auth flow
		_ = c.WriteJSON(map[string]string{"type": "auth_required", "ha_version": "2026.4"})
		var authMsg map[string]string
		_ = c.ReadJSON(&authMsg)
		_ = c.WriteJSON(map[string]string{"type": "auth_ok", "ha_version": "2026.4"})

		// Read command and delegate
		var cmd map[string]any
		if err := c.ReadJSON(&cmd); err != nil {
			t.Errorf("reading command: %v", err)
			return
		}
		handler(c, cmd)
	}))
}

// connectWSTest creates and connects a WS client to the test server.
func connectWSTest(t *testing.T, srv *httptest.Server) *WSClient {
	t.Helper()
	wsURL := "http" + strings.TrimPrefix(srv.URL, "http")
	ws := NewWSClient(wsURL, "tok")
	if err := ws.Connect(context.Background()); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	return ws
}

// sendWSResult sends a success result for the given command.
func sendWSResult(t *testing.T, c *websocket.Conn, cmd map[string]any, data any) {
	t.Helper()
	resultJSON, err := json.Marshal(data)
	if err != nil {
		t.Fatalf("marshalling result: %v", err)
	}
	_ = c.WriteJSON(map[string]any{
		"id":      cmd["id"],
		"type":    "result",
		"success": true,
		"result":  json.RawMessage(resultJSON),
	})
}

// sendWSError sends an error result for the given command.
func sendWSError(c *websocket.Conn, cmd map[string]any, code, message string) {
	_ = c.WriteJSON(map[string]any{
		"id":      cmd["id"],
		"type":    "result",
		"success": false,
		"error":   map[string]string{"code": code, "message": message},
	})
}

func TestWSClient_AuthSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer func() { _ = c.Close() }()

		// Send auth_required
		_ = c.WriteJSON(map[string]string{
			"type":       "auth_required",
			"ha_version": "2025.1",
		})

		// Read auth message
		var authMsg map[string]string
		if err := c.ReadJSON(&authMsg); err != nil {
			t.Errorf("reading auth: %v", err)
			return
		}
		if authMsg["type"] != "auth" {
			t.Errorf("expected auth message, got %q", authMsg["type"])
			return
		}

		// Send auth_ok
		_ = c.WriteJSON(map[string]string{
			"type":       "auth_ok",
			"ha_version": "2025.1",
		})
	}))
	defer srv.Close()

	wsURL := "http" + strings.TrimPrefix(srv.URL, "http")
	ws := NewWSClient(wsURL, "test-token")
	err := ws.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect failed: %v", err)
	}
	_ = ws.Close()
}

func TestWSClient_AuthFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade failed: %v", err)
			return
		}
		defer func() { _ = c.Close() }()

		_ = c.WriteJSON(map[string]string{
			"type":       "auth_required",
			"ha_version": "2025.1",
		})

		var authMsg map[string]string
		_ = c.ReadJSON(&authMsg)

		_ = c.WriteJSON(map[string]string{
			"type":    "auth_invalid",
			"message": "Invalid access token",
		})
	}))
	defer srv.Close()

	wsURL := "http" + strings.TrimPrefix(srv.URL, "http")
	ws := NewWSClient(wsURL, "bad-token")
	err := ws.Connect(context.Background())
	if err == nil {
		_ = ws.Close()
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Errorf("error = %q, want it to contain 'authentication failed'", err.Error())
	}
}

func TestWSClient_TraceList(t *testing.T) {
	// HA returns a flat array of trace summaries (not a map).
	traceData := []TraceSummary{
		{
			RunID:     "run1",
			Domain:    "automation",
			ItemID:    "test_auto",
			State:     "stopped",
			Execution: "finished",
			Trigger:   "time_pattern",
			LastStep:  "action/0",
			Timestamp: TraceSummaryTimestamp{
				Start:  "2026-04-16T09:42:00+00:00",
				Finish: "2026-04-16T09:42:01+00:00",
			},
		},
	}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "trace/list" {
			t.Errorf("expected trace/list, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, traceData)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.TraceList(context.Background(), "automation")
	if err != nil {
		t.Fatalf("TraceList failed: %v", err)
	}

	traces, ok := result["automation.test_auto"]
	if !ok {
		t.Fatal("expected traces for automation.test_auto")
	}
	if len(traces) != 1 {
		t.Fatalf("expected 1 trace, got %d", len(traces))
	}
	if traces[0].RunID != "run1" {
		t.Errorf("run_id = %q, want %q", traces[0].RunID, "run1")
	}
	if traces[0].Execution != "finished" {
		t.Errorf("execution = %q, want %q", traces[0].Execution, "finished")
	}
}

func TestWSClient_TraceList_Empty(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		// HA returns [] when no traces exist.
		sendWSResult(t, c, cmd, []TraceSummary{})
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.TraceList(context.Background(), "automation")
	if err != nil {
		t.Fatalf("TraceList failed: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty result, got %d keys", len(result))
	}
}

func TestWSClient_TraceList_MultiAutomation(t *testing.T) {
	traceData := []TraceSummary{
		{
			RunID:  "run1",
			Domain: "automation", ItemID: "lights",
			State: "stopped", Execution: "finished", Trigger: "state",
			LastStep:  "action/0",
			Timestamp: TraceSummaryTimestamp{Start: "2026-04-16T09:00:00+00:00", Finish: "2026-04-16T09:00:01+00:00"},
		},
		{
			RunID:  "run2",
			Domain: "automation", ItemID: "lights",
			State: "stopped", Execution: "finished", Trigger: "state",
			LastStep:  "action/0",
			Timestamp: TraceSummaryTimestamp{Start: "2026-04-16T10:00:00+00:00", Finish: "2026-04-16T10:00:01+00:00"},
		},
		{
			RunID:  "run3",
			Domain: "automation", ItemID: "climate",
			State: "stopped", Execution: "error", Trigger: "time",
			LastStep:  "condition/0",
			Timestamp: TraceSummaryTimestamp{Start: "2026-04-16T11:00:00+00:00", Finish: "2026-04-16T11:00:01+00:00"},
			Error:     "template error",
		},
	}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		sendWSResult(t, c, cmd, traceData)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.TraceList(context.Background(), "automation")
	if err != nil {
		t.Fatalf("TraceList failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 automation keys, got %d", len(result))
	}
	if len(result["automation.lights"]) != 2 {
		t.Errorf("lights traces = %d, want 2", len(result["automation.lights"]))
	}
	if len(result["automation.climate"]) != 1 {
		t.Errorf("climate traces = %d, want 1", len(result["automation.climate"]))
	}
	if result["automation.climate"][0].Error != "template error" {
		t.Errorf("climate error = %q, want %q", result["automation.climate"][0].Error, "template error")
	}
}

func TestWSClient_SystemLogList(t *testing.T) {
	logData := []SystemLogEntry{
		{
			Name:          "homeassistant.components.recorder",
			Message:       []string{"Unable to find entity"},
			Level:         "ERROR",
			Source:        []any{"recorder/core.py", float64(195)},
			Timestamp:     1745308920.123,
			Exception:     "",
			Count:         3,
			FirstOccurred: 1745308800.0,
		},
		{
			Name:          "custom_components.hacs.base",
			Message:       []string{"Rate limit exceeded", "Try again later"},
			Level:         "WARNING",
			Source:        []any{"hacs/base.py", float64(42)},
			Timestamp:     1745308950.456,
			Exception:     "Traceback...",
			Count:         1,
			FirstOccurred: 1745308950.456,
		},
	}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "system_log/list" {
			t.Errorf("expected system_log/list, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, logData)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.SystemLogList(context.Background())
	if err != nil {
		t.Fatalf("SystemLogList failed: %v", err)
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result[0].Name != "homeassistant.components.recorder" {
		t.Errorf("name = %q, want %q", result[0].Name, "homeassistant.components.recorder")
	}
	if result[0].Count != 3 {
		t.Errorf("count = %d, want 3", result[0].Count)
	}
	if result[1].Level != "WARNING" {
		t.Errorf("level = %q, want %q", result[1].Level, "WARNING")
	}
	if len(result[1].Message) != 2 {
		t.Errorf("message lines = %d, want 2", len(result[1].Message))
	}
}

func TestWSClient_SystemLogList_Error(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		sendWSError(c, cmd, "unknown_command", "Unknown command.")
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	_, err := ws.SystemLogList(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "system_log/list failed") {
		t.Errorf("error = %q, want it to contain 'system_log/list failed'", err.Error())
	}
}

func TestWSClient_EntityRegistryList(t *testing.T) {
	entries := []EntityRegistryEntry{
		{EntityID: "light.kitchen", AreaID: "kitchen", Labels: []string{"lighting"}, Platform: "hue"},
		{EntityID: "sensor.temp", AreaID: "living_room", Platform: "mqtt"},
	}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "config/entity_registry/list" {
			t.Errorf("expected config/entity_registry/list, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, entries)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.EntityRegistryList(context.Background())
	if err != nil {
		t.Fatalf("EntityRegistryList failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}
	if result[0].EntityID != "light.kitchen" {
		t.Errorf("entry[0].EntityID = %q, want light.kitchen", result[0].EntityID)
	}
	if result[0].AreaID != "kitchen" {
		t.Errorf("entry[0].AreaID = %q, want kitchen", result[0].AreaID)
	}
	if len(result[0].Labels) != 1 || result[0].Labels[0] != "lighting" {
		t.Errorf("entry[0].Labels = %v, want [lighting]", result[0].Labels)
	}
}

func TestWSClient_AreaRegistryList(t *testing.T) {
	areas := []AreaEntry{
		{AreaID: "kitchen", Name: "Kitchen", FloorID: "ground"},
		{AreaID: "living_room", Name: "Living Room"},
	}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "config/area_registry/list" {
			t.Errorf("expected config/area_registry/list, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, areas)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.AreaRegistryList(context.Background())
	if err != nil {
		t.Fatalf("AreaRegistryList failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 areas, got %d", len(result))
	}
	if result[0].Name != "Kitchen" {
		t.Errorf("area[0].Name = %q, want Kitchen", result[0].Name)
	}
	if result[0].FloorID != "ground" {
		t.Errorf("area[0].FloorID = %q, want ground", result[0].FloorID)
	}
}

func TestWSClient_LabelRegistryList(t *testing.T) {
	labels := []LabelEntry{
		{LabelID: "energy", Name: "Energy", Color: "green", Icon: "mdi:flash", Description: "Energy monitoring"},
	}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		sendWSResult(t, c, cmd, labels)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.LabelRegistryList(context.Background())
	if err != nil {
		t.Fatalf("LabelRegistryList failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 label, got %d", len(result))
	}
	if result[0].LabelID != "energy" {
		t.Errorf("label.LabelID = %q, want energy", result[0].LabelID)
	}
	if result[0].Color != "green" {
		t.Errorf("label.Color = %q, want green", result[0].Color)
	}
	if result[0].Description != "Energy monitoring" {
		t.Errorf("label.Description = %q, want 'Energy monitoring'", result[0].Description)
	}
}

func TestWSClient_FloorRegistryList(t *testing.T) {
	level := 0
	floors := []FloorEntry{
		{FloorID: "ground", Name: "Ground Floor", Level: &level},
	}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "config/floor_registry/list" {
			t.Errorf("expected config/floor_registry/list, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, floors)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.FloorRegistryList(context.Background())
	if err != nil {
		t.Fatalf("FloorRegistryList failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 floor, got %d", len(result))
	}
	if result[0].Name != "Ground Floor" {
		t.Errorf("floor[0].Name = %q, want Ground Floor", result[0].Name)
	}
	if result[0].Level == nil || *result[0].Level != 0 {
		t.Errorf("floor[0].Level = %v, want 0", result[0].Level)
	}
}

func TestWSClient_SendCommand_Error(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		sendWSError(c, cmd, "invalid_info", "Entity not found")
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	_, err := ws.EntityRegistryList(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Entity not found") {
		t.Errorf("error = %q, want it to contain 'Entity not found'", err.Error())
	}
}

func TestWSClient_LabelRegistryCreate(t *testing.T) {
	want := LabelEntry{LabelID: "new_label", Name: "Energy", Color: "green", Icon: "mdi:flash"}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "config/label_registry/create" {
			t.Errorf("expected label create, got %q", cmd["type"])
			return
		}
		if cmd["name"] != "Energy" {
			t.Errorf("name = %q, want 'Energy'", cmd["name"])
		}
		sendWSResult(t, c, cmd, want)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	entry, err := ws.LabelRegistryCreate(context.Background(), "Energy", "green", "mdi:flash", "")
	if err != nil {
		t.Fatalf("LabelRegistryCreate failed: %v", err)
	}
	if entry.LabelID != "new_label" {
		t.Errorf("LabelID = %q, want 'new_label'", entry.LabelID)
	}
	if entry.Color != "green" {
		t.Errorf("Color = %q, want 'green'", entry.Color)
	}
}

func TestWSClient_LabelRegistryDelete(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "config/label_registry/delete" {
			t.Errorf("expected label delete, got %q", cmd["type"])
			return
		}
		if cmd["label_id"] != "my_label" {
			t.Errorf("label_id = %q, want 'my_label'", cmd["label_id"])
		}
		sendWSResult(t, c, cmd, nil)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	if err := ws.LabelRegistryDelete(context.Background(), "my_label"); err != nil {
		t.Fatalf("LabelRegistryDelete failed: %v", err)
	}
}

func TestWSClient_AreaRegistryCreate(t *testing.T) {
	want := AreaEntry{AreaID: "new_kitchen", Name: "Kitchen"}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "config/area_registry/create" {
			t.Errorf("expected area create, got %q", cmd["type"])
			return
		}
		if cmd["name"] != "Kitchen" {
			t.Errorf("name = %q, want 'Kitchen'", cmd["name"])
		}
		sendWSResult(t, c, cmd, want)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	entry, err := ws.AreaRegistryCreate(context.Background(), "Kitchen", "", "")
	if err != nil {
		t.Fatalf("AreaRegistryCreate failed: %v", err)
	}
	if entry.Name != "Kitchen" {
		t.Errorf("Name = %q, want 'Kitchen'", entry.Name)
	}
}

func TestWSClient_AreaRegistryDelete(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		sendWSResult(t, c, cmd, nil)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	if err := ws.AreaRegistryDelete(context.Background(), "kitchen"); err != nil {
		t.Fatalf("AreaRegistryDelete failed: %v", err)
	}
}

func TestWSClient_FloorRegistryCreate(t *testing.T) {
	level := 1
	want := FloorEntry{FloorID: "first_floor", Name: "First Floor", Level: &level}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "config/floor_registry/create" {
			t.Errorf("expected floor create, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, want)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	entry, err := ws.FloorRegistryCreate(context.Background(), "First Floor", "", &level)
	if err != nil {
		t.Fatalf("FloorRegistryCreate failed: %v", err)
	}
	if entry.Name != "First Floor" {
		t.Errorf("Name = %q, want 'First Floor'", entry.Name)
	}
}

func TestWSClient_FloorRegistryDelete(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		sendWSResult(t, c, cmd, nil)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	if err := ws.FloorRegistryDelete(context.Background(), "ground"); err != nil {
		t.Fatalf("FloorRegistryDelete failed: %v", err)
	}
}

func TestWSClient_DeviceRegistryList(t *testing.T) {
	devices := []DeviceRegistryEntry{
		{ID: "dev1", Name: "My Device", AreaID: "kitchen"},
	}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "config/device_registry/list" {
			t.Errorf("expected device list, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, devices)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.DeviceRegistryList(context.Background())
	if err != nil {
		t.Fatalf("DeviceRegistryList failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 device, got %d", len(result))
	}
	if result[0].ID != "dev1" {
		t.Errorf("ID = %q, want 'dev1'", result[0].ID)
	}
}

func TestWSClient_EntityRegistryUpdate(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "config/entity_registry/update" {
			t.Errorf("expected entity update, got %q", cmd["type"])
			return
		}
		if cmd["entity_id"] != "light.kitchen" {
			t.Errorf("entity_id = %q, want 'light.kitchen'", cmd["entity_id"])
		}
		sendWSResult(t, c, cmd, map[string]any{"entity_id": "light.kitchen"})
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	err := ws.EntityRegistryUpdate(context.Background(), "light.kitchen", map[string]any{"area_id": "bedroom"})
	if err != nil {
		t.Fatalf("EntityRegistryUpdate failed: %v", err)
	}
}

func TestWSClient_TraceGet(t *testing.T) {
	traceJSON := json.RawMessage(`{"run_id":"run1","domain":"automation","item_id":"test","state":"stopped"}`)

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "trace/get" {
			t.Errorf("expected trace/get, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, traceJSON)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	data, err := ws.TraceGet(context.Background(), "automation", "test", "run1")
	if err != nil {
		t.Fatalf("TraceGet failed: %v", err)
	}
	if len(data) == 0 {
		t.Error("TraceGet returned empty data")
	}
}

func TestWSClient_CheckConfig_OK(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "call_service" {
			t.Errorf("expected call_service, got %q", cmd["type"])
			return
		}
		if cmd["service"] != "check_config" {
			t.Errorf("service = %q, want 'check_config'", cmd["service"])
			return
		}
		_ = c.WriteJSON(map[string]any{
			"id":      cmd["id"],
			"type":    "result",
			"success": true,
			"result":  map[string]any{"result": "valid"},
		})
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	valid, err := ws.CheckConfig(context.Background())
	if err != nil {
		t.Fatalf("CheckConfig: %v", err)
	}
	if !valid {
		t.Error("CheckConfig returned false, want true")
	}
}

func TestWSClient_CheckConfig_Failure(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		_ = c.WriteJSON(map[string]any{
			"id":      cmd["id"],
			"type":    "result",
			"success": false,
			"error":   map[string]string{"code": "service_error", "message": "check_config failed"},
		})
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	_, err := ws.CheckConfig(context.Background())
	if err == nil {
		t.Fatal("expected error from failed check_config")
	}
}

func TestWSClient_Close_Unconnected(t *testing.T) {
	ws := NewWSClient("http://localhost:9999", "tok")
	// Close before connecting should not panic
	err := ws.Close()
	if err != nil {
		t.Errorf("Close unconnected = %v, want nil", err)
	}
}

func TestWSClient_DashboardList(t *testing.T) {
	dashboards := []LovelaceDashboard{
		{ID: "lovelace", URLPath: "", Title: "Home", Mode: "storage"},
		{ID: "energy", URLPath: "energy", Title: "Energy Dashboard", Mode: "storage"},
	}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "lovelace/dashboards/list" {
			t.Errorf("expected lovelace/dashboards/list, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, dashboards)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.DashboardList(context.Background())
	if err != nil {
		t.Fatalf("DashboardList failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 dashboards, got %d", len(result))
	}
	if result[0].ID != "lovelace" {
		t.Errorf("ID = %q, want 'lovelace'", result[0].ID)
	}
	if result[1].Title != "Energy Dashboard" {
		t.Errorf("Title = %q, want 'Energy Dashboard'", result[1].Title)
	}
}

func TestWSClient_DashboardConfigRaw(t *testing.T) {
	configJSON := json.RawMessage(`{"title":"Home","views":[{"title":"Main"}]}`)

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "lovelace/config" {
			t.Errorf("expected lovelace/config, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, configJSON)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	raw, err := ws.DashboardConfigRaw(context.Background(), "")
	if err != nil {
		t.Fatalf("DashboardConfigRaw failed: %v", err)
	}
	if len(raw) == 0 {
		t.Error("DashboardConfigRaw returned empty result")
	}
}

func TestWSClient_DashboardConfig(t *testing.T) {
	configData := LovelaceConfig{
		Views: []json.RawMessage{json.RawMessage(`{"title":"Main"}`)},
	}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		sendWSResult(t, c, cmd, configData)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	cfg, err := ws.DashboardConfig(context.Background(), "")
	if err != nil {
		t.Fatalf("DashboardConfig failed: %v", err)
	}
	if len(cfg.Views) != 1 {
		t.Errorf("Views len = %d, want 1", len(cfg.Views))
	}
}

func TestWSClient_DashboardConfigSave(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "lovelace/config/save" {
			t.Errorf("expected lovelace/config/save, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, nil)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	config := json.RawMessage(`{"title":"Home","views":[]}`)
	if err := ws.DashboardConfigSave(context.Background(), "", config); err != nil {
		t.Fatalf("DashboardConfigSave failed: %v", err)
	}
}

func TestWSClient_DashboardCreate(t *testing.T) {
	want := LovelaceDashboard{ID: "new-dash", URLPath: "my-dash", Title: "My Dashboard", Mode: "storage"}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "lovelace/dashboards/create" {
			t.Errorf("expected lovelace/dashboards/create, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, want)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.DashboardCreate(context.Background(), DashboardCreateParams{
		URLPath:       "my-dash",
		Title:         "My Dashboard",
		ShowInSidebar: true,
	})
	if err != nil {
		t.Fatalf("DashboardCreate failed: %v", err)
	}
	if result.Title != "My Dashboard" {
		t.Errorf("Title = %q, want 'My Dashboard'", result.Title)
	}
}

func TestWSClient_DashboardDelete(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "lovelace/dashboards/delete" {
			t.Errorf("expected lovelace/dashboards/delete, got %q", cmd["type"])
			return
		}
		if cmd["dashboard_id"] != "old-dash" {
			t.Errorf("dashboard_id = %q, want 'old-dash'", cmd["dashboard_id"])
		}
		sendWSResult(t, c, cmd, nil)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	if err := ws.DashboardDelete(context.Background(), "old-dash"); err != nil {
		t.Fatalf("DashboardDelete failed: %v", err)
	}
}

func TestWSClient_LovelaceInfo(t *testing.T) {
	info := LovelaceInfo{Mode: "storage"}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "lovelace/info" {
			t.Errorf("expected lovelace/info, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, info)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.LovelaceInfo(context.Background())
	if err != nil {
		t.Fatalf("LovelaceInfo failed: %v", err)
	}
	if result.Mode != "storage" {
		t.Errorf("Mode = %q, want 'storage'", result.Mode)
	}
}

func TestWSClient_IntegrationManifestList(t *testing.T) {
	manifests := []IntegrationManifest{
		{Domain: "mqtt", Name: "MQTT", IsBuiltIn: true},
		{Domain: "hacs", Name: "HACS", Version: "1.34.0", IsBuiltIn: false},
	}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "manifest/list" {
			t.Errorf("expected manifest/list, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, manifests)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.IntegrationManifestList(context.Background())
	if err != nil {
		t.Fatalf("IntegrationManifestList failed: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 manifests, got %d", len(result))
	}
	if result[0].Domain != "mqtt" {
		t.Errorf("Domain = %q, want 'mqtt'", result[0].Domain)
	}
	if result[1].Version != "1.34.0" {
		t.Errorf("Version = %q, want '1.34.0'", result[1].Version)
	}
}

func TestWSClient_SupervisorAPI_RequestShape(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "hassio/api" {
			t.Errorf("type = %q, want 'hassio/api'", cmd["type"])
		}
		if cmd["endpoint"] != "/addons" {
			t.Errorf("endpoint = %q, want '/addons'", cmd["endpoint"])
		}
		if cmd["method"] != "get" {
			t.Errorf("method = %q, want 'get'", cmd["method"])
		}
		if _, hasData := cmd["data"]; hasData {
			t.Error("data should be omitted when nil")
		}
		sendWSResult(t, c, cmd, map[string]any{"addons": []any{}})
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.SupervisorAPI(context.Background(), "/addons", "get", nil)
	if err != nil {
		t.Fatalf("SupervisorAPI failed: %v", err)
	}
	if !strings.Contains(string(result), "addons") {
		t.Errorf("result does not contain expected key: %s", string(result))
	}
}

func TestWSClient_SupervisorAPI_DefaultMethodAndData(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["method"] != "get" {
			t.Errorf("method = %q, want 'get' (default when empty)", cmd["method"])
		}
		data, ok := cmd["data"].(map[string]any)
		if !ok || data["confirm"] != true {
			t.Errorf("data = %v, want {confirm: true}", cmd["data"])
		}
		sendWSResult(t, c, cmd, map[string]any{})
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	if _, err := ws.SupervisorAPI(context.Background(), "/addons/foo/restart", "", map[string]any{"confirm": true}); err != nil {
		t.Fatalf("SupervisorAPI failed: %v", err)
	}
}

func TestWSClient_ResourceList(t *testing.T) {
	resources := []LovelaceResource{
		{ID: "1", Type: "module", URL: "/local/custom.js"},
	}

	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "lovelace/resources" {
			t.Errorf("expected lovelace/resources, got %q", cmd["type"])
			return
		}
		sendWSResult(t, c, cmd, resources)
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	result, err := ws.ResourceList(context.Background())
	if err != nil {
		t.Fatalf("ResourceList failed: %v", err)
	}
	if len(result) != 1 {
		t.Fatalf("expected 1 resource, got %d", len(result))
	}
	if result[0].URL != "/local/custom.js" {
		t.Errorf("URL = %q, want '/local/custom.js'", result[0].URL)
	}
}

func TestWSClient_AreaRegistryList_Error(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		sendWSError(c, cmd, "not_found", "area registry unavailable")
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	_, err := ws.AreaRegistryList(context.Background())
	if err == nil {
		t.Fatal("expected error from AreaRegistryList when WS returns error")
	}
}

func TestWSClient_LabelRegistryList_Error(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		sendWSError(c, cmd, "not_found", "label registry unavailable")
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	_, err := ws.LabelRegistryList(context.Background())
	if err == nil {
		t.Fatal("expected error from LabelRegistryList when WS returns error")
	}
}

func TestWSClient_FloorRegistryList_Error(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		sendWSError(c, cmd, "not_found", "floor registry unavailable")
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	_, err := ws.FloorRegistryList(context.Background())
	if err == nil {
		t.Fatal("expected error from FloorRegistryList when WS returns error")
	}
}

func TestWSClient_DeviceRegistryList_Error(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		sendWSError(c, cmd, "not_found", "device registry unavailable")
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	_, err := ws.DeviceRegistryList(context.Background())
	if err == nil {
		t.Fatal("expected error from DeviceRegistryList when WS returns error")
	}
}

func TestWSClient_DashboardList_Error(t *testing.T) {
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		sendWSError(c, cmd, "not_found", "dashboard list unavailable")
	})
	defer srv.Close()

	ws := connectWSTest(t, srv)
	defer func() { _ = ws.Close() }()

	_, err := ws.DashboardList(context.Background())
	if err == nil {
		t.Fatal("expected error from DashboardList when WS returns error")
	}
}
