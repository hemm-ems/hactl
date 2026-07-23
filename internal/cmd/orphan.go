package cmd

import (
	"context"
	"log/slog"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// removeOrphanedEntity deletes entityID's entity registry entry after its
// definition has been removed from the config files.
//
// Deleting the YAML is only half a delete. HA keeps the registry entry for
// every entity that ever had a unique_id, so once the definition is gone the
// entity stays listed with `state: unavailable` and `restored: true` — a ghost
// that `ent ls` still shows and that silently re-adopts the id if something
// with the same unique_id is created later. `auto delete` has always cleaned
// this up; `script delete` and `tpl delete` did not, so the same operation
// left a different amount of debris depending on which family it ran against.
//
// Best-effort by design: the definition is already gone, so failing the whole
// command over the cleanup would report a failure for a delete that did in
// fact happen. Failures are logged, not returned.
func removeOrphanedEntity(ctx context.Context, cfg *config.Config, entityID string) {
	if entityID == "" {
		return
	}
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		slog.Warn("could not connect to HA to clean up entity registry", "entity_id", entityID, "error", err)
		return
	}
	defer func() { _ = ws.Close() }()
	if err := ws.EntityRegistryRemove(ctx, entityID); err != nil {
		slog.Warn("could not remove orphaned entity registry entry", "entity_id", entityID, "error", err)
	}
}

// registeredEntityID reports entityID if HA's entity registry holds it, and ""
// otherwise. Resolving before the delete matters: afterwards the entry is
// indistinguishable from a ghost left by an earlier delete.
func registeredEntityID(ctx context.Context, cfg *config.Config, entityID string) string {
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		slog.Debug("could not read entity registry", "error", err)
		return ""
	}
	defer func() { _ = ws.Close() }()
	entries, err := ws.EntityRegistryList(ctx)
	if err != nil {
		slog.Debug("could not list entity registry", "error", err)
		return ""
	}
	if _, ok := findEntityRegistryEntry(entries, entityID); ok {
		return entityID
	}
	return ""
}

// templateEntityIDs returns the entity_ids HA registered for a template
// unique_id. A template entry is addressed by unique_id, not entity_id, and
// HA derives the entity_id from the entry's `name`, so the only reliable map
// from one to the other is the registry itself (platform "template").
func templateEntityIDs(ctx context.Context, cfg *config.Config, uniqueID string) []string {
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		slog.Debug("could not read entity registry", "error", err)
		return nil
	}
	defer func() { _ = ws.Close() }()
	entries, err := ws.EntityRegistryList(ctx)
	if err != nil {
		slog.Debug("could not list entity registry", "error", err)
		return nil
	}
	var out []string
	for _, e := range entries {
		if e.Platform == "template" && e.UniqueID == uniqueID {
			out = append(out, e.EntityID)
		}
	}
	return out
}
