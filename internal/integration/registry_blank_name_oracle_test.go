//go:build integration

package integration

import (
	"encoding/json"
	"maps"
	"strings"
	"testing"
)

// ============================================================================
// Oracle for the blank registry name (the P1 behind `area create "" --confirm`).
//
// A live-fire run against a real instance created an area whose `area_id` was
// the empty string, and from that moment EVERY `hactl area` command failed —
// `ls`, `create` and `delete` alike — because H-14 fails the whole listing when
// one record arrives without its identity, and `delete` has to list first. The
// family could only be recovered by a raw WebSocket delete, outside hactl.
//
// Two facts decide the code, and neither may be assumed:
//
//  1. Does HA itself refuse the name? If it does, hactl needs no gate — the
//     server is the gate. If it does not, the refusal has to be client-side,
//     because by the time hactl can see the answer the damage is already
//     persisted in HA's registry.
//  2. What identity does HA mint? A blank name with a generated non-blank id
//     would be ugly but survivable; a blank id is what takes the family down.
//
// Probed 2026-07-30 against ghcr.io/home-assistant/home-assistant:stable, for
// `config/{area,floor,label}_registry/create`. All three answer identically:
//
//	name ""     → success, area_id/floor_id/label_id ""        ← the outage
//	name "   "  → success, …_id "unknown", name kept as "   "  ← an id HA chose
//	name "   "  → REFUSED (invalid_info, "already in use") while a record named
//	              "" exists — HA's uniqueness check normalises whitespace away,
//	              so "" and "   " are one name to it
//
// Neither form is refused on its own merits, and the two failure modes differ:
// an empty name mints an identity-less record that poisons every listing of
// that registry (H-14), while a whitespace-only name is filed under an
// identifier the caller never asked for and could not have predicted — hactl's
// own H-11 pole ("never invent an identifier") pointing the other way, at HA.
// The third fact is why hactl cannot treat the two as different inputs either:
// HA already considers them the same name. All are refused client-side, before
// the wire call, because after it there is nothing left to refuse.
//
// This test deliberately does NOT run hactl: the runHactl* helpers fail any
// command whose output or error carries the degeneracy marker (H-14's scan),
// which is exactly what `area ls` does while the blank record exists. The wire
// is the oracle here; what hactl does with such a payload is pinned by the unit
// tier (TestRegistryListBlankIdentityErrorPointsAtRecovery).
// ============================================================================

// blankNameRegistry is one registry's create/delete/list command triple.
type blankNameRegistry struct {
	kind    string
	create  string
	delete  string
	list    string
	idField string
}

var blankNameRegistries = []blankNameRegistry{
	{"area", "config/area_registry/create", "config/area_registry/delete", "config/area_registry/list", "area_id"},
	{"floor", "config/floor_registry/create", "config/floor_registry/delete", "config/floor_registry/list", "floor_id"},
	{"label", "config/label_registry/create", "config/label_registry/delete", "config/label_registry/list", "label_id"},
}

// haMintedIDForABlankName is the identifier HA falls back to when a name
// slugifies to nothing but is not itself empty. It is not hactl's to choose,
// and the point of pinning it is that the caller cannot predict it either.
const haMintedIDForABlankName = "unknown"

// TestOracleRegistryCreateAcceptsABlankName asks HA what it does with a name
// that is empty, and with one that is only whitespace. Each case runs in its
// own subtest so the record is gone before the next one asks: HA's uniqueness
// check treats the two names as one (the third subtest pins that), so probing
// them in a shared session would measure the collision instead of the create.
func TestOracleRegistryCreateAcceptsABlankName(t *testing.T) {
	for _, reg := range blankNameRegistries {
		t.Run(reg.kind, func(t *testing.T) {
			t.Run("empty_name_mints_no_identity", func(t *testing.T) {
				ws := dialRawWS(t, ha.URL(), ha.Token())
				id := createNamed(t, ws, reg, "")
				if id != "" {
					t.Errorf("%s minted %s=%q for an empty name — HA no longer produces the "+
						"identity-less record the client-side gate exists to prevent; re-read the "+
						"gate's justification before relaxing it", reg.create, reg.idField, id)
				}
				// The record really is in the registry, not merely echoed back:
				// this listing is the payload every hactl command of this family
				// must decode before it can answer anything at all.
				if got := blankIdentityCount(t, ws, reg); got != 1 {
					t.Errorf("%s shows %d records with a blank %s, want exactly the one just created",
						reg.list, got, reg.idField)
				}
			})

			t.Run("whitespace_name_gets_an_id_ha_chose", func(t *testing.T) {
				ws := dialRawWS(t, ha.URL(), ha.Token())
				id := createNamed(t, ws, reg, "   ")
				if id != haMintedIDForABlankName {
					t.Errorf("%s minted %s=%q for a whitespace-only name, want %q — HA's fallback "+
						"changed, so the second half of the gate's justification (an identifier the "+
						"caller cannot predict) needs re-reading",
						reg.create, reg.idField, id, haMintedIDForABlankName)
				}
				if got := blankIdentityCount(t, ws, reg); got != 0 {
					t.Errorf("a whitespace-only name left %d identity-less %s records behind — it is "+
						"supposed to take HA's fallback id, not a blank one", got, reg.kind)
				}
			})

			t.Run("empty_and_whitespace_are_one_name_to_ha", func(t *testing.T) {
				ws := dialRawWS(t, ha.URL(), ha.Token())
				createNamed(t, ws, reg, "")
				env := ws.send(t, map[string]any{"type": reg.create, "name": "   "})
				if env.Success {
					t.Fatalf("%s accepted a whitespace-only name beside an empty-named record — HA's "+
						"uniqueness check no longer normalises whitespace, so the two really are "+
						"different inputs now (%s)", reg.create, env.Result)
				}
				if code := env.errCode(t); code != "invalid_info" {
					t.Errorf("%s refused the collision with code %q, want %q (%s)",
						reg.create, code, "invalid_info", env.Error)
				}
				if !strings.Contains(string(env.Error), "already in use") {
					t.Errorf("%s refused for some reason other than a name collision: %s",
						reg.create, env.Error)
				}
			})
		})
	}
}

// createNamed creates one registry record over the raw wire and returns the
// identifier HA minted for it. The delete is registered before anything is
// asserted: a failing assertion must not leave the record behind, or every
// later test in this package inherits the outage.
func createNamed(t *testing.T, ws *rawWS, reg blankNameRegistry, name string) string {
	t.Helper()
	env := ws.send(t, map[string]any{"type": reg.create, "name": name})
	if !env.Success {
		t.Fatalf("ORACLE RESULT: HA REFUSED %s with name %q (%s) — the server is the gate for this "+
			"registry now, and hactl's client-side refusal became belt-and-braces; record that "+
			"and revisit", reg.create, name, env.Error)
	}
	var created map[string]any
	if err := json.Unmarshal(env.Result, &created); err != nil {
		t.Fatalf("parsing the created %s: %v (%s)", reg.kind, err, env.Result)
	}
	id, _ := created[reg.idField].(string)
	t.Cleanup(func() {
		cleanup := dialRawWS(t, ha.URL(), ha.Token())
		cleanup.mustSucceed(t, map[string]any{"type": reg.delete, reg.idField: id})
	})
	t.Logf("ORACLE: %s name=%q → %s", reg.create, name, env.Result)
	return id
}

// TestOracleFloorLevelZeroIsStored — the wire fact behind `floor create
// --level 0` silently dropping the level. hactl elided the level whenever it
// was 0 (`if flagFloorLevel != 0`), so HA never saw it and the floor came back
// with `level: null` — while the manual's own canonical example is
// `--level 0` for a ground floor.
//
// The fix (send the level whenever the flag was given) is only worth making if
// HA distinguishes 0 from absent, so ask it: an omitted level, an explicit 0,
// and a negative level (a basement — the other value a naive `> 0` guard would
// swallow) must come back as three different answers.
//
// Probed 2026-07-30 against ghcr.io/home-assistant/home-assistant:stable.
func TestOracleFloorLevelZeroIsStored(t *testing.T) {
	const create = "config/floor_registry/create"
	cases := []struct {
		want   any
		name   string
		params map[string]any
	}{
		{nil, "oracle-level-absent", map[string]any{}},
		{float64(0), "oracle-level-zero", map[string]any{"level": 0}},
		{float64(-1), "oracle-level-negative", map[string]any{"level": -1}},
	}
	ws := dialRawWS(t, ha.URL(), ha.Token())
	for _, tc := range cases {
		msg := map[string]any{"type": create, "name": tc.name}
		maps.Copy(msg, tc.params)
		env := ws.mustSucceed(t, msg)
		var created map[string]any
		if err := json.Unmarshal(env.Result, &created); err != nil {
			t.Fatalf("parsing the created floor: %v (%s)", err, env.Result)
		}
		floorID, _ := created["floor_id"].(string)
		t.Cleanup(func() {
			cleanup := dialRawWS(t, ha.URL(), ha.Token())
			cleanup.mustSucceed(t, map[string]any{"type": "config/floor_registry/delete", "floor_id": floorID})
		})
		t.Logf("ORACLE: %s %v → %s", create, tc.params, env.Result)
		if got := created["level"]; got != tc.want {
			t.Errorf("%s with %v stored level %#v, want %#v — HA no longer distinguishes an "+
				"explicit level from an absent one, and the Changed()-based fix in floor.go "+
				"rests on it doing so", create, tc.params, got, tc.want)
		}
	}
}

// blankIdentityCount reports how many records in the registry carry no identity.
func blankIdentityCount(t *testing.T, ws *rawWS, reg blankNameRegistry) int {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{"type": reg.list})
	var entries []map[string]any
	if err := json.Unmarshal(env.Result, &entries); err != nil {
		t.Fatalf("parsing %s: %v", reg.list, err)
	}
	blank := 0
	for _, e := range entries {
		if s, _ := e[reg.idField].(string); s == "" {
			blank++
		}
	}
	return blank
}
