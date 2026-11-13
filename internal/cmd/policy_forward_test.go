package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/manual"
)

// Service calls past their target (manual "Service calls: reach"; D-106,
// D-107): the entities a group or a scene forwards a call to, and the typed
// address — automation, script, helper, template entity — of what a call
// reaches. Also a where:-narrowed allow deciding a write at the command layer
// (D-55, D-80).

// liveAnswer is a websocket answer a test may change between two runs; the
// fake server marshals it under its lock.
type liveAnswer struct {
	mu sync.Mutex
	v  any
}

func (a *liveAnswer) MarshalJSON() ([]byte, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return json.Marshal(a.v)
}

func (a *liveAnswer) set(v any) {
	a.mu.Lock()
	a.v = v
	a.mu.Unlock()
}

// fwdState is one row of the rig's GET /api/states.
type fwdState struct {
	id    string
	attrs map[string]any
}

// fwdStates is a house with a light group, a scene, an automation, a script,
// a helper and a template lock — every way a call reaches more than its
// target names, or a typed address behind an entity.
var fwdStates = []fwdState{
	{"light.all_lights", map[string]any{"entity_id": []string{"light.sofa", "light.desk"}}},
	{"light.sofa", nil},
	{"light.desk", nil},
	{"scene.leave_home", map[string]any{"entity_id": []string{"light.sofa", "lock.front_door"}, "id": "1700000000"}},
	{"lock.front_door", nil},
	{"automation.heating_control", map[string]any{"id": "prod_heating"}},
	{"automation.no_id", nil},
	{"script.prod_disarm", nil},
	{"input_boolean.alarm_armed", nil},
}

// fwdRegistry is the entity registry of that house.
var fwdRegistry = []any{
	map[string]any{"entity_id": "light.all_lights", "unique_id": "grp1", "platform": "group"},
	map[string]any{"entity_id": "light.sofa", "unique_id": "s1", "platform": "demo"},
	map[string]any{"entity_id": "light.desk", "unique_id": "d1", "platform": "demo"},
	map[string]any{"entity_id": "scene.leave_home", "unique_id": "1700000000", "platform": "homeassistant"},
	map[string]any{"entity_id": "lock.front_door", "unique_id": "front_door_lock", "platform": "template"},
	map[string]any{"entity_id": "automation.heating_control", "unique_id": "prod_heating", "platform": "automation"},
	map[string]any{"entity_id": "script.prod_disarm", "unique_id": "prod_disarm", "platform": "script"},
	map[string]any{"entity_id": "input_boolean.alarm_armed", "unique_id": "alarm_armed", "platform": "input_boolean"},
}

type fwdRig struct {
	*cmdTestServer

	reach    *liveAnswer
	registry *liveAnswer
	mu       sync.Mutex
	posts    []string
}

func startFwdRig(t *testing.T, reach any) *fwdRig {
	t.Helper()
	rig := &fwdRig{reach: &liveAnswer{v: reach}, registry: &liveAnswer{v: fwdRegistry}}
	ws := map[string]any{
		"extract_from_target":         rig.reach,
		"config/entity_registry/list": rig.registry,
		"config/device_registry/list": []any{},
		"config/area_registry/list": []any{
			map[string]any{"area_id": "wohnzimmer", "name": "Wohnzimmer"},
			map[string]any{"area_id": "kueche", "name": "Küche"},
		},
		"config/label_registry/list": []any{},
	}
	services := `[
	 {"domain":"light","services":{"turn_on":{"fields":{},"target":{"entity":[{"domain":["light"]}]}}}},
	 {"domain":"scene","services":{"turn_on":{"fields":{},"target":{}}}},
	 {"domain":"automation","services":{"turn_off":{"fields":{},"target":{}}}},
	 {"domain":"script","services":{"turn_off":{"fields":{},"target":{}},"turn_on":{"fields":{},"target":{}}}},
	 {"domain":"input_boolean","services":{"turn_off":{"fields":{},"target":{}}}},
	 {"domain":"lock","services":{"unlock":{"fields":{},"target":{}}}}
	]`
	handlers := map[string]http.HandlerFunc{
		"/api/services": func(w http.ResponseWriter, _ *http.Request) { _, _ = fmt.Fprint(w, services) },
		"/api/services/": func(w http.ResponseWriter, r *http.Request) {
			rig.mu.Lock()
			rig.posts = append(rig.posts, strings.TrimPrefix(r.URL.Path, "/api/services/"))
			rig.mu.Unlock()
			_, _ = fmt.Fprint(w, "[]")
		},
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			rows := make([]map[string]any, 0, len(fwdStates))
			for _, s := range fwdStates {
				attrs := s.attrs
				if attrs == nil {
					attrs = map[string]any{}
				}
				rows = append(rows, map[string]any{"entity_id": s.id, "state": "on", "attributes": attrs})
			}
			_ = json.NewEncoder(w).Encode(rows)
		},
		"/api/states/": func(w http.ResponseWriter, r *http.Request) {
			id := strings.TrimPrefix(r.URL.Path, "/api/states/")
			_ = json.NewEncoder(w).Encode(map[string]any{"entity_id": id, "state": "off", "attributes": map[string]any{}})
		},
	}
	rig.cmdTestServer = startCmdServer(t, ws, handlers)
	return rig
}

func (r *fwdRig) sent() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.posts)
}

// refusedBoth runs args as a preview and under --confirm and asserts both are
// refused naming every denied line, and that no service was called.
func (r *fwdRig) refusedBoth(t *testing.T, what string, args []string, denied ...string) {
	t.Helper()
	for _, confirm := range []bool{false, true} {
		run := slices.Clone(args)
		if confirm {
			run = append(run, "--confirm")
		}
		out, err := runPolicyCLI(t, r.dir, run...)
		for _, d := range denied {
			wantRefused(t, fmt.Sprintf("%s (confirm=%v)", what, confirm), out, err, d)
		}
	}
	if got := r.sent(); len(got) != 0 {
		t.Errorf("%s: a refused call was sent: %q", what, got)
	}
}

// TestSvcCallChecksTheMembersAGroupOrSceneForwardsTo (D-106): the live-fire
// reproduction. Home Assistant's reach answer names a light group or a scene
// alone, but the group switches its members and the scene sets every entity it
// lists — each needs control. A denied light behind a group, a denied lock
// behind a scene: refused in the preview and under --confirm, nothing sent;
// the same call is allowed once nothing behind it is denied.
func TestSvcCallChecksTheMembersAGroupOrSceneForwardsTo(t *testing.T) {
	base := svcPolicy(`    default: deny`, `    allow:`,
		`      "entity:light.*": [control]`, `      "entity:lock.*": [control]`, `      "entity:scene.*": [control]`,
		`      "service:light.*": [control]`, `      "service:scene.turn_on": [control]`)

	rig := startFwdRig(t, reachOf("light.all_lights"))
	writePolicyFile(t, rig.dir, base+"    deny:\n      \"entity:light.sofa\": [control]\n")
	rig.refusedBoth(t, "light group", []string{"svc", "call", "light.turn_on", "--data", `{"entity_id":"light.all_lights"}`},
		`control entity:light.sofa — deny "entity:light.sofa" [control] (line 13)`)

	rig = startFwdRig(t, reachOf("scene.leave_home"))
	writePolicyFile(t, rig.dir, base+"    deny:\n      \"entity:lock.front_door\": [control]\n")
	rig.refusedBoth(t, "scene", []string{"svc", "call", "scene.turn_on", "--data", `{"entity_id":"scene.leave_home"}`},
		`control entity:lock.front_door — deny "entity:lock.front_door" [control] (line 13)`)

	writePolicyFile(t, rig.dir, base)
	plan, out, err := planOf(t, rig.dir, "svc", "call", "scene.turn_on", "--data", `{"entity_id":"scene.leave_home"}`)
	if err != nil {
		t.Fatalf("nothing behind the scene is denied: %v\n%s", err, out)
	}
	for _, want := range []string{"control entity:light.sofa", "control entity:lock.front_door", "control entity:scene.leave_home"} {
		if !slices.Contains(plan, want) {
			t.Errorf("the scene's plan lacks %q: %q", want, plan)
		}
	}
}

// TestSvcCallChecksTheTypedAddressOfWhatItReaches (D-107): the owner's deny on
// an automation, a helper, a template entity or a script by its typed address
// also refuses switching it — turning an automation off, a helper off,
// unlocking a template lock, stopping a script. Only a deny decides that check:
// under `default: deny`, a profile that allows the entity and the service
// still switches it without an allow on the typed address.
func TestSvcCallChecksTheTypedAddressOfWhatItReaches(t *testing.T) {
	for _, tc := range []struct {
		name, pattern, entity, service, denied string
	}{
		{"automation", "automation:prod_*", "automation.heating_control", "automation.turn_off", "control automation:prod_heating"},
		{"helper", "helper:input_boolean/alarm_armed", "input_boolean.alarm_armed", "input_boolean.turn_off", "control helper:input_boolean/alarm_armed"},
		{"template", "template:front_door_lock", "lock.front_door", "lock.unlock", "control template:front_door_lock"},
		{"script", "script:prod_*", "script.prod_disarm", "script.turn_off", "control script:prod_disarm"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := startFwdRig(t, reachOf(tc.entity))
			writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, fmt.Sprintf(`      %q: [write]`, tc.pattern)))
			args := []string{"svc", "call", tc.service, "--data", fmt.Sprintf(`{"entity_id":%q}`, tc.entity)}
			denied := fmt.Sprintf(`%s — deny %q [write] (line 6)`, tc.denied, tc.pattern)
			out, err := runPolicyCLI(t, rig.dir, args...)
			wantRefused(t, tc.name+" preview", out, err, denied)
			out, err = runPolicyCLI(t, rig.dir, append(args, "--confirm")...)
			wantRefused(t, tc.name+" --confirm", out, err, denied)
			if got := rig.sent(); len(got) != 0 {
				t.Errorf("%s: a refused call was sent: %q", tc.name, got)
			}

			domain, _, _ := strings.Cut(tc.entity, ".")
			writePolicyFile(t, rig.dir, svcPolicy(`    default: deny`, `    allow:`,
				fmt.Sprintf(`      "entity:%s.*": [control]`, domain), fmt.Sprintf(`      "service:%s": [control]`, tc.service)))
			out, err = runPolicyCLI(t, rig.dir, append(args, "--confirm")...)
			if err != nil {
				t.Errorf("default: deny decided a typed address only a deny may decide: %v\n%s", err, out)
			}
		})
	}

	// An automation with no config id has no typed address: its entity alone.
	rig := startFwdRig(t, reachOf("automation.no_id"))
	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "automation:prod_*": [write]`))
	if out, err := runPolicyCLI(t, rig.dir, "svc", "call", "automation.turn_off", "--data", `{"entity_id":"automation.no_id"}`); err != nil {
		t.Errorf("an automation without a config id was refused by a deny on another id: %v\n%s", err, out)
	}
}

// TestScriptRunReachErrorListsEveryDenial (E-7, manual table `script run`):
// with Home Assistant's reach answer an error, the run is refused for its
// reach — and the refusal still lists the denied script entity, which the
// manual promises is checked for every run.
func TestScriptRunReachErrorListsEveryDenial(t *testing.T) {
	rig := startSvcRig(t, wsErrorResponse{Code: "unknown_error", Message: "boom"})
	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "entity:script.morning_renamed": [control]`))
	out, err := runPolicyCLI(t, rig.dir, "script", "run", "morning_renamed")
	wantRefused(t, "entity denial", out, err, `control entity:script.morning_renamed — deny "entity:script.morning_renamed" [control] (line 6)`)
	if !strings.Contains(out, "fail-closed: reach unknown") {
		t.Errorf("the reach-unknown denial is missing:\n%s", out)
	}
	if !strings.Contains(out, "  checked: 3 resources, 2 denied\n") {
		t.Errorf("checked/denied counts:\n%s", out)
	}
}

// TestWhereSelectorDecidesAWriteLive (D-55, D-80): a where:-narrowed allow
// grants a write while its selector holds — an entity in the allowed area is
// switched — and once the entity has moved out of the area the same call is
// refused by the default, also under --confirm after a preview that passed,
// which then says so.
func TestWhereSelectorDecidesAWriteLive(t *testing.T) {
	rig := startFwdRig(t, reachOf("light.sofa"))
	inArea := func(area string) []any {
		out := slices.Clone(fwdRegistry)
		out[1] = map[string]any{"entity_id": "light.sofa", "unique_id": "s1", "platform": "demo", "area_id": area}
		return out
	}
	rig.registry.set(inArea("wohnzimmer"))
	writePolicyFile(t, rig.dir, svcPolicy(`    default: deny`, `    allow:`,
		`      "entity:light.*": [control]`, `      "service:light.*": [control]`,
		`    where:`, `      "entity:light.*": { area: wohnzimmer }`))
	args := []string{"svc", "call", "light.turn_on", "--data", `{"entity_id":"light.sofa"}`}
	out, err := runPolicyCLI(t, rig.dir, args...)
	if err != nil || !strings.Contains(out, " 2 checked, allowed (profile agent)") {
		t.Fatalf("a light in the allowed area: %v\n%s", err, out)
	}
	recordWitness(stateCacheDir(rig.dir), manual.SessionKey(), "hactl svc call", []string{"light.turn_on"}, time.Now())

	rig.registry.set(inArea("kueche"))
	out, err = runPolicyCLI(t, rig.dir, append(args, "--confirm")...)
	wantRefused(t, "moved out of the area", out, err, "control entity:light.sofa — default: deny")
	if !strings.Contains(out, "  hint: the preview had passed") {
		t.Errorf("the refused --confirm does not say its preview had passed:\n%s", out)
	}
	if got := rig.sent(); len(got) != 0 {
		t.Errorf("a refused call was sent: %q", got)
	}
}
