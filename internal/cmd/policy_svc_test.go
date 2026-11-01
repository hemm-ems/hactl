package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/manual"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// The service-call family under a policy (manual "Enforcement (writes)":
// "Service calls: reach", "Service calls: the service itself"; D-59, D-73,
// D-84, R-1, R-2, R-10): svc call, script run, and the services hactl sends
// itself (script apply's script.reload). Every expectation below is written
// from the manual's table and those decision rows, not from the mapping code.

// svcRig is a fake Home Assistant (and companion) for the family: a live
// state set, the entity registry (a renamed script entity whose unique_id —
// its config key — differs from its object id, D-84), a service list, a
// fixed extract_from_target answer, and a companion script route. It records
// every service call POSTed and every companion PUT.
type svcRig struct {
	*cmdTestServer

	mu    sync.Mutex
	posts map[string]int // "<domain>/<service>" → calls
	puts  map[string]int // "dry_run=<bool>" → companion script PUTs
	log   []string       // every HTTP request, in order: "<METHOD> <path>"
}

// svcLive is the instance's live entity set. script.morning_renamed is the
// script whose config key is `morning` (a registry rename, D-84).
var svcLive = []string{"light.a", "light.b", "lock.door", "script.morning_renamed", "scene.evening"}

func startSvcRig(t *testing.T, reach any) *svcRig {
	t.Helper()
	rig := &svcRig{posts: map[string]int{}, puts: map[string]int{}}
	ws, handlers := rig.fixtures(reach)
	rig.cmdTestServer = startCmdServer(t, ws, handlers)
	pointAtCompanion(t, rig.cmdTestServer)
	return rig
}

// fixtures are the rig's websocket answers and HTTP handlers, recording into
// rig; reach is the extract_from_target answer.
func (rig *svcRig) fixtures(reach any) (map[string]any, map[string]http.HandlerFunc) {
	if rig.posts == nil {
		rig.posts, rig.puts = map[string]int{}, map[string]int{}
	}
	note := func(r *http.Request) {
		rig.mu.Lock()
		rig.log = append(rig.log, r.Method+" "+r.URL.Path)
		rig.mu.Unlock()
	}
	ws := map[string]any{
		"extract_from_target": reach,
		"config/entity_registry/list": []any{
			map[string]any{"entity_id": "light.a", "unique_id": "la"},
			map[string]any{"entity_id": "light.b", "unique_id": "lb"},
			map[string]any{"entity_id": "lock.door", "unique_id": "ld"},
			map[string]any{"entity_id": "script.morning_renamed", "unique_id": "morning", "platform": "script"},
		},
		"config/device_registry/list": []any{},
		"config/area_registry/list":   []any{},
		"config/label_registry/list":  []any{},
		"validate_config":             map[string]any{"actions": map[string]any{"valid": true, "error": nil}},
	}
	services := `[
	 {"domain":"light","services":{"turn_on":{"fields":{},"target":{"entity":[{"domain":["light"]}]}}}},
	 {"domain":"homeassistant","services":{"turn_on":{"fields":{},"target":{}},"restart":{"fields":{}}}},
	 {"domain":"scene","services":{"apply":{"fields":{}}}},
	 {"domain":"script","services":{"turn_on":{"fields":{},"target":{}},"reload":{"fields":{}},"morning":{"fields":{}}}},
	 {"domain":"zwave_js","services":{"clear_lock_usercode":{"fields":{},"target":{}}}}
	]`
	handlers := map[string]http.HandlerFunc{
		"/api/services": func(w http.ResponseWriter, r *http.Request) {
			note(r)
			_, _ = io.WriteString(w, services)
		},
		"/api/services/": func(w http.ResponseWriter, r *http.Request) {
			note(r)
			rig.mu.Lock()
			rig.posts[strings.TrimPrefix(r.URL.Path, "/api/services/")]++
			rig.mu.Unlock()
			_, _ = io.WriteString(w, "[]")
		},
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			note(r)
			rows := make([]map[string]string, 0, len(svcLive))
			for _, id := range svcLive {
				rows = append(rows, map[string]string{"entity_id": id, "state": "on"})
			}
			_ = json.NewEncoder(w).Encode(rows)
		},
		"/api/states/": func(w http.ResponseWriter, r *http.Request) {
			note(r)
			id := strings.TrimPrefix(r.URL.Path, "/api/states/")
			if !slices.Contains(svcLive, id) {
				http.Error(w, "Entity not found.", http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"entity_id": id, "state": "off"})
		},
		"/v1/config/script": func(w http.ResponseWriter, r *http.Request) {
			note(r)
			switch r.Method {
			case http.MethodGet:
				_ = json.NewEncoder(w).Encode(map[string]string{"id": "morning", "content": "morning:\n  alias: Old\n  sequence: []\n"})
			case http.MethodPut:
				rig.mu.Lock()
				rig.puts["dry_run="+r.URL.Query().Get("dry_run")]++
				rig.mu.Unlock()
				_, _ = io.WriteString(w, `{"status":"applied","reloaded":true}`)
			default:
				http.Error(w, "not found", http.StatusNotFound)
			}
		},
	}
	return ws, handlers
}

func (r *svcRig) posted(path string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.posts[path]
}

func (r *svcRig) put(dryRun bool) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.puts[fmt.Sprintf("dry_run=%v", dryRun)]
}

func (r *svcRig) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.log)
}

// reachOf is an extract_from_target answer.
func reachOf(ids ...string) map[string]any {
	if ids == nil {
		ids = []string{}
	}
	return map[string]any{"referenced_entities": ids, "referenced_devices": []string{}, "referenced_areas": []string{}}
}

// svcPolicy is a profile `agent` with the given rule lines under it (already
// indented by four spaces).
func svcPolicy(lines ...string) string {
	return "version: 1\nactive: agent\nprofiles:\n  agent:\n" + strings.Join(lines, "\n") + "\n"
}

// planOf runs args under the rig's policy and returns the plan the command
// declared, as `<verb> <resource>` strings sorted, and the run's output/error.
func planOf(t *testing.T, dir string, args ...string) ([]string, string, error) {
	t.Helper()
	rec := &coverageRecorder{}
	policyRecorder = rec
	defer func() { policyRecorder = nil }()
	out, err := runPolicyCLI(t, dir, args...)
	got := make([]string, 0, len(rec.plan))
	for _, c := range rec.plan {
		s := c.String()
		if c.DenyOnly {
			s += reqmap.DenyOnlySuffix
		}
		got = append(got, s)
	}
	sort.Strings(got)
	return got, out, err
}

// hasPolicyNote reports whether an allowed preview's prose carries the
// policy line, `policy: <n> checked, allowed (profile agent)` (the field
// column is padded to the preview's widest key).
func hasPolicyNote(out string, n int) bool {
	return regexp.MustCompile(fmt.Sprintf(`\n  policy: +%d checked, allowed \(profile agent\)\n`, n)).MatchString(out)
}

// wantRefused asserts a policy refusal naming denied (a full `denied:` line
// body, e.g. `control entity:light.a — deny "entity:light.a" [control] (line 6)`).
func wantRefused(t *testing.T, what, out string, err error, denied string) {
	t.Helper()
	if cliExitCode(err) != 1 || policyErrorClass(err) != enforce.ClassDenied {
		t.Fatalf("%s: exit %d class %q (%v), want 1 policy_denied\n%s", what, cliExitCode(err), policyErrorClass(err), err, out)
	}
	if !strings.Contains(out, "  denied: "+denied+"\n") {
		t.Errorf("%s: the report does not name %q:\n%s", what, denied, out)
	}
}

// TestSvcCallRefusedByItsReach (E-1, D-59 as refined): a light service called
// on an area needs control on every light Home Assistant says the area
// reaches; a deny on one of them refuses the preview and --confirm alike,
// naming the rule, and nothing is called. An entity service counts only its
// own domain: a deny on the lock the same area holds does not refuse it.
func TestSvcCallRefusedByItsReach(t *testing.T) {
	rig := startSvcRig(t, reachOf("light.a", "lock.door"))
	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "entity:light.a": [control]`))
	args := []string{"svc", "call", "light.turn_on", "--data", `{"area_id":"hall"}`}

	out, err := runPolicyCLI(t, rig.dir, args...)
	want := "policy_denied: would call light.turn_on — refused by policy, nothing was written\n" +
		"  denied: control entity:light.a — deny \"entity:light.a\" [control] (line 6)\n" +
		"  checked: 2 resources, 1 denied\n" +
		"  profile: agent (selected by active)\n" +
		"  hint: the owner would change the rule on line 6; allowed: 1 of 2 checked resources, but the command is refused as a whole — report it; do not edit policy.yaml\n"
	if out != want {
		t.Errorf("refused preview\n got: %q\nwant: %q", out, want)
	}
	if err == nil || err.Error() != `policy_denied: 1 of 2 resources denied (profile agent)` {
		t.Errorf("stderr line = %v", err)
	}
	out, err = runPolicyCLI(t, rig.dir, append(args, "--confirm")...)
	wantRefused(t, "--confirm", out, err, `control entity:light.a — deny "entity:light.a" [control] (line 6)`)
	if n := rig.posted("light/turn_on"); n != 0 {
		t.Errorf("a refused plan called the service %d times", n)
	}

	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "entity:lock.door": [control]`))
	plan, out, err := planOf(t, rig.dir, append(args, "--confirm")...)
	if err != nil {
		t.Fatalf("a deny outside the entity service's domain refused it: %v\n%s", err, out)
	}
	if !slices.Equal(plan, []string{"control entity:light.a", "control service:light.turn_on"}) {
		t.Errorf("plan = %q", plan)
	}
	if n := rig.posted("light/turn_on"); n != 1 {
		t.Errorf("service calls = %d, want 1", n)
	}
}

// TestSvcCallChecksTheServiceAlways (E-2, R-10, D-73): the service is checked
// on every call and never skipped because the entities passed; it is decided
// like any resource — `default: deny` needs an allow for it, a profile without
// `default: deny` allows a service no deny matches. A target-less call needs
// its service check alone and asks Home Assistant nothing for it.
func TestSvcCallChecksTheServiceAlways(t *testing.T) {
	rig := startSvcRig(t, reachOf("light.a"))
	writePolicyFile(t, rig.dir, svcPolicy(`    default: deny`, `    allow:`, `      "entity:*": [control]`))
	out, err := runPolicyCLI(t, rig.dir, "svc", "call", "light.turn_on", "--data", `{"area_id":"hall"}`)
	wantRefused(t, "default deny, entities allowed", out, err, "control service:light.turn_on — default: deny")
	if strings.Contains(out, "denied: control entity:") {
		t.Errorf("the allowed entity was reported denied:\n%s", out)
	}

	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "entity:lock.*": [control]`))
	before := rig.commandCount("extract_from_target")
	plan, out, err := planOf(t, rig.dir, "svc", "call", "homeassistant.restart")
	if err != nil || !hasPolicyNote(out, 1) {
		t.Fatalf("a default-allow profile must allow a service no deny matches: %v\n%s", err, out)
	}
	if !slices.Equal(plan, []string{"control service:homeassistant.restart"}) {
		t.Errorf("target-less plan = %q, want the service check alone", plan)
	}
	if rig.commandCount("extract_from_target") != before {
		t.Error("a call with no target asked Home Assistant for its reach")
	}

	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "service:homeassistant.restart": [control]`))
	out, err = runPolicyCLI(t, rig.dir, "svc", "call", "homeassistant.restart", "--confirm")
	wantRefused(t, "deny on the service", out, err, `control service:homeassistant.restart — deny "service:homeassistant.restart" [control] (line 6)`)
	if rig.posted("homeassistant/restart") != 0 {
		t.Error("a refused service was called")
	}
}

// TestSvcCallReachOfOtherServices (R-1): a service that is not one domain's
// entity service — homeassistant.*, an integration's own service — is checked
// against the WHOLE referenced set; `entity_id: all` is every
// live entity of the domain for an entity service and every live entity
// otherwise, and is never sent to extract_from_target.
func TestSvcCallReachOfOtherServices(t *testing.T) {
	rig := startSvcRig(t, reachOf("light.a", "lock.door"))
	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "entity:lock.door": [control]`))
	lockDenied := `control entity:lock.door — deny "entity:lock.door" [control] (line 6)`

	asked := rig.commandCount("extract_from_target")
	out, err := runPolicyCLI(t, rig.dir, "svc", "call", "zwave_js.clear_lock_usercode", "--data", `{"entity_id":"all"}`)
	wantRefused(t, "platform service with all", out, err, lockDenied)
	if rig.commandCount("extract_from_target") != asked {
		t.Error("`all` was sent to extract_from_target")
	}

	plan, out, err := planOf(t, rig.dir, "svc", "call", "light.turn_on", "--data", `{"entity_id":"all"}`)
	if err != nil {
		t.Fatalf("light.turn_on all reaches no lock: %v\n%s", err, out)
	}
	if !slices.Equal(plan, []string{"control entity:light.a", "control entity:light.b", "control service:light.turn_on"}) {
		t.Errorf("entity service with all: plan %q, want every light", plan)
	}

	out, err = runPolicyCLI(t, rig.dir, "svc", "call", "homeassistant.turn_on", "--data", `{"area_id":"hall"}`)
	wantRefused(t, "homeassistant.turn_on on an area", out, err, lockDenied)
}

// TestSvcCallFanOutAndScriptRuns (R-2, D-84): homeassistant.turn_on calls each
// domain's own turn_on and starts every script in its reach, so it needs those
// services and script:<config key>/run — the key read from the entity
// registry, not the (renamed) entity id.
func TestSvcCallFanOutAndScriptRuns(t *testing.T) {
	rig := startSvcRig(t, reachOf("light.a", "script.morning_renamed"))
	writePolicyFile(t, rig.dir, policyAllowEverything)
	args := []string{"svc", "call", "homeassistant.turn_on", "--data", `{"area_id":"hall"}`}
	plan, out, err := planOf(t, rig.dir, args...)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := []string{
		"control entity:light.a", "control entity:script.morning_renamed", "control script:morning (deny only)", "control script:morning/run",
		"control service:homeassistant.turn_on", "control service:light.turn_on", "control service:script.turn_on",
	}
	if !slices.Equal(plan, want) {
		t.Errorf("plan\n got: %q\nwant: %q", plan, want)
	}
	for rule, denied := range map[string]string{
		`"script:morning/run": [control]`:     `control script:morning/run — deny "script:morning/run" [control] (line 6)`,
		`"service:script.turn_on": [control]`: `control service:script.turn_on — deny "service:script.turn_on" [control] (line 6)`,
	} {
		writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      `+rule))
		out, err := runPolicyCLI(t, rig.dir, append(args, "--confirm")...)
		wantRefused(t, rule, out, err, denied)
	}
	if rig.posted("homeassistant/turn_on") != 0 {
		t.Error("a refused fan-out was called")
	}
}

// TestSvcCallScansDataForEntities (D-73): every mapping key and every string
// value of --data, at any depth, that names a live entity needs control —
// scene.apply's `entities: {lock.door: unlocked}` names it as a key.
func TestSvcCallScansDataForEntities(t *testing.T) {
	rig := startSvcRig(t, reachOf())
	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "entity:lock.door": [control]`))
	denied := `control entity:lock.door — deny "entity:lock.door" [control] (line 6)`
	for _, data := range []string{
		`{"entities":{"lock.door":"unlocked"}}`,
		`{"entities":{"light.a":"on"},"extra":[{"deep":["LOCK.DOOR"]}]}`,
	} {
		out, err := runPolicyCLI(t, rig.dir, "svc", "call", "scene.apply", "--data", data, "--confirm")
		wantRefused(t, data, out, err, denied)
	}
	if rig.posted("scene/apply") != 0 {
		t.Error("a refused scene.apply was called")
	}
}

// TestSvcCallPerScriptServiceChecksItsRun (D-73, D-84): script.<x> starts the
// script whose config key is <x> — its run is checked under that key with no
// registry lookup, also when the call carries no data.
func TestSvcCallPerScriptServiceChecksItsRun(t *testing.T) {
	rig := startSvcRig(t, reachOf())
	writePolicyFile(t, rig.dir, policyAllowEverything)
	plan, out, err := planOf(t, rig.dir, "svc", "call", "script.morning")
	if err != nil || !slices.Equal(plan, []string{"control script:morning/run", "control service:script.morning"}) {
		t.Fatalf("plan %q, %v\n%s", plan, err, out)
	}
	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "script:morning/run": [control]`))
	out, err = runPolicyCLI(t, rig.dir, "svc", "call", "script.morning", "--confirm")
	wantRefused(t, "script.morning", out, err, `control script:morning/run — deny "script:morning/run" [control] (line 6)`)
	if rig.posted("script/morning") != 0 {
		t.Error("a refused script was started")
	}
}

// TestSvcCallReachUnknownIsRefused (D-73): when Home Assistant answers the
// reach question with an error, the call cannot be decided — refused
// `fail-closed: reach unknown` in the preview and under --confirm, under a
// profile that would allow everything, and never sent.
func TestSvcCallReachUnknownIsRefused(t *testing.T) {
	rig := startSvcRig(t, wsErrorResponse{Code: "invalid_format", Message: "bad target"})
	writePolicyFile(t, rig.dir, policyAllowEverything)
	for _, confirm := range []bool{false, true} {
		args := []string{"svc", "call", "light.turn_on", "--data", `{"area_id":"hall"}`, "--json"}
		if confirm {
			args = append(args, "--confirm")
		}
		out, err := runPolicyCLI(t, rig.dir, args...)
		if cliExitCode(err) != 1 || policyErrorClass(err) != enforce.ClassDenied {
			t.Fatalf("confirm=%v: exit %d (%v)\n%s", confirm, cliExitCode(err), err, out)
		}
		m := decodeObject(t, out)
		wantField(t, "refusal", m, "dry_run", !confirm)
		pol, _ := m["details"].(map[string]any)["policy"].(map[string]any)
		denied, _ := pol["denied"].([]any)
		if len(denied) != 1 {
			t.Fatalf("denied = %v", pol["denied"])
		}
		d, _ := denied[0].(map[string]any)
		if rule, _ := d["rule"].(string); !strings.HasPrefix(rule, "fail-closed: reach unknown") || d["line"] != float64(0) {
			t.Errorf("confirm=%v: denial %v, want fail-closed: reach unknown, line 0", confirm, d)
		}
	}
	if rig.posted("light/turn_on") != 0 {
		t.Error("a call whose reach is unknown was sent")
	}
}

// TestSvcCallConfirmAfterAPassingPreviewSaysSo (D-80, R-18, H-2's parity with a
// witness present): a preview that passed, then a policy edited before
// --confirm — the confirmed run is refused with the same rule the preview
// would now name, says the preview had passed, and sends nothing.
func TestSvcCallConfirmAfterAPassingPreviewSaysSo(t *testing.T) {
	rig := startSvcRig(t, reachOf("light.a"))
	writePolicyFile(t, rig.dir, policyAllowEverything)
	args := []string{"svc", "call", "light.turn_on", "--data", `{"area_id":"hall"}`}
	if out, err := runPolicyCLI(t, rig.dir, args...); err != nil {
		t.Fatalf("allowed preview: %v\n%s", err, out)
	}
	recordWitness(stateCacheDir(rig.dir), manual.SessionKey(), "hactl svc call", []string{"light.turn_on"}, time.Now())
	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "entity:light.*": [control]`))
	denied := `control entity:light.a — deny "entity:light.*" [control] (line 6)`
	preview, perr := runPolicyCLI(t, rig.dir, args...)
	wantRefused(t, "preview", preview, perr, denied)
	confirmed, cerr := runPolicyCLI(t, rig.dir, append(args, "--confirm")...)
	wantRefused(t, "--confirm", confirmed, cerr, denied)
	if perr.Error() != cerr.Error() {
		t.Errorf("preview and --confirm name different rules:\n%s\n%s", perr, cerr)
	}
	if !strings.Contains(confirmed, "  hint: the preview had passed") || strings.Contains(preview, "the preview had passed") {
		t.Errorf("the refused --confirm does not say its preview had passed:\n%s", confirmed)
	}
	if rig.posted("light/turn_on") != 0 {
		t.Error("the refused --confirm called the service")
	}
}

// TestScriptRunPlan (manual table: `script run <x>`; D-73, D-84): the run
// checks control script:<config key>/run, control service:script.turn_on and
// control entity:script.<x> — the key from the entity registry — in the
// preview and under --confirm, before the call.
func TestScriptRunPlan(t *testing.T) {
	rig := startSvcRig(t, reachOf("script.morning_renamed"))
	writePolicyFile(t, rig.dir, policyAllowEverything)
	plan, out, err := planOf(t, rig.dir, "script", "run", "morning_renamed")
	if err != nil || !hasPolicyNote(out, 4) {
		t.Fatalf("allowed preview: %v\n%s", err, out)
	}
	want := []string{
		"control entity:script.morning_renamed", "control script:morning (deny only)", "control script:morning/run",
		"control service:script.turn_on",
	}
	if !slices.Equal(plan, want) {
		t.Errorf("plan\n got: %q\nwant: %q", plan, want)
	}
	for rule, denied := range map[string]string{
		`"script:morning/run": [control]`:            `control script:morning/run — deny "script:morning/run" [control] (line 6)`,
		`"service:script.turn_on": [control]`:        `control service:script.turn_on — deny "service:script.turn_on" [control] (line 6)`,
		`"entity:script.morning_renamed": [control]`: `control entity:script.morning_renamed — deny "entity:script.morning_renamed" [control] (line 6)`,
	} {
		writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      `+rule))
		for _, confirm := range []bool{false, true} {
			args := []string{"script", "run", "morning_renamed"}
			if confirm {
				args = append(args, "--confirm")
			}
			out, err := runPolicyCLI(t, rig.dir, args...)
			wantRefused(t, fmt.Sprintf("%s confirm=%v", rule, confirm), out, err, denied)
		}
	}
	if rig.posted("script/turn_on") != 0 {
		t.Error("a refused script run was sent")
	}
}

// TestScriptRunPlanWithoutAReachAnswerStillChecksTheRun: the manual promises
// the entity and the run for every `script run`. When Home Assistant's reach
// answer names no script entity, the plan still holds control on the entity
// and the run of every script (kind-wide) — a superset, never fewer.
func TestScriptRunPlanWithoutAReachAnswerStillChecksTheRun(t *testing.T) {
	rig := startSvcRig(t, reachOf())
	writePolicyFile(t, rig.dir, policyAllowEverything)
	plan, out, err := planOf(t, rig.dir, "script", "run", "morning_renamed")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := []string{
		"control entity:script.morning_renamed", "control script:*/run", "control script:morning (deny only)",
		"control service:script.turn_on",
	}
	if !slices.Equal(plan, want) {
		t.Errorf("plan\n got: %q\nwant: %q", plan, want)
	}
	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "script:other/run": [control]`))
	out, err = runPolicyCLI(t, rig.dir, "script", "run", "morning_renamed")
	wantRefused(t, "kind-wide run", out, err, `control script:*/run — deny "script:other/run" [control] (line 6)`)
}

// TestScriptApplyPlanChecksTheReload (D-73, manual table: `script apply`):
// hactl reloads scripts itself after the write, so the plan holds
// control service:script.reload beside edit script:<id>/config — a profile
// that may edit the script but not reload is refused before anything is
// written, in the preview (before the companion's dry-run PUT) and under
// --confirm (before the backup and the write).
func TestScriptApplyPlanChecksTheReload(t *testing.T) {
	rig := startSvcRig(t, reachOf())
	file := filepath.Join(rig.dir, "morning.yaml")
	if err := os.WriteFile(file, []byte("morning:\n  alias: New\n  sequence: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"script", "apply", "morning", "-f", file}

	writePolicyFile(t, rig.dir, policyAllowEverything)
	plan, out, err := planOf(t, rig.dir, args...)
	if err != nil || !hasPolicyNote(out, 2) {
		t.Fatalf("allowed preview: %v\n%s", err, out)
	}
	if !slices.Equal(plan, []string{"control service:script.reload", "edit script:morning/config"}) {
		t.Errorf("plan = %q", plan)
	}
	dryPuts := rig.put(true)

	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "service:script.reload": [control]`))
	denied := `control service:script.reload — deny "service:script.reload" [control] (line 6)`
	out, err = runPolicyCLI(t, rig.dir, args...)
	wantRefused(t, "preview", out, err, denied)
	if strings.Contains(out, "diff:") {
		t.Errorf("the refusal followed a printed diff — the report must be the whole stdout:\n%s", out)
	}
	out, err = runPolicyCLI(t, rig.dir, append(args, "--confirm")...)
	wantRefused(t, "--confirm", out, err, denied)
	if rig.put(true) != dryPuts || rig.put(false) != 0 || rig.posted("script/reload") != 0 {
		t.Errorf("a refused apply wrote: dry-run PUTs %d (was %d), PUTs %d, reloads %d", rig.put(true), dryPuts, rig.put(false), rig.posted("script/reload"))
	}
	if entries, err := os.ReadDir(filepath.Join(rig.dir, "backups")); err == nil && len(entries) > 0 {
		t.Errorf("a refused apply wrote a backup: %v", entries)
	}
}

// TestServiceFamilyNoProfileIsByteIdentical (D-58): with no policy file, and
// with a file that puts no profile in effect, each command's preview is byte
// for byte what it printed before the policy existed and sends exactly the
// requests it sent then — no reach question, no live-set or registry read —
// and no audit log appears. The pinned outputs and request lists were taken
// from the commands as they were before this family was wired (60a8501).
func TestServiceFamilyNoProfileIsByteIdentical(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string // "<file>" is replaced by the script file
		out  string
		reqs []string
	}{
		{
			name: "svc call",
			args: []string{"svc", "call", "light.turn_on", "--data", `{"area_id":"hall"}`},
			out: "dry-run: would call light.turn_on\n  domain:  light\n  service: turn_on\n  data:    {\"area_id\":\"hall\"}\n" +
				"re-run with --confirm after the user explicitly confirms this exact action\n",
			reqs: []string{"GET /api/services"},
		},
		{
			name: "script run",
			args: []string{"script", "run", "morning_renamed"},
			out:  "dry-run: would execute script.morning_renamed\n  entity_id: script.morning_renamed\n  via:       script.turn_on\nuse --confirm to run it\n",
			reqs: []string{"GET /api/states/script.morning_renamed"},
		},
		{
			name: "script apply",
			args: []string{"script", "apply", "morning", "-f", "<file>"},
			out: "diff:\n-alias: Old\n+alias: New\n sequence: []\ndry-run: would apply script\n  script:     morning\n" +
				"  validation: ok (HA validate_config)\nuse --confirm to apply\n",
			reqs: []string{"GET /v1/config/script", "PUT /v1/config/script"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, body := range []string{"", policyImplicitAllow} {
				checkNoProfilePreview(t, body, tc.args, tc.out, tc.reqs)
			}
		})
	}
}

// The family's --confirm commands in R-12's coverage harness: each confirmed
// run, under an allow-all profile, sends only writes whose request-layer
// verdicts its plan holds — the reach, the fan-out, the script runs, the
// entities --data names, and the service hactl sends itself.
func init() {
	coverage := func(name string, reach any, args ...string) confirmedCoverageCase {
		ws, handlers := (&svcRig{}).fixtures(reach)
		return confirmedCoverageCase{name: name, args: args, ws: ws, http: handlers}
	}
	apply := coverage("script apply", reachOf(), "script", "apply", "morning", "-f", "<file>", "--confirm")
	apply.setup = func(t *testing.T, ts *cmdTestServer, args []string) []string {
		t.Helper()
		pointAtCompanion(t, ts)
		file := filepath.Join(ts.dir, "morning.yaml")
		if err := os.WriteFile(file, []byte("morning:\n  alias: New\n  sequence: []\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		args[slices.Index(args, "<file>")] = file
		return args
	}
	confirmedCoverageCases = append(confirmedCoverageCases,
		coverage("svc call on an area", reachOf("light.a", "lock.door"),
			"svc", "call", "light.turn_on", "--data", `{"area_id":"hall"}`, "--confirm"),
		coverage("svc call homeassistant fan-out with a script", reachOf("light.a", "script.morning_renamed"),
			"svc", "call", "homeassistant.turn_on", "--data", `{"area_id":"hall"}`, "--confirm"),
		coverage("svc call platform service with all", reachOf(),
			"svc", "call", "zwave_js.clear_lock_usercode", "--data", `{"entity_id":"all"}`, "--confirm"),
		coverage("svc call with entities in --data", reachOf(),
			"svc", "call", "scene.apply", "--data", `{"entities":{"lock.door":"unlocked"}}`, "--confirm"),
		coverage("svc call per-script service", reachOf(), "svc", "call", "script.morning", "--confirm"),
		coverage("svc call --return", reachOf("light.b"),
			"svc", "call", "light.turn_on", "--data", `{"entity_id":"light.b"}`, "--return", "--confirm"),
		coverage("script run", reachOf("script.morning_renamed"), "script", "run", "morning_renamed", "--confirm"),
		apply,
	)
}

// checkNoProfilePreview runs one preview of TestServiceFamilyNoProfileIsByteIdentical
// under policy body ("" = no file) and holds it to the pinned output and
// requests.
func checkNoProfilePreview(t *testing.T, body string, template []string, wantOut string, wantReqs []string) {
	t.Helper()
	rig := startSvcRig(t, reachOf("light.a"))
	file := filepath.Join(rig.dir, "morning.yaml")
	if err := os.WriteFile(file, []byte("morning:\n  alias: New\n  sequence: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := slices.Clone(template)
	if i := slices.Index(args, "<file>"); i >= 0 {
		args[i] = file
	}
	if body != "" {
		writePolicyFile(t, rig.dir, body)
	}
	out, err := runPolicyCLI(t, rig.dir, args...)
	if err != nil {
		t.Fatalf("policy %q: %v\n%s", body, err, out)
	}
	if out != wantOut {
		t.Errorf("policy %q: preview\n got: %q\nwant: %q", body, out, wantOut)
	}
	if got := rig.requests(); !slices.Equal(got, wantReqs) {
		t.Errorf("policy %q: requests %q, want %q", body, got, wantReqs)
	}
	if n, m := rig.commandCount("extract_from_target"), rig.commandCount("config/entity_registry/list"); n != 0 || m != 0 {
		t.Errorf("policy %q: asked the reach %d times and read the entity registry %d times", body, n, m)
	}
	if _, err := os.Stat(filepath.Join(rig.dir, "cache", enforce.AuditFileName)); err == nil {
		t.Errorf("policy %q: an audit log was written", body)
	}
}
