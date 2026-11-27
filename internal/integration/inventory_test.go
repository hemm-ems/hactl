//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// registrySnapshot reads both registries straight from HA, so the
// assertions below compare hactl against HA's own answer, not against
// hactl's model of it (H-9).
func registrySnapshot(t *testing.T) ([]haapi.EntityRegistryEntry, map[string]haapi.DeviceRegistryEntry, map[string][]string) {
	t.Helper()
	cfg := loadConfig(t)
	ctx := context.Background()
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("ws: %v", err)
	}
	defer func() { _ = ws.Close() }()
	ents, err := ws.EntityRegistryList(ctx)
	if err != nil {
		t.Fatalf("entity registry: %v", err)
	}
	devs, err := ws.DeviceRegistryList(ctx)
	if err != nil {
		t.Fatalf("device registry: %v", err)
	}
	byID := make(map[string]haapi.DeviceRegistryEntry, len(devs))
	for _, d := range devs {
		byID[d.ID] = d
	}
	devEntries, complete, err := ws.DeviceConfigEntries(ctx)
	if err != nil || !complete {
		t.Fatalf("device config entries: complete=%v err=%v", complete, err)
	}
	return ents, byID, devEntries
}

// TestEntLsOwnershipMatchesTheRegistry: every row's platform, device_id and
// config_entry_id is what HA's entity registry says, and device is that
// device's user-facing name (D-236).
func TestEntLsOwnershipMatchesTheRegistry(t *testing.T) {
	ents, devs, _ := registrySnapshot(t)
	var rows []map[string]any
	if err := json.Unmarshal([]byte(runHactl(t, "ent", "ls", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	byEntity := map[string]map[string]any{}
	for _, r := range rows {
		id, _ := r["entity_id"].(string)
		byEntity[id] = r
	}

	withDevice, withEntry := 0, 0
	for _, e := range ents {
		r, ok := byEntity[e.EntityID]
		if !ok {
			continue // registered but no state (disabled): ent ls lists states
		}
		wantDevice, wantDeviceID := "", ""
		if d, ok := devs[e.DeviceID]; ok && e.DeviceID != "" {
			wantDevice, wantDeviceID = firstNonEmptyStr(d.NameByUser, d.Name, d.ID), d.ID
			withDevice++
		}
		if e.ConfigEntryID != "" {
			withEntry++
		}
		for key, want := range map[string]string{
			"platform": e.Platform, "device": wantDevice,
			"device_id": wantDeviceID, "config_entry_id": e.ConfigEntryID,
		} {
			if r[key] != want {
				t.Errorf("%s %s = %#v, HA's registry says %q", e.EntityID, key, r[key], want)
			}
		}
	}
	if withDevice == 0 || withEntry == 0 {
		t.Fatalf("fixture exercises nothing: %d listed entities with a device, %d with a config entry", withDevice, withEntry)
	}

	// --platform keeps exactly the registry's entities of that platform.
	platform := ""
	for _, e := range ents {
		if e.DeviceID != "" && byEntity[e.EntityID] != nil {
			platform = e.Platform
			break
		}
	}
	var narrowed []map[string]any
	if err := json.Unmarshal([]byte(runHactl(t, "ent", "ls", "--platform", platform, "--json")), &narrowed); err != nil {
		t.Fatal(err)
	}
	want := 0
	for _, e := range ents {
		if e.Platform == platform && byEntity[e.EntityID] != nil {
			want++
		}
	}
	if len(narrowed) != want {
		t.Errorf("--platform %s kept %d rows, HA's registry has %d listed entities of it", platform, len(narrowed), want)
	}
}

// TestConfigEntriesCountsMatchTheRegistry: the counts are HA's registry rows
// naming each entry (D-237).
func TestConfigEntriesCountsMatchTheRegistry(t *testing.T) {
	ents, _, devEntries := registrySnapshot(t)
	wantEnts, wantDevs := map[string]float64{}, map[string]float64{}
	for _, e := range ents {
		if e.ConfigEntryID != "" {
			wantEnts[e.ConfigEntryID]++
		}
	}
	for _, ids := range devEntries {
		for _, id := range ids {
			wantDevs[id]++
		}
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(runHactl(t, "config", "entries", "--json")), &rows); err != nil {
		t.Fatal(err)
	}
	nonzero := 0
	for _, r := range rows {
		id, _ := r["entry_id"].(string)
		if r["entities"] != wantEnts[id] || r["devices"] != wantDevs[id] {
			t.Errorf("%s: entities=%v devices=%v, HA's registries say %v/%v", id, r["entities"], r["devices"], wantEnts[id], wantDevs[id])
		}
		if wantEnts[id] > 0 {
			nonzero++
		}
	}
	if nonzero == 0 {
		t.Fatal("fixture exercises nothing: no config entry owns an entity")
	}
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
