package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// energyPrefsFixture mirrors HA's energy/get_prefs shape (data.py): a grid
// source with both flow directions, a solar source, and one tracked device.
var energyPrefsFixture = map[string]any{
	"energy_sources": []any{
		map[string]any{
			"type":      "grid",
			"flow_from": []any{map[string]any{"stat_energy_from": "sensor.grid_in"}},
			"flow_to":   []any{map[string]any{"stat_energy_to": "sensor.grid_out"}},
		},
		map[string]any{
			"type":             "solar",
			"stat_energy_from": "sensor.pv_yield",
		},
	},
	"device_consumption": []any{
		map[string]any{"stat_consumption": "sensor.dishwasher_energy", "name": "Dishwasher"},
	},
}

// TestRunEnergyShow_RendersSourcesAndDevices — every statistic feeding the
// dashboard appears with its type and direction, in both renders (H-10). This
// is the field case behind issue #114: the solar source's statistic is
// visible without leaving hactl for raw WS.
func TestRunEnergyShow_RendersSourcesAndDevices(t *testing.T) {
	ts := startCmdServer(t, map[string]any{"energy/get_prefs": energyPrefsFixture}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runEnergyShow(context.Background(), &buf); err != nil {
		t.Fatalf("runEnergyShow: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"grid", "consumption", "sensor.grid_in",
		"return", "sensor.grid_out",
		"solar", "production", "sensor.pv_yield",
		"sensor.dishwasher_energy", "Dishwasher",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("text output missing %q:\n%s", want, out)
		}
	}

	buf.Reset()
	withFlagJSON(t, true)
	if err := runEnergyShow(context.Background(), &buf); err != nil {
		t.Fatalf("runEnergyShow --json: %v", err)
	}
	obj, ok := assertValidJSON(t, buf.String()).(map[string]any)
	if !ok {
		t.Fatalf("JSON output is not an object: %s", buf.String())
	}
	if obj["configured"] != true {
		t.Errorf("configured = %v, want true", obj["configured"])
	}
	sources, _ := obj["sources"].([]any)
	if len(sources) != 3 {
		t.Errorf("sources = %v, want 3 rows (grid in, grid out, solar)", sources)
	}
}

// TestRunEnergyShow_Unconfigured — HA answers "No prefs" for an instance
// whose Energy dashboard was never set up (oracle-probed, see
// TestOracleEnergyGetPrefsUnconfigured); the command renders that as an
// honest negative, never as an error and never as an empty success. --json
// distinguishes by field ({"configured": false}), not emptiness (H-7).
func TestRunEnergyShow_Unconfigured(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"energy/get_prefs": wsErrorResponse{Code: "not_found", Message: "No prefs"},
	}, nil)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runEnergyShow(context.Background(), &buf); err != nil {
		t.Fatalf("unconfigured must be an answer, not an error: %v", err)
	}
	if !strings.Contains(buf.String(), "no energy configuration") {
		t.Errorf("text output does not state the honest negative: %q", buf.String())
	}

	buf.Reset()
	withFlagJSON(t, true)
	if err := runEnergyShow(context.Background(), &buf); err != nil {
		t.Fatalf("runEnergyShow --json: %v", err)
	}
	obj, ok := assertValidJSON(t, buf.String()).(map[string]any)
	if !ok {
		t.Fatalf("JSON output is not an object: %s", buf.String())
	}
	if obj["configured"] != false {
		t.Errorf(`JSON = %v, want {"configured": false}`, obj)
	}
}
