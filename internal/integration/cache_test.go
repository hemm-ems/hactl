//go:build integration

package integration

import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCacheStatus(t *testing.T) {
	out := runHactl(t, "cache", "status")
	assertNotContains(t, out, "panic")
	// Should show some cache info (may be empty on first run)
	if len(strings.TrimSpace(out)) == 0 {
		t.Error("cache status returned empty output")
	}
}

func TestCacheRefreshTraces(t *testing.T) {
	out := runHactl(t, "cache", "refresh", "traces")
	assertNotContains(t, out, "panic")
}

func TestCacheRefreshLogs(t *testing.T) {
	out := runHactl(t, "cache", "refresh", "logs")
	assertNotContains(t, out, "panic")
}

func TestCacheRefreshAll(t *testing.T) {
	out := runHactl(t, "cache", "refresh")
	assertNotContains(t, out, "panic")
}

// TestCacheClearAndStatus checks what `cache clear` leaves behind, not merely
// that it did not panic. "wipe all local cache" is the promise; the stable-ID
// registry (cache/ids.json) is local cache, and leaving it means `trc:a7` still
// resolves to a trace the same command just deleted.
func TestCacheClearAndStatus(t *testing.T) {
	// Mint some stable IDs so the registry is non-empty before the clear. A
	// log entry is written first: a fresh HA 2026.11 logs no warning at
	// start-up, and without one nothing is minted and the test skipped itself
	// silently — leaving the shared cache synced for every test after it.
	writeSystemLogWarning(t, "hactl cache test: a warning to mint a stable id from")
	runHactl(t, "cache", "refresh")
	runHactl(t, "log", "--errors", "--warnings")

	idsPath := filepath.Join(ha.Dir(), "cache", "ids.json")
	if _, err := os.Stat(idsPath); err != nil {
		t.Skipf("no ids.json was minted, nothing to prove: %v", err)
	}

	clearOut := runHactl(t, "cache", "clear")
	assertContains(t, clearOut, "cache cleared")

	if _, err := os.Stat(idsPath); !os.IsNotExist(err) {
		t.Errorf("cache clear left %s behind (err=%v); stable IDs still resolve to data it deleted",
			idsPath, err)
	}

	statusOut := runHactl(t, "cache", "status")
	assertNotContains(t, statusOut, "panic")
}

// writeSystemLogWarning logs a warning in the shared instance's system log
// through HA's own system_log.write service, over plain HTTP.
func writeSystemLogWarning(t *testing.T, message string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"message": message, "level": "warning"})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ha.URL()+"/api/services/system_log/write", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+ha.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("system_log.write: %s", resp.Status)
	}
}
