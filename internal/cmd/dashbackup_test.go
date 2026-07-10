package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteDashboardSnapshot(t *testing.T) {
	dir := t.TempDir()
	raw := []byte(`{"views":[{"cards":[]}]}`)

	// Default dashboard (empty url_path) -> "default".
	path, err := writeDashboardSnapshot(dir, "", raw)
	if err != nil {
		t.Fatalf("writeDashboardSnapshot: %v", err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(raw) {
		t.Fatalf("snapshot content mismatch: %q err=%v", got, err)
	}
	if base := filepath.Base(filepath.Dir(path)); base != "dashboards" {
		t.Errorf("snapshot not under backups/dashboards, got dir %s", filepath.Dir(path))
	}
	if name := filepath.Base(path); name[:len("default.")] != "default." {
		t.Errorf("default dashboard snapshot name = %s, want default.*", name)
	}

	// A url_path with a slash must be sanitized into the filename.
	p2, err := writeDashboardSnapshot(dir, "lovelace/energy", raw)
	if err != nil {
		t.Fatalf("writeDashboardSnapshot slash: %v", err)
	}
	if name := filepath.Base(p2); name[:len("lovelace_energy.")] != "lovelace_energy." {
		t.Errorf("slash not sanitized in snapshot name: %s", name)
	}
}
