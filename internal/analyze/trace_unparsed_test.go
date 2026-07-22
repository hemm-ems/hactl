package analyze

import (
	"strings"
	"testing"
)

// This file covers one rule: an empty decode is not a success.
//
// A wire-shape bug made RawTrace unmarshal into nothing, and because
// overallResult treated "no script_execution and no state" as a pass, every
// automation run rendered as "  .    PASS" — 1,101 unit tests and 235
// integration tests stayed green while `trace show` told the user nothing but
// good news. Empty must be spelled "unparsed", loudly.

// TestOverallResult_EmptyIsNotPass pins the core rule: with neither
// script_execution nor state present we do not know what happened.
func TestOverallResult_EmptyIsNotPass(t *testing.T) {
	raw := &RawTrace{Trace: RawTraceMeta{Execution: "", State: ""}}
	got := overallResult(raw)
	if got == StepPass {
		t.Errorf("overallResult(empty execution, empty state) = %q — empty must not be spelled success", got)
	}
	if got != StepUnknown {
		t.Errorf("overallResult(empty) = %q, want %q", got, StepUnknown)
	}
}

// TestCondense_EmptyDecodeIsUnknown guards the exact shape of the regression:
// a decode that yielded nothing — no domain, no item_id, no run_id, no steps —
// must report that, not a pass.
func TestCondense_EmptyDecodeIsUnknown(t *testing.T) {
	ct := Condense(&RawTrace{})

	if ct.Result == StepPass {
		t.Error("Condense of an all-empty trace reported a pass")
	}
	if ct.Result != StepUnknown {
		t.Errorf("Condense(empty).Result = %q, want %q", ct.Result, StepUnknown)
	}
	if ct.AutoID != "" {
		t.Errorf("Condense(empty).AutoID = %q, want empty — not punctuation from domain+\".\"+item_id", ct.AutoID)
	}
}

// TestCondense_EmptyDecodeOverridesStrayState covers a partial decode: even if
// a stray "state" survives, a trace with no identity and no steps is unparsed.
func TestCondense_EmptyDecodeOverridesStrayState(t *testing.T) {
	ct := Condense(&RawTrace{Trace: RawTraceMeta{State: "stopped"}})
	if ct.Result != StepUnknown {
		t.Errorf("Condense(no identity, no steps).Result = %q, want %q", ct.Result, StepUnknown)
	}
}

// TestCondense_IdentityAloneIsNotUnparsed makes sure the degeneracy check is
// narrow: a trace that decoded its identity is parsed, whatever its steps say.
func TestCondense_IdentityAloneIsNotUnparsed(t *testing.T) {
	ct := Condense(&RawTrace{Trace: RawTraceMeta{
		Domain: "automation", ItemID: "porch_light", RunID: "r1", Execution: "finished",
	}})
	if ct.Result != StepPass {
		t.Errorf("Condense(identified, finished).Result = %q, want %q", ct.Result, StepPass)
	}
	if ct.AutoID != "automation.porch_light" {
		t.Errorf("AutoID = %q, want automation.porch_light", ct.AutoID)
	}
}

// TestFormatCondensed_UnparsedNeverLooksLikePass is the renderer half: the
// degenerate trace prints the UNPARSED marker, never PASS, and never a bare
// "." where the automation ID belongs.
func TestFormatCondensed_UnparsedNeverLooksLikePass(t *testing.T) {
	out := FormatCondensed(Condense(&RawTrace{}))

	if !strings.Contains(out, "UNPARSED") {
		t.Errorf("degenerate trace output %q does not carry the UNPARSED marker", out)
	}
	if strings.Contains(out, "PASS") {
		t.Errorf("degenerate trace output claims PASS: %q", out)
	}
	for f := range strings.FieldsSeq(out) {
		if f == "." {
			t.Errorf("degenerate trace output renders a bare \".\" for the automation ID: %q", out)
		}
	}
}

// TestUnparsedMarkerMatchesRendering keeps the exported marker — which the
// integration harness greps for in every command's stdout — in lockstep with
// what FormatCondensed actually prints.
func TestUnparsedMarkerMatchesRendering(t *testing.T) {
	if got := strings.ToUpper(string(StepUnknown)); got != UnparsedMarker {
		t.Errorf("rendered StepUnknown = %q, but UnparsedMarker = %q", got, UnparsedMarker)
	}
	if !strings.Contains(FormatCondensed(Condense(&RawTrace{})), UnparsedMarker) {
		t.Error("FormatCondensed no longer prints UnparsedMarker for a degenerate trace")
	}
}

// TestFormatCondensed_KeepsHeaderLayout ensures the omit-empty-ID change does
// not disturb the normal header (run id, automation id, time, result).
func TestFormatCondensed_KeepsHeaderLayout(t *testing.T) {
	raw := loadTestTrace(t, "climate_schedule_pass.json")
	out := FormatCondensed(Condense(raw))
	head, _, _ := strings.Cut(out, "\n")
	want := "run-pass-001  automation.climate_schedule  09:42:00  PASS"
	if head != want {
		t.Errorf("header = %q, want %q", head, want)
	}
}
