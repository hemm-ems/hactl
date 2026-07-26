//go:build companion_discovery

package companiontest_discovery

import (
	"github.com/hemm-ems/hactl/internal/config"
)

// minimalConfig returns a config with HA_URL set and no companion override —
// forces Discover() to attempt Supervisor auto-discovery.
func minimalConfig(haURL string) *config.Config {
	return &config.Config{
		URL:   haURL,
		Token: "any-token-accepted",
	}
}
