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

// withAutoLsFlags pins the `auto ls` filter flags for one test.
func withAutoLsFlags(t *testing.T, pattern, label string) {
	t.Helper()
	oldPattern, oldLabel, oldFailing, oldRestored, oldSince := flagAutoPattern, flagAutoLabel, flagAutoFailing, flagAutoRestored, flagSince
	flagAutoPattern, flagAutoLabel, flagAutoFailing, flagAutoRestored, flagSince = pattern, label, false, false, "24h"
	t.Cleanup(func() {
		flagAutoPattern, flagAutoLabel, flagAutoFailing, flagAutoRestored, flagSince = oldPattern, oldLabel, oldFailing, oldRestored, oldSince
	})
}

// autoLabelServer serves two automations whose /api/states attributes carry the
// exact key set a live HA emits for an automation (current, friendly_name, id,
// last_triggered, mode) — deliberately with no `labels` key, because registry
// labels are never exposed as state attributes. The entity registry is the only
// place the label assignment exists.
func autoLabelServer(t *testing.T) *cmdTestServer {
	t.Helper()
	states := []map[string]any{
		{
			"entity_id": "automation.victron_charge",
			"state":     "on",
			"attributes": map[string]any{
				"id": "1700000000001", "friendly_name": "Victron charge", "mode": "single",
				"current": 0, "last_triggered": "2026-07-21T10:00:00Z",
			},
		},
		{
			"entity_id": "automation.climate_schedule",
			"state":     "on",
			"attributes": map[string]any{
				"id": "1700000000002", "friendly_name": "Climate schedule", "mode": "single",
				"current": 0, "last_triggered": "2026-07-21T09:00:00Z",
			},
		},
	}
	statesJSON, _ := json.Marshal(states)

	return startCmdServer(t, map[string]any{
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "automation.victron_charge", "area_id": "cellar", "labels": []string{"lbl_batteries"}},
			{"entity_id": "automation.climate_schedule", "area_id": "", "labels": []string{}},
		},
		"config/area_registry/list": []map[string]any{
			{"area_id": "cellar", "name": "Cellar", "labels": []string{}, "floor_id": ""},
		},
		"config/label_registry/list": []map[string]any{
			{"label_id": "lbl_batteries", "name": "batteries", "color": "", "icon": "", "description": ""},
		},
		"config/floor_registry/list": []map[string]any{},
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
		"/api/logbook/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
}

// `auto ls` must fill the labels column from the entity registry, not from state
// attributes (which never carry labels).
func TestRunAutoLs_LabelsComeFromEntityRegistry(t *testing.T) {
	ts := autoLabelServer(t)
	withFlagDir(t, ts.dir)
	withAutoLsFlags(t, "", "")

	var buf bytes.Buffer
	if err := runAutoLs(context.Background(), &buf); err != nil {
		t.Fatalf("runAutoLs failed: %v", err)
	}
	out := buf.String()
	// "batteries" appears nowhere in the entity ids, so a match can only come
	// from the registry-derived labels column.
	if !strings.Contains(out, "batteries") {
		t.Errorf("labels column is blank: registry label 'batteries' missing from output:\n%s", out)
	}
}

// `auto ls --label batteries` must return the labelled automation. Documented in
// docs/manual.md: "uses HA entity registry labels".
func TestRunAutoLs_LabelFilterMatchesRegistryLabel(t *testing.T) {
	ts := autoLabelServer(t)
	withFlagDir(t, ts.dir)
	withAutoLsFlags(t, "", "batteries")

	var buf bytes.Buffer
	if err := runAutoLs(context.Background(), &buf); err != nil {
		t.Fatalf("runAutoLs --label failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "victron_charge") {
		t.Errorf("--label batteries returned no rows; want automation.victron_charge:\n%s", out)
	}
	if strings.Contains(out, "climate_schedule") {
		t.Errorf("--label batteries leaked an unlabelled automation:\n%s", out)
	}
}

// If the entity registry cannot be read, labels are unknown — `--label` then
// matches nothing for a reason that has nothing to do with the filter. That
// must be said out loud instead of rendering a silent zero-row table.
func TestRunAutoLs_LabelFilterWarnsWhenRegistryUnavailable(t *testing.T) {
	states := []map[string]any{
		{"entity_id": "automation.victron_charge", "state": "on", "attributes": map[string]any{"id": "1"}},
	}
	statesJSON, _ := json.Marshal(states)

	// No entity_registry/list response: fetchRegistryContext fails.
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
		"/api/logbook/": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
		},
	})
	withFlagDir(t, ts.dir)
	withAutoLsFlags(t, "", "batteries")
	logBuf := captureDefaultLogger(t)

	var buf bytes.Buffer
	if err := runAutoLs(context.Background(), &buf); err != nil {
		t.Fatalf("runAutoLs failed: %v", err)
	}
	if strings.Contains(buf.String(), "victron_charge") {
		t.Fatalf("precondition: expected no rows without registry labels:\n%s", buf.String())
	}

	found := false
	for line := range strings.SplitSeq(strings.TrimSpace(logBuf.String()), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "label") {
			found = true
		}
	}
	if !found {
		t.Errorf("--label matched nothing because labels were unavailable, with no WARN saying so; log was:\n%s", logBuf.String())
	}
}
