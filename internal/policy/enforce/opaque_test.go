package enforce_test

import (
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// TestOpaqueServiceNeedsAnExactAllowWhileAnythingIsDenied — D-184: an opaque
// service (conversation.process, shell_command.*) is allowed while the
// profile denies anything only by an allow naming it exactly; a target-only
// service is untouched, and a profile denying nothing changes nothing.
func TestOpaqueServiceNeedsAnExactAllowWhileAnythingIsDenied(t *testing.T) {
	conv := policy.Resource{Kind: policy.KindService, ID: "conversation.process"}
	shell := policy.Resource{Kind: policy.KindService, ID: "shell_command.open_garage"}
	light := policy.Resource{Kind: policy.KindService, ID: "light.turn_on"}
	const denyLock = "version: 1\nactive: a\nprofiles:\n  a:\n    deny:\n      \"entity:lock.*\": [control]\n"
	for _, c := range []struct {
		name, body string
		res        policy.Resource
		allowed    bool
	}{
		{"nothing denied", "version: 1\nactive: a\nprofiles:\n  a:\n    allow:\n      \"*\": [read]\n", conv, true},
		{"a deny, the default allows", denyLock, conv, false},
		{"a deny, a domain service allowed by its exact name", denyLock, shell, false},
		{"a deny, a target-only service", denyLock, light, true},
		{"a deny, an exact allow", denyLock + "    allow:\n      \"service:conversation.process\": [control]\n", conv, true},
		{"a deny, a wildcard allow", denyLock + "    allow:\n      \"service:conversation.*\": [control]\n", conv, false},
		{"a deny, an exact allow without control", denyLock + "    allow:\n      \"service:conversation.process\": [read]\n", conv, false},
		{"default deny, service:* allowed", "version: 1\nactive: a\nprofiles:\n  a:\n    default: deny\n    allow:\n      \"service:*\": [control]\n", conv, false},
		{"default deny, the exact allow", "version: 1\nactive: a\nprofiles:\n  a:\n    default: deny\n    allow:\n      \"service:shell_command.open_garage\": [control]\n", shell, true},
		{"an exact deny stays a deny", "version: 1\nactive: a\nprofiles:\n  a:\n    deny:\n      \"service:conversation.process\": [control]\n", conv, false},
	} {
		pol, _ := loadPolicy(t, c.body)
		v := enforce.OpaqueService(pol, c.res, policy.Control, pol.Decide(c.res, policy.Control, nil))
		if v.Allowed != c.allowed {
			t.Errorf("%s: control %s allowed %v (%s), want %v", c.name, c.res.ID, v.Allowed, v.Rule, c.allowed)
		}
		if !v.Allowed && c.name != "an exact deny stays a deny" && v.Rule != enforce.RuleOpaqueServiceNeedsAllow {
			t.Errorf("%s: rule %q, want the opaque-service rule", c.name, v.Rule)
		}
	}
}
