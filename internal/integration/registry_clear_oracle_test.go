//go:build integration

package integration

import (
	"context"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// ============================================================================
// ORACLE — finding #81, H-27, docs/decisions.md D-44.
//
// `ent`/`device set-area --clear` and `set-label --remove` (internal/cmd/ent.go,
// internal/cmd/device.go) send `area_id: nil` and, when a removal empties the
// set, `labels: []` over the same config/entity_registry|device_registry
// `update` WS commands the existing merge path already uses. That behaviour
// was read off Home Assistant core's DEV BRANCH source
// (homeassistant/components/config/entity_registry.py,
// WS_CONFIG_SCHEMA `vol.Optional("area_id"): vol.Any(str, None)`, and
// entity_registry.async_update_entity treating an explicit None as "clear the
// area"), which is not proof for 2026.7.4 (the reference instance) or `stable`
// (the rig image) — AGENTS.md step 1's "probe, don't assume".
//
// These tests ask Home Assistant directly, bypassing hactl's CLI so nothing
// about the answer travels through the code under test (H-12's discipline,
// applied to a question rather than to a regression).
//
// RESULT, 2026-08-01: both pass against the `stable` image, which is 2026.7.4 —
// the same version the reference instance runs, so the answer covers both the
// rig and live profiles rather than only the rig. Both registries accept
// `area_id: null` and `labels: []` and read the value back cleared. The four
// oracle markers this file existed to resolve are gone.
//
// The label test asserts its own setup took before it clears: without that, an
// empty read-back would pass whether or not the clear did anything, and this
// file had never been watched fail.
// ============================================================================

// pickAnyDevice returns any device HA's registry holds, unlike
// pickWritableDevice in write_device_test.go, which additionally requires one
// with own-area-less entities for the H-8 inheritance assertions that test
// makes and this one does not need.
func pickAnyDevice(t *testing.T, ws *haapi.WSClient) haapi.DeviceRegistryEntry {
	t.Helper()
	devices, err := ws.DeviceRegistryList(context.Background())
	if err != nil {
		t.Fatalf("listing device registry: %v", err)
	}
	if len(devices) == 0 {
		t.Fatal("no device in the fixture to probe against")
	}
	return devices[0]
}

// TestClearWiresNullAreaID asks whether `area_id: null` clears an entity's (and
// a device's) area and reads back empty, which is what clearAreaWireValue
// assumes for `ent`/`device set-area --clear`.
func TestClearWiresNullAreaID(t *testing.T) {
	t.Run("entity", func(t *testing.T) {
		inst := getWriteHA(t)
		ws := writeWS(t, inst)
		ctx := context.Background()

		target := pickRegisteredEntity(t, ws)
		before := registryEntry(t, ws, target.EntityID)

		area, err := ws.AreaRegistryCreate(ctx, "Oracle Clear Area", "mdi:floor-plan", "")
		if err != nil {
			t.Fatalf("creating area: %v", err)
		}
		t.Cleanup(func() {
			var restore any
			if before.AreaID != "" {
				restore = before.AreaID
			}
			_ = ws.EntityRegistryUpdate(ctx, target.EntityID, map[string]any{"area_id": restore})
			_ = ws.AreaRegistryDelete(ctx, area.AreaID)
		})

		// Setup: give the entity a real area, so clearing it is an observable
		// change rather than a no-op that happened to already hold true.
		if err := ws.EntityRegistryUpdate(ctx, target.EntityID, map[string]any{"area_id": area.AreaID}); err != nil {
			t.Fatalf("setting up a real area before clearing it: %v", err)
		}
		if got := registryEntry(t, ws, target.EntityID).AreaID; got != area.AreaID {
			t.Fatalf("setup did not take: area_id = %q, want %q", got, area.AreaID)
		}

		// The question.
		if err := ws.EntityRegistryUpdate(ctx, target.EntityID, map[string]any{"area_id": nil}); err != nil {
			t.Fatalf("HA REJECTED area_id: null: %v — clearAreaWireValue's premise is FALSE for this "+
				"version; `ent set-area --clear` needs a different mechanism", err)
		}
		if got := registryEntry(t, ws, target.EntityID).AreaID; got != "" {
			t.Fatalf("area_id: null did NOT clear the area — read back as %q, want \"\". "+
				"clearAreaWireValue's premise is FALSE for this version", got)
		}
	})

	t.Run("device", func(t *testing.T) {
		inst := getDeviceWriteHA(t)
		ws := oracleWS(t, inst)
		ctx := context.Background()

		device := pickAnyDevice(t, ws)
		before := deviceEntry(t, ws, device.ID)

		area, err := ws.AreaRegistryCreate(ctx, "Oracle Clear Device Area", "mdi:floor-plan", "")
		if err != nil {
			t.Fatalf("creating area: %v", err)
		}
		t.Cleanup(func() {
			var restore any
			if before.AreaID != "" {
				restore = before.AreaID
			}
			_ = ws.DeviceRegistryUpdate(ctx, device.ID, map[string]any{"area_id": restore})
			_ = ws.AreaRegistryDelete(ctx, area.AreaID)
		})

		if err := ws.DeviceRegistryUpdate(ctx, device.ID, map[string]any{"area_id": area.AreaID}); err != nil {
			t.Fatalf("setting up a real area before clearing it: %v", err)
		}
		if got := deviceEntry(t, ws, device.ID).AreaID; got != area.AreaID {
			t.Fatalf("setup did not take: area_id = %q, want %q", got, area.AreaID)
		}

		if err := ws.DeviceRegistryUpdate(ctx, device.ID, map[string]any{"area_id": nil}); err != nil {
			t.Fatalf("HA REJECTED area_id: null on the device registry: %v — clearAreaWireValue's "+
				"premise is FALSE for this version; `device set-area --clear` needs a different mechanism", err)
		}
		if got := deviceEntry(t, ws, device.ID).AreaID; got != "" {
			t.Fatalf("device area_id: null did NOT clear the area — read back as %q, want \"\"", got)
		}
	})
}

// TestClearWiresEmptyLabels asks whether `labels: []` clears an entity's (and a
// device's) label set and reads back empty, which is what runEntSetLabel and
// runDeviceSetLabel assume when a --remove empties the set.
func TestClearWiresEmptyLabels(t *testing.T) {
	t.Run("entity", func(t *testing.T) {
		inst := getWriteHA(t)
		ws := writeWS(t, inst)
		ctx := context.Background()

		target := pickRegisteredEntity(t, ws)
		before := registryEntry(t, ws, target.EntityID)

		label, err := ws.LabelRegistryCreate(ctx, "Oracle Clear Label", "red", "mdi:tag", "")
		if err != nil {
			t.Fatalf("creating label: %v", err)
		}
		t.Cleanup(func() {
			restore := before.Labels
			if restore == nil {
				restore = []string{}
			}
			_ = ws.EntityRegistryUpdate(ctx, target.EntityID, map[string]any{"labels": restore})
			_ = ws.LabelRegistryDelete(ctx, label.LabelID)
		})

		if err := ws.EntityRegistryUpdate(ctx, target.EntityID, map[string]any{"labels": []string{label.LabelID}}); err != nil {
			t.Fatalf("setting up a real label before clearing it: %v", err)
		}
		if got := registryEntry(t, ws, target.EntityID).Labels; len(got) != 1 || got[0] != label.LabelID {
			t.Fatalf("setup did not take: labels = %v, want [%s]", got, label.LabelID)
		}

		// The question.
		if err := ws.EntityRegistryUpdate(ctx, target.EntityID, map[string]any{"labels": []string{}}); err != nil {
			t.Fatalf("HA REJECTED labels: []: %v — the empty-list write runEntSetLabel sends when a "+
				"--remove empties the set is FALSE for this version", err)
		}
		if got := registryEntry(t, ws, target.EntityID).Labels; len(got) != 0 {
			t.Fatalf("labels: [] did NOT clear the label set — read back as %v, want none. "+
				"runEntSetLabel's empty-list premise is FALSE for this version", got)
		}
	})

	t.Run("device", func(t *testing.T) {
		inst := getDeviceWriteHA(t)
		ws := oracleWS(t, inst)
		ctx := context.Background()

		device := pickAnyDevice(t, ws)
		before := deviceEntry(t, ws, device.ID)

		label, err := ws.LabelRegistryCreate(ctx, "Oracle Clear Device Label", "red", "mdi:tag", "")
		if err != nil {
			t.Fatalf("creating label: %v", err)
		}
		t.Cleanup(func() {
			restore := before.Labels
			if restore == nil {
				restore = []string{}
			}
			_ = ws.DeviceRegistryUpdate(ctx, device.ID, map[string]any{"labels": restore})
			_ = ws.LabelRegistryDelete(ctx, label.LabelID)
		})

		if err := ws.DeviceRegistryUpdate(ctx, device.ID, map[string]any{"labels": []string{label.LabelID}}); err != nil {
			t.Fatalf("setting up a real label before clearing it: %v", err)
		}
		if got := deviceEntry(t, ws, device.ID).Labels; len(got) != 1 || got[0] != label.LabelID {
			t.Fatalf("setup did not take: labels = %v, want [%s]", got, label.LabelID)
		}

		if err := ws.DeviceRegistryUpdate(ctx, device.ID, map[string]any{"labels": []string{}}); err != nil {
			t.Fatalf("HA REJECTED labels: [] on the device registry: %v — runDeviceSetLabel's "+
				"empty-list premise is FALSE for this version", err)
		}
		if got := deviceEntry(t, ws, device.ID).Labels; len(got) != 0 {
			t.Fatalf("device labels: [] did NOT clear the label set — read back as %v, want none", got)
		}
	})
}
