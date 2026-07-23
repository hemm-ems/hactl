//go:build integration

package integration

import (
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
	// Mint some stable IDs so the registry is non-empty before the clear.
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
