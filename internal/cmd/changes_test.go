package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// Logbook entry shape captured from /api/logbook/<datetime>.
// Source: homeassistant/components/logbook/processor.py (HA 2026.4.4) —
// the EventProcessor emits these context_* keys alongside the standard ones.
// Three representative cases below: a user-driven service call, an automation
// trigger, and a system state change with no user attribution.

func TestLogbookEntry_DecodesUserTriggeredChange(t *testing.T) {
	data := []byte(`{
		"when": "2026-05-21T10:00:00+00:00",
		"name": "Kitchen Light",
		"state": "on",
		"entity_id": "light.kitchen",
		"domain": "light",
		"context_user_id": "ae7c1d92b8f4429fae3e08d8a9b1c2d4",
		"context_id": "01HXYZ_USER_CTX"
	}`)

	var e logbookEntry
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if e.ContextUserID != "ae7c1d92b8f4429fae3e08d8a9b1c2d4" {
		t.Errorf("ContextUserID = %q, want UUID", e.ContextUserID)
	}
	if e.ContextID != "01HXYZ_USER_CTX" {
		t.Errorf("ContextID = %q, want 01HXYZ_USER_CTX", e.ContextID)
	}
}

func TestLogbookEntry_DecodesAutomationTrigger(t *testing.T) {
	// An entity-state-change triggered by an automation. HA's logbook
	// ContextAugmenter adds context_event_type=automation_triggered plus
	// context_name=<automation alias> and the originating entity_id.
	data := []byte(`{
		"when": "2026-05-21T10:01:00+00:00",
		"name": "Kitchen Light",
		"state": "on",
		"entity_id": "light.kitchen",
		"domain": "light",
		"context_event_type": "automation_triggered",
		"context_name": "Sunset Lights",
		"context_entity_id": "automation.sunset_lights",
		"context_entity_id_name": "Sunset Lights",
		"context_source": "time",
		"context_id": "01HXYZ_AUTO_CTX"
	}`)

	var e logbookEntry
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if e.ContextEventType != "automation_triggered" {
		t.Errorf("ContextEventType = %q", e.ContextEventType)
	}
	if e.ContextName != "Sunset Lights" {
		t.Errorf("ContextName = %q, want 'Sunset Lights'", e.ContextName)
	}
	if e.ContextEntityID != "automation.sunset_lights" {
		t.Errorf("ContextEntityID = %q", e.ContextEntityID)
	}
	if e.ContextEntityIDName != "Sunset Lights" {
		t.Errorf("ContextEntityIDName = %q", e.ContextEntityIDName)
	}
	if e.ContextSource != "time" {
		t.Errorf("ContextSource = %q, want 'time'", e.ContextSource)
	}
	if e.ContextUserID != "" {
		t.Errorf("ContextUserID should be empty for automation trigger, got %q", e.ContextUserID)
	}
}

// changesFixture wires a cmdTestServer serving WS registries + auth list
// plus /api/logbook with the given response body.
func changesFixture(t *testing.T, users []map[string]any, logbookBody string) *cmdTestServer {
	t.Helper()
	return startCmdServer(t, map[string]any{
		"config/auth/list": users,
	}, map[string]http.HandlerFunc{
		"/api/logbook/": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(logbookBody))
		},
	})
}

func TestRunChanges_HasWhoColumn(t *testing.T) {
	body := `[
		{"when":"2026-05-21T10:00:00+00:00","name":"Kitchen Light","state":"on","entity_id":"light.kitchen","domain":"light","context_user_id":"` + janUUID + `"},
		{"when":"2026-05-21T10:01:00+00:00","name":"Kitchen Light","state":"off","entity_id":"light.kitchen","domain":"light","context_event_type":"automation_triggered","context_name":"Sunset Lights"},
		{"when":"2026-05-21T10:02:00+00:00","name":"Temperature","state":"21.5","entity_id":"sensor.temp","domain":"sensor"}
	]`
	ts := changesFixture(t, []map[string]any{{"id": janUUID, "name": "Jan"}}, body)
	withFlagDir(t, ts.dir)

	oldSince := flagSince
	flagSince = "24h"
	defer func() { flagSince = oldSince }()

	var buf bytes.Buffer
	if err := runChanges(context.Background(), &buf); err != nil {
		t.Fatalf("runChanges: %v", err)
	}
	out := buf.String()

	// Header
	if !strings.Contains(out, "who") {
		t.Fatalf("output missing 'who' header:\n%s", out)
	}
	// Per-row attribution
	if !strings.Contains(out, "User Jan") {
		t.Errorf("row 1 missing 'User Jan':\n%s", out)
	}
	if !strings.Contains(out, "Automation: Sunset Lights") {
		t.Errorf("row 2 missing automation label:\n%s", out)
	}
	if !strings.Contains(out, "Home Assistant") {
		t.Errorf("row 3 missing 'Home Assistant' fallback:\n%s", out)
	}
}

func TestRunChanges_JSON_PreservesContextFields(t *testing.T) {
	body := `[
		{"when":"2026-05-21T10:00:00+00:00","name":"Kitchen Light","state":"on","entity_id":"light.kitchen","domain":"light","context_user_id":"` + janUUID + `","context_id":"ctx1"}
	]`
	ts := changesFixture(t, []map[string]any{{"id": janUUID, "name": "Jan"}}, body)
	withFlagDir(t, ts.dir)

	oldSince := flagSince
	oldJSON := flagJSON
	flagSince = "24h"
	flagJSON = true
	defer func() {
		flagSince = oldSince
		flagJSON = oldJSON
	}()

	var buf bytes.Buffer
	if err := runChanges(context.Background(), &buf); err != nil {
		t.Fatalf("runChanges JSON: %v", err)
	}
	// The JSON output goes through format.Table.Render which writes raw rows;
	// at minimum the context_user_id must reach the consumer.
	if !strings.Contains(buf.String(), janUUID) {
		t.Errorf("JSON output missing context_user_id %q:\n%s", janUUID, buf.String())
	}
}

func TestLogbookEntry_DecodesSystemChange(t *testing.T) {
	// A state change without any context augmentation — typical for
	// integrations pushing state updates with no user attribution.
	data := []byte(`{
		"when": "2026-05-21T10:02:00+00:00",
		"name": "Outdoor Temperature",
		"state": "17.3",
		"entity_id": "sensor.outdoor_temp",
		"domain": "sensor"
	}`)

	var e logbookEntry
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if e.ContextUserID != "" || e.ContextName != "" || e.ContextEventType != "" {
		t.Errorf("expected all context_* fields empty, got %+v", e)
	}
}
