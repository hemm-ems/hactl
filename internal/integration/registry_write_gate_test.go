//go:build integration

package integration

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// TestRegistryCreateBlankNameChangesNothingInHA — the gate proven where it
// matters: against a real registry, read back from HA rather than from hactl's
// own output (H-12).
//
// The unit tier proves no request leaves the client. This proves the
// consequence: the registry HA holds is the one it held before, so the family
// stays usable. `<family> ls` immediately afterwards is part of the assertion —
// it is the command that broke in the field, and the runHactl helper fails it
// on the degeneracy marker, so a blank record slipping through would surface
// here as a failing listing even if the identifier set matched.
func TestRegistryCreateBlankNameChangesNothingInHA(t *testing.T) {
	for _, reg := range blankNameRegistries {
		t.Run(reg.kind, func(t *testing.T) {
			ws := dialRawWS(t, ha.URL(), ha.Token())
			before := registryIDs(t, ws, reg)

			for _, name := range []string{"", "   "} {
				out, err := runHactlErr(t, reg.kind, "create", name, "--confirm")
				if err == nil {
					t.Fatalf("%s create %q --confirm was accepted against a real HA:\n%s", reg.kind, name, out)
				}
				if !strings.Contains(err.Error(), "blank") {
					t.Errorf("%s create %q failed for some other reason: %v", reg.kind, name, err)
				}
			}

			if after := registryIDs(t, ws, reg); after != before {
				t.Errorf("the %s registry changed: %q → %q", reg.kind, before, after)
			}
			// The listing that broke in the field still answers.
			runHactl(t, reg.kind, "ls")
		})
	}
}

// TestFloorCreateLevelZeroReachesHA — H-12 for the level fix: the floor HA
// holds carries level 0, read back over the wire with hactl nowhere in the
// path. `created floor …` is printed unconditionally once the call returns
// nil, so asserting on it would hold whether or not the level arrived.
func TestFloorCreateLevelZeroReachesHA(t *testing.T) {
	const floorID = "integ_level_zero_floor"
	ws := dialRawWS(t, ha.URL(), ha.Token())

	out := runHactl(t, "floor", "create", "integ level zero floor", "--level", "0", "--confirm")
	t.Cleanup(func() {
		cleanup := dialRawWS(t, ha.URL(), ha.Token())
		cleanup.mustSucceed(t, map[string]any{"type": "config/floor_registry/delete", "floor_id": floorID})
	})
	if !strings.Contains(out, "created floor") {
		t.Fatalf("floor create did not report a creation:\n%s", out)
	}

	env := ws.mustSucceed(t, map[string]any{"type": "config/floor_registry/list"})
	var floors []map[string]any
	if err := json.Unmarshal(env.Result, &floors); err != nil {
		t.Fatalf("parsing the floor registry: %v", err)
	}
	var found bool
	for _, f := range floors {
		if id, _ := f["floor_id"].(string); id != floorID {
			continue
		}
		found = true
		if f["level"] != float64(0) {
			t.Errorf("HA stored level %#v for a floor created with --level 0 — the flag is still "+
				"being dropped somewhere between the CLI and the wire", f["level"])
		}
	}
	if !found {
		t.Fatalf("floor %s is not in HA's registry after a confirmed create", floorID)
	}
}

// registryIDs renders a registry's identifiers as one canonical string, so a
// before/after comparison is a single readable assertion. Sorted, because the
// wire order is HA's and this test is about membership, not ordering.
func registryIDs(t *testing.T, ws *rawWS, reg blankNameRegistry) string {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{"type": reg.list})
	var entries []map[string]any
	if err := json.Unmarshal(env.Result, &entries); err != nil {
		t.Fatalf("parsing %s: %v", reg.list, err)
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		id, _ := e[reg.idField].(string)
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return strings.Join(ids, ",")
}
