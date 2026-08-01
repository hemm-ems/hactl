package cmd

import "testing"

// TestRunsColumnNamesTheWindowItCounted — live-fire #72.
//
// `auto ls --pattern x --since 1h --json` answered `"runs_24h": "0"` for a count
// that covered one hour. The count was right and its NAME was a claim about it
// that the invocation contradicted — a JSON consumer that trusts the key over
// the command line that produced it reads the wrong number.
//
// The default is asserted alongside, because it is the whole reason this is a
// derivation rather than a rename: nobody who did not touch `--since` sees any
// change, so the machine contract for the common call is untouched.
func TestRunsColumnNamesTheWindowItCounted(t *testing.T) {
	for _, tc := range []struct{ since, want string }{
		{"24h", "runs_24h"},
		{"1h", "runs_1h"},
		{"7d", "runs_7d"},
		{"90m", "runs_90m"},
		// A window that cannot be spelled inside an identifier is left unnamed
		// rather than misnamed: an unnamed count is honest, a misnamed one is
		// the defect itself.
		{"", "runs"},
		{"1h30m ", "runs"},
	} {
		if got := runsColumn(tc.since); got != tc.want {
			t.Errorf("runsColumn(%q) = %q, want %q", tc.since, got, tc.want)
		}
	}
}
