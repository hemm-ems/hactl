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

// TestCCShowUnknown pins what `cc show` does with a domain that does not
// exist. The old body accepted both outcomes — it returned early when the
// command succeeded and asserted nothing when it failed — so it could not tell
// a refusal from a confident answer about a component HA has never heard of.
//
// The contract is a refusal that names the argument: `cc show` is the read side
// of the fabrication defect that made `cc ls` invent custom components out of
// built-in `update.*` entities, and an empty-but-successful answer there is the
// same class of lie (TC-3 — emptiness is never success).
func TestCCShowUnknown(t *testing.T) {
	const unknown = "hactl_nonexistent_component"

	out, err := runHactlErr(t, "cc", "show", unknown)
	if err == nil {
		t.Fatalf("cc show %s succeeded for a component HA does not have; output:\n%s", unknown, out)
	}
	if !strings.Contains(err.Error(), unknown) {
		t.Errorf("cc show %s failed with %q, which does not name the component asked for", unknown, err)
	}
	// A refusal must not also print a record: half an answer next to an error is
	// how a caller ends up quoting a component that does not exist.
	if strings.Contains(out, "domain:") {
		t.Errorf("cc show %s printed a component record alongside its error:\n%s", unknown, out)
	}
}

func TestCCLogsUnknown(t *testing.T) {
	// Logs for a nonexistent component should not panic
	out, _ := runHactlErr(t, "cc", "logs", "nonexistent_component")
	assertNotContains(t, out, "panic")
}
