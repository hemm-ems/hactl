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

// TestIssuesJSONAgreesWithTheTable proves the two renderings of `issues` report
// the same repairs registry (H-10, H-11).
//
// This replaced a test whose whole body was `_ = runHactl(t, "issues")`. The
// interesting case here is the empty one, because it is the one a fresh HA
// actually produces: `--json` has to spell "no issues" as `[]`, a value a
// caller can range over, and the human text has to say the same thing. A
// command that printed prose on stdout under --json, or `null`, or that lost
// the rows on one path only, is red here and was green before.
func TestIssuesJSONAgreesWithTheTable(t *testing.T) {
	rows := runHactlJSON[[]map[string]any](t, "issues")
	if rows == nil {
		t.Fatal("issues --json decoded to nil: an empty registry must still be an empty JSON array")
	}

	table := runHactl(t, "issues")
	switch {
	case len(rows) == 0:
		assertContains(t, table, "no active issues")
	default:
		assertContains(t, table, "domain")
		for _, row := range rows {
			domain, _ := row["domain"].(string)
			if domain == "" {
				t.Errorf("issues --json row has no domain: %v", row)
				continue
			}
			assertContains(t, table, domain)
		}
	}
}
