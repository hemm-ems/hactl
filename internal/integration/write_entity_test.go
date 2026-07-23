//go:build integration

package integration

import (
	"context"
	"reflect"
	"sort"
	"sync"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// Entity-registry writes (invariant H-12).
//
// Every assertion here reads the registry straight from HA over WebSocket, so
// no part of the expectation travels through the code under test. hactl's own
// echo ("labels set to [...]") is printed the moment the WS call returns nil,
// which is true whether or not HA stored anything.
//
// These writes mutate registry state that the read tests assert on, so they run
// against their own instance rather than the shared one.
// ============================================================================

var (
	writeOnce sync.Once
	writeHA   *hatest.Instance
)

func getWriteHA(t *testing.T) *hatest.Instance {
	t.Helper()
	writeOnce.Do(func() {
		writeHA = hatest.StartShared(t, hatest.WithFixture("basic"))
		waitForRunning(t, writeHA)
	})
	if writeHA == nil {
		t.Fatal("write HA instance unavailable")
	}
	return writeHA
}

func writeWS(t *testing.T, inst *hatest.Instance) *haapi.WSClient {
	t.Helper()
	ws := haapi.NewWSClient(inst.URL(), inst.Token())
	if err := ws.Connect(context.Background()); err != nil {
		t.Fatalf("write-instance ws connect: %v", err)
	}
	t.Cleanup(func() { _ = ws.Close() })
	return ws
}

// registryEntry reads one entity's registry entry from HA itself.
func registryEntry(t *testing.T, ws *haapi.WSClient, entityID string) haapi.EntityRegistryEntry {
	t.Helper()
	entries, err := ws.EntityRegistryList(context.Background())
	if err != nil {
		t.Fatalf("listing entity registry: %v", err)
	}
	for _, e := range entries {
		if e.EntityID == entityID {
			return e
		}
	}
	t.Fatalf("entity %s is not in HA's registry", entityID)
	return haapi.EntityRegistryEntry{}
}

// pickRegisteredEntity returns a deterministic registry entity to write to,
// chosen from HA's own answer rather than hardcoded, so the fixture can change
// without silently skipping the gate.
func pickRegisteredEntity(t *testing.T, ws *haapi.WSClient) haapi.EntityRegistryEntry {
	t.Helper()
	entries, err := ws.EntityRegistryList(context.Background())
	if err != nil {
		t.Fatalf("listing entity registry: %v", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].EntityID < entries[j].EntityID })
	for _, e := range entries {
		if e.DisabledBy == "" && e.UniqueID != "" {
			return e
		}
	}
	t.Fatal("no writable entity in HA's registry")
	return haapi.EntityRegistryEntry{}
}

// witness blanks the field a command is supposed to change, so what remains is
// an independent witness that the write touched nothing else. H-12 step 4.
func witness(e haapi.EntityRegistryEntry, changed string) haapi.EntityRegistryEntry {
	switch changed {
	case "labels":
		e.Labels = nil
	case "area_id":
		e.AreaID = ""
	}
	return e
}

func assertWitnessUnchanged(t *testing.T, before, after haapi.EntityRegistryEntry, changed string) {
	t.Helper()
	b, a := witness(before, changed), witness(after, changed)
	if !reflect.DeepEqual(b, a) {
		t.Errorf("write to %q changed fields it never mentioned:\n before: %s\n after:  %s",
			changed, mustJSON(t, b), mustJSON(t, a))
	}
}

func labelIDsOf(e haapi.EntityRegistryEntry) map[string]bool {
	out := make(map[string]bool, len(e.Labels))
	for _, l := range e.Labels {
		out[l] = true
	}
	return out
}

// TestEntSetLabelRoundTrip proves `ent set-label --confirm` reaches HA, that a
// second call MERGES rather than replaces (the manual promises "merged labels"),
// and that deleting the label removes it from the entity again.
func TestEntSetLabelRoundTrip(t *testing.T) {
	inst := getWriteHA(t)
	ws := writeWS(t, inst)
	ctx := context.Background()

	target := pickRegisteredEntity(t, ws)
	before := registryEntry(t, ws, target.EntityID)

	labelA, err := ws.LabelRegistryCreate(ctx, "Write RT A", "red", "mdi:alpha-a", "")
	if err != nil {
		t.Fatalf("creating label A: %v", err)
	}
	labelB, err := ws.LabelRegistryCreate(ctx, "Write RT B", "blue", "mdi:alpha-b", "")
	if err != nil {
		t.Fatalf("creating label B: %v", err)
	}
	t.Cleanup(func() {
		_ = ws.LabelRegistryDelete(ctx, labelA.LabelID)
		_ = ws.LabelRegistryDelete(ctx, labelB.LabelID)
		_ = ws.EntityRegistryUpdate(ctx, target.EntityID, map[string]any{"labels": before.Labels})
	})

	// --- dry-run must not write ---
	runHactlDir(t, inst.Dir(), "ent", "set-label", target.EntityID, labelA.LabelID)
	if got := registryEntry(t, ws, target.EntityID); labelIDsOf(got)[labelA.LabelID] {
		t.Fatalf("dry-run wrote label %s to HA", labelA.LabelID)
	}

	// --- first confirmed write reaches HA ---
	runHactlDir(t, inst.Dir(), "ent", "set-label", target.EntityID, labelA.LabelID, "--confirm")
	afterA := registryEntry(t, ws, target.EntityID)
	if !labelIDsOf(afterA)[labelA.LabelID] {
		t.Fatalf("set-label did not reach HA: labels are %v, want %s among them",
			afterA.Labels, labelA.LabelID)
	}
	assertWitnessUnchanged(t, before, afterA, "labels")

	// --- second write MERGES, it does not replace ---
	runHactlDir(t, inst.Dir(), "ent", "set-label", target.EntityID, labelB.LabelID, "--confirm")
	afterB := registryEntry(t, ws, target.EntityID)
	got := labelIDsOf(afterB)
	if !got[labelA.LabelID] || !got[labelB.LabelID] {
		t.Fatalf("set-label replaced instead of merging: labels are %v, want both %s and %s",
			afterB.Labels, labelA.LabelID, labelB.LabelID)
	}
	assertWitnessUnchanged(t, before, afterB, "labels")

	// --- removal: deleting the label detaches it from the entity ---
	if err := ws.LabelRegistryDelete(ctx, labelB.LabelID); err != nil {
		t.Fatalf("deleting label B: %v", err)
	}
	afterDelete := registryEntry(t, ws, target.EntityID)
	if labelIDsOf(afterDelete)[labelB.LabelID] {
		t.Errorf("deleted label %s is still attached: %v", labelB.LabelID, afterDelete.Labels)
	}

	// --- restore, and assert the restore ---
	if err := ws.EntityRegistryUpdate(ctx, target.EntityID, map[string]any{"labels": before.Labels}); err != nil {
		t.Fatalf("restoring labels: %v", err)
	}
	restored := registryEntry(t, ws, target.EntityID)
	if !reflect.DeepEqual(labelIDsOf(restored), labelIDsOf(before)) {
		t.Errorf("restore left the entity with %v, want %v", restored.Labels, before.Labels)
	}
}

// TestEntSetAreaRoundTrip proves `ent set-area --confirm` writes the area_id HA
// then reports, resolves an area by name as well as by id, and leaves the rest
// of the registry entry alone.
func TestEntSetAreaRoundTrip(t *testing.T) {
	inst := getWriteHA(t)
	ws := writeWS(t, inst)
	ctx := context.Background()

	target := pickRegisteredEntity(t, ws)
	before := registryEntry(t, ws, target.EntityID)

	area, err := ws.AreaRegistryCreate(ctx, "Write RT Area", "mdi:floor-plan", "")
	if err != nil {
		t.Fatalf("creating area: %v", err)
	}
	t.Cleanup(func() {
		_ = ws.EntityRegistryUpdate(ctx, target.EntityID, map[string]any{"area_id": before.AreaID})
		_ = ws.AreaRegistryDelete(ctx, area.AreaID)
	})

	// --- dry-run must not write ---
	runHactlDir(t, inst.Dir(), "ent", "set-area", target.EntityID, area.AreaID)
	if got := registryEntry(t, ws, target.EntityID); got.AreaID == area.AreaID {
		t.Fatalf("dry-run wrote area %s to HA", area.AreaID)
	}

	// --- confirmed write reaches HA ---
	runHactlDir(t, inst.Dir(), "ent", "set-area", target.EntityID, area.AreaID, "--confirm")
	after := registryEntry(t, ws, target.EntityID)
	if after.AreaID != area.AreaID {
		t.Fatalf("set-area did not reach HA: area_id is %q, want %q", after.AreaID, area.AreaID)
	}
	assertWitnessUnchanged(t, before, after, "area_id")

	// HA is the oracle for what the area now contains.
	if !areaContains(t, inst, area.AreaID, target.EntityID) {
		t.Errorf("HA's own area_entities(%s) does not list %s after the write",
			area.AreaID, target.EntityID)
	}

	// --- the same command resolves an area by NAME, as the manual documents ---
	area2, err := ws.AreaRegistryCreate(ctx, "Write RT Area Two", "mdi:floor-plan", "")
	if err != nil {
		t.Fatalf("creating second area: %v", err)
	}
	t.Cleanup(func() { _ = ws.AreaRegistryDelete(ctx, area2.AreaID) })
	runHactlDir(t, inst.Dir(), "ent", "set-area", target.EntityID, area2.Name, "--confirm")
	if got := registryEntry(t, ws, target.EntityID); got.AreaID != area2.AreaID {
		t.Fatalf("set-area by name did not reach HA: area_id is %q, want %q", got.AreaID, area2.AreaID)
	}

	// --- restore, and assert the restore ---
	if err := ws.EntityRegistryUpdate(ctx, target.EntityID, map[string]any{"area_id": before.AreaID}); err != nil {
		t.Fatalf("restoring area: %v", err)
	}
	if got := registryEntry(t, ws, target.EntityID); got.AreaID != before.AreaID {
		t.Errorf("restore left area_id %q, want %q", got.AreaID, before.AreaID)
	}
}

// areaContains asks HA itself which entities an area holds (invariant H-9).
func areaContains(t *testing.T, inst *hatest.Instance, areaID, entityID string) bool {
	t.Helper()
	for _, e := range oracleAreaEntities(t, inst, areaID) {
		if e == entityID {
			return true
		}
	}
	return false
}

// TestEntSetLabelAndSetAreaAgreeOnUnknownEntity pins the two commands to the
// same answer for the same input.
//
// They disagreed: `set-area` resolved the entity in the registry first and
// failed, while `set-label` never looked, so a typo produced a confident
// "would set entity labels" plan at exit 0 — under the manual's stop-at-the-
// first-miss rule that reads as a successful plan. The dry run must fail
// exactly where the confirmed run would.
func TestEntSetLabelAndSetAreaAgreeOnUnknownEntity(t *testing.T) {
	inst := getWriteHA(t)
	ws := writeWS(t, inst)
	ctx := context.Background()

	const ghost = "sensor.this_entity_does_not_exist_xyz"
	if entries, err := ws.EntityRegistryList(ctx); err == nil {
		for _, e := range entries {
			if e.EntityID == ghost {
				t.Fatalf("precondition failed: %s exists in the registry", ghost)
			}
		}
	}

	label, err := ws.LabelRegistryCreate(ctx, "Write RT Ghost", "green", "mdi:ghost", "")
	if err != nil {
		t.Fatalf("creating label: %v", err)
	}
	t.Cleanup(func() { _ = ws.LabelRegistryDelete(ctx, label.LabelID) })

	area, err := ws.AreaRegistryCreate(ctx, "Write RT Ghost Area", "mdi:floor-plan", "")
	if err != nil {
		t.Fatalf("creating area: %v", err)
	}
	t.Cleanup(func() { _ = ws.AreaRegistryDelete(ctx, area.AreaID) })

	_, labelErr := runHactlDirErr(t, inst.Dir(), "ent", "set-label", ghost, label.LabelID)
	_, areaErr := runHactlDirErr(t, inst.Dir(), "ent", "set-area", ghost, area.AreaID)

	if (labelErr == nil) != (areaErr == nil) {
		t.Fatalf("set-label and set-area disagree on the same unknown entity:\n"+
			"  set-label err: %v\n  set-area  err: %v", labelErr, areaErr)
	}
	if labelErr == nil {
		t.Errorf("set-label planned a write for %s, which is not in the registry", ghost)
	}
}
