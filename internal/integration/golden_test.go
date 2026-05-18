//go:build integration

package integration

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// goldenDir returns the absolute path to testdata/golden/.
func goldenDir() string {
	//nolint:dogsled // only need file from runtime.Caller
	_, file, _, _ := runtime.Caller(0)
	// integration/ → hactl root → testdata/golden
	root := filepath.Join(filepath.Dir(file), "..", "..")
	return filepath.Join(root, "testdata", "golden")
}

// assertGolden compares got against a golden file testdata/golden/<name>.txt.
// If HACTL_UPDATE_GOLDEN=1, the golden file is overwritten instead.
func assertGolden(t *testing.T, name, got string) {
	t.Helper()

	sanitized := sanitizeGolden(got)
	dir := goldenDir()
	path := filepath.Join(dir, name+".txt")

	if os.Getenv("HACTL_UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatalf("creating golden dir: %v", err)
		}
		if err := os.WriteFile(path, []byte(sanitized), 0o600); err != nil {
			t.Fatalf("writing golden file: %v", err)
		}
		t.Logf("updated golden file: %s", path)
		return
	}

	expected, err := os.ReadFile(path) //nolint:gosec // golden file path is test-controlled
	if err != nil {
		t.Fatalf("reading golden file %s: %v (run with HACTL_UPDATE_GOLDEN=1 to create)", path, err)
	}

	want := strings.ReplaceAll(strings.TrimRight(string(expected), "\r\n"), "\r", "")
	gotTrimmed := strings.ReplaceAll(strings.TrimRight(sanitized, "\r\n"), "\r", "")

	if gotTrimmed != want {
		t.Errorf("golden mismatch for %s\n--- want ---\n%s\n--- got ---\n%s",
			name, want, gotTrimmed)
	}
}
