//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// Permission policy, phase 2 stage 2b: writes are enforced (H-29's Docker
// tier). Every expectation below is the contract's — docs/manual.md
// "Enforcement (writes)" and its per-command table, docs/policy.md "What hactl
// enforces", D-58, D-60, D-66, D-70, D-71, D-73, D-75, D-76, D-80 — and each
// refusal is proven twice: hactl's report names the rule written in the
// test's own policy text, and Home Assistant, read back over a raw socket,
// shows nothing was written (H-12).
//
// Commands that need the companion (`ref replace`, `ent rename`, `auto
// apply`, `script apply`) are enforced in internal/companiontest's
// policy_enforce_e2e_test.go: this tier boots Home Assistant alone.
// ============================================================================

const pgProfile = "pg_agent"

// pgSvc is `svc call <service> --data <data>`.
func pgSvc(service, data string) []string {
	return []string{"svc", "call", service, "--data", data}
}

func pgEntityData(entityID string) string {
	return fmt.Sprintf(`{"entity_id":%q}`, entityID)
}

// ---------------------------------------------------------------------------
// One test per verb: a deny refuses preview and --confirm naming the rule,
// nothing is written; an allow lets the same command through.
// ---------------------------------------------------------------------------

// TestPolicyEnforceControlVerb — control: `svc call light.toggle` on a denied
// light is refused; on an allowed one it switches the light. The profile is
// default: deny, so the allowed call needs both its service and its entity
// allowed (E-2).
func TestPolicyEnforceControlVerb(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	allowed, denied := rig.Lights[0], rig.Lights[1]

	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    default: deny
    allow:
      "service:light.*": [control]
      "entity:light.*": [control]
    deny:
      "entity:%s": [control]
`, pgProfile, pgProfile, denied)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)

	before := pgStateOf(t, inst, denied).State
	assertPolicyRefused(t, dir, pgSvc("light.toggle", pgEntityData(denied)), pgDenied{
		Resource: "entity:" + denied, Verb: "control",
		Rule: fmt.Sprintf(`deny "entity:%s" [control]`, denied),
		Line: policyLineOf(t, policy, `"entity:`+denied+`"`),
	})
	if got := pgStateOf(t, inst, denied).State; got != before {
		t.Errorf("%s went %s → %s: a refused control reached Home Assistant", denied, before, got)
	}

	beforeAllowed := pgStateOf(t, inst, allowed).State
	assertPolicyAllowedPreview(t, dir, pgSvc("light.toggle", pgEntityData(allowed))...)
	if got := pgStateOf(t, inst, allowed).State; got != beforeAllowed {
		t.Fatalf("an allowed preview switched %s (%s → %s)", allowed, beforeAllowed, got)
	}
	if out, err := runHactlDirErr(t, dir, append(pgSvc("light.toggle", pgEntityData(allowed)), "--confirm")...); err != nil {
		t.Fatalf("allowed light.toggle --confirm refused: %v\n%s", err, out)
	}
	if got := pgStateOf(t, inst, allowed).State; got == beforeAllowed {
		t.Errorf("allowed light.toggle --confirm left %s %s: the allow did not let the call through", allowed, got)
	}
}

// TestPolicyEnforceEditVerb — edit: `ent set-label` checks only
// entity:<x>/registry/labels (manual table), so a profile that allows exactly
// that sub-resource lets one entity be labelled and refuses the denied one.
func TestPolicyEnforceEditVerb(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	allowed, denied := rig.Lights[0], rig.Lights[1]
	ws := dialRawWS(t, inst.URL(), inst.Token())
	label := pgCreateLabel(t, ws, "pg_policy_edit_label")
	t.Cleanup(func() {
		dialRawWS(t, inst.URL(), inst.Token()).send(t, map[string]any{"type": "config/label_registry/delete", "label_id": label})
	})

	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    default: deny
    allow:
      "entity:*/registry/labels": [edit]
    deny:
      "entity:%s/registry/labels": [edit]
`, pgProfile, pgProfile, denied)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)

	beforeDenied := pgEntityRegistry(t, ws)[denied]
	assertPolicyRefused(t, dir, []string{"ent", "set-label", denied, label}, pgDenied{
		Resource: "entity:" + denied + "/registry/labels", Verb: "edit",
		Rule: fmt.Sprintf(`deny "entity:%s/registry/labels" [edit]`, denied),
		Line: policyLineOf(t, policy, `"entity:`+denied+`/registry/labels"`),
	})
	if after := pgEntityRegistry(t, ws)[denied]; !reflect.DeepEqual(after, beforeDenied) {
		t.Errorf("refused set-label changed %s's registry row:\n before %+v\n after  %+v", denied, beforeDenied, after)
	}

	assertPolicyAllowedPreview(t, dir, "ent", "set-label", allowed, label)
	if out, err := runHactlDirErr(t, dir, "ent", "set-label", allowed, label, "--confirm"); err != nil {
		t.Fatalf("allowed set-label --confirm refused: %v\n%s", err, out)
	}
	if !slices.Contains(pgEntityRegistry(t, ws)[allowed].Labels, label) {
		t.Errorf("allowed set-label --confirm did not label %s in HA's registry", allowed)
	}
}

// TestPolicyEnforceCreateVerb — create: `dash create` checks create
// dashboard:<url_path>.
func TestPolicyEnforceCreateVerb(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	const deniedURL, allowedURL = "pg-policy-create-denied", "pg-policy-create-ok"
	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "dashboard:%s": [create]
`, pgProfile, pgProfile, deniedURL)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
	ws := dialRawWS(t, inst.URL(), inst.Token())
	t.Cleanup(func() {
		cleanup := dialRawWS(t, inst.URL(), inst.Token())
		for _, u := range []string{deniedURL, allowedURL} {
			if id, ok := pgDashboards(t, cleanup)[u]; ok {
				cleanup.send(t, map[string]any{"type": "lovelace/dashboards/delete", "dashboard_id": id})
			}
		}
	})

	assertPolicyRefused(t, dir, []string{"dash", "create", "--url-path", deniedURL, "--title", "PG Denied"}, pgDenied{
		Resource: "dashboard:" + deniedURL, Verb: "create",
		Rule: fmt.Sprintf(`deny "dashboard:%s" [create]`, deniedURL),
		Line: policyLineOf(t, policy, `"dashboard:`+deniedURL+`"`),
	})
	if _, exists := pgDashboards(t, ws)[deniedURL]; exists {
		t.Errorf("refused dash create left dashboard %s in HA", deniedURL)
	}

	assertPolicyAllowedPreview(t, dir, "dash", "create", "--url-path", allowedURL, "--title", "PG OK")
	if out, err := runHactlDirErr(t, dir, "dash", "create", "--url-path", allowedURL, "--title", "PG OK", "--confirm"); err != nil {
		t.Fatalf("allowed dash create --confirm refused: %v\n%s", err, out)
	}
	if _, exists := pgDashboards(t, ws)[allowedURL]; !exists {
		t.Errorf("allowed dash create --confirm: %s is not in HA's dashboard list", allowedURL)
	}
}

// TestPolicyEnforceDeleteVerb — delete: `label delete` of a label nothing
// holds checks delete label:<id> alone.
func TestPolicyEnforceDeleteVerb(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	ws := dialRawWS(t, inst.URL(), inst.Token())
	keep := pgCreateLabel(t, ws, "pg_policy_keep")
	drop := pgCreateLabel(t, ws, "pg_policy_drop")
	t.Cleanup(func() {
		cleanup := dialRawWS(t, inst.URL(), inst.Token())
		for _, id := range []string{keep, drop} {
			cleanup.send(t, map[string]any{"type": "config/label_registry/delete", "label_id": id})
		}
	})
	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "label:%s": [delete]
`, pgProfile, pgProfile, keep)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)

	assertPolicyRefused(t, dir, []string{"label", "delete", keep}, pgDenied{
		Resource: "label:" + keep, Verb: "delete",
		Rule: fmt.Sprintf(`deny "label:%s" [delete]`, keep),
		Line: policyLineOf(t, policy, `"label:`+keep+`"`),
	})
	if _, exists := pgLabels(t, ws)[keep]; !exists {
		t.Errorf("refused label delete removed %s from HA", keep)
	}

	assertPolicyAllowedPreview(t, dir, "label", "delete", drop)
	if out, err := runHactlDirErr(t, dir, "label", "delete", drop, "--confirm"); err != nil {
		t.Fatalf("allowed label delete --confirm refused: %v\n%s", err, out)
	}
	if _, exists := pgLabels(t, ws)[drop]; exists {
		t.Errorf("allowed label delete --confirm left %s in HA", drop)
	}
}

// TestPolicyEnforceKindWideCreate — D-75: HA mints an area's id, so `area
// create` checks create on every area id; a create-deny on one area refuses
// every area create and reports `area:*` with kind_wide. A label create under
// the same profile is untouched.
func TestPolicyEnforceKindWideCreate(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	const areaName, labelName = "PG Policy Fresh Area", "PG Policy Fresh Label"
	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "area:pg_policy_secret": [create]
`, pgProfile, pgProfile)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
	ws := dialRawWS(t, inst.URL(), inst.Token())
	t.Cleanup(func() {
		cleanup := dialRawWS(t, inst.URL(), inst.Token())
		for id, row := range pgAreas(t, cleanup) {
			if row.Name == areaName {
				cleanup.send(t, map[string]any{"type": "config/area_registry/delete", "area_id": id})
			}
		}
		for id, row := range pgLabels(t, cleanup) {
			if row.Name == labelName {
				cleanup.send(t, map[string]any{"type": "config/label_registry/delete", "label_id": id})
			}
		}
	})

	assertPolicyRefused(t, dir, []string{"area", "create", areaName}, pgDenied{
		Resource: "area:*", Verb: "create", KindWide: true,
		Rule: `deny "area:pg_policy_secret" [create]`,
		Line: policyLineOf(t, policy, `"area:pg_policy_secret"`),
	})
	for _, row := range pgAreas(t, ws) {
		if row.Name == areaName {
			t.Errorf("refused area create left %q in HA's area registry", areaName)
		}
	}

	assertPolicyAllowedPreview(t, dir, "label", "create", labelName)
	if out, err := runHactlDirErr(t, dir, "label", "create", labelName, "--confirm"); err != nil {
		t.Fatalf("label create under an area-only deny was refused: %v\n%s", err, out)
	}
	found := false
	for _, row := range pgLabels(t, ws) {
		found = found || row.Name == labelName
	}
	if !found {
		t.Errorf("allowed label create --confirm: no label %q in HA", labelName)
	}
}

// ---------------------------------------------------------------------------
// E-1 / R-1 / R-2: a service call is checked by what it reaches.
// ---------------------------------------------------------------------------

// TestPolicyEnforceServiceReach builds an area holding an allowed light and a
// denied lock, a label on the device of a denied light, and a group of an
// allowed and a denied light, then calls services over each target form. Each
// refusal leaves every light and the lock as they were.
func TestPolicyEnforceServiceReach(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	lightOK, lightDenied := rig.Lights[0], rig.Lights[1]
	ws := dialRawWS(t, inst.URL(), inst.Token())

	area := pgCreateArea(t, ws, "PG Policy Reach Area")
	pgUpdateEntity(t, ws, lightOK, map[string]any{"area_id": area})
	pgUpdateEntity(t, ws, rig.Lock, map[string]any{"area_id": area})
	label := pgCreateLabel(t, ws, "pg_policy_reach_label")
	ws.mustSucceed(t, map[string]any{
		"type": "config/device_registry/update", "device_id": rig.DevOf[lightDenied], "labels": []string{label},
	})
	pgCallService(t, inst, "group", "set", map[string]any{
		"object_id": "pg_policy_lights", "entities": []string{lightOK, lightDenied},
	})
	t.Cleanup(func() {
		cleanup := dialRawWS(t, inst.URL(), inst.Token())
		cleanup.send(t, map[string]any{"type": "config/device_registry/update", "device_id": rig.DevOf[lightDenied], "labels": []string{}})
		cleanup.send(t, map[string]any{"type": "config/label_registry/delete", "label_id": label})
		cleanup.send(t, map[string]any{"type": "config/area_registry/delete", "area_id": area})
		pgHTTP(t, inst, http.MethodPost, "/api/services/group/remove", map[string]any{"object_id": "pg_policy_lights"})
	})
	if _, ok := currentStates(t, inst)["group.pg_policy_lights"]; !ok {
		t.Fatal("group.set answered but group.pg_policy_lights has no state — the group case would prove nothing")
	}

	lockPolicy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "entity:lock.*": [control]
`, pgProfile, pgProfile)
	lockDir := pgInstanceDir(t, inst.URL(), inst.Token(), lockPolicy)
	lockDenied := pgDenied{
		Resource: "entity:" + rig.Lock, Verb: "control",
		Rule: `deny "entity:lock.*" [control]`, Line: policyLineOf(t, lockPolicy, `"entity:lock.*"`),
	}

	lightPolicy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "entity:%s": [control]
`, pgProfile, pgProfile, lightDenied)
	lightDir := pgInstanceDir(t, inst.URL(), inst.Token(), lightPolicy)
	lightDeniedEntry := pgDenied{
		Resource: "entity:" + lightDenied, Verb: "control",
		Rule: fmt.Sprintf(`deny "entity:%s" [control]`, lightDenied),
		Line: policyLineOf(t, lightPolicy, `"entity:`+lightDenied+`"`),
	}

	watched := []string{lightOK, lightDenied, rig.Lock}
	snapshot := func() map[string]string {
		out := map[string]string{}
		for _, id := range watched {
			out[id] = pgStateOf(t, inst, id).State
		}
		return out
	}
	refused := func(t *testing.T, dir string, args []string, want pgDenied) {
		t.Helper()
		before := snapshot()
		assertPolicyRefused(t, dir, args, want)
		if after := snapshot(); !reflect.DeepEqual(before, after) {
			t.Errorf("refused %v changed states (all or nothing, E-7):\n before %v\n after  %v", args, before, after)
		}
	}
	lockTarget := "unlocked"
	if pgStateOf(t, inst, rig.Lock).State == "unlocked" {
		lockTarget = "locked"
	}

	// R-1: homeassistant.* is not one domain's entity service, so the whole
	// area counts — the lock too, although homeassistant.toggle never
	// switches a lock.
	t.Run("area/homeassistant.toggle", func(t *testing.T) {
		refused(t, lockDir, pgSvc("homeassistant.toggle", fmt.Sprintf(`{"area_id":%q}`, area)), lockDenied)
	})
	// The light domain's own service counts only the area's lights.
	t.Run("area/light.toggle counts lights only", func(t *testing.T) {
		assertPolicyAllowedPreview(t, lockDir, pgSvc("light.toggle", fmt.Sprintf(`{"area_id":%q}`, area))...)
	})
	// `all` on a non-entity service means every live entity of every domain.
	t.Run("entity_id all/homeassistant.toggle", func(t *testing.T) {
		refused(t, lockDir, pgSvc("homeassistant.toggle", `{"entity_id":"all"}`), lockDenied)
	})
	// An entity named in --data, outside the target: a mapping key.
	t.Run("data key/scene.apply", func(t *testing.T) {
		refused(t, lockDir, pgSvc("scene.apply", fmt.Sprintf(`{"entities":{%q:%q}}`, rig.Lock, lockTarget)), lockDenied)
	})
	t.Run("device/light.toggle", func(t *testing.T) {
		refused(t, lightDir, pgSvc("light.toggle", fmt.Sprintf(`{"device_id":%q}`, rig.DevOf[lightDenied])), lightDeniedEntry)
	})
	t.Run("label on the device/light.toggle", func(t *testing.T) {
		refused(t, lightDir, pgSvc("light.toggle", fmt.Sprintf(`{"label_id":%q}`, label)), lightDeniedEntry)
	})
	t.Run("group/light.toggle", func(t *testing.T) {
		refused(t, lightDir, pgSvc("light.toggle", pgEntityData("group.pg_policy_lights")), lightDeniedEntry)
	})
	t.Run("entity_id all/light.toggle", func(t *testing.T) {
		refused(t, lightDir, pgSvc("light.toggle", `{"entity_id":"all"}`), lightDeniedEntry)
	})
	// The same profile lets a call through whose reach holds no denied entity.
	t.Run("allowed reach goes through", func(t *testing.T) {
		before := pgStateOf(t, inst, lightOK).State
		args := append(pgSvc("light.toggle", pgEntityData(lightOK)), "--confirm")
		if out, err := runHactlDirErr(t, lightDir, args...); err != nil {
			t.Fatalf("light.toggle of %s under a deny on %s was refused: %v\n%s", lightOK, lightDenied, err, out)
		}
		if got := pgStateOf(t, inst, lightOK).State; got == before {
			t.Errorf("allowed light.toggle --confirm left %s %s", lightOK, got)
		}
	})
}

// TestPolicyEnforceScriptStart — R-2 and D-73: every way of starting a script
// needs control script:<id>/run, and homeassistant.turn_on on a script entity
// also needs the script domain's own service.
func TestPolicyEnforceScriptStart(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	const deniedEntity, allowedEntity = "script.pg_policy_denied", "script.pg_policy_allowed"
	lastTriggered := func(id string) any { return pgStateOf(t, inst, id).Attributes["last_triggered"] }

	runPolicy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "script:pg_policy_denied/run": [control]
`, pgProfile, pgProfile)
	runDir := pgInstanceDir(t, inst.URL(), inst.Token(), runPolicy)
	runDenied := pgDenied{
		Resource: "script:pg_policy_denied/run", Verb: "control",
		Rule: `deny "script:pg_policy_denied/run" [control]`,
		Line: policyLineOf(t, runPolicy, `"script:pg_policy_denied/run"`),
	}

	beforeDenied := lastTriggered(deniedEntity)
	for name, args := range map[string][]string{
		"script run":                  {"script", "run", "pg_policy_denied"},
		"svc call script.<x>":         pgSvc("script.pg_policy_denied", `{}`),
		"svc call script.turn_on":     pgSvc("script.turn_on", pgEntityData(deniedEntity)),
		"homeassistant.turn_on (R-2)": pgSvc("homeassistant.turn_on", pgEntityData(deniedEntity)),
	} {
		t.Run(name, func(t *testing.T) {
			assertPolicyRefused(t, runDir, args, runDenied)
			if got := lastTriggered(deniedEntity); !reflect.DeepEqual(got, beforeDenied) {
				t.Errorf("%v: %s last_triggered %v → %v — the refused start ran the script", args, deniedEntity, beforeDenied, got)
			}
		})
	}

	beforeAllowed := lastTriggered(allowedEntity)
	assertPolicyAllowedPreview(t, runDir, "script", "run", "pg_policy_allowed")
	if out, err := runHactlDirErr(t, runDir, "script", "run", "pg_policy_allowed", "--confirm"); err != nil {
		t.Fatalf("allowed script run --confirm refused: %v\n%s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for reflect.DeepEqual(lastTriggered(allowedEntity), beforeAllowed) && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	afterAllowed := lastTriggered(allowedEntity)
	if reflect.DeepEqual(afterAllowed, beforeAllowed) {
		t.Fatalf("allowed script run --confirm never ran %s (last_triggered still %v)", allowedEntity, afterAllowed)
	}

	// R-2's other half: the per-domain service homeassistant.turn_on fans out
	// to. Denying script.turn_on refuses the start although the script's own
	// run is allowed.
	svcPolicy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "service:script.turn_on": [control]
`, pgProfile, pgProfile)
	svcDir := pgInstanceDir(t, inst.URL(), inst.Token(), svcPolicy)
	svcDenied := pgDenied{
		Resource: "service:script.turn_on", Verb: "control",
		Rule: `deny "service:script.turn_on" [control]`,
		Line: policyLineOf(t, svcPolicy, `"service:script.turn_on"`),
	}
	assertPolicyRefused(t, svcDir, pgSvc("homeassistant.turn_on", pgEntityData(allowedEntity)), svcDenied)
	assertPolicyRefused(t, svcDir, []string{"script", "run", "pg_policy_allowed"}, svcDenied)
	if got := lastTriggered(allowedEntity); !reflect.DeepEqual(got, afterAllowed) {
		t.Errorf("a start refused for service:script.turn_on ran %s (last_triggered %v → %v)", allowedEntity, afterAllowed, got)
	}
}

// TestPolicyEnforceTargetlessService — E-2 under D-52/D-73: the service is
// checked on every call, also without a target; default: deny refuses a
// service no allow names even though every entity is allowed; a profile
// without default: deny allows a service no deny matches.
func TestPolicyEnforceTargetlessService(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	location := func() [2]float64 {
		status, body := pgHTTP(t, inst, http.MethodGet, "/api/config", nil)
		if status != http.StatusOK {
			t.Fatalf("GET /api/config: %d %s", status, body)
		}
		var cfg struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		}
		if err := json.Unmarshal(body, &cfg); err != nil {
			t.Fatalf("/api/config did not parse: %v", err)
		}
		return [2]float64{cfg.Latitude, cfg.Longitude}
	}
	original := location()
	t.Cleanup(func() {
		pgCallService(t, inst, "homeassistant", "set_location", map[string]any{"latitude": original[0], "longitude": original[1]})
	})
	moved := [2]float64{original[0] - 3.5, original[1] - 1.5}
	args := pgSvc("homeassistant.set_location", fmt.Sprintf(`{"latitude":%v,"longitude":%v}`, moved[0], moved[1]))

	defaultDeny := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    default: deny
    allow:
      "entity:*": [control]
`, pgProfile, pgProfile)
	assertPolicyRefused(t, pgInstanceDir(t, inst.URL(), inst.Token(), defaultDeny), args, pgDenied{
		Resource: "service:homeassistant.set_location", Verb: "control", Rule: "default: deny", Line: 0,
	})

	serviceDeny := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "service:homeassistant.set_location": [control]
`, pgProfile, pgProfile)
	assertPolicyRefused(t, pgInstanceDir(t, inst.URL(), inst.Token(), serviceDeny), args, pgDenied{
		Resource: "service:homeassistant.set_location", Verb: "control",
		Rule: `deny "service:homeassistant.set_location" [control]`,
		Line: policyLineOf(t, serviceDeny, `"service:homeassistant.set_location"`),
	})
	if got := location(); got != original {
		t.Fatalf("a refused set_location moved the home from %v to %v", original, got)
	}

	// D-52: no default: deny and no service rule — the service is allowed.
	d52 := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "entity:lock.*": [write]
`, pgProfile, pgProfile)
	d52Dir := pgInstanceDir(t, inst.URL(), inst.Token(), d52)
	assertPolicyAllowedPreview(t, d52Dir, args...)
	if out, err := runHactlDirErr(t, d52Dir, append(slices.Clone(args), "--confirm")...); err != nil {
		t.Fatalf("set_location under a default-allow profile was refused: %v\n%s", err, out)
	}
	if got := location(); got != moved {
		t.Errorf("allowed set_location --confirm: home at %v, want %v", got, moved)
	}
}

// ---------------------------------------------------------------------------
// E-7 / D-66 on dashboards; R-9 cascades.
// ---------------------------------------------------------------------------

// TestPolicyEnforceDashboardAllOrNothing — `dash replace` rewriting two views
// of which one is denied is refused whole: neither view is written. `dash
// save` changing only the title (a change addressable only at the dashboard)
// is refused by the view deny as a partial denial (D-66). A replace that
// touches only the allowed view goes through: writes are checked at the
// finest address.
func TestPolicyEnforceDashboardAllOrNothing(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	const urlPath = pgE7Dashboard
	config := map[string]any{
		"title": "PG E7",
		"views": []any{
			map[string]any{"title": "Open", "path": "open", "cards": []any{map[string]any{
				"type": "entities", "entities": []any{"sensor.pg_policy_old", "sensor.pg_policy_only_open"},
			}}},
			map[string]any{"title": "Secret", "path": "secret", "cards": []any{map[string]any{
				"type": "entities", "entities": []any{"sensor.pg_policy_old"},
			}}},
		},
	}
	pgCreateDashboard(t, inst, urlPath, config)
	ws := dialRawWS(t, inst.URL(), inst.Token())
	before := pgE7DashboardConfig(t, ws)

	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "dashboard:%s/views/secret": [edit]
`, pgProfile, pgProfile, urlPath)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
	rule := fmt.Sprintf(`deny "dashboard:%s/views/secret" [edit]`, urlPath)
	line := policyLineOf(t, policy, `/views/secret"`)

	assertPolicyRefused(t, dir, []string{"dash", "replace", "sensor.pg_policy_old", "sensor.pg_policy_new", urlPath}, pgDenied{
		Resource: "dashboard:" + urlPath + "/views/secret", Verb: "edit", Rule: rule, Line: line,
	})
	if got := pgE7DashboardConfig(t, ws); !reflect.DeepEqual(got, before) {
		t.Fatalf("refused dash replace wrote to HA:\n before %s\n after  %s", mustJSON(t, before), mustJSON(t, got))
	}

	retitled := map[string]any{"title": "PG E7 renamed", "views": config["views"]}
	b, err := json.Marshal(retitled)
	if err != nil {
		t.Fatal(err)
	}
	file := writeJSONFile(t, string(b))
	assertPolicyRefused(t, dir, []string{"dash", "save", urlPath, "--file", file}, pgDenied{
		Resource: "dashboard:" + urlPath, Verb: "edit", Rule: rule, Line: line, Partial: true,
	})
	if got := pgE7DashboardConfig(t, ws); !reflect.DeepEqual(got, before) {
		t.Fatalf("refused dash save wrote to HA:\n before %s\n after  %s", mustJSON(t, before), mustJSON(t, got))
	}

	finer := []string{"dash", "replace", "sensor.pg_policy_only_open", "sensor.pg_policy_moved", urlPath}
	assertPolicyAllowedPreview(t, dir, finer...)
	if out, err := runHactlDirErr(t, dir, append(slices.Clone(finer), "--confirm")...); err != nil {
		t.Fatalf("a replace in the allowed view only was refused: %v\n%s", err, out)
	}
	got := pgE7DashboardConfig(t, ws)
	views, _ := got["views"].([]any)
	if len(views) != 2 {
		t.Fatalf("dashboard has %d views after the allowed replace: %s", len(views), mustJSON(t, got))
	}
	if s := mustJSON(t, views[0]); !strings.Contains(s, "sensor.pg_policy_moved") || strings.Contains(s, "sensor.pg_policy_only_open") {
		t.Errorf("allowed replace did not rewrite the open view: %s", s)
	}
	beforeViews, _ := before["views"].([]any)
	if !reflect.DeepEqual(views[1], beforeViews[1]) {
		t.Errorf("allowed replace touched the denied view:\n before %s\n after  %s", mustJSON(t, beforeViews[1]), mustJSON(t, views[1]))
	}
}

// TestPolicyEnforceLabelDeleteCascade — R-9 / D-76: deleting a label edits
// every holder, so a deny on any holder's label field (entity, device) or on
// a holding area refuses the delete, the label and every holder untouched.
func TestPolicyEnforceLabelDeleteCascade(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	holder := rig.Lights[2]
	device := rig.DevOf[rig.Lights[0]]
	ws := dialRawWS(t, inst.URL(), inst.Token())

	label := pgCreateLabel(t, ws, "pg_policy_cascade")
	area := pgCreateArea(t, ws, "PG Policy Cascade Area")
	pgUpdateEntity(t, ws, holder, map[string]any{"labels": []string{label}})
	ws.mustSucceed(t, map[string]any{"type": "config/device_registry/update", "device_id": device, "labels": []string{label}})
	ws.mustSucceed(t, map[string]any{"type": "config/area_registry/update", "area_id": area, "labels": []string{label}})
	t.Cleanup(func() {
		cleanup := dialRawWS(t, inst.URL(), inst.Token())
		cleanup.send(t, map[string]any{"type": "config/device_registry/update", "device_id": device, "labels": []string{}})
		cleanup.send(t, map[string]any{"type": "config/entity_registry/update", "entity_id": holder, "labels": []string{}})
		cleanup.send(t, map[string]any{"type": "config/label_registry/delete", "label_id": label})
		cleanup.send(t, map[string]any{"type": "config/area_registry/delete", "area_id": area})
	})

	holdersIntact := func(t *testing.T) {
		t.Helper()
		if _, ok := pgLabels(t, ws)[label]; !ok {
			t.Errorf("refused label delete removed %s", label)
		}
		if !slices.Contains(pgEntityRegistry(t, ws)[holder].Labels, label) {
			t.Errorf("refused label delete stripped %s from entity %s", label, holder)
		}
		if !slices.Contains(pgDeviceRegistry(t, ws)[device].Labels, label) {
			t.Errorf("refused label delete stripped %s from device %s", label, device)
		}
		if !slices.Contains(pgAreas(t, ws)[area].Labels, label) {
			t.Errorf("refused label delete stripped %s from area %s", label, area)
		}
	}
	holdersIntact(t)

	for _, tc := range []struct {
		name, pattern, verbs, resource string
	}{
		{"entity holder", "entity:" + holder + "/registry/labels", "edit", "entity:" + holder + "/registry/labels"},
		{"device holder", "device:" + device + "/registry/labels", "edit", "device:" + device + "/registry/labels"},
		{"area holder", "area:" + area, "edit", "area:" + area},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      %q: [%s]
`, pgProfile, pgProfile, tc.pattern, tc.verbs)
			dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
			assertPolicyRefused(t, dir, []string{"label", "delete", label}, pgDenied{
				Resource: tc.resource, Verb: "edit",
				Rule: fmt.Sprintf(`deny %q [%s]`, tc.pattern, tc.verbs),
				Line: policyLineOf(t, policy, fmt.Sprintf("%q", tc.pattern)),
			})
			holdersIntact(t)
		})
	}

	open := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "entity:lock.*": [write]
`, pgProfile, pgProfile)
	openDir := pgInstanceDir(t, inst.URL(), inst.Token(), open)
	if out, err := runHactlDirErr(t, openDir, "label", "delete", label, "--confirm"); err != nil {
		t.Fatalf("label delete with no holder denied was refused: %v\n%s", err, out)
	}
	if _, ok := pgLabels(t, ws)[label]; ok {
		t.Errorf("allowed label delete --confirm left %s in HA", label)
	}
}

// ---------------------------------------------------------------------------
// D-58, D-60, D-71: no file changes nothing; a broken file refuses; the audit
// log records every write check.
// ---------------------------------------------------------------------------

// TestPolicyAbsentChangesNothing — D-58: with no policy.yaml, and with a file
// that puts no profile in effect (implicit allow-all — even one whose only
// profile denies every write), a write's preview is byte-identical to hactl
// without the feature, the same requests reach Home Assistant, the confirmed
// call goes through, and no audit log appears. A named allow-all profile is
// the control that shows each instrument would see the feature.
func TestPolicyAbsentChangesNothing(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	light := rig.Lights[2]
	proxy := newRecordingProxy(t, inst.URL())
	data := pgEntityData(light)
	args := pgSvc("light.toggle", data)

	// Today's preview, verbatim (internal/cmd/svc.go, dryrun.go at 39e39f1).
	wantPreview := "dry-run: would call light.toggle\n" +
		"  domain:  light\n" +
		"  service: toggle\n" +
		"  data:    " + data + "\n" +
		"re-run with --confirm after the user explicitly confirms this exact action\n"

	implicit := `version: 1
profiles:
  pg_other:
    default: deny
    deny:
      "entity:*": [write]
`
	for name, policy := range map[string]string{"no file": "", "implicit allow-all": implicit} {
		t.Run(name, func(t *testing.T) {
			dir := pgInstanceDir(t, proxy.URL(), inst.Token(), policy)
			assertRunsAsWithoutPolicy(t, inst, proxy, dir, light, args, wantPreview)
		})
	}

	t.Run("control: a named allow-all profile is seen", func(t *testing.T) {
		dir := pgInstanceDir(t, proxy.URL(), inst.Token(), "version: 1\nactive: pg_open\nprofiles:\n  pg_open: {}\n")
		out, err := runHactlDirErr(t, dir, args...)
		if err != nil {
			t.Fatalf("preview under an allow-all profile failed: %v\n%s", err, out)
		}
		if out == wantPreview || !allowedPreviewLine("pg_open").MatchString(out) {
			t.Errorf("preview under a named profile lacks the policy line — the instruments above would not see the feature:\n%s", out)
		}
		if _, err := os.Stat(auditLogPath(dir)); err != nil {
			t.Errorf("no audit log under a named profile: %v", err)
		}
	})
}

// assertRunsAsWithoutPolicy drives one write's preview, --json preview and
// --confirm in dir and holds each to hactl without the feature: the preview
// verbatim, exactly today's requests on the wire, no details.policy, no
// audit log — and the confirmed call reaching Home Assistant.
func assertRunsAsWithoutPolicy(t *testing.T, inst *hatest.Instance, proxy *recordingProxy, dir, light string, args []string, wantPreview string) {
	t.Helper()
	// Today's requests (internal/haapi at 39e39f1): the preview looks the
	// service up; the confirmed run looks it up and posts the call.
	wantPreviewTrace := []string{"GET /api/services"}
	wantConfirmTrace := []string{"GET /api/services", "POST /api/services/light/toggle"}

	proxy.take()
	out, err := runHactlDirErr(t, dir, args...)
	if err != nil {
		t.Fatalf("preview failed: %v\n%s", err, out)
	}
	if out != wantPreview {
		t.Errorf("preview is not byte-identical to hactl without a policy:\n got  %q\n want %q", out, wantPreview)
	}
	if got := proxy.take(); !slices.Equal(got, wantPreviewTrace) {
		t.Errorf("preview sent %v, want exactly %v (no extra request, D-58)", got, wantPreviewTrace)
	}

	jout, jerr := runHactlDirErr(t, dir, append(slices.Clone(args), "--json")...)
	if jerr != nil {
		t.Fatalf("--json preview failed: %v\n%s", jerr, jout)
	}
	var doc struct {
		Details map[string]any `json:"details"`
	}
	if uerr := json.Unmarshal([]byte(jout), &doc); uerr != nil {
		t.Fatalf("--json preview: %v\n%s", uerr, jout)
	}
	if _, has := doc.Details["policy"]; has {
		t.Errorf("--json preview carries details.policy with no profile in effect: %s", jout)
	}

	before := pgStateOf(t, inst, light).State
	proxy.take()
	cout, cerr := runHactlDirErr(t, dir, append(slices.Clone(args), "--confirm")...)
	if cerr != nil {
		t.Fatalf("--confirm failed: %v\n%s", cerr, cout)
	}
	if got := proxy.take(); !slices.Equal(got, wantConfirmTrace) {
		t.Errorf("--confirm sent %v, want exactly %v (D-58)", got, wantConfirmTrace)
	}
	if strings.Contains(cout, "policy") {
		t.Errorf("--confirm output mentions the policy with none in effect:\n%s", cout)
	}
	if got := pgStateOf(t, inst, light).State; got == before {
		t.Errorf("--confirm did not switch %s (still %s)", light, got)
	}
	if _, serr := os.Stat(auditLogPath(dir)); !os.IsNotExist(serr) {
		t.Errorf("an audit log exists with no profile in effect (stat err %v)", serr)
	}
}

// TestPolicyBrokenFileRefusesCommands — D-60: a broken policy.yaml refuses a
// write and a read alike, with policy_broken naming file and line, nothing on
// stdout and nothing sent to Home Assistant; `policy show` and `version` still
// run.
func TestPolicyBrokenFileRefusesCommands(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	proxy := newRecordingProxy(t, inst.URL())
	broken := `version: 1
active: pg_agent
profiles:
  pg_agent:
    deny:
      "entity:lock.*": [wirte]
`
	const problemAt = "policy.yaml:6:"
	dir := pgInstanceDir(t, proxy.URL(), inst.Token(), broken)

	for _, args := range [][]string{
		pgSvc("light.toggle", pgEntityData(rig.Lights[0])),
		append(pgSvc("light.toggle", pgEntityData(rig.Lights[0])), "--confirm"),
		{"ent", "ls"},
	} {
		proxy.take()
		out, err := runHactlDirErr(t, dir, args...)
		if pgExitCode(err) != 1 {
			t.Errorf("hactl %v under a broken policy: exit %d, want 1\n%s", args, pgExitCode(err), out)
			continue
		}
		if msg := err.Error(); !strings.HasPrefix(msg, "policy_broken: ") || !strings.Contains(msg, problemAt) {
			t.Errorf("hactl %v: refusal %q, want `policy_broken: <file>:6: …`", args, msg)
		}
		if out != "" {
			t.Errorf("hactl %v: refused with output on stdout:\n%s", args, out)
		}
		if sent := proxy.take(); len(sent) != 0 {
			t.Errorf("hactl %v: refused under a broken policy but sent %v", args, sent)
		}
	}

	proxy.take()
	out, err := runHactlDirErr(t, dir, "policy", "show")
	if pgExitCode(err) != 1 || !strings.Contains(out, problemAt) {
		t.Errorf("policy show on a broken file: exit %d, want 1 with the problem listed:\n%s", pgExitCode(err), out)
	}
	vout, verr := runHactlDirErr(t, dir, "version")
	if verr != nil || strings.TrimSpace(vout) == "" {
		t.Errorf("version under a broken policy: err %v, output %q — it reads no instance and must still run", verr, vout)
	}
	if sent := proxy.take(); len(sent) != 0 {
		t.Errorf("policy show / version sent %v to Home Assistant", sent)
	}
}

// TestPolicyAuditLogRecordsWriteChecks — D-71: while a profile is in effect,
// every write check is one JSON line in <instance>/cache/policy-audit.log —
// a denied preview and an allowed confirmed run alike, the plan check and the
// request check — without --data contents; `cache clear` keeps it.
func TestPolicyAuditLogRecordsWriteChecks(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	denied := rig.Lights[1]
	const marker = "pg_audit_marker_5d1c"
	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "entity:%s": [control]
`, pgProfile, pgProfile, denied)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)

	if _, err := runHactlDirErr(t, dir, pgSvc("light.toggle", pgEntityData(denied))...); pgExitCode(err) != 1 {
		t.Fatalf("the denied preview was not refused: %v", err)
	}
	notify := pgSvc("persistent_notification.create", fmt.Sprintf(`{"message":%q,"notification_id":"pg_policy_audit"}`, marker))
	if out, err := runHactlDirErr(t, dir, notify...); err != nil {
		t.Fatalf("allowed preview failed: %v\n%s", err, out)
	}
	if out, err := runHactlDirErr(t, dir, append(slices.Clone(notify), "--confirm")...); err != nil {
		t.Fatalf("allowed --confirm failed: %v\n%s", err, out)
	}

	raw, err := os.ReadFile(auditLogPath(dir))
	if err != nil {
		t.Fatalf("no audit log after three write checks: %v", err)
	}
	if strings.Contains(string(raw), marker) {
		t.Errorf("the audit log holds --data contents (%q):\n%s", marker, raw)
	}
	lines := readAuditLines(t, raw)

	has := func(desc string, match func(pgAuditLine) bool) {
		t.Helper()
		if !slices.ContainsFunc(lines, match) {
			t.Errorf("audit log has no line for %s:\n%s", desc, raw)
		}
	}
	has("the denied preview's plan check", func(l pgAuditLine) bool {
		return l == pgAuditLine{
			Time: l.Time, Command: "hactl svc call light.toggle", DryRun: true, Layer: "command",
			Resource: "entity:" + denied, Verb: "control", Allowed: false,
			Rule: fmt.Sprintf(`deny "entity:%s" [control]`, denied), Line: policyLineOf(t, policy, `"entity:`+denied+`"`),
			Profile: pgProfile,
		}
	})
	has("the allowed preview's plan check", func(l pgAuditLine) bool {
		return l.Command == "hactl svc call persistent_notification.create" && l.DryRun && l.Layer == "command" &&
			l.Resource == "service:persistent_notification.create" && l.Verb == "control" && l.Allowed && l.Profile == pgProfile
	})
	has("the confirmed run's plan check", func(l pgAuditLine) bool {
		return l.Command == "hactl svc call persistent_notification.create" && !l.DryRun && l.Layer == "command" &&
			l.Resource == "service:persistent_notification.create" && l.Verb == "control" && l.Allowed && l.Profile == pgProfile
	})
	has("the confirmed run's request check", func(l pgAuditLine) bool {
		return l.Command == "hactl svc call persistent_notification.create" && !l.DryRun && l.Layer == "request" && l.Allowed
	})

	// cache clear is itself a checked write (delete system:cache) and never
	// removes the log.
	if out, cerr := runHactlDirErr(t, dir, "cache", "clear"); cerr != nil {
		t.Fatalf("cache clear under a profile that does not deny it failed: %v\n%s", cerr, out)
	}
	after, err := os.ReadFile(auditLogPath(dir))
	if err != nil {
		t.Fatalf("cache clear removed the audit log: %v", err)
	}
	if !bytes.HasPrefix(after, raw) {
		t.Errorf("cache clear rewrote the audit log instead of appending to it")
	}
	if !slices.ContainsFunc(readAuditLines(t, after), func(l pgAuditLine) bool {
		return l.Command == "hactl cache clear" && l.Resource == "system:cache" && l.Verb == "delete" && l.Allowed
	}) {
		t.Errorf("cache clear's check (delete system:cache) is not in the audit log:\n%s", after)
	}
}

// readAuditLines parses every line of the audit log, holding each to D-71's
// fields and write verbs.
func readAuditLines(t *testing.T, raw []byte) []pgAuditLine {
	t.Helper()
	fields := []string{"allowed", "command", "dry_run", "layer", "line", "profile", "resource", "rule", "time", "verb"}
	var out []pgAuditLine
	sc := bufio.NewScanner(strings.NewReader(string(raw)))
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		text := sc.Text()
		if text == "" {
			continue
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal([]byte(text), &keys); err != nil {
			t.Fatalf("audit line is not a JSON object: %v\n%s", err, text)
		}
		for _, f := range fields {
			if _, ok := keys[f]; !ok {
				t.Errorf("audit line lacks %q: %s", f, text)
			}
		}
		var l pgAuditLine
		if err := json.Unmarshal([]byte(text), &l); err != nil {
			t.Fatalf("audit line did not decode: %v\n%s", err, text)
		}
		if _, err := time.Parse(time.RFC3339, l.Time); err != nil {
			t.Errorf("audit line time %q is not RFC 3339: %v", l.Time, err)
		}
		if !slices.Contains([]string{"control", "edit", "create", "delete"}, l.Verb) {
			t.Errorf("audit line records verb %q — the log holds write checks only", l.Verb)
		}
		if l.Layer != "command" && l.Layer != "request" {
			t.Errorf("audit line layer %q, want command or request", l.Layer)
		}
		out = append(out, l)
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("reading audit log: %v", err)
	}
	return out
}
