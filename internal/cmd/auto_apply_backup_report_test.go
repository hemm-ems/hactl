package cmd

// N-14 (live re-test 2026-11-01): `auto apply --confirm` printed
//
//	time=… level=INFO msg="backup created" path=…
//
// on stderr, next to a result that already says `backup: <path>`. The manual
// ("Write path (automations)"): the result names the backup, "and that is the
// only place it is reported: nothing about the backup goes to stderr at the
// default log level." `auto apply` is the only caller of the writer's backup,
// so the result is a complete report and the log line was noise on the stream
// callers read for refusals and warnings.

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoApplyConfirmReportsTheBackupInTheResultOnly(t *testing.T) {
	for _, jsonMode := range []bool{false, true} {
		name := "text"
		if jsonMode {
			name = "json"
		}
		t.Run(name, func(t *testing.T) {
			logBuf := captureDefaultLogger(t)
			rig := startIdentityRig(t, "pg_target", "pg_target")
			file := rig.file(t, "applied.yaml", "id: pg_target")

			// The confirm guard wants this command's own dry run first.
			if out, err := rig.run(t, "auto", "apply", "pg_target", "-f", file); err != nil {
				t.Fatalf("auto apply (dry run): %v\n%s", err, out)
			}
			args := []string{"auto", "apply", "pg_target", "-f", file, "--confirm"}
			if jsonMode {
				args = append(args, "--json")
			}
			out, err := rig.run(t, args...)
			if err != nil {
				t.Fatalf("auto apply --confirm: %v\n%s", err, out)
			}
			if _, confirms := rig.writes(); confirms != 1 {
				t.Fatalf("precondition: want exactly one confirmed write, got %d", confirms)
			}

			backupDir := filepath.Join(rig.ts.dir, "backups")
			reported := reportedBackup(t, out, jsonMode)
			if !strings.HasPrefix(reported, backupDir+string(filepath.Separator)) {
				t.Errorf("the result does not name the backup under %s (got %q):\n%s", backupDir, reported, out)
			}

			if strings.Contains(logBuf.String(), "backup created") {
				t.Errorf("a confirmed apply logged the backup on stderr at the default level, "+
					"although the result already names it:\n%s", logBuf.String())
			}
		})
	}
}

// reportedBackup is the backup path the result names: the `backup:` line in
// text, `details.backup` under --json.
func reportedBackup(t *testing.T, out string, jsonMode bool) string {
	t.Helper()
	if jsonMode {
		var doc struct {
			Details map[string]any `json:"details"`
		}
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("--json result is not one JSON document: %v\n%s", err, out)
		}
		path, _ := doc.Details["backup"].(string)
		return path
	}
	for line := range strings.SplitSeq(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "backup:"); ok {
			return strings.TrimSpace(rest)
		}
	}
	return ""
}
