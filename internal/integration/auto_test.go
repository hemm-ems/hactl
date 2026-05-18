//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestAutoLs(t *testing.T) {
	out := runHactl(t, "auto", "ls")

	// Should show a table header
	if !strings.Contains(out, "id") {
		t.Errorf("auto ls output missing 'id' header: %s", out)
	}
	if !strings.Contains(out, "state") {
		t.Errorf("auto ls output missing 'state' header: %s", out)
	}

	// Our fixture has automations defined — they should appear as entities
	// Note: automations from automations.yaml get IDs like automation.climate_schedule
	// They may or may not be visible depending on HA's loading, but the command should not fail
}

func TestAutoLsJSON(t *testing.T) {
	out := runHactl(t, "auto", "ls", "--json")
	// JSON output should start with [ or contain valid JSON
	trimmed := strings.TrimSpace(out)
	if trimmed != "" && !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
		t.Errorf("auto ls --json did not produce JSON-like output: %s", out)
	}
}

func TestAutoLsJSONSchema(t *testing.T) {
	entries := runHactlJSON[[]map[string]string](t, "auto", "ls")
	if len(entries) == 0 {
		t.Skip("no automations loaded in HA")
	}
	first := entries[0]
	for _, key := range []string{"id", "state"} {
		if _, ok := first[key]; !ok {
			t.Errorf("auto ls --json entry missing key %q", key)
		}
	}
}

func TestAutoLsFailing(t *testing.T) {
	// Should not error even when no automations are failing
	out := runHactl(t, "auto", "ls", "--failing")
	_ = out // just ensure no error
}

func TestAutoShow(t *testing.T) {
	// auto show needs an automation to exist
	out := runHactl(t, "auto", "ls", "--json")
	var entries []map[string]string
	if err := json.Unmarshal([]byte(out), &entries); err != nil || len(entries) == 0 {
		t.Skip("no automations available for auto show test")
	}
	autoID := entries[0]["id"]

	showOut := runHactl(t, "auto", "show", autoID)
	assertContains(t, showOut, autoID)
	assertContains(t, showOut, "state=")
	// Should contain either traces section or "traces: none"
	if !strings.Contains(showOut, "traces") {
		t.Errorf("auto show missing traces section: %s", showOut)
	}
}

func TestAutoShowUnknown(t *testing.T) {
	_, err := runHactlErr(t, "auto", "show", "nonexistent_automation_xyz")
	if err == nil {
		t.Error("auto show nonexistent_automation_xyz expected error, got nil")
	}
}

func TestAutoShowTriggerContent(t *testing.T) {
	out, err := runHactlErr(t, "auto", "show", "climate_schedule")
	if err != nil {
		t.Skip("climate_schedule not available")
	}
	assertContains(t, out, "climate_schedule")
	assertContains(t, out, "state=")
	assertContains(t, out, "mode=")
}

func TestAutoLsPattern(t *testing.T) {
	// --pattern should filter automations by glob
	// Even if no automations match, the command should succeed with just the header
	out := runHactl(t, "auto", "ls", "--pattern", "nonexistent_xyz_*")
	assertContains(t, out, "id") // header should still appear
}

func TestAutoLsPatternMatch(t *testing.T) {
	// First get all automations to find one that exists
	entries := runHactlJSON[[]map[string]string](t, "auto", "ls")
	if len(entries) == 0 {
		t.Skip("no automations loaded in HA")
	}
	autoID := entries[0]["id"]

	// Use exact name as pattern — should return exactly that one
	out := runHactl(t, "auto", "ls", "--pattern", autoID)
	assertContains(t, out, autoID)
}

func TestAutoLsPatternWildcard(t *testing.T) {
	// Pattern with * should match all automations (same as no filter)
	out := runHactl(t, "auto", "ls", "--pattern", "*")
	assertContains(t, out, "id") // header present
}

func TestAutoLsPatternJSON(t *testing.T) {
	// --pattern + --json should work together
	entries := runHactlJSON[[]map[string]string](t, "auto", "ls")
	if len(entries) == 0 {
		t.Skip("no automations loaded in HA")
	}
	autoID := entries[0]["id"]

	filtered := runHactlJSON[[]map[string]string](t, "auto", "ls", "--pattern", autoID)
	if len(filtered) != 1 {
		t.Errorf("auto ls --pattern %s --json returned %d items, want 1", autoID, len(filtered))
	}
}

func TestAutoLsPatternSubstring(t *testing.T) {
	// Bare substring (no glob chars) should match
	entries := runHactlJSON[[]map[string]string](t, "auto", "ls")
	if len(entries) == 0 {
		t.Skip("no automations loaded in HA")
	}
	// "climate" should substring-match "climate_schedule"
	out := runHactl(t, "auto", "ls", "--pattern", "climate")
	assertContains(t, out, "climate_schedule")
}

func TestAutoLsLabelNoMatch(t *testing.T) {
	// --label with a nonexistent label should return only headers
	out := runHactl(t, "auto", "ls", "--label", "nonexistent_label_xyz")
	assertContains(t, out, "id") // header present
	// Should not contain any automation IDs
	entries := runHactlJSON[[]map[string]string](t, "auto", "ls")
	for _, e := range entries {
		assertNotContains(t, out, e["id"])
	}
}

func TestAutoLsLabelHelp(t *testing.T) {
	out := runHactl(t, "auto", "ls", "--help")
	assertContains(t, out, "--label")
}
