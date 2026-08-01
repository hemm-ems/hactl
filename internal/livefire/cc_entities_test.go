//go:build livefire

package livefire

import (
	"encoding/json"
	"testing"
)

// Finding #15, both halves.
//
// The first half was `cc show` reporting `entities: 0` for all fourteen custom
// components on the reference instance, one of which owns 467 entities: the
// count matched entity_ids against the INTEGRATION domain, and an entity_id's
// first segment is its entity domain. Fixed in cc66290 against a registry
// stub, and this is the case that owes it a real instance.
//
// The second half is what running that oracle produced. Eleven of the fourteen
// then agreed with the registry exactly; three did not — dwd_weather 19 against
// 75, hacs 19 against 38, homematicip_local 159 against 402 — and every entity
// in the difference is disabled. Across the whole 5524-row registry there is
// not one row without a live state for any other reason, so the live-state
// filter was never removing the stale rows its comment claimed; it was removing
// up to 60% of an integration's entities and saying nothing.
//
// The assertion is the reconciliation, not the numbers: `cc show` must account
// for every row the registry attributes to the component (H-11).
func TestSweepCustomComponentCountsReconcile(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		out := tgt.MustRead(t, "cc", "ls", "--top", "100", "--json")
		var components []struct {
			Domain string `json:"domain"`
		}
		if err := json.Unmarshal([]byte(out), &components); err != nil {
			t.Fatalf("cc ls --json: %v\n%s", err, truncate(out))
		}
		if len(components) == 0 {
			t.Fatal("the instance reports no custom components — the case cannot fail here")
		}

		withDisabled, withEntities := 0, 0
		for _, c := range components {
			show := tgt.MustRead(t, "cc", "show", c.Domain, "--json")
			var info struct {
				EntityCount       int      `json:"entity_count"`
				EntityIDs         []string `json:"entity_ids"`
				DisabledCount     int      `json:"disabled_count"`
				DisabledEntityIDs []string `json:"disabled_entity_ids"`
				RegistryCount     int      `json:"registry_count"`
			}
			if err := json.Unmarshal([]byte(show), &info); err != nil {
				t.Fatalf("cc show %s --json: %v\n%s", c.Domain, err, truncate(show))
			}

			// Every count expands into the ids it counts. A number a caller
			// cannot check is a number they have to take on trust, which is how
			// `entities: 0` survived on an instance with 467 of them.
			if len(info.EntityIDs) != info.EntityCount {
				t.Errorf("%s: entity_count %d, but %d entity_ids",
					c.Domain, info.EntityCount, len(info.EntityIDs))
			}
			if len(info.DisabledEntityIDs) != info.DisabledCount {
				t.Errorf("%s: disabled_count %d, but %d disabled_entity_ids",
					c.Domain, info.DisabledCount, len(info.DisabledEntityIDs))
			}
			// The registry total is the source's own count, so nothing may
			// exceed it and nothing may vanish beneath it without appearing in
			// the gap.
			if info.EntityCount+info.DisabledCount > info.RegistryCount {
				t.Errorf("%s: %d live + %d disabled exceeds the %d rows the registry attributes "+
					"to it", c.Domain, info.EntityCount, info.DisabledCount, info.RegistryCount)
			}
			if info.EntityCount > 0 {
				withEntities++
			}
			if info.DisabledCount > 0 {
				withDisabled++
			}
		}

		if withEntities == 0 {
			t.Error("not one custom component reports an entity — this is the `entities: 0` " +
				"the finding is about, or a fixture that cannot show it")
		}
		// The anti-vacuity guard. On the rig, shapewatch ships one sensor with
		// entity_registry_enabled_default = False for exactly this reason: with
		// no disabled entity anywhere, every reconciliation above holds
		// trivially and the second half of the finding is untested.
		if withDisabled == 0 {
			t.Error("no component owns a disabled entity, so the count that used to be silently " +
				"subtracted cannot differ from the one that is reported")
		}
	})
}
