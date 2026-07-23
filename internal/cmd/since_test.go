package cmd

import (
	"testing"
	"time"
)

// TestParseSince_RejectsNegative pins a round-2 finding: --since names how far
// BACK to look, so a negative value inverted the query window (since > until).
// HA answers an inverted window with an empty result, which is indistinguishable
// from "nothing happened" — and under the manual's "stop at the first miss"
// rule an agent reports that as a confident negative answer.
func TestParseSince_RejectsNegative(t *testing.T) {
	for _, s := range []string{"-5h", "-1d", "-30m"} {
		if d, err := parseSince(s); err == nil {
			t.Errorf("parseSince(%q) = %v, want an error — a negative look-back silently inverts the window", s, d)
		}
	}

	// Positive forms must keep working, including the custom "d" suffix.
	for _, tc := range []struct {
		in   string
		want time.Duration
	}{
		{"24h", 24 * time.Hour},
		{"7d", 7 * 24 * time.Hour},
		{"0h", 0},
		{"90m", 90 * time.Minute},
	} {
		got, err := parseSince(tc.in)
		if err != nil {
			t.Errorf("parseSince(%q) errored: %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("parseSince(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
