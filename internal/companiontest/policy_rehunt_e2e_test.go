//go:build companion

package companiontest

// The policy re-hunt of 2026-11-20 (D-188..D-196), companion half, kept as
// the live acceptance of its fixes: every verdict is read back from Home
// Assistant over raw REST (or from the docker container), never from hactl's
// own answer. A route the policy stops logs "HELD"; one that gets through
// fails the test with "BREACH" — the hunt's assertions, unchanged.

import (
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func rhcVerdict(t *testing.T, name string, breach bool, detail string) {
	t.Helper()
	if breach {
		t.Errorf("BREACH %s\n      %s", name, detail)
		return
	}
	t.Logf("HELD %s\n      %s", name, detail)
}

func rhcPlace(t *testing.T, path, body string) {
	t.Helper()
	c := exec.CommandContext(t.Context(), "docker", "exec", "-i", "hactl-companion-test-ha", "sh", "-c", //nolint:gosec // G204: the test's own fixture path into the test container
		"mkdir -p \"$(dirname "+path+")\" && cat > "+path)
	c.Stdin = strings.NewReader(body)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("placing %s: %v %s", path, err, out)
	}
}

// The writer profile, plus scene/group/logbook services, and the secret
// helper unreadable.
const rhcProfile = `    default: deny
    allow:
      "entity:*": [read]
      "automation:pg_*": [read, edit, create, delete]
      "entity:automation.pg_*": [control, create, delete]
      "script:pg_*": [read, edit, create, delete, control]
      "entity:script.pg_*": [control, create, delete]
      "template:pg_hunt_agent_*": [read, edit, create, delete]
      "entity:sensor.pg_hunt_agent_*": [read, create, delete]
      "entity:switch.pg_hunt_agent_*": [read, create, delete, control]
      "entity:input_boolean.pg_hunt_ok": [control]
      "entity:light.pg_hunt_hall*": [control]
      "entity:scene.pg_*": [control, create, delete]
      "entity:group.pg_*": [control, create, delete]
      "entity:*/registry/labels": [edit]
      "service:input_boolean.*": [control]
      "service:light.*": [control]
      "service:scene.*": [control]
      "service:group.*": [control]
      "service:logbook.log": [control]
      "service:homeassistant.turn_on": [control]
      "service:automation.*": [control]
      "service:script.*": [control]
      "file:**": [edit]
    deny:
      "entity:input_boolean.pg_hunt_guard": [control]
      "entity:light.pg_hunt_bedroom": [control]
      "entity:input_boolean.pg_hunt_secret": [read]
`

//  1. Automations the agent writes that switch the guard through a service
//     field that is not a target.
func TestE2EPolicyRehuntWrittenNonTargetFields(t *testing.T) {
	huntSetup(t)
	dir := policyE2EDir(t, pgE2EPolicy(rhcProfile))
	tmp := t.TempDir()
	o, e, c := runPolicyE2E(t, dir, nil, "policy", "check", "entity:"+huntSecret, "read")
	t.Logf("policy check secret read: %d %s", c, huntLine(o+e, 200))
	cases := []struct{ id, actions string }{
		{"pg_rh_a_scene_lit", "  - action: scene.apply\n    data:\n      entities:\n        input_boolean.pg_hunt_guard: \"on\"\n"},
		{"pg_rh_a_scene_tpl_lit", "  - action: scene.apply\n    data:\n      entities: \"{{ {'input_boolean.pg_hunt_guard': 'on'} }}\"\n"},
		{"pg_rh_a_scene_tpl_cat", "  - action: scene.apply\n    data:\n      entities: \"{{ {('input_boolean.pg_hunt_' ~ 'guard'): 'on'} }}\"\n"},
		{"pg_rh_a_scene_var", "  - variables:\n      e: input_boolean.pg_hunt_guard\n  - action: scene.apply\n    data:\n      entities: \"{{ {e: 'on'} }}\"\n"},
		{"pg_rh_a_group_tpl_cat", "  - action: group.set\n    data:\n      object_id: pg_rh_tg\n      entities: \"{{ ['input_boolean.pg_hunt_' ~ 'guard'] }}\"\n  - delay: 1\n  - action: homeassistant.turn_on\n    target:\n      entity_id: group.pg_rh_tg\n"},
		{"pg_rh_a_scene_create_cat", "  - action: scene.create\n    data:\n      scene_id: pg_rh_ts\n      entities: \"{{ {('input_boolean.pg_hunt_' ~ 'guard'): 'on'} }}\"\n  - delay: 1\n  - action: scene.turn_on\n    target:\n      entity_id: scene.pg_rh_ts\n"},
		{"pg_rh_a_all", "  - action: input_boolean.turn_on\n    target:\n      entity_id: all\n"},
		{"pg_rh_a_all_upper", "  - action: input_boolean.turn_on\n    data:\n      entity_id: ALL\n"},
	}
	for i, k := range cases {
		huntResetGuard(t)
		huntSvc(t, "input_boolean", "turn_off", map[string]any{"entity_id": huntOK})
		ev := fmt.Sprintf("pg_rh_fire_%d", i)
		f := huntWrite(t, tmp, k.id+".yaml", "id: "+k.id+"\nalias: "+k.id+"\ntriggers:\n  - trigger: event\n    event_type: "+ev+"\nactions:\n"+k.actions)
		pe, ce, rep := huntAgent(t, dir, "auto", "create", "-f", f)
		if ce == 0 {
			waitForHAState(t, "automation."+k.id)
			huntHTTP(t, http.MethodPost, "/api/events/"+ev, map[string]any{})
			time.Sleep(3 * time.Second)
		}
		g := huntState(t, huntGuard)
		rhcVerdict(t, fmt.Sprintf("agent automation %s → guard=%s (preview=%d confirm=%d)", k.id, g, pe, ce), g == "on", rep)
	}
	huntResetGuard(t)
}

//  2. Reads the agent's own template entity, automation trace or logbook row
//     make of an entity the profile may not read.
func TestE2EPolicyRehuntReadsThroughWrites(t *testing.T) {
	huntSetup(t)
	dir := policyE2EDir(t, pgE2EPolicy(rhcProfile))
	tmp := t.TempDir()
	o, e, c := runPolicyE2E(t, dir, nil, "ent", "show", huntSecret)
	t.Logf("baseline agent ent show secret: exit=%d %s", c, huntLine(o+e, 200))
	o, e, c = runPolicyE2E(t, dir, nil, "tpl", "eval", "{{ state_attr('"+huntSecret+"','friendly_name') }}")
	t.Logf("baseline agent tpl eval secret: exit=%d %s", c, huntLine(o+e, 200))

	sensors := []struct{ id, state string }{
		{"pg_hunt_agent_r_lit", "{{ state_attr('input_boolean.pg_hunt_secret','friendly_name') }}"},
		{"pg_hunt_agent_r_cat", "{{ state_attr('input_boolean.pg_hunt_' ~ 'secret','friendly_name') }}"},
		{"pg_hunt_agent_r_sel", "{{ states.input_boolean | selectattr('object_id','search','secr') | map(attribute='name') | join(',') }}"},
		{"pg_hunt_agent_r_all", "{{ states.input_boolean | map(attribute='name') | join(',') }}"},
	}
	for _, s := range sensors {
		f := huntWrite(t, tmp, s.id+".yaml", "sensor:\n  - name: "+s.id+"\n    unique_id: "+s.id+"\n    state: \""+s.state+"\"\n")
		pe, ce, rep := huntAgent(t, dir, "tpl", "create", "-f", f)
		ownerVal := "<not created>"
		agentOut := ""
		if ce == 0 {
			waitForHAState(t, "sensor."+s.id)
			time.Sleep(2 * time.Second)
			ownerVal = huntState(t, "sensor."+s.id)
			ao, ae, _ := runPolicyE2E(t, dir, nil, "ent", "show", "sensor."+s.id)
			agentOut = ao + ae
		}
		rhcVerdict(t, fmt.Sprintf("agent template sensor %s: HA state=%q; agent ent show prints secret name=%v (preview=%d confirm=%d)",
			s.id, ownerVal, strings.Contains(agentOut, "Xylophone"), pe, ce),
			strings.Contains(agentOut, "Xylophone"), rep+"\n      agent view: "+huntLine(agentOut, 300))
	}

	// automation variables → trace show of the agent's own automation
	f := huntWrite(t, tmp, "trace.yaml", "id: pg_rh_a_trace\nalias: pg_rh_a_trace\ntriggers:\n  - trigger: event\n    event_type: pg_rh_trace_fire\nactions:\n  - variables:\n      s: \"{{ state_attr('input_boolean.pg_hunt_' ~ 'secret','friendly_name') }}\"\n  - action: input_boolean.turn_on\n    target:\n      entity_id: input_boolean.pg_hunt_ok\n")
	pe, ce, rep := huntAgent(t, dir, "auto", "create", "-f", f)
	if ce == 0 {
		waitForHAState(t, "automation.pg_rh_a_trace")
		huntHTTP(t, http.MethodPost, "/api/events/pg_rh_trace_fire", map[string]any{})
		time.Sleep(3 * time.Second)
		for _, args := range [][]string{{"trace", "show", "pg_rh_a_trace"}, {"trace", "show", "pg_rh_a_trace", "--full"}, {"trace", "show", "pg_rh_a_trace", "--json"}} {
			ao, ae, ac := runPolicyE2E(t, dir, nil, args...)
			rhcVerdict(t, fmt.Sprintf("agent automation variable s=<secret name> → %s exit=%d prints secret=%v", strings.Join(args, " "), ac, strings.Contains(ao+ae, "Xylophone")),
				strings.Contains(ao+ae, "Xylophone"), huntLine(ao+ae, 300))
		}
	} else {
		t.Logf("HELD trace route: auto create refused (preview=%d confirm=%d)\n      %s", pe, ce, rep)
	}

	// automation logbook.log on an allowed entity → changes
	f = huntWrite(t, tmp, "lb.yaml", "id: pg_rh_a_lb\nalias: pg_rh_a_lb\ntriggers:\n  - trigger: event\n    event_type: pg_rh_lb_fire\nactions:\n  - action: logbook.log\n    data:\n      name: RH\n      entity_id: input_boolean.pg_hunt_ok\n      message: \"RHC-{{ state_attr('input_boolean.pg_hunt_' ~ 'secret','friendly_name') }}\"\n")
	pe, ce, rep = huntAgent(t, dir, "auto", "create", "-f", f)
	if ce == 0 {
		waitForHAState(t, "automation.pg_rh_a_lb")
		huntHTTP(t, http.MethodPost, "/api/events/pg_rh_lb_fire", map[string]any{})
		time.Sleep(3 * time.Second)
		ao, ae, ac := runPolicyE2E(t, dir, nil, "changes", "--since", "15m")
		rhcVerdict(t, fmt.Sprintf("agent automation logbook.log <secret name> on pg_hunt_ok → changes exit=%d prints secret=%v", ac, strings.Contains(ao+ae, "Xylophone")),
			strings.Contains(ao+ae, "Xylophone"), huntLine(ao+ae, 300))
	} else {
		t.Logf("HELD logbook route: auto create refused (preview=%d confirm=%d)\n      %s", pe, ce, rep)
	}
}

//  3. Blueprints for scripts and template entities (the F4 tests use
//     automation blueprints).
func TestE2EPolicyRehuntBlueprintKinds(t *testing.T) {
	huntSetup(t)
	dir := policyE2EDir(t, pgE2EPolicy(rhcProfile))
	tmp := t.TempDir()
	rhcPlace(t, "/config/blueprints/script/pg/pg_rh_s.yaml", `blueprint:
  name: PG RH Script
  domain: script
  input:
    tgt:
      name: T
      default: input_boolean.pg_hunt_guard
sequence:
  - action: input_boolean.turn_on
    target:
      entity_id: !input tgt
mode: single
`)
	rhcPlace(t, "/config/blueprints/template/pg/pg_rh_t_read.yaml", `blueprint:
  name: PG RH Tpl Read
  domain: template
  input:
    src:
      name: S
      default: input_boolean.pg_hunt_secret
variables:
  src: !input src
sensor:
  state: "{{ state_attr(src, 'friendly_name') }}"
`)
	rhcPlace(t, "/config/blueprints/template/pg/pg_rh_t_sw.yaml", `blueprint:
  name: PG RH Tpl Switch
  domain: template
  input:
    tgt:
      name: T
      default: input_boolean.pg_hunt_guard
switch:
  state: "{{ false }}"
  turn_on:
    - action: input_boolean.turn_on
      target:
        entity_id: !input tgt
  turn_off:
    - action: input_boolean.turn_off
      target:
        entity_id: input_boolean.pg_hunt_ok
`)
	// reload blueprints is not needed: HA reads them on use.
	huntResetGuard(t)
	f := huntWrite(t, tmp, "bps.yaml", "pg_rh_bps:\n  use_blueprint:\n    path: pg/pg_rh_s.yaml\n")
	pe, ce, rep := huntAgent(t, dir, "script", "create", "-f", f)
	if ce == 0 {
		waitForHAState(t, "script.pg_rh_bps")
		_, _, r := huntAgent(t, dir, "script", "run", "pg_rh_bps")
		rep += "\n      run: " + r
		time.Sleep(2 * time.Second)
	}
	g := huntState(t, huntGuard)
	rhcVerdict(t, fmt.Sprintf("agent script from blueprint with default target guard → guard=%s (preview=%d confirm=%d)", g, pe, ce), g == "on", rep)

	f = huntWrite(t, tmp, "bpt1.yaml", "use_blueprint:\n  path: pg/pg_rh_t_read.yaml\nname: pg_hunt_agent_bpt_read\nunique_id: pg_hunt_agent_bpt_read\n")
	pe, ce, rep = huntAgent(t, dir, "tpl", "create", "-f", f)
	val := "<not created>"
	agentOut := ""
	if ce == 0 {
		time.Sleep(4 * time.Second)
		val = huntState(t, "sensor.pg_hunt_agent_bpt_read")
		ao, ae, _ := runPolicyE2E(t, dir, nil, "ent", "show", "sensor.pg_hunt_agent_bpt_read")
		agentOut = ao + ae
	}
	rhcVerdict(t, fmt.Sprintf("agent template sensor from blueprint reading secret by default input: HA state=%q, agent sees secret=%v (preview=%d confirm=%d)", val, strings.Contains(agentOut, "Xylophone"), pe, ce),
		strings.Contains(agentOut, "Xylophone"), rep+"\n      agent view: "+huntLine(agentOut, 300))

	huntResetGuard(t)
	f = huntWrite(t, tmp, "bpt2.yaml", "use_blueprint:\n  path: pg/pg_rh_t_sw.yaml\nname: pg_hunt_agent_bpt_sw\nunique_id: pg_hunt_agent_bpt_sw\n")
	pe, ce, rep = huntAgent(t, dir, "tpl", "create", "-f", f)
	if ce == 0 {
		time.Sleep(4 * time.Second)
		t.Logf("switch.pg_hunt_agent_bpt_sw state: %s", huntState(t, "switch.pg_hunt_agent_bpt_sw"))
		huntSvc(t, "switch", "turn_on", map[string]any{"entity_id": "switch.pg_hunt_agent_bpt_sw"})
		time.Sleep(2 * time.Second)
	}
	g = huntState(t, huntGuard)
	rhcVerdict(t, fmt.Sprintf("agent template switch from blueprint with default turn_on target guard → guard=%s (preview=%d confirm=%d)", g, pe, ce), g == "on", rep)
	huntResetGuard(t)
}

//  4. Owner config that targets a label in a way HA's search may not report;
//     the agent then labels the guard (D-186).
func TestE2EPolicyRehuntLabelTargetForms(t *testing.T) {
	admin := huntSetup(t)
	dir := policyE2EDir(t, pgE2EPolicy(rhcProfile))
	tmp := t.TempDir()
	own := func(args ...string) {
		o, e, c := runPolicyE2E(t, admin, nil, append(args, "--confirm")...)
		t.Logf("owner %v exit=%d %s", args, c, huntLine(o+e, 200))
	}
	for _, l := range []string{"pg_rh_lab_tpl", "pg_rh_lab_var", "pg_rh_lab_data", "pg_rh_lab_flow"} {
		own("label", "create", l)
	}
	own("tpl", "create", "-f", huntWrite(t, tmp, "own_sw.yaml", `switch:
  - name: PG RH Owner Lab Switch
    unique_id: pg_rh_owner_lab_switch
    turn_on:
      - action: input_boolean.turn_on
        target:
          label_id:
            - pg_rh_lab_tpl
    turn_off:
      - action: input_boolean.turn_off
        target:
          entity_id: input_boolean.pg_hunt_ok
`))
	own("auto", "create", "-f", huntWrite(t, tmp, "own_var.yaml", `id: pg_rh_owner_var
alias: pg_rh_owner_var
triggers:
  - trigger: event
    event_type: pg_rh_owner_var_fire
variables:
  lab: pg_rh_lab_var
actions:
  - action: input_boolean.turn_on
    target:
      label_id: "{{ lab }}"
`))
	own("auto", "create", "-f", huntWrite(t, tmp, "own_data.yaml", `id: pg_rh_owner_data
alias: pg_rh_owner_data
triggers:
  - trigger: event
    event_type: pg_rh_owner_data_fire
actions:
  - action: input_boolean.turn_on
    data:
      label_id: pg_rh_lab_data
`))
	own("auto", "create", "-f", huntWrite(t, tmp, "own_flow.yaml", `id: pg_rh_owner_flow
alias: pg_rh_owner_flow
triggers:
  - trigger: event
    event_type: pg_rh_owner_flow_fire
actions:
  - action: input_boolean.turn_on
    target: {label_id: [pg_rh_lab_flow]}
`))
	waitForHAState(t, "switch.pg_rh_owner_lab_switch")
	waitForHAState(t, "automation.pg_rh_owner_var")
	time.Sleep(2 * time.Second)

	cases := []struct {
		label string
		fire  func()
	}{
		{"pg_rh_lab_tpl", func() {
			huntSvc(t, "switch", "turn_on", map[string]any{"entity_id": "switch.pg_rh_owner_lab_switch"})
		}},
		{"pg_rh_lab_var", func() { huntHTTP(t, http.MethodPost, "/api/events/pg_rh_owner_var_fire", map[string]any{}) }},
		{"pg_rh_lab_data", func() { huntHTTP(t, http.MethodPost, "/api/events/pg_rh_owner_data_fire", map[string]any{}) }},
		{"pg_rh_lab_flow", func() { huntHTTP(t, http.MethodPost, "/api/events/pg_rh_owner_flow_fire", map[string]any{}) }},
	}
	for _, k := range cases {
		huntResetGuard(t)
		pe, ce, rep := huntAgent(t, dir, "ent", "set-label", huntGuard, k.label)
		k.fire()
		time.Sleep(2500 * time.Millisecond)
		g := huntState(t, huntGuard)
		rhcVerdict(t, fmt.Sprintf("agent ent set-label guard %s, then the owner's config targeting it runs → guard=%s (preview=%d confirm=%d)", k.label, g, pe, ce),
			g == "on" && ce == 0, rep)
		if ce == 0 {
			own("ent", "set-label", huntGuard, "--remove", k.label)
		}
	}
	huntResetGuard(t)
}

//  5. Opaque services inside a script the agent writes, under a default-allow
//     profile that denies only the guard.
func TestE2EPolicyRehuntOpaqueInScript(t *testing.T) {
	huntSetup(t)
	dir := policyE2EDir(t, pgE2EPolicy(`    deny:
      "entity:input_boolean.pg_hunt_guard": [control]
`))
	tmp := t.TempDir()
	// oracle: does the default agent switch the guard by name?
	huntResetGuard(t)
	code, b := huntHTTP(t, http.MethodPost, "/api/services/conversation/process?return_response", map[string]any{"text": "turn on PG Hunt Guard", "language": "en"})
	time.Sleep(1500 * time.Millisecond)
	t.Logf("ORACLE owner conversation.process: http %d %s → guard=%s", code, huntLine(string(b), 200), huntState(t, huntGuard))
	huntResetGuard(t)
	o, e, c := runPolicyE2E(t, dir, nil, "svc", "call", "conversation.process", "--data", `{"text":"turn on PG Hunt Guard","language":"en"}`)
	t.Logf("agent svc call conversation.process (direct, baseline): exit=%d %s", c, huntLine(o+e, 200))
	for _, k := range []struct{ id, seq string }{
		{"pg_rh_s_conv", "    - action: conversation.process\n      data:\n        text: turn on PG Hunt Guard\n        language: en\n"},
		{"pg_rh_s_conv_upper", "    - action: CONVERSATION.PROCESS\n      data:\n        text: turn on PG Hunt Guard\n        language: en\n"},
		{"pg_rh_s_conv_par", "    - parallel:\n        - sequence:\n            - service: conversation.process\n              data_template:\n                text: \"turn on PG Hunt {{ 'Guard' }}\"\n"},
	} {
		huntResetGuard(t)
		f := huntWrite(t, tmp, k.id+".yaml", k.id+":\n  sequence:\n"+k.seq)
		pe, ce, rep := huntAgent(t, dir, "script", "create", "-f", f)
		if ce == 0 {
			waitForHAState(t, "script."+k.id)
			_, _, r := huntAgent(t, dir, "script", "run", k.id)
			rep += "\n      run: " + r
			time.Sleep(2500 * time.Millisecond)
		}
		g := huntState(t, huntGuard)
		rhcVerdict(t, fmt.Sprintf("agent script %s → guard=%s (preview=%d confirm=%d)", k.id, g, pe, ce), g == "on", rep)
	}
	huntResetGuard(t)
}
