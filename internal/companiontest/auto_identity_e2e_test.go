//go:build companion

package companiontest

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// B-19 (D-130, issue #169) end to end: `auto apply` and `auto rollback` refuse a
// file or backup whose top-level `id:` is not the target's config id — a
// differing id, a missing one — in the dry run already and under --confirm,
// against a real Home Assistant and a real companion, and automations.yaml is
// byte-identical afterwards. The matching id is TestE2EAutoApplyWritesOnly-
// ItsOwnEntryCLI, which applies and rolls back `auto cat`'s own output.
// Written from the contract (D-130, manual "Write path (automations)") without
// reading internal/cmd/auto.go.

var topLevelIDLine = regexp.MustCompile(`(?m)^id:.*\n`)

// seededCandidate returns `auto cat seeded_test_auto` with its id: line
// replaced by idLine ("" drops it).
func seededCandidate(t *testing.T, idLine string) string {
	t.Helper()
	stored, err := runHactlE2E(t, "auto", "cat", "seeded_test_auto")
	if err != nil {
		t.Fatalf("auto cat: %v\n%s", err, stored)
	}
	if !topLevelIDLine.MatchString(stored) {
		t.Fatalf("auto cat printed no top-level id: line:\n%s", stored)
	}
	if idLine != "" {
		idLine += "\n"
	}
	return topLevelIDLine.ReplaceAllLiteralString(stored, idLine)
}

func TestE2EAutoApplyRefusesAFileOfAnotherIdentityCLI(t *testing.T) {
	for _, tc := range []struct {
		name, idLine, phrase string
	}{
		{"differing id", `id: "seeded_other_xyz"`,
			`has id "seeded_other_xyz", not the config id of the automation it would replace ("seeded_test_auto")`},
		{"missing id", "",
			`has no id, not the config id of the automation it would replace ("seeded_test_auto")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := filepath.Join(t.TempDir(), "candidate.yaml")
			if err := os.WriteFile(candidate, []byte(seededCandidate(t, tc.idLine)), 0o600); err != nil {
				t.Fatal(err)
			}
			before := storedAutomationsYAML(t)
			for _, mode := range [][]string{nil, {"--confirm"}} {
				args := append([]string{"auto", "apply", "seeded_test_auto", "-f", candidate}, mode...)
				stdout, stderr, err := runHactlE2EStreams(t, args...)
				assertE2EIdentityRefusal(t, strings.Join(args, " "), stdout, stderr, err, tc.phrase)
			}
			if after := storedAutomationsYAML(t); after != before {
				t.Errorf("a refused apply changed automations.yaml.\n--- before:\n%s\n--- after:\n%s", before, after)
			}
		})
	}
}

// TestE2EAutoRollbackRefusesABackupOfAnotherIdentityCLI plants a backup for
// seeded_test_auto, named the way `auto apply` names backups and stamped in
// the future so it is the one a rollback picks, whose id: differs.
func TestE2EAutoRollbackRefusesABackupOfAnotherIdentityCLI(t *testing.T) {
	dir := filepath.Join(instanceDir, "backups")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(dir, "2099-01-01T00-00-00_seeded_test_auto.yaml")
	if err := os.WriteFile(backup, []byte(seededCandidate(t, `id: "seeded_other_xyz"`)), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(backup) })

	before := storedAutomationsYAML(t)
	for _, args := range [][]string{
		{"auto", "rollback", "seeded_test_auto"},
		{"auto", "rollback", "seeded_test_auto", "--confirm"},
	} {
		stdout, stderr, err := runHactlE2EStreams(t, args...)
		assertE2EIdentityRefusal(t, strings.Join(args, " "), stdout, stderr, err,
			`has id "seeded_other_xyz", not the config id of the automation it would restore ("seeded_test_auto")`)
	}
	if after := storedAutomationsYAML(t); after != before {
		t.Errorf("a refused rollback changed automations.yaml.\n--- before:\n%s\n--- after:\n%s", before, after)
	}
}

func assertE2EIdentityRefusal(t *testing.T, what, stdout, stderr string, err error, phrase string) {
	t.Helper()
	if err == nil {
		t.Fatalf("%s: exit 0, want 1 — a candidate of another identity must be refused\n%s%s", what, stdout, stderr)
	}
	if strings.TrimSpace(stdout) != "" {
		t.Errorf("%s: stdout %q, want empty — no diff or plan before the refusal", what, stdout)
	}
	for _, want := range []string{phrase, "hactl auto create -f", "hactl auto delete seeded_test_auto", "hactl ent rename"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("%s: stderr does not say %q:\n%s", what, want, stderr)
		}
	}
}
