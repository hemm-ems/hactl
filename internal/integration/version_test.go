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
	lines := strings.Split(trimmed, "\n")

	// First line: hactl <version> (commit <hash>, built <date>)
	re := regexp.MustCompile(`^hactl .+ \(commit .+, built .+\)$`)
	if !re.MatchString(lines[0]) {
		t.Errorf("version output does not match expected pattern: %q", lines[0])
	}
	// Self-discovery footer: canonical project + issues URL (#43)
	assertContains(t, trimmed, "project: https://github.com/hemm-ems/hactl")
	assertContains(t, trimmed, "issues:  https://github.com/hemm-ems/hactl/issues")
}

func TestVersionStats(t *testing.T) {
	out := runHactl(t, "version", "--stats")
	// Stats should contain byte count and token estimate
	assertContains(t, out, "stats:")
	assertContains(t, out, "bytes")
	assertContains(t, out, "tokens")
}
