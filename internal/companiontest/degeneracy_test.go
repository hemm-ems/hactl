// This file deliberately carries NO build tag: the detector below is wired into
// runHactlE2E (e2e_test.go, which is companion-tagged), but its own correctness
// must be provable without a Home Assistant and a companion container.

package companiontest

import (
	"testing"

	"strings"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/degeneracy"
)

// looksDegenerateE2E reports whether the real hactl binary rendered — or failed
// with — a payload it could not decode (H-14).
//
// This tier runs the real hactl binary against a real Home Assistant and a real
// companion, so it is the one place in the repo where a genuine wire-shape
// change would actually show up: every other tier feeds hactl a payload that
// some Go code in this repo produced. Until now it scanned for nothing. The
// UNPARSED sweep introduced with H-7 covered internal/integration's runHactl*
// helpers only, which left the highest-fidelity tier in the repo exempt from
// the one class of defect the marker exists to catch.
//
// runHactlE2E returns combined stdout+stderr, so a single scan covers a
// rendered marker and a command that failed with one alike.
func looksDegenerateE2E(out string) bool {
	return strings.Contains(out, degeneracy.Marker)
}

// assertNoDegenerateE2EOutput fails the calling test when hactl rendered or
// reported an identity-less record. Called by runHactlE2E, so every end-to-end
// test — including the ones that assert nothing about their output — is a
// detector for the class.
func assertNoDegenerateE2EOutput(t *testing.T, args []string, out string) {
	t.Helper()
	if looksDegenerateE2E(out) {
		t.Fatalf("hactl %v produced the %q degeneracy marker — a wire record decoded without its "+
			"identity and was rendered or reported anyway:\n%s", args, degeneracy.Marker, out)
	}
}

// TestAssertNoDegenerateE2EOutput_AcceptsCleanOutput pins the no-false-positive
// half, and keeps the wrapper referenced in untagged builds where e2e_test.go
// is compiled out.
func TestAssertNoDegenerateE2EOutput_AcceptsCleanOutput(t *testing.T) {
	assertNoDegenerateE2EOutput(t, []string{"ent", "related", "sensor.x"}, "no related entities\n")
	assertNoDegenerateE2EOutput(t, []string{"auto", "ls"}, "0 automations\n")
}

// TestLooksDegenerateE2E_FlagsPoisonedDecode proves the scan sees what
// degeneracy.Check actually produces, rather than assuming the two agree on the
// token. It uses a companion record because this tier's distinguishing feature
// is that it drives a real companion.
func TestLooksDegenerateE2E_FlagsPoisonedDecode(t *testing.T) {
	// The envelope keeps its identity, so only the nested entry is degenerate:
	// this proves Check reaches records through a slice field, which is how
	// every companion list response is shaped.
	resp := companion.RelatedEntityResponse{
		EntityID: "light.porch",
		Related:  []companion.RelatedEntityEntry{{Relationship: "yaml-reference"}},
	}
	err := degeneracy.Check("companion /v1/related", &resp)
	if err == nil {
		t.Fatal("a related-entity entry with no entity_id decoded without complaint")
	}
	if !looksDegenerateE2E(err.Error()) {
		t.Errorf("detector missed the degeneracy error: %q", err)
	}
	if !looksDegenerateE2E(resp.Related[0].EntityID) {
		t.Errorf("the identity-less record was not poisoned in place: %q", resp.Related[0].EntityID)
	}
}

// TestLooksDegenerateE2E_IgnoresLegitimateOutput pins the other half: an empty
// answer is not a degenerate one, and a lowercase "unparsed" in a payload is
// not the marker.
func TestLooksDegenerateE2E_IgnoresLegitimateOutput(t *testing.T) {
	for _, out := range []string{
		"",
		"no related entities\n",
		"[]\n",
		"{}\n",
		"0 automations\n",
		`{"state":"unparsed"}` + "\n",
	} {
		if looksDegenerateE2E(out) {
			t.Errorf("detector false-positived on legitimate output %q", out)
		}
	}
}
