// This file deliberately carries NO build tag: the degeneracy detector below
// is wired into every runHactl* helper (helpers_test.go, integration-tagged),
// but its own correctness must be provable without an HA container.

package integration

import (
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/analyze"
)

// looksDegenerate reports whether command output carries the marker hactl
// prints when a decode produced nothing at all.
//
// Why this exists: a wire-format mismatch once made every automation trace
// unmarshal into an empty struct, and because "empty" was rendered as success,
// `trace show` printed "  .    PASS" for every run — failed, aborted, cancelled
// alike. 1,101 unit tests and 235 integration tests stayed green through it,
// because none of them asserted on the one thing that was wrong. Checking the
// marker in the shared helpers turns all 235 integration tests, including the
// ones that assert nothing about their output, into detectors for this class
// of bug.
//
// The marker is analyze.UnparsedMarker, uppercase and emitted only by
// FormatCondensed, so legitimately empty results ("no traces found", an empty
// JSON array) do not trip it.
func looksDegenerate(out string) bool {
	return strings.Contains(out, analyze.UnparsedMarker)
}

// assertNoDegenerateOutput fails the calling test when a command rendered a
// trace it could not parse. Called by every runHactl* helper.
func assertNoDegenerateOutput(t *testing.T, args []string, out string) {
	t.Helper()
	if looksDegenerate(out) {
		t.Fatalf("hactl %v printed the %q degeneracy marker — a trace decoded to nothing "+
			"and was rendered anyway:\n%s", args, analyze.UnparsedMarker, out)
	}
}

// TestAssertNoDegenerateOutput_AcceptsCleanOutput exercises the wrapper the
// runHactl* helpers call, so healthy command output never trips it. It also
// keeps the wrapper referenced in untagged builds, where helpers_test.go is
// compiled out.
func TestAssertNoDegenerateOutput_AcceptsCleanOutput(t *testing.T) {
	assertNoDegenerateOutput(t, []string{"auto", "ls"}, "no automations found\n")
}

// TestLooksDegenerate_FlagsUnparsedRender feeds the detector the real rendering
// of a trace that decoded to nothing.
func TestLooksDegenerate_FlagsUnparsedRender(t *testing.T) {
	out := analyze.FormatCondensed(analyze.Condense(&analyze.RawTrace{}))
	if !looksDegenerate(out) {
		t.Errorf("detector missed a degenerate trace render: %q", out)
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
