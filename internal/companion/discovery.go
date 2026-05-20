package companion

import (
	"context"
	"log/slog"
	"strings"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// companionSlug is the Supervisor add-on slug for hactl-companion.
const companionSlug = "hactl_companion"

// DiscoveryReason categorises why companion discovery failed.
type DiscoveryReason string

const (
	// ReasonAuthDenied — token lacks hassio_admin scope.
	ReasonAuthDenied DiscoveryReason = "auth_denied"
	// ReasonAddonMissing — hactl-companion add-on is not installed.
	ReasonAddonMissing DiscoveryReason = "addon_missing"
	// ReasonUnreachable — add-on is installed but Ingress or Supervisor cannot be reached.
	ReasonUnreachable DiscoveryReason = "unreachable"
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
			"Auto-discovery uses the Supervisor API, which requires a long-lived token\n" +
			"created by an HA admin (owner). The current token is denied on /api/hassio/*.\n\n" +
			"Fix: create a new long-lived token as an HA owner, or set COMPANION_URL in .env\n" +
			"     (Settings → Add-ons → hactl companion → Web UI → copy the URL)."
	case ReasonAddonMissing:
		hint = "companion not found (add-on not installed)\n\n" +
			"Install hactl-companion from HA → Settings → Add-ons, then re-run this command.\n" +
			"Or set COMPANION_URL in .env if the add-on is reachable at a direct URL."
	default:
		hint = "companion not found (unreachable)\n\n" +
			"The hactl-companion add-on appears to be installed but cannot be reached.\n" +
			"Check Ingress / network, or set COMPANION_URL in .env for a direct connection."
	}
	return &DiscoveryError{Reason: reason, msg: hint}
}

// classifyWSError inspects the error message from a failed HassioAddonInfo call
// and returns the most likely DiscoveryReason.
func classifyWSError(errMsg string) DiscoveryReason {
	lower := strings.ToLower(errMsg)
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

// Discover finds the companion URL.
// Priority:
//  1. Explicit COMPANION_URL from config (.env)
//  2. WS hassio/addon/info → construct ingress URL (HA OS/Supervised only)
//
// Returns the companion base URL or a *DiscoveryError if not found.
func Discover(ctx context.Context, cfg *config.Config, ws *haapi.WSClient) (string, error) {
	// 1. Explicit config
	if cfg.CompanionURL != "" {
		slog.Debug("companion URL from config", "url", cfg.CompanionURL)
		return cfg.CompanionURL, nil
	}

	// 2. Try Supervisor addon info via WS
	if ws != nil {
		info, err := ws.HassioAddonInfo(ctx, companionSlug)
		if err == nil && info.IngressURL != "" {
			url := strings.TrimRight(cfg.URL, "/") + info.IngressURL
			slog.Debug("companion URL from hassio/addon/info", "url", url)
			return url, nil
		}
		if err != nil {
			reason := classifyWSError(err.Error())
			slog.Debug("hassio/addon/info unavailable", "error", err, "reason", reason)
			return "", newDiscoveryError(reason)
		}
	}

	return "", newDiscoveryError(ReasonUnreachable)
}
