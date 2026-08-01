package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// energyPrefsFixture is what a current instance answers energy/get_prefs with:
// every source type on the flat stat_energy_* fields, because HA's store
// migrates the flow_from/flow_to grid form away while loading it.
//
// It used to be one grid source in the flow form plus a solar source, above a
// comment claiming to mirror data.py. Both halves were wrong the same way:
// data.py calls that form legacy, and solar is the single type for which the
// old rule ("stat_energy_from means production") is true. The fixture could
// not fail, which is exactly why the real instance's grid import and gas meter
// both read as production for months (finding #26).
//
// The values are the shape of the reference instance's own prefs, captured
// 2026-07-31 — including the null-valued price keys, which are what HA's
// migration writes.
var energyPrefsFixture = map[string]any{
	"energy_sources": []any{
		map[string]any{
			"type":                "grid",
			"stat_energy_from":    "sensor.grid_in",
			"stat_energy_to":      "sensor.grid_out",
			"stat_cost":           nil,
			"number_energy_price": 0.36,
			"cost_adjustment_day": 0,
		},
		map[string]any{
			"type":             "solar",
			"stat_energy_from": "sensor.pv_yield",
		},
		map[string]any{
			"type":             "battery",
			"stat_energy_from": "sensor.battery_out",
			"stat_energy_to":   "sensor.battery_in",
		},
		map[string]any{
			"type":             "gas",
			"stat_energy_from": "sensor.gas_meter",
		},
		map[string]any{
			"type":             "water",
			"stat_energy_from": "sensor.water_meter",
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
	if len(sources) != 7 {
		t.Errorf("sources = %d rows, want 7 (grid in/out, solar, battery out/in, gas, water)", len(sources))
	}
}

// TestEnergyDirectionIsAPropertyOfTheSourceType is finding #26.
//
// `hactl energy show` on the reference instance printed
//
//	grid  production sensor.energie_gesamt_daheim_dayly
//	gas   production sensor.bad_therme_an_energy_2
//
// for a household grid meter and a gas boiler. The direction was read off the
// field name alone — anything on `stat_energy_from` was production — while
// eight lines above, the same function called the legacy grid's flow_from
// "consumption". Two halves of one rule, in one function, disagreeing.
//
// The case is a table over EVERY source type HA defines, and that is the
// point: the shipped rule was correct for solar, which is the one type the old
// fixture carried besides a grid source in a form no live instance answers
// with. A fixture covering two of five types cannot tell "direction follows
// the field" from "direction follows the type and the field".
func TestEnergyDirectionIsAPropertyOfTheSourceType(t *testing.T) {
	for _, tc := range []struct {
		sourceType, from, to string
	}{
		// Grid import is the household consuming; export is the return.
		{"grid", "consumption", "return"},
		// The only genuinely generative source.
		{"solar", "production", ""},
		// HA: "positive when discharging, negative when charging". A battery
		// does not produce energy, it gives back what it was given (D-16).
		{"battery", "discharge", "charge"},
		// "the source of gas consumption" / "of water consumption".
		{"gas", "consumption", ""},
		{"water", "consumption", ""},
	} {
		t.Run(tc.sourceType, func(t *testing.T) {
			// An empty cell above means "HA's schema gives this type no such
			// field", which renders the same way an unknown type does: the
			// tool has nothing to say about a statistic that cannot be there.
			for _, leg := range []struct {
				field string
				to    bool
				want  string
			}{
				{"stat_energy_from", false, orUnknown(tc.from)},
				{"stat_energy_to", true, orUnknown(tc.to)},
			} {
				if got := energyDirection(tc.sourceType, leg.to); got != leg.want {
					t.Errorf("%s %s = %q, want %q", tc.sourceType, leg.field, got, leg.want)
				}
			}
		})
	}
}

func orUnknown(direction string) string {
	if direction == "" {
		return energyDirectionUnknown
	}
	return direction
}

// TestEnergyUnknownSourceTypeIsNotGuessedAt — HA's SourceType union is closed
// today and TestOracleEnergySourceTypes fails the build when it gains a
// member, but a build running against a newer HA than its oracle must not
// invent a claim about a type it has never heard of. The statistic is still
// shown; the direction says it does not know (H-14's spirit).
func TestEnergyUnknownSourceTypeIsNotGuessedAt(t *testing.T) {
	prefs := map[string]any{
		"energy_sources": []any{
			map[string]any{"type": "heat", "stat_energy_from": "sensor.district_heating"},
		},
		"device_consumption": []any{},
	}
	ts := startCmdServer(t, map[string]any{"energy/get_prefs": prefs}, nil)
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)

	var buf bytes.Buffer
	if err := runEnergyShow(context.Background(), &buf); err != nil {
		t.Fatalf("runEnergyShow: %v", err)
	}
	obj, _ := assertValidJSON(t, buf.String()).(map[string]any)
	sources, _ := obj["sources"].([]any)
	if len(sources) != 1 {
		t.Fatalf("sources = %v, want the unknown source to still be listed", sources)
	}
	row, _ := sources[0].(map[string]any)
	if row["direction"] != energyDirectionUnknown {
		t.Errorf("direction = %v for an unknown source type, want %q — a guess here is the "+
			"defect this fix removes, one type later", row["direction"], energyDirectionUnknown)
	}
	if row["statistic"] != "sensor.district_heating" {
		t.Errorf("statistic = %v; an unclassifiable source is still worth showing", row["statistic"])
	}
}

// TestEnergyLegacyGridFormReadsTheSameTable — an instance old enough not to
// have run HA's prefs migration still answers with flow_from/flow_to. Those
// arms route through energyDirectionByType like everything else, so the two
// forms of one grid source cannot disagree about what a grid import is; that
// disagreement, inside a single function, was finding #26.
func TestEnergyLegacyGridFormReadsTheSameTable(t *testing.T) {
	legacy := map[string]any{
		"energy_sources": []any{
			map[string]any{
				"type":      "grid",
				"flow_from": []any{map[string]any{"stat_energy_from": "sensor.grid_in"}},
				"flow_to":   []any{map[string]any{"stat_energy_to": "sensor.grid_out"}},
			},
		},
		"device_consumption": []any{},
	}
	ts := startCmdServer(t, map[string]any{"energy/get_prefs": legacy}, nil)
	withFlagDir(t, ts.dir)
	withFlagJSON(t, true)

	var buf bytes.Buffer
	if err := runEnergyShow(context.Background(), &buf); err != nil {
		t.Fatalf("runEnergyShow: %v", err)
	}
	obj, _ := assertValidJSON(t, buf.String()).(map[string]any)
	sources, _ := obj["sources"].([]any)
	if len(sources) != 2 {
		t.Fatalf("sources = %v, want the legacy grid's two flows", sources)
	}
	got := map[string]string{}
	for _, s := range sources {
		row, _ := s.(map[string]any)
		stat, _ := row["statistic"].(string)
		dir, _ := row["direction"].(string)
		got[stat] = dir
	}
	if got["sensor.grid_in"] != "consumption" || got["sensor.grid_out"] != "return" {
		t.Errorf("legacy grid directions = %v, want the same answers the flat form gives", got)
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
