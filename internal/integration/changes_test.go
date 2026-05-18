//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestChanges(t *testing.T) {
	out := runHactl(t, "changes", "--since", "1h")
	// Fresh HA may have no changes, so "no changes" is acceptable
	if !strings.Contains(out, "time") && !strings.Contains(out, "no changes") {
		// At minimum, output should not be empty
		if strings.TrimSpace(out) == "" {
			t.Error("changes returned empty output")
		}
	}
	assertNotContains(t, out, "panic")
}

func TestChangesJSON(t *testing.T) {
	out := runHactl(t, "changes", "--since", "1h", "--json")
	trimmed := strings.TrimSpace(out)
	if trimmed == "" {
		t.Skip("no changes in last 1h")
	}

	// Should be valid JSON array or "no changes" message
	if strings.HasPrefix(trimmed, "[") {
		var entries []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			t.Errorf("changes --json returned invalid JSON: %v", err)
		}
	} else if !strings.Contains(out, "no changes") {
		t.Errorf("changes --json returned unexpected output: %s", out)
	}
}

func TestChangesCustomSince(t *testing.T) {
	out := runHactl(t, "changes", "--since", "7d")
	assertNotContains(t, out, "panic")
}
