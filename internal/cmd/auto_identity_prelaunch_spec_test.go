package cmd

// Blind spec tests for two pre-existing `auto` defects from the live test of
// 2026-11-01, written from the contract only — docs/decisions.md D-130 and the
// manual's "Write path (automations)" and "Automations" prose — without reading
// internal/cmd/auto.go:
//
//   - B-2 (F-13): `auto create` claimed "live entity_id (automation.x) differs
//     from config id (x)" when the two agreed.
//   - B-19 (D-130, issue #169): `auto apply` and `auto rollback` silently
//     replaced the target with an automation of another identity when the
//     file's (or backup's) `id:` differed from the target's config id or was
//     missing. They now refuse, with or without a policy, in the dry run
//     already: exit 1, the refusal on stderr, empty stdout, nothing validated,
//     rehearsed or written.
//
// Every case drives the real command tree through RunWithOutput, so it reads
// what a caller reads: stdout, and the error that becomes stderr + exit code.

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// --- B-2: auto create's entity_id note ----------------------------------------

// createNoteCompanion is a companion whose automation create answers with the
// given config id and live entity_id — the two values the note compares.
func createNoteCompanion(t *testing.T, id, entityID string) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/config/automation", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":"ok","id":%q,"entity_id":%q,"reloaded":true}`, id, entityID)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestAutoCreateNotesTheEntityIDOnlyWhenItDiffers — the manual (Automations):
// "when the entity's object id (the part after `automation.`) is not the config
// id, the result adds `note: live entity_id (automation.<x>) differs from config
// id (<id>)`. When they agree there is no note." The note compared the full
// entity_id with the bare config id, so it fired on every create (F-13).
func TestAutoCreateNotesTheEntityIDOnlyWhenItDiffers(t *testing.T) {
	for _, tc := range []struct {
		name, entityID string
		wantNote       bool
	}{
		{"object id equals config id", "automation.morning", false},
		{"object id differs from config id", "automation.good_morning", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			companionURL := createNoteCompanion(t, "morning", tc.entityID)
			dir := t.TempDir()
			env := fmt.Sprintf("HA_URL=http://127.0.0.1:19999\nHA_TOKEN=test\nCOMPANION_URL=%s\n", companionURL)
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(dir, "payload.yaml")
			if err := os.WriteFile(payload, []byte("id: morning\nalias: Morning\ntriggers: []\nconditions: []\nactions: []\n"), 0o600); err != nil {
				t.Fatal(err)
			}

			out, err := runCLI(t, "auto", "create", "--file", payload, "--confirm", "--dir", dir)
			if err != nil {
				t.Fatalf("auto create --confirm: %v\n%s", err, out)
			}
			note := "note: live entity_id (" + tc.entityID + ") differs from config id (morning)"
			if tc.wantNote && !strings.Contains(out, note) {
				t.Errorf("the entity's object id differs from the config id, but the result has no %q:\n%s", note, out)
			}
			if !tc.wantNote && strings.Contains(out, "differs from config id") {
				t.Errorf("entity_id %s agrees with config id morning, yet the result claims they differ:\n%s", tc.entityID, out)
			}
		})
	}
}

// --- B-19: the file's id: must be the target's config id ---------------------

// identityRig is a Home Assistant holding one automation — config id, entity
// object id and alias as given — and a companion serving its single entry.
// It counts every write the companion receives (dry_run rehearsals included)
// and every validate_config, so a test can say "nothing validated, rehearsed or
// written" from what reached the wire.
type identityRig struct {
	ts       *cmdTestServer
	configID string

	mu       sync.Mutex
	puts     int // PUT /v1/config/automation, dry_run or not
	confirms int // PUT /v1/config/automation with dry_run=false
}

func startIdentityRig(t *testing.T, configID, objectID string) *identityRig {
	t.Helper()
	rig := &identityRig{configID: configID}
	stored := fmt.Sprintf("id: '%s'\nalias: Target\ntriggers: []\nconditions: []\nactions: []\n", configID)
	states := fmt.Sprintf(`[{"entity_id":"automation.%s","state":"on","attributes":{"id":%q,"friendly_name":"Target"}}]`, objectID, configID)
	handlers := map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, states)
		},
		"/v1/config/automation": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("id") != configID {
				http.Error(w, "Automation not found: "+r.URL.Query().Get("id"), http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			switch r.Method {
			case http.MethodGet:
				payload, _ := json.Marshal(map[string]string{"id": configID, "content": stored})
				_, _ = w.Write(payload)
			case http.MethodPut:
				_, _ = io.ReadAll(r.Body)
				rig.mu.Lock()
				rig.puts++
				if r.URL.Query().Get("dry_run") != "true" {
					rig.confirms++
				}
				rig.mu.Unlock()
				_, _ = fmt.Fprint(w, `{"status":"applied","reloaded":true}`)
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		},
	}
	rig.ts = startCmdServer(t, map[string]any{
		"validate_config": map[string]any{
			"triggers":   map[string]any{"valid": true, "error": nil},
			"conditions": map[string]any{"valid": true, "error": nil},
			"actions":    map[string]any{"valid": true, "error": nil},
		},
	}, handlers)
	pointAtCompanion(t, rig.ts)
	return rig
}

func (r *identityRig) writes() (puts, confirms int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.puts, r.confirms
}

// file writes a candidate whose first line is idLine ("" for no id: at all).
func (r *identityRig) file(t *testing.T, name, idLine string) string {
	t.Helper()
	body := "alias: Target\ntriggers: []\nconditions: []\nactions:\n  - delay: '00:00:05'\n"
	if idLine != "" {
		body = idLine + "\n" + body
	}
	path := filepath.Join(r.ts.dir, name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// backup writes a backup file for the target, named the way `auto apply`
// names its backups (keyed by the config id), holding idLine.
func (r *identityRig) backup(t *testing.T, stamp, idLine string) string {
	t.Helper()
	dir := filepath.Join(r.ts.dir, "backups")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	body := "alias: Target\ntriggers: []\nconditions: []\nactions: []\n"
	if idLine != "" {
		body = idLine + "\n" + body
	}
	path := filepath.Join(dir, stamp+"_"+r.configID+".yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// identityExitCode is the exit code cmd/hactl/main.go makes of err.
func identityExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

func (r *identityRig) run(t *testing.T, args ...string) (string, error) {
	t.Helper()
	return runCLI(t, append(args, "--dir", r.ts.dir)...)
}

// assertIdentityRefusal: D-130 — exit 1, empty stdout (no diff printed first),
// the error naming both ids and the deliberate paths, and nothing validated,
// rehearsed or written.
func assertIdentityRefusal(t *testing.T, rig *identityRig, what, out string, err error, wantPhrase, oldID string) {
	t.Helper()
	if identityExitCode(err) != 1 {
		t.Fatalf("%s: exit %d (%v), want 1 — a candidate of another identity must be refused\n%s", what, identityExitCode(err), err, out)
	}
	if out != "" {
		t.Errorf("%s: stdout %q, want empty — the refusal comes before any diff or plan", what, out)
	}
	msg := err.Error()
	for _, want := range []string{
		wantPhrase,
		"hactl auto create -f",       // a new config id, deliberately
		"hactl auto delete " + oldID, // … then retire the old one
		"hactl ent rename",           // a new entity id, deliberately
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("%s: the error does not say %q:\n%s", what, want, msg)
		}
	}
	if puts, _ := rig.writes(); puts != 0 {
		t.Errorf("%s: %d writes reached the companion (dry_run rehearsals included), want 0", what, puts)
	}
	if n := rig.ts.commandCount("validate_config"); n != 0 {
		t.Errorf("%s: validate_config sent %d times, want 0 — nothing is validated before the identity check", what, n)
	}
}

// TestAutoApplyRefusesAFileOfAnotherIdentity — D-130: a differing id, a
// missing one, and a non-string one whose text matches are each refused in the
// dry run and under --confirm, with the manual's wording.
func TestAutoApplyRefusesAFileOfAnotherIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, configID, objectID, idLine, phrase string
	}{
		{"differing id", "pg_target", "pg_target", "id: pg_other",
			`has id "pg_other", not the config id of the automation it would replace ("pg_target")`},
		{"missing id", "pg_target", "pg_target", "",
			`has no id, not the config id of the automation it would replace ("pg_target")`},
		{"unquoted number matching the config id", "1712345678901", "climate_schedule", "id: 1712345678901",
			`has id 1712345678901 (a number — quote it), not the config id of the automation it would replace ("1712345678901")`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range [][]string{nil, {"--confirm"}} {
				rig := startIdentityRig(t, tc.configID, tc.objectID)
				file := rig.file(t, "new.yaml", tc.idLine)
				args := append([]string{"auto", "apply", tc.configID, "-f", file}, mode...)
				out, err := rig.run(t, args...)
				assertIdentityRefusal(t, rig, strings.Join(args, " "), out, err, tc.phrase, tc.configID)
				if _, confirms := rig.writes(); confirms != 0 {
					t.Errorf("%v: a confirmed write landed", args)
				}
				if entries, _ := os.ReadDir(filepath.Join(rig.ts.dir, "backups")); len(entries) != 0 {
					t.Errorf("%v: a refused apply wrote a backup", args)
				}
			}
		})
	}
}

// TestAutoApplyRefusesAForeignIDWhateverTheTargetIsCalled — the rule compares
// with the target's config id, not with the reference typed on the command
// line: naming the target by its entity object id or alias does not make a
// file carrying that object id acceptable.
func TestAutoApplyRefusesAForeignIDWhateverTheTargetIsCalled(t *testing.T) {
	for _, ref := range []string{"climate_schedule", "automation.climate_schedule", "Target"} {
		t.Run(ref, func(t *testing.T) {
			rig := startIdentityRig(t, "1712345678901", "climate_schedule")
			file := rig.file(t, "new.yaml", "id: climate_schedule")
			out, err := rig.run(t, "auto", "apply", ref, "-f", file)
			assertIdentityRefusal(t, rig, "auto apply "+ref, out, err,
				`has id "climate_schedule", not the config id of the automation it would replace ("1712345678901")`, "1712345678901")
		})
	}
}

// TestAutoApplyKeepsAcceptingAMatchingID — the control: "Nothing changes for a
// file whose `id:` matches." A rule that refused every file would satisfy the
// refusals above just as well.
func TestAutoApplyKeepsAcceptingAMatchingID(t *testing.T) {
	for _, tc := range []struct{ name, configID, objectID, idLine string }{
		{"plain id", "pg_target", "pg_target", "id: pg_target"},
		{"quoted timestamp id", "1712345678901", "climate_schedule", "id: '1712345678901'"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := startIdentityRig(t, tc.configID, tc.objectID)
			file := rig.file(t, "new.yaml", tc.idLine)
			out, err := rig.run(t, "auto", "apply", tc.objectID, "-f", file)
			if err != nil {
				t.Fatalf("auto apply with a matching id: %v\n%s", err, out)
			}
			if !strings.Contains(out, "dry-run") {
				t.Errorf("auto apply with a matching id previewed nothing:\n%s", out)
			}
		})
	}
}

// TestAutoRollbackRefusesABackupOfAnotherIdentity — D-130: rollback (and the
// deprecated top-level `rollback`) applies the same rule to the backup it
// would restore, against the id it would restore it to — the resolved config
// id, or the id in the backup's file name when no target is given.
func TestAutoRollbackRefusesABackupOfAnotherIdentity(t *testing.T) {
	for _, tc := range []struct {
		name, idLine, phrase string
	}{
		{"differing id", "id: pg_other",
			`has id "pg_other", not the config id of the automation it would restore ("pg_target")`},
		{"missing id", "",
			`has no id, not the config id of the automation it would restore ("pg_target")`},
	} {
		for _, args := range [][]string{
			{"auto", "rollback", "pg_target"},
			{"auto", "rollback"}, // the target from the backup's name
			{"rollback", "pg_target"},
			{"auto", "rollback", "pg_target", "--confirm"},
		} {
			t.Run(tc.name+"/"+strings.Join(args, " "), func(t *testing.T) {
				rig := startIdentityRig(t, "pg_target", "pg_target")
				path := rig.backup(t, "2026-11-01T09-00-00", tc.idLine)
				out, err := rig.run(t, args...)
				assertIdentityRefusal(t, rig, strings.Join(args, " "), out, err, tc.phrase, "pg_target")
				if !strings.Contains(err.Error(), "backup ") || !strings.Contains(err.Error(), filepath.Base(path)) {
					t.Errorf("the refusal does not name the backup it would restore (%s):\n%v", filepath.Base(path), err)
				}
			})
		}
	}
}

// TestAutoRollbackKeepsRestoringAMatchingBackup — the control for the
// rollback half.
func TestAutoRollbackKeepsRestoringAMatchingBackup(t *testing.T) {
	rig := startIdentityRig(t, "pg_target", "pg_target")
	rig.backup(t, "2026-11-01T09-00-00", "id: pg_target")
	out, err := rig.run(t, "auto", "rollback", "pg_target")
	if err != nil {
		t.Fatalf("auto rollback of a matching backup: %v\n%s", err, out)
	}
	if !strings.Contains(out, "dry-run") {
		t.Errorf("auto rollback of a matching backup previewed nothing:\n%s", out)
	}
}
