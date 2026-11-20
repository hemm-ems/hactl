//go:build companion

package companiontest

// Policy hunt 3 (2026-11-20), live: every verdict is read back from Home
// Assistant (REST) or from what the agent's own hactl prints; HELD = the
// route is stopped, BREACH = it gets through and fails the test. The fixes
// are D-198..D-206: a template reads what it names (D-198), a reload needs
// no delete (D-199), a config entry reload needs control on the entry's
// entities (D-200), trigger.to_state reads its entity (D-201), a condition
// reads what it compares (D-202), call_service events are unknowable
// (D-203), reserved names are not rebound (D-204), string escapes decode as
// jinja2's (D-205). The over-refusal survey is a test too: each ordinary
// task passes, each negative twin is refused.

import (
	"context"
	"fmt"
	"net/http"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const h3SecretPin = "H3PIN4711"
const h3SecretValue = "H3SECRETVALUE9"

// P_full: secret helper and pin unreadable; any input_boolean switchable but
// the guard.
const h3Full = `    default: deny
    allow:
      "entity:*": [read]
      "automation:pg_*": [read, edit, create, delete]
      "entity:automation.pg_*": [control, create, delete]
      "template:pg_hunt_agent_*": [read, edit, create, delete]
      "entity:sensor.pg_hunt_agent_*": [read, create, delete]
      "entity:input_boolean.*": [control]
      "entity:light.pg_hunt_hall*": [control]
      "entity:scene.pg_*": [control, create, delete]
      "entity:group.pg_*": [control, create, delete]
      "service:input_boolean.*": [control]
      "service:light.*": [control]
      "service:scene.*": [control]
      "service:group.*": [control]
      "service:logbook.log": [control]
      "service:homeassistant.turn_on": [control]
      "service:automation.*": [control]
      "file:**": [edit]
    deny:
      "entity:input_boolean.pg_hunt_guard": [control]
      "entity:light.pg_hunt_bedroom": [control]
      "entity:input_boolean.pg_hunt_secret": [read]
      "entity:input_text.pg_h3_pin": [read]
`

// P_attr: as P_full, but only the secret's friendly_name is unreadable (the
// quick start's latitude/longitude shape).
var h3Attr = strings.Replace(h3Full, `      "entity:input_boolean.pg_hunt_secret": [read]`,
	`      "entity:input_boolean.pg_hunt_secret/attr/friendly_name": [read]`, 1)

var h3Once bool

func h3Setup(t *testing.T) {
	t.Helper()
	huntSetup(t)
	if h3Once {
		return
	}
	h3Once = true
	// an owner file of actions, and a secret
	rhcPlace(t, "/config/pg_h3_inc.yaml", "- action: input_boolean.turn_on\n  target:\n    entity_id: input_boolean.pg_hunt_guard\n")
	cmd := exec.CommandContext(t.Context(), "docker", "exec", "hactl-companion-test-ha", "sh", "-c",
		"touch /config/secrets.yaml && grep -q h3_pw /config/secrets.yaml || printf '\\nh3_pw: "+h3SecretValue+"\\n' >> /config/secrets.yaml; cat /config/secrets.yaml")
	out, err := cmd.CombinedOutput()
	t.Logf("secrets.yaml: %v %s", err, huntLine(string(out), 200))
}

// h3DropAutomation removes an automation from automations.yaml by text and
// reloads: Home Assistant's config editor answers 500 for every automation
// while the file holds a `!secret` tag, so the editor cannot delete it.
func h3DropAutomation(t *testing.T, id string) {
	t.Helper()
	script := "awk -v id='- id: " + id + "' '$0==id{skip=1;next} skip&&/^- /{skip=0} !skip' /config/automations.yaml > /tmp/a.yaml && cat /tmp/a.yaml > /config/automations.yaml"
	out, err := exec.CommandContext(context.Background(), "docker", "exec", "hactl-companion-test-ha", "sh", "-c", script).CombinedOutput() //nolint:gosec // G204: fixed container; id is a test constant
	if err != nil {
		t.Errorf("drop %s from automations.yaml: %v %s", id, err, out)
		return
	}
	huntSvc(t, "automation", "reload", map[string]any{})
	if code, _ := huntHTTP(t, http.MethodGet, "/api/config/automation/config/pg_h3_none", nil); code == http.StatusInternalServerError {
		t.Errorf("after dropping %s the automation editor still answers 500", id)
	}
}

func h3Changes(t *testing.T, dir string) string {
	t.Helper()
	ao, ae, ac := runPolicyE2E(t, dir, nil, "changes", "--since", "15m")
	return fmt.Sprintf("exit=%d %s", ac, ao+ae)
}

func h3Toggle(t *testing.T, id string) {
	t.Helper()
	huntSvc(t, "input_boolean", "toggle", map[string]any{"entity_id": id})
	time.Sleep(3 * time.Second)
}

func h3Auto(t *testing.T, dir, tmp, id, body string) (int, int, string) {
	t.Helper()
	f := huntWrite(t, tmp, id+".yaml", "id: "+id+"\nalias: "+id+"\nmode: queued\n"+body)
	pe, ce, rep := huntAgent(t, dir, "auto", "create", "-f", f)
	if ce == 0 {
		waitForHAState(t, "automation."+id)
	}
	return pe, ce, rep
}

// 1. Template routes that read a read-denied entity through "safe" names:
// rebinding trigger (D-204), a state_changed or call_service event (D-203),
// a wait trigger, a template sensor reading back what it saw.
func TestE2EPolicyHunt3TemplateReads(t *testing.T) {
	h3Setup(t)
	dir := policyE2EDir(t, pgE2EPolicy(h3Full))
	tmp := t.TempDir()
	o, e, c := runPolicyE2E(t, dir, nil, "ent", "show", huntSecret)
	t.Logf("baseline agent ent show secret: exit=%d %s", c, huntLine(o+e, 200))

	type tc struct{ id, body, marker string }
	match := `'pg_hunt_secret' in (trigger.event.data.entity_id | string)`
	cases := []tc{
		{"pg_h3_base", "triggers:\n  - trigger: event\n    event_type: state_changed\nconditions:\n  - condition: template\n    value_template: \"{{ " + match + " }}\"\nactions:\n  - action: logbook.log\n    data:\n      name: H3\n      entity_id: input_boolean.pg_hunt_ok\n      message: \"H3BASE-{{ trigger.event.data.new_state.name }}\"\n", "H3BASE-"},
		{"pg_h3_vars", "triggers:\n  - trigger: event\n    event_type: state_changed\nconditions:\n  - condition: template\n    value_template: \"{{ " + match + " }}\"\nactions:\n  - action: logbook.log\n    data:\n      name: H3\n      entity_id: input_boolean.pg_hunt_ok\n      message: \"H3VARS-{{ trigger.event.data.new_state.name }}\"\n  - variables:\n      trigger: 0\n", "H3VARS-"},
		{"pg_h3_resp", "triggers:\n  - trigger: event\n    event_type: state_changed\nconditions:\n  - condition: template\n    value_template: \"{{ " + match + " }}\"\nactions:\n  - action: logbook.log\n    data:\n      name: H3\n      entity_id: input_boolean.pg_hunt_ok\n      message: \"H3RESP-{{ trigger.event.data.new_state.name }}\"\n  - if:\n      - condition: template\n        value_template: \"{{ false }}\"\n    then:\n      - action: input_boolean.turn_on\n        target:\n          entity_id: input_boolean.pg_hunt_ok\n        response_variable: trigger\n", "H3RESP-"},
		{"pg_h3_bound", "triggers:\n  - trigger: event\n    event_type: state_changed\nconditions:\n  - condition: template\n    value_template: \"{% for trigger in [] %}{% endfor %}{{ " + match + " }}\"\nactions:\n  - action: logbook.log\n    data:\n      name: H3\n      entity_id: input_boolean.pg_hunt_ok\n      message: \"{% for trigger in [] %}{% endfor %}H3BOUND-{{ trigger.event.data.new_state.name }}\"\n", "H3BOUND-"},
		{"pg_h3_wait", "triggers:\n  - trigger: event\n    event_type: pg_h3_wait_fire\nactions:\n  - repeat:\n      sequence:\n        - wait_for_trigger:\n            - trigger: event\n              event_type: state_changed\n          timeout: 30\n      until: \"{{ wait.trigger is none or 'pg_hunt_secret' in (wait.trigger.event.data.entity_id | string) }}\"\n  - action: logbook.log\n    data:\n      name: H3\n      entity_id: input_boolean.pg_hunt_ok\n      message: \"H3WAIT-{{ wait.trigger.event.data.new_state.name if wait.trigger else 'timeout' }}\"\n", "H3WAIT-"},
		{"pg_h3_callsvc", "triggers:\n  - trigger: event\n    event_type: call_service\n    event_data:\n      domain: input_boolean\nactions:\n  - action: logbook.log\n    data:\n      name: H3\n      entity_id: input_boolean.pg_hunt_ok\n      message: \"H3CALL-{{ trigger.event.data.service_data }}\"\n", "H3CALL-"},
	}
	created := map[string]bool{}
	for _, k := range cases {
		pe, ce, rep := h3Auto(t, dir, tmp, k.id, k.body)
		created[k.id] = ce == 0
		t.Logf("write %s: preview=%d confirm=%d\n      %s", k.id, pe, ce, rep)
	}
	// fire
	huntHTTP(t, http.MethodPost, "/api/events/pg_h3_wait_fire", map[string]any{})
	time.Sleep(2 * time.Second)
	h3Toggle(t, huntSecret)
	huntSvc(t, "input_boolean", "toggle", map[string]any{"entity_id": huntOK})
	time.Sleep(4 * time.Second)
	ch := h3Changes(t, dir)
	for _, k := range cases {
		lines := []string{}
		for l := range strings.SplitSeq(ch, "\n") {
			if strings.Contains(l, k.marker) {
				lines = append(lines, strings.TrimSpace(l))
			}
		}
		leak := strings.Contains(strings.Join(lines, " "), "Xylophone") || strings.Contains(strings.Join(lines, " "), h3SecretPin)
		rhcVerdict(t, fmt.Sprintf("%s (written=%v) → agent `changes` prints secret=%v", k.id, created[k.id], leak), leak, huntLine(strings.Join(lines, " || "), 500))
	}

	// trigger-based template sensor, bound-name override; agent reads it back
	sensor := "triggers:\n  - trigger: event\n    event_type: state_changed\nsensor:\n  - name: pg_hunt_agent_h3b\n    unique_id: pg_hunt_agent_h3b\n    state: \"{% for trigger in [] %}{% endfor %}{{ trigger.event.data.new_state.name if 'pg_hunt_secret' in (trigger.event.data.entity_id | string) else this.state }}\"\n"
	f := huntWrite(t, tmp, "h3b.yaml", sensor)
	pe, ce, rep := huntAgent(t, dir, "tpl", "create", "-f", f)
	out := ""
	if ce == 0 {
		waitForHAState(t, "sensor.pg_hunt_agent_h3b")
		h3Toggle(t, huntSecret)
		ao, ae, _ := runPolicyE2E(t, dir, nil, "ent", "show", "sensor.pg_hunt_agent_h3b")
		out = ao + ae
	}
	rhcVerdict(t, fmt.Sprintf("template sensor pg_hunt_agent_h3b (bound trigger) HA state=%q → agent ent show prints secret=%v (preview=%d confirm=%d)", huntState(t, "sensor.pg_hunt_agent_h3b"), strings.Contains(out, "Xylophone"), pe, ce),
		strings.Contains(out, "Xylophone"), rep+"\n      agent: "+huntLine(out, 300))
}

// 2. A partly denied entity: the trigger hands the automation the whole
// state (D-201); a template-free condition compares a denied attribute
// (D-202).
func TestE2EPolicyHunt3PartialAttr(t *testing.T) {
	h3Setup(t)
	dir := policyE2EDir(t, pgE2EPolicy(h3Attr))
	tmp := t.TempDir()
	o, e, c := runPolicyE2E(t, dir, nil, "ent", "show", huntSecret)
	t.Logf("baseline agent ent show secret (friendly_name denied): exit=%d prints name=%v %s", c, strings.Contains(o+e, "Xylophone"), huntLine(o+e, 300))
	o, e, c = runPolicyE2E(t, dir, nil, "ent", "show", huntSecret, "--json")
	t.Logf("baseline agent ent show secret --json: exit=%d prints name=%v", c, strings.Contains(o+e, "Xylophone"))

	pe, ce, rep := h3Auto(t, dir, tmp, "pg_h3_attr", "triggers:\n  - trigger: state\n    entity_id: input_boolean.pg_hunt_secret\nactions:\n  - action: logbook.log\n    data:\n      name: H3\n      entity_id: input_boolean.pg_hunt_ok\n      message: \"H3ATTR-{{ trigger.to_state.attributes.friendly_name }}\"\n")
	t.Logf("write pg_h3_attr: preview=%d confirm=%d\n      %s", pe, ce, rep)
	// template-free 1-bit oracle: condition on the denied attribute
	huntSvc(t, "input_boolean", "turn_off", map[string]any{"entity_id": huntOK})
	pe2, ce2, rep2 := h3Auto(t, dir, tmp, "pg_h3_attrcond", "triggers:\n  - trigger: event\n    event_type: pg_h3_attrcond_fire\nconditions:\n  - condition: state\n    entity_id: input_boolean.pg_hunt_secret\n    attribute: friendly_name\n    state: \"PG Hunt Secret Xylophone\"\nactions:\n  - action: input_boolean.turn_on\n    target:\n      entity_id: input_boolean.pg_hunt_ok\n")
	t.Logf("write pg_h3_attrcond: preview=%d confirm=%d\n      %s", pe2, ce2, rep2)
	// trigger-based template sensor
	f := huntWrite(t, tmp, "h3a.yaml", "triggers:\n  - trigger: state\n    entity_id: input_boolean.pg_hunt_secret\nsensor:\n  - name: pg_hunt_agent_h3a\n    unique_id: pg_hunt_agent_h3a\n    state: \"{{ trigger.to_state.name }}\"\n")
	pe3, ce3, rep3 := huntAgent(t, dir, "tpl", "create", "-f", f)
	if ce3 == 0 {
		waitForHAState(t, "sensor.pg_hunt_agent_h3a")
	}
	h3Toggle(t, huntSecret)
	huntHTTP(t, http.MethodPost, "/api/events/pg_h3_attrcond_fire", map[string]any{})
	time.Sleep(3 * time.Second)

	ch := h3Changes(t, dir)
	lines := []string{}
	for l := range strings.SplitSeq(ch, "\n") {
		if strings.Contains(l, "H3ATTR-") {
			lines = append(lines, strings.TrimSpace(l))
		}
	}
	leak := strings.Contains(strings.Join(lines, " "), "Xylophone")
	rhcVerdict(t, fmt.Sprintf("pg_h3_attr (state trigger, friendly_name denied) → agent changes prints it=%v", leak), leak, huntLine(strings.Join(lines, " || "), 400))
	ok := huntState(t, huntOK)
	rhcVerdict(t, fmt.Sprintf("pg_h3_attrcond (condition on denied attribute == secret) → pg_hunt_ok=%s (on = the agent learns the denied value)", ok), ok == "on" && ce2 == 0, rep2)
	ao, ae, _ := runPolicyE2E(t, dir, nil, "ent", "show", "sensor.pg_hunt_agent_h3a")
	rhcVerdict(t, fmt.Sprintf("template sensor pg_hunt_agent_h3a {{ trigger.to_state.name }} HA=%q → agent ent show prints secret=%v (preview=%d confirm=%d)", huntState(t, "sensor.pg_hunt_agent_h3a"), strings.Contains(ao+ae, "Xylophone"), pe3, ce3),
		strings.Contains(ao+ae, "Xylophone"), rep3+"\n      agent: "+huntLine(ao+ae, 300))
}

// 3. Control routes: Jinja string escapes hactl folded differently (D-205),
// and YAML tags hactl reads as plain strings.
func TestE2EPolicyHunt3ControlRoutes(t *testing.T) {
	h3Setup(t)
	dir := policyE2EDir(t, pgE2EPolicy(h3Full))
	strict := policyE2EDir(t, pgE2EPolicy(rhcProfile))
	tmp := t.TempDir()
	o, e, c := runPolicyE2E(t, dir, nil, "policy", "check", "entity:"+huntGuard, "control")
	t.Logf("policy check guard control: %d %s", c, huntLine(o+e, 200))
	o, e, c = runPolicyE2E(t, dir, nil, "config", "file", "secrets.yaml")
	t.Logf("baseline agent config file secrets.yaml: exit=%d prints secret=%v %s", c, strings.Contains(o+e, h3SecretValue), huntLine(o+e, 200))

	cases := []struct {
		id, dir, actions string
		check            string // entity whose "on" is the breach
		owner            bool
	}{
		{"pg_h3_lit_ctrl", dir, "  - action: scene.apply\n    data:\n      entities: \"{{ {'input_boolean.pg_hunt_guard': 'on'} }}\"\n", huntGuard, false},
		{"pg_h3_esc_x", dir, "  - action: scene.apply\n    data:\n      entities: \"{{ {'input_boolean.pg_hunt_gu\\\\x61rd': 'on'} }}\"\n", huntGuard, false},
		{"pg_h3_esc_u", dir, "  - action: scene.apply\n    data:\n      entities: \"{{ {'input_boolean.pg_hunt_gu\\\\u0061rd': 'on'} }}\"\n", huntGuard, false},
		{"pg_h3_esc_oct", dir, "  - action: scene.apply\n    data:\n      entities: \"{{ {'input_boolean.pg_hunt_gu\\\\141rd': 'on'} }}\"\n", huntGuard, false},
		{"pg_h3_esc_grp", strict, "  - action: group.set\n    data:\n      object_id: pg_h3_eg\n      entities: \"{{ 'light.pg_hunt_hall\\\\x2clight.pg_hunt_bedroom' }}\"\n  - delay: 1\n  - action: homeassistant.turn_on\n    target:\n      entity_id: group.pg_h3_eg\n", huntGuard, false},
		{"pg_h3_include", dir, "", huntGuard, false},
		{"pg_h3_secret", dir, "  - action: logbook.log\n    data:\n      name: H3\n      entity_id: input_boolean.pg_hunt_ok\n      message: !secret h3_pw\n", "", false},
	}
	for i, k := range cases {
		huntResetGuard(t)
		ev := fmt.Sprintf("pg_h3_ctl_%d", i)
		body := "triggers:\n  - trigger: event\n    event_type: " + ev + "\n"
		if k.id == "pg_h3_include" {
			body += "actions: !include pg_h3_inc.yaml\n"
		} else {
			body += "actions:\n" + k.actions
		}
		pe, ce, rep := h3Auto(t, k.dir, tmp, k.id, body)
		if ce == 0 && strings.Contains(body, "!secret") {
			t.Cleanup(func() { h3DropAutomation(t, k.id) })
		}
		if ce == 0 {
			out, _ := exec.CommandContext(t.Context(), "docker", "exec", "hactl-companion-test-ha", "sh", "-c", "grep -n -A12 'id: "+k.id+"' /config/automations.yaml | head -20").CombinedOutput() //nolint:gosec // G204: fixed container; k.id is a test constant
			t.Logf("automations.yaml for %s:\n%s", k.id, out)
			st, attrs, _ := haStateOf(t, "automation."+k.id)
			t.Logf("automation.%s state=%s attrs=%v", k.id, st, attrs)
			huntHTTP(t, http.MethodPost, "/api/events/"+ev, map[string]any{})
			time.Sleep(4 * time.Second)
		}
		if k.check != "" {
			g := huntState(t, k.check)
			rhcVerdict(t, fmt.Sprintf("%s → %s=%s (preview=%d confirm=%d)", k.id, k.check, g, pe, ce), g == "on", rep)
		} else {
			ch := h3Changes(t, dir)
			leak := strings.Contains(ch, h3SecretValue)
			rhcVerdict(t, fmt.Sprintf("%s → agent changes prints secrets.yaml value=%v (preview=%d confirm=%d)", k.id, leak, pe, ce), leak, rep)
		}
	}
	huntResetGuard(t)
}

// P_lights: a typical "default deny + control lights" profile with one light
// held back (the bedroom), plus pg_* automation writing.
const h3Lights = `    default: deny
    allow:
      "entity:*": [read]
      "entity:light.*": [control]
      "service:light.*": [control]
      "service:scene.*": [control]
      "service:notify.*": [control]
      "service:persistent_notification.*": [control]
      "entity:scene.pg_*": [control, create, delete]
      "automation:pg_*": [read, edit, create, delete]
      "entity:automation.pg_*": [control, create, delete]
    deny:
      "entity:light.pg_hunt_bedroom": [control]
`

// P_open: default allow, deny one entity's control (the "front door lock" shape).
const h3Open = `    deny:
      "entity:input_boolean.pg_hunt_guard": [control]
`

// 4. Ordinary tasks a well-meaning agent writes.
func TestE2EPolicyHunt3Natural(t *testing.T) {
	h3Setup(t)
	dir := policyE2EDir(t, pgE2EPolicy(h3Lights))
	tmp := t.TempDir()
	// baseline: the group directly
	pe, ce, rep := huntAgent(t, dir, "svc", "call", "light.turn_on", "-d", `{"entity_id":"`+huntGroup+`"}`)
	t.Logf("baseline svc call light.turn_on %s: preview=%d confirm=%d\n      %s", huntGroup, pe, ce, rep)
	huntResetGuard(t)

	// snapshot / restore through a light group holding a denied light
	huntSvc(t, "input_boolean", "turn_on", map[string]any{"entity_id": huntOK})
	time.Sleep(time.Second)
	t.Logf("before: group=%s hall=%s bedroom=%s guard=%s", huntState(t, huntGroup), huntState(t, huntHall), huntState(t, huntBedroom), huntState(t, huntGuard))
	pe, ce, rep = h3Auto(t, dir, tmp, "pg_h3_snap", "triggers:\n  - trigger: event\n    event_type: pg_h3_snap_fire\nactions:\n  - action: scene.create\n    data:\n      scene_id: pg_h3_before\n      snapshot_entities:\n        - "+huntGroup+"\n  - action: light.turn_off\n    target:\n      entity_id: "+huntHall+"\n  - delay: 1\n  - action: scene.turn_on\n    target:\n      entity_id: scene.pg_h3_before\n")
	if ce == 0 {
		huntHTTP(t, http.MethodPost, "/api/events/pg_h3_snap_fire", map[string]any{})
		time.Sleep(4 * time.Second)
	}
	g := huntState(t, huntGuard)
	rhcVerdict(t, fmt.Sprintf("scene.create snapshot_entities=[light group holding the denied bedroom] → restore: guard(bedroom)=%s (preview=%d confirm=%d)", g, pe, ce), g == "on", rep)
	huntResetGuard(t)

	// scene.create with the group as an entities key, then turn it on
	pe, ce, rep = h3Auto(t, dir, tmp, "pg_h3_scgrp", "triggers:\n  - trigger: event\n    event_type: pg_h3_scgrp_fire\nactions:\n  - action: scene.create\n    data:\n      scene_id: pg_h3_evening\n      entities:\n        "+huntGroup+": \"on\"\n  - action: scene.turn_on\n    target:\n      entity_id: scene.pg_h3_evening\n")
	if ce == 0 {
		huntSvc(t, "input_boolean", "turn_off", map[string]any{"entity_id": huntOK})
		huntHTTP(t, http.MethodPost, "/api/events/pg_h3_scgrp_fire", map[string]any{})
		time.Sleep(4 * time.Second)
	}
	g = huntState(t, huntGuard)
	rhcVerdict(t, fmt.Sprintf("scene.create entities={light group: on} + scene.turn_on → guard(bedroom)=%s (preview=%d confirm=%d)", g, pe, ce), g == "on", rep)
	huntResetGuard(t)

	// "turn off all lights that are on"
	for _, k := range []struct{ id, tgt string }{
		{"pg_h3_alloff_tpl", "\"{{ states.light | selectattr('state','eq','on') | map(attribute='entity_id') | list }}\""},
		{"pg_h3_alloff_all", "all"},
	} {
		huntSvc(t, "input_boolean", "turn_on", map[string]any{"entity_id": huntGuard})
		pe, ce, rep = h3Auto(t, dir, tmp, k.id, "triggers:\n  - trigger: event\n    event_type: "+k.id+"_fire\nactions:\n  - action: light.turn_off\n    target:\n      entity_id: "+k.tgt+"\n")
		if ce == 0 {
			huntHTTP(t, http.MethodPost, "/api/events/"+k.id+"_fire", map[string]any{})
			time.Sleep(3 * time.Second)
		}
		g = huntState(t, huntGuard)
		rhcVerdict(t, fmt.Sprintf("%s light.turn_off target %s → guard(bedroom) on→%s (preview=%d confirm=%d)", k.id, k.tgt, g, pe, ce), g == "off", rep)
	}
	huntResetGuard(t)
}

// h3ReadDeny makes a profile deny reading the secret: the twin of each
// ordinary task reads it and is refused.
const h3ReadDeny = "      \"entity:input_boolean.pg_hunt_secret\": [read]\n"

// 5. Ordinary harmless tasks a well-meaning agent writes pass under a
// default-deny and a default-allow profile (D-198, D-199); each negative
// twin — the same template on a read-denied entity — is refused.
func TestE2EPolicyHunt3OverRefusal(t *testing.T) {
	h3Setup(t)
	tmp := t.TempDir()
	auto := func(dir, id, body string) (string, int) {
		t.Helper()
		f := huntWrite(t, tmp, id+".yaml", "id: "+id+"\nalias: "+id+"\n"+body)
		o, e, c := runPolicyE2E(t, dir, nil, "auto", "create", "-f", f)
		return o + " // " + e, c
	}
	ordinary := []struct{ id, body string }{
		{"pg_h3_or_trig", "triggers:\n  - trigger: state\n    entity_id: " + huntHall + "\n    to: \"on\"\nactions:\n  - action: persistent_notification.create\n    data:\n      message: \"{{ trigger.to_state.name }} turned on at {{ now().strftime('%H:%M') }}\"\n"},
		{"pg_h3_or_sun", "triggers:\n  - trigger: state\n    entity_id: " + huntHall + "\nconditions:\n  - condition: template\n    value_template: \"{{ is_state('sun.sun', 'below_horizon') }}\"\nactions:\n  - action: light.turn_off\n    target:\n      entity_id: " + huntHall + "\n"},
		{"pg_h3_or_list", "triggers:\n  - trigger: time\n    at: \"23:00:00\"\nactions:\n  - action: persistent_notification.create\n    data:\n      message: \"Lights still on: {{ states.light | selectattr('state','eq','on') | map(attribute='name') | join(', ') }}\"\n"},
		{"pg_h3_or_brt", "triggers:\n  - trigger: state\n    entity_id: " + huntHall + "\nactions:\n  - action: light.turn_on\n    target:\n      entity_id: " + huntHall + "\n    data:\n      brightness: \"{{ 255 if now().hour < 20 else 80 }}\"\n"},
		{"pg_h3_or_states1", "triggers:\n  - trigger: state\n    entity_id: " + huntHall + "\nactions:\n  - action: persistent_notification.create\n    data:\n      message: \"Sun is {{ states('sun.sun') }}\"\n"},
	}
	twins := []struct{ id, body string }{
		{"pg_h3_tw_trig", "triggers:\n  - trigger: state\n    entity_id: " + huntSecret + "\nactions:\n  - action: persistent_notification.create\n    data:\n      message: \"{{ trigger.to_state.name }} changed\"\n"},
		{"pg_h3_tw_sun", "triggers:\n  - trigger: state\n    entity_id: " + huntHall + "\nconditions:\n  - condition: template\n    value_template: \"{{ is_state('" + huntSecret + "', 'on') }}\"\nactions:\n  - action: light.turn_off\n    target:\n      entity_id: " + huntHall + "\n"},
		{"pg_h3_tw_list", "triggers:\n  - trigger: time\n    at: \"23:00:00\"\nactions:\n  - action: persistent_notification.create\n    data:\n      message: \"On: {{ states.input_boolean | selectattr('state','eq','on') | map(attribute='name') | join(', ') }}\"\n"},
		{"pg_h3_tw_states1", "triggers:\n  - trigger: state\n    entity_id: " + huntHall + "\nactions:\n  - action: persistent_notification.create\n    data:\n      message: \"Secret is {{ states('" + huntSecret + "') }}\"\n"},
		{"pg_h3_tw_cond", "triggers:\n  - trigger: state\n    entity_id: " + huntHall + "\nconditions:\n  - condition: state\n    entity_id: " + huntSecret + "\n    state: \"on\"\nactions:\n  - action: light.turn_off\n    target:\n      entity_id: " + huntHall + "\n"},
	}
	for _, prof := range []struct {
		name, body string
		open       bool
	}{
		{"P_lights(default deny)", h3Lights, false},
		{"P_open(default allow, one deny)", h3Open, true},
		{"P_lights + secret unreadable", h3Lights + h3ReadDeny, false},
		{"P_open + secret unreadable", h3Open + h3ReadDeny, true},
	} {
		dir := policyE2EDir(t, pgE2EPolicy(prof.body))
		for _, k := range ordinary {
			out, c := auto(dir, k.id, k.body)
			if c != 0 {
				t.Errorf("over-refusal: %s %s refused (exit %d)\n      %s", prof.name, k.id, c, huntLine(out, 700))
				continue
			}
			t.Logf("allowed %s %s: %s", prof.name, k.id, huntLine(out, 160))
		}
		if o, e, c := runPolicyE2E(t, dir, nil, "tpl", "eval", "{{ now().hour }}"); c != 0 {
			t.Errorf("over-refusal: %s tpl eval now().hour: exit %d %s", prof.name, c, huntLine(o+" // "+e, 500))
		}
		if o, e, c := runPolicyE2E(t, dir, nil, "tpl", "eval", "Sun is {{ states('sun.sun') }}"); c != 0 || !strings.Contains(o, "Sun is ") {
			t.Errorf("over-refusal: %s tpl eval states('sun.sun'): exit %d %s", prof.name, c, huntLine(o+" // "+e, 500))
		}
		for _, svc := range []string{"homeassistant.restart", "automation.reload"} {
			o, e, c := runPolicyE2E(t, dir, nil, "svc", "call", svc)
			if strings.Contains(o+e, "delete entity:") || (prof.open && c != 0) {
				t.Errorf("over-refusal: %s svc call %s (preview): exit %d — a reload or restart needs no delete (D-199)\n      %s", prof.name, svc, c, huntLine(o+" // "+e, 500))
			}
		}
		if !strings.Contains(prof.body, h3ReadDeny) {
			continue
		}
		for _, k := range twins {
			if out, c := auto(dir, k.id, k.body); c == 0 || !strings.Contains(out, "entity:"+huntSecret) {
				t.Errorf("BREACH %s %s: the twin reading the unreadable secret was not refused naming it (exit %d)\n      %s", prof.name, k.id, c, huntLine(out, 700))
			}
		}
		if o, e, c := runPolicyE2E(t, dir, nil, "tpl", "eval", "{{ states('"+huntSecret+"') }}"); c == 0 || !strings.Contains(o+e, "entity:"+huntSecret) {
			t.Errorf("BREACH %s tpl eval of the unreadable secret: exit %d %s", prof.name, c, huntLine(o+" // "+e, 500))
		}
	}
}
