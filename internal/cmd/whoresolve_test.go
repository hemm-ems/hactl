package cmd

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/hemm-ems/hactl/internal/haapi"
)

var wsTestUpgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true },
}

// startAuthListServer spins up a WS server that completes the HA auth
// handshake, then responds to config/auth/list with the given fn.
func startAuthListServer(t *testing.T, respond func(c *websocket.Conn, cmd map[string]any)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := wsTestUpgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.WriteJSON(map[string]string{"type": "auth_required", "ha_version": "2026.4"})
		var auth map[string]string
		_ = c.ReadJSON(&auth)
		_ = c.WriteJSON(map[string]string{"type": "auth_ok", "ha_version": "2026.4"})
		var cmd map[string]any
		if err := c.ReadJSON(&cmd); err != nil {
			return
		}
		respond(c, cmd)
	}))
}

func TestTriggerLabel(t *testing.T) {
	users := map[string]haapi.UserEntry{
		"ae7c1d92b8f4429fae3e08d8a9b1c2d4": {ID: "ae7c1d92b8f4429fae3e08d8a9b1c2d4", Name: "Jan", Username: "jan", IsOwner: true},
		"11111111111111111111111111111111": {ID: "11111111111111111111111111111111", Name: "Home Assistant Content", SystemGenerated: true},
	}

	tests := []struct {
		name  string
		entry logbookEntry
		want  string
	}{
		{
			name:  "user with known name",
			entry: logbookEntry{ContextUserID: "ae7c1d92b8f4429fae3e08d8a9b1c2d4"},
			want:  "User Jan",
		},
		{
			name:  "user_id present but not in cache → UUID fallback",
			entry: logbookEntry{ContextUserID: "deadbeefcafe1234deadbeefcafe1234"},
			want:  "User deadbeef…",
		},
		{
			name: "system_generated user (e.g. Supervisor) still gets named",
			// SystemGenerated names are usually "Home Assistant Content"; they
			// still resolve from the cache the same way.
			entry: logbookEntry{ContextUserID: "11111111111111111111111111111111"},
			want:  "User Home Assistant Content",
		},
		{
			name: "automation_triggered",
			entry: logbookEntry{
				ContextEventType: "automation_triggered",
				ContextName:      "Sunset Lights",
				ContextEntityID:  "automation.sunset_lights",
			},
			want: "Automation: Sunset Lights",
		},
		{
			name: "script_started",
			entry: logbookEntry{
				ContextEventType: "script_started",
				ContextName:      "morning_routine",
			},
			want: "Script: morning_routine",
		},
		{
			name: "device firing (context_name set, no recognized event_type)",
			entry: logbookEntry{
				ContextName: "Living-room remote",
			},
			want: "Device: Living-room remote",
		},
		{
			name:  "no attribution → Home Assistant",
			entry: logbookEntry{},
			want:  "Home Assistant",
		},
		{
			name: "user_id wins over event_type/name (rule order)",
			entry: logbookEntry{
				ContextUserID:    "ae7c1d92b8f4429fae3e08d8a9b1c2d4",
				ContextEventType: "automation_triggered",
				ContextName:      "Sunset Lights",
			},
			want: "User Jan",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := triggerLabel(tc.entry, users)
			if got != tc.want {
				t.Errorf("triggerLabel(%+v) = %q, want %q", tc.entry, got, tc.want)
			}
		})
	}
}

func TestTriggerLabel_NilUsersMap(t *testing.T) {
	// loadUsers may return a nil map on graceful-degrade; the resolver
	// must not panic and must fall back to the UUID-truncated form.
	got := triggerLabel(logbookEntry{ContextUserID: "ae7c1d92b8f4429fae3e08d8a9b1c2d4"}, nil)
	if got != "User ae7c1d92…" {
		t.Errorf("triggerLabel with nil users = %q, want UUID fallback", got)
	}
}

func TestLoadUsers_Success(t *testing.T) {
	srv := startAuthListServer(t, func(c *websocket.Conn, cmd map[string]any) {
		if cmd["type"] != "config/auth/list" {
			t.Errorf("expected config/auth/list, got %q", cmd["type"])
			return
		}
		data, _ := json.Marshal([]haapi.UserEntry{
			{ID: "u1", Name: "Jan"},
			{ID: "u2", Name: "Eva"},
		})
		_ = c.WriteJSON(map[string]any{
			"id": cmd["id"], "type": "result", "success": true, "result": json.RawMessage(data),
		})
	})
	defer srv.Close()

	ws := haapi.NewWSClient("http"+strings.TrimPrefix(srv.URL, "http"), "tok")
	if err := ws.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = ws.Close() }()

	got := loadUsers(context.Background(), ws)
	if len(got) != 2 {
		t.Fatalf("expected 2 users in map, got %d", len(got))
	}
	if got["u1"].Name != "Jan" {
		t.Errorf("u1.Name = %q, want Jan", got["u1"].Name)
	}
}

func TestLoadUsers_GracefulDegrade_AdminDenied(t *testing.T) {
	srv := startAuthListServer(t, func(c *websocket.Conn, cmd map[string]any) {
		_ = c.WriteJSON(map[string]any{
			"id": cmd["id"], "type": "result", "success": false,
			"error": map[string]string{"code": "unauthorized", "message": "Unauthorized"},
		})
	})
	defer srv.Close()

	ws := haapi.NewWSClient("http"+strings.TrimPrefix(srv.URL, "http"), "tok")
	if err := ws.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = ws.Close() }()

	got := loadUsers(context.Background(), ws)
	if got == nil {
		t.Fatal("loadUsers should return a non-nil (empty) map on degrade")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map on degrade, got %d entries", len(got))
	}
}

func TestLoadUsers_GracefulDegrade_OtherError(t *testing.T) {
	srv := startAuthListServer(t, func(c *websocket.Conn, cmd map[string]any) {
		_ = c.WriteJSON(map[string]any{
			"id": cmd["id"], "type": "result", "success": false,
			"error": map[string]string{"code": "unknown_command", "message": "Unknown command."},
		})
	})
	defer srv.Close()

	ws := haapi.NewWSClient("http"+strings.TrimPrefix(srv.URL, "http"), "tok")
	if err := ws.Connect(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = ws.Close() }()

	got := loadUsers(context.Background(), ws)
	if got == nil || len(got) != 0 {
		t.Errorf("expected empty map on transient failure, got %v", got)
	}
}
