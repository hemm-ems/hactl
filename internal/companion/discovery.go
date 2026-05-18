package companion

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// companionSlug is the Supervisor add-on slug for hactl-companion.
const companionSlug = "hactl_companion"

// Discover finds the companion URL.
// Priority:
//  1. Explicit COMPANION_URL from config (.env)
//  2. WS hassio/addon/info → construct ingress URL (HA OS/Supervised only)
//
// Returns the companion base URL or an error if not found.
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
			slog.Debug("hassio/addon/info unavailable", "error", err)
		}
	}

	return "", errors.New("companion not found: set COMPANION_URL in .env or install hactl-companion add-on")
}
