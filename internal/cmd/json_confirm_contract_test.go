package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// H-10 on the branch that WROTE.
//
// json_contract_test.go sweeps every READ command's --json. It excludes writes
// by design (classifyCommand → catMutating), and that exclusion is exactly
// where the defect lived: `--json` was honoured on every preview and silently
// dropped the moment `--confirm` was added, in fourteen commands including
// `svc call`, so a caller scripting `--confirm --json` got a JSON parse error
// immediately after a real, successful mutation, at exit 0.
//
// This file is the other half. It drives each write twice against one fake HA —
// once as a preview, once confirmed — and requires BOTH to parse, and to be
// distinguishable by `dry_run` rather than by remembering which flags were
// passed. The two halves together are the executable statement of H-10; the
// `result` surface (dev/surfaces/result.manifest) is the static one, and it is
// the one that quantifies over every write in the tree rather than over the
// ones a fake HA can be made to answer.
// ---------------------------------------------------------------------------

// confirmDriven maps a write command's path (minus "hactl ") to the arguments
// that make it run against buildConfirmFixture's data — positionals and
// command-specific flags, never --json or --confirm, which the sweep adds.
//
// A `nil` entry means "no arguments needed". A command in the tree that is in
// neither this map nor confirmNotDriven fails TestJSONConfirmContract by name,
// so a write added tomorrow cannot join the tree silently.
func confirmDriven(f *confirmFixture) map[string][]string {
	return map[string][]string{
		"area create":      {"Cellar"},
		"area delete":      {"kitchen_id"},
		"floor create":     {"Attic"},
		"floor delete":     {"ground"},
		"label create":     {"Comfort"},
		"label delete":     {"energy"},
		"ent set-area":     {"light.kitchen", "Kitchen"},
		"ent set-label":    {"light.kitchen", "energy"},
		"device set-area":  {"dev1", "Kitchen"},
		"device set-label": {"dev1", "energy"},
		"svc call":         {"light.turn_on", "--data", `{"entity_id":"light.kitchen"}`},
		"script run":       {"wakeup"},
		"dash create":      {"--url-path", "new-dash", "--title", "New Dash"},
		"dash delete":      {"main"},
		"dash save":        {"main", "--file", f.dashConfigFile},
		"dash replace":     {"main", "light.kitchen", "light.pantry"},
		"config delete":    {"entry1"},
		"tpl create":       {"--file", f.tplFile},
		"tpl delete":       {"room_temp"},
		"script create":    {"--file", f.scriptFile},
		"script delete":    {"wakeup"},
		"script apply":     {"wakeup", "--file", f.scriptFile},
		"helper create":    {"input_boolean", "--file", f.helperFile},
		"helper delete":    {"guest_mode"},
		"auto create":      {"--file", f.autoFile},
		"auto delete":      {"morning"},
	}
}

// confirmNotDriven records the writes this sweep does not execute, and why.
//
// It is not an escape hatch: every entry names a reason the fake HA cannot
// produce the situation, and each of these commands is still covered
// statically by the `result` surface, which derives its sites from the source
// and therefore cannot miss them. The distinction the sweep can make and the
// surface cannot is that the bytes really parse; the distinction the surface
// can make and the sweep cannot is that no site was forgotten.
var confirmNotDriven = map[string]string{
	"auto apply":                 "writer.Apply drives HA's automation config API through internal/writer with a backup directory and a validate_config round trip; the confirmed branch renders through done() and is exercised end to end by TestAutoApplyRollbackRoundTrip",
	"auto rollback":              "needs a backup file written by a previous confirmed apply; the rollback path is exercised end to end by TestAutoApplyRollbackRoundTrip",
	"rollback":                   "the deprecated alias dispatches to the same runRollback and the same flag variable as auto rollback, so driving it here would assert the same function twice",
	"ent rename":                 "a rename is two writes (registry + every reference) and reports through the shared ref-replace table, which renders as JSON already; TestE2EEntRenameCLI drives it against a real HA and a real companion",
	"ref replace":                "reports through the same shared ref-replace table as ent rename, which honours --json on both branches; the confirmed run additionally needs writable dashboards",
	"config flow-start":          "a config flow is server-side state HA hands out and expires; the result goes through renderFlowResult, which writes HA's own JSON document verbatim under --json",
	"config flow-step":           "same renderFlowResult path, and a step needs a live flow id from a flow HA actually started",
	"config options":             "same renderFlowResult path, over an options flow that only exists while HA holds it open",
	"companion wireguard config": "writes a .conf inside the add-on container; the tunnel is not HA state and the command's result comes from the companion's own JSON response",
	"companion wireguard up":     "starts an interface inside the add-on container; same absence of any HA-side state to fake",
	"companion wireguard down":   "stops that interface; same absence of any HA-side state to fake",
}

// confirmFixture is the write-capable sibling of contractFixture: one fake HA
// plus one companion stub that ACCEPT writes, and the temp files the create
// commands read their payloads from.
type confirmFixture struct {
	dir            string
	dashConfigFile string
	tplFile        string
	scriptFile     string
	helperFile     string
	autoFile       string
}

// buildConfirmFixture stands up a fake HA whose registries, services, lovelace
// collection and config entries all accept a write, plus a companion stub that
// accepts the four YAML-backed families.
//
// The stubs are deliberately stateless: a dry run must leave them exactly as a
// confirmed run found them, so the same fixture can answer both halves of every
// case without the order of the two runs mattering.
func buildConfirmFixture(t *testing.T) *confirmFixture {
	t.Helper()

	states := []map[string]any{
		{
			"entity_id": "light.kitchen", "state": "on",
			"last_changed": "2026-01-01T09:00:00+00:00", "last_updated": "2026-01-01T09:00:00+00:00",
			"attributes": map[string]any{"friendly_name": "Kitchen Light"},
		},
		{
			"entity_id": "script.wakeup", "state": "on",
			"last_changed": "2026-01-01T07:00:00+00:00", "last_updated": "2026-01-01T07:00:00+00:00",
			"attributes": map[string]any{"friendly_name": "Wake Up", "mode": "single", "current": 0},
		},
		{
			"entity_id": "automation.morning", "state": "on",
			"last_changed": "2026-01-01T08:00:00+00:00", "last_updated": "2026-01-01T08:00:00+00:00",
			"attributes": map[string]any{
				"friendly_name": "Morning Routine", "mode": "single", "current": 0,
				"id": "morning", "last_triggered": "2026-01-01T08:00:00+00:00",
			},
		},
	}

	httpHandlers := map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(states)
		},
		"/api/states/": func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimPrefix(r.URL.Path, "/api/states/")
			for _, s := range states {
				if s["entity_id"] == id {
					w.Header().Set("Content-Type", "application/json")
					_ = json.NewEncoder(w).Encode(s)
					return
				}
			}
			http.Error(w, "not found", http.StatusNotFound)
		},
		// The service registry `svc call` probes before it plans (H-2), and
		// the call endpoint itself.
		"/api/services": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `[{"domain":"light","services":{"turn_on":{}}},{"domain":"script","services":{"turn_on":{},"reload":{}}}]`)
		},
		"/api/services/": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `[]`)
		},
		"/api/config/config_entries/entry": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `[{"entry_id":"entry1","domain":"mydomain","title":"My Domain","state":"loaded","source":"user","supports_options":true}]`)
		},
		"/api/config/config_entries/entry/": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"require_restart":false}`)
		},
		"/api/config/core/check_config": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"result":"valid","errors":null}`)
		},
	}

	lovelaceConfig := map[string]any{
		"views": []map[string]any{
			{"title": "Main", "path": "main", "cards": []map[string]any{
				{"type": "entities", "entities": []string{"light.kitchen"}},
			}},
		},
	}

	wsResponses := map[string]any{
		"config/area_registry/list":    []map[string]any{{"area_id": "kitchen_id", "name": "Kitchen"}},
		"config/area_registry/create":  map[string]any{"area_id": "cellar", "name": "Cellar"},
		"config/area_registry/delete":  map[string]any{},
		"config/floor_registry/list":   []map[string]any{{"floor_id": "ground", "name": "Ground", "level": 0}},
		"config/floor_registry/create": map[string]any{"floor_id": "attic", "name": "Attic", "level": 2},
		"config/floor_registry/delete": map[string]any{},
		"config/label_registry/list":   []map[string]any{{"label_id": "energy", "name": "Energy"}},
		"config/label_registry/create": map[string]any{"label_id": "comfort", "name": "Comfort"},
		"config/label_registry/delete": map[string]any{},
		"config/device_registry/list": []map[string]any{
			{"id": "dev1", "name": "My Device", "manufacturer": "Acme", "model": "X1", "area_id": "kitchen_id"},
		},
		"config/device_registry/update": map[string]any{"id": "dev1", "name": "My Device", "area_id": "kitchen_id"},
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "light.kitchen", "device_id": "dev1", "area_id": "kitchen_id", "labels": []string{"energy"}},
			{"entity_id": "script.wakeup"},
			{"entity_id": "automation.morning"},
		},
		"config/entity_registry/update": map[string]any{"entity_id": "light.kitchen", "area_id": "kitchen_id"},
		"config/entity_registry/remove": map[string]any{},
		"lovelace/dashboards/list": []map[string]any{
			{"id": "dash1", "url_path": "main", "title": "Main", "mode": "storage"},
		},
		"lovelace/dashboards/create": map[string]any{"id": "dash2", "url_path": "new-dash", "title": "New Dash", "mode": "storage"},
		"lovelace/dashboards/delete": map[string]any{},
		"lovelace/config":            lovelaceConfig,
		"lovelace/config/save":       map[string]any{},
		// `script apply --confirm` refuses outright when HA's validate_config
		// is unavailable, so the fake HA has to answer it for the confirmed
		// half of that case to exist at all.
		"validate_config": map[string]any{
			"triggers":   map[string]any{"valid": true, "error": nil},
			"conditions": map[string]any{"valid": true, "error": nil},
			"actions":    map[string]any{"valid": true, "error": nil},
		},
	}

	ts := startCmdServer(t, wsResponses, httpHandlers)
	cc := startConfirmCompanion(t)

	envPath := filepath.Join(ts.dir, ".env")
	env, err := os.ReadFile(envPath) //nolint:gosec // path from t.TempDir(), not user input
	if err != nil {
		t.Fatalf("reading fixture .env: %v", err)
	}
	env = fmt.Appendf(env, "COMPANION_URL=%s\nCOMPANION_TOKEN=test-token\n", cc.URL)
	if err := os.WriteFile(envPath, env, 0o600); err != nil { //nolint:gosec // fixture dir from t.TempDir()
		t.Fatalf("wiring COMPANION_URL into fixture .env: %v", err)
	}

	write := func(name, content string) string {
		t.Helper()
		p := filepath.Join(ts.dir, name)
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatalf("writing fixture file %s: %v", name, err)
		}
		return p
	}

	return &confirmFixture{
		dir:            ts.dir,
		dashConfigFile: write("dash.json", `{"views":[{"title":"Main","path":"main","cards":[]}]}`),
		tplFile:        write("tpl.yaml", "unique_id: room_temp\nname: Room Temp\nstate: \"{{ 21 }}\"\n"),
		scriptFile:     write("script.yaml", "wakeup:\n  alias: Wake Up\n  sequence:\n    - delay: \"00:00:01\"\n"),
		helperFile:     write("helper.yaml", "guest_mode:\n  name: Guest Mode\n"),
		autoFile:       write("auto.yaml", "id: morning\nalias: Morning Routine\ntriggers: []\nactions: []\n"),
	}
}

// startConfirmCompanion answers the YAML-backed write families. Every route is
// method-dispatched exactly as internal/companion/client.go calls it: GET reads
// the definition (which every delete and apply resolves before it plans, H-2),
// POST creates, PUT applies, DELETE removes.
func startConfirmCompanion(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()

	ok := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}

	mux.HandleFunc("/v1/health", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, `{"status":"ok","version":"2026.7.8"}`)
	})
	mux.HandleFunc("/v1/status", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, `{"version":"2026.7.8","supervisor_reachable":true,"has_ha_cli":true,"config_writable":true,"ingress_active":false,"auth_mode":"bearer"}`)
	})
	mux.HandleFunc("/v1/config/template", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			ok(w, `{"status":"ok","unique_id":"room_temp","domain":"sensor","content":"unique_id: room_temp\nname: Room Temp\n"}`)
		case http.MethodPost:
			ok(w, `{"status":"ok","unique_id":"room_temp","domain":"sensor","reloaded":true}`)
		case http.MethodDelete:
			ok(w, `{"status":"ok","deleted":true,"backup":".hactl_backups/template.yaml.bak"}`)
		default:
			ok(w, `{"status":"ok","written":true,"reloaded":true}`)
		}
	})
	mux.HandleFunc("/v1/config/script", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			ok(w, `{"status":"ok","id":"wakeup","content":"wakeup:\n  alias: Wake Up\n  sequence: []\n"}`)
		case http.MethodPost:
			ok(w, `{"status":"ok","id":"wakeup","reloaded":true}`)
		case http.MethodDelete:
			ok(w, `{"status":"ok","deleted":true,"backup":".hactl_backups/scripts.yaml.bak"}`)
		default:
			ok(w, `{"status":"ok","written":true,"reloaded":true,"backup":".hactl_backups/scripts.yaml.bak"}`)
		}
	})
	mux.HandleFunc("/v1/config/automation", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			ok(w, `{"status":"ok","id":"morning","content":"id: morning\nalias: Morning Routine\n"}`)
		case http.MethodPost:
			ok(w, `{"status":"ok","id":"morning","entity_id":"automation.morning","reloaded":true}`)
		case http.MethodDelete:
			ok(w, `{"status":"ok","deleted":true,"backup":".hactl_backups/automations.yaml.bak"}`)
		default:
			ok(w, `{"status":"ok","written":true,"reloaded":true}`)
		}
	})
	mux.HandleFunc("/v1/config/helper", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			ok(w, `{"status":"ok","id":"guest_mode","domain":"input_boolean","content":"guest_mode:\n  name: Guest Mode\n"}`)
		case http.MethodPost:
			ok(w, `{"status":"ok","id":"guest_mode","domain":"input_boolean","entity_id":"input_boolean.guest_mode","reloaded":true,"entity_created":true}`)
		case http.MethodDelete:
			ok(w, `{"status":"ok","deleted":true,"backup":".hactl_backups/configuration.yaml.bak"}`)
		default:
			ok(w, `{"status":"ok","written":true,"reloaded":true}`)
		}
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// TestJSONConfirmContract drives every write the fixture can answer, twice, and
// requires both documents to parse — the preview as a plan, the confirmed run
// as a result.
//
// The closure clause is the important one: a --confirm-gated command in the
// live cobra tree that appears in neither confirmDriven nor confirmNotDriven
// fails this test by name. "N write commands ignore --json" becomes a build
// failure here rather than something a human has to notice against a real HA,
// which is how the defect this test exists for reached a release.
func TestJSONConfirmContract(t *testing.T) {
	rootCmd.InitDefaultHelpCmd()
	rootCmd.InitDefaultCompletionCmd()

	var writes []*confirmWrite
	for _, leaf := range leafCommands(rootCmd) {
		if !isMutating(leaf) {
			continue
		}
		args := cmdArgsOf(leaf)
		writes = append(writes, &confirmWrite{path: strings.Join(args, " "), args: args})
	}
	if len(writes) == 0 {
		t.Fatal("no --confirm-gated command in the tree — the walk has stopped matching")
	}
	sort.Slice(writes, func(i, j int) bool { return writes[i].path < writes[j].path })

	var driven, excluded []string
	for _, wcmd := range writes {
		// One fixture per command: a confirmed delete must not be able to
		// change the answer a later command's preview reads.
		f := buildConfirmFixture(t)
		extra, ok := confirmDriven(f)[wcmd.path]
		if !ok {
			if reason, known := confirmNotDriven[wcmd.path]; known {
				if len(reason) < 40 {
					t.Errorf("H-10 confirm sweep: %q is excluded with a reason too thin to be one: %q", wcmd.path, reason)
				}
				excluded = append(excluded, wcmd.path)
				continue
			}
			t.Errorf("H-10 confirm sweep: write command %q is in neither confirmDriven nor confirmNotDriven — "+
				"drive it, or record why the fixture cannot, so the gap is not silent", wcmd.path)
			continue
		}
		driven = append(driven, wcmd.path)
		t.Run(strings.ReplaceAll(wcmd.path, " ", "_"), func(t *testing.T) {
			assertWriteJSONContract(t, f.dir, wcmd.args, extra)
		})
	}

	t.Logf("H-10 confirm sweep: drove %d write command(s) through preview AND --confirm under --json: %s",
		len(driven), strings.Join(driven, ", "))
	t.Logf("H-10 confirm sweep: %d write(s) not driven here (static coverage via dev/surfaces/result.manifest): %s",
		len(excluded), strings.Join(excluded, ", "))
}

// confirmWrite is one --confirm-gated leaf of the live tree.
type confirmWrite struct {
	path string
	args []string
}

// assertWriteJSONContract runs one write as a preview and as a confirmed run,
// under --json, and asserts the machine contract on both.
func assertWriteJSONContract(t *testing.T, dir string, cmdArgs, extra []string) {
	t.Helper()

	run := func(confirm bool) string {
		t.Helper()
		args := []string{"hactl"}
		args = append(args, cmdArgs...)
		args = append(args, extra...)
		args = append(args, "--dir", dir, "--json")
		if confirm {
			args = append(args, "--confirm")
		}
		var buf bytes.Buffer
		if err := RunWithOutput(args, &buf); err != nil {
			t.Fatalf("command failed: %v\nargs: %v\noutput: %s", err, args[1:], buf.String())
		}
		return buf.String()
	}

	for _, tc := range []struct {
		name       string
		confirm    bool
		wantDryRun bool
	}{
		{"preview", false, true},
		{"confirmed", true, false},
	} {
		out := run(tc.confirm)

		// (1) stdout parses strictly as JSON, with nothing else on it.
		var doc map[string]any
		if err := json.Unmarshal([]byte(out), &doc); err != nil {
			t.Fatalf("%s: --json output does not parse as a JSON object: %v\noutput:\n%s", tc.name, err, out)
		}

		// (2) no human header line: the first non-whitespace byte opens the
		// document. `auto create` shipped a prose validation line ahead of a
		// valid object, which parses nowhere.
		trimmed := strings.TrimLeft(out, " \t\r\n")
		if len(trimmed) == 0 || trimmed[0] != '{' {
			t.Fatalf("%s: --json output does not start with a JSON object (a human header line precedes it):\n%s", tc.name, out)
		}

		// (3) a caller can tell a plan from a result by reading the document,
		// not by remembering which flags it passed. This is the clause the
		// defect broke: with --confirm the answer was prose, so there was
		// nothing to read at all.
		dryRun, ok := doc["dry_run"].(bool)
		if !ok {
			t.Errorf("%s: --json output carries no boolean `dry_run` field, so a plan is indistinguishable from a result: %s", tc.name, out)
		} else if dryRun != tc.wantDryRun {
			t.Errorf("%s: dry_run = %v, want %v\n%s", tc.name, dryRun, tc.wantDryRun, out)
		}
		if action, _ := doc["action"].(string); action == "" {
			t.Errorf("%s: --json output carries no `action` naming what was planned or done: %s", tc.name, out)
		}
		if !tc.wantDryRun {
			if okField, hasOK := doc["ok"].(bool); !hasOK || !okField {
				t.Errorf("confirmed: --json output does not report `ok: true` for a write that succeeded: %s", out)
			}
		}
	}
}
