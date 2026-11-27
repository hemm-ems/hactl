package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// `--dead <dur>` answers "which entities had no valid state for <dur>" from
// what hactl already holds, and asks the recorder only about the rest, in
// bounded batches (#198, D-238). It never asks an open-ended question: #195
// showed what one costs on a real house.

// deadWindowCap is the longest window --dead accepts. Past it a flapping
// entity's history is unbounded in rows, and HA's default recorder keeps 10
// days anyway, so a longer window could only answer `partly_recorded` for the
// entities the recorder is asked about.
const deadWindowCap = 30 * 24 * time.Hour

// deadHistoryBatchBytes bounds one history request's filter_entity_id list.
// HA 2026.11 accepted a 16 KB query in TestOracleDeadHistoryShape; 4 KB keeps
// each recorder query to about a hundred ids.
const deadHistoryBatchBytes = 4096

// Verdict reasons. The first three are decided (dead); the last two are not,
// and are listed so that "not dead" is never inferred from a missing answer.
const (
	deadGhost       = "ghost"           // restored: no live entity behind the state
	deadUnchanged   = "unchanged"       // invalid, and unchanged since before the window
	deadNoValid     = "no_valid_state"  // the recorder covers the window; every row invalid
	deadUnrecorded  = "unrecorded"      // the recorder has no rows (excluded, purged, never recorded)
	deadPartlyKnown = "partly_recorded" // rows start after the window opens; what came before is unknown
)

// timestampStateDomains are the domains whose state is the time the entity
// was last used, `unknown` until it first is: a never-pressed button is
// healthy, not dead. Read from HA 2026.11.2's entity base classes (each
// `state` returns a `__last_*` timestamp or None); input_button inherits
// ButtonEntity.
var timestampStateDomains = map[string]bool{
	"ai_task": true, "button": true, "conversation": true, "event": true,
	"image": true, "infrared": true, "input_button": true, "notify": true,
	"radio_frequency": true, "scene": true, "stt": true, "tts": true,
	"wake_word": true,
}

// validState reports whether state is a valid state for entityID: anything
// but unavailable, and anything but unknown outside the timestamp domains.
func validState(entityID, state string) bool {
	switch state {
	case "unavailable":
		return false
	case "unknown":
		return timestampStateDomains[haapi.EntityIDDomain(entityID)]
	}
	return true
}

// deadVerdict is one entity's answer under --dead. dead is false for an
// undecided entity; alive entities get no verdict at all.
type deadVerdict struct {
	reason string
	dead   bool
}

// deadFromWhatWeHold decides every entity it can without the recorder and
// returns the ids it cannot, in input order.
//
// A valid current state is alive for any window. A restored state is a ghost
// (#54). An invalid state whose last_changed is older than the window start
// has been that state, continuously, since then: last_changed resets on every
// restart (HA core.py), so an OLD last_changed is conclusive.
func deadFromWhatWeHold(states []entityState, windowStart time.Time) (map[string]deadVerdict, []string) {
	verdicts := map[string]deadVerdict{}
	var ask []string
	for _, s := range states {
		switch {
		case isRestoredAttr(s.Attributes):
			verdicts[s.EntityID] = deadVerdict{reason: deadGhost, dead: true}
		case validState(s.EntityID, s.State):
			// alive
		default:
			changed, err := time.Parse(time.RFC3339Nano, s.LastChanged)
			if err == nil && !changed.After(windowStart) {
				verdicts[s.EntityID] = deadVerdict{reason: deadUnchanged, dead: true}
				continue
			}
			ask = append(ask, s.EntityID)
		}
	}
	return verdicts, ask
}

// judgeHistory decides one entity from its recorder rows for the window.
// alive is true when any row is valid. HA stamps the start-time state with
// the window start itself (TestOracleDeadHistoryShape), so a first row later
// than that means the recorder does not reach back to the window start.
func judgeHistory(entityID string, rows []haapi.MinimalHistoryRow, windowStart time.Time) (deadVerdict, bool) {
	if len(rows) == 0 {
		return deadVerdict{reason: deadUnrecorded}, false
	}
	for _, r := range rows {
		if validState(entityID, r.State) {
			return deadVerdict{}, true
		}
	}
	first, err := time.Parse(time.RFC3339Nano, rows[0].LastChanged)
	if err != nil || first.After(windowStart.Add(time.Millisecond)) {
		return deadVerdict{reason: deadPartlyKnown}, false
	}
	return deadVerdict{reason: deadNoValid, dead: true}, false
}

// historyBatches splits ids into lists whose comma-joined length stays
// within deadHistoryBatchBytes. An id longer than the budget gets a batch of
// its own rather than being dropped.
func historyBatches(ids []string) [][]string {
	var out [][]string
	cur := make([]string, 0, len(ids))
	size := 0
	for _, id := range ids {
		add := len(id)
		if len(cur) > 0 {
			add++ // the comma
		}
		if len(cur) > 0 && size+add > deadHistoryBatchBytes {
			out = append(out, cur)
			cur, size = make([]string, 0, len(ids)), 0
			add = len(id)
		}
		cur = append(cur, id)
		size += add
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// deadHistory is the recorder half of --dead: one request per batch,
// sequential, stopping at the first failure — a partial answer would leave
// some entities silently undecided.
type deadHistory interface {
	GetHistoryMinimal(ctx context.Context, start, end string, entityIDs []string) (map[string][]haapi.MinimalHistoryRow, error)
}

// judgeDead returns the verdict of every entity that is dead or undecided
// over the window ending at now. Alive entities are absent.
func judgeDead(ctx context.Context, client deadHistory, states []entityState, now time.Time, window time.Duration) (map[string]deadVerdict, error) {
	windowStart := now.Add(-window)
	verdicts, ask := deadFromWhatWeHold(states, windowStart)
	start, end := windowStart.UTC().Format(time.RFC3339Nano), now.UTC().Format(time.RFC3339Nano)
	batches := historyBatches(ask)
	for i, batch := range batches {
		answer, err := client.GetHistoryMinimal(ctx, start, end, batch)
		if err != nil {
			return nil, fmt.Errorf("--dead: recorder history, batch %d of %d: %w", i+1, len(batches), err)
		}
		for _, id := range batch {
			if v, alive := judgeHistory(id, answer[id], windowStart); !alive {
				verdicts[id] = v
			}
		}
	}
	return verdicts, nil
}

// parseDeadWindow reads the --dead value: a window like --since's, capped.
func parseDeadWindow(raw string) (time.Duration, error) {
	d, err := parseSinceRaw(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("--dead %q: %w", raw, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("--dead %q: the window must be positive", raw)
	}
	if d > deadWindowCap {
		return 0, fmt.Errorf("--dead %q: longer than %s, which is the most --dead asks the recorder about", raw, "30d")
	}
	return d, nil
}

// Device verdict reasons (D-238).
const (
	deadAllEntities   = "all_entities_dead"  // every enabled entity with a state is dead
	deadNoStateful    = "no_stateful_entity" // nothing to judge it by: disabled or stateless entities only
	deadPartlyDecided = "entities_undecided" // none alive, some undecided
)

// judgeDevice derives a device's verdict from its entities' (D-238): alive
// when any enabled entity with a state is alive; dead when every one is
// dead; undecided otherwise. hasState says whether an entity has a state at
// all (a disabled entity has none and says nothing about the device).
func judgeDevice(entities []haapi.EntityRegistryEntry, hasState map[string]bool, verdicts map[string]deadVerdict) (deadVerdict, bool) {
	judged, dead := 0, 0
	for _, e := range entities {
		if e.DisabledBy != "" || !hasState[e.EntityID] {
			continue
		}
		judged++
		v, decided := verdicts[e.EntityID]
		if !decided {
			return deadVerdict{}, true // no verdict means alive
		}
		if v.dead {
			dead++
		}
	}
	switch {
	case judged == 0:
		return deadVerdict{reason: deadNoStateful}, false
	case dead == judged:
		return deadVerdict{reason: deadAllEntities, dead: true}, false
	default:
		return deadVerdict{reason: deadPartlyDecided}, false
	}
}

// filterEntitiesByDead keeps the entities that are dead or undecided over
// the ent ls --dead window, in order, and returns their verdicts.
func filterEntitiesByDead(ctx context.Context, client deadHistory, states []entityState) ([]entityState, map[string]deadVerdict, error) {
	verdicts, err := judgeDead(ctx, client, states, time.Now(), flagEntDead.d)
	if err != nil {
		return nil, nil, err
	}
	kept := states[:0]
	for _, s := range states {
		if _, ok := verdicts[s.EntityID]; ok {
			kept = append(kept, s)
		}
	}
	return kept, verdicts, nil
}

// filterDevicesByDead keeps the devices that are dead or undecided over the
// --dead window and returns their verdicts. It reads the states once and asks
// the recorder only about the listed devices' entities (D-238).
func filterDevicesByDead(ctx context.Context, cfg *config.Config, rc *deviceRegistryContext, devices []haapi.DeviceRegistryEntry) ([]haapi.DeviceRegistryEntry, map[string]deadVerdict, error) {
	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.GetStates(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("--dead: fetching states: %w", err)
	}
	var all []entityState
	if err = json.Unmarshal(data, &all); err != nil {
		return nil, nil, fmt.Errorf("--dead: parsing states: %w", err)
	}
	if err = degeneracy.Check("/api/states", &all); err != nil {
		return nil, nil, err
	}
	byID := make(map[string]entityState, len(all))
	for _, s := range all {
		byID[s.EntityID] = s
	}
	hasState := map[string]bool{}
	var judged []entityState
	for _, d := range devices {
		for _, e := range rc.entityByID[d.ID] {
			if s, ok := byID[e.EntityID]; ok && e.DisabledBy == "" {
				hasState[e.EntityID] = true
				judged = append(judged, s)
			}
		}
	}
	entityVerdicts, err := judgeDead(ctx, client, judged, time.Now(), flagDeviceDead.d)
	if err != nil {
		return nil, nil, err
	}
	verdicts := map[string]deadVerdict{}
	kept := devices[:0]
	for _, d := range devices {
		if v, alive := judgeDevice(rc.entityByID[d.ID], hasState, entityVerdicts); !alive {
			verdicts[d.ID] = v
			kept = append(kept, d)
		}
	}
	return kept, verdicts, nil
}
