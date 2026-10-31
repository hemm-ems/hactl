package glob_test

// Spec tests for the shared glob engine, authored from the manual's "Matching"
// section, D-2, D-28 and D-63 and the phase-1 review findings 5 and 8. The
// engine's implementation was not read.

import (
	"testing"

	"github.com/hemm-ems/hactl/internal/glob"
)

// TestQuestionMarkIsOneCharacter — findings 5 and 8 (bugs): the manual says
// `?` is exactly one character, one Unicode code point, not one byte. Area and
// label names and file paths are often non-ASCII. One engine, so this fixes
// --pattern and the policy together. Red today: `?` consumes one byte, so it
// cannot stand for ü (two bytes in UTF-8).
func TestQuestionMarkIsOneCharacter(t *testing.T) {
	for _, tc := range []struct {
		s, pattern string
		want       bool
	}{
		{"küche.yaml", "k?che.yaml", true},
		{"kuche.yaml", "k?che.yaml", true},
		{"kueche.yaml", "k?che.yaml", false},
		{"Küche", "K?che", true},
		{"Küche", "k?che", true}, // and still case-insensitive (D-2)
		{"Küche", "K??che", false},
		{"Øl", "??", true},
		{"Øl", "?", false},
		{"Øl", "???", false},
		{"Garten🌻", "Garten?", true},
		{"ü", "?", true},
	} {
		if got := glob.Match(tc.s, tc.pattern); got != tc.want {
			t.Errorf("Match(%q, %q) = %v, want %v (`?` is one code point)", tc.s, tc.pattern, got, tc.want)
		}
	}
	if !glob.MatchQualified("input_select.küche", "input_select.k?che") {
		t.Errorf("MatchQualified does not take `?` as one code point either")
	}
}

// TestPolicyAndFilterShareOneEngineButNotTheAnchor — D-63: one matcher engine,
// two entry points. glob.Match (the policy's) anchors at the full id only;
// glob.MatchQualified (the filters') keeps D-28's second anchor.
func TestPolicyAndFilterShareOneEngineButNotTheAnchor(t *testing.T) {
	for _, tc := range []struct {
		s, pattern     string
		full, filtered bool
	}{
		{"light.kitchen_lamp", "kitchen*", false, true},
		{"lock.front_door", "front_door", false, true},
		{"lock.front_door", "*.front_door", true, true},
		{"binary_sensor.door", "sensor.*", false, false},
		{"light.turn_on", "turn_on", false, true},
	} {
		if got := glob.Match(tc.s, tc.pattern); got != tc.full {
			t.Errorf("Match(%q, %q) = %v, want %v (full id only)", tc.s, tc.pattern, got, tc.full)
		}
		if got := glob.MatchQualified(tc.s, tc.pattern); got != tc.filtered {
			t.Errorf("MatchQualified(%q, %q) = %v, want %v (D-28 keeps the second anchor for filters)", tc.s, tc.pattern, got, tc.filtered)
		}
	}
}
