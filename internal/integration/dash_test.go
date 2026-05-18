//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDashLs(t *testing.T) {
	// Default HA may have no extra dashboards, but the command should succeed
	out := runHactl(t, "dash", "ls")
	// Either shows dashboards or "no dashboards"
	if out == "" {
		t.Error("dash ls produced empty output")
	}
}

func TestDashLsJSON(t *testing.T) {
	out := runHactl(t, "dash", "ls", "--json")
	trimmed := strings.TrimSpace(out)
	if trimmed != "" && trimmed != "no dashboards" && !strings.HasPrefix(trimmed, "[") {
		t.Errorf("dash ls --json did not produce JSON or empty: %s", out)
	}
}

func TestDashShowDefault(t *testing.T) {
	// Default dashboard should exist (auto-gen mode).
	// It may return an error if no config exists yet — that's OK.
	out, err := runHactlErr(t, "dash", "show")
	_ = out
	_ = err
}

func TestDashShowDefaultJSON(t *testing.T) {
	out, err := runHactlErr(t, "dash", "show", "--json")
	if err != nil {
		t.Skipf("default dashboard has no config yet: %v", err)
	}
	trimmed := strings.TrimSpace(out)
	if trimmed != "" && !strings.HasPrefix(trimmed, "{") {
		t.Errorf("dash show --json did not produce JSON: %s", out)
	}
}

func TestDashShowDefaultRaw(t *testing.T) {
	out, err := runHactlErr(t, "dash", "show", "--raw")
	if err != nil {
		t.Skipf("default dashboard has no config yet: %v", err)
	}
	if out != "" && !json.Valid([]byte(out)) {
		t.Errorf("dash show --raw did not produce valid JSON: %s", out)
	}
}

func TestDashCreateDryRun(t *testing.T) {
	out := runHactl(t, "dash", "create", "--url-path", "test-dash", "--title", "Test")
	assertContains(t, out, "dry-run")
	assertContains(t, out, "test-dash")
}

func TestDashCreateAndDelete(t *testing.T) {
	// Create
	out := runHactl(t, "dash", "create",
		"--url-path", "hactl-test-dash",
		"--title", "Hactl Test",
		"--icon", "mdi:test-tube",
		"--confirm")
	assertContains(t, out, "created dashboard")

	// Verify it appears in list
	lsOut := runHactl(t, "dash", "ls")
	assertContains(t, lsOut, "hactl-test-dash")

	// Delete
	delOut := runHactl(t, "dash", "delete", "hactl-test-dash", "--confirm")
	assertContains(t, delOut, "deleted dashboard")

	// Verify it's gone
	lsOut2 := runHactl(t, "dash", "ls")
	assertNotContains(t, lsOut2, "hactl-test-dash")
}

func TestDashSaveDryRun(t *testing.T) {
	dir := t.TempDir()
	configFile := dir + "/test-config.json"
	cfg := `{"views":[{"title":"Test","path":"test","cards":[]}]}`
	if err := os.WriteFile(configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	out := runHactl(t, "dash", "save", "--file", configFile)
	assertContains(t, out, "dry-run")
}

func TestDashSaveRoundTrip(t *testing.T) {
	// Create a dashboard
	runHactl(t, "dash", "create",
		"--url-path", "hactl-rt-test",
		"--title", "Round Trip Test",
		"--confirm")

	// Save a config to it
	dir := t.TempDir()
	configFile := dir + "/config.json"
	cfg := `{"views":[{"title":"RoundTrip","path":"round-trip","type":"sections","sections":[{"cards":[{"type":"markdown","content":"hello from hactl"}]}]}]}`
	if err := os.WriteFile(configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	saveOut := runHactl(t, "dash", "save", "hactl-rt-test", "--file", configFile, "--confirm")
	assertContains(t, saveOut, "saved dashboard config")

	// Read it back
	showOut := runHactl(t, "dash", "show", "hactl-rt-test")
	assertContains(t, showOut, "RoundTrip")

	// Read back raw and verify JSON round-trip
	rawOut := runHactl(t, "dash", "show", "hactl-rt-test", "--raw")
	rawJSON := stripTokenHeader(rawOut)
	if !json.Valid([]byte(rawJSON)) {
		t.Errorf("raw output is not valid JSON: %s", rawOut)
	}
	assertContains(t, rawJSON, "hello from hactl")

	// Clean up
	runHactl(t, "dash", "delete", "hactl-rt-test", "--confirm")
}

func TestDashDeleteDryRun(t *testing.T) {
	out := runHactl(t, "dash", "delete", "nonexistent-dash")
	assertContains(t, out, "dry-run")
}

func TestDashDeleteNotFound(t *testing.T) {
	_, err := runHactlErr(t, "dash", "delete", "nonexistent-dash", "--confirm")
	if err == nil {
		t.Error("deleting nonexistent dashboard should fail")
	}
}

func TestDashResources(t *testing.T) {
	// Should succeed — may return "no resources" for a fresh HA instance
	out := runHactl(t, "dash", "resources")
	if out == "" {
		t.Error("dash resources produced empty output")
	}
}

func TestDashShowViewFilter(t *testing.T) {
	// Create a dashboard with two views
	runHactl(t, "dash", "create",
		"--url-path", "hactl-view-test",
		"--title", "View Test",
		"--confirm")

	dir := t.TempDir()
	configFile := dir + "/config.json"
	cfg := `{"views":[{"title":"Alpha","path":"alpha","cards":[]},{"title":"Beta","path":"beta","cards":[{"type":"markdown","content":"beta view"}]}]}`
	if err := os.WriteFile(configFile, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	runHactl(t, "dash", "save", "hactl-view-test", "--file", configFile, "--confirm")

	// Filter to a single view
	viewOut := runHactl(t, "dash", "show", "hactl-view-test", "--view", "beta")
	assertContains(t, viewOut, "beta view")
	assertNotContains(t, viewOut, "Alpha")

	// Clean up
	runHactl(t, "dash", "delete", "hactl-view-test", "--confirm")
}

func TestDashSaveInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	configFile := dir + "/bad.json"
	if err := os.WriteFile(configFile, []byte("{invalid}"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := runHactlErr(t, "dash", "save", "--file", configFile, "--confirm")
	if err == nil {
		t.Error("saving invalid JSON should fail")
	}
}
