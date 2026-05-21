//go:build companion_discovery

package companiontest_discovery

import (
	"errors"

	"github.com/hemm-ems/hactl/internal/companion"
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

// asDiscoveryError is a tiny wrapper around errors.As to keep the assertion
// in tests readable.
func asDiscoveryError(err error, target **companion.DiscoveryError) bool {
	return errors.As(err, target)
}
