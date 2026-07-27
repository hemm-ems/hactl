package cmd

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rollbackRig stands up a fake HA holding one automation whose config id,
// object id and alias are three different strings — the way every UI-authored
// automation is — plus one backup file for it, named the way `auto apply`
// names backups: keyed by the CONFIG id.
func rollbackRig(t *testing.T) (configID string) {
	t.Helper()
	configID = "1712345678901"
	states := `[{"entity_id":"automation.climate_schedule","state":"on","attributes":{"id":"1712345678901","friendly_name":"Climate Schedule"}}]`

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, states)
		},
	})
	withFlagDir(t, ts.dir)

	backupDir := filepath.Join(ts.dir, "backups")
	if err := os.MkdirAll(backupDir, 0o750); err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(backupDir, "2026-07-27T10-00-00_"+configID+".yaml")
	if err := os.WriteFile(backup, []byte("alias: Climate Schedule\ntrigger: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return configID
}

// TestAutoRollbackAcceptsTheIdentifierAutoLsPrints is D-1 applied to the last
// unresolved member of the family (found by TestAutomationRefSurfaceIsClosed,
// watched red on runRollback before this fix).
//
// Backups are keyed by config id, because `auto apply` resolves its target
// before backing up. `auto rollback` matched the caller's raw reference
// against those filenames — so the object id `auto ls` prints, the entity_id,
// and the alias all answered "no backup found for automation …" for an
// automation whose backup existed. A preview that refuses an identifier its
// sibling accepts is the same defect `auto diff`/`apply` had in issue #94.
func TestAutoRollbackAcceptsTheIdentifierAutoLsPrints(t *testing.T) {
	configID := rollbackRig(t)

	for _, ref := range []string{
		configID,                      // config id — canonical printed form
		"climate_schedule",            // entity object id — the `id` column of `auto ls`
		"automation.climate_schedule", // full entity_id — `auto show`
		"Climate Schedule",            // alias — friendly_name
	} {
		t.Run(ref, func(t *testing.T) {
			var buf bytes.Buffer
			if err := runRollback(context.Background(), &buf, ref); err != nil {
				t.Fatalf("auto rollback %q (dry-run): %v", ref, err)
			}
			out := buf.String()
			if !strings.Contains(out, "dry-run") {
				t.Errorf("auto rollback %q previewed nothing: %q", ref, out)
			}
			// The plan names the automation by its config id (D-1: the config
			// id is the canonical printed form) and the backup it resolved.
			if !strings.Contains(out, configID) {
				t.Errorf("auto rollback %q plan does not name config id %q:\n%s", ref, configID, out)
			}
		})
	}
}

// TestAutoRollbackStillRefusesWhatDoesNotExist is the negative control: a
// rollback that resolved every reference to "the most recent backup" would
// satisfy the test above just as well.
func TestAutoRollbackStillRefusesWhatDoesNotExist(t *testing.T) {
	rollbackRig(t)

	var buf bytes.Buffer
	err := runRollback(context.Background(), &buf, "totally_bogus_automation_xyz")
	if err == nil {
		t.Fatalf("dry-run planned a rollback for an id that names no automation and no backup; output:\n%s", buf.String())
	}
}
