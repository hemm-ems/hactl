package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// deviceWriteStub wires a cmdTestServer with the registries the device write
// commands resolve against: one device, one area, one label.
func deviceWriteStub(t *testing.T) *cmdTestServer {
	t.Helper()
	return startCmdServer(t, map[string]any{
		"config/device_registry/list": []any{map[string]any{
			"id": "dev1", "name": "Wozi Tv", "area_id": "",
			"labels": []string{},
		}},
		"config/area_registry/list": []any{map[string]any{
			"area_id": "wohnzimmer", "name": "Wohnzimmer",
		}},
		"config/label_registry/list": []any{map[string]any{
			"label_id": "energy", "name": "Energy",
		}},
	}, nil)
}

// TestDeviceSetAreaDryRunRefusesUnresolvable — H-2: the preview fails exactly
// where --confirm would. An unknown device and an unknown area both end the
// command before any plan is printed (confirm.manifest row).
func TestDeviceSetAreaDryRunRefusesUnresolvable(t *testing.T) {
	ts := deviceWriteStub(t)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	err := runDeviceSetArea(context.Background(), &buf, "no_such_device", "wohnzimmer")
	if err == nil || !strings.Contains(err.Error(), `device "no_such_device" not found`) {
		t.Errorf("unknown device: err = %v, want a device-not-found refusal", err)
	}
	if buf.Len() > 0 {
		t.Errorf("refusal printed a plan first: %q", buf.String())
	}

	buf.Reset()
	err = runDeviceSetArea(context.Background(), &buf, "dev1", "no_such_area")
	if err == nil || !strings.Contains(err.Error(), `area "no_such_area" not found`) {
		t.Errorf("unknown area: err = %v, want an area-not-found refusal", err)
	}
	if buf.Len() > 0 {
		t.Errorf("refusal printed a plan first: %q", buf.String())
	}
}

// TestDeviceSetLabelDryRunRefusesUnresolvable — same H-2 shape for set-label:
// an unknown label or device refuses before the plan (confirm.manifest row).
func TestDeviceSetLabelDryRunRefusesUnresolvable(t *testing.T) {
	ts := deviceWriteStub(t)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	err := runDeviceSetLabel(context.Background(), &buf, "dev1", []string{"no_such_label"})
	if err == nil || !strings.Contains(err.Error(), `label "no_such_label" not found`) {
		t.Errorf("unknown label: err = %v, want a label-not-found refusal", err)
	}
	if buf.Len() > 0 {
		t.Errorf("refusal printed a plan first: %q", buf.String())
	}

	buf.Reset()
	err = runDeviceSetLabel(context.Background(), &buf, "no_such_device", []string{"Energy"})
	if err == nil || !strings.Contains(err.Error(), `device "no_such_device" not found`) {
		t.Errorf("unknown device: err = %v, want a device-not-found refusal", err)
	}
}

// TestDeviceSetAreaDryRunPlan — the preview resolves device (by NAME, H-17)
// and area (by name), and renders the shared dry-run shape.
func TestDeviceSetAreaDryRunPlan(t *testing.T) {
	ts := deviceWriteStub(t)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runDeviceSetArea(context.Background(), &buf, "Wozi Tv", "Wohnzimmer"); err != nil {
		t.Fatalf("runDeviceSetArea dry-run: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"dry-run: would set device area",
		"dev1",
		"Wohnzimmer (wohnzimmer)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run plan missing %q:\n%s", want, out)
		}
	}
}

// TestDeviceSetLabelDryRunPlan — labels resolve by name and merge into the
// device's existing set; the plan carries real arrays under --json (mirrors
// dryRunEntSetLabelSummary).
func TestDeviceSetLabelDryRunPlan(t *testing.T) {
	ts := deviceWriteStub(t)
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)

	var buf bytes.Buffer
	if err := runDeviceSetLabel(context.Background(), &buf, "dev1", []string{"Energy"}); err != nil {
		t.Fatalf("runDeviceSetLabel dry-run: %v", err)
	}
	obj, ok := assertValidJSON(t, buf.String()).(map[string]any)
	if !ok {
		t.Fatalf("dry-run JSON is not an object: %s", buf.String())
	}
	if obj["dry_run"] != true {
		t.Errorf("dry_run = %v, want true", obj["dry_run"])
	}
	details, _ := obj["details"].(map[string]any)
	newLabels, _ := details["new_labels"].([]any)
	if len(newLabels) != 1 || newLabels[0] != "energy" {
		t.Errorf("new_labels = %v, want [energy] (name resolved to id)", newLabels)
	}
}
