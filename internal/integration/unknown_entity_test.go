//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// aLiveEntityID returns an entity HA itself reports a state for, so the
// negative control is real rather than assumed.
func aLiveEntityID(t *testing.T) string {
	t.Helper()
	cfg := loadConfig(t)
	data, err := haapi.New(cfg.URL, cfg.Token).GetStates(context.Background())
	if err != nil {
		t.Fatalf("fetching states: %v", err)
	}
	var states []struct {
		EntityID string `json:"entity_id"`
	}
	if err := json.Unmarshal(data, &states); err != nil {
		t.Fatalf("parsing states: %v", err)
	}
	ids := make([]string, 0, len(states))
	for _, s := range states {
		ids = append(ids, s.EntityID)
	}
	if len(ids) == 0 {
		t.Fatal("HA reports no entities at all")
	}
	sort.Strings(ids)
	return ids[0]
}

// A nonexistent entity and a real-but-quiet one must not produce the same
// answer. `ent show` 404s on a typo; `ent hist`, `ent who` and `ent anomalies`
// exited 0 with an empty result, which under the manual's "stop at the first
// miss" rule reads as a verified negative — the tool confirming that nothing
// happened, when in fact it was never asked about a real entity.
//
// The distinguishing question is not "does HA hold a state for it" alone: an
// entity that was deleted can still have recorder history, and reporting that
// history is the point of the command. So the check runs only once the result
// is already empty — no live state AND nothing recorded means there is nothing
// to have been quiet about.
func TestUnknownEntityIsNotAQuietEntity(t *testing.T) {
	const ghost = "sensor.definitely_not_a_real_entity_xyz"

	// A real entity that is genuinely quiet — the negative control. It exists,
	// so an empty answer about it is a fact, and these commands must keep
	// exiting 0 for it.
	quiet := aLiveEntityID(t)

	for _, tc := range []struct {
		name string
		args []string
	}{
		{"hist", []string{"ent", "hist"}},
		{"who", []string{"ent", "who"}},
		{"anomalies", []string{"ent", "anomalies"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ghostOut, ghostErr := runHactlErr(t, append(append([]string{}, tc.args...), ghost)...)
			if ghostErr == nil {
				t.Errorf("hactl %s %s exited 0 for an entity that does not exist; "+
					"an agent following stop-at-the-first-miss reads this as a verified negative.\noutput:\n%s",
					strings.Join(tc.args, " "), ghost, ghostOut)
			}

			quietOut, quietErr := runHactlErr(t, append(append([]string{}, tc.args...), quiet)...)
			if quietErr != nil {
				t.Errorf("hactl %s %s failed for an entity that exists: %v\noutput:\n%s",
					strings.Join(tc.args, " "), quiet, quietErr, quietOut)
			}
		})
	}

	// `ent show` is the family member that always got this right; it is the
	// standard the others are held to.
	if _, err := runHactlErr(t, "ent", "show", ghost); err == nil {
		t.Error("ent show stopped rejecting an unknown entity — the standard the others follow moved")
	}
}
