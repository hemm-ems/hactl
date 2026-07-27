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

// TestDashShowDefault drives `dash show` (no argument) through BOTH states of
// the default dashboard, with the state set explicitly through HA's own
// lovelace/config/save and lovelace/config/delete (lovelace_oracle_test.go).
//
// History, because this test inverted twice: the original body accepted every
// outcome (`_ = out; _ = err`); its replacement read HA first and required
// hactl to FAIL when no config is stored — which asserted defect D67 as
// correct. D-3 decides the pole: the auto-generated state is an answer, not an
// error — name the state, point to `dash ls`, and never fabricate a strategy
// render. The old expectation being wrong is the finding.
func TestDashShowDefault(t *testing.T) {
	// --- auto-generated: an honest report, exit 0 ---
	deleteDefaultDashboardConfig(t, ha)
	out := runHactl(t, "dash", "show")
	assertContains(t, out, "auto-generated")
	assertContains(t, out, "dash ls")

	// --- stored: the views summary, matching what HA holds ---
	storeDefaultDashboardConfig(t, ha, map[string]any{"views": []any{
		map[string]any{"title": "OracleDefault", "path": "oracle-default",
			"cards": []any{map[string]any{"type": "markdown", "content": "d3"}}},
	}})
	out = runHactl(t, "dash", "show")
	assertContains(t, out, "OracleDefault")
	assertContains(t, out, "oracle-default")
	assertNotContains(t, out, "auto-generated")
}

// TestDashShowDefaultJSON pins the machine contract (H-10): a caller must be
// able to tell the two states apart by looking at the object. The stored
// answer is the config document; the auto-generated answer carries
// "state": "auto-generated" and no views.
func TestDashShowDefaultJSON(t *testing.T) {
	deleteDefaultDashboardConfig(t, ha)
	out := runHactl(t, "dash", "show", "--json")
	var autoObj map[string]any
	if err := json.Unmarshal([]byte(out), &autoObj); err != nil {
		t.Fatalf("auto-generated --json did not parse: %v\n%s", err, out)
	}
	if autoObj["state"] != "auto-generated" {
		t.Errorf("auto-generated --json state = %v, want auto-generated:\n%s", autoObj["state"], out)
	}
	if _, hasViews := autoObj["views"]; hasViews {
		t.Errorf("auto-generated --json must not fabricate views:\n%s", out)
	}

	storeDefaultDashboardConfig(t, ha, map[string]any{"views": []any{
		map[string]any{"title": "JsonDefault", "path": "json-default"},
	}})
	out = runHactl(t, "dash", "show", "--json")
	var storedObj map[string]any
	if err := json.Unmarshal([]byte(out), &storedObj); err != nil {
		t.Fatalf("stored --json did not parse: %v\n%s", err, out)
	}
	if _, hasViews := storedObj["views"]; !hasViews {
		t.Errorf("stored --json must carry the config document:\n%s", out)
	}
	if _, hasState := storedObj["state"]; hasState {
		t.Errorf("stored --json must not carry the auto-generated discriminator:\n%s", out)
	}
}

// TestDashShowDefaultRaw: --raw exists for round-trip editing, so for the
// auto-generated default (no stored config to round-trip) it must refuse
// rather than fabricate; with a stored config it emits that document.
func TestDashShowDefaultRaw(t *testing.T) {
	deleteDefaultDashboardConfig(t, ha)
	out, err := runHactlErr(t, "dash", "show", "--raw")
	if err == nil {
		t.Fatalf("--raw must refuse for the auto-generated default, got:\n%s", out)
	}
	if !strings.Contains(err.Error(), "auto-generated") {
		t.Errorf("--raw refusal must name the state, got: %v", err)
	}

	storeDefaultDashboardConfig(t, ha, map[string]any{"views": []any{
		map[string]any{"title": "RawDefault", "path": "raw-default"},
	}})
	out = runHactl(t, "dash", "show", "--raw", "--tokensmax=0")
	rawJSON := stripTokenHeader(out)
	if !json.Valid([]byte(rawJSON)) {
		t.Errorf("dash show --raw did not produce valid JSON: %s", out)
	}
	assertContains(t, rawJSON, "raw-default")
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

// TestDashDeleteAgreesOnUnknownDashboard pins the dry run and the confirmed
// run to the same answer.
//
// These were two tests, and together they documented the defect: the confirmed
// run failed on a dashboard HA does not have, while the dry run printed
// "dry-run: would delete dashboard" and exited 0 for the same argument. Under
// the manual's stop-at-the-first-miss rule a typo read as a verified plan, so
// the dry-run half is inverted here deliberately.
func TestDashDeleteAgreesOnUnknownDashboard(t *testing.T) {
	for _, args := range [][]string{
		{"dash", "delete", "nonexistent-dash"},
		{"dash", "delete", "nonexistent-dash", "--confirm"},
	} {
		out, err := runHactlErr(t, args...)
		if err == nil {
			t.Errorf("%v: expected failure for a dashboard HA does not have, got:\n%s", args, out)
		}
	}
}

// TestDashDeleteDryRunPreviewsRealDashboard is the other half: a dashboard
// that does exist previews, names itself, and is still there afterwards.
func TestDashDeleteDryRunPreviewsRealDashboard(t *testing.T) {
	const urlPath = "hactl-preview-target"
	runHactl(t, "dash", "create", "--url-path", urlPath, "--title", "Preview Target", "--confirm")
	t.Cleanup(func() { _, _ = runHactlErr(t, "dash", "delete", urlPath, "--confirm") })

	out := runHactl(t, "dash", "delete", urlPath)
	assertContains(t, out, "dry-run")
	// The title is the witness that the preview resolved against HA.
	assertContains(t, out, "Preview Target")

	if out := runHactl(t, "dash", "ls"); !strings.Contains(out, urlPath) {
		t.Errorf("dry-run delete removed the dashboard:\n%s", out)
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

	viewJSON := runHactl(t, "dash", "show", "hactl-view-test", "--view", "beta", "--json")
	var jsonView map[string]any
	if err := json.Unmarshal([]byte(viewJSON), &jsonView); err != nil {
		t.Fatalf("dash show --view --json invalid: %v\n%s", err, viewJSON)
	}
	if jsonView["path"] != "beta" {
		t.Errorf("json view path = %v, want beta", jsonView["path"])
	}
	if _, hasViews := jsonView["views"]; hasViews {
		t.Errorf("json view output should not include full dashboard views: %s", viewJSON)
	}
	assertNotContains(t, viewJSON, "Alpha")

	viewRaw := stripTokenHeader(runHactl(t, "dash", "show", "hactl-view-test", "--view", "beta", "--raw", "--tokensmax=0"))
	var rawView map[string]any
	if err := json.Unmarshal([]byte(viewRaw), &rawView); err != nil {
		t.Fatalf("dash show --view --raw invalid: %v\n%s", err, viewRaw)
	}
	if rawView["path"] != "beta" {
		t.Errorf("raw view path = %v, want beta", rawView["path"])
	}
	if _, hasViews := rawView["views"]; hasViews {
		t.Errorf("raw view output should not include full dashboard views: %s", viewRaw)
	}
	assertNotContains(t, viewRaw, "Alpha")

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
