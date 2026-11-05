//go:build integration

package integration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hemm-ems/hactl/internal/cmd"
	"github.com/hemm-ems/hactl/internal/hatest"
	"github.com/hemm-ems/hactl/internal/mcpserver"
	"github.com/hemm-ems/hactl/internal/surfaceaudit"
)

// ============================================================================
// W-6: the deny-all sweeps (SPEC-policy-usp.md §0, §2 W-6, G-6). The claim
// under test: "an agent that reaches Home Assistant only through hactl can do
// and see only what your policy allows". Every write command, driven with
// real targets under a profile that may write nothing, leaves Home Assistant
// byte-identical; every read command, under a profile that may read nothing,
// prints nothing Home Assistant holds.
//
// The truth is read outside hactl (H-12): registries, dashboards, config
// entries and states over a raw websocket and plain REST, the config files
// from the bind-mounted /config, and Home Assistant's own event bus, which
// fires for every service call, registry change and dashboard save that
// reaches it — including one that changes nothing.
//
// Both sweeps are closed over the command tree: the write sweep against the
// `cmd` rows of dev/surfaces/policy.manifest, the read sweep against every
// leaf command (cmd.LeafCommandPaths). A command added tomorrow fails here
// until it is driven or excepted with a reason.
// ============================================================================

const sweepProfile = "pg_sweep"

// sweepWritePolicy may read everything and write nothing.
const sweepWritePolicy = "version: 1\nactive: " + sweepProfile + "\nprofiles:\n  " + sweepProfile + ":\n" +
	"    default: deny\n    allow:\n      \"*\": [read]\n"

// sweepReadPolicy may do nothing at all.
const sweepReadPolicy = "version: 1\nactive: " + sweepProfile + "\nprofiles:\n  " + sweepProfile + ":\n" +
	"    default: deny\n"

// sweepRepoRoot is the module root, where dev/surfaces lives.
func sweepRepoRoot(t *testing.T) string {
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

// sweepManifestWriteCommands reads the policy write surface: every `cmd` row
// of dev/surfaces/policy.manifest, split into the rows a session checks
// (proven or debt) and the exempt ones, with their reasons.
func sweepManifestWriteCommands(t *testing.T) (checked map[string]bool, exempt map[string]string) {
	t.Helper()
	m, err := surfaceaudit.LoadManifest(sweepRepoRoot(t), "policy")
	if err != nil {
		t.Fatalf("loading the policy manifest: %v", err)
	}
	checked, exempt = map[string]bool{}, map[string]string{}
	for key, d := range m.Entries {
		path, ok := strings.CutPrefix(key, "cmd ")
		if !ok {
			continue
		}
		if d.Kind == surfaceaudit.Exempt {
			exempt[path] = d.Detail
		} else {
			checked[path] = true
		}
	}
	if len(checked) < 30 {
		t.Fatalf("the manifest names %d checked write commands — the cmd rows have stopped matching", len(checked))
	}
	return checked, exempt
}

// sweepCommandPath is the canonical command path args resolve to.
func sweepCommandPath(t *testing.T, args []string) string {
	t.Helper()
	p, err := cmd.FindCommandPath(args)
	if err != nil {
		t.Fatalf("%v resolves to no command: %v", args, err)
	}
	return p
}

// ---------------------------------------------------------------------------
// The oracle: a raw snapshot of Home Assistant.
// ---------------------------------------------------------------------------

// sweepStateDomains are the domains whose state only a write changes. The
// demo's sensors, weather, media players and the like drift on their own and
// would turn the snapshot into noise.
var sweepStateDomains = map[string]bool{
	"alarm_control_panel": true, "automation": true, "button": true, "climate": true, "counter": true,
	"cover": true, "fan": true, "group": true, "humidifier": true, "input_boolean": true,
	"input_button": true, "input_datetime": true, "input_number": true, "input_select": true,
	"input_text": true, "light": true, "lock": true, "number": true, "scene": true, "script": true,
	"select": true, "siren": true, "switch": true, "timer": true, "valve": true, "water_heater": true,
}

// sweepSnapshot is Home Assistant as read outside hactl, one canonical text
// per part, so a difference names the part it is in.
type sweepSnapshot map[string]string

func sweepCanonicalJSON(t *testing.T, what string, raw []byte) string {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("%s did not parse: %v", what, err)
	}
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// takeSweepSnapshot reads every registry, every dashboard's list entry and
// stored config, every config entry, the state of every entity in
// sweepStateDomains, and a hash of every YAML file under /config.
func takeSweepSnapshot(t *testing.T, inst *hatest.Instance) sweepSnapshot {
	t.Helper()
	ws := dialRawWS(t, inst.URL(), inst.Token())
	snap := sweepSnapshot{}
	for _, typ := range []string{
		"config/entity_registry/list", "config/device_registry/list", "config/area_registry/list",
		"config/floor_registry/list", "config/label_registry/list", "lovelace/dashboards/list",
		"config_entries/get",
	} {
		snap[typ] = sweepCanonicalJSON(t, typ, ws.mustSucceed(t, map[string]any{"type": typ}).Result)
	}
	dashes := pgDashboards(t, ws)
	urlPaths := make([]string, 0, 1+len(dashes))
	urlPaths = append(urlPaths, "")
	for u := range dashes {
		urlPaths = append(urlPaths, u)
	}
	sort.Strings(urlPaths)
	for _, u := range urlPaths {
		msg := map[string]any{"type": "lovelace/config"}
		if u != "" {
			msg["url_path"] = u
		}
		env := ws.send(t, msg)
		key := "lovelace/config " + u
		if env.Success {
			snap[key] = sweepCanonicalJSON(t, key, env.Result)
		} else {
			snap[key] = "error " + env.errCode(t)
		}
	}

	var lines []string
	for _, s := range pgRawStates(t, inst) {
		id, _ := s["entity_id"].(string)
		domain, _, _ := strings.Cut(id, ".")
		if !sweepStateDomains[domain] {
			continue
		}
		attrs, _ := s["attributes"].(map[string]any)
		lines = append(lines, fmt.Sprintf("%s state=%v last_changed=%v last_triggered=%v",
			id, s["state"], s["last_changed"], attrs["last_triggered"]))
	}
	sort.Strings(lines)
	snap["states"] = strings.Join(lines, "\n")

	root := inst.ConfigDir()
	if root == "" {
		t.Fatal("the policy rig has no bind-mounted /config — the config files cannot be compared")
	}
	var paths []string
	walkErr := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".storage" || d.Name() == "deps" || d.Name() == "tts") {
			return filepath.SkipDir
		}
		if d.IsDir() || (!strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml")) {
			return nil
		}
		paths = append(paths, p)
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walking the config files: %v", walkErr)
	}
	files := make([]string, 0, len(paths))
	for _, p := range paths {
		b, rerr := os.ReadFile(filepath.Clean(p))
		if rerr != nil {
			t.Fatalf("hashing %s: %v", p, rerr)
		}
		sum := sha256.Sum256(b)
		rel, _ := filepath.Rel(root, p)
		files = append(files, rel+" "+hex.EncodeToString(sum[:]))
	}
	if len(files) == 0 {
		t.Fatal("no YAML file under /config — the file half of the snapshot would compare nothing")
	}
	sort.Strings(files)
	snap["files"] = strings.Join(files, "\n")
	return snap
}

// assertSweepSnapshotsEqual fails for every part that differs, naming the
// first differing line.
func assertSweepSnapshotsEqual(t *testing.T, before, after sweepSnapshot) {
	t.Helper()
	keys := make([]string, 0, len(before))
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
		b, a := before[k], after[k]
		if b == a {
			continue
		}
		bl, al := strings.Split(b, "\n"), strings.Split(a, "\n")
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
		t.Errorf("Home Assistant changed under the deny-all profile — %s, first difference at line %d:\n  before: %s\n  after:  %s",
			k, i+1, was, now)
	}
}

// ---------------------------------------------------------------------------
// The oracle's second half: Home Assistant's event bus.
// ---------------------------------------------------------------------------

// sweepWriteEvent reports whether an event of this type means a write reached
// Home Assistant: a service call, a run, a registry change, a dashboard
// save, a reload.
func sweepWriteEvent(eventType string) bool {
	switch eventType {
	case "call_service", "automation_triggered", "script_started", "lovelace_updated",
		"panels_updated", "service_registered", "service_removed", "component_loaded",
		"core_config_updated", "themes_updated", "user_added", "user_removed", "user_updated":
		return true
	}
	return strings.HasSuffix(eventType, "_registry_updated") || strings.HasSuffix(eventType, "_reloaded")
}

// sweepEvents records every write event Home Assistant fires while it runs.
type sweepEvents struct {
	conn *websocket.Conn
	mu   sync.Mutex
	seen []string
	done chan struct{}
}

func recordSweepEvents(t *testing.T, inst *hatest.Instance) *sweepEvents {
	t.Helper()
	u, err := url.Parse(inst.URL())
	if err != nil {
		t.Fatal(err)
	}
	u.Scheme, u.Path = "ws", "/api/websocket"
	conn, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dialing HA websocket for events: %v", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	var msg map[string]any
	if err := conn.ReadJSON(&msg); err != nil {
		t.Fatalf("reading auth_required: %v", err)
	}
	if err := conn.WriteJSON(map[string]string{"type": "auth", "access_token": inst.Token()}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&msg); err != nil || msg["type"] != "auth_ok" {
		t.Fatalf("event connection not authenticated: %v %v", err, msg)
	}
	if err := conn.WriteJSON(map[string]any{"id": 1, "type": "subscribe_events"}); err != nil {
		t.Fatal(err)
	}
	if err := conn.ReadJSON(&msg); err != nil || msg["success"] != true {
		t.Fatalf("subscribe_events refused: %v %v", err, msg)
	}
	e := &sweepEvents{conn: conn, done: make(chan struct{})}
	go func() {
		defer close(e.done)
		for {
			var ev struct {
				Event struct {
					Type string         `json:"event_type"`
					Data map[string]any `json:"data"`
				} `json:"event"`
			}
			if err := conn.ReadJSON(&ev); err != nil {
				return
			}
			if !sweepWriteEvent(ev.Event.Type) {
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
	t.Cleanup(func() { _ = conn.Close(); <-e.done })
	return e
}

// take returns the write events seen since the last take, and forgets them.
func (e *sweepEvents) take() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := e.seen
	e.seen = nil
	return out
}

// waitFor waits until a write event holding want has been seen.
func (e *sweepEvents) waitFor(want string, within time.Duration) bool {
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
// The write sweep.
// ---------------------------------------------------------------------------

// sweepOutcome is what a write command under the deny-all profile must do.
type sweepOutcome int

const (
	// refusedByPolicy: exit 1, policy_denied, every denial by `default: deny`
	// (or a fail-closed rule), in the dry run and under --confirm alike.
	refusedByPolicy sweepOutcome = iota
	// needsCompanionFirst: the command reads the companion before it can
	// know its plan, and this tier has no companion — it ends non-zero naming
	// the companion, before any plan. The companion tier's sweep
	// (TestE2EPolicySweepWritesDenyAllCLI) drives it to the policy.
	needsCompanionFirst
	// probeSkipped: `config show` is a read whose options-flow probe is the
	// write; the probe is skipped with a note and the read answers (manual,
	// "What each write command checks").
	probeSkipped
)

type sweepCase struct {
	args    []string
	outcome sweepOutcome
}

// sweepNoConfirm are the write commands without --confirm: each checks its
// plan as it runs (policy_enforce.go writesWithoutConfirm). A wrong entry
// here fails loudly: --confirm would be an unknown flag.
var sweepNoConfirm = map[string]bool{
	"hactl cache clear": true, "hactl cache refresh": true, "hactl config flow-inspect": true,
}

// sweepNotDriven are the manifest's exempt write rows: no session covers
// them, so no profile can refuse them, and the sweep says why it does not
// run each. TestPolicySweepWritesDenyAll fails when the manifest's exempt
// rows and this list part ways.
var sweepNotDriven = map[string]string{
	"hactl setup":     "writes the instance's own .env and touches no policy resource; never available over MCP",
	"hactl agent add": "the owner's command, behind a terminal (D-149, D-163); no session, never over MCP",
	"hactl mcp":       "the dispatcher: every call it runs is a command this sweep drives itself",
}

// assertSweepRefusal checks one refused run: exit 1, the class
// policy_denied, every denial decided by `default: deny` or fail-closed, at
// least one by `default: deny`, nothing written.
func assertSweepRefusal(t *testing.T, args []string, stdout string, err error, asJSON bool, wantDryRun bool) {
	t.Helper()
	if pgExitCode(err) != 1 {
		t.Errorf("hactl %v: exit %d (%v), want 1 — the deny-all profile refuses every write\n%s",
			args, pgExitCode(err), err, stdout)
		return
	}
	if first := strings.SplitN(err.Error(), "\n", 2)[0]; !strings.HasPrefix(first, "policy_denied: ") {
		t.Errorf("hactl %v: refused, but not by the policy: %q\n%s", args, first, stdout)
		return
	}
	if !asJSON {
		if !strings.Contains(stdout, "policy_denied: would ") || !strings.Contains(stdout, "refused by policy, nothing was written") {
			t.Errorf("hactl %v: refusal report missing on stdout:\n%s", args, stdout)
		}
		if !strings.Contains(stdout, " — default: deny") {
			t.Errorf("hactl %v: no denial by `default: deny` — the profile allows no write:\n%s", args, stdout)
		}
		return
	}
	r := decodeRefusal(t, fmt.Sprintf("hactl %v", args), stdout, wantDryRun)
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
			t.Errorf("hactl %v: denial %v names a rule the deny-all profile does not have", args, d)
		}
	}
	if !byDefault {
		t.Errorf("hactl %v: no denial by `default: deny` in %v", args, r.Details.Policy.Denied)
	}
	if r.Details.Policy.Profile != sweepProfile {
		t.Errorf("hactl %v: refused under profile %q, want %q", args, r.Details.Policy.Profile, sweepProfile)
	}
}

// runSweepCase drives one write command: its dry run in text and --json,
// then --confirm in text and --json (or, for a command without --confirm,
// the run itself in both forms).
func runSweepCase(t *testing.T, dir string, c sweepCase) {
	t.Helper()
	path := sweepCommandPath(t, c.args)
	noConfirm := sweepNoConfirm[path]
	runs := [][]string{slices.Clone(c.args), append(slices.Clone(c.args), "--json")}
	if !noConfirm && c.outcome == refusedByPolicy {
		runs = append(runs, append(slices.Clone(c.args), "--confirm"), append(slices.Clone(c.args), "--confirm", "--json"))
	}
	for _, args := range runs {
		stdout, stderr, err := pgReads(t, dir, args...)
		asJSON := slices.Contains(args, "--json")
		switch c.outcome {
		case refusedByPolicy:
			wantDryRun := !noConfirm && !slices.Contains(args, "--confirm")
			assertSweepRefusal(t, args, stdout, err, asJSON, wantDryRun)
		case needsCompanionFirst:
			if err == nil || !strings.Contains(err.Error(), "companion") {
				t.Errorf("hactl %v: want an error naming the companion this tier lacks, got %v\n%s", args, err, stdout)
			}
			if strings.Contains(stdout, "dry-run: would") {
				t.Errorf("hactl %v: printed a plan without the companion its plan needs:\n%s", args, stdout)
			}
		case probeSkipped:
			if err != nil {
				t.Errorf("hactl %v: %v — config show itself is never refused for its probe\n%s", args, err, stdout)
			}
			if !strings.Contains(stdout+stderr, "options-flow probe skipped: policy_denied: edit config_entry:") {
				t.Errorf("hactl %v: no note that the options-flow probe was skipped by the policy:\n%s\n%s", args, stdout, stderr)
			}
		}
	}
}

// sweepWriteFile writes content into the test's temp dir and returns its path.
func sweepWriteFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// sweepSeed is what the write sweep acts on: one real object of every kind
// a write command takes, built over the raw websocket as setup.
type sweepSeed struct {
	area, floor, label string
	dashboard          string
	lights             []string
	device             string
	lock               string
	entry              string
}

func seedSweep(t *testing.T, inst *hatest.Instance, dir string) sweepSeed {
	t.Helper()
	ws := dialRawWS(t, inst.URL(), inst.Token())
	rig := buildPGRig(t, inst)
	s := sweepSeed{lights: rig.Lights, device: rig.DevOf[rig.Lights[0]], lock: rig.Lock, dashboard: "pg-sweep"}

	floorEnv := ws.mustSucceed(t, map[string]any{"type": "config/floor_registry/create", "name": "PG Sweep Floor"})
	var floor struct {
		ID string `json:"floor_id"`
	}
	if err := json.Unmarshal(floorEnv.Result, &floor); err != nil || floor.ID == "" {
		t.Fatalf("floor create answered no floor_id: %v %s", err, floorEnv.Result)
	}
	s.floor = floor.ID
	areaEnv := ws.mustSucceed(t, map[string]any{"type": "config/area_registry/create", "name": "PG Sweep Area", "floor_id": s.floor})
	var area struct {
		ID string `json:"area_id"`
	}
	if err := json.Unmarshal(areaEnv.Result, &area); err != nil || area.ID == "" {
		t.Fatalf("area create answered no area_id: %v %s", err, areaEnv.Result)
	}
	s.area = area.ID
	s.label = pgCreateLabel(t, ws, "PG Sweep Label")
	// One light in the area and holding the label, so area delete and label
	// delete have holders to plan for.
	pgUpdateEntity(t, ws, s.lights[0], map[string]any{"area_id": s.area, "labels": []string{s.label}})
	t.Cleanup(func() {
		c := dialRawWS(t, inst.URL(), inst.Token())
		c.send(t, map[string]any{"type": "config/entity_registry/update", "entity_id": s.lights[0], "name": nil, "area_id": nil, "labels": []string{}})
		c.send(t, map[string]any{"type": "config/label_registry/delete", "label_id": s.label})
		c.send(t, map[string]any{"type": "config/area_registry/delete", "area_id": s.area})
		c.send(t, map[string]any{"type": "config/floor_registry/delete", "floor_id": s.floor})
	})

	pgCreateDashboard(t, inst, s.dashboard, map[string]any{
		"title": "PG Sweep",
		"views": []any{map[string]any{"title": "Main", "path": "main", "cards": []any{
			map[string]any{"type": "entities", "entities": []any{s.lights[0]}},
		}}},
	})

	s.entry = sweepDemoEntry(t, inst)

	// The backup auto rollback restores: the fixture's automation, as
	// `auto apply` would have saved it (<stamp>_<config id>.yaml).
	backups := filepath.Join(dir, "backups")
	if err := os.MkdirAll(backups, 0o700); err != nil {
		t.Fatal(err)
	}
	sweepWriteFile(t, backups, "2026-11-05T10-00-00.000000_pg_policy_prod_heating.yaml",
		"id: pg_policy_prod_heating\nalias: PG Policy Prod Heating\ntriggers:\n  - trigger: event\n"+
			"    event_type: pg_policy_never_fired\nactions:\n  - delay: \"00:00:05\"\n")
	return s
}

// sweepDemoEntry is the demo integration's config entry id.
func sweepDemoEntry(t *testing.T, inst *hatest.Instance) string {
	t.Helper()
	ws := dialRawWS(t, inst.URL(), inst.Token())
	var ce []struct {
		ID     string `json:"entry_id"`
		Domain string `json:"domain"`
	}
	if err := json.Unmarshal(ws.mustSucceed(t, map[string]any{"type": "config_entries/get"}).Result, &ce); err != nil {
		t.Fatalf("config entries did not parse: %v", err)
	}
	for _, e := range ce {
		if e.Domain == "demo" {
			return e.ID
		}
	}
	t.Fatal("policy rig: no demo config entry — config delete/options/show would have no target")
	return ""
}

// sweepWriteCases is every write command, with a real target from seed.
func sweepWriteCases(t *testing.T, inst *hatest.Instance, tmp string, s sweepSeed) []sweepCase {
	t.Helper()
	ws := dialRawWS(t, inst.URL(), inst.Token())
	stored := ws.mustSucceed(t, map[string]any{"type": "lovelace/config", "url_path": s.dashboard}).Result
	f := func(name, content string) string { return sweepWriteFile(t, tmp, name, content) }
	data := func(id string) string { return `{"entity_id":"` + id + `"}` }
	return []sweepCase{
		{args: []string{"area", "create", "PG Sweep New Area"}},
		{args: []string{"area", "delete", s.area}},
		{args: []string{"floor", "create", "PG Sweep New Floor"}},
		{args: []string{"floor", "delete", s.floor}},
		{args: []string{"label", "create", "PG Sweep New Label"}},
		{args: []string{"label", "delete", s.label}},
		{args: []string{"auto", "create", "-f", f("auto-new.yaml",
			"id: pg_sweep_new\nalias: PG Sweep New\ntriggers:\n  - trigger: event\n    event_type: pg_sweep_never\nactions:\n  - delay: 1\n")}},
		{args: []string{"auto", "apply", "pg_policy_prod_heating", "-f", f("auto-changed.yaml",
			"id: pg_policy_prod_heating\nalias: PG Policy Prod Heating\ntriggers:\n  - trigger: event\n    event_type: pg_policy_never_fired\nactions:\n  - delay: \"00:00:09\"\n")},
			outcome: needsCompanionFirst},
		{args: []string{"auto", "delete", "pg_policy_prod_heating"}, outcome: needsCompanionFirst},
		{args: []string{"auto", "rollback", "pg_policy_prod_heating"}},
		{args: []string{"rollback", "pg_policy_prod_heating"}},
		{args: []string{"cache", "clear"}},
		{args: []string{"cache", "refresh"}},
		{args: []string{"companion", "wireguard", "config", "-f", f("wg0.conf", "[Interface]\nPrivateKey = x\n")}},
		{args: []string{"companion", "wireguard", "up"}},
		{args: []string{"companion", "wireguard", "down"}},
		{args: []string{"config", "delete", s.entry}},
		{args: []string{"config", "options", s.entry}},
		{args: []string{"config", "flow-start", "demo"}},
		{args: []string{"config", "flow-step", "pg_sweep_no_such_flow", "--data", "{}"}},
		{args: []string{"config", "flow-inspect", "pg_sweep_no_such_flow"}},
		{args: []string{"config", "show", s.entry, "--probe-options-flow"}, outcome: probeSkipped},
		{args: []string{"dash", "create", "--url-path", "pg-sweep-new", "--title", "PG Sweep New"}},
		{args: []string{"dash", "delete", s.dashboard}},
		{args: []string{"dash", "replace", s.lights[0], s.lights[1], s.dashboard}},
		{args: []string{"dash", "save", s.dashboard, "-f", f("dash-changed.json",
			`{"title":"PG Sweep","views":[{"title":"Main","path":"main","cards":[]}]}`)}},
		// G-6: the same config again. It is still a save — Home Assistant
		// rewrites the dashboard and fires lovelace_updated (the canary below
		// observes exactly that) — so it is refused like any other (D-180).
		{args: []string{"dash", "save", s.dashboard, "-f", f("dash-identical.json", string(stored))}},
		{args: []string{"device", "set-area", s.device, s.area}},
		{args: []string{"device", "set-label", s.device, s.label}},
		{args: []string{"ent", "rename", s.lights[0], "light.pg_sweep_renamed"}, outcome: needsCompanionFirst},
		{args: []string{"ent", "set-area", s.lights[1], s.area}},
		{args: []string{"ent", "set-label", s.lights[1], s.label}},
		{args: []string{"helper", "create", "input_boolean", "-f", f("helper-new.yaml", "pg_sweep_new:\n  name: PG Sweep New\n")}},
		{args: []string{"helper", "delete", "pg_policy_lock_state"}, outcome: needsCompanionFirst},
		{args: []string{"ref", "replace", s.lights[0], "light.pg_sweep_renamed"}, outcome: needsCompanionFirst},
		{args: []string{"script", "create", "-f", f("script-new.yaml", "pg_sweep_new:\n  alias: PG Sweep New\n  sequence:\n    - delay: 1\n")}},
		{args: []string{"script", "apply", "pg_policy_allowed", "-f", f("script-changed.yaml",
			"pg_policy_allowed:\n  alias: PG Policy Allowed Runner\n  sequence:\n    - event: pg_policy_changed\n")},
			outcome: needsCompanionFirst},
		{args: []string{"script", "delete", "pg_policy_allowed"}, outcome: needsCompanionFirst},
		{args: []string{"script", "run", "pg_policy_allowed"}},
		{args: []string{"svc", "call", "light.turn_on", "--data", data(s.lights[0])}},
		{args: []string{"svc", "call", "lock.unlock", "--data", data(s.lock)}},
		{args: []string{"svc", "call", "homeassistant.restart"}},
		{args: []string{"svc", "call", "scene.apply", "--data", `{"entities":{"` + s.lights[0] + `":"on"}}`}},
		{args: []string{"tpl", "create", "-f", f("tpl-new.yaml", "sensor:\n  - name: PG Sweep New\n    unique_id: pg_sweep_new\n    state: \"1\"\n")}},
		{args: []string{"tpl", "delete", "pg_policy_lock"}, outcome: needsCompanionFirst},
	}
}

// TestPolicySweepWritesDenyAll — W-6 / §0: under `default: deny` with
// `"*": [read]`, every write command of the policy write surface, aimed at a
// real object, is refused by the policy in its dry run and under --confirm,
// and Home Assistant — its registries, dashboards, config entries, states,
// config files and event bus — is exactly as it was.
func TestPolicySweepWritesDenyAll(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), sweepWritePolicy)
	tmp := t.TempDir()
	seed := seedSweep(t, inst, dir)
	cases := sweepWriteCases(t, inst, tmp, seed)

	// Closure: every write row of the manifest is driven, and every case is a
	// write row (G-6's four included).
	checked, exempt := sweepManifestWriteCommands(t)
	driven := map[string]bool{}
	for _, c := range cases {
		p := sweepCommandPath(t, c.args)
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
	for p := range exempt {
		if _, ok := sweepNotDriven[p]; !ok {
			t.Errorf("%s is an exempt write row of policy.manifest the sweep does not account for", p)
		}
	}
	for p := range sweepNotDriven {
		if _, ok := exempt[p]; !ok {
			t.Errorf("the sweep excepts %s, which is no exempt write row of policy.manifest", p)
		}
	}

	events := recordSweepEvents(t, inst)
	// Canary: the owner saves pg-sweep's config unchanged, outside hactl. Home
	// Assistant answers with lovelace_updated — so an unchanged save is a
	// write that reaches it (D-180), and the recorder sees writes.
	owner := dialRawWS(t, inst.URL(), inst.Token())
	var storedCfg map[string]any
	if err := json.Unmarshal(owner.mustSucceed(t, map[string]any{"type": "lovelace/config", "url_path": seed.dashboard}).Result, &storedCfg); err != nil {
		t.Fatal(err)
	}
	owner.mustSucceed(t, map[string]any{"type": "lovelace/config/save", "url_path": seed.dashboard, "config": storedCfg})
	if !events.waitFor("lovelace_updated url_path="+seed.dashboard, 10*time.Second) {
		t.Fatalf("canary: an unchanged lovelace/config/save fired no lovelace_updated (seen %v) — the event oracle is blind", events.take())
	}
	time.Sleep(2 * time.Second)
	events.take()

	before := takeSweepSnapshot(t, inst)
	for _, c := range cases {
		runSweepCase(t, dir, c)
	}
	time.Sleep(3 * time.Second)
	if seen := events.take(); len(seen) > 0 {
		t.Errorf("Home Assistant fired write events while the deny-all profile was in effect:\n  %s", strings.Join(seen, "\n  "))
	}
	assertSweepSnapshotsEqual(t, before, takeSweepSnapshot(t, inst))
	t.Logf("deny-all write sweep: %d cases over %d write commands; %d manifest rows not driven (exempt)", len(cases), len(driven), len(exempt))
}

// ---------------------------------------------------------------------------
// The read sweep.
// ---------------------------------------------------------------------------

// sweepReadExempt are the commands the read sweep does not run, and why.
var sweepReadExempt = map[string]string{
	"hactl setup":           "interactive; writes the instance's own .env and reads nothing an agent is shown",
	"hactl mcp":             "the server: every call it dispatches is a command this sweep runs over MCP itself",
	"hactl agent add":       "the owner's command, behind a terminal (D-149, D-163); never over MCP",
	"hactl policy activate": "the owner's command, behind a terminal (D-149); never over MCP",
}

// sweepReadNeedsNothing are the commands that read no instance (D-60's exempt
// set, D-172): they run without a session, so the profile refuses nothing of
// theirs. They are still run and held to the leak check — against the values
// the sweep seeded, since their static text (the manual's examples name
// `sun.sun`, `zone.home`, "Kitchen") is not read from anywhere.
var sweepReadNeedsNothing = map[string]string{
	"hactl version":               "reads no instance (D-60)",
	"hactl help":                  "prints help text; reads no instance (D-60)",
	"hactl completion bash":       "prints a completion script; reads no instance (D-60)",
	"hactl completion zsh":        "prints a completion script; reads no instance (D-60)",
	"hactl completion fish":       "prints a completion script; reads no instance (D-60)",
	"hactl completion powershell": "prints a completion script; reads no instance (D-60)",
	"hactl rtfm":                  "prints the manual; reads no instance (D-60)",
	"hactl policy show":           "reads the policy file only (D-60)",
	"hactl policy lint":           "D-60's exempt set; under a profile with no rules it has nothing to check against the instance",
	"hactl agent ls":              "reads agent directories under --root, not Home Assistant (D-167)",
}

// sweepSeeded is the part of needles the sweep put into Home Assistant
// itself: no static text can carry it.
func sweepSeeded(needles []string) []string {
	var out []string
	for _, n := range needles {
		if strings.Contains(strings.ToLower(n), "pg_sweep") || strings.Contains(n, "PG Sweep") || strings.Contains(n, "pgsweep") {
			out = append(out, n)
		}
	}
	return out
}

// sweepHidden is a value only Home Assistant holds: no output of a read
// command under the read-nothing profile may carry it.
const sweepHidden = "pgsweep-hidden-7f3a"

// seedReadSweep builds what the read sweep looks for: an area, floor and
// label with distinctive names, a dashboard naming the secret, and an
// input_text holding the secret as its state.
func seedReadSweep(t *testing.T, inst *hatest.Instance) {
	t.Helper()
	ws := dialRawWS(t, inst.URL(), inst.Token())
	floorEnv := ws.mustSucceed(t, map[string]any{"type": "config/floor_registry/create", "name": "PG Sweep Secret Floor"})
	var floor struct {
		ID string `json:"floor_id"`
	}
	_ = json.Unmarshal(floorEnv.Result, &floor)
	area := pgCreateArea(t, ws, "PG Sweep Secret Area")
	label := pgCreateLabel(t, ws, "PG Sweep Secret Label")
	textEnv := ws.mustSucceed(t, map[string]any{"type": "input_text/create", "name": "PG Sweep Secret Text", "max": 100})
	var text struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(textEnv.Result, &text); err != nil || text.ID == "" {
		t.Fatalf("input_text/create answered no id: %v %s", err, textEnv.Result)
	}
	t.Cleanup(func() {
		c := dialRawWS(t, inst.URL(), inst.Token())
		c.send(t, map[string]any{"type": "input_text/delete", "input_text_id": text.ID})
		c.send(t, map[string]any{"type": "config/label_registry/delete", "label_id": label})
		c.send(t, map[string]any{"type": "config/area_registry/delete", "area_id": area})
		c.send(t, map[string]any{"type": "config/floor_registry/delete", "floor_id": floor.ID})
	})
	waitForState(t, inst, "input_text.pg_sweep_secret_text")
	pgCallService(t, inst, "input_text", "set_value", map[string]any{"entity_id": "input_text.pg_sweep_secret_text", "value": sweepHidden})
	pgUpdateEntity(t, ws, "input_text.pg_sweep_secret_text", map[string]any{"area_id": area, "labels": []string{label}, "name": "PG Sweep Secret Text"})
	pgCreateDashboard(t, inst, "pg-sweep-secret", map[string]any{
		"title": "PG Sweep Secret Dashboard",
		"views": []any{map[string]any{"title": "PG Sweep Secret View", "path": "secret", "cards": []any{
			map[string]any{"type": "markdown", "content": sweepHidden},
			map[string]any{"type": "entities", "entities": []any{"input_text.pg_sweep_secret_text"}},
		}}},
	})
}

// waitForState polls HA until it has a state for id.
func waitForState(t *testing.T, inst *hatest.Instance, id string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if code, _ := pgHTTP(t, inst, http.MethodGet, "/api/states/"+id, nil); code == http.StatusOK {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("HA never reported a state for %s", id)
}

// sweepNeedles is what the oracle reads from Home Assistant directly and no
// read may print: every entity id, every area, floor and label name, every
// entity's friendly name, the seeded secret, and the seeded objects' state.
func sweepNeedles(t *testing.T, inst *hatest.Instance) []string {
	t.Helper()
	set := map[string]bool{sweepHidden: true}
	for _, s := range pgRawStates(t, inst) {
		id, _ := s["entity_id"].(string)
		set[id] = true
		if attrs, ok := s["attributes"].(map[string]any); ok {
			if name, ok := attrs["friendly_name"].(string); ok && len(name) >= 8 {
				set[name] = true
			}
		}
	}
	ws := dialRawWS(t, inst.URL(), inst.Token())
	for _, typ := range []string{"config/area_registry/list", "config/floor_registry/list", "config/label_registry/list"} {
		var rows []struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(ws.mustSucceed(t, map[string]any{"type": typ}).Result, &rows); err != nil {
			t.Fatalf("%s did not parse: %v", typ, err)
		}
		for _, r := range rows {
			if len(r.Name) >= 6 {
				set[r.Name] = true
			}
		}
	}
	for _, must := range []string{"input_text.pg_sweep_secret_text", "PG Sweep Secret Area", "PG Sweep Secret Floor", "PG Sweep Secret Label", "PG Sweep Secret Text"} {
		if !set[must] {
			t.Fatalf("oracle: HA does not hold the seeded %q — the leak check would look for nothing", must)
		}
	}
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// sweepReadCases is one run per read command, aimed at a seeded or fixture
// object where the command takes one.
func sweepReadCases(t *testing.T, tmp, entry string) [][]string {
	t.Helper()
	const target = "input_text.pg_sweep_secret_text"
	diff := sweepWriteFile(t, tmp, "diff.yaml", "id: pg_policy_prod_heating\nalias: x\ntriggers: []\nactions: []\n")
	sdiff := sweepWriteFile(t, tmp, "sdiff.yaml", "pg_policy_allowed:\n  sequence: []\n")
	agents := t.TempDir()
	return [][]string{
		{"agent", "ls", "--root", agents},
		{"area", "ls"},
		{"auto", "cat", "pg_policy_prod_heating"},
		{"auto", "diff", "pg_policy_prod_heating", "-f", diff},
		{"auto", "ls"},
		{"auto", "show", "pg_policy_prod_heating"},
		{"cache", "status"},
		{"cc", "logs", "demo"},
		{"cc", "ls"},
		{"cc", "show", "demo"},
		{"changes"},
		{"companion", "logs"},
		{"companion", "status"},
		{"companion", "wireguard", "status"},
		{"config", "block", "automations.yaml", "pg_policy_prod_heating"},
		{"config", "entries"},
		{"config", "file", "configuration.yaml"},
		{"config", "files"},
		{"config", "show", entry},
		{"dash", "grep", target},
		{"dash", "ls"},
		{"dash", "resources"},
		{"dash", "show", "pg-sweep-secret"},
		{"device", "ls"},
		{"device", "show", "pg_sweep_no_such_device"},
		{"energy", "show"},
		{"ent", "anomalies", target},
		{"ent", "hist", target},
		{"ent", "ls"},
		{"ent", "related", target},
		{"ent", "show", target},
		{"ent", "who", target},
		{"floor", "ls"},
		{"grep", "pg_sweep"},
		{"health"},
		{"helper", "cat", target},
		{"helper", "ls"},
		{"helper", "show", target},
		{"issues"},
		{"label", "ls"},
		{"log"},
		{"log", "show", "1"},
		{"policy", "check", "entity:" + target, "read"},
		{"policy", "lint"},
		{"policy", "review"},
		{"policy", "show"},
		{"ref", "scan", target},
		{"ref", "validate"},
		{"rtfm"},
		{"script", "cat", "pg_policy_allowed"},
		{"script", "diff", "pg_policy_allowed", "-f", sdiff},
		{"script", "ls"},
		{"script", "show", "pg_policy_allowed"},
		{"tpl", "cat", "pg_policy_lock"},
		{"tpl", "eval", "{{ states('" + target + "') }}"},
		{"trace", "show", "automation.pg_policy_prod_heating"},
		{"version"},
		{"help"},
		{"completion", "bash"},
		{"completion", "zsh"},
		{"completion", "fish"},
		{"completion", "powershell"},
	}
}

// sweepLeaks returns the needles in out that the caller did not hand the
// command itself: echoing an argument back is not reading it, and neither is
// the command's own guess of an entity id from it — `auto show x`, finding
// no automation it may read, asks for `automation.x` and is refused naming it,
// whether or not x exists.
func sweepLeaks(out string, args, needles []string) []string {
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

// sweepMCPCall runs one tool call through an in-process MCP server pinned to
// dir — the agent's own transport, without --allow-writes (reads need none).
func sweepMCPCall(t *testing.T, dir string, args []string) (string, bool) {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	srv := mcpserver.NewServer(mcpserver.Options{
		Runner: cmd.RunWithOutputContext, ResolvePath: cmd.FindCommandPath,
		NoManualInject: true, Dir: dir, Version: "test",
	})
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Wait() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "sweep", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	quoted := make([]string, 0, len(args))
	for _, a := range args {
		quoted = append(quoted, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "hactl", Arguments: map[string]any{"command": strings.Join(quoted, " ")}})
	if err != nil {
		t.Fatalf("CallTool(%v): %v", args, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
			sb.WriteString("\n")
		}
	}
	return sb.String(), res.IsError
}

// assertSweepReadClosure holds the read sweep to the command tree: every
// leaf that is no write row is run or excepted, and every case is such a
// leaf. It returns the commands run.
func assertSweepReadClosure(t *testing.T, cases [][]string) map[string]bool {
	t.Helper()
	checked, exempt := sweepManifestWriteCommands(t)
	leaves := map[string]bool{}
	for _, p := range cmd.LeafCommandPaths() {
		if _, ex := exempt[p]; !checked[p] && !ex {
			leaves[p] = true
		}
	}
	// `config show` is a read whose probe is a write row: it is read here.
	leaves["hactl config show"] = true
	if len(leaves) < 50 {
		t.Fatalf("only %d read commands derived from the command tree — the derivation has stopped matching", len(leaves))
	}
	ran := map[string]bool{}
	for _, c := range cases {
		p := sweepCommandPath(t, c)
		if !leaves[p] {
			t.Errorf("the read sweep runs %s, which is no read command", p)
		}
		ran[p] = true
	}
	for p := range leaves {
		if _, ex := sweepReadExempt[p]; !ran[p] && !ex {
			t.Errorf("%s is a read command the deny-all read sweep does not run — add a case or an exemption with a reason", p)
		}
	}
	for p := range sweepReadExempt {
		if ran[p] {
			t.Errorf("%s is both run and exempt", p)
		}
	}
	return ran
}

// TestPolicySweepReadsDenyAll — W-6 / W-2 acceptance: under a profile that
// may read nothing, every read command — over an in-process MCP server and as
// the CLI binary, in prose and --json — prints no entity id, no area, floor
// or label name, no friendly name and no seeded state that Home Assistant
// holds. Echoing an argument back is not a leak; everything else is.
func TestPolicySweepReadsDenyAll(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	seedReadSweep(t, inst)
	needles := sweepNeedles(t, inst)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), sweepReadPolicy)
	bin := pgReadsBinary(t)
	cases := sweepReadCases(t, t.TempDir(), sweepDemoEntry(t, inst))

	if len(sweepSeeded(needles)) < 5 {
		t.Fatalf("only %v of the needles are the sweep's own seeds", sweepSeeded(needles))
	}
	ran := assertSweepReadClosure(t, cases)

	seeded := sweepSeeded(needles)
	refusedOrEmpty := 0
	for _, c := range cases {
		path := sweepCommandPath(t, c)
		_, needsNothing := sweepReadNeedsNothing[path]
		look := needles
		if needsNothing {
			look = seeded
		}
		for _, form := range [][]string{c, append(slices.Clone(c), "--json")} {
			text, isErr := sweepMCPCall(t, dir, form)
			if leaks := sweepLeaks(text, form, look); len(leaks) > 0 {
				t.Errorf("MCP %v: the read-nothing profile let %q through:\n%s", form, leaks, text)
			}
			r := pgReadsExec(t, bin, dir, form...)
			if leaks := sweepLeaks(r.stdout+r.stderr, form, look); len(leaks) > 0 {
				t.Errorf("CLI: the read-nothing profile let %q through:\n%s", leaks, r)
			}
			if needsNothing {
				continue
			}
			// Under the read-nothing profile a read is refused, ends in an
			// error before reading, or answers with what it hid counted.
			if isErr || r.code != 0 || strings.Contains(r.stdout+r.stderr, "hidden by policy: ") {
				refusedOrEmpty++
			}
		}
	}
	t.Logf("deny-all read sweep: %d read commands, %d runs per transport (prose and --json), %d exempt; %d of the non-exempt runs refused, failed or counted hidden rows",
		len(ran), 2*len(cases), len(sweepReadExempt), refusedOrEmpty)
	if refusedOrEmpty == 0 {
		t.Error("no run was refused or hid anything — the profile was not in effect")
	}
}

// ---------------------------------------------------------------------------
// W-3 (G-3): the policy file removed between two calls.
// ---------------------------------------------------------------------------

// TestPolicySweepRequiredPolicyRemovedBetweenCalls — G-3's probe as a test:
// in an instance directory marked HACTL_POLICY=required, a call refused by
// the policy stays refused after policy.yaml is renamed away — now as
// policy_missing — and the lock it aimed at never moves.
func TestPolicySweepRequiredPolicyRemovedBetweenCalls(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	dir := setupInstanceDir(t, inst, sweepWritePolicy, true)
	bin := pgReadsBinary(t)
	before := pgStateOf(t, inst, rig.Lock).State
	call := []string{"svc", "call", "lock.unlock", "--data", `{"entity_id":"` + rig.Lock + `"}`}

	r := pgReadsExec(t, bin, dir, append(slices.Clone(call), "--confirm")...)
	if r.code != 1 || !strings.Contains(r.stdout, "policy_denied: would ") {
		t.Errorf("with policy.yaml: want the policy's refusal\n%s", r)
	}
	if err := os.Rename(filepath.Join(dir, "policy.yaml"), filepath.Join(dir, "policy.yaml.bak")); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{call, append(slices.Clone(call), "--confirm")} {
		r = pgReadsExec(t, bin, dir, args...)
		if r.code != 1 || !strings.Contains(r.stderr, "policy_missing: ") || !strings.Contains(r.stderr, "this instance requires a policy; the owner restores it") {
			t.Errorf("after the file was removed: want policy_missing, exit 1 — never allow-all\n%s", r)
		}
		if strings.Contains(r.stdout, "dry-run: would") {
			t.Errorf("after the file was removed: a plan was printed\n%s", r)
		}
	}
	if after := pgStateOf(t, inst, rig.Lock).State; after != before {
		t.Errorf("%s went %q → %q while the instance's policy was missing", rig.Lock, before, after)
	}
}
