package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckCandidateIDReadsTheTopLevelIDAsWritten — D-130's rule, one row per
// way a text's `id:` can be or fail to be the target's config id. The cmd-level
// spec tests cover the three the manual names (differing, missing, unquoted
// number); these are the shapes YAML adds on top, each of which a decoded map
// would read differently from how the companion compares it.
func TestCheckCandidateIDReadsTheTopLevelIDAsWritten(t *testing.T) {
	for _, tc := range []struct {
		name, text, configID string
		want                 string // "" accepts; otherwise the phrase after the file name
	}{
		{"plain string", "id: pg_target\nalias: x\n", "pg_target", ""},
		{"single-quoted timestamp", "id: '1712345678901'\nalias: x\n", "1712345678901", ""},
		{"double-quoted", "alias: x\nid: \"pg_target\"\n", "pg_target", ""},
		{"through an anchor", "base: &i pg_target\nid: *i\n", "pg_target", ""},
		{"differing", "id: pg_other\n", "pg_target", `has id "pg_other"`},
		{"case matters", "id: PG_Target\n", "pg_target", `has id "PG_Target"`},
		{"missing", "alias: x\n", "pg_target", "has no id"},
		{"null", "id:\nalias: x\n", "pg_target", "has no id"},
		{"tilde", "id: ~\n", "pg_target", "has no id"},
		{"empty document", "", "pg_target", "has no id"},
		{"only an id nested below the top level", "alias: x\nactions:\n  - id: pg_target\n", "pg_target", "has no id"},
		{"unquoted integer", "id: 1712345678901\n", "1712345678901", "has id 1712345678901 (a number — quote it)"},
		{"unquoted float", "id: 1.5\n", "1.5", "has id 1.5 (a number — quote it)"},
		{"a bool", "id: true\n", "true", "has id true (not a string — quote it)"},
		{"a mapping", "id:\n  a: b\n", "pg_target", "has an id that is not a string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckCandidateID("new.yaml", []byte(tc.text), tc.configID)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("a text whose id is the config id %q was refused: %v", tc.configID, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted, want a refusal saying %q", tc.want)
			}
			if prefix := "file new.yaml " + tc.want + ", not the config id of the automation it would replace"; !strings.HasPrefix(err.Error(), prefix) {
				t.Errorf("refusal:\n%v\nwant it to start %q", err, prefix)
			}
		})
	}
}

// TestCheckCandidateIDLeavesANonMappingToTheParser — a YAML list or scalar is
// not an automation, and the parser downstream refuses it in its own words; an
// identity refusal ("has no id") would describe it wrongly. Unparseable YAML
// is refused here, as the same parse error the write path gives.
func TestCheckCandidateIDLeavesANonMappingToTheParser(t *testing.T) {
	for _, text := range []string{"- id: pg_target\n", "just a string\n"} {
		if err := CheckCandidateID("new.yaml", []byte(text), "pg_target"); err != nil {
			t.Errorf("%q: %v, want nil — not a mapping is the parser's to refuse", text, err)
		}
	}
	err := CheckCandidateID("new.yaml", []byte("id: [unclosed\n"), "pg_target")
	if err == nil || !strings.HasPrefix(err.Error(), "parsing local YAML: ") {
		t.Errorf("unparseable YAML: %v, want the write path's parse error", err)
	}
}

// TestPlanRollbackRefusesABackupOfAnotherIdentity — the preview reads the
// backup it names, so it refuses what Rollback would (H-2, D-130), against the
// id in the backup's file name when no target is given.
func TestPlanRollbackRefusesABackupOfAnotherIdentity(t *testing.T) {
	backupDir := t.TempDir()
	backup := filepath.Join(backupDir, "2026-11-01T09-00-00_pg_target.yaml")
	if err := os.WriteFile(backup, []byte("id: pg_other\nalias: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := New(nil, nil, nil, backupDir)
	for _, target := range []string{"pg_target", ""} {
		_, err := w.PlanRollback(target)
		if err == nil {
			t.Fatalf("PlanRollback(%q) planned to restore a backup carrying another id", target)
		}
		want := "backup " + backup + ` has id "pg_other", not the config id of the automation it would restore ("pg_target"): restoring it`
		if !strings.HasPrefix(err.Error(), want) {
			t.Errorf("PlanRollback(%q):\n%v\nwant it to start %q", target, err, want)
		}
	}

	if err := os.WriteFile(backup, []byte("id: pg_target\nalias: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	plan, err := w.PlanRollback("")
	if err != nil || plan.AutomationID != "pg_target" {
		t.Fatalf("a matching backup: plan %+v, err %v", plan, err)
	}
}
