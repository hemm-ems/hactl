package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// entLabelUnmakeStub wires two entities sharing one label, so a removal test
// can assert the PARTIAL nature finding #81 is about: taking a label off one
// entity must leave that entity's other labels alone AND leave the same label
// on every OTHER entity untouched — the property that distinguishes
// `ent set-label --remove` from `label delete`, which strips a label from
// every holder at once.
func entLabelUnmakeStub(t *testing.T) *cmdTestServer {
	t.Helper()
	return startCmdServer(t, map[string]any{
		"config/entity_registry/list": []any{
			map[string]any{"entity_id": "sensor.a", "labels": []string{"energy", "red"}},
			map[string]any{"entity_id": "sensor.b", "labels": []string{"red"}},
		},
		"config/label_registry/list": []any{
			map[string]any{"label_id": "energy", "name": "Energy"},
			map[string]any{"label_id": "red", "name": "Red"},
		},
		"config/entity_registry/update": map[string]any{"entity_id": "sensor.a"},
	}, nil)
}

// TestRunEntSetLabel_RemoveTakesOneLabelOff is the unmake surface's proof for
// `hactl ent set-label` (dev/surfaces/unmake.manifest, H-27): --remove takes
// one label off ONE entity, leaves that entity's other label alone, and never
// touches a second entity carrying the same label — the shape `label delete
// red --confirm` cannot express, because it removes "red" from both
// sensor.a and sensor.b at once.
func TestRunEntSetLabel_RemoveTakesOneLabelOff(t *testing.T) {
	ts := entLabelUnmakeStub(t)
	withFlagDir(t, ts.dir)
	oldConfirm := flagEntConfirm
	flagEntConfirm = true
	defer func() { flagEntConfirm = oldConfirm }()
	oldRemove := flagEntRemoveLabel
	flagEntRemoveLabel = []string{"red"}
	defer func() { flagEntRemoveLabel = oldRemove }()

	var buf bytes.Buffer
	if err := runEntSetLabel(context.Background(), &buf, "sensor.a", nil); err != nil {
		t.Fatalf("runEntSetLabel --remove red failed: %v", err)
	}

	params := ts.lastParams("config/entity_registry/update")
	if params["entity_id"] != "sensor.a" {
		t.Fatalf("update targeted %v, want sensor.a only", params["entity_id"])
	}
	labels, _ := params["labels"].([]any)
	if len(labels) != 1 || labels[0] != "energy" {
		t.Fatalf("wire labels = %v, want [energy] — red should have come off, energy should stay (partial removal)",
			labels)
	}
	if got := ts.commandCount("config/entity_registry/update"); got != 1 {
		t.Fatalf("entity_registry/update sent %d times, want exactly 1 — sensor.b must be untouched", got)
	}
	if !strings.Contains(buf.String(), "sensor.a") {
		t.Errorf("output missing entity: %q", buf.String())
	}
}

// TestRunEntSetLabel_RemoveDryRunPreviewsWithoutWriting mirrors H-2 for the
// new flag: the preview names what would come off and sends nothing.
func TestRunEntSetLabel_RemoveDryRunPreviewsWithoutWriting(t *testing.T) {
	ts := entLabelUnmakeStub(t)
	withFlagDir(t, ts.dir)
	oldConfirm := flagEntConfirm
	flagEntConfirm = false
	defer func() { flagEntConfirm = oldConfirm }()
	oldRemove := flagEntRemoveLabel
	flagEntRemoveLabel = []string{"red"}
	defer func() { flagEntRemoveLabel = oldRemove }()

	var buf bytes.Buffer
	if err := runEntSetLabel(context.Background(), &buf, "sensor.a", nil); err != nil {
		t.Fatalf("runEntSetLabel --remove dry-run failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("output missing dry-run marker: %q", out)
	}
	if !strings.Contains(out, "removed_labels") {
		t.Errorf("dry-run plan does not name what --remove would take off: %q", out)
	}
	if got := ts.commandCount("config/entity_registry/update"); got != 0 {
		t.Fatalf("dry-run sent %d entity registry updates, want 0", got)
	}
}

// TestRunEntSetLabel_AddAndRemoveSameLabelIsRefused is H-25's exclusivity
// clause one flag over: a label named on both sides — even under different
// spellings, "energy" the name and "energy" the ID both resolve to the same
// registry entry — says two things about itself in one call, so the command
// ends before it plans anything (D-44).
func TestRunEntSetLabel_AddAndRemoveSameLabelIsRefused(t *testing.T) {
	ts := entLabelUnmakeStub(t)
	withFlagDir(t, ts.dir)
	oldRemove := flagEntRemoveLabel
	flagEntRemoveLabel = []string{"Energy"}
	defer func() { flagEntRemoveLabel = oldRemove }()

	var buf bytes.Buffer
	err := runEntSetLabel(context.Background(), &buf, "sensor.a", []string{"energy"})
	if err == nil || !errors.Is(err, errFlagContract) {
		t.Fatalf("err = %v, want a flag-contract refusal for naming the same label to add and remove", err)
	}
	if buf.Len() > 0 {
		t.Errorf("refusal printed a plan first: %q", buf.String())
	}
	if got := ts.commandCount("config/entity_registry/update"); got != 0 {
		t.Fatalf("conflict sent %d entity registry updates, want 0", got)
	}
}

// TestRunEntSetLabel_NeitherAddNorRemoveIsRefused closes the arity gap the
// loosened Args contract (takesAtLeast(1), to allow a remove-only call) opens:
// a call naming no labels at all — nothing to add, nothing to remove — used to
// be impossible because Args required two positionals; it is refused here
// instead, before any connection is made.
func TestRunEntSetLabel_NeitherAddNorRemoveIsRefused(t *testing.T) {
	oldRemove := flagEntRemoveLabel
	flagEntRemoveLabel = nil
	defer func() { flagEntRemoveLabel = oldRemove }()

	var buf bytes.Buffer
	err := runEntSetLabel(context.Background(), &buf, "sensor.a", nil)
	if err == nil || !strings.Contains(err.Error(), "no labels given") {
		t.Fatalf("err = %v, want a refusal naming that no labels were given", err)
	}
	if buf.Len() > 0 {
		t.Errorf("refusal printed a plan first: %q", buf.String())
	}
}

// entAreaUnmakeStub wires two entities in one area, so a clear test can assert
// the same partial-removal property `set-area --clear` has to have: clearing
// sensor.a's area must not touch sensor.b's.
func entAreaUnmakeStub(t *testing.T) *cmdTestServer {
	t.Helper()
	return startCmdServer(t, map[string]any{
		"config/area_registry/list": []any{
			map[string]any{"area_id": "kitchen_id", "name": "Kitchen"},
		},
		"config/entity_registry/list": []any{
			map[string]any{"entity_id": "light.a", "area_id": "kitchen_id"},
			map[string]any{"entity_id": "light.b", "area_id": "kitchen_id"},
		},
		"config/entity_registry/update": map[string]any{"entity_id": "light.a"},
	}, nil)
}

// TestRunEntSetArea_ClearRemovesTheArea is the unmake surface's proof for
// `hactl ent set-area` (dev/surfaces/unmake.manifest, H-27): --clear sends
// area_id: nil for the one entity named, and light.b — sharing the same area —
// is never touched. That is the property `area delete` cannot offer: it would
// clear the area from both.
func TestRunEntSetArea_ClearRemovesTheArea(t *testing.T) {
	ts := entAreaUnmakeStub(t)
	withFlagDir(t, ts.dir)
	oldConfirm := flagEntConfirm
	flagEntConfirm = true
	defer func() { flagEntConfirm = oldConfirm }()
	oldClear := flagEntAreaClear
	flagEntAreaClear = true
	defer func() { flagEntAreaClear = oldClear }()

	var buf bytes.Buffer
	if err := runEntSetArea(context.Background(), &buf, "light.a", ""); err != nil {
		t.Fatalf("runEntSetArea --clear failed: %v", err)
	}

	params := ts.lastParams("config/entity_registry/update")
	if params["entity_id"] != "light.a" {
		t.Fatalf("update targeted %v, want light.a only", params["entity_id"])
	}
	// A cleared area_id must be present and null, not merely absent: an absent
	// key would be indistinguishable from a no-op update on the wire.
	areaID, present := params["area_id"]
	if !present {
		t.Fatal("wire payload carries no area_id key at all — --clear must send one, even if null")
	}
	if areaID != nil {
		t.Fatalf("wire area_id = %v, want nil (JSON null)", areaID)
	}
	if got := ts.commandCount("config/entity_registry/update"); got != 1 {
		t.Fatalf("entity_registry/update sent %d times, want exactly 1 — light.b must be untouched", got)
	}
	if !strings.Contains(buf.String(), "cleared") {
		t.Errorf("output does not say the area was cleared: %q", buf.String())
	}
}

// TestRunEntSetArea_ClearDryRunPreviewsWithoutWriting mirrors H-2 for --clear.
func TestRunEntSetArea_ClearDryRunPreviewsWithoutWriting(t *testing.T) {
	ts := entAreaUnmakeStub(t)
	withFlagDir(t, ts.dir)
	oldConfirm := flagEntConfirm
	flagEntConfirm = false
	defer func() { flagEntConfirm = oldConfirm }()
	oldClear := flagEntAreaClear
	flagEntAreaClear = true
	defer func() { flagEntAreaClear = oldClear }()

	var buf bytes.Buffer
	if err := runEntSetArea(context.Background(), &buf, "light.a", ""); err != nil {
		t.Fatalf("runEntSetArea --clear dry-run failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "dry-run") || !strings.Contains(out, "clear") {
		t.Errorf("output missing dry-run/clear marker: %q", out)
	}
	if got := ts.commandCount("config/entity_registry/update"); got != 0 {
		t.Fatalf("dry-run sent %d entity registry updates, want 0", got)
	}
}

// TestRunEntSetArea_AreaAndClearIsRefused is H-25's exclusivity clause: the
// <area> positional and --clear each say what the area should become, and
// naming both ends the command rather than picking a winner.
func TestRunEntSetArea_AreaAndClearIsRefused(t *testing.T) {
	ts := entAreaUnmakeStub(t)
	withFlagDir(t, ts.dir)
	oldClear := flagEntAreaClear
	flagEntAreaClear = true
	defer func() { flagEntAreaClear = oldClear }()

	var buf bytes.Buffer
	err := runEntSetArea(context.Background(), &buf, "light.a", "Kitchen")
	if err == nil || !errors.Is(err, errFlagContract) {
		t.Fatalf("err = %v, want a flag-contract refusal for naming both <area> and --clear", err)
	}
	if buf.Len() > 0 {
		t.Errorf("refusal printed a plan first: %q", buf.String())
	}
	if got := ts.commandCount("config/entity_registry/update"); got != 0 {
		t.Fatalf("conflict sent %d entity registry updates, want 0", got)
	}
}

// TestRunEntSetArea_NeitherAreaNorClearIsRefused closes the arity gap the
// loosened Args contract (takesBetween(1, 2), to allow --clear with only the
// entity named) opens.
func TestRunEntSetArea_NeitherAreaNorClearIsRefused(t *testing.T) {
	oldClear := flagEntAreaClear
	flagEntAreaClear = false
	defer func() { flagEntAreaClear = oldClear }()

	var buf bytes.Buffer
	err := runEntSetArea(context.Background(), &buf, "light.a", "")
	if err == nil || !strings.Contains(err.Error(), "no area given") {
		t.Fatalf("err = %v, want a refusal naming that no area was given", err)
	}
	if buf.Len() > 0 {
		t.Errorf("refusal printed a plan first: %q", buf.String())
	}
}
