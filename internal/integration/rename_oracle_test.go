//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// TestOracleEntityRenameSemantics probes the four HA behaviours issue #113's
// `ent rename` forks on, before any of it is built:
//
//  1. the premise — config/entity_registry/update with new_entity_id renames;
//  2. collision — renaming onto an existing entity_id must ERROR server-side
//     (if HA silently accepted it, hactl's local pre-check would be the only
//     thing standing between a race and a clobbered entity, and the fix would
//     need a different posture);
//  3. old-id disposition — is the old id gone from the registry immediately
//     (decides whether the H-12 round-trip needs ghost-retry read-backs);
//  4. non-registry (state-only) entity — the update must ERROR rather than
//     silently no-op (if it no-ops, resolving against the registry in the dry
//     run is correctness, not just courtesy).
//
// Cross-domain renames are deliberately NOT probed as a blocker: whichever
// way HA answers, hactl passes the server's verdict through (the same
// documented posture as helper delete's confirm-time 409), so a different
// answer changes neither code nor test — the D-8 discriminator.
func TestOracleEntityRenameSemantics(t *testing.T) {
	inst := getWriteHA(t)
	ws := writeWS(t, inst)
	ctx := context.Background()

	target := pickRegisteredEntity(t, ws)
	domain := strings.SplitN(target.EntityID, ".", 2)[0]
	renamed := domain + ".hactl_rename_oracle_tmp"

	// (1) the premise: rename works at all.
	if err := ws.EntityRegistryUpdate(ctx, target.EntityID, map[string]any{"new_entity_id": renamed}); err != nil {
		t.Fatalf("rename %s -> %s refused: %v — the registry-rename premise of #113 fails", target.EntityID, renamed, err)
	}
	// Whatever else happens, put the id back.
	t.Cleanup(func() {
		_ = ws.EntityRegistryUpdate(ctx, renamed, map[string]any{"new_entity_id": target.EntityID})
	})

	// (3) old-id disposition, read straight from the registry.
	entries, err := ws.EntityRegistryList(ctx)
	if err != nil {
		t.Fatalf("entity registry list: %v", err)
	}
	var oldPresent, newPresent bool
	var other haapi.EntityRegistryEntry
	for _, e := range entries {
		switch e.EntityID {
		case target.EntityID:
			oldPresent = true
		case renamed:
			newPresent = true
		default:
			if other.EntityID == "" && e.DisabledBy == "" && e.UniqueID != "" && e.EntityID != renamed {
				other = e
			}
		}
	}
	if !newPresent {
		t.Fatalf("renamed id %s not in the registry after the update", renamed)
	}
	if oldPresent {
		t.Errorf("old id %s STILL in the registry after the rename — the fix must treat rename as copy+ghost, not move", target.EntityID)
	}

	// (2) collision: renaming onto an existing id errors server-side.
	if other.EntityID == "" {
		t.Fatal("no second registry entity to probe the collision with")
	}
	err = ws.EntityRegistryUpdate(ctx, renamed, map[string]any{"new_entity_id": other.EntityID})
	if err == nil {
		t.Fatalf("HA accepted a rename onto existing %s — hactl's pre-check would be the only guard; the fix must not rely on the server refusing", other.EntityID)
	}
	t.Logf("collision error shape (for the CLI's pass-through wording): %v", err)

	// (4) a state-only entity cannot be renamed — error, not silent no-op.
	stateOnly := "sensor.hactl_rename_oracle_stateonly"
	if code, body := ddRequest(t, inst, "POST", "/api/states/"+stateOnly, map[string]any{
		"state": "42",
	}); code != 200 && code != 201 {
		t.Fatalf("seeding state-only entity: %d %s", code, body)
	}
	err = ws.EntityRegistryUpdate(ctx, stateOnly, map[string]any{"new_entity_id": "sensor.hactl_rename_oracle_stateonly_2"})
	if err == nil {
		t.Fatalf("HA silently accepted a registry update for state-only %s — the dry run MUST resolve against the registry, and the confirm path cannot trust the server to refuse", stateOnly)
	}
	t.Logf("state-only error shape: %v", err)

	// Rename back NOW (not only in cleanup) and assert the restore, so later
	// tests in this package see the rig exactly as they expect it.
	if err := ws.EntityRegistryUpdate(ctx, renamed, map[string]any{"new_entity_id": target.EntityID}); err != nil {
		t.Fatalf("renaming back: %v", err)
	}
	if got := registryEntry(t, ws, target.EntityID); got.UniqueID != target.UniqueID {
		t.Errorf("restore mismatch: unique_id %q, want %q", got.UniqueID, target.UniqueID)
	}
}
