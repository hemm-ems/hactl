package cmd

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// deviceLabelUnmakeStub mirrors entLabelUnmakeStub one registry over: two
// devices sharing one label, so a removal test can assert the same partial
// nature `device set-label --remove` needs (finding #81, H-27).
func deviceLabelUnmakeStub(t *testing.T) *cmdTestServer {
	t.Helper()
	return startCmdServer(t, map[string]any{
		"config/device_registry/list": []any{
			map[string]any{"id": "dev_a", "name": "Device A", "labels": []string{"energy", "red"}},
			map[string]any{"id": "dev_b", "name": "Device B", "labels": []string{"red"}},
		},
		"config/label_registry/list": []any{
			map[string]any{"label_id": "energy", "name": "Energy"},
			map[string]any{"label_id": "red", "name": "Red"},
		},
		"config/device_registry/update": map[string]any{"id": "dev_a"},
	}, nil)
}

// TestRunDeviceSetLabel_RemoveTakesOneLabelOff is the unmake surface's proof
// for `hactl device set-label` (dev/surfaces/unmake.manifest, H-27): --remove
// takes one label off dev_a alone, leaving dev_a's other label and dev_b's
// copy of the removed label untouched.
func TestRunDeviceSetLabel_RemoveTakesOneLabelOff(t *testing.T) {
	ts := deviceLabelUnmakeStub(t)
	withFlagDir(t, ts.dir)
	oldConfirm := flagDeviceConfirm
	flagDeviceConfirm = true
	defer func() { flagDeviceConfirm = oldConfirm }()
	oldRemove := flagDeviceRemoveLabel
	flagDeviceRemoveLabel = []string{"red"}
	defer func() { flagDeviceRemoveLabel = oldRemove }()

	var buf bytes.Buffer
	if err := runDeviceSetLabel(context.Background(), &buf, "dev_a", nil); err != nil {
		t.Fatalf("runDeviceSetLabel --remove red failed: %v", err)
	}

	params := ts.lastParams("config/device_registry/update")
	if params["device_id"] != "dev_a" {
		t.Fatalf("update targeted %v, want dev_a only", params["device_id"])
	}
	labels, _ := params["labels"].([]any)
	if len(labels) != 1 || labels[0] != "energy" {
		t.Fatalf("wire labels = %v, want [energy] — red should have come off, energy should stay (partial removal)",
			labels)
	}
	if got := ts.commandCount("config/device_registry/update"); got != 1 {
		t.Fatalf("device_registry/update sent %d times, want exactly 1 — dev_b must be untouched", got)
	}
}

// TestRunDeviceSetLabel_AddAndRemoveSameLabelIsRefused mirrors the ent-side
// exclusivity test one registry over.
func TestRunDeviceSetLabel_AddAndRemoveSameLabelIsRefused(t *testing.T) {
	ts := deviceLabelUnmakeStub(t)
	withFlagDir(t, ts.dir)
	oldRemove := flagDeviceRemoveLabel
	flagDeviceRemoveLabel = []string{"Energy"}
	defer func() { flagDeviceRemoveLabel = oldRemove }()

	var buf bytes.Buffer
	err := runDeviceSetLabel(context.Background(), &buf, "dev_a", []string{"energy"})
	if err == nil || !errors.Is(err, errFlagContract) {
		t.Fatalf("err = %v, want a flag-contract refusal for naming the same label to add and remove", err)
	}
	if got := ts.commandCount("config/device_registry/update"); got != 0 {
		t.Fatalf("conflict sent %d device registry updates, want 0", got)
	}
}

// TestRunDeviceSetLabel_NeitherAddNorRemoveIsRefused mirrors the ent-side
// arity test.
func TestRunDeviceSetLabel_NeitherAddNorRemoveIsRefused(t *testing.T) {
	oldRemove := flagDeviceRemoveLabel
	flagDeviceRemoveLabel = nil
	defer func() { flagDeviceRemoveLabel = oldRemove }()

	var buf bytes.Buffer
	err := runDeviceSetLabel(context.Background(), &buf, "dev_a", nil)
	if err == nil || !strings.Contains(err.Error(), "no labels given") {
		t.Fatalf("err = %v, want a refusal naming that no labels were given", err)
	}
}

// deviceAreaUnmakeStub mirrors entAreaUnmakeStub one registry over.
func deviceAreaUnmakeStub(t *testing.T) *cmdTestServer {
	t.Helper()
	return startCmdServer(t, map[string]any{
		"config/area_registry/list": []any{
			map[string]any{"area_id": "kitchen_id", "name": "Kitchen"},
		},
		"config/device_registry/list": []any{
			map[string]any{"id": "dev_a", "name": "Device A", "area_id": "kitchen_id"},
			map[string]any{"id": "dev_b", "name": "Device B", "area_id": "kitchen_id"},
		},
		"config/device_registry/update": map[string]any{"id": "dev_a"},
	}, nil)
}

// TestRunDeviceSetArea_ClearRemovesTheArea is the unmake surface's proof for
// `hactl device set-area` (dev/surfaces/unmake.manifest, H-27): --clear sends
// area_id: nil for dev_a alone; dev_b, sharing the same area, is untouched.
func TestRunDeviceSetArea_ClearRemovesTheArea(t *testing.T) {
	ts := deviceAreaUnmakeStub(t)
	withFlagDir(t, ts.dir)
	oldConfirm := flagDeviceConfirm
	flagDeviceConfirm = true
	defer func() { flagDeviceConfirm = oldConfirm }()
	oldClear := flagDeviceAreaClear
	flagDeviceAreaClear = true
	defer func() { flagDeviceAreaClear = oldClear }()

	var buf bytes.Buffer
	if err := runDeviceSetArea(context.Background(), &buf, "dev_a", ""); err != nil {
		t.Fatalf("runDeviceSetArea --clear failed: %v", err)
	}

	params := ts.lastParams("config/device_registry/update")
	if params["device_id"] != "dev_a" {
		t.Fatalf("update targeted %v, want dev_a only", params["device_id"])
	}
	areaID, present := params["area_id"]
	if !present {
		t.Fatal("wire payload carries no area_id key at all — --clear must send one, even if null")
	}
	if areaID != nil {
		t.Fatalf("wire area_id = %v, want nil (JSON null)", areaID)
	}
	if got := ts.commandCount("config/device_registry/update"); got != 1 {
		t.Fatalf("device_registry/update sent %d times, want exactly 1 — dev_b must be untouched", got)
	}
	if !strings.Contains(buf.String(), "cleared") {
		t.Errorf("output does not say the area was cleared: %q", buf.String())
	}
}

// TestRunDeviceSetArea_AreaAndClearIsRefused mirrors the ent-side exclusivity
// test.
func TestRunDeviceSetArea_AreaAndClearIsRefused(t *testing.T) {
	ts := deviceAreaUnmakeStub(t)
	withFlagDir(t, ts.dir)
	oldClear := flagDeviceAreaClear
	flagDeviceAreaClear = true
	defer func() { flagDeviceAreaClear = oldClear }()

	var buf bytes.Buffer
	err := runDeviceSetArea(context.Background(), &buf, "dev_a", "Kitchen")
	if err == nil || !errors.Is(err, errFlagContract) {
		t.Fatalf("err = %v, want a flag-contract refusal for naming both <area> and --clear", err)
	}
	if got := ts.commandCount("config/device_registry/update"); got != 0 {
		t.Fatalf("conflict sent %d device registry updates, want 0", got)
	}
}

// TestRunDeviceSetArea_NeitherAreaNorClearIsRefused mirrors the ent-side
// arity test.
func TestRunDeviceSetArea_NeitherAreaNorClearIsRefused(t *testing.T) {
	oldClear := flagDeviceAreaClear
	flagDeviceAreaClear = false
	defer func() { flagDeviceAreaClear = oldClear }()

	var buf bytes.Buffer
	err := runDeviceSetArea(context.Background(), &buf, "dev_a", "")
	if err == nil || !strings.Contains(err.Error(), "no area given") {
		t.Fatalf("err = %v, want a refusal naming that no area was given", err)
	}
}
