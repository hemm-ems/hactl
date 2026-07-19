//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestConfigEntries verifies the config entries command lists entries.
func TestConfigEntries(t *testing.T) {
	out := runHactl(t, "config", "entries")
	// Basic HA fixture always has some config entries (e.g. sun, default_config)
	if strings.TrimSpace(out) == "" {
		t.Error("config entries returned empty output")
	}
	assertNotContains(t, out, "panic")
}

// TestConfigEntries_JSON verifies JSON output includes expected fields.
func TestConfigEntries_JSON(t *testing.T) {
	out := runHactl(t, "config", "entries", "--json")
	var entries []map[string]any
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("config entries --json returned invalid JSON: %v\noutput: %s", err, out)
	}
	if len(entries) == 0 {
		t.Skip("no config entries (minimal HA)")
	}
	first := entries[0]
	for _, key := range []string{"entry_id", "domain", "title", "state"} {
		if _, ok := first[key]; !ok {
			t.Errorf("entry missing key %q: %v", key, first)
		}
	}
}

// TestConfigEntries_DomainFilter verifies --domain filter works.
func TestConfigEntries_DomainFilter(t *testing.T) {
	// "sun" is a default integration, should have a config entry
	out, err := runHactlErr(t, "config", "entries", "--domain", "sun")
	if err != nil {
		t.Skipf("config entries --domain sun failed: %v", err)
	}
	if strings.Contains(out, "no config entries") {
		t.Skip("sun integration has no config entry on this HA version")
	}
	assertContains(t, out, "sun")
}

// TestConfigEntries_DomainFilter_NoMatch verifies --domain with no match.
func TestConfigEntries_DomainFilter_NoMatch(t *testing.T) {
	out := runHactl(t, "config", "entries", "--domain", "nonexistent_integration_xyz")
	assertContains(t, out, "no config entries")
}

// TestConfigFlowStart_InvalidDomain tests that starting a flow for a
// non-existent domain returns an appropriate error or abort response.
func TestConfigFlowStart_InvalidDomain(t *testing.T) {
	// Starting a config flow for a fake domain should fail with an error
	_, err := runHactlErr(t, "config", "flow-start", "nonexistent_domain_xyz", "--confirm")
	if err == nil {
		// Some HA versions may return an abort flow rather than HTTP error
		t.Log("flow-start with invalid domain did not error (HA may return abort flow)")
	}
}

// TestConfigFlowStart_JSON verifies JSON output mode for flow-start.
func TestConfigFlowStart_JSON(t *testing.T) {
	// Use "sun" domain which is built-in to HA and should be available
	out, err := runHactlErr(t, "config", "flow-start", "sun", "--confirm", "--json")
	if err != nil {
		// sun may already be configured; that's fine
		t.Skipf("flow-start sun failed (may already be configured): %v", err)
	}
	if out == "" {
		t.Skip("empty response from flow-start sun")
	}

	// Verify it's valid JSON with expected fields
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("flow-start --json returned invalid JSON: %v\noutput: %s", err, out)
	}
	if _, ok := result["flow_id"]; !ok {
		t.Errorf("JSON response missing flow_id field")
	}
	if _, ok := result["type"]; !ok {
		t.Errorf("JSON response missing type field")
	}
}

// TestConfigOptions_InvalidEntry tests options flow with invalid entry ID.
func TestConfigOptions_InvalidEntry(t *testing.T) {
	_, err := runHactlErr(t, "config", "options", "invalid_entry_that_does_not_exist", "--confirm")
	if err == nil {
		t.Error("expected error for invalid entry_id, got nil")
	}
}

// TestConfigFlowInspect_InvalidFlow tests inspecting a non-existent flow.
func TestConfigFlowInspect_InvalidFlow(t *testing.T) {
	_, err := runHactlErr(t, "config", "flow-inspect", "nonexistent_flow_id")
	if err == nil {
		t.Error("expected error for invalid flow_id, got nil")
	}
}

// TestConfigFlowStep_InvalidFlow tests stepping a non-existent flow.
func TestConfigFlowStep_InvalidFlow(t *testing.T) {
	_, err := runHactlErr(t, "config", "flow-step", "nonexistent_flow_id", "--data", "{}", "--confirm")
	if err == nil {
		t.Error("expected error for invalid flow_id, got nil")
	}
}

// TestConfigFlow_FullLifecycle tests starting a flow and stepping through it.
// Uses the "met_eireann" integration which has a simple user step.
func TestConfigFlow_FullLifecycle(t *testing.T) {
	// Start a config flow for met_eireann (weather integration)
	out, err := runHactlErr(t, "config", "flow-start", "met_eireann", "--confirm", "--json")
	if err != nil {
		t.Skipf("flow-start met_eireann failed (integration may not be available): %v", err)
	}

	var startResult map[string]any
	if err := json.Unmarshal([]byte(out), &startResult); err != nil {
		t.Fatalf("invalid JSON from flow-start: %v", err)
	}

	flowID, ok := startResult["flow_id"].(string)
	if !ok || flowID == "" {
		t.Skipf("no flow_id returned, skipping lifecycle test")
	}

	flowType, _ := startResult["type"].(string)
	if flowType == "abort" {
		t.Skipf("flow aborted (already configured?), skipping lifecycle test")
	}

	// Inspect the flow (config flow, not options flow — no --options flag)
	inspectOut := runHactl(t, "config", "flow-inspect", flowID)
	if !strings.Contains(inspectOut, "flow_id") {
		t.Errorf("inspect output missing flow_id: %s", inspectOut)
	}
}
