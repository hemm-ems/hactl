package cmd

import (
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// TestAHelperLookupTellsADeniedHelperFromAMissingOne characterises a stated
// limit (D-183), kept by decision: `helper show`/`helper cat` address a
// helper by its bare id, which names no domain, so the policy decides it on
// the answer — a read-denied helper that exists is refused, one that does not
// exist is "not found". The two answers tell existence apart; neither shows
// the helper's content.
func TestAHelperLookupTellsADeniedHelperFromAMissingOne(t *testing.T) {
	rig := newTypedRig(t)
	writePolicyFile(t, rig.ts.dir, "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"helper:input_boolean/guest_mode\": [read]\n")
	for _, verb := range []string{"show", "cat"} {
		out, err := runPolicyCLI(t, rig.ts.dir, "helper", verb, "guest_mode")
		if policyErrorClass(err) != enforce.ClassDenied || strings.Contains(out, "Guest Mode") {
			t.Errorf("helper %s of a denied helper: class %q (%v)\n%s", verb, policyErrorClass(err), err, out)
		}
		out, err = runPolicyCLI(t, rig.ts.dir, "helper", verb, "no_such_helper")
		if err == nil || policyErrorClass(err) == enforce.ClassDenied {
			t.Errorf("helper %s of a missing helper: class %q (%v), want a not-found error\n%s", verb, policyErrorClass(err), err, out)
		}
	}
}
