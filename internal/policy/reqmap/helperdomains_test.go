package reqmap

import (
	"maps"
	"slices"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
)

// TestHelperPlatformsAreThePolicyHelperDomains — the typed-control mapping
// (D-107) and `policy lint`'s half-protected check (D-115) decide which entity
// is a helper from the same set: a domain one knows and the other does not
// would resolve the same entity to two different owners.
func TestHelperPlatformsAreThePolicyHelperDomains(t *testing.T) {
	got := slices.Sorted(maps.Keys(helperPlatforms))
	want := slices.Sorted(slices.Values(policy.HelperDomains()))
	if !slices.Equal(got, want) {
		t.Errorf("reqmap helperPlatforms %v, policy.HelperDomains %v — keep them in lockstep", got, want)
	}
}
