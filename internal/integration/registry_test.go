//go:build integration

package integration

import (
	"strings"
	"testing"
)

func TestLabelLs(t *testing.T) {
	out := runHactl(t, "label", "ls")
	// Should show header or "no labels"
	if !strings.Contains(out, "label_id") && !strings.Contains(out, "no labels") {
		t.Errorf("label ls unexpected output: %s", out)
	}
}

func TestLabelCreate(t *testing.T) {
	out := runHactl(t, "label", "create", "integ-test-label", "--color", "blue")
	assertContains(t, out, "created label")
	assertContains(t, out, "integ-test-label")

	// Verify it shows up in label ls
	lsOut := runHactl(t, "label", "ls")
	assertContains(t, lsOut, "integ-test-label")
}

func TestLabelLsJSON(t *testing.T) {
	out := runHactl(t, "label", "ls", "--json")
	trimmed := strings.TrimSpace(out)
	if trimmed != "" && !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
		t.Errorf("label ls --json did not produce JSON: %s", out)
	}
}

func TestAreaLs(t *testing.T) {
	out := runHactl(t, "area", "ls")
	// Should show header or "no areas"
	if !strings.Contains(out, "area_id") && !strings.Contains(out, "no areas") {
		t.Errorf("area ls unexpected output: %s", out)
	}
}

func TestAreaLsJSON(t *testing.T) {
	out := runHactl(t, "area", "ls", "--json")
	trimmed := strings.TrimSpace(out)
	if trimmed != "" && !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
		t.Errorf("area ls --json did not produce JSON: %s", out)
	}
}

func TestFloorLs(t *testing.T) {
	out := runHactl(t, "floor", "ls")
	// Should show header or "no floors"
	if !strings.Contains(out, "floor_id") && !strings.Contains(out, "no floors") {
		t.Errorf("floor ls unexpected output: %s", out)
	}
}

func TestFloorLsJSON(t *testing.T) {
	out := runHactl(t, "floor", "ls", "--json")
	trimmed := strings.TrimSpace(out)
	// May return "no floors" text or JSON array
	if trimmed != "" && !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") && !strings.Contains(trimmed, "no floors") {
		t.Errorf("floor ls --json did not produce JSON or expected message: %s", out)
	}
}

func TestAutoLsHasAreaLabelsColumns(t *testing.T) {
	out := runHactl(t, "auto", "ls")
	assertContains(t, out, "area")
	assertContains(t, out, "labels")
}

func TestScriptLsHasAreaLabelsColumns(t *testing.T) {
	out := runHactl(t, "script", "ls")
	assertContains(t, out, "area")
	assertContains(t, out, "labels")
}
