//go:build integration

package integration

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// deviceWriteHA is a dedicated instance for device-registry writes. Devices
// only exist where a config-entry integration creates them, so this boots the
// oracle fixture (demo:) rather than basic — and it is NOT the shared oracle
// instance, whose tests read a carefully placed device graph that a write
// test mutating device placement would race.
var (
	deviceWriteHA   *hatest.Instance
	deviceWriteOnce sync.Once
)

func getDeviceWriteHA(t *testing.T) *hatest.Instance {
	t.Helper()
	deviceWriteOnce.Do(func() {
		deviceWriteHA = hatest.StartShared(t, hatest.WithFixture("oracle"))
		waitForRunning(t, deviceWriteHA)
	})
	if deviceWriteHA == nil {
		t.Fatal("device write HA instance unavailable")
	}
	return deviceWriteHA
}

// deviceEntry re-reads one device from HA's registry over WS — the read-back
// path H-12 demands (never through hactl).
func deviceEntry(t *testing.T, ws *haapi.WSClient, deviceID string) haapi.DeviceRegistryEntry {
	t.Helper()
	devices, err := ws.DeviceRegistryList(context.Background())
	if err != nil {
		t.Fatalf("device registry list: %v", err)
	}
	for _, d := range devices {
		if d.ID == deviceID {
			return d
		}
	}
	t.Fatalf("device %s not in registry", deviceID)
	return haapi.DeviceRegistryEntry{}
}

// pickWritableDevice returns the demo device with the most entities (ties by
// id), so the H-8 inheritance half below has entities to observe.
func pickWritableDevice(t *testing.T, ws *haapi.WSClient) (haapi.DeviceRegistryEntry, []string) {
	t.Helper()
	ctx := context.Background()
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
		// Only entities WITHOUT an own area_id inherit (H-8), and only
		// enabled, unhidden ones are observable through HA's area_entities —
		// the sun integration's device carries disabled-by-default sensors
		// that the registry lists but area_entities never answers with.
		if e.DeviceID != "" && e.AreaID == "" && e.DisabledBy == "" && e.HiddenBy == "" {
			byDevice[e.DeviceID] = append(byDevice[e.DeviceID], e.EntityID)
		}
	}
	var chosen haapi.DeviceRegistryEntry
	best := 0
	for _, d := range devices {
		n := len(byDevice[d.ID])
		if n > best || (n == best && n > 0 && d.ID < chosen.ID) {
			chosen, best = d, n
		}
	}
	if best == 0 {
		t.Fatal("no device with inheritable entities in the fixture; H-8 half cannot be observed")
	}
	return chosen, byDevice[chosen.ID]
}

func deviceWitness(d haapi.DeviceRegistryEntry, changed ...string) haapi.DeviceRegistryEntry {
	for _, c := range changed {
		switch c {
		case "area_id":
			d.AreaID = ""
		case "labels":
			d.Labels = nil
		}
	}
	return d
}

// assertDeviceWitnessUnchanged — H-12 clauses 3+4 for the device registry:
// the whole entry, with the deliberately changed fields blanked, must survive
// the write byte-identically. Manufacturer, model and sw_version are fields
// the commands never print — the independent witnesses.
func assertDeviceWitnessUnchanged(t *testing.T, before, after haapi.DeviceRegistryEntry, changed ...string) {
	t.Helper()
	b, a := deviceWitness(before, changed...), deviceWitness(after, changed...)
	if !reflect.DeepEqual(b, a) {
		t.Errorf("write to %v changed device fields it never mentioned:\n before: %+v\n after:  %+v",
			changed, b, a)
	}
}

// TestDeviceSetAreaLabelRoundTrip — H-12 for `device set-area` and
// `device set-label` (writeback.manifest rows): dry runs change nothing,
// confirmed writes reach HA's device registry directly, untouched fields
// survive, HA's own area_entities confirms the H-8 inheritance, and the
// restore is asserted.
func TestDeviceSetAreaLabelRoundTrip(t *testing.T) {
	inst := getDeviceWriteHA(t)
	ws := oracleWS(t, inst)
	ctx := context.Background()

	device, inheritingEntities := pickWritableDevice(t, ws)
	before := deviceEntry(t, ws, device.ID)

	area, err := ws.AreaRegistryCreate(ctx, "Device RT Area", "mdi:floor-plan", "")
	if err != nil {
		t.Fatalf("creating area: %v", err)
	}
	label, err := ws.LabelRegistryCreate(ctx, "Device RT Label", "red", "mdi:tag", "")
	if err != nil {
		t.Fatalf("creating label: %v", err)
	}
	t.Cleanup(func() {
		var restoreArea any
		if before.AreaID != "" {
			restoreArea = before.AreaID
		}
		restoreLabels := before.Labels
		if restoreLabels == nil {
			restoreLabels = []string{}
		}
		_ = ws.DeviceRegistryUpdate(ctx, device.ID, map[string]any{
			"area_id": restoreArea, "labels": restoreLabels,
		})
		_ = ws.AreaRegistryDelete(ctx, area.AreaID)
		_ = ws.LabelRegistryDelete(ctx, label.LabelID)
	})

	// --- H-12 clause 3': the dry runs changed nothing, checked independently ---
	runHactlDir(t, inst.Dir(), "device", "set-area", device.ID, area.AreaID)
	if got := deviceEntry(t, ws, device.ID); got.AreaID == area.AreaID {
		t.Fatalf("set-area dry-run wrote area %s to HA", area.AreaID)
	}
	runHactlDir(t, inst.Dir(), "device", "set-label", device.ID, label.LabelID)
	for _, l := range deviceEntry(t, ws, device.ID).Labels {
		if l == label.LabelID {
			t.Fatalf("set-label dry-run wrote label %s to HA", label.LabelID)
		}
	}

	// --- confirmed set-area, area resolved by NAME (as the manual documents) ---
	runHactlDir(t, inst.Dir(), "device", "set-area", device.ID, area.Name, "--confirm")
	after := deviceEntry(t, ws, device.ID)
	if after.AreaID != area.AreaID {
		t.Fatalf("set-area did not reach HA: area_id is %q, want %q", after.AreaID, area.AreaID)
	}
	assertDeviceWitnessUnchanged(t, before, after, "area_id")

	// H-8, asked of HA itself: the device's own-area-less entities now answer
	// to the new area via inheritance.
	for _, entityID := range inheritingEntities {
		if !areaContains(t, inst, area.AreaID, entityID) {
			t.Errorf("HA's area_entities(%s) does not list %s after placing its device there (H-8)",
				area.AreaID, entityID)
		}
	}

	// --- confirmed set-label, label resolved by NAME; merge semantics ---
	runHactlDir(t, inst.Dir(), "device", "set-label", device.ID, label.Name, "--confirm")
	after2 := deviceEntry(t, ws, device.ID)
	got := map[string]bool{}
	for _, l := range after2.Labels {
		got[l] = true
	}
	if !got[label.LabelID] {
		t.Fatalf("set-label did not reach HA: %v lacks %s", after2.Labels, label.LabelID)
	}
	for _, l := range before.Labels {
		if !got[l] {
			t.Errorf("set-label dropped pre-existing label %s — the manual promises a merge", l)
		}
	}
	assertDeviceWitnessUnchanged(t, after, after2, "labels")

	// --- device resolvable by NAME too (H-17), observed on a second write ---
	if name := after2.Name; name != "" {
		runHactlDir(t, inst.Dir(), "device", "set-area", name, area.Name, "--confirm")
		if got := deviceEntry(t, ws, device.ID); got.AreaID != area.AreaID {
			t.Errorf("set-area by device name did not resolve to %s", device.ID)
		}
	}

	// --- restore, and assert the restore ---
	var restoreArea any
	if before.AreaID != "" {
		restoreArea = before.AreaID
	}
	restoreLabels := before.Labels
	if restoreLabels == nil {
		restoreLabels = []string{}
	}
	if err := ws.DeviceRegistryUpdate(ctx, device.ID, map[string]any{
		"area_id": restoreArea, "labels": restoreLabels,
	}); err != nil {
		t.Fatalf("restoring device: %v", err)
	}
	restored := deviceEntry(t, ws, device.ID)
	if restored.AreaID != before.AreaID {
		t.Errorf("restore left area_id %q, want %q", restored.AreaID, before.AreaID)
	}
}
