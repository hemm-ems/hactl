//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// Target expansion — the oracle behind SPEC-permissions.md, hard case E-1.
//
// A service call may name an area, floor, label, device or group instead of
// entity ids, and Home Assistant decides at call time which entities that
// reaches. A permission check ("may this caller control every entity the call
// reaches?") is only as right as that set, and hactl computes no such set
// today: serviceCallReach recognises only "no target" and `entity_id: all`.
//
// HA's own rules (helpers/target.py async_extract_referenced_entity_ids, read
// in 2026.7.3 through 2026.10.4) are not a one-liner — an entity-level area
// overrides its device's, hidden entities drop out of every indirect path but
// not a direct one, a label reaches devices and areas as well as entities —
// and a re-implementation would be expectations authored from hactl's model
// again. The WebSocket command `extract_from_target` runs the same function.
// These tests ask HA both questions — "which entities would you reach?" and
// "which entities did you act on?" — and require the answers to agree, so the
// permission layer can ask HA instead of re-deriving it.
//
// The service path calls the extractor with expand_group=True (the WS
// command's default is False) and then keeps only the entities registered for
// the service's domain that are available; the comparison applies exactly
// that restriction and nothing else.
// ============================================================================

// targetsRig is the registry graph buildTargetsRig places onto demo lights.
type targetsRig struct {
	FloorID, AreaOnFloor, AreaOffFloor, LabelID string
	GroupID                                     string

	DeviceInherit string // device placed in AreaOnFloor; its light has no area of its own
	LightInherit  string
	LightDirect   string // entity-level AreaOnFloor, device in no area
	DeviceOverride,
	LightOverride string // device in AreaOnFloor, entity overridden to AreaOffFloor
	LightHidden    string // entity-level AreaOnFloor, hidden_by user
	DeviceLabelled string // device carries LabelID; its light does not
	LightViaDevLbl string
	LightLabelled  string // entity carries LabelID directly
	CrossDomain    string // a non-light entity in AreaOnFloor
}

func TestOracleTargetExpansion(t *testing.T) {
	opts := []hatest.Option{hatest.WithFixture("targets")}
	if img := os.Getenv("HACTL_HA_IMAGE"); img != "" {
		opts = append(opts, hatest.WithImage(img))
	}
	inst := hatest.Start(t, opts...)
	waitForRunning(t, inst)
	rig := buildTargetsRig(t, inst)
	ws := dialRawWS(t, inst.URL(), inst.Token())

	targets := []struct {
		name   string
		target map[string]any
	}{
		{"area", map[string]any{"area_id": rig.AreaOnFloor}},
		{"area_override", map[string]any{"area_id": rig.AreaOffFloor}},
		{"floor", map[string]any{"floor_id": rig.FloorID}},
		{"label", map[string]any{"label_id": rig.LabelID}},
		{"device", map[string]any{"device_id": rig.DeviceInherit}},
		{"hidden_direct", map[string]any{"entity_id": rig.LightHidden}},
		{"group", map[string]any{"entity_id": rig.GroupID}},
	}

	// The comparison: what HA predicts equals what HA does.
	for _, tc := range targets {
		t.Run("predicts_reach/"+tc.name, func(t *testing.T) {
			predicted := lightsOnly(t, inst, extractFromTarget(t, ws, tc.target))
			acted := toggleLights(t, inst, tc.target)
			if !slices.Equal(predicted, acted) {
				t.Fatalf("target %v: extract_from_target (expand_group, light domain, available) predicted %v, "+
					"light.toggle acted on %v — a permission check built on the prediction would check the wrong set",
					tc.target, predicted, acted)
			}
			if len(acted) == 0 {
				t.Fatalf("target %v reached nothing: the rig is inert for this form and proves no agreement", tc.target)
			}
		})
	}

	// The rules themselves, as observed: each is a place where a plausible
	// re-implementation is wrong, recorded so a change in HA is noticed rather
	// than inherited.
	reach := func(target map[string]any) []string {
		return extractFromTarget(t, ws, target)
	}
	observe := func(rule string, set []string, id string, want bool) {
		t.Helper()
		if slices.Contains(set, id) != want {
			t.Errorf("%s: %s in reach = %v, want %v (reach: %v)", rule, id, !want, want, set)
		}
	}
	area := reach(map[string]any{"area_id": rig.AreaOnFloor})
	observe("an area reaches a light through its device", area, rig.LightInherit, true)
	observe("an area reaches a light assigned to it directly", area, rig.LightDirect, true)
	observe("an entity-level area overrides the device's", area, rig.LightOverride, false)
	observe("a hidden entity drops out of an area target", area, rig.LightHidden, false)
	observe("an area target is not domain-scoped before the call", area, rig.CrossDomain, true)

	observe("the overriding area reaches its entity", reach(map[string]any{"area_id": rig.AreaOffFloor}), rig.LightOverride, true)

	floor := reach(map[string]any{"floor_id": rig.FloorID})
	observe("a floor reaches its area's device-inherited light", floor, rig.LightInherit, true)
	observe("a floor does not reach an area off the floor", floor, rig.LightOverride, false)

	label := reach(map[string]any{"label_id": rig.LabelID})
	observe("a label reaches a light it is attached to", label, rig.LightLabelled, true)
	observe("a label on a device reaches the device's light", label, rig.LightViaDevLbl, true)
	observe("a label on an area reaches the area's light", label, rig.LightOverride, true)

	observe("a hidden entity named directly is reached", reach(map[string]any{"entity_id": rig.LightHidden}), rig.LightHidden, true)

	// `all` cannot be asked at all: the command validates entity_id as real
	// ids and refuses the sentinel, while the service path accepts it and
	// skips the extractor. A caller must classify `all` itself — the spec's
	// `entity:<domain>.*` — and never send it here.
	if env := ws.send(t, map[string]any{
		"type":   "extract_from_target",
		"target": map[string]any{"entity_id": "all"},
	}); env.Success || env.errCode(t) != "invalid_format" {
		t.Errorf("extract_from_target {entity_id: all}: success=%v error=%s, want a refusal with invalid_format",
			env.Success, env.Error)
	}
}

// extractFromTarget asks HA which entities a target references, with the
// expand_group setting the service path uses. It returns the sorted ids.
func extractFromTarget(t *testing.T, ws *rawWS, target map[string]any) []string {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{
		"type":         "extract_from_target",
		"target":       target,
		"expand_group": true,
	})
	var result struct {
		ReferencedEntities []string `json:"referenced_entities"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		t.Fatalf("extract_from_target result did not parse: %v\n%s", err, env.Result)
	}
	if result.ReferencedEntities == nil {
		t.Fatalf("extract_from_target answered without referenced_entities: %s", env.Result)
	}
	sort.Strings(result.ReferencedEntities)
	return result.ReferencedEntities
}

// lightsOnly applies the service path's own restriction for light.toggle:
// entities of the light domain whose state is not unavailable.
func lightsOnly(t *testing.T, inst *hatest.Instance, ids []string) []string {
	t.Helper()
	states := currentStates(t, inst)
	out := []string{}
	for _, id := range ids {
		state, ok := states[id]
		if strings.HasPrefix(id, "light.") && ok && state != "unavailable" {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// toggleLights calls light.toggle on target and returns the light entities
// whose state change HA attributed to the call's own context.
func toggleLights(t *testing.T, inst *hatest.Instance, target map[string]any) []string {
	t.Helper()
	client := haapi.New(inst.URL(), inst.Token())
	changes, err := client.CallService(context.Background(), "light", "toggle", target)
	if err != nil {
		t.Fatalf("light.toggle %v: %v", target, err)
	}
	out := []string{}
	for _, c := range changes {
		if strings.HasPrefix(c.EntityID, "light.") && !slices.Contains(out, c.EntityID) {
			out = append(out, c.EntityID)
		}
	}
	sort.Strings(out)
	return out
}

func currentStates(t *testing.T, inst *hatest.Instance) map[string]string {
	t.Helper()
	raw, err := haapi.New(inst.URL(), inst.Token()).GetStates(context.Background())
	if err != nil {
		t.Fatalf("GET /api/states: %v", err)
	}
	var rows []struct {
		EntityID string `json:"entity_id"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		t.Fatalf("states did not parse: %v", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.EntityID] = r.State
	}
	return out
}

// buildTargetsRig places six demo lights so every expansion path has exactly
// one light that only that path can reach.
func buildTargetsRig(t *testing.T, inst *hatest.Instance) targetsRig {
	t.Helper()
	ctx := context.Background()
	ws := oracleWS(t, inst)
	raw := dialRawWS(t, inst.URL(), inst.Token())

	entities, err := ws.EntityRegistryList(ctx)
	if err != nil {
		t.Fatalf("entity registry list: %v", err)
	}
	devices, lightOf := oneLightPerDevice(t, entities)

	var rig targetsRig
	level := 0
	floor, err := ws.FloorRegistryCreate(ctx, "Targets Floor", "mdi:home", &level)
	if err != nil {
		t.Fatalf("create floor: %v", err)
	}
	rig.FloorID = floor.FloorID
	on, err := ws.AreaRegistryCreate(ctx, "Targets On Floor", "mdi:sofa", rig.FloorID)
	if err != nil {
		t.Fatalf("create area: %v", err)
	}
	rig.AreaOnFloor = on.AreaID
	off, err := ws.AreaRegistryCreate(ctx, "Targets Off Floor", "mdi:sofa-outline", "")
	if err != nil {
		t.Fatalf("create area: %v", err)
	}
	rig.AreaOffFloor = off.AreaID
	label, err := ws.LabelRegistryCreate(ctx, "Targets Label", "blue", "mdi:tag", "")
	if err != nil {
		t.Fatalf("create label: %v", err)
	}
	rig.LabelID = label.LabelID

	device := func(dev string, changes map[string]any) {
		if err := ws.DeviceRegistryUpdate(ctx, dev, changes); err != nil {
			t.Fatalf("update device %s: %v", dev, err)
		}
	}
	// HA refuses an entity-level area on an entity without a name of its own
	// ("set the area on its device instead"), and demo lights take their
	// device's name — so every entity-level placement names the entity too.
	entity := func(id string, changes map[string]any) {
		if _, hasArea := changes["area_id"]; hasArea {
			changes["name"] = "Targets " + id
		}
		if err := ws.EntityRegistryUpdate(ctx, id, changes); err != nil {
			t.Fatalf("update entity %s: %v", id, err)
		}
	}

	rig.DeviceInherit, rig.LightInherit = devices[0], lightOf[devices[0]]
	device(rig.DeviceInherit, map[string]any{"area_id": rig.AreaOnFloor})

	rig.LightDirect = lightOf[devices[1]]
	entity(rig.LightDirect, map[string]any{"area_id": rig.AreaOnFloor})

	rig.DeviceOverride, rig.LightOverride = devices[2], lightOf[devices[2]]
	device(rig.DeviceOverride, map[string]any{"area_id": rig.AreaOnFloor})
	entity(rig.LightOverride, map[string]any{"area_id": rig.AreaOffFloor})

	rig.LightHidden = lightOf[devices[3]]
	entity(rig.LightHidden, map[string]any{"area_id": rig.AreaOnFloor, "hidden_by": "user"})

	rig.DeviceLabelled, rig.LightViaDevLbl = devices[4], lightOf[devices[4]]
	device(rig.DeviceLabelled, map[string]any{"labels": []string{rig.LabelID}})

	rig.LightLabelled = lightOf[devices[5]]
	entity(rig.LightLabelled, map[string]any{"labels": []string{rig.LabelID}})

	// The label also sits on the off-floor area, so label→area is a path of
	// its own. area_registry/update has no haapi method; the raw session does.
	raw.mustSucceed(t, map[string]any{
		"type":    "config/area_registry/update",
		"area_id": rig.AreaOffFloor,
		"labels":  []string{rig.LabelID},
	})

	// A non-light in the same area: the extractor's set spans domains, and the
	// service path narrows it. Without one the narrowing is invisible.
	for _, e := range entities {
		if strings.HasPrefix(e.EntityID, "switch.") && e.DisabledBy == "" && e.HiddenBy == "" {
			rig.CrossDomain = e.EntityID
			break
		}
	}
	if rig.CrossDomain == "" {
		t.Fatal("targets rig: no demo switch to serve as the cross-domain entity")
	}
	entity(rig.CrossDomain, map[string]any{"area_id": rig.AreaOnFloor})

	// Old-style group over two lights the other paths also reach, created at
	// runtime: expand_group must turn group.<id> into its members.
	client := haapi.New(inst.URL(), inst.Token())
	if _, err := client.CallService(ctx, "group", "set", map[string]any{
		"object_id": "targets_lights",
		"entities":  []string{rig.LightInherit, rig.LightLabelled},
	}); err != nil {
		t.Fatalf("group.set: %v", err)
	}
	rig.GroupID = "group.targets_lights"
	if _, ok := currentStates(t, inst)[rig.GroupID]; !ok {
		t.Fatalf("group.set answered but %s has no state", rig.GroupID)
	}
	return rig
}

// oneLightPerDevice returns the demo devices that own a light, ordered by that
// light's id, and the light of each — refusing a device with two lights,
// because a device-level placement would then reach both.
func oneLightPerDevice(t *testing.T, entities []haapi.EntityRegistryEntry) ([]string, map[string]string) {
	t.Helper()
	lightOf := map[string]string{}
	var devices []string
	for _, e := range entities {
		if !strings.HasPrefix(e.EntityID, "light.") || e.DeviceID == "" || e.DisabledBy != "" {
			continue
		}
		if prev, seen := lightOf[e.DeviceID]; seen {
			t.Fatalf("targets rig: device of %s owns a second light %s; a device placement would reach both",
				prev, e.EntityID)
		}
		devices = append(devices, e.DeviceID)
		lightOf[e.DeviceID] = e.EntityID
	}
	sort.Slice(devices, func(i, j int) bool { return lightOf[devices[i]] < lightOf[devices[j]] })
	if len(devices) < 6 {
		t.Fatalf("targets rig: need 6 demo lights on 6 devices, found %d (%v)", len(devices), lightOf)
	}
	return devices, lightOf
}
