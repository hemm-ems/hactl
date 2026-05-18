//go:build integration

package integration

import "testing"

// Golden-file tests capture hactl output and compare against committed snapshots.
// Run with HACTL_UPDATE_GOLDEN=1 to regenerate golden files after intentional format changes.

func TestGoldenHealth(t *testing.T) {
	out := runHactl(t, "health")
	assertGolden(t, "health", out)
}

func TestGoldenAutoLs(t *testing.T) {
	out := runHactl(t, "auto", "ls")
	assertGolden(t, "auto_ls", out)
}

func TestGoldenEntLs(t *testing.T) {
	// Use a narrow pattern to get deterministic output
	out := runHactl(t, "ent", "ls", "--pattern", "sun.*")
	assertGolden(t, "ent_ls_sun", out)
}

func TestGoldenIssues(t *testing.T) {
	out := runHactl(t, "issues")
	assertGolden(t, "issues", out)
}

func TestGoldenCacheStatus(t *testing.T) {
	out := runHactl(t, "cache", "status")
	assertGolden(t, "cache_status", out)
}

func TestGoldenVersion(t *testing.T) {
	out := runHactl(t, "version")
	assertGolden(t, "version", out)
}

func TestGoldenScriptLs(t *testing.T) {
	out := runHactl(t, "script", "ls")
	assertGolden(t, "script_ls", out)
}

func TestGoldenScriptLsPatternKino(t *testing.T) {
	out := runHactl(t, "script", "ls", "--pattern", "kino")
	assertGolden(t, "script_ls_kino", out)
}

func TestGoldenEntLsDomainSun(t *testing.T) {
	out := runHactl(t, "ent", "ls", "--domain", "sun")
	assertGolden(t, "ent_ls_domain_sun", out)
}
