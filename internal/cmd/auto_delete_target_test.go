package cmd

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// autoDeleteRig stands up one server playing both fake HA and fake companion.
// The companion half accepts exactly the identifier forms the real companion
// accepts — the config id (its GET matches nothing else) — and records every
// id it was handed, so the test can see which string hactl actually sent.
func autoDeleteRig(t *testing.T) (deleted *[]string) {
	t.Helper()
	const configID = "1712345678901"
	states := `[{"entity_id":"automation.climate_schedule","state":"on","attributes":{"id":"1712345678901","friendly_name":"Climate Schedule"}}]`

	var mu sync.Mutex
	var deletedIDs []string
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, states)
		},
		"/v1/config/automation": func(w http.ResponseWriter, r *http.Request) {
			id := r.URL.Query().Get("id")
			w.Header().Set("Content-Type", "application/json")
			switch r.Method {
			case http.MethodGet:
				if id != configID {
					w.WriteHeader(http.StatusNotFound)
					_, _ = fmt.Fprint(w, `{"error":"automation not found"}`)
					return
				}
				_, _ = fmt.Fprintf(w, `{"id":%q,"content":"alias: Climate Schedule\n"}`, configID)
			case http.MethodDelete:
				mu.Lock()
				deletedIDs = append(deletedIDs, id)
				mu.Unlock()
				if id != configID {
					w.WriteHeader(http.StatusNotFound)
					_, _ = fmt.Fprint(w, `{"error":"automation not found"}`)
					return
				}
				_, _ = fmt.Fprint(w, `{"status":"deleted","reloaded":true}`)
			default:
				w.WriteHeader(http.StatusMethodNotAllowed)
			}
		},
	})
	withFlagDir(t, ts.dir)

	// startCmdServer wrote HA_URL/HA_TOKEN; the companion lives on the same
	// server, so discovery can be pointed straight at it.
	env := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=test-token\nCOMPANION_URL=%s\n", ts.srv.URL, ts.srv.URL)
	if err := os.WriteFile(filepath.Join(ts.dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	return &deletedIDs
}

// TestAutoDeleteSendsTheCompanionAnIdItAccepts is D-1 on the confirmed half of
// `auto delete`. The companion's DELETE resolves a config id, an alias, or a
// live entity_id — never the bare object id, which is exactly the string
// `auto ls` prints in its `id` column. hactl used to forward the caller's raw
// reference, so `auto delete <object id>` previewed happily (the live entity
// resolves) and then 404'd under --confirm: a dry run succeeding where the
// confirmed run fails, the inverse of the H-2 contract, on the identifier the
// family itself displays (H-17).
//
// Resolving to the config id — the canonical form (D-1) — makes every printed
// identifier deletable, so the fake companion here accepts only the config id
// and the test asserts on the id it received.
func TestAutoDeleteSendsTheCompanionAnIdItAccepts(t *testing.T) {
	const configID = "1712345678901"
	for _, ref := range []string{
		configID,                      // config id — canonical printed form
		"climate_schedule",            // entity object id — the `id` column of `auto ls`
		"automation.climate_schedule", // full entity_id — `auto show`
		"Climate Schedule",            // alias — friendly_name
	} {
		t.Run(ref, func(t *testing.T) {
			deleted := autoDeleteRig(t)
			oldConfirm := flagAutoConfirm
			flagAutoConfirm = true
			t.Cleanup(func() { flagAutoConfirm = oldConfirm })

			var buf bytes.Buffer
			if err := runAutoDelete(context.Background(), &buf, ref); err != nil {
				t.Fatalf("auto delete --confirm %q: %v\noutput:\n%s", ref, err, buf.String())
			}
			if !strings.Contains(buf.String(), "deleted automation") {
				t.Errorf("auto delete %q reported no deletion: %q", ref, buf.String())
			}
			if len(*deleted) != 1 || (*deleted)[0] != configID {
				t.Errorf("companion DELETE received ids %v, want exactly [%q] — the CLI must "+
					"hand the companion an identifier the companion accepts, whatever form "+
					"the caller held", *deleted, configID)
			}
		})
	}
}

// TestAutoDeleteDryRunStillRefusesUnresolvable is the negative control against
// the rig above: with a live fake HA and a live fake companion answering, a
// fabricated id must still end the command rather than become a plan.
func TestAutoDeleteDryRunStillRefusesUnresolvable(t *testing.T) {
	deleted := autoDeleteRig(t)
	oldConfirm := flagAutoConfirm
	flagAutoConfirm = false
	t.Cleanup(func() { flagAutoConfirm = oldConfirm })

	var buf bytes.Buffer
	err := runAutoDelete(context.Background(), &buf, "totally_bogus_automation_xyz")
	if err == nil {
		t.Fatalf("dry-run planned a delete for an id that names nothing; output:\n%s", buf.String())
	}
	if len(*deleted) != 0 {
		t.Errorf("a refused dry run must not DELETE anything, companion saw %v", *deleted)
	}
}
