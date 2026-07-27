package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// The two sources an actor answer can come from (D-4, docs/decisions.md).
// Every rendered answer names one of them.
const (
	actorSourceLogbook      = "logbook"
	actorSourceStateContext = "state context"
)

// actorAnswer is THE answer to "who/what changed this entity" — the single
// shared resolution `ent show` and `ent who` both render (D-4). Two resolvers
// were how D70 happened: `ent show` read the state's context while `ent who`
// read the logbook, and for entities HA's logbook excludes the two commands
// gave different answers to the same question, each individually right.
type actorAnswer struct {
	// ChangedBy is the resolved label ("User Jan", "Automation: Sunset
	// Lights", "Home Assistant", ...), classified by triggerLabel either way.
	ChangedBy string
	// Source is actorSourceLogbook or actorSourceStateContext — which HA
	// source produced ChangedBy.
	Source string
	// LogbookExcluded is true when HA's logbook structurally cannot answer
	// for this entity — that is WHY the state-context fallback fired. It is
	// false for a merely quiet entity, so an empty window stays
	// distinguishable from an excluded one (H-10).
	LogbookExcluded bool
	// ExclusionReason says which exclusion rule applies, for prose output.
	// Empty unless LogbookExcluded.
	ExclusionReason string
}

// Label renders the answer with its source named, identically for both
// commands — the label is part of the contract, not decoration: without it the
// two commands' answers are two bare names that can disagree with no visible
// reason.
func (a actorAnswer) Label() string {
	if a.LogbookExcluded {
		return fmt.Sprintf("%s (source: %s; excluded from logbook — %s)",
			a.ChangedBy, a.Source, a.ExclusionReason)
	}
	return fmt.Sprintf("%s (source: %s)", a.ChangedBy, a.Source)
}

// resolveActor is the one shared actor resolution (D-4): the logbook's newest
// entry when the logbook answered, else the state's own context — with the
// logbook-exclusion rule consulted so the fallback can say WHY the logbook had
// nothing. Order matters and is deliberate:
//
//   - Real logbook entries always win, even for an entity the exclusion
//     predicate claims is excluded — HA's actual answer outranks our mirror of
//     HA's filter, so a predicate drift can only ever mislabel an empty case,
//     never override data.
//   - The state context is the fallback because it is poorer, not wrong: it
//     carries only the propagated user id, so it can name the distal human but
//     never the proximate automation/script/device (H-11's precedence needs
//     the logbook's context_event_type/context_name fields).
func resolveActor(entries []logbookEntry, st entityState, users map[string]haapi.UserEntry) actorAnswer {
	if len(entries) > 0 {
		return actorAnswer{
			ChangedBy: triggerLabel(newestLogbookEntry(entries), users),
			Source:    actorSourceLogbook,
		}
	}
	reason := logbookExclusionReason(st.EntityID, st.Attributes)
	return actorAnswer{
		ChangedBy:       triggerLabel(logbookEntry{ContextUserID: st.Context.UserID}, users),
		Source:          actorSourceStateContext,
		LogbookExcluded: reason != "",
		ExclusionReason: reason,
	}
}

// newestLogbookEntry picks the entry with the latest `when`, not a positional
// one — the REST logbook returns ascending order today, but the resolver's
// answer must not depend on which end HA (or a proxy) puts the newest row.
// Unparseable timestamps lose to parseable ones; among equals the later row
// wins, so a fully unparseable list degrades to "last row".
func newestLogbookEntry(entries []logbookEntry) logbookEntry {
	best := 0
	var bestTime time.Time
	bestParsed := false
	for i, e := range entries {
		ts, err := time.Parse(time.RFC3339, e.When)
		parsed := err == nil
		switch {
		case parsed && (!bestParsed || !ts.Before(bestTime)):
			best, bestTime, bestParsed = i, ts, true
		case !parsed && !bestParsed:
			best = i
		}
	}
	return entries[best]
}

// alwaysContinuousDomains and nonNumericSensorDeviceClasses mirror HA's own
// logbook filter — homeassistant/components/logbook/const.py
// ALWAYS_CONTINUOUS_DOMAINS and homeassistant/components/sensor/const.py
// NON_NUMERIC_DEVICE_CLASSES, verified verbatim against HA 2026.7.2 source
// (the version the Docker tiers run). If HA widens either set, an entity we
// call covered goes quiet instead of excluded — misleading only in prose,
// never overriding data (see resolveActor's ordering).
var alwaysContinuousDomains = map[string]bool{
	"counter":   true,
	"image":     true,
	"proximity": true,
}

var nonNumericSensorDeviceClasses = map[string]bool{
	"date":      true,
	"enum":      true,
	"timestamp": true,
	"uptime":    true,
}

// logbookExclusionReason reports why HA's logbook will never hold an entry for
// this entity, or "" when the logbook covers it. It mirrors HA's own filter
// exactly (homeassistant/components/logbook/helpers.py, async_filter_entities
// + is_sensor_continuous, verified against HA 2026.7.2 source; the same rule
// the D70 audit confirmed live — a numeric sensor's logbook is `[]` forever):
// the counter, image and proximity domains never appear, and a sensor is
// dropped as "continuous" when its state has a unit_of_measurement, a
// state_class, or a numeric device_class. HA's registry-capabilities branch of
// is_sensor_continuous only applies when no live state exists; both callers
// here hold the live state, so the attributes branch is the whole rule.
func logbookExclusionReason(entityID string, attrs map[string]any) string {
	domain, _, _ := strings.Cut(entityID, ".")
	if alwaysContinuousDomains[domain] {
		return "HA's logbook never records the " + domain + " domain"
	}
	if domain != "sensor" {
		return ""
	}
	if _, ok := attrs["unit_of_measurement"]; ok {
		return "continuous sensor: has unit_of_measurement"
	}
	if _, ok := attrs["state_class"]; ok {
		return "continuous sensor: has state_class"
	}
	if dc, ok := attrs["device_class"].(string); ok && dc != "" && !nonNumericSensorDeviceClasses[dc] {
		return "continuous sensor: numeric device_class " + dc
	}
	return ""
}

// fetchLogbookEntries is the one place hactl's actor resolution reads the
// logbook: GET /api/logbook/<start>?end_time=&entity=, decoded and
// degeneracy-checked (H-14). Both `ent who` and `ent show` go through it, so
// there is a single decode site to be wrong at.
func fetchLogbookEntries(ctx context.Context, client *haapi.Client, start, end time.Time, entityID string) ([]logbookEntry, error) {
	data, err := client.GetLogbookFiltered(ctx,
		start.Format(time.RFC3339), end.Format(time.RFC3339), entityID)
	if err != nil {
		return nil, fmt.Errorf("fetching logbook: %w", err)
	}
	var entries []logbookEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing logbook: %w", err)
	}
	if err := degeneracy.Check("/api/logbook", &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// triggerLabel classifies a logbook entry's trigger into a human label, e.g.
// "User Jan", "Automation: Sunset Lights", "Script: morning_routine",
// "Device: Living-room remote", or "Home Assistant".
//
// Rule order matters — see the unit tests for the exact precedence:
//  1. ContextEventType == "automation_triggered" + ContextName → "Automation: ..."
//  2. ContextEventType == "script_started" + ContextName → "Script: ..."
//  3. ContextName present (e.g. device-fired event) → "Device: ..."
//  4. ContextUserID present → look up name in users (UUID fallback if absent).
//  5. Otherwise → "Home Assistant".
//
// HA propagates the ORIGINATING human's user id down the whole causal chain,
// so an automation fired by a user's toggle carries BOTH context_user_id (the
// human who started the chain) and context_event_type/context_name (the
// automation/script/device that actually made this particular change). The
// user id is the distal cause; the automation/script/device is the proximate
// one that changed the entity, so it takes precedence — reversing this order
// (as an earlier version of this function did) attributed every
// automation/script/device-caused change to a plain user edit whenever the
// chain happened to trace back to a human action.
//
// users may be nil (graceful-degrade when config/auth/list is admin-denied);
// the function still returns a sensible label in that case.
func triggerLabel(e logbookEntry, users map[string]haapi.UserEntry) string {
	switch e.ContextEventType {
	case "automation_triggered":
		if e.ContextName != "" {
			return "Automation: " + e.ContextName
		}
	case "script_started":
		if e.ContextName != "" {
			return "Script: " + e.ContextName
		}
	}
	if e.ContextName != "" {
		return "Device: " + e.ContextName
	}
	if e.ContextUserID != "" {
		if u, ok := users[e.ContextUserID]; ok && u.Name != "" {
			return "User " + u.Name
		}
		// Truncated UUID keeps the label scannable while still distinguishing users.
		return "User " + truncateUUID(e.ContextUserID)
	}
	return "Home Assistant"
}

func truncateUUID(s string) string {
	if len(s) <= 8 {
		return s
	}
	return s[:8] + "…"
}

// loadUsers fetches the HA user list once for the lifetime of a command.
//
// config/auth/list is admin-only — when the long-lived token lacks admin
// scope, HA returns APIError{Code:"unauthorized"}. We degrade gracefully:
// return an empty map plus a single stderr warning so the caller still gets
// automation/script/device attribution (none of which need user resolution).
// All errors are absorbed (logged at debug); the function never fails the
// command, which is why it returns a single value.
func loadUsers(ctx context.Context, ws *haapi.WSClient) map[string]haapi.UserEntry {
	users, err := ws.UserList(ctx)
	if err != nil {
		var apiErr *haapi.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "unauthorized" {
			fmt.Fprintln(os.Stderr,
				"hactl: long-lived token is not from an admin user — "+
					"showing raw user UUIDs in 'changed_by'. "+
					"Use an admin token to resolve user names.")
			return map[string]haapi.UserEntry{}
		}
		// Other failures (network, parse, unknown_command on test fixtures)
		// shouldn't kill the whole command. Degrade silently — `slog.Debug`
		// for diagnosis, but no user-visible warning.
		slog.Debug("loading HA user list", "error", err)
		return map[string]haapi.UserEntry{}
	}
	out := make(map[string]haapi.UserEntry, len(users))
	for _, u := range users {
		out[u.ID] = u
	}
	return out
}
