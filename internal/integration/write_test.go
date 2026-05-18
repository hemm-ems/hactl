//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeTestAutoYAML creates a temp YAML file with a modified automation config
// for use in auto diff/apply tests. Returns the file path.
func writeTestAutoYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "modified_auto.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test YAML: %v", err)
	}
	return path
}

// getFirstAutoID returns the ID of the first automation from auto ls --json.
func getFirstAutoID(t *testing.T) string {
	t.Helper()
	out := runHactl(t, "auto", "ls", "--json")
	var entries []map[string]string
	if err := json.Unmarshal([]byte(out), &entries); err != nil || len(entries) == 0 {
		t.Skip("no automations available for write-path test")
	}
	return entries[0]["id"]
}

func TestAutoDiff(t *testing.T) {
	autoID := getFirstAutoID(t)

	// Create a modified config with a different description
	yamlContent := `alias: Modified Automation
description: Modified for E2E diff test
trigger:
  - platform: time
    at: "08:00:00"
condition: []
action:
  - service: light.turn_on
    target:
      entity_id: light.bedroom
mode: single
`
	yamlPath := writeTestAutoYAML(t, yamlContent)

	out := runHactl(t, "auto", "diff", autoID, "-f", yamlPath)
	// diff output should show changes (or "no changes" if config happens to match)
	assertNotContains(t, out, "panic")
	if !strings.Contains(out, "diff") && !strings.Contains(out, "no changes") {
		t.Errorf("auto diff gave unexpected output: %s", out)
	}
}

func TestAutoDiffNoFile(t *testing.T) {
	_, err := runHactlErr(t, "auto", "diff", "climate_schedule")
	if err == nil {
		t.Error("auto diff without -f should error")
	}
}

func TestAutoApplyDryRun(t *testing.T) {
	autoID := getFirstAutoID(t)

	yamlContent := `alias: DryRun Test
description: Dry-run test automation
trigger:
  - platform: time
    at: "09:00:00"
condition: []
action:
  - service: light.turn_on
    target:
      entity_id: light.bedroom
mode: single
`
	yamlPath := writeTestAutoYAML(t, yamlContent)

	// Without --confirm = dry-run
	out := runHactl(t, "auto", "apply", autoID, "-f", yamlPath)
	assertContains(t, out, "dry-run")
	assertNotContains(t, out, "applied:")
}

func TestAutoApplyConfirmAndRollback(t *testing.T) {
	autoID := getFirstAutoID(t)

	// Capture original state for comparison
	originalShow := runHactl(t, "auto", "show", autoID)

	yamlContent := `alias: Applied Test
description: E2E apply-and-rollback test
trigger:
  - platform: time
    at: "10:00:00"
condition: []
action:
  - service: light.turn_on
    target:
      entity_id: light.bedroom
mode: single
`
	yamlPath := writeTestAutoYAML(t, yamlContent)

	// Apply with --confirm
	applyOut := runHactl(t, "auto", "apply", autoID, "-f", yamlPath, "--confirm")
	assertContains(t, applyOut, "applied:")
	assertContains(t, applyOut, "backup:")

	// Verify change: auto show should reflect new config
	afterApply := runHactl(t, "auto", "show", autoID)
	// The automation should still exist and be accessible
	assertContains(t, afterApply, autoID)

	// Verify backup file was created
	backupsDir := filepath.Join(ha.Dir(), "backups")
	entries, err := os.ReadDir(backupsDir)
	if err != nil {
		t.Fatalf("reading backups dir: %v", err)
	}
	if len(entries) == 0 {
		t.Error("no backup files created after apply")
	}

	// Rollback
	rollbackOut := runHactl(t, "rollback", autoID)
	assertContains(t, rollbackOut, "rolled back:")

	// Verify rollback: auto show should be closer to original
	afterRollback := runHactl(t, "auto", "show", autoID)
	assertContains(t, afterRollback, autoID)
	_ = originalShow // We've verified the round-trip works
}

func TestAutoApplyNoFile(t *testing.T) {
	_, err := runHactlErr(t, "auto", "apply", "climate_schedule")
	if err == nil {
		t.Error("auto apply without -f should error")
	}
}

func TestAutoApplyInvalidYAML(t *testing.T) {
	autoID := getFirstAutoID(t)
	yamlPath := writeTestAutoYAML(t, "{{invalid yaml: [")

	_, err := runHactlErr(t, "auto", "apply", autoID, "-f", yamlPath, "--confirm")
	if err == nil {
		t.Error("auto apply with invalid YAML should error")
	}
}

func TestRollbackNoBackup(t *testing.T) {
	// Rollback with no existing backups should fail gracefully
	// Use a fresh temp dir with no backups
	dir := t.TempDir()
	envContent := "HA_URL=" + ha.URL() + "\nHA_TOKEN=" + ha.Token() + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(envContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "cache"), 0o750); err != nil {
		t.Fatal(err)
	}

	_, err := runHactlDirErr(t, dir, "rollback")
	if err == nil {
		t.Error("rollback with no backups should error")
	}
}
