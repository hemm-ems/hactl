//go:build livefire

package livefire

import (
	"encoding/json"
	"testing"
)

// Finding #26: `energy show` called grid import and gas consumption
// "production".
//
//	grid  production sensor.energie_gesamt_daheim_dayly
//	gas   production sensor.bad_therme_an_energy_2
//
// A household grid meter and a gas boiler, both reported as generating energy.
// The direction was read off the field name — anything on `stat_energy_from`
// was production — while the same function called the legacy grid form's
// flow_from "consumption" eight lines above. Direction is a property of the
// source TYPE and the field, and one of the two halves of that rule had it.
//
// The case asserts the rule rather than the symptom: for every row the
// instance serves, the direction has to be the one its source type gives that
// field. Pinning the two statistic ids from the report instead would pass the
// day someone relabels gas and leaves water wrong.
func TestSweepEnergyDirectionFollowsTheSourceType(t *testing.T) {
	// The same table internal/cmd/energy.go holds, written out again on
	// purpose: a gate that imports its expectation from the code under test
	// asserts that the code equals itself. TestOracleEnergySourceTypes
	// (make test-int) is what keeps both honest against HA's own data.py.
	want := map[string][2]string{
		"grid":    {"consumption", "return"},
		"solar":   {"production", "unknown"},
		"battery": {"discharge", "charge"},
		"gas":     {"consumption", "unknown"},
		"water":   {"consumption", "unknown"},
	}

	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		out := tgt.MustRead(t, "energy", "show", "--json")
		var prefs struct {
			Configured bool `json:"configured"`
			Sources    []struct {
				Type      string `json:"type"`
				Direction string `json:"direction"`
				Statistic string `json:"statistic"`
			} `json:"sources"`
		}
		if err := json.Unmarshal([]byte(out), &prefs); err != nil {
			t.Fatalf("energy show --json: %v\n%s", err, truncate(out))
		}
		if !prefs.Configured || len(prefs.Sources) == 0 {
			t.Fatal("the instance serves no energy sources — the case cannot fail here, " +
				"which is not the same as passing")
		}

		for _, row := range prefs.Sources {
			directions, known := want[row.Type]
			if !known {
				t.Errorf("source type %q is not in the direction table; TestOracleEnergySourceTypes "+
					"should have caught that before a user did", row.Type)
				continue
			}
			// A statistic reached the output on one of the two fields, and the
			// direction is the only evidence of which. Both legs are checked
			// because "never production" would pass with everything labelled
			// "return".
			if row.Direction != directions[0] && row.Direction != directions[1] {
				t.Errorf("%s %s %s — a %s statistic reads %q or %q, never this",
					row.Type, row.Direction, row.Statistic, row.Type, directions[0], directions[1])
			}
			if row.Direction == "production" && row.Type != "solar" {
				t.Errorf("%s %s is called production; only a generative source produces "+
					"(finding #26)", row.Type, row.Statistic)
			}
		}
	})
}
