package cmd

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// parseSince converts a duration string like "24h" or "7d" to time.Duration.
// Shared by every command that honors the global --since flag.
func parseSince(s string) (time.Duration, error) {
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
