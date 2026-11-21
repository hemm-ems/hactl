//go:build integration

package integration

import (
	"encoding/json"
	"testing"
)

// ============================================================================
// W-1 (SPEC-policy-usp.md, decision D-137) checks what an automation's actions would switch.
// A device action carries no `action:`; it names its target in `entity_id`.
// Which identifier is that — the entity_id, or the entity registry's uuid?
// The check must resolve exactly what HA resolves, so this asks HA twice:
//
//  1. what HA itself offers for a device's actions (device_automation/action/list);
//  2. which forms HA accepts when validating an automation (validate_config).
// ============================================================================

func TestOracleDeviceActionEntityIdentifier(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	ws := dialRawWS(t, inst.URL(), inst.Token())

	var lightID, lightUUID, deviceID string
	var rows []struct {
		ID       string `json:"id"`
		EntityID string `json:"entity_id"`
		DeviceID string `json:"device_id"`
	}
	if err := json.Unmarshal(ws.mustSucceed(t, map[string]any{"type": "config/entity_registry/list"}).Result, &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if len(r.EntityID) > 6 && r.EntityID[:6] == "light." && r.DeviceID != "" && r.ID != "" {
			lightID, lightUUID, deviceID = r.EntityID, r.ID, r.DeviceID
			break
		}
	}
	if lightID == "" {
		t.Fatal("no light with a device in the policyenforce fixture")
	}

	env := ws.mustSucceed(t, map[string]any{"type": "device_automation/action/list", "device_id": deviceID})
	var actions []map[string]any
	if err := json.Unmarshal(env.Result, &actions); err != nil {
		t.Fatal(err)
	}
	offered := ""
	for _, a := range actions {
		if a["domain"] == "light" {
			offered, _ = a["entity_id"].(string)
			break
		}
	}
	t.Logf("light %s uuid %s; HA offers entity_id=%q", lightID, lightUUID, offered)
	// Observed on HA stable 2026-11-05: the registry uuid. W-1 resolves it
	// through the entity registry; if HA ever offers the entity_id instead,
	// the resolution is still right, but this fact is what the code relies on.
	if offered != lightUUID {
		t.Errorf("device action entity_id = %q, want the registry uuid %q (the entity_id is %q)", offered, lightUUID, lightID)
	}

	for name, ref := range map[string]string{"uuid": lightUUID, "entity_id": lightID} {
		cfg := map[string]any{
			"type": "validate_config",
			"actions": []any{map[string]any{
				"device_id": deviceID, "domain": "light", "entity_id": ref, "type": "turn_on",
			}},
		}
		res := ws.mustSucceed(t, cfg)
		var v map[string]struct {
			Valid bool    `json:"valid"`
			Error *string `json:"error"`
		}
		_ = json.Unmarshal(res.Result, &v)
		// Both forms are valid, so W-1 must accept both: a uuid resolved
		// through the registry, an entity_id as it is.
		if !v["actions"].Valid {
			t.Errorf("validate_config refused the %s form: %v", name, v["actions"].Error)
		}
	}
}
