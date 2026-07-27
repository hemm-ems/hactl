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
			// Was "user_id wins over event_type/name (rule order)", asserting
			// "User Jan". That encoded defect #3: HA propagates the
			// originating human's user id down the causal chain, so an
			// automation fired by a user's toggle carries BOTH
			// context_user_id (the human who started the chain) AND
			// context_event_type/context_name (the automation that actually
			// made this change). The proximate cause is what changed the
			// entity, so it must win over the propagated, more distal user
			// id — the old expectation had this backwards, and every
			// automation/script/device-caused change was misreported as a
			// plain user edit.
			name: "automation event_type wins over propagated user_id (rule order)",
			entry: logbookEntry{
				ContextUserID:    "ae7c1d92b8f4429fae3e08d8a9b1c2d4",
				ContextEventType: "automation_triggered",
				ContextName:      "Sunset Lights",
			},
			want: "Automation: Sunset Lights",
		},
		{
			name: "script_started wins over propagated user_id (rule order)",
			entry: logbookEntry{
				ContextUserID:    "ae7c1d92b8f4429fae3e08d8a9b1c2d4",
				ContextEventType: "script_started",
				ContextName:      "morning_routine",
			},
			want: "Script: morning_routine",
		},
		{
			name: "device firing wins over propagated user_id (rule order)",
			entry: logbookEntry{
				ContextUserID: "ae7c1d92b8f4429fae3e08d8a9b1c2d4",
				ContextName:   "Living-room remote",
			},
			want: "Device: Living-room remote",
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

// TestLogbookExclusionReason pins hactl's mirror of HA's own logbook filter
// (homeassistant/components/logbook/helpers.py async_filter_entities +
// is_sensor_continuous, HA 2026.7.2): counter/image/proximity always; a sensor
// when it has a unit_of_measurement, a state_class, or a numeric device_class.
func TestLogbookExclusionReason(t *testing.T) {
	cases := []struct {
		name     string
		entityID string
		attrs    map[string]any
		excluded bool
	}{
		{"sensor with unit", "sensor.energy", map[string]any{"unit_of_measurement": "kWh"}, true},
		{"sensor with state_class only", "sensor.count", map[string]any{"state_class": "measurement"}, true},
		{"sensor with numeric device_class only", "sensor.power", map[string]any{"device_class": "power"}, true},
		{"sensor with non-numeric device_class date", "sensor.d", map[string]any{"device_class": "date"}, false},
		{"sensor with non-numeric device_class enum", "sensor.e", map[string]any{"device_class": "enum"}, false},
		{"sensor with non-numeric device_class timestamp", "sensor.t", map[string]any{"device_class": "timestamp"}, false},
		{"sensor with non-numeric device_class uptime", "sensor.u", map[string]any{"device_class": "uptime"}, false},
		{"plain text sensor", "sensor.status", map[string]any{"friendly_name": "Status"}, false},
		{"plain text sensor nil attrs", "sensor.status", nil, false},
		{"counter domain", "counter.visits", map[string]any{}, true},
		{"image domain", "image.cam", map[string]any{}, true},
		{"proximity domain", "proximity.home", map[string]any{}, true},
		// The domain gate: only the sensor domain is conditionally continuous.
		// A light with a stray unit attribute stays covered.
		{"non-sensor with unit attr", "light.kitchen", map[string]any{"unit_of_measurement": "weird"}, false},
		{"input_number with unit", "input_number.level", map[string]any{"unit_of_measurement": "%"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := logbookExclusionReason(tc.entityID, tc.attrs)
			if (got != "") != tc.excluded {
				t.Errorf("logbookExclusionReason(%q, %v) = %q, want excluded=%v",
					tc.entityID, tc.attrs, got, tc.excluded)
			}
		})
	}
}

// TestResolveActor pins the shared resolution's order (D-4): a real logbook
// answer always wins — even over the exclusion predicate — and the
// state-context fallback carries the exclusion flag exactly when the predicate
// is why the logbook had nothing.
func TestResolveActor(t *testing.T) {
	users := map[string]haapi.UserEntry{
		"ae7c1d92b8f4429fae3e08d8a9b1c2d4": {ID: "ae7c1d92b8f4429fae3e08d8a9b1c2d4", Name: "Jan"},
	}
	coveredState := entityState{
		EntityID:   "light.kitchen",
		Attributes: map[string]any{},
		Context:    haapi.Context{UserID: "ae7c1d92b8f4429fae3e08d8a9b1c2d4"},
	}
	excludedState := entityState{
		EntityID:   "sensor.energy",
		Attributes: map[string]any{"unit_of_measurement": "kWh"},
		Context:    haapi.Context{UserID: "ae7c1d92b8f4429fae3e08d8a9b1c2d4"},
	}
	entries := []logbookEntry{
		{When: "2026-05-21T09:00:00+00:00", ContextUserID: "ae7c1d92b8f4429fae3e08d8a9b1c2d4"},
		{When: "2026-05-21T10:00:00+00:00", ContextEventType: "automation_triggered", ContextName: "Sunset Lights"},
	}

	t.Run("logbook answer wins and is labelled", func(t *testing.T) {
		got := resolveActor(entries, coveredState, users)
		want := actorAnswer{ChangedBy: "Automation: Sunset Lights", Source: actorSourceLogbook}
		if got != want {
			t.Errorf("resolveActor = %+v, want %+v", got, want)
		}
	})
	t.Run("logbook answer wins even for a predicate-excluded entity", func(t *testing.T) {
		// HA's actual answer outranks our mirror of HA's filter: predicate
		// drift must never override data.
		got := resolveActor(entries, excludedState, users)
		if got.Source != actorSourceLogbook || got.LogbookExcluded {
			t.Errorf("real entries must resolve from the logbook, got %+v", got)
		}
	})
	t.Run("quiet covered entity falls back without exclusion", func(t *testing.T) {
		got := resolveActor(nil, coveredState, users)
		want := actorAnswer{ChangedBy: "User Jan", Source: actorSourceStateContext}
		if got != want {
			t.Errorf("resolveActor = %+v, want %+v", got, want)
		}
	})
	t.Run("excluded entity falls back with the exclusion named", func(t *testing.T) {
		got := resolveActor(nil, excludedState, users)
		if got.ChangedBy != "User Jan" || got.Source != actorSourceStateContext {
			t.Errorf("fallback answer wrong: %+v", got)
		}
		if !got.LogbookExcluded || got.ExclusionReason == "" {
			t.Errorf("exclusion must be flagged with a reason: %+v", got)
		}
	})
}

// TestActorAnswer_LabelNamesTheSource: the rendered form both commands print.
// Dropping the source label reduces the two commands to bare names that can
// disagree with no visible reason — the D70 trap this decision closes.
func TestActorAnswer_LabelNamesTheSource(t *testing.T) {
	plain := actorAnswer{ChangedBy: "User Jan", Source: actorSourceStateContext}
	if got := plain.Label(); got != "User Jan (source: state context)" {
		t.Errorf("Label() = %q, want the source named", got)
	}
	logbook := actorAnswer{ChangedBy: "Automation: X", Source: actorSourceLogbook}
	if got := logbook.Label(); got != "Automation: X (source: logbook)" {
		t.Errorf("Label() = %q, want the source named", got)
	}
	excluded := actorAnswer{
		ChangedBy:       "Home Assistant",
		Source:          actorSourceStateContext,
		LogbookExcluded: true,
		ExclusionReason: "continuous sensor: has unit_of_measurement",
	}
	got := excluded.Label()
	if !strings.Contains(got, "excluded from logbook") ||
		!strings.Contains(got, "continuous sensor: has unit_of_measurement") ||
		!strings.Contains(got, "source: state context") {
		t.Errorf("Label() = %q, must name source AND exclusion AND reason", got)
	}
}

// TestNewestLogbookEntry: the resolver picks the newest `when`, independent of
// which end HA put it at, and degrades positionally for unparseable rows.
func TestNewestLogbookEntry(t *testing.T) {
	newest := logbookEntry{When: "2026-05-21T10:00:00+00:00", ContextName: "Newest"}
	older := logbookEntry{When: "2026-05-21T09:00:00+00:00", ContextName: "Older"}
	cases := []struct {
		name    string
		entries []logbookEntry
		want    string
	}{
		{"ascending (HA's REST order)", []logbookEntry{older, newest}, "Newest"},
		{"descending", []logbookEntry{newest, older}, "Newest"},
		{"single", []logbookEntry{older}, "Older"},
		{"unparseable rows lose to parseable", []logbookEntry{{When: "garbage", ContextName: "Bad"}, older}, "Older"},
		{"all unparseable degrades to last", []logbookEntry{{When: "", ContextName: "A"}, {When: "", ContextName: "B"}}, "B"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := newestLogbookEntry(tc.entries); got.ContextName != tc.want {
				t.Errorf("newestLogbookEntry = %q, want %q", got.ContextName, tc.want)
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
