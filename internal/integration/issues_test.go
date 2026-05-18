//go:build integration

package integration

import (
	"strings"
	"testing"
)

func TestIssues(t *testing.T) {
	out := runHactl(t, "issues")
	// Fresh HA typically has no issues — "no active issues" is expected
	if !strings.Contains(out, "no active issues") && !strings.Contains(out, "domain") {
		t.Errorf("issues returned unexpected output: %s", out)
	}
	assertNotContains(t, out, "panic")
}

func TestIssuesNoError(t *testing.T) {
	// Ensure the command completes without error
	_ = runHactl(t, "issues")
}
