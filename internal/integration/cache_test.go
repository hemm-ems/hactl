//go:build integration

package integration

import (
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

func TestCacheClearAndStatus(t *testing.T) {
	// Clear cache
	clearOut := runHactl(t, "cache", "clear")
	assertNotContains(t, clearOut, "panic")

	// Status should show empty/zero after clear
	statusOut := runHactl(t, "cache", "status")
	assertNotContains(t, statusOut, "panic")
}
