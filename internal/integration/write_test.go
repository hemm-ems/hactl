//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This tier boots Home Assistant alone. Since `auto apply`/`auto rollback`
// stopped writing through HA's own config endpoint (D-14, issue #128), that
// makes it the tier where those commands cannot run — so what it asserts here
// is the refusal. The round trip itself moved to internal/companiontest, where
// a companion exists: `auto_write_e2e_test.go`.

// writeTestAutoYAML creates a temp YAML file with an automation config, for the
// cases that need a candidate file on disk.
func writeTestAutoYAML(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "modified_auto.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("writing test YAML: %v", err)
	}
	return path
}

// TestAutoWritesRefuseWithoutACompanion pins the absence of a fallback.
//
// Home Assistant's `POST /api/config/automation/config/<id>` still exists and
// still works; using it is what re-dumped the whole automations.yaml on every
// confirmed apply. So the interesting property is not that the companion route
// works — companiontest proves that — but that hactl does NOT quietly reach for
// the endpoint that does the damage when the companion is missing. A silent
// fallback restores exactly the behaviour the reroute removed, on the instances
// least likely to notice.
func TestAutoWritesRefuseWithoutACompanion(t *testing.T) {
	candidate := writeTestAutoYAML(t, `alias: Should Never Be Written
trigger: []
condition: []
action: []
`)
	for _, args := range [][]string{
		{"auto", "diff", "climate_schedule", "-f", candidate},
		{"auto", "apply", "climate_schedule", "-f", candidate},
		{"auto", "apply", "climate_schedule", "-f", candidate, "--confirm"},
	} {
		out, err := runHactlErr(t, args...)
		if err == nil {
			t.Errorf("%v succeeded on an instance with no companion:\n%s", args, out)
			continue
		}
		// The reason lives in the error, not in stdout: a refused command
		// writes nothing to its answer stream (H-22).
		if !strings.Contains(err.Error(), "companion") {
			t.Errorf("%v failed for a reason that does not name the companion: %v", args, err)
		}
		if out != "" {
			t.Errorf("%v wrote to stdout while refusing:\n%s", args, out)
		}
	}
}

func TestAutoApplyNoFile(t *testing.T) {
	_, err := runHactlErr(t, "auto", "apply", "climate_schedule")
	if err == nil {
		t.Error("auto apply without -f should error")
	}
}

func TestAutoDiffNoFile(t *testing.T) {
	_, err := runHactlErr(t, "auto", "diff", "climate_schedule")
	if err == nil {
		t.Error("auto diff without -f should error")
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

// mustJSON renders a decoded config for an error message, so a mismatch shows
// what the two sides actually were.
func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling config: %v", err)
	}
	return string(b)
}
