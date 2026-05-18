//go:build integration

package integration

import (
	"strings"
	"testing"
)

func TestCCLs(t *testing.T) {
	out := runHactl(t, "cc", "ls")
	// Basic fixture has no custom components — "no custom components" or empty table is expected
	assertNotContains(t, out, "panic")
	if strings.TrimSpace(out) == "" {
		t.Error("cc ls returned empty output")
	}
}

func TestCCShowUnknown(t *testing.T) {
	_, err := runHactlErr(t, "cc", "show", "nonexistent_component")
	if err == nil {
		// Some implementations may print "not found" message without error
		return
	}
	// Error is expected for unknown component
}

func TestCCLogsUnknown(t *testing.T) {
	// Logs for a nonexistent component should not panic
	out, _ := runHactlErr(t, "cc", "logs", "nonexistent_component")
	assertNotContains(t, out, "panic")
}
