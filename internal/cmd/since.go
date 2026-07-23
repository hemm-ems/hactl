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
