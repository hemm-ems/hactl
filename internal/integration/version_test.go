//go:build integration

package integration

import (
	"regexp"
	"strings"
	"testing"
)

func TestVersion(t *testing.T) {
	out := runHactl(t, "version")
	trimmed := strings.TrimSpace(stripTokenHeader(out))

	// Should match pattern: hactl <version> (commit <hash>, built <date>)
	re := regexp.MustCompile(`^hactl .+ \(commit .+, built .+\)$`)
	if !re.MatchString(trimmed) {
		t.Errorf("version output does not match expected pattern: %q", trimmed)
	}
}

func TestVersionStats(t *testing.T) {
	out := runHactl(t, "version", "--stats")
	// Stats should contain byte count and token estimate
	assertContains(t, out, "stats:")
	assertContains(t, out, "bytes")
	assertContains(t, out, "tokens")
}
