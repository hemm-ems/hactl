// This file deliberately carries NO build tag: the degeneracy detector below
// is wired into every runHactl* helper (helpers_test.go, integration-tagged),
// but its own correctness must be provable without an HA container.

package integration

import (
	"errors"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/analyze"
	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// looksDegenerate reports whether command output carries the marker hactl
// prints when a decode produced nothing at all.
//
// Why this exists: a wire-format mismatch once made every automation trace
// unmarshal into an empty struct, and because "empty" was rendered as success,
// `trace show` printed "  .    PASS" for every run — failed, aborted, cancelled
// alike. 1,101 unit tests and 235 integration tests stayed green through it,
// because none of them asserted on the one thing that was wrong. Checking the
// marker in the shared helpers turns all of the integration tests, including the
// ones that assert nothing about their output, into detectors for this class
// of bug.
//
// The marker is degeneracy.Marker (which analyze.UnparsedMarker is defined as),
// uppercase and emitted only by a renderer or an error that has given up on a
// decode, so legitimately empty results ("no traces found", an empty JSON array)
// do not trip it.
//
// H-14 generalised the poison past traces: every wire record hactl decodes now
// declares its identity, and a record that decoded without one is poisoned with
// this marker *and* fails its command. That is why the scan below covers the
// error a command returned as well as its stdout — for most decode paths the
// loud answer is a failure, not a rendered row.
func looksDegenerate(out string) bool {
	return strings.Contains(out, degeneracy.Marker)
}

// assertNoDegenerateOutput fails the calling test when a command rendered — or
// failed on — a payload it could not decode. Called by every runHactl* helper,
// with the command's error as well as its stdout, so no choke point is exempt.
func assertNoDegenerateOutput(t *testing.T, args []string, out string, err error) {
	t.Helper()
	if looksDegenerate(out) {
		t.Fatalf("hactl %v printed the %q degeneracy marker — a wire record decoded to nothing "+
			"and was rendered anyway:\n%s", args, degeneracy.Marker, out)
	}
	if err != nil && looksDegenerate(err.Error()) {
		t.Fatalf("hactl %v failed with the %q degeneracy marker — a wire payload did not match the "+
			"shape hactl decodes:\n%v\noutput: %s", args, degeneracy.Marker, err, out)
	}
}

// TestAssertNoDegenerateOutput_AcceptsCleanOutput exercises the wrapper the
// runHactl* helpers call, so healthy command output never trips it. It also
// keeps the wrapper referenced in untagged builds, where helpers_test.go is
// compiled out.
func TestAssertNoDegenerateOutput_AcceptsCleanOutput(t *testing.T) {
	assertNoDegenerateOutput(t, []string{"auto", "ls"}, "no automations found\n", nil)
	assertNoDegenerateOutput(t, []string{"ent", "show", "sensor.nope"},
		"", errors.New("unknown entity: sensor.nope"))
}

// TestLooksDegenerate_FlagsUnparsedRender feeds the detector the real rendering
// of a trace that decoded to nothing.
func TestLooksDegenerate_FlagsUnparsedRender(t *testing.T) {
	out := analyze.FormatCondensed(analyze.Condense(&analyze.RawTrace{}))
	if !looksDegenerate(out) {
		t.Errorf("detector missed a degenerate trace render: %q", out)
	}
}

// TestLooksDegenerate_FlagsPoisonedDecode is the H-14 half: a wire record that
// decoded without its identity must reach the detector, both through the error
// the decode returns and through the value it poisoned in place. Without this,
// the generalised poison would rely on the trace renderer being involved.
func TestLooksDegenerate_FlagsPoisonedDecode(t *testing.T) {
	entries := []haapi.EntityRegistryEntry{{Name: "Porch light"}}
	err := degeneracy.Check("config/entity_registry/list", &entries)
	if err == nil {
		t.Fatal("an entity registry entry with no entity_id decoded without complaint")
	}
	if !looksDegenerate(err.Error()) {
		t.Errorf("detector missed the degeneracy error: %q", err)
	}
	if !looksDegenerate(entries[0].EntityID) {
		t.Errorf("the identity-less record was not poisoned in place: %q", entries[0].EntityID)
	}
}

// TestLooksDegenerate_IgnoresLegitimateOutput pins the no-false-positive half:
// an empty result set is not a degenerate one.
func TestLooksDegenerate_IgnoresLegitimateOutput(t *testing.T) {
	wire := []byte(`{
		"run_id": "abc123", "domain": "automation", "item_id": "porch_light",
		"state": "stopped", "script_execution": "finished",
		"timestamp": {"start": "2026-07-21T05:00:00.000000+00:00"},
		"trace": {"trigger/0": [{"path": "trigger/0", "timestamp": "2026-07-21T05:00:00.000000+00:00"}]}
	}`)
	var raw analyze.RawTrace
	if err := raw.UnmarshalJSON(wire); err != nil {
		t.Fatalf("unmarshalling a healthy trace: %v", err)
	}

	for _, out := range []string{
		"",
		"no traces found\n",
		"[]\n",
		"{}\n",
		"0 automations\n",
		"[~12 tok]\n",
		"error: unknown trace ID: trc:zz\n",
		`{"result":"unparsed"}` + "\n", // lowercase JSON payload, not the render
		analyze.FormatCondensed(analyze.Condense(&raw)),
	} {
		if looksDegenerate(out) {
			t.Errorf("detector false-positived on legitimate output %q", out)
		}
	}
}
