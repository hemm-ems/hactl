//go:build integration

package integration

import (
	"strings"
	"testing"
)

func TestLog(t *testing.T) {
	// Log command should not fail; HA will have some log entries
	out := runHactl(t, "log")
	_ = out // just ensure no error
}

func TestLogErrors(t *testing.T) {
	out := runHactl(t, "log", "--errors")
	_ = out // may or may not have errors; should not fail
}

func TestLogErrorsUnique(t *testing.T) {
	out := runHactl(t, "log", "--errors", "--unique")
	// Should show a table or empty result
	assertNotContains(t, out, "panic")
}

func TestLogComponent(t *testing.T) {
	// Filter by a component — homeassistant is always present in logs
	out := runHactl(t, "log", "--component", "homeassistant")
	assertNotContains(t, out, "panic")
}

func TestLogOutput(t *testing.T) {
	lines := runHactlLines(t, "log")
	// HA always produces some log output; at minimum we expect lines
	if len(lines) == 0 {
		t.Log("log returned empty output (possible on very fresh HA)")
	}
	for _, l := range lines {
		assertNotContains(t, l, "panic")
	}
}

func TestLogErrorsUniqueTable(t *testing.T) {
	out := runHactl(t, "log", "--errors", "--unique")
	if strings.TrimSpace(out) == "" {
		t.Log("no errors in log (expected for clean HA instance)")
		return
	}
	// If there are errors, output should have table format with a header
	if !strings.Contains(out, "count") && !strings.Contains(out, "message") && !strings.Contains(out, "no errors") {
		t.Errorf("log --errors --unique has unexpected format: %s", out)
	}
}
