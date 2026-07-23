package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// entWhoFixture wires up a cmdTestServer for runEntWho tests.
// Returns the server (use ts.dir as flagDir). The logbook handler matches
// any path starting with /api/logbook/ (the start datetime is in the path).
func entWhoFixture(t *testing.T, users []map[string]any, logbookBody string) *cmdTestServer {
	t.Helper()
	return startCmdServer(t, map[string]any{
		"config/auth/list": users,
	}, map[string]http.HandlerFunc{
		"/api/logbook/": func(w http.ResponseWriter, r *http.Request) {
			// HA's logbook accepts ?entity=<id>; assert the filter is wired through
			// so the integration test catches regressions in the URL building.
			if r.URL.Query().Get("entity") != "light.kitchen" {
				t.Errorf("expected ?entity=light.kitchen, got %q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(logbookBody))
		},
		// The entity exists — an empty logbook for it is a fact about a quiet
		// entity, not a typo. `ent who` distinguishes the two by asking for the
		// state, so the stub must answer as HA would for a real entity.
		"/api/states/light.kitchen": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"entity_id":"light.kitchen","state":"on","attributes":{}}`))
		},
	})
}

func TestRunEntWho_TableAndSummary(t *testing.T) {
	// Five events over the last day:
	//   3 × User Jan, 1 × Automation: Sunset Lights, 1 × Home Assistant.
	body := `[
		{"when":"2026-05-21T10:00:00+00:00","name":"Kitchen Light","state":"on","entity_id":"light.kitchen","domain":"light","context_user_id":"` + janUUID + `"},
		{"when":"2026-05-21T10:05:00+00:00","name":"Kitchen Light","state":"off","entity_id":"light.kitchen","domain":"light","context_user_id":"` + janUUID + `"},
		{"when":"2026-05-21T11:00:00+00:00","name":"Kitchen Light","state":"on","entity_id":"light.kitchen","domain":"light","context_event_type":"automation_triggered","context_name":"Sunset Lights"},
		{"when":"2026-05-21T12:00:00+00:00","name":"Kitchen Light","state":"on","entity_id":"light.kitchen","domain":"light","context_user_id":"` + janUUID + `"},
		{"when":"2026-05-21T13:00:00+00:00","name":"Kitchen Light","state":"off","entity_id":"light.kitchen","domain":"light"}
	]`
	ts := entWhoFixture(t, []map[string]any{{"id": janUUID, "name": "Jan"}}, body)
	withFlagDir(t, ts.dir)

	oldSince := flagSince
	flagSince = "24h"
	defer func() { flagSince = oldSince }()

	var buf bytes.Buffer
	if err := runEntWho(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntWho: %v", err)
	}
	out := buf.String()

	// Per-event table
	if !strings.Contains(out, "User Jan") {
		t.Errorf("missing 'User Jan' attribution:\n%s", out)
	}
	if !strings.Contains(out, "Automation: Sunset Lights") {
		t.Errorf("missing automation row:\n%s", out)
	}
	if !strings.Contains(out, "Home Assistant") {
		t.Errorf("missing 'Home Assistant' fallback:\n%s", out)
	}

	// Summary section (must be after the per-event table). Both label and
	// count appear on the same row.
	summaryIdx := strings.Index(out, "summary")
	if summaryIdx < 0 {
		t.Fatalf("missing 'summary' header:\n%s", out)
	}
	summaryPart := out[summaryIdx:]
	if !strings.Contains(summaryPart, "User Jan") || !strings.Contains(summaryPart, "3") {
		t.Errorf("summary missing 'User Jan' count 3:\n%s", summaryPart)
	}
	if !strings.Contains(summaryPart, "Automation: Sunset Lights") || !strings.Contains(summaryPart, "1") {
		t.Errorf("summary missing automation count 1:\n%s", summaryPart)
	}
}

func TestRunEntWho_Empty(t *testing.T) {
	ts := entWhoFixture(t, []map[string]any{}, `[]`)
	withFlagDir(t, ts.dir)

	oldSince := flagSince
	flagSince = "24h"
	defer func() { flagSince = oldSince }()

	var buf bytes.Buffer
	if err := runEntWho(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntWho: %v", err)
	}
	if !strings.Contains(buf.String(), "no changes") {
		t.Errorf("empty result should report 'no changes', got:\n%s", buf.String())
	}
}

func TestRunEntWho_JSON_Shape(t *testing.T) {
	body := `[
		{"when":"2026-05-21T10:00:00+00:00","name":"Kitchen Light","state":"on","entity_id":"light.kitchen","domain":"light","context_user_id":"` + janUUID + `"},
		{"when":"2026-05-21T11:00:00+00:00","name":"Kitchen Light","state":"on","entity_id":"light.kitchen","domain":"light","context_event_type":"automation_triggered","context_name":"Sunset Lights"}
	]`
	ts := entWhoFixture(t, []map[string]any{{"id": janUUID, "name": "Jan"}}, body)
	withFlagDir(t, ts.dir)

	oldSince, oldJSON := flagSince, flagJSON
	flagSince = "24h"
	flagJSON = true
	defer func() {
		flagSince = oldSince
		flagJSON = oldJSON
	}()

	var buf bytes.Buffer
	if err := runEntWho(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntWho JSON: %v", err)
	}

	var got struct {
		Events []struct {
			When        string `json:"when"`
			State       string `json:"state"`
			EntityID    string `json:"entity_id"`
			ChangedBy   string `json:"changed_by"`
			ContextID   string `json:"context_id"`
			ContextUser string `json:"context_user_id"`
		} `json:"events"`
		Summary []struct {
			Trigger string `json:"trigger"`
			Count   int    `json:"count"`
		} `json:"summary"`
		Window struct {
			Since string `json:"since"`
			Until string `json:"until"`
		} `json:"window"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, buf.String())
	}

	if len(got.Events) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got.Events))
	}
	if got.Events[0].ChangedBy != "User Jan" {
		t.Errorf("events[0].changed_by = %q, want 'User Jan'", got.Events[0].ChangedBy)
	}
	if got.Events[1].ChangedBy != "Automation: Sunset Lights" {
		t.Errorf("events[1].changed_by = %q", got.Events[1].ChangedBy)
	}

	if len(got.Summary) < 2 {
		t.Fatalf("expected ≥2 summary rows, got %d", len(got.Summary))
	}
	// Summary must be sorted by count desc — both labels have count 1 here,
	// so we just check both appear.
	labels := map[string]int{}
	for _, s := range got.Summary {
		labels[s.Trigger] = s.Count
	}
	if labels["User Jan"] != 1 || labels["Automation: Sunset Lights"] != 1 {
		t.Errorf("summary counts wrong: %+v", labels)
	}

	if got.Window.Since == "" || got.Window.Until == "" {
		t.Errorf("window times missing: %+v", got.Window)
	}
}
