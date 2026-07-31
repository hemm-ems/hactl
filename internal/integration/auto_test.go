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

// autoLsIDs returns the `id` column of `auto ls --json`.
func autoLsIDs(t *testing.T, raw string) []string {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("auto ls --json did not parse: %v\noutput:\n%s", err, raw)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if v, ok := r["id"].(string); ok {
			out = append(out, v)
		}
	}
	return out
}

// TestAutoLsFailing is the negative control for the `--failing` filter.
//
// The basic fixture's triggers are deliberately inert, so Home Assistant holds
// no errored automation trace at all and the only correct answer is the empty
// set — while plain `auto ls` still lists every automation. Asserting both
// halves is what makes this a test: the old body was
// `out := runHactl(t, "auto", "ls", "--failing"); _ = out`, which passed for a
// filter that returned everything, for one that returned nothing whatever HA
// held, and for one that returned three rows of fiction.
//
// The positive control lives in TestAutoLsFailingMatchesHA (oracle rig, where
// automations really do fail); a filter needs both to be pinned.
func TestAutoLsFailing(t *testing.T) {
	haFailing := oracleErroredTraceItemIDs(t, ha)

	failing := autoLsIDs(t, runHactl(t, "auto", "ls", "--failing", "--top", "1000", "--json"))
	assertSameSet(t, "auto ls --failing (HA's errored automation traces)", haFailing, failing)

	all := autoLsIDs(t, runHactl(t, "auto", "ls", "--top", "1000", "--json"))
	if len(all) == 0 {
		t.Fatal("precondition: auto ls lists no automations at all, so --failing returning nothing " +
			"would prove nothing; the basic fixture defines three")
	}
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

// TestAutoLsPattern — a glob that matches nothing succeeds and says what it
// searched (D-29). It used to assert the bare table header, which is what an
// instance with no automations at all prints: the two cases a caller most needs
// to tell apart were byte-identical (live-fire #28).
func TestAutoLsPattern(t *testing.T) {
	out := runHactl(t, "auto", "ls", "--pattern", "nonexistent_xyz_*")
	assertContains(t, out, "--pattern")
	assertContains(t, out, "nonexistent_xyz_*")
	assertContains(t, out, "automations on this instance")
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
	// A label nothing carries: the answer names the label rather than the
	// inventory, and carries no automation of its own (D-29).
	out := runHactl(t, "auto", "ls", "--label", "nonexistent_label_xyz")
	assertContains(t, out, "--label")
	assertContains(t, out, "nonexistent_label_xyz")
	entries := runHactlJSON[[]map[string]string](t, "auto", "ls")
	for _, e := range entries {
		assertNotContains(t, out, e["id"])
	}
}

func TestAutoLsLabelHelp(t *testing.T) {
	out := runHactl(t, "auto", "ls", "--help")
	assertContains(t, out, "--label")
}
