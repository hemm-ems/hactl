//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// The oracle harness (invariant H-9).
//
// Rule: for any read Home Assistant can answer itself, the expected value is
// computed from HA at test time — never hardcoded, never golden-filed.
//
// This exists because hand-written expectations are written by the same person
// who wrote the implementation, and they make the same modelling mistake. The
// device-inheritance bug (H-8) is the worked example: every hand-written
// expectation in the suite agreed with the buggy code, because the author also
// forgot that an entity inherits its device's area. HA's own `area_entities()`
// does not forget, because it IS the definition.
// ============================================================================

var (
	oracleOnce sync.Once
	oracleHA   *hatest.Instance
	oracleRig  rigIDs
)

// rigIDs records the identifiers the oracle rig created, so tests can refer to
// them without re-deriving (and without hardcoding HA-assigned ids).
type rigIDs struct {
	FloorID  string
	AreaMain string // area whose members are entity-assigned
	AreaDev  string // area whose members are DEVICE-assigned (the H-8 case)
	AreaMainName,
	AreaDevName string
	LabelDirect string // label id attached to an entity directly
	LabelDevice string // label id attached to a DEVICE only (the H-8 case)
	LabelDirectName,
	LabelDeviceName string
	// DeviceID placed into AreaDev, and the entities that must inherit from it.
	DeviceID           string
	DeviceName         string
	InheritedEntityIDs []string
	// Entity whose own area overrides its device's area.
	OverrideEntityID string
	OverrideAreaID   string
}

func getOracleHA(t *testing.T) (*hatest.Instance, rigIDs) {
	t.Helper()
	oracleOnce.Do(func() {
		oracleHA = hatest.StartShared(t, hatest.WithFixture("oracle"))
		waitForRunning(t, oracleHA)
		oracleRig = buildOracleRig(t, oracleHA)
		exerciseOracleRig(t, oracleHA)
	})
	if oracleHA == nil {
		t.Fatal("oracle HA instance unavailable")
	}
	return oracleHA, oracleRig
}

func oracleWS(t *testing.T, inst *hatest.Instance) *haapi.WSClient {
	t.Helper()
	ws := haapi.NewWSClient(inst.URL(), inst.Token())
	if err := ws.Connect(context.Background()); err != nil {
		t.Fatalf("oracle ws connect: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

// buildOracleRig creates the registry graph the fixture YAML cannot express.
// Areas, labels and floors have no YAML representation, and devices cannot be
// created at all — they arrive with the `demo:` integration and are only
// *placed* here.
func buildOracleRig(t *testing.T, inst *hatest.Instance) rigIDs {
	t.Helper()
	ctx := context.Background()
	ws := haapi.NewWSClient(inst.URL(), inst.Token())
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("oracle rig ws connect: %v", err)
	}
	defer func() { _ = ws.Close() }()

	var rig rigIDs
	level := 0
	floor, err := ws.FloorRegistryCreate(ctx, "Oracle Floor", "mdi:home", &level)
	if err != nil {
		t.Fatalf("create floor: %v", err)
	}
	rig.FloorID = floor.FloorID

	// H-8: two areas whose names are prefix-overlapping, so a substring filter
	// that should discriminate has something to get wrong.
	rig.AreaMainName = "Oracle Room"
	rig.AreaDevName = "Oracle Room Annex"
	main, err := ws.AreaRegistryCreate(ctx, rig.AreaMainName, "mdi:sofa", rig.FloorID)
	if err != nil {
		t.Fatalf("create area %q: %v", rig.AreaMainName, err)
	}
	rig.AreaMain = main.AreaID
	annex, err := ws.AreaRegistryCreate(ctx, rig.AreaDevName, "mdi:sofa-outline", rig.FloorID)
	if err != nil {
		t.Fatalf("create area %q: %v", rig.AreaDevName, err)
	}
	rig.AreaDev = annex.AreaID

	// H-8: label NAME differs from label ID ("Oracle Power" -> oracle_power),
	// and one label is attached only to a device.
	rig.LabelDirectName = "Oracle Direct"
	rig.LabelDeviceName = "Oracle Device Side"
	ld, err := ws.LabelRegistryCreate(ctx, rig.LabelDirectName, "blue", "mdi:tag", "attached to an entity")
	if err != nil {
		t.Fatalf("create label: %v", err)
	}
	rig.LabelDirect = ld.LabelID
	lv, err := ws.LabelRegistryCreate(ctx, rig.LabelDeviceName, "green", "mdi:tag-outline", "attached to a device")
	if err != nil {
		t.Fatalf("create label: %v", err)
	}
	rig.LabelDevice = lv.LabelID

	// Pick a demo device that owns at least two entities, so device-siblings and
	// device-inherited area are both observable on it.
	devices, err := ws.DeviceRegistryList(ctx)
	if err != nil {
		t.Fatalf("device registry list: %v", err)
	}
	entities, err := ws.EntityRegistryList(ctx)
	if err != nil {
		t.Fatalf("entity registry list: %v", err)
	}
	byDevice := map[string][]string{}
	for _, e := range entities {
		if e.DeviceID != "" {
			byDevice[e.DeviceID] = append(byDevice[e.DeviceID], e.EntityID)
		}
	}
	var chosen haapi.DeviceRegistryEntry
	best := 0
	for _, d := range devices {
		n := len(byDevice[d.ID])
		name := d.NameByUser
		if name == "" {
			name = d.Name
		}
		// Deterministic: highest entity count, ties broken by name.
		if n > best || (n == best && n > 0 && name < chosen.Name) {
			chosen, best = d, n
		}
	}
	if best == 0 {
		t.Fatal("oracle rig: no demo device with entities found; H-8 cannot be tested")
	}
	rig.DeviceID = chosen.ID
	rig.DeviceName = chosen.NameByUser
	if rig.DeviceName == "" {
		rig.DeviceName = chosen.Name
	}

	// The device goes into AreaDev and carries LabelDevice. Its entities get
	// NEITHER — so anything they report must have been inherited.
	if err := ws.DeviceRegistryUpdate(ctx, rig.DeviceID, map[string]any{
		"area_id": rig.AreaDev,
		"labels":  []string{rig.LabelDevice},
	}); err != nil {
		t.Fatalf("place device %s: %v", rig.DeviceID, err)
	}
	sibs := append([]string(nil), byDevice[rig.DeviceID]...)
	sort.Strings(sibs)
	rig.InheritedEntityIDs = sibs

	// One of the device's own entities overrides the device area, so the
	// precedence direction (entity wins) is tested alongside the fallback.
	rig.OverrideEntityID = sibs[0]
	rig.OverrideAreaID = rig.AreaMain
	rig.InheritedEntityIDs = sibs[1:]
	if len(rig.InheritedEntityIDs) == 0 {
		t.Fatalf("oracle rig: device %q has only one entity; need >=2 for override+inherit", rig.DeviceName)
	}
	if err := ws.EntityRegistryUpdate(ctx, rig.OverrideEntityID, map[string]any{
		"area_id": rig.OverrideAreaID,
	}); err != nil {
		t.Fatalf("override entity area: %v", err)
	}

	// A plain entity-level assignment, as the control group.
	if err := ws.EntityRegistryUpdate(ctx, "input_number.oracle_level", map[string]any{
		"area_id": rig.AreaMain,
		"labels":  []string{rig.LabelDirect},
	}); err != nil {
		t.Fatalf("assign input_number.oracle_level: %v", err)
	}
	return rig
}

// exerciseOracleRig fires the fixture's automations for real, so traces exist.
// H-8's second clause: a distinguishing fixture that is never exercised proves
// nothing. These automations are triggered by genuine state changes rather than
// automation.trigger, so conditions evaluate and a blocked action is genuinely
// skipped.
func exerciseOracleRig(t *testing.T, inst *hatest.Instance) {
	t.Helper()
	ctx := context.Background()
	client := haapi.New(inst.URL(), inst.Token())

	toggle := func(entityID string) {
		for _, svc := range []string{"turn_on", "turn_off"} {
			if err := client.CallService(ctx, "input_boolean", svc,
				map[string]any{"entity_id": entityID}); err != nil {
				t.Fatalf("call input_boolean.%s on %s: %v", svc, entityID, err)
			}
			time.Sleep(600 * time.Millisecond)
		}
	}
	// Three rounds so HA's system_log aggregates repeats into count>1, which is
	// what `log --unique` must report (H-11).
	for range 3 {
		toggle("input_boolean.oracle_trigger_a")
		toggle("input_boolean.oracle_trigger_b")
	}
	for range 2 {
		if err := client.CallService(ctx, "script", "turn_on",
			map[string]any{"entity_id": "script.oracle_script_broken"}); err != nil {
			t.Fatalf("run script.oracle_script_broken: %v", err)
		}
		time.Sleep(600 * time.Millisecond)
	}
	time.Sleep(1500 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// Oracle accessors: each returns HA's OWN answer to a question hactl also answers.
// ---------------------------------------------------------------------------

// oracleAreaEntities asks HA which entities are in an area, using HA's own
// `area_entities()` template function — the canonical definition of area
// membership, inheritance included.
func oracleAreaEntities(t *testing.T, inst *hatest.Instance, areaID string) []string {
	t.Helper()
	return oracleTemplateList(t, inst,
		fmt.Sprintf(`{{ area_entities('%s') | sort | join(',') }}`, areaID))
}

// oracleLabelEntities asks HA which entities carry a label, via `label_entities()`.
func oracleLabelEntities(t *testing.T, inst *hatest.Instance, labelID string) []string {
	t.Helper()
	return oracleTemplateList(t, inst,
		fmt.Sprintf(`{{ label_entities('%s') | sort | join(',') }}`, labelID))
}

// oracleEntityArea asks HA for one entity's effective area name, via `area_name()`.
func oracleEntityArea(t *testing.T, inst *hatest.Instance, entityID string) string {
	t.Helper()
	client := haapi.New(inst.URL(), inst.Token())
	out, err := client.RenderTemplate(context.Background(),
		fmt.Sprintf(`{{ area_name('%s') }}`, entityID))
	if err != nil {
		t.Fatalf("oracle area_name(%s): %v", entityID, err)
	}
	out = strings.TrimSpace(out)
	if out == "None" {
		return ""
	}
	return out
}

func oracleTemplateList(t *testing.T, inst *hatest.Instance, tmpl string) []string {
	t.Helper()
	client := haapi.New(inst.URL(), inst.Token())
	out, err := client.RenderTemplate(context.Background(), tmpl)
	if err != nil {
		t.Fatalf("oracle template %q: %v", tmpl, err)
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return nil
	}
	parts := strings.Split(out, ",")
	res := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			res = append(res, p)
		}
	}
	sort.Strings(res)
	return res
}

// oracleTraceItemIDs returns the item_ids HA actually keys traces by, per domain.
func oracleTraceItemIDs(t *testing.T, inst *hatest.Instance, domain string) map[string]int {
	t.Helper()
	ws := oracleWS(t, inst)
	res, err := ws.TraceList(context.Background(), domain)
	if err != nil {
		t.Fatalf("oracle trace/list %s: %v", domain, err)
	}
	counts := map[string]int{}
	for key, list := range res {
		itemID := strings.TrimPrefix(key, domain+".")
		counts[itemID] = len(list)
	}
	return counts
}

// oracleErroredTraceItemIDs returns the item_ids that have at least one errored run.
func oracleErroredTraceItemIDs(t *testing.T, inst *hatest.Instance, domain string) []string {
	t.Helper()
	ws := oracleWS(t, inst)
	res, err := ws.TraceList(context.Background(), domain)
	if err != nil {
		t.Fatalf("oracle trace/list %s: %v", domain, err)
	}
	var out []string
	for key, list := range res {
		for _, tr := range list {
			if tr.Error != "" {
				out = append(out, strings.TrimPrefix(key, domain+"."))
				break
			}
		}
	}
	sort.Strings(out)
	return out
}

// oracleCustomIntegrations returns the domains HA itself considers non-built-in.
func oracleCustomIntegrations(t *testing.T, inst *hatest.Instance) []string {
	t.Helper()
	ws := oracleWS(t, inst)
	manifests, err := ws.IntegrationManifestList(context.Background())
	if err != nil {
		t.Fatalf("oracle manifest/list: %v", err)
	}
	var out []string
	for _, m := range manifests {
		if !m.IsBuiltIn {
			out = append(out, m.Domain)
		}
	}
	sort.Strings(out)
	return out
}

// oracleLogNames returns HA's full logger names and their occurrence counts.
func oracleLogNames(t *testing.T, inst *hatest.Instance) map[string]int {
	t.Helper()
	ws := oracleWS(t, inst)
	entries, err := ws.SystemLogList(context.Background())
	if err != nil {
		t.Fatalf("oracle system_log/list: %v", err)
	}
	out := map[string]int{}
	for _, e := range entries {
		out[e.Name] += e.Count
	}
	return out
}

// ---------------------------------------------------------------------------
// Set comparison
// ---------------------------------------------------------------------------

// assertSameSet fails with a symmetric difference — the only diff shape that
// makes a set bug legible at a glance.
func assertSameSet(t *testing.T, what string, want, got []string) {
	t.Helper()
	w := append([]string(nil), want...)
	g := append([]string(nil), got...)
	sort.Strings(w)
	sort.Strings(g)
	if slices.Equal(w, g) {
		return
	}
	var missing, extra []string
	for _, x := range w {
		if !slices.Contains(g, x) {
			missing = append(missing, x)
		}
	}
	for _, x := range g {
		if !slices.Contains(w, x) {
			extra = append(extra, x)
		}
	}
	t.Errorf("%s: set mismatch\n  HA says (%d): %v\n  hactl says (%d): %v\n  missing from hactl (%d): %v\n  invented by hactl (%d): %v",
		what, len(w), w, len(g), g, len(missing), missing, len(extra), extra)
}

// entIDsFromJSON extracts entity_id values from `ent ls --json` style output.
// It defeats the default --top so a set comparison is over the true set.
func entIDsFromJSON(t *testing.T, raw string) []string {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("decoding hactl JSON: %v\noutput was:\n%s", err, raw)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		if v, ok := r["entity_id"].(string); ok {
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}
