//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// H-8 — An entity's effective area and labels include the ones it inherits
// from its device.
//
// R2: HA's own rule (homeassistant/helpers/template/extensions/areas.py,
// AreaExtension.area_name/area_entities) is: an entity's effective area is
// entity.area_id if set, else device_registry[entity.device_id].area_id.
// Placing the DEVICE in a room is the normal HA pattern; entity-level area
// assignment is the exception. hactl only ever read the entity's own field.
//
// Labels do NOT follow the same rule — confirmed against running HA 2026.7.2
// source (homeassistant/helpers/template/extensions/labels.py): label_entities()
// resolves via entity_registry.async_entries_for_label with no device
// expansion at all. TestEntLsLabelMatchesOracleInheritance exists specifically
// to pin that down: it asserts hactl agrees with HA's own (empty) answer for a
// device-only label, so a well-intentioned "fix" that copies the area fallback
// onto labels doesn't silently reappear later.
// ============================================================================

// relatedRow mirrors one row of `ent related`'s table: entity_id, relationship, detail.
type relatedRow struct {
	EntityID     string `json:"entity_id"`
	Relationship string `json:"relationship"`
	Detail       string `json:"detail"`
}

// entRelatedJSON runs `ent related <id> --json --top 1000` and decodes it strictly.
func entRelatedJSON(t *testing.T, dir, entityID string) []relatedRow {
	t.Helper()
	raw := runHactlDir(t, dir, "ent", "related", entityID, "--json", "--top", "1000")
	var rows []relatedRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("ent related %s --json did not parse: %v\noutput:\n%s", entityID, err, raw)
	}
	return rows
}

// hasAreaNeighbor reports whether rows contains entityID with relationship "area-neighbor".
// Deliberately relationship-specific: InheritedEntityIDs are also device-siblings
// (findDeviceSiblings, unrelated to H-8), so a mere entity_id presence check
// would pass even with the area-neighbor fallback still broken.
func hasAreaNeighbor(rows []relatedRow, entityID string) bool {
	for _, r := range rows {
		if r.EntityID == entityID && r.Relationship == "area-neighbor" {
			return true
		}
	}
	return false
}

// oracleDomainAreaPeers returns, for entities of the same domain as sourceID in
// the FULL entity registry, those whose HA-computed effective area
// (oracleEntityArea, i.e. area_name()) equals sourceID's — the ground truth
// findAreaNeighbors (ent.go site 3) must reproduce. Unlike oracleAreaEntities
// (area_entities()), this does not silently drop disabled entities, matching
// findAreaNeighbors' own registry-only (not live-state) semantics.
func oracleDomainAreaPeers(t *testing.T, inst *hatest.Instance, sourceID string) []string {
	t.Helper()
	ws := oracleWS(t, inst)
	entries, err := ws.EntityRegistryList(context.Background())
	if err != nil {
		t.Fatalf("entity registry list: %v", err)
	}
	domain, _, _ := strings.Cut(sourceID, ".")
	sourceArea := oracleEntityArea(t, inst, sourceID)
	if sourceArea == "" {
		return nil
	}
	var peers []string
	for _, e := range entries {
		if e.EntityID == sourceID {
			continue
		}
		d, _, _ := strings.Cut(e.EntityID, ".")
		if d != domain {
			continue
		}
		if oracleEntityArea(t, inst, e.EntityID) == sourceArea {
			peers = append(peers, e.EntityID)
		}
	}
	sort.Strings(peers)
	return peers
}

// TestEntLsAreaMatchesOracleInheritance is the headline R2 regression: `ent ls
// --area` must set-equal HA's own area_entities(), which already folds in
// device-inherited membership.
func TestEntLsAreaMatchesOracleInheritance(t *testing.T) {
	inst, rig := getOracleHA(t)

	want := oracleAreaEntities(t, inst, rig.AreaDev)
	if len(want) == 0 {
		t.Fatal("precondition: HA reports no entities in AreaDev; rig is broken")
	}

	raw := runHactlDir(t, inst.Dir(), "ent", "ls", "--area", rig.AreaDevName, "--json", "--top", "1000")
	got := entIDsFromJSON(t, raw)

	assertSameSet(t, "ent ls --area "+rig.AreaDevName, want, got)
}

// TestEntLsLabelMatchesOracleInheritance asserts hactl agrees with HA's own
// label_entities() for a label attached only to a device — which, per HA's
// source (see the file-level comment), is EMPTY: labels do not inherit
// through the device the way area does. This pins hactl's (correct) choice
// not to add a label fallback.
func TestEntLsLabelMatchesOracleInheritance(t *testing.T) {
	inst, rig := getOracleHA(t)

	want := oracleLabelEntities(t, inst, rig.LabelDevice)

	raw := runHactlDir(t, inst.Dir(), "ent", "ls", "--label", rig.LabelDeviceName, "--json", "--top", "1000")
	got := entIDsFromJSON(t, raw)

	assertSameSet(t, "ent ls --label "+rig.LabelDeviceName, want, got)
}

// TestEntShowOverrideAreaViaDeviceEntities proves the precedence direction is
// still correct after adding the device fallback: an entity with its OWN area
// must show that area, never the device's, even though it lives on the same
// device as the inherited entities. The override entity in this rig
// (binary_sensor.sun_solar_rising) is disabled by default in the demo
// integration and so carries no live state — /api/states-backed commands
// (`ent show`, `ent ls`) can't see it at all — so this is checked via `device
// show`, which reads the entity registry directly (site 4) and is where the
// override would actually be lost if a fix wrongly made the device always win.
func TestEntShowOverrideAreaViaDeviceEntities(t *testing.T) {
	inst, rig := getOracleHA(t)

	wantArea := oracleEntityArea(t, inst, rig.OverrideEntityID)
	if wantArea == "" {
		t.Fatal("precondition: HA reports no area for the override entity")
	}
	if wantArea != rig.AreaMainName {
		t.Fatalf("precondition: override entity's HA area = %q, want rig.AreaMainName %q", wantArea, rig.AreaMainName)
	}

	out := runHactlDir(t, inst.Dir(), "device", "show", rig.DeviceID, "--full", "--top", "1000")
	row := entityRowLine(t, out, rig.OverrideEntityID)
	if !strings.Contains(row, wantArea) {
		t.Errorf("device show %s: override entity %s row does not show its own area %q (device's own area is %q):\nrow: %q\nfull output:\n%s",
			rig.DeviceID, rig.OverrideEntityID, wantArea, rig.AreaDevName, row, out)
	}
	if strings.Contains(row, rig.AreaDevName) {
		t.Errorf("device show %s: override entity %s row wrongly shows the DEVICE's area %q instead of its own %q:\nrow: %q",
			rig.DeviceID, rig.OverrideEntityID, rig.AreaDevName, wantArea, row)
	}
}

// entityRowLine returns the line of `out` that starts with entityID (the
// device-show entity table is plain text; there is one row per entity).
func entityRowLine(t *testing.T, out, entityID string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), entityID) {
			return line
		}
	}
	t.Fatalf("output has no row for %s:\n%s", entityID, out)
	return ""
}

// TestEntShowInheritedAreaLine covers site 2 directly: `ent show` on a
// device-inherited entity must print an `area:` line matching HA's own
// area_name() — not an empty one, which is what the pre-fix code produced.
func TestEntShowInheritedAreaLine(t *testing.T) {
	inst, rig := getOracleHA(t)
	entityID := rig.InheritedEntityIDs[0]

	wantArea := oracleEntityArea(t, inst, entityID)
	if wantArea == "" {
		t.Fatal("precondition: HA reports no area for the inherited entity")
	}

	out := runHactlDir(t, inst.Dir(), "ent", "show", entityID)
	wantLine := "area:         " + wantArea
	if !strings.Contains(out, wantLine) {
		t.Errorf("ent show %s missing area line %q (HA says area_name(%s) = %q).\nfull output:\n%s",
			entityID, wantLine, entityID, wantArea, out)
	}
}

// TestEntRelatedAreaNeighborsUseInheritedArea is the site-3 regression:
// findAreaNeighbors must use the entity's EFFECTIVE (device-inherited) area,
// not just its own (empty) AreaID field. Fixing registryContext.areaName
// alone does not reach this — findAreaNeighbors reads ent.AreaID inline.
func TestEntRelatedAreaNeighborsUseInheritedArea(t *testing.T) {
	inst, rig := getOracleHA(t)
	source := rig.InheritedEntityIDs[0]

	wantPeers := oracleDomainAreaPeers(t, inst, source)
	if len(wantPeers) == 0 {
		t.Fatalf("precondition: HA reports no same-domain area peers for %s; rig %+v cannot exercise site 3", source, rig)
	}

	rows := entRelatedJSON(t, inst.Dir(), source)
	for _, peer := range wantPeers {
		if !hasAreaNeighbor(rows, peer) {
			t.Errorf("ent related %s: missing area-neighbor row for %s (HA agrees their effective areas match).\nrows: %+v",
				source, peer, rows)
		}
	}
}

// TestDeviceShowEntitiesShowInheritedArea is the site-4 regression: the
// entity table inside `device show` used its own hand-rolled area lookup with
// no device fallback, separate from registryContext.areaName.
func TestDeviceShowEntitiesShowInheritedArea(t *testing.T) {
	inst, rig := getOracleHA(t)

	out := runHactlDir(t, inst.Dir(), "device", "show", rig.DeviceID, "--full", "--top", "1000")
	for _, entityID := range rig.InheritedEntityIDs {
		wantArea := oracleEntityArea(t, inst, entityID)
		row := entityRowLine(t, out, entityID)
		if !strings.Contains(row, wantArea) {
			t.Errorf("device show %s: entity %s row missing inherited area %q:\nrow: %q",
				rig.DeviceID, entityID, wantArea, row)
		}
	}
}

// TestEntLsAreaNegativeControl guards against a fix broad enough to match
// everything: an entity with no area at all — neither its own nor any
// device's (this fixture's plain input_boolean helpers have no device_id) —
// must never appear in an --area result.
func TestEntLsAreaNegativeControl(t *testing.T) {
	inst, rig := getOracleHA(t)
	const unrelated = "input_boolean.oracle_never"

	if got := oracleEntityArea(t, inst, unrelated); got != "" {
		t.Fatalf("precondition: HA reports %s has area %q, want none", unrelated, got)
	}

	raw := runHactlDir(t, inst.Dir(), "ent", "ls", "--area", rig.AreaDevName, "--json", "--top", "1000")
	got := entIDsFromJSON(t, raw)
	for _, id := range got {
		if id == unrelated {
			t.Errorf("ent ls --area %s wrongly included %s, which HA says has no area at all",
				rig.AreaDevName, unrelated)
		}
	}

	raw2 := runHactlDir(t, inst.Dir(), "ent", "ls", "--area", rig.AreaMainName, "--json", "--top", "1000")
	got2 := entIDsFromJSON(t, raw2)
	for _, id := range got2 {
		if id == unrelated {
			t.Errorf("ent ls --area %s wrongly included %s, which HA says has no area at all",
				rig.AreaMainName, unrelated)
		}
	}
}

// TestEntShowJSONIncludesTableFields covers the --json completeness gap: the
// human path computes name/unit/area/labels/changed_by but --json used to
// encode only the raw state struct.
func TestEntShowJSONIncludesTableFields(t *testing.T) {
	inst, rig := getOracleHA(t)
	const entityID = "input_number.oracle_level"

	wantArea := oracleEntityArea(t, inst, entityID)
	if wantArea == "" {
		t.Fatal("precondition: HA reports no area for input_number.oracle_level")
	}

	raw := runHactlDir(t, inst.Dir(), "ent", "show", entityID, "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("ent show --json did not parse: %v\noutput:\n%s", err, raw)
	}

	for _, field := range []string{"entity_id", "state", "name", "area", "labels", "changed_by"} {
		if _, ok := got[field]; !ok {
			t.Errorf("ent show %s --json missing field %q; got keys: %v", entityID, field, keysOf(got))
		}
	}
	if got["area"] != wantArea {
		t.Errorf("ent show %s --json area = %v, want %q (HA's area_name())", entityID, got["area"], wantArea)
	}
	if labels, _ := got["labels"].(string); !strings.Contains(labels, rig.LabelDirectName) {
		t.Errorf("ent show %s --json labels = %v, want to contain %q", entityID, got["labels"], rig.LabelDirectName)
	}
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// TestEntHistJSONParsesStrictly and its siblings below are the H-7-adjacent
// regression: ent hist/anomalies/related used to print a human header line
// before the JSON table body, so --json output failed strict decoding.

func TestEntHistJSONParsesStrictly(t *testing.T) {
	inst, _ := getOracleHA(t)
	// input_number.oracle_level is set to 77 by automation cfgid_boost_charge
	// during exerciseOracleRig, so it has genuine numeric history.
	raw := runHactlDir(t, inst.Dir(), "ent", "hist", "input_number.oracle_level", "--json")
	var points []map[string]any
	if err := json.Unmarshal([]byte(raw), &points); err != nil {
		t.Fatalf("ent hist --json did not parse: %v\noutput:\n%s", err, raw)
	}
	if len(points) == 0 {
		t.Fatal("precondition: expected at least one history point for input_number.oracle_level")
	}
}

func TestEntAnomaliesJSONParsesStrictly(t *testing.T) {
	inst, _ := getOracleHA(t)
	raw := runHactlDir(t, inst.Dir(), "ent", "anomalies", "input_number.oracle_level", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("ent anomalies --json did not parse: %v\noutput:\n%s", err, raw)
	}
}

func TestEntRelatedJSONParsesStrictly(t *testing.T) {
	inst, rig := getOracleHA(t)
	rows := entRelatedJSON(t, inst.Dir(), rig.InheritedEntityIDs[0])
	if len(rows) == 0 {
		t.Fatal("precondition: expected at least one related row (device siblings alone should produce several)")
	}
}

// oracleAreaPeers is oracleDomainAreaPeers without the domain filter: every
// OTHER registry entity whose HA-computed effective area equals sourceID's.
// This is what "area neighbors" means in HA's own vocabulary — area_entities()
// has no notion of a domain — and therefore what `ent related` must reproduce.
func oracleAreaPeers(t *testing.T, inst *hatest.Instance, sourceID string) []string {
	t.Helper()
	ws := oracleWS(t, inst)
	entries, err := ws.EntityRegistryList(context.Background())
	if err != nil {
		t.Fatalf("entity registry list: %v", err)
	}
	sourceArea := oracleEntityArea(t, inst, sourceID)
	if sourceArea == "" {
		return nil
	}
	var peers []string
	for _, e := range entries {
		if e.EntityID == sourceID {
			continue
		}
		if oracleEntityArea(t, inst, e.EntityID) == sourceArea {
			peers = append(peers, e.EntityID)
		}
	}
	sort.Strings(peers)
	return peers
}

// TestEntRelatedAreaNeighborsAreNotDomainScoped (R1).
//
// `ent related` promised "area neighbors" and delivered same-area-AND-same-
// domain ones: a light in the same room as a sensor was silently absent, so
// the answer was narrower than both `ent ls --area` and HA's own
// area_entities() — for a command whose whole purpose is finding what else is
// involved before a change or a delete. The filter was invisible in the
// output: there is no "same domain" column, and the manual qualifies nothing.
func TestEntRelatedAreaNeighborsAreNotDomainScoped(t *testing.T) {
	inst, rig := getOracleHA(t)
	source := rig.InheritedEntityIDs[0]

	want := oracleAreaPeers(t, inst, source)
	if len(want) == 0 {
		t.Fatalf("precondition: HA reports no area peers for %s; rig %+v cannot exercise this", source, rig)
	}
	// The rig must contain at least one peer of a DIFFERENT domain, or this
	// test would pass with the domain filter still in place.
	sourceDomain, _, _ := strings.Cut(source, ".")
	crossDomain := false
	for _, p := range want {
		if d, _, _ := strings.Cut(p, "."); d != sourceDomain {
			crossDomain = true
			break
		}
	}
	if !crossDomain {
		t.Fatalf("precondition: every area peer of %s shares its domain; a domain filter would be invisible here", source)
	}

	rows := entRelatedJSON(t, inst.Dir(), source)
	var got []string
	for _, r := range rows {
		if r.Relationship == "area-neighbor" {
			got = append(got, r.EntityID)
		}
	}
	assertSameSet(t, "ent related "+source+" area-neighbors", want, got)
}
