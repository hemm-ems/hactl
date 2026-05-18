//go:build integration

package integration

import (
	"strings"
	"testing"
)

// --- Area create / delete lifecycle ---

func TestAreaCreateDelete(t *testing.T) {
	// Create
	out := runHactl(t, "area", "create", "integ-test-area")
	assertContains(t, out, "created area")

	// Verify it shows up in area ls
	lsOut := runHactl(t, "area", "ls")
	assertContains(t, lsOut, "integ-test-area")

	// Extract area_id from create output: "created area "integ-test-area" (id=integ_test_area)"
	// The id is the normalized form
	areaID := "integ_test_area"
	if idx := strings.Index(out, "(id="); idx >= 0 {
		end := strings.Index(out[idx:], ")")
		if end > 4 {
			areaID = out[idx+4 : idx+end]
		}
	}

	// Delete (dry-run first)
	dryOut := runHactl(t, "area", "delete", areaID)
	assertContains(t, dryOut, "dry-run")

	// Delete for real
	delOut := runHactl(t, "area", "delete", areaID, "--confirm")
	assertContains(t, delOut, "deleted area")

	// Verify removed
	lsAfter := runHactl(t, "area", "ls")
	assertNotContains(t, lsAfter, areaID)
}

// --- Floor create / delete lifecycle ---

func TestFloorCreateDelete(t *testing.T) {
	out := runHactl(t, "floor", "create", "integ-test-floor")
	assertContains(t, out, "created floor")

	lsOut := runHactl(t, "floor", "ls")
	assertContains(t, lsOut, "integ_test_floor")

	floorID := "integ_test_floor"
	if idx := strings.Index(out, "(id="); idx >= 0 {
		end := strings.Index(out[idx:], ")")
		if end > 4 {
			floorID = out[idx+4 : idx+end]
		}
	}

	// Delete (dry-run first)
	dryOut := runHactl(t, "floor", "delete", floorID)
	assertContains(t, dryOut, "dry-run")

	// Delete for real
	delOut := runHactl(t, "floor", "delete", floorID, "--confirm")
	assertContains(t, delOut, "deleted floor")
}

// --- Label delete lifecycle ---

func TestLabelDeleteLifecycle(t *testing.T) {
	// Create a label first
	out := runHactl(t, "label", "create", "integ-delete-label", "--color", "green")
	assertContains(t, out, "created label")

	// Extract actual label_id from output: created label "integ-delete-label" (id=integ_delete_label)
	labelID := "integ_delete_label"
	if idx := strings.Index(out, "(id="); idx >= 0 {
		end := strings.Index(out[idx:], ")")
		if end > 4 {
			labelID = out[idx+4 : idx+end]
		}
	}

	// Delete (dry-run first)
	dryOut := runHactl(t, "label", "delete", labelID)
	assertContains(t, dryOut, "dry-run")

	// Delete for real
	delOut := runHactl(t, "label", "delete", labelID, "--confirm")
	assertContains(t, delOut, "deleted label")

	// Verify removed
	lsAfter := runHactl(t, "label", "ls")
	assertNotContains(t, lsAfter, labelID)
}
