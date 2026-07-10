package cmd

import (
	"os"
	"path/filepath"
	"strings"
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
	if got, rerr := os.ReadFile(path); rerr != nil || string(got) != string(raw) { //nolint:gosec // reads back a file just written under t.TempDir()
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

	// A traversal-style url_path must never escape the snapshot dir.
	p3, err := writeDashboardSnapshot(dir, "../../../etc/hosts", raw)
	if err != nil {
		t.Fatalf("writeDashboardSnapshot traversal: %v", err)
	}
	wantPrefix := filepath.Join(dir, "backups", "dashboards") + string(os.PathSeparator)
	if !strings.HasPrefix(p3, wantPrefix) {
		t.Errorf("traversal url_path escaped snapshot dir: %s (want under %s)", p3, wantPrefix)
	}
	if strings.ContainsRune(filepath.Base(p3), os.PathSeparator) {
		t.Errorf("snapshot filename contains a path separator: %s", p3)
	}
}

func TestSanitizeSnapshotName(t *testing.T) {
	cases := map[string]string{
		"":                "default",
		"lovelace":        "lovelace",
		"lovelace-dev":    "lovelace-dev",
		"lovelace/energy": "lovelace_energy",
		"..":              "default",
		"...":             "default",
		"../../etc/hosts": "_.._etc_hosts",
		"a\\b":            "a_b",
		"weird name!$":    "weird_name__",
	}
	for in, want := range cases {
		if got := sanitizeSnapshotName(in); got != want {
			t.Errorf("sanitizeSnapshotName(%q) = %q, want %q", in, got, want)
		}
		// Invariant: the result is always a single, separator-free component.
		if strings.ContainsAny(sanitizeSnapshotName(in), `/\`) {
			t.Errorf("sanitizeSnapshotName(%q) leaked a separator", in)
		}
	}
}
