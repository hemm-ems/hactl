package cmd

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// triggerLabel classifies a logbook entry's trigger into a human label, e.g.
// "User Jan", "Automation: Sunset Lights", "Script: morning_routine",
// "Device: Living-room remote", or "Home Assistant".
//
// Rule order matters — see the unit tests for the exact precedence:
//  1. ContextUserID present → look up name in users (UUID fallback if absent).
//  2. ContextEventType == "automation_triggered" + ContextName → "Automation: ..."
//  3. ContextEventType == "script_started" + ContextName → "Script: ..."
//  4. ContextName present (e.g. device-fired event) → "Device: ..."
//  5. Otherwise → "Home Assistant".
//
// users may be nil (graceful-degrade when config/auth/list is admin-denied);
// the function still returns a sensible label in that case.
func triggerLabel(e logbookEntry, users map[string]haapi.UserEntry) string {
	if e.ContextUserID != "" {
		if u, ok := users[e.ContextUserID]; ok && u.Name != "" {
			return "User " + u.Name
		}
		// Truncated UUID keeps the label scannable while still distinguishing users.
		return "User " + truncateUUID(e.ContextUserID)
	}
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
// Any other error (network, parse) propagates.
func loadUsers(ctx context.Context, ws *haapi.WSClient) (map[string]haapi.UserEntry, error) {
	users, err := ws.UserList(ctx)
	if err != nil {
		var apiErr *haapi.APIError
		if errors.As(err, &apiErr) && apiErr.Code == "unauthorized" {
			fmt.Fprintln(os.Stderr,
				"hactl: long-lived token is not from an admin user — "+
					"showing raw user UUIDs in 'changed_by'. "+
					"Use an admin token to resolve user names.")
			return map[string]haapi.UserEntry{}, nil
		}
		// Other failures (network, parse, unknown_command on test fixtures)
		// shouldn't kill the whole command. Degrade silently — `slog.Debug`
		// for diagnosis, but no user-visible warning.
		slog.Debug("loading HA user list", "error", err)
		return map[string]haapi.UserEntry{}, nil
	}
	out := make(map[string]haapi.UserEntry, len(users))
	for _, u := range users {
		out[u.ID] = u
	}
	return out, nil
}
