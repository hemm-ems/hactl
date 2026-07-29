//go:build integration

package integration

import (
	"context"
	"testing"
)

// TestOracleEntityRegistryListCarriesIdentityFields — the wire truth issue
// #110 rests on: does `config/entity_registry/list` itself carry `platform`,
// `unique_id` and `config_entry_id`, or are those get-only? hactl's
// EntityRegistryEntry has decoded Platform and UniqueID for a while, but a
// field the wire never sends decodes silently to "" and proves nothing —
// orphan.go already consumes UniqueID from this very call on that unprobed
// assumption. The fork: if list omits them, `ent show` must switch to a
// per-entity `config/entity_registry/get` instead (different code).
//
// The oracle rig is the probe surface because its `demo:` integration owns
// devices, and HA only mints devices for config entries — so device-backed
// entries MUST have a config_entry_id server-side, and its absence in the
// list payload is then a fact about the wire, not about the fixture.
func TestOracleEntityRegistryListCarriesIdentityFields(t *testing.T) {
	inst, _ := getOracleHA(t)
	ws := oracleWS(t, inst)
	ctx := context.Background()

	entries, err := ws.EntityRegistryList(ctx)
	if err != nil {
		t.Fatalf("entity registry list: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("registry is empty — this oracle proves nothing")
	}

	var deviceBacked, withPlatform, withUnique, withConfigEntry int
	for _, e := range entries {
		if e.DeviceID == "" {
			continue
		}
		deviceBacked++
		if e.Platform != "" {
			withPlatform++
		}
		if e.UniqueID != "" {
			withUnique++
		}
		if e.ConfigEntryID != "" {
			withConfigEntry++
		}
	}
	if deviceBacked == 0 {
		t.Fatal("no device-backed registry entries — the config_entry_id half of this oracle proves nothing")
	}
	if withPlatform != deviceBacked {
		t.Errorf("platform carried by %d of %d device-backed list entries — a device-backed entity always has an owning platform, so the list payload is dropping it", withPlatform, deviceBacked)
	}
	if withUnique != deviceBacked {
		t.Errorf("unique_id carried by %d of %d device-backed list entries — platform entities always have one, so the list payload omits it and every consumer of UniqueID from this call reads \"\" (orphan.go does today)", withUnique, deviceBacked)
	}
	if withConfigEntry != deviceBacked {
		t.Errorf("config_entry_id carried by %d of %d device-backed list entries — devices only exist for config entries, so the list payload omits it and ent show must read config/entity_registry/get instead", withConfigEntry, deviceBacked)
	}
}
