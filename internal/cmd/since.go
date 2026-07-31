package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseSince converts a duration string like "24h" or "7d" to time.Duration.
// Shared by every command that honors the global --since flag.
//
// --since names how far BACK to look, so a negative value is always a mistake.
// Accepting one silently inverted the query window (since > until), which HA
// answers with an empty result — indistinguishable from "nothing happened".
// Under the manual's "stop at the first miss" rule that reads as a confident
// negative answer, so this rejects rather than guessing at the intent.
func parseSince(s string) (time.Duration, error) {
	d, err := parseSinceRaw(s)
	if err != nil {
		return 0, err
	}
	if d < 0 {
		return 0, fmt.Errorf("invalid duration: %s (--since is a look-back window; it cannot be negative)", s)
	}
	return d, nil
}

// runsColumn is the name of the run-count column, for the window the caller
// actually asked for.
//
// The column was called `runs_24h` whatever `--since` said, so `auto ls
// --pattern x --since 1h --json` answered `"runs_24h": "0"` for a count that
// covered one hour (#72). A name is not a label beside the value, it is a claim
// ABOUT the value, and a JSON consumer that trusts the key over the invocation
// that produced it reads the wrong number — which is the whole of H-11 one
// layer up from the count itself.
//
// Derived from the flag rather than from a table, so it cannot fall out of step
// with it: the default `--since 24h` still produces exactly `runs_24h`, and no
// caller who did not touch the flag sees any change. A window hactl cannot name
// falls back to `runs`, because an unnamed count is honest and a misnamed one is
// not.
func runsColumn(since string) string {
	if !validWindowName(since) {
		return "runs"
	}
	return "runs_" + strings.ToLower(since)
}

// validWindowName reports whether a --since value can be spelled inside a
// column name: letters and digits only, so the key stays a plain identifier for
// whatever reads the JSON.
func validWindowName(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z':
		default:
			return false
		}
	}
	return true
}

func parseSinceRaw(s string) (time.Duration, error) {
	if after, found := strings.CutSuffix(s, "d"); found {
		days, err := strconv.Atoi(after)
		if err != nil {
			return 0, fmt.Errorf("invalid duration: %s", s)
		}
		return time.Duration(days) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid duration: %s", s)
	}
	return d, nil
}
