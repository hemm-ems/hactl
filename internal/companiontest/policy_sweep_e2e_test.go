//go:build companion

package companiontest

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/hemm-ems/hactl/docs"
	"github.com/hemm-ems/hactl/internal/cmd"
	"github.com/hemm-ems/hactl/internal/surfaceaudit"
)

// ============================================================================
// W-6: the deny-all sweeps on the companion tier (SPEC-policy-usp.md §0, W-6,
// G-6). The integration tier (internal/integration/policy_sweep_test.go)
// sweeps against Home Assistant alone, where the commands that read the
// companion before they can plan end on its absence; here every write
// command reaches its plan, so every one must be refused by the policy, and
// every read command that needs the companion is read through it.
//
// The truth is read outside hactl (H-12): registries, dashboards, config
// entries and states over a raw websocket and REST, every YAML file under
// /config and the companion's backup directory from inside the container,
// and Home Assistant's event bus.
// ============================================================================

const e2eSweepProfile = "pg_sweep"

var e2eSweepWritePolicy = "version: 1\nactive: " + e2eSweepProfile + "\nprofiles:\n  " + e2eSweepProfile + ":\n" +
	"    default: deny\n    allow:\n      \"*\": [read]\n"

var e2eSweepReadPolicy = "version: 1\nactive: " + e2eSweepProfile + "\nprofiles:\n  " + e2eSweepProfile + ":\n" +
	"    default: deny\n"

// e2eSweepDir is an instance directory with the tier's credentials holding
// policy as policy.yaml.
func e2eSweepDir(t *testing.T, policy string) string {
	t.Helper()
	dir, err := createE2EInstanceDir(haURL, haToken, compURL, companionToken)
	if err != nil {
		t.Fatalf("creating instance dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// ---------------------------------------------------------------------------
// A raw websocket outside hactl.
// ---------------------------------------------------------------------------

type e2eSweepWS struct {
	conn *websocket.Conn
	next int
}

type e2eSweepEnvelope struct {
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Error   struct {
		Code string `json:"code"`
	} `json:"error"`
}

func dialE2ESweepWS(t *testing.T) *e2eSweepWS {
	t.Helper()
	u, err := url.Parse(haURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Scheme, u.Path = "ws", "/api/websocket"
	conn, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("ws connect: %v", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	t.Cleanup(func() { _ = conn.Close() })
	var msg map[string]any
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(map[string]string{"type": "auth", "access_token": haToken}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&msg); err != nil || msg["type"] != "auth_ok" {
		t.Fatalf("ws auth: %v %v", err, msg)
	}
	return &e2eSweepWS{conn: conn}
}

func (w *e2eSweepWS) send(t *testing.T, msg map[string]any) e2eSweepEnvelope {
	t.Helper()
	w.next++
	msg["id"] = w.next
	if err := w.conn.WriteJSON(msg); err != nil {
		t.Fatalf("sending %v: %v", msg["type"], err)
	}
	var env e2eSweepEnvelope
	if err := w.conn.ReadJSON(&env); err != nil {
		t.Fatalf("reading %v: %v", msg["type"], err)
	}
	return env
}

func (w *e2eSweepWS) must(t *testing.T, msg map[string]any) json.RawMessage {
	t.Helper()
	typ := msg["type"]
	env := w.send(t, msg)
	if !env.Success {
		t.Fatalf("%v failed: %s", typ, env.Error.Code)
	}
	return env.Result
}

// ---------------------------------------------------------------------------
// The oracle: a raw snapshot, and the event bus.
// ---------------------------------------------------------------------------

var e2eSweepStateDomains = map[string]bool{
	"automation": true, "button": true, "counter": true, "input_boolean": true, "input_button": true,
	"input_datetime": true, "input_number": true, "input_select": true, "input_text": true, "light": true,
	"lock": true, "scene": true, "script": true, "switch": true, "timer": true,
}

func e2eSweepCanonical(t *testing.T, what string, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s did not parse: %v", what, err)
	}
	b, _ := json.MarshalIndent(v, "", " ")
	return string(b)
}

// e2eSweepSnapshot reads Home Assistant's registries, dashboards and their
// stored configs, config entries, the states of the domains only a write
// changes, and — from inside the container — a hash of every YAML file
// under /config and the companion's backups.
func e2eSweepSnapshot(t *testing.T) map[string]string {
	t.Helper()
	ws := dialE2ESweepWS(t)
	snap := map[string]string{}
	for _, typ := range []string{
		"config/entity_registry/list", "config/device_registry/list", "config/area_registry/list",
		"config/floor_registry/list", "config/label_registry/list", "lovelace/dashboards/list", "config_entries/get",
	} {
		snap[typ] = e2eSweepCanonical(t, typ, ws.must(t, map[string]any{"type": typ}))
	}
	var dashes []struct {
		URLPath string `json:"url_path"`
	}
	_ = json.Unmarshal(ws.must(t, map[string]any{"type": "lovelace/dashboards/list"}), &dashes)
	paths := make([]string, 0, 1+len(dashes))
	paths = append(paths, "")
	for _, d := range dashes {
		paths = append(paths, d.URLPath)
	}
	sort.Strings(paths)
	for _, u := range paths {
		msg := map[string]any{"type": "lovelace/config"}
		if u != "" {
			msg["url_path"] = u
		}
		env := ws.send(t, msg)
		if env.Success {
			snap["lovelace/config "+u] = e2eSweepCanonical(t, u, env.Result)
		} else {
			snap["lovelace/config "+u] = "error " + env.Error.Code
		}
	}

	code, body := w1HARest(t, http.MethodGet, "/api/states", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/states: %d", code)
	}
	var states []struct {
		ID          string         `json:"entity_id"`
		State       string         `json:"state"`
		LastChanged string         `json:"last_changed"`
		Attributes  map[string]any `json:"attributes"`
	}
	if err := json.Unmarshal(body, &states); err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, s := range states {
		d, _, _ := strings.Cut(s.ID, ".")
		if e2eSweepStateDomains[d] {
			lines = append(lines, fmt.Sprintf("%s state=%s last_changed=%s last_triggered=%v", s.ID, s.State, s.LastChanged, s.Attributes["last_triggered"]))
		}
	}
	sort.Strings(lines)
	snap["states"] = strings.Join(lines, "\n")

	var out bytes.Buffer
	c := exec.CommandContext(t.Context(), "docker", "exec", "hactl-companion-test-ha", "sh", "-c",
		"cd /config && find . \\( -name .storage -o -name deps -o -name tts \\) -prune -o -type f \\( -name '*.yaml' -o -name '*.yml' \\) -print | sort | xargs sha1sum; "+
			"echo '--- backups'; ls -1 .hactl_backups 2>/dev/null | sort")
	c.Stdout, c.Stderr = &out, &out
	if err := c.Run(); err != nil {
		t.Fatalf("hashing /config in the container: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "./configuration.yaml") {
		t.Fatalf("the file half of the snapshot sees no configuration.yaml:\n%s", out.String())
	}
	snap["files"] = out.String()
	return snap
}

func assertE2ESweepUnchanged(t *testing.T, before, after map[string]string) {
	t.Helper()
	keys := make([]string, 0, len(before)+len(after))
	for k := range before {
		keys = append(keys, k)
	}
	for k := range after {
		if _, ok := before[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if before[k] == after[k] {
			continue
		}
		bl, al := strings.Split(before[k], "\n"), strings.Split(after[k], "\n")
		i := 0
		for i < len(bl) && i < len(al) && bl[i] == al[i] {
			i++
		}
		var was, now string
		if i < len(bl) {
			was = bl[i]
		}
		if i < len(al) {
			now = al[i]
		}
		t.Errorf("Home Assistant changed under the deny-all profile — %s, first difference at line %d:\n  before: %s\n  after:  %s", k, i+1, was, now)
	}
}

func e2eSweepWriteEvent(eventType string) bool {
	switch eventType {
	case "call_service", "automation_triggered", "script_started", "lovelace_updated",
		"panels_updated", "service_registered", "service_removed", "component_loaded",
		"core_config_updated", "themes_updated", "user_added", "user_removed", "user_updated":
		return true
	}
	return strings.HasSuffix(eventType, "_registry_updated") || strings.HasSuffix(eventType, "_reloaded")
}

type e2eSweepEvents struct {
	mu   sync.Mutex
	seen []string
}

func recordE2ESweepEvents(t *testing.T) *e2eSweepEvents {
	t.Helper()
	ws := dialE2ESweepWS(t)
	if env := ws.send(t, map[string]any{"type": "subscribe_events"}); !env.Success {
		t.Fatalf("subscribe_events refused: %s", env.Error.Code)
	}
	e := &e2eSweepEvents{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			var ev struct {
				Event struct {
					Type string         `json:"event_type"`
					Data map[string]any `json:"data"`
				} `json:"event"`
			}
			if err := ws.conn.ReadJSON(&ev); err != nil {
				return
			}
			if !e2eSweepWriteEvent(ev.Event.Type) {
				continue
			}
			line := ev.Event.Type
			if d, ok := ev.Event.Data["domain"]; ok {
				line += fmt.Sprintf(" %v.%v", d, ev.Event.Data["service"])
			}
			if v, ok := ev.Event.Data["url_path"]; ok {
				line += fmt.Sprintf(" url_path=%v", v)
			}
			if v, ok := ev.Event.Data["entity_id"]; ok {
				line += fmt.Sprintf(" entity_id=%v", v)
			}
			e.mu.Lock()
			e.seen = append(e.seen, line)
			e.mu.Unlock()
		}
	}()
	t.Cleanup(func() { _ = ws.conn.Close(); <-done })
	return e
}

func (e *e2eSweepEvents) take() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.seen
	e.seen = nil
	return out
}

func (e *e2eSweepEvents) waitFor(want string, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		found := slices.ContainsFunc(e.seen, func(s string) bool { return strings.Contains(s, want) })
		e.mu.Unlock()
		if found {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}

// ---------------------------------------------------------------------------
// The closure: the policy write surface and the command tree.
// ---------------------------------------------------------------------------

func e2eSweepRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

// e2eSweepManifest returns the policy manifest's checked write rows and its
// exempt ones.
func e2eSweepManifest(t *testing.T) (checked map[string]bool, exempt map[string]bool) {
	t.Helper()
	m, err := surfaceaudit.LoadManifest(e2eSweepRepoRoot(t), "policy")
	if err != nil {
		t.Fatalf("loading the policy manifest: %v", err)
	}
	checked, exempt = map[string]bool{}, map[string]bool{}
	for key, d := range m.Entries {
		if p, ok := strings.CutPrefix(key, "cmd "); ok {
			if d.Kind == surfaceaudit.Exempt {
				exempt[p] = true
			} else {
				checked[p] = true
			}
		}
	}
	if len(checked) < 30 {
		t.Fatalf("the manifest names %d checked write commands — the cmd rows have stopped matching", len(checked))
	}
	return checked, exempt
}

func e2eSweepPath(t *testing.T, args []string) string {
	t.Helper()
	p, err := cmd.FindCommandPath(args)
	if err != nil {
		t.Fatalf("%v resolves to no command: %v", args, err)
	}
	return p
}

// ---------------------------------------------------------------------------
// The write sweep.
// ---------------------------------------------------------------------------

// e2eSweepSeed is one real object of every kind a write command takes.
type e2eSweepSeed struct {
	area, floor, label, device, entry string
	// probeEntry has an options flow and no diagnostics platform, so `config
	// show --probe-options-flow` would run the probe; "" when the tier's HA
	// has none.
	probeEntry string
}

const (
	e2eSweepAuto      = "pg_sweep_auto"
	e2eSweepScript    = "pg_sweep_script"
	e2eSweepTpl       = "pg_sweep_tpl"
	e2eSweepHelper    = "pg_sweep_helper"
	e2eSweepHelperEnt = "input_boolean.pg_sweep_helper"
	e2eSweepDash      = "pg-sweep"
)

// e2eSweepAutoYAML is the seeded automation; it switches the seeded helper,
// so `ref replace` and `ent rename` have a reference to rewrite.
const e2eSweepAutoYAML = "id: " + e2eSweepAuto + "\nalias: PG Sweep Auto\ntriggers:\n  - trigger: event\n    event_type: pg_sweep_never\n" +
	"actions:\n  - action: input_boolean.turn_on\n    target:\n      entity_id: " + e2eSweepHelperEnt + "\n"

func seedE2ESweep(t *testing.T, sweepDir string) e2eSweepSeed {
	t.Helper()
	ctx := context.Background()
	cleanupTyped := func() {
		_, _ = testClient.DeleteAutomationDef(ctx, e2eSweepAuto)
		_, _ = testClient.DeleteScriptDef(ctx, e2eSweepScript)
		_, _ = testClient.DeleteTemplate(ctx, e2eSweepTpl)
		_, _ = testClient.DeleteHelper(ctx, e2eSweepHelper, "input_boolean")
	}
	cleanupTyped()
	t.Cleanup(cleanupTyped)
	for _, c := range [][]string{
		{"helper", "create", "input_boolean", "-f", writeTempYAML(t, "h.yaml", e2eSweepHelper+":\n  name: PG Sweep Helper\n")},
		{"auto", "create", "-f", writeTempYAML(t, "a.yaml", e2eSweepAutoYAML)},
		{"script", "create", "-f", writeTempYAML(t, "s.yaml", e2eSweepScript+":\n  alias: PG Sweep Script\n  sequence:\n    - delay: 0\n")},
		{"tpl", "create", "-f", writeTempYAML(t, "t.yaml", "sensor:\n  - name: PG Sweep Tpl\n    unique_id: "+e2eSweepTpl+"\n    state: \"1\"\n")},
	} {
		if out, err := runHactlE2E(t, append(c, "--confirm")...); err != nil {
			t.Fatalf("seeding %v as the owner: %v\n%s", c[:2], err, out)
		}
	}
	waitForAutomationInHA(t, e2eSweepAuto)
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, _, ok := haStateOf(t, e2eSweepHelperEnt); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("HA never reported the seeded helper %s", e2eSweepHelperEnt)
		}
		time.Sleep(time.Second)
	}

	ws := dialE2ESweepWS(t)
	var s e2eSweepSeed
	var floor struct {
		ID string `json:"floor_id"`
	}
	_ = json.Unmarshal(ws.must(t, map[string]any{"type": "config/floor_registry/create", "name": "PG Sweep Floor"}), &floor)
	var area struct {
		ID string `json:"area_id"`
	}
	_ = json.Unmarshal(ws.must(t, map[string]any{"type": "config/area_registry/create", "name": "PG Sweep Area", "floor_id": floor.ID}), &area)
	var label struct {
		ID string `json:"label_id"`
	}
	_ = json.Unmarshal(ws.must(t, map[string]any{"type": "config/label_registry/create", "name": "PG Sweep Label"}), &label)
	if floor.ID == "" || area.ID == "" || label.ID == "" {
		t.Fatalf("seeding registries: floor %q area %q label %q", floor.ID, area.ID, label.ID)
	}
	s.floor, s.area, s.label = floor.ID, area.ID, label.ID
	ws.must(t, map[string]any{"type": "config/entity_registry/update", "entity_id": e2eSweepHelperEnt, "area_id": s.area, "labels": []string{s.label}})
	var dash struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(ws.must(t, map[string]any{"type": "lovelace/dashboards/create", "url_path": e2eSweepDash, "title": "PG Sweep",
		"require_admin": false, "show_in_sidebar": false}), &dash)
	ws.must(t, map[string]any{"type": "lovelace/config/save", "url_path": e2eSweepDash, "config": map[string]any{
		"title": "PG Sweep", "views": []any{map[string]any{"title": "Main", "path": "main", "cards": []any{
			map[string]any{"type": "entities", "entities": []any{e2eSweepHelperEnt}},
		}}},
	}})
	t.Cleanup(func() {
		c := dialE2ESweepWS(t)
		c.send(t, map[string]any{"type": "lovelace/dashboards/delete", "dashboard_id": dash.ID})
		c.send(t, map[string]any{"type": "config/label_registry/delete", "label_id": s.label})
		c.send(t, map[string]any{"type": "config/area_registry/delete", "area_id": s.area})
		c.send(t, map[string]any{"type": "config/floor_registry/delete", "floor_id": s.floor})
	})

	var devices []struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(ws.must(t, map[string]any{"type": "config/device_registry/list"}), &devices)
	var entries []struct {
		ID       string `json:"entry_id"`
		Options  bool   `json:"supports_options"`
		Disabled any    `json:"disabled_by"`
	}
	_ = json.Unmarshal(ws.must(t, map[string]any{"type": "config_entries/get"}), &entries)
	for _, e := range entries {
		if !e.Options || e.Disabled != nil {
			continue
		}
		if s.entry == "" {
			s.entry = e.ID
		}
		// `config show` probes an options flow only for an entry without a
		// diagnostics platform (Home Assistant answers 404 for it).
		if code, _ := w1HARest(t, http.MethodGet, "/api/diagnostics/config_entry/"+e.ID, nil); code == http.StatusNotFound && s.probeEntry == "" {
			s.probeEntry = e.ID
		}
	}
	if len(devices) == 0 || s.entry == "" {
		t.Fatalf("the tier's HA has no device (%d) or no config entry with an options flow (%q) — the sweep would have no target", len(devices), s.entry)
	}
	s.device = devices[0].ID

	backups := filepath.Join(sweepDir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backups, "2026-11-05T10-00-00.000000_"+e2eSweepAuto+".yaml"), []byte(e2eSweepAutoYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	return s
}

// e2eSweepCase is one write command and whether it is `config show`, whose
// options-flow probe is skipped rather than refused.
type e2eSweepCase struct {
	args  []string
	probe bool
}

func e2eSweepWriteCases(t *testing.T, s e2eSweepSeed, stored json.RawMessage) []e2eSweepCase {
	t.Helper()
	f := writeTempYAML
	data := func(id string) string { return `{"entity_id":"` + id + `"}` }
	c := func(args ...string) e2eSweepCase { return e2eSweepCase{args: args} }
	return []e2eSweepCase{
		c("area", "create", "PG Sweep New Area"),
		c("area", "delete", s.area),
		c("floor", "create", "PG Sweep New Floor"),
		c("floor", "delete", s.floor),
		c("label", "create", "PG Sweep New Label"),
		c("label", "delete", s.label),
		c("auto", "create", "-f", f(t, "auto-new.yaml", "id: pg_sweep_new\nalias: PG Sweep New\ntriggers:\n  - trigger: event\n    event_type: pg_sweep_never\nactions:\n  - delay: 1\n")),
		c("auto", "apply", e2eSweepAuto, "-f", f(t, "auto-changed.yaml", strings.Replace(e2eSweepAutoYAML, "PG Sweep Auto", "PG Sweep Auto Changed", 1))),
		c("auto", "delete", e2eSweepAuto),
		c("auto", "rollback", e2eSweepAuto),
		c("rollback", e2eSweepAuto),
		c("cache", "clear"),
		c("cache", "refresh"),
		c("companion", "wireguard", "config", "-f", f(t, "wg0.conf", "[Interface]\nPrivateKey = x\n")),
		c("companion", "wireguard", "up"),
		c("companion", "wireguard", "down"),
		c("config", "delete", s.entry),
		c("config", "options", s.entry),
		c("config", "flow-start", "demo"),
		c("config", "flow-step", "pg_sweep_no_such_flow", "--data", "{}"),
		c("config", "flow-inspect", "pg_sweep_no_such_flow"),
		{args: []string{"config", "show", cmp.Or(s.probeEntry, s.entry), "--probe-options-flow"}, probe: true},
		c("dash", "create", "--url-path", "pg-sweep-new", "--title", "PG Sweep New"),
		c("dash", "delete", e2eSweepDash),
		c("dash", "replace", e2eSweepHelperEnt, "input_boolean.pg_sweep_other", e2eSweepDash),
		c("dash", "save", e2eSweepDash, "-f", f(t, "dash-changed.json", `{"title":"PG Sweep","views":[{"title":"Main","path":"main","cards":[]}]}`)),
		// G-6 / D-180: the stored config, unchanged.
		c("dash", "save", e2eSweepDash, "-f", f(t, "dash-identical.json", string(stored))),
		c("device", "set-area", s.device, s.area),
		c("device", "set-label", s.device, s.label),
		c("ent", "rename", e2eSweepHelperEnt, "input_boolean.pg_sweep_renamed"),
		c("ent", "set-area", e2eSweepHelperEnt, s.area),
		c("ent", "set-label", e2eSweepHelperEnt, s.label),
		c("helper", "create", "input_boolean", "-f", f(t, "helper-new.yaml", "pg_sweep_new:\n  name: PG Sweep New\n")),
		c("helper", "delete", e2eSweepHelper),
		c("ref", "replace", e2eSweepHelperEnt, "input_boolean.pg_sweep_renamed"),
		c("script", "create", "-f", f(t, "script-new.yaml", "pg_sweep_new:\n  alias: PG Sweep New\n  sequence:\n    - delay: 1\n")),
		c("script", "apply", e2eSweepScript, "-f", f(t, "script-changed.yaml", e2eSweepScript+":\n  alias: PG Sweep Script\n  sequence:\n    - delay: 2\n")),
		c("script", "delete", e2eSweepScript),
		c("script", "run", e2eSweepScript),
		c("svc", "call", "input_boolean.turn_on", "--data", data(e2eSweepHelperEnt)),
		c("svc", "call", "script.turn_on", "--data", data("script."+e2eSweepScript)),
		c("svc", "call", "homeassistant.restart"),
		c("tpl", "create", "-f", f(t, "tpl-new.yaml", "sensor:\n  - name: PG Sweep New\n    unique_id: pg_sweep_new\n    state: \"1\"\n")),
		c("tpl", "delete", e2eSweepTpl),
	}
}

// e2eSweepNoConfirm are the write commands without --confirm.
var e2eSweepNoConfirm = map[string]bool{
	"hactl cache clear": true, "hactl cache refresh": true, "hactl config flow-inspect": true,
}

// assertE2ESweepRefused checks one refused run of the binary.
func assertE2ESweepRefused(t *testing.T, args []string, stdout, stderr string, code int, wantDryRun bool) {
	t.Helper()
	if code != 1 || !slices.ContainsFunc(strings.Split(stderr, "\n"), func(l string) bool { return strings.HasPrefix(l, "policy_denied: ") }) {
		t.Errorf("hactl %v: exit %d, want 1 with a policy_denied line on stderr — the deny-all profile refuses every write\nstdout:\n%s\nstderr:\n%s",
			args, code, stdout, stderr)
		return
	}
	if !slices.Contains(args, "--json") {
		if !strings.Contains(stdout, "policy_denied: would ") || !strings.Contains(stdout, " — default: deny") {
			t.Errorf("hactl %v: no refusal report naming `default: deny` on stdout:\n%s", args, stdout)
		}
		return
	}
	r := decodeE2ERefusal(t, fmt.Sprintf("hactl %v", args), stdout, wantDryRun)
	if r.Details.Policy == nil {
		return
	}
	byDefault := false
	for _, d := range r.Details.Policy.Denied {
		switch {
		case d.Rule == "default: deny":
			byDefault = true
		case strings.HasPrefix(d.Rule, "fail-closed: "):
		default:
			t.Errorf("hactl %v: denial %+v names a rule the deny-all profile does not have", args, d)
		}
	}
	if !byDefault || r.Details.Policy.Profile != e2eSweepProfile {
		t.Errorf("hactl %v: want a denial by `default: deny` under %s, got %+v (profile %s)", args, e2eSweepProfile, r.Details.Policy.Denied, r.Details.Policy.Profile)
	}
}

// TestE2EPolicySweepWritesDenyAllCLI — W-6 / §0 on the companion tier: under
// `default: deny` with `"*": [read]`, every write command of the policy write
// surface, aimed at a real object (an automation, a script, a helper, a
// template entity, an area, a floor, a label, a dashboard, a backup), is
// refused by the policy — the dry run and --confirm, prose and --json — and
// Home Assistant, its config files and the companion's backups are exactly as
// they were, with no write event on its bus. G-6's four (auto rollback,
// helper create/delete, floor delete) reach the policy, not an earlier error,
// and so does an unchanged dash save (D-180).
func TestE2EPolicySweepWritesDenyAllCLI(t *testing.T) {
	dir := e2eSweepDir(t, e2eSweepWritePolicy)
	seed := seedE2ESweep(t, dir)
	ws := dialE2ESweepWS(t)
	stored := ws.must(t, map[string]any{"type": "lovelace/config", "url_path": e2eSweepDash})
	cases := e2eSweepWriteCases(t, seed, stored)

	checked, exempt := e2eSweepManifest(t)
	driven := map[string]bool{}
	for _, c := range cases {
		p := e2eSweepPath(t, c.args)
		if !checked[p] {
			t.Errorf("the sweep drives %s, which is no checked write row of policy.manifest", p)
		}
		driven[p] = true
	}
	for p := range checked {
		if !driven[p] {
			t.Errorf("%s is a write command (policy.manifest) the deny-all sweep does not drive — add a case", p)
		}
	}
	for _, p := range []string{"hactl setup", "hactl agent add", "hactl mcp"} {
		if !exempt[p] {
			t.Errorf("%s is no longer an exempt write row — the sweep must drive it", p)
		}
	}
	if len(exempt) != 3 {
		t.Errorf("policy.manifest has %d exempt write rows, the sweep accounts for 3 (setup, agent add, mcp)", len(exempt))
	}

	events := recordE2ESweepEvents(t)
	var storedCfg map[string]any
	_ = json.Unmarshal(stored, &storedCfg)
	ws.must(t, map[string]any{"type": "lovelace/config/save", "url_path": e2eSweepDash, "config": storedCfg})
	if !events.waitFor("lovelace_updated url_path="+e2eSweepDash, 10*time.Second) {
		t.Fatalf("canary: an unchanged lovelace/config/save fired no lovelace_updated (seen %v) — the event oracle is blind", events.take())
	}
	time.Sleep(2 * time.Second)
	events.take()

	before := e2eSweepSnapshot(t)
	for _, c := range cases {
		noConfirm := e2eSweepNoConfirm[e2eSweepPath(t, c.args)]
		forms := [][]string{c.args, append(slices.Clone(c.args), "--json")}
		if !noConfirm && !c.probe {
			forms = append(forms, append(slices.Clone(c.args), "--confirm"), append(slices.Clone(c.args), "--confirm", "--json"))
		}
		for _, args := range forms {
			stdout, stderr, code := runPolicyE2E(t, dir, nil, args...)
			if c.probe {
				// The read answers; the probe — a transient options flow — never
				// runs: skipped by the policy, with a note, where it would run.
				skipped := strings.Contains(stdout+stderr, "options-flow probe skipped: policy_denied: edit config_entry:")
				probed := strings.Contains(stdout, "options_flow")
				if code != 0 || probed || (seed.probeEntry != "" && !skipped) {
					t.Errorf("hactl %v: exit %d, want 0 with no options-flow probe run (skipped by the policy for %q)\nstdout:\n%s\nstderr:\n%s",
						args, code, seed.probeEntry, stdout, stderr)
				}
				continue
			}
			assertE2ESweepRefused(t, args, stdout, stderr, code, !noConfirm && !slices.Contains(args, "--confirm"))
		}
	}
	time.Sleep(3 * time.Second)
	if seen := events.take(); len(seen) > 0 {
		t.Errorf("Home Assistant fired write events while the deny-all profile was in effect:\n  %s", strings.Join(seen, "\n  "))
	}
	assertE2ESweepUnchanged(t, before, e2eSweepSnapshot(t))
	t.Logf("deny-all write sweep (companion tier): %d cases over %d write commands", len(cases), len(driven))
}

// ---------------------------------------------------------------------------
// The read sweep.
// ---------------------------------------------------------------------------

const e2eSweepHidden = "pgsweep-e2e-hidden-41c9"

// e2eSweepReadExempt are the commands the read sweep does not run.
var e2eSweepReadExempt = map[string]string{
	"hactl setup":           "interactive; writes the instance's own .env",
	"hactl mcp":             "the server; the integration tier's read sweep runs every command over MCP",
	"hactl agent add":       "the owner's command, behind a terminal (D-149, D-163)",
	"hactl policy activate": "the owner's command, behind a terminal (D-149)",
}

// e2eSweepReadsNothing are the commands that read no instance (D-60, D-172).
var e2eSweepReadsNothing = map[string]bool{
	"hactl version": true, "hactl help": true, "hactl rtfm": true, "hactl policy show": true,
	"hactl policy lint": true, "hactl agent ls": true, "hactl completion bash": true,
	"hactl completion zsh": true, "hactl completion fish": true, "hactl completion powershell": true,
}

// e2eSweepLeaks returns the needles in out that are neither an argument nor
// the command's own guess of an entity id from one (`auto show x` asks for
// `automation.x` when it may read no automation).
func e2eSweepLeaks(out string, args, needles []string) []string {
	argv := strings.Join(args, " ")
	var leaks []string
	for _, n := range needles {
		if !strings.Contains(out, n) || strings.Contains(argv, n) {
			continue
		}
		if _, object, ok := strings.Cut(n, "."); ok && slices.Contains(args, object) {
			continue
		}
		leaks = append(leaks, n)
	}
	return leaks
}

// e2eSweepReadNeedles is what the oracle reads from Home Assistant directly
// and no read may print — every entity id, every friendly name, every area,
// floor and label name, and the hidden marker — and the part of it the sweep
// seeded itself.
func e2eSweepReadNeedles(t *testing.T, ws *e2eSweepWS) (all, seeded []string) {
	t.Helper()
	needles := map[string]bool{e2eSweepHidden: true}
	code, body := w1HARest(t, http.MethodGet, "/api/states", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/states: %d", code)
	}
	var states []struct {
		ID    string         `json:"entity_id"`
		Attrs map[string]any `json:"attributes"`
	}
	if err := json.Unmarshal(body, &states); err != nil {
		t.Fatal(err)
	}
	for _, s := range states {
		needles[s.ID] = true
		if n, ok := s.Attrs["friendly_name"].(string); ok && len(n) >= 8 {
			needles[n] = true
		}
	}
	for _, typ := range []string{"config/area_registry/list", "config/floor_registry/list", "config/label_registry/list"} {
		var rows []struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(ws.must(t, map[string]any{"type": typ}), &rows)
		for _, r := range rows {
			if len(r.Name) >= 6 {
				needles[r.Name] = true
			}
		}
	}
	for _, must := range []string{e2eSweepHelperEnt, "automation.pg_sweep_secret_auto", "script." + e2eSweepScript, "sensor.pg_sweep_tpl", "PG Sweep Area", "PG Sweep Label", "PG Sweep Floor"} {
		if !needles[must] {
			t.Fatalf("oracle: HA does not hold %q — the leak check would look for nothing", must)
		}
	}

	// A name the manual's own prose carries ("Home Assistant", "Kitchen")
	// cannot tell a leak from a sentence; entity ids and the sweep's seeds
	// are kept whatever the manual says.
	for n := range needles {
		if !strings.Contains(n, ".") && !strings.Contains(n, "PG Sweep") && !strings.Contains(n, "pgsweep") && strings.Contains(docs.Manual, n) {
			delete(needles, n)
		}
	}
	all = make([]string, 0, len(needles))
	for n := range needles {
		all = append(all, n)
		if strings.Contains(strings.ToLower(n), "pg_sweep") || strings.Contains(n, "PG Sweep") || strings.Contains(n, "pgsweep") {
			seeded = append(seeded, n)
		}
	}
	sort.Strings(all)
	sort.Strings(seeded)
	return all, seeded
}

// assertE2ESweepReadClosure holds the read sweep to the command tree and
// returns the commands it runs.
func assertE2ESweepReadClosure(t *testing.T, cases [][]string) map[string]bool {
	t.Helper()
	checked, exempt := e2eSweepManifest(t)
	leaves := map[string]bool{"hactl config show": true}
	for _, p := range cmd.LeafCommandPaths() {
		// cobra adds `help` and `completion` to the tree on the first Execute,
		// which this tier never runs in process; they read no instance, and
		// the integration tier's read sweep runs them.
		if !checked[p] && !exempt[p] && p != "hactl help" && !strings.HasPrefix(p, "hactl completion") {
			leaves[p] = true
		}
	}
	ran := map[string]bool{}
	for _, c := range cases {
		p := e2eSweepPath(t, c)
		if !leaves[p] {
			t.Errorf("the read sweep runs %s, which is no read command", p)
		}
		ran[p] = true
	}
	for p := range leaves {
		if _, ex := e2eSweepReadExempt[p]; !ran[p] && !ex {
			t.Errorf("%s is a read command the deny-all read sweep does not run — add a case or an exemption with a reason", p)
		}
	}

	return ran
}

// TestE2EPolicySweepReadsDenyAllCLI — W-6 / W-2 acceptance with the companion
// in play: under a profile that may read nothing, no read command — the ones
// that read config files, typed definitions and search hits through the
// companion included — prints an entity id, an area, floor or label name, a
// friendly name, or the secret the seeded automation, script, template,
// helper, dashboard and config file carry. Echoing an argument is not a leak.
func TestE2EPolicySweepReadsDenyAllCLI(t *testing.T) {
	seed := seedE2ESweep(t, t.TempDir())
	ctx := context.Background()
	// The secret, in every place a read could find it.
	_, _ = testClient.DeleteAutomationDef(ctx, "pg_sweep_secret_auto")
	hiddenAuto := "id: pg_sweep_secret_auto\nalias: PG Sweep Secret Auto\ndescription: " + e2eSweepHidden + "\ntriggers:\n  - trigger: event\n    event_type: pg_sweep_never\n" +
		"actions:\n  - action: input_boolean.turn_on\n    target:\n      entity_id: " + e2eSweepHelperEnt + "\n"
	if out, err := runHactlE2E(t, "auto", "create", "-f", writeTempYAML(t, "secret.yaml", hiddenAuto), "--confirm"); err != nil {
		t.Fatalf("seeding the secret automation: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = testClient.DeleteAutomationDef(ctx, "pg_sweep_secret_auto") })
	waitForAutomationInHA(t, "pg_sweep_secret_auto")
	ws := dialE2ESweepWS(t)
	ws.must(t, map[string]any{"type": "lovelace/config/save", "url_path": e2eSweepDash, "config": map[string]any{
		"title": "PG Sweep", "views": []any{map[string]any{"title": "Main", "path": "main", "cards": []any{
			map[string]any{"type": "markdown", "content": e2eSweepHidden},
			map[string]any{"type": "entities", "entities": []any{e2eSweepHelperEnt}},
		}}},
	}})

	if _, err := testClient.GetHelper(ctx, e2eSweepHelper); err != nil {
		t.Fatalf("fixture: the seeded helper is not readable as the owner, so `helper cat` would prove nothing: %v", err)
	}
	all, seeded := e2eSweepReadNeedles(t, ws)

	dir := e2eSweepDir(t, e2eSweepReadPolicy)
	tmp := t.TempDir()
	diff := writeTempYAML(t, "diff.yaml", e2eSweepAutoYAML)
	cases := [][]string{
		{"agent", "ls", "--root", tmp},
		{"area", "ls"},
		{"auto", "cat", "pg_sweep_secret_auto"},
		{"auto", "diff", e2eSweepAuto, "-f", diff},
		{"auto", "ls"},
		{"auto", "show", "pg_sweep_secret_auto"},
		{"cache", "status"},
		{"cc", "logs", "hacs"},
		{"cc", "ls"},
		{"cc", "show", "hacs"},
		{"changes"},
		{"companion", "logs"},
		{"companion", "status"},
		{"companion", "wireguard", "status"},
		{"config", "block", "automations.yaml", "pg_sweep_secret_auto"},
		{"config", "entries"},
		{"config", "file", "automations.yaml"},
		{"config", "files"},
		{"config", "show", seed.entry},
		{"dash", "grep", e2eSweepHelperEnt},
		{"dash", "ls"},
		{"dash", "resources"},
		{"dash", "show", e2eSweepDash},
		{"device", "ls"},
		{"device", "show", seed.device},
		{"energy", "show"},
		{"ent", "anomalies", e2eSweepHelperEnt},
		{"ent", "hist", e2eSweepHelperEnt},
		{"ent", "ls"},
		{"ent", "related", e2eSweepHelperEnt},
		{"ent", "show", e2eSweepHelperEnt},
		{"ent", "who", e2eSweepHelperEnt},
		{"floor", "ls"},
		{"grep", "pg_sweep"},
		{"health"},
		{"helper", "cat", e2eSweepHelper},
		{"helper", "ls"},
		{"helper", "show", e2eSweepHelper},
		{"issues"},
		{"label", "ls"},
		{"log"},
		{"log", "show", "1"},
		{"policy", "check", "entity:" + e2eSweepHelperEnt, "read"},
		{"policy", "lint"},
		{"policy", "review"},
		{"policy", "show"},
		{"ref", "scan", e2eSweepHelperEnt},
		{"ref", "validate"},
		{"rtfm"},
		{"script", "cat", e2eSweepScript},
		{"script", "diff", e2eSweepScript, "-f", writeTempYAML(t, "sdiff.yaml", e2eSweepScript+":\n  sequence: []\n")},
		{"script", "ls"},
		{"script", "show", e2eSweepScript},
		{"tpl", "cat", e2eSweepTpl},
		{"tpl", "eval", "{{ states('" + e2eSweepHelperEnt + "') }}"},
		{"trace", "show", "automation.pg_sweep_secret_auto"},
		{"version"},
	}

	ran := assertE2ESweepReadClosure(t, cases)
	refused := 0
	for _, c := range cases {
		// The commands that read no instance print static text (the manual
		// names `sun.sun`, `zone.home`): they are held to the seeded values.
		_, static := e2eSweepReadsNothing[e2eSweepPath(t, c)]
		look := all
		if static {
			look = seeded
		}
		for _, args := range [][]string{c, append(slices.Clone(c), "--json")} {
			stdout, stderr, code := runPolicyE2E(t, dir, nil, args...)
			if leaks := e2eSweepLeaks(stdout+stderr, args, look); len(leaks) > 0 {
				t.Errorf("hactl %v (exit %d): the read-nothing profile let %q through\nstdout:\n%s\nstderr:\n%s", args, code, leaks, stdout, stderr)
			}
			if !static && (code != 0 || strings.Contains(stdout+stderr, "hidden by policy: ")) {
				refused++
			}
		}
	}
	t.Logf("deny-all read sweep (companion tier): %d read commands, %d runs (prose and --json), %d exempt, %d refused, failed or hid rows",
		len(ran), 2*len(cases), len(e2eSweepReadExempt), refused)
	if refused == 0 {
		t.Error("no run was refused or hid anything — the profile was not in effect")
	}
}
