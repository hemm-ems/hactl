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
	// The basic fixture's default_config always registers config entries (sun,
	// met, radio_browser, …); an empty list here is a real failure, not an
	// environment to skip.
	if len(entries) == 0 {
		t.Fatalf("config entries --json returned no entries; the basic fixture always has some")
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

// TestConfigFlowStartDryRunPreviewsUnloadedDomain proves the dry run previews a
// flow-capable-but-not-yet-configured integration instead of rejecting it.
//
// The old resolver validated the domain against manifest/list (integrations HA
// has *loaded*), so met_eireann — installable, with a working config flow, but
// not configured on this instance — was refused as "no loaded integration",
// failing exactly where a confirmed flow-start succeeds. That is the inverse of
// the H-2 contract and it broke the command's whole purpose (you start a flow
// for something not yet configured). The resolver now uses HA's flow_handlers
// list, the authority the confirmed run agrees with.
func TestConfigFlowStartDryRunPreviewsUnloadedDomain(t *testing.T) {
	// met_eireann is not configured on the shared instance (so it is absent from
	// manifest/list) yet does expose a config flow.
	out, err := runHactlErr(t, "config", "flow-start", "met_eireann")
	if err != nil {
		t.Fatalf("dry-run flow-start for an installable flow domain must preview, got: %v\noutput: %s", err, out)
	}
	assertContains(t, out, "met_eireann")
	assertContains(t, out, "dry-run")
}

// TestConfigFlowStartRejectsUnknownDomain proves the dry run refuses exactly
// what the confirmed run refuses: a domain with no config flow. The dry run
// resolves the domain against HA's flow-handler list, and --confirm 404s on the
// same input ("Invalid handler specified"); the two must agree.
//
// The old test only t.Log'd when --confirm happened not to error — a silent
// pass that let a broken flow path stay green.
func TestConfigFlowStartRejectsUnknownDomain(t *testing.T) {
	const domain = "nonexistent_domain_xyz"
	_, dryErr := runHactlErr(t, "config", "flow-start", domain)
	_, confirmErr := runHactlErr(t, "config", "flow-start", domain, "--confirm")
	if dryErr == nil {
		t.Errorf("dry-run flow-start for %q must error (no such config flow)", domain)
	}
	if confirmErr == nil {
		t.Errorf("--confirm flow-start for %q must error (HA rejects the handler)", domain)
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
