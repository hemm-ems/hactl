package companion

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// DiscoveryReason categorises why companion discovery failed.
type DiscoveryReason string

const (
	// ReasonAuthDenied — token lacks hassio_admin scope.
	ReasonAuthDenied DiscoveryReason = "auth_denied"
	// ReasonAddonMissing — hactl-companion add-on is not installed.
	ReasonAddonMissing DiscoveryReason = "addon_missing"
	// ReasonUnreachable — add-on is installed but Ingress or Supervisor cannot be reached.
	ReasonUnreachable DiscoveryReason = "unreachable"
	// ReasonProtocolMismatch — HA does not expose the Supervisor WS proxy.
	// Most often this means HA Container (no Supervisor) rather than HA OS/Supervised.
	ReasonProtocolMismatch DiscoveryReason = "protocol_mismatch"
)

// DiscoveryError is returned when the companion cannot be found, with a typed
// Reason so callers can render a targeted fix hint.
type DiscoveryError struct {
	Reason DiscoveryReason
	msg    string
}

func (e *DiscoveryError) Error() string { return e.msg }

func newDiscoveryError(reason DiscoveryReason) *DiscoveryError {
	var hint string
	switch reason {
	case ReasonAuthDenied:
		hint = "companion not found (token lacks hassio_admin scope)\n\n" +
			"Auto-discovery uses the Supervisor WS proxy, which requires a long-lived\n" +
			"token created by an HA admin (owner). The current token is denied.\n\n" +
			"Fix: create a new long-lived token as an HA owner, or set COMPANION_URL in .env\n" +
			"     (Settings → Add-ons → hactl companion → Web UI → copy the URL)."
	case ReasonAddonMissing:
		hint = "companion not found (add-on not installed)\n\n" +
			"Install hactl-companion from HA → Settings → Add-ons, then re-run this command.\n" +
			"Or set COMPANION_URL in .env if the add-on is reachable at a direct URL."
	case ReasonProtocolMismatch:
		hint = "companion not found (HA does not expose the Supervisor WS proxy)\n\n" +
			"This usually means HA Container (Docker) rather than HA OS / Supervised — there\n" +
			"is no Supervisor to enumerate add-ons via WS. Set COMPANION_URL in .env to point\n" +
			"directly at the companion."
	default:
		hint = "companion not found (unreachable)\n\n" +
			"The hactl-companion add-on appears to be installed but cannot be reached.\n" +
			"Check Ingress / network, or set COMPANION_URL in .env for a direct connection."
	}
	return &DiscoveryError{Reason: reason, msg: hint}
}

// classifyWSError inspects the error message from a failed Supervisor WS call
// and returns the most likely DiscoveryReason.
func classifyWSError(errMsg string) DiscoveryReason {
	lower := strings.ToLower(errMsg)
	if strings.Contains(lower, "unknown_command") ||
		strings.Contains(lower, "unknown_message_type") ||
		strings.Contains(lower, "unknown command") {
		return ReasonProtocolMismatch
	}
	if strings.Contains(lower, "forbidden") ||
		strings.Contains(lower, "unauthorized") ||
		strings.Contains(lower, "401") ||
		strings.Contains(lower, "403") {
		return ReasonAuthDenied
	}
	if strings.Contains(lower, "not found") ||
		strings.Contains(lower, "addon not found") {
		return ReasonAddonMissing
	}
	return ReasonUnreachable
}

// addonEntry is the subset of /addons enumeration we use to pick the companion.
type addonEntry struct {
	Slug    string `json:"slug"`
	Name    string `json:"name"`
	State   string `json:"state"`
	Version string `json:"version"`
	Ingress bool   `json:"ingress"`
}

// addonInfo is the subset of /addons/<slug>/info we use to build the URL.
type addonInfo struct {
	Slug       string `json:"slug"`
	State      string `json:"state"`
	Version    string `json:"version"`
	Ingress    bool   `json:"ingress"`
	IngressURL string `json:"ingress_url"`
}

// matchCompanion picks the companion add-on out of a Supervisor /addons list.
// Match strategy (in order):
//  1. slug exactly "hactl_companion" (local repo / dev install)
//  2. slug ends in "_hactl_companion" (Supervisor repo install: `<repoId>_hactl_companion`)
//  3. name equals "hactl companion" (case-insensitive) — last-resort fallback
//
// Returns empty string if no entry matches.
func matchCompanion(addons []addonEntry) string {
	for _, a := range addons {
		if a.Slug == "hactl_companion" {
			return a.Slug
		}
	}
	for _, a := range addons {
		if strings.HasSuffix(a.Slug, "_hactl_companion") {
			return a.Slug
		}
	}
	for _, a := range addons {
		if strings.EqualFold(a.Name, "hactl companion") {
			return a.Slug
		}
	}
	return ""
}

// Discover finds the companion URL.
// Priority:
//  1. Explicit COMPANION_URL from config (.env)
//  2. Enumerate add-ons via the Supervisor WS proxy (`supervisor/api`), pick the
//     companion by slug/name, then fetch its ingress URL.
//
// Returns the companion base URL or a *DiscoveryError if not found.
func Discover(ctx context.Context, cfg *config.Config, ws *haapi.WSClient) (string, error) {
	if cfg.CompanionURL != "" {
		slog.Debug("companion URL from config", "url", cfg.CompanionURL)
		return cfg.CompanionURL, nil
	}

	if ws == nil {
		return "", newDiscoveryError(ReasonUnreachable)
	}

	listRaw, err := ws.SupervisorAPI(ctx, "/addons", "get", nil)
	if err != nil {
		reason := classifyWSError(err.Error())
		slog.Debug("supervisor/api /addons failed", "error", err, "reason", reason)
		return "", newDiscoveryError(reason)
	}

	var listResp struct {
		Addons []addonEntry `json:"addons"`
	}
	if jsonErr := json.Unmarshal(listRaw, &listResp); jsonErr != nil {
		return "", fmt.Errorf("parsing /addons response: %w", jsonErr)
	}

	slug := matchCompanion(listResp.Addons)
	if slug == "" {
		slog.Debug("companion add-on not in Supervisor /addons list", "count", len(listResp.Addons))
		return "", newDiscoveryError(ReasonAddonMissing)
	}

	infoRaw, err := ws.SupervisorAPI(ctx, "/addons/"+slug+"/info", "get", nil)
	if err != nil {
		reason := classifyWSError(err.Error())
		slog.Debug("supervisor/api /addons/<slug>/info failed", "slug", slug, "error", err, "reason", reason)
		return "", newDiscoveryError(reason)
	}

	var info addonInfo
	if jsonErr := json.Unmarshal(infoRaw, &info); jsonErr != nil {
		return "", fmt.Errorf("parsing /addons/%s/info response: %w", slug, jsonErr)
	}
	if info.IngressURL == "" {
		slog.Debug("companion add-on info has no ingress_url", "slug", slug, "state", info.State)
		return "", newDiscoveryError(ReasonUnreachable)
	}

	url := strings.TrimRight(cfg.URL, "/") + info.IngressURL
	slog.Debug("companion URL from Supervisor WS proxy", "slug", slug, "url", url)
	return url, nil
}
