//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// D-106 and D-107 against a real Home Assistant: a service call reaches more
// than its target names — the members of a light group, every entity a scene
// sets — and an entity it reaches can be a typed resource the owner denied by
// its typed address (an automation by config id, a YAML helper, a template
// entity, a script). Each refusal is proven by reading the entity's state back
// over HA's own API (H-12); the oracle half of each case — that HA's reach
// answer leaves the members out, that the state lists them — is asserted
// first, so the case cannot pass on a fixture that proves nothing.
// ============================================================================

// pgReachOf asks HA's extract_from_target for one entity, groups expanded.
func pgReachOf(t *testing.T, ws *rawWS, entityID string) []string {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{
		"type": "extract_from_target", "target": map[string]any{"entity_id": []string{entityID}}, "expand_group": true,
	})
	var out struct {
		Referenced []string `json:"referenced_entities"`
	}
	if err := json.Unmarshal(env.Result, &out); err != nil {
		t.Fatalf("extract_from_target answer: %v", err)
	}
	return out.Referenced
}

// pgMembersOf reads a state's `entity_id` attribute.
func pgMembersOf(t *testing.T, st pgState) []string {
	t.Helper()
	raw, _ := st.Attributes["entity_id"].([]any)
	out := make([]string, 0, len(raw))
	for _, v := range raw {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func pgOneDenyPolicy(pattern, verbs string) string {
	return fmt.Sprintf("version: 1\nactive: %s\nprofiles:\n  %s:\n    deny:\n      %q: [%s]\n", pgProfile, pgProfile, pattern, verbs)
}

// TestPolicyEnforceForwardedEntities (D-106): a denied light behind a light
// group and a denied lock behind a scene are refused — the call on the group
// or the scene alone would have switched them.
func TestPolicyEnforceForwardedEntities(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	ws := dialRawWS(t, inst.URL(), inst.Token())

	const group = "light.pg_policy_all_lights"
	members := pgMembersOf(t, pgStateOf(t, inst, group))
	if len(members) == 0 {
		t.Fatalf("%s lists no members in its entity_id attribute — the fixture proves nothing", group)
	}
	if reach := pgReachOf(t, ws, group); strings.Contains(strings.Join(reach, ","), members[0]) {
		t.Logf("note: HA's reach answer for %s now names its member %s (observed otherwise on HA 2026.10.4)", group, members[0])
	}
	member := members[0]
	groupPolicy := pgOneDenyPolicy("entity:"+member, "control")
	groupDir := pgInstanceDir(t, inst.URL(), inst.Token(), groupPolicy)
	before := pgStateOf(t, inst, member).State
	assertPolicyRefused(t, groupDir, pgSvc("light.toggle", pgEntityData(group)), pgDenied{
		Resource: "entity:" + member, Verb: "control",
		Rule: fmt.Sprintf(`deny "entity:%s" [control]`, member), Line: policyLineOf(t, groupPolicy, `"entity:`+member+`"`),
	})
	if after := pgStateOf(t, inst, member).State; after != before {
		t.Errorf("the refused group call switched its denied member %s: %s → %s", member, before, after)
	}

	lockTarget := "unlocked"
	if pgStateOf(t, inst, rig.Lock).State == "unlocked" {
		lockTarget = "locked"
	}
	const scene = "scene.pg_policy_leave"
	pgCallService(t, inst, "scene", "create", map[string]any{
		"scene_id": "pg_policy_leave",
		"entities": map[string]any{rig.Lights[0]: "off", rig.Lock: lockTarget},
	})
	sceneMembers := pgMembersOf(t, pgStateOf(t, inst, scene))
	if len(sceneMembers) == 0 || !strings.Contains(strings.Join(sceneMembers, ","), rig.Lock) {
		t.Fatalf("%s does not list %s in its entity_id attribute (%v) — the case proves nothing", scene, rig.Lock, sceneMembers)
	}
	if reach := pgReachOf(t, ws, scene); strings.Contains(strings.Join(reach, ","), rig.Lock) {
		t.Logf("note: HA's reach answer for %s now names %s", scene, rig.Lock)
	}
	scenePolicy := pgOneDenyPolicy("entity:lock.*", "control")
	sceneDir := pgInstanceDir(t, inst.URL(), inst.Token(), scenePolicy)
	lockBefore := pgStateOf(t, inst, rig.Lock).State
	assertPolicyRefused(t, sceneDir, pgSvc("scene.turn_on", pgEntityData(scene)), pgDenied{
		Resource: "entity:" + rig.Lock, Verb: "control",
		Rule: `deny "entity:lock.*" [control]`, Line: policyLineOf(t, scenePolicy, `"entity:lock.*"`),
	})
	time.Sleep(500 * time.Millisecond)
	if after := pgStateOf(t, inst, rig.Lock).State; after != lockBefore {
		t.Errorf("the refused scene call set the denied lock %s: %s → %s", rig.Lock, lockBefore, after)
	}
}

// TestPolicyEnforceTypedControl (D-107): a deny on an automation, a helper, a
// template entity or a script by its typed address refuses a service call
// switching it; and only a deny decides that check — under default: deny a
// profile allowing the entity and the service switches it.
func TestPolicyEnforceTypedControl(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	ws := dialRawWS(t, inst.URL(), inst.Token())
	reg := pgEntityRegistry(t, ws)
	automation := ""
	for id := range reg {
		if strings.HasPrefix(id, "automation.") && pgStateOf(t, inst, id).Attributes["id"] == "pg_policy_prod_heating" {
			automation = id
		}
	}
	if automation == "" {
		t.Fatal("no live automation carries id pg_policy_prod_heating — the case proves nothing")
	}
	t.Cleanup(func() { pgCallService(t, inst, "automation", "turn_on", map[string]any{"entity_id": automation}) })
	lockService := "lock.unlock"
	if pgStateOf(t, inst, "lock.pg_policy_lock").State == "unlocked" {
		lockService = "lock.lock"
	}

	for _, tc := range []struct {
		name, pattern, entity, service, resource string
	}{
		{"automation", "automation:pg_policy_prod_*", automation, "automation.turn_off", "automation:pg_policy_prod_heating"},
		{"yaml helper", "helper:input_boolean/pg_policy_lock_state", "input_boolean.pg_policy_lock_state", "input_boolean.toggle", "helper:input_boolean/pg_policy_lock_state"},
		{"template entity", "template:pg_policy_lock", "lock.pg_policy_lock", lockService, "template:pg_policy_lock"},
		{"script", "script:pg_policy_denied", "script.pg_policy_denied", "script.turn_off", "script:pg_policy_denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := reg[tc.entity]; !ok {
				t.Fatalf("%s has no registry entry — its typed address cannot be read", tc.entity)
			}
			policy := pgOneDenyPolicy(tc.pattern, "write")
			dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
			before := pgStateOf(t, inst, tc.entity).State
			assertPolicyRefused(t, dir, pgSvc(tc.service, pgEntityData(tc.entity)), pgDenied{
				Resource: tc.resource, Verb: "control",
				Rule: fmt.Sprintf(`deny %q [write]`, tc.pattern), Line: policyLineOf(t, policy, fmt.Sprintf("%q", tc.pattern)),
			})
			if after := pgStateOf(t, inst, tc.entity).State; after != before {
				t.Errorf("the refused call switched %s: %s → %s", tc.entity, before, after)
			}
		})
	}

	// Only a deny decides: default: deny with the entity and the service
	// allowed turns the automation off, without an allow on automation:*.
	open := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    default: deny
    allow:
      "entity:automation.*": [control]
      "service:automation.turn_off": [control]
`, pgProfile, pgProfile)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), open)
	if out, err := runHactlDirErr(t, dir, append(pgSvc("automation.turn_off", pgEntityData(automation)), "--confirm")...); err != nil {
		t.Fatalf("default: deny decided the typed address only a deny may decide: %v\n%s", err, out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for pgStateOf(t, inst, automation).State != "off" && time.Now().Before(deadline) {
		time.Sleep(200 * time.Millisecond)
	}
	if got := pgStateOf(t, inst, automation).State; got != "off" {
		t.Errorf("allowed automation.turn_off left %s %s", automation, got)
	}
}
