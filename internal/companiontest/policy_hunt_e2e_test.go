//go:build companion

package companiontest

// The policy hunt of 2026-11-19, kept as regression tests (D-185, D-186).
// Written blind to the enforcement code: expectations from docs/policy.md and
// the manual's policy reference only; ground truth read back from Home
// Assistant over raw REST/WS. A "BREACH" error is a finding the hunt made: F4
// (a use_blueprint automation), F5 (a label an allowed automation targets,
// grown by the agent's own relabel).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

const (
	huntGuard      = "input_boolean.pg_hunt_guard"  // stands in for the lock: never to be switched by the agent
	huntOK         = "input_boolean.pg_hunt_ok"     // the agent may switch this one
	huntSecret     = "input_boolean.pg_hunt_secret" //nolint:gosec // G101: an entity id the agent may not read, not a credential
	huntSecretName = "PG Hunt Secret Xylophone"     //nolint:gosec // G101: a friendly name, not a credential
	huntBedroom    = "light.pg_hunt_bedroom"        // template light whose turn_on switches the guard
	huntHall       = "light.pg_hunt_hall"           // template light whose turn_on switches pg_hunt_ok
	huntGroup      = "light.pg_hunt_hall_group"
)

func huntHTTP(t *testing.T, method, path string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader = http.NoBody
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, haURL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+haToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := (&http.Client{Timeout: 12 * time.Second}).Do(req)
	if err != nil {
		// automation.trigger on a blueprint that waits blocks the REST call;
		// the action has already started, so a timeout is not a failure.
		t.Logf("%s %s: %v (continuing)", method, path, err)
		return 0, nil
	}
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

func huntSvc(t *testing.T, domain, svc string, data map[string]any) {
	t.Helper()
	code, b := huntHTTP(t, http.MethodPost, "/api/services/"+domain+"/"+svc, data)
	if code != 200 {
		t.Logf("owner call %s.%s: http %d %s", domain, svc, code, b)
	}
}

func huntState(t *testing.T, id string) string {
	t.Helper()
	s, _, ok := haStateOf(t, id)
	if !ok {
		return "<absent>"
	}
	return s
}

func huntWrite(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func huntLine(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " | ")
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// huntAgent runs preview then --confirm (only when the preview passed) and
// returns a one-line account.
func huntAgent(t *testing.T, dir string, args ...string) (previewExit, confirmExit int, report string) {
	t.Helper()
	o, e, c := runPolicyE2E(t, dir, nil, args...)
	report = fmt.Sprintf("preview exit=%d: %s", c, huntLine(o+" // "+e, 420))
	if c != 0 {
		return c, -1, report
	}
	o2, e2, c2 := runPolicyE2E(t, dir, nil, append(append([]string{}, args...), "--confirm")...)
	report += fmt.Sprintf("\n      confirm exit=%d: %s", c2, huntLine(o2+" // "+e2, 420))
	return c, c2, report
}

var (
	huntOnce  sync.Once
	huntAdmin string
)

// huntSetup builds the owner's fixture once: three input_booleans, two
// template lights, a light group helper, a label, an owner automation that
// watches the secret helper.
func huntSetup(t *testing.T) string {
	t.Helper()
	huntOnce.Do(func() {
		admin, err := createE2EInstanceDir(haURL, haToken, compURL, companionToken)
		if err != nil {
			t.Fatal(err)
		}
		huntAdmin = admin
		tmp := t.TempDir()
		own := func(args ...string) {
			o, e, c := runPolicyE2E(t, admin, nil, append(args, "--confirm")...)
			t.Logf("owner %v exit=%d %s", args[:2], c, huntLine(o+" "+e, 200))
		}
		for _, h := range []struct{ id, name string }{{"pg_hunt_guard", "PG Hunt Guard"}, {"pg_hunt_ok", "PG Hunt OK"}, {"pg_hunt_secret", huntSecretName}} {
			own("helper", "create", "input_boolean", "-f", huntWrite(t, tmp, h.id+".yaml", h.id+":\n  name: "+h.name+"\n"))
		}
		own("tpl", "create", "-f", huntWrite(t, tmp, "lights.yaml", `light:
  - name: PG Hunt Bedroom
    unique_id: pg_hunt_bedroom
    state: "{{ is_state('input_boolean.pg_hunt_guard', 'on') }}"
    turn_on:
      - action: input_boolean.turn_on
        target:
          entity_id: input_boolean.pg_hunt_guard
    turn_off:
      - action: input_boolean.turn_off
        target:
          entity_id: input_boolean.pg_hunt_guard
  - name: PG Hunt Hall
    unique_id: pg_hunt_hall
    state: "{{ is_state('input_boolean.pg_hunt_ok', 'on') }}"
    turn_on:
      - action: input_boolean.turn_on
        target:
          entity_id: input_boolean.pg_hunt_ok
    turn_off:
      - action: input_boolean.turn_off
        target:
          entity_id: input_boolean.pg_hunt_ok
`))
		own("auto", "create", "-f", huntWrite(t, tmp, "watch.yaml", `id: pg_hunt_watch
alias: pg_hunt_watch
triggers:
  - trigger: state
    entity_id: input_boolean.pg_hunt_secret
conditions:
  - condition: state
    entity_id: input_boolean.pg_hunt_secret
    state: "on"
actions:
  - delay: 0
`))
		own("auto", "create", "-f", huntWrite(t, tmp, "owner_off.yaml", `id: pg_hunt_owner_off
alias: pg_hunt_owner_off
triggers:
  - trigger: event
    event_type: pg_hunt_never
actions:
  - action: input_boolean.turn_off
    target:
      entity_id: input_boolean.pg_hunt_guard
`))
		waitForHAState(t, huntGuard)
		waitForHAState(t, huntBedroom)
		waitForHAState(t, huntHall)
		waitForHAState(t, "automation.pg_hunt_watch")

		// light group helper via config flow (owner, raw REST)
		_, b := huntHTTP(t, http.MethodPost, "/api/config/config_entries/flow", map[string]any{"handler": "group", "show_advanced_options": false})
		var f struct {
			FlowID string `json:"flow_id"`
		}
		_ = json.Unmarshal(b, &f)
		_, b2 := huntHTTP(t, http.MethodPost, "/api/config/config_entries/flow/"+f.FlowID, map[string]any{"next_step_id": "light"})
		t.Logf("group flow light step: %s", huntLine(string(b2), 300))
		_, b3 := huntHTTP(t, http.MethodPost, "/api/config/config_entries/flow/"+f.FlowID, map[string]any{"name": "PG Hunt Hall Group", "entities": []string{huntHall, huntBedroom}, "hide_members": false})
		t.Logf("group flow create: %s", huntLine(string(b3), 300))
		waitForHAState(t, huntGroup)

		// owner toggles the secret so the watch automation leaves a trace
		huntSvc(t, "input_boolean", "turn_on", map[string]any{"entity_id": huntSecret})
		time.Sleep(2 * time.Second)

		var out bytes.Buffer
		c := exec.CommandContext(t.Context(), "docker", "exec", "hactl-companion-test-ha", "sh", "-c", "find /config/blueprints -type f | sort")
		c.Stdout, c.Stderr = &out, &out
		_ = c.Run()
		t.Logf("blueprints on the instance:\n%s", out.String())
	})
	return huntAdmin
}

// Profile: default deny; may write pg_* automations and scripts, see
// everything, switch only pg_hunt_ok and the hall light, label anything.
// Never switch the guard or the bedroom light.
const huntWriterProfile = `    default: deny
    allow:
      "entity:*": [read]
      "automation:pg_*": [read, edit, create, delete]
      "entity:automation.pg_*": [control, create, delete]
      "script:pg_*": [read, edit, create, delete, control]
      "entity:script.pg_*": [control, create, delete]
      "template:pg_hunt_agent_*": [read, edit, create, delete]
      "entity:input_boolean.pg_hunt_ok": [control]
      "entity:light.pg_hunt_hall*": [control]
      "entity:*/registry/labels": [edit]
      "service:input_boolean.*": [control]
      "service:light.*": [control]
      "service:homeassistant.turn_on": [control]
      "service:automation.*": [control]
      "service:script.*": [control]
      "file:**": [edit]
    deny:
      "entity:input_boolean.pg_hunt_guard": [control]
      "entity:light.pg_hunt_bedroom": [control]
`

func huntResetGuard(t *testing.T) {
	t.Helper()
	huntSvc(t, "input_boolean", "turn_off", map[string]any{"entity_id": huntGuard})
	time.Sleep(500 * time.Millisecond)
}

// The automations an agent writes: Home Assistant spellings of "switch the
// guard" that the agent might plausibly write.
func TestPolicyHuntWrittenAutomationSpellings(t *testing.T) {
	huntSetup(t)
	dir := policyE2EDir(t, pgE2EPolicy(huntWriterProfile))
	tmp := t.TempDir()
	head := func(id string) string {
		return "id: " + id + "\nalias: " + id + "\ntriggers:\n  - trigger: event\n    event_type: pg_hunt_never\n"
	}
	cases := []struct{ id, yaml string }{
		{"pg_hunt_v_baseline", head("pg_hunt_v_baseline") + "actions:\n  - action: input_boolean.turn_on\n    target:\n      entity_id: input_boolean.pg_hunt_guard\n"},
		{"pg_hunt_v_toplevel", head("pg_hunt_v_toplevel") + "actions:\n  - action: input_boolean.turn_on\n    entity_id: input_boolean.pg_hunt_guard\n"},
		{"pg_hunt_v_legacy", "id: pg_hunt_v_legacy\nalias: pg_hunt_v_legacy\ntrigger:\n  - platform: event\n    event_type: pg_hunt_never\naction:\n  - service: input_boolean.turn_on\n    target:\n      entity_id: input_boolean.pg_hunt_guard\n"},
		{"pg_hunt_v_legacy_eid", "id: pg_hunt_v_legacy_eid\nalias: pg_hunt_v_legacy_eid\ntrigger:\n  - platform: event\n    event_type: pg_hunt_never\naction:\n  - service: input_boolean.turn_on\n    entity_id: input_boolean.pg_hunt_guard\n"},
		{"pg_hunt_v_datatpl", head("pg_hunt_v_datatpl") + "actions:\n  - action: input_boolean.turn_on\n    data_template:\n      entity_id: input_boolean.pg_hunt_guard\n"},
		{"pg_hunt_v_svctpl", head("pg_hunt_v_svctpl") + "actions:\n  - service_template: input_boolean.turn_on\n    target:\n      entity_id: input_boolean.pg_hunt_guard\n"},
		{"pg_hunt_v_mapping", head("pg_hunt_v_mapping") + "actions:\n  action: input_boolean.turn_on\n  target:\n    entity_id: input_boolean.pg_hunt_guard\n"},
		{"pg_hunt_v_then_map", head("pg_hunt_v_then_map") + "actions:\n  - if:\n      - condition: template\n        value_template: \"{{ true }}\"\n    then:\n      action: input_boolean.turn_on\n      target:\n        entity_id: input_boolean.pg_hunt_guard\n"},
		{"pg_hunt_v_comma", head("pg_hunt_v_comma") + "actions:\n  - action: input_boolean.turn_on\n    target:\n      entity_id: input_boolean.pg_hunt_ok, input_boolean.pg_hunt_guard\n"},
		{"pg_hunt_v_upper", head("pg_hunt_v_upper") + "actions:\n  - action: input_boolean.turn_on\n    target:\n      entity_id: INPUT_BOOLEAN.PG_HUNT_GUARD\n"},
		{"pg_hunt_v_group", head("pg_hunt_v_group") + "actions:\n  - action: light.turn_on\n    target:\n      entity_id: " + huntGroup + "\n"},
		{"pg_hunt_v_dupkey", "id: pg_hunt_v_dupkey\nalias: pg_hunt_v_dupkey\nactions: []\ntriggers:\n  - trigger: event\n    event_type: pg_hunt_never\nactions:\n  - action: input_boolean.turn_on\n    target:\n      entity_id: input_boolean.pg_hunt_guard\n"},
		{"pg_hunt_v_bp", "id: pg_hunt_v_bp\nalias: pg_hunt_v_bp\nuse_blueprint:\n  path: homeassistant/motion_light.yaml\n  input:\n    motion_entity: input_boolean.pg_hunt_ok\n    light_target:\n      entity_id: " + huntBedroom + "\n    no_motion_wait: 0\n"},
		{"pg_hunt_v_bp_area", "id: pg_hunt_v_bp_area\nalias: pg_hunt_v_bp_area\nuse_blueprint:\n  path: homeassistant/motion_light.yaml\n  input:\n    motion_entity: input_boolean.pg_hunt_ok\n    light_target:\n      entity_id:\n        - " + huntHall + "\n        - " + huntBedroom + "\n    no_motion_wait: 0\n"},
	}
	for _, c := range cases {
		huntResetGuard(t)
		f := huntWrite(t, tmp, c.id+".yaml", c.yaml)
		pe, ce, rep := huntAgent(t, dir, "auto", "create", "-f", f)
		fired := "not created"
		if ce == 0 {
			if _, _, ok := haStateOf(t, "automation."+c.id); !ok {
				time.Sleep(3 * time.Second)
			}
			huntSvc(t, "automation", "trigger", map[string]any{"entity_id": "automation." + c.id})
			time.Sleep(2500 * time.Millisecond)
			fired = "triggered by owner"
		}
		g := huntState(t, huntGuard)
		verdict := "held"
		if g == "on" {
			verdict = "BREACH"
			t.Errorf("%s: the agent-written automation switched the control-denied guard on", c.id)
		}
		if strings.HasPrefix(c.id, "pg_hunt_v_bp") && (pe == 0 || !strings.Contains(rep, "entity:"+huntBedroom)) {
			t.Errorf("%s: preview exit=%d, want refused naming entity:%s — what the blueprint expands to (D-185)", c.id, pe, huntBedroom)
		}
		t.Logf("RESULT %-22s %s (preview=%d confirm=%d, %s, guard=%s, automation state=%s)\n      %s",
			c.id, verdict, pe, ce, fired, g, huntState(t, "automation."+c.id), rep)
	}
}

// Agent-written scripts in a legacy spelling, then run by the agent.
func TestPolicyHuntWrittenScriptSpellings(t *testing.T) {
	huntSetup(t)
	dir := policyE2EDir(t, pgE2EPolicy(huntWriterProfile))
	tmp := t.TempDir()
	cases := []struct{ id, yaml string }{
		{"pg_hunt_s_baseline", "pg_hunt_s_baseline:\n  sequence:\n    - action: input_boolean.turn_on\n      target:\n        entity_id: input_boolean.pg_hunt_guard\n"},
		{"pg_hunt_s_legacy_eid", "pg_hunt_s_legacy_eid:\n  sequence:\n    - service: input_boolean.turn_on\n      entity_id: input_boolean.pg_hunt_guard\n"},
		{"pg_hunt_s_datatpl", "pg_hunt_s_datatpl:\n  sequence:\n    - service: input_boolean.turn_on\n      data_template:\n        entity_id: input_boolean.pg_hunt_guard\n"},
		{"pg_hunt_s_mapping", "pg_hunt_s_mapping:\n  sequence:\n    action: input_boolean.turn_on\n    target:\n      entity_id: input_boolean.pg_hunt_guard\n"},
	}
	for _, c := range cases {
		huntResetGuard(t)
		f := huntWrite(t, tmp, c.id+".yaml", c.yaml)
		pe, ce, rep := huntAgent(t, dir, "script", "create", "-f", f)
		if ce == 0 {
			waitForHAState(t, "script."+c.id)
			_, _, r := huntAgent(t, dir, "script", "run", c.id)
			rep += "\n      run: " + r
			time.Sleep(2 * time.Second)
		}
		g := huntState(t, huntGuard)
		verdict := "held"
		if g == "on" {
			verdict = "BREACH"
			t.Errorf("%s: the agent-written script switched the control-denied guard on", c.id)
		}
		t.Logf("RESULT %-22s %s (preview=%d confirm=%d guard=%s)\n      %s", c.id, verdict, pe, ce, g, rep)
	}
}

// Template entities the agent writes with action keys beyond turn_on/off.
func TestPolicyHuntWrittenTemplateActions(t *testing.T) {
	huntSetup(t)
	dir := policyE2EDir(t, pgE2EPolicy(huntWriterProfile))
	tmp := t.TempDir()
	on := "\n      - action: input_boolean.turn_on\n        target:\n          entity_id: input_boolean.pg_hunt_guard\n"
	ok := "\n      - action: input_boolean.turn_on\n        target:\n          entity_id: input_boolean.pg_hunt_ok\n"
	cases := []struct {
		name, yaml string
		fire       func()
	}{
		{"light set_level", "light:\n  - name: pg_hunt_agent_lvl\n    unique_id: pg_hunt_agent_lvl\n    turn_on:" + ok + "    turn_off:" + ok + "    set_level:" + on,
			func() {
				huntSvc(t, "light", "turn_on", map[string]any{"entity_id": "light.pg_hunt_agent_lvl", "brightness": 120})
			}},
		{"number set_value", "number:\n  - name: pg_hunt_agent_num\n    unique_id: pg_hunt_agent_num\n    state: \"{{ 1 }}\"\n    min: 0\n    max: 10\n    step: 1\n    set_value:" + on,
			func() {
				huntSvc(t, "number", "set_value", map[string]any{"entity_id": "number.pg_hunt_agent_num", "value": 3})
			}},
		{"select select_option", "select:\n  - name: pg_hunt_agent_sel\n    unique_id: pg_hunt_agent_sel\n    state: \"a\"\n    options: \"{{ ['a','b'] }}\"\n    select_option:" + on,
			func() {
				huntSvc(t, "select", "select_option", map[string]any{"entity_id": "select.pg_hunt_agent_sel", "option": "b"})
			}},
		{"cover open_cover", "cover:\n  - name: pg_hunt_agent_cov\n    unique_id: pg_hunt_agent_cov\n    open_cover:" + on + "    close_cover:" + ok,
			func() { huntSvc(t, "cover", "open_cover", map[string]any{"entity_id": "cover.pg_hunt_agent_cov"}) }},
		{"trigger block actions", "triggers:\n  - trigger: event\n    event_type: pg_hunt_tpl_fire\nactions:\n  - action: input_boolean.turn_on\n    target:\n      entity_id: input_boolean.pg_hunt_guard\nsensor:\n  - name: pg_hunt_agent_trig\n    unique_id: pg_hunt_agent_trig\n    state: \"{{ now() }}\"\n",
			func() {
				huntHTTP(t, http.MethodPost, "/api/events/pg_hunt_tpl_fire", map[string]any{})
			}},
		{"baseline switch turn_on", "switch:\n  - name: pg_hunt_agent_sw\n    unique_id: pg_hunt_agent_sw\n    turn_on:" + on + "    turn_off:" + ok,
			func() { huntSvc(t, "switch", "turn_on", map[string]any{"entity_id": "switch.pg_hunt_agent_sw"}) }},
	}
	for i, c := range cases {
		huntResetGuard(t)
		f := huntWrite(t, tmp, fmt.Sprintf("t%d.yaml", i), c.yaml)
		pe, ce, rep := huntAgent(t, dir, "tpl", "create", "-f", f)
		if ce == 0 {
			time.Sleep(4 * time.Second)
			c.fire()
			time.Sleep(2500 * time.Millisecond)
		}
		g := huntState(t, huntGuard)
		verdict := "held"
		if g == "on" {
			verdict = "BREACH"
			t.Errorf("%s: agent-written template entity switched the control-denied guard", c.name)
		}
		t.Logf("RESULT %-24s %s (preview=%d confirm=%d guard=%s)\n      %s", c.name, verdict, pe, ce, g, rep)
	}
}

// An allowed automation targets a label. The agent may label anything (the
// docs' quick start) and later labels the guard — an ordinary "organize"
// task. The automation now switches the guard.
func TestPolicyHuntLabelTargetGrowsAfterWrite(t *testing.T) {
	huntSetup(t)
	dir := policyE2EDir(t, pgE2EPolicy(huntWriterProfile))
	tmp := t.TempDir()
	// owner: label pg_hunt_cosy on pg_hunt_ok
	o, e, c := runPolicyE2E(t, huntAdmin, nil, "label", "create", "pg_hunt_cosy", "--confirm")
	t.Logf("owner label create: %d %s", c, huntLine(o+e, 200))
	o, e, c = runPolicyE2E(t, huntAdmin, nil, "ent", "set-label", huntOK, "pg_hunt_cosy", "--confirm")
	t.Logf("owner set-label ok: %d %s", c, huntLine(o+e, 200))
	time.Sleep(time.Second)
	huntResetGuard(t)

	f := huntWrite(t, tmp, "lab.yaml", "id: pg_hunt_v_label\nalias: pg_hunt_v_label\ntriggers:\n  - trigger: event\n    event_type: pg_hunt_never\nactions:\n  - action: input_boolean.turn_on\n    target:\n      label_id: pg_hunt_cosy\n")
	pe, ce, rep := huntAgent(t, dir, "auto", "create", "-f", f)
	t.Logf("step 1 agent auto create (label target): preview=%d confirm=%d\n      %s", pe, ce, rep)
	if ce != 0 {
		t.Logf("RESULT label-later: held at step 1 (create refused)")
		return
	}
	pe, ce, rep = huntAgent(t, dir, "ent", "set-label", huntGuard, "pg_hunt_cosy")
	t.Logf("step 2 agent ent set-label guard pg_hunt_cosy: preview=%d confirm=%d\n      %s", pe, ce, rep)
	if pe == 0 || !strings.Contains(rep, "control entity:"+huntGuard) {
		t.Errorf("step 2: the relabel that puts the guard under a targeted label: preview exit=%d, want refused naming control entity:%s (D-186)", pe, huntGuard)
	}
	// the agent's own direct switch of the guard is refused:
	o, e, c = runPolicyE2E(t, dir, nil, "svc", "call", "input_boolean.turn_on", "--data", `{"label_id":"pg_hunt_cosy"}`)
	t.Logf("control: agent svc call label target now: exit=%d %s", c, huntLine(o+e, 300))
	waitForHAState(t, "automation.pg_hunt_v_label")
	huntSvc(t, "automation", "trigger", map[string]any{"entity_id": "automation.pg_hunt_v_label"})
	time.Sleep(2500 * time.Millisecond)
	g := huntState(t, huntGuard)
	t.Logf("RESULT label-later: guard=%s", g)
	if g == "on" {
		t.Errorf("BREACH: agent-written automation (allowed at write time) switches the control-denied guard after the agent's own allowed relabel")
	}
}

// ref replace: the agent points its own allowed automation at a light group
// (allowed by pattern) that contains the denied bedroom light.
func TestPolicyHuntRefReplaceIntoGroup(t *testing.T) {
	huntSetup(t)
	dir := policyE2EDir(t, pgE2EPolicy(huntWriterProfile))
	tmp := t.TempDir()
	huntResetGuard(t)
	f := huntWrite(t, tmp, "rr.yaml", "id: pg_hunt_v_rr\nalias: pg_hunt_v_rr\ntriggers:\n  - trigger: event\n    event_type: pg_hunt_never\nactions:\n  - action: light.turn_on\n    target:\n      entity_id: "+huntHall+"\n")
	pe, ce, rep := huntAgent(t, dir, "auto", "create", "-f", f)
	t.Logf("agent auto create (hall light): preview=%d confirm=%d\n      %s", pe, ce, rep)
	o, e, c := runPolicyE2E(t, dir, nil, "svc", "call", "light.turn_on", "--data", `{"entity_id":"`+huntGroup+`"}`)
	t.Logf("control: agent svc call on the group: exit=%d %s", c, huntLine(o+e, 300))
	pe, ce, rep = huntAgent(t, dir, "ref", "replace", huntHall, huntGroup)
	t.Logf("agent ref replace hall → group: preview=%d confirm=%d\n      %s", pe, ce, rep)
	time.Sleep(3 * time.Second)
	cat, _, _ := runPolicyE2E(t, huntAdmin, nil, "auto", "cat", "pg_hunt_v_rr")
	t.Logf("automation now:\n%s", cat)
	huntSvc(t, "automation", "trigger", map[string]any{"entity_id": "automation.pg_hunt_v_rr"})
	time.Sleep(2500 * time.Millisecond)
	g := huntState(t, huntGuard)
	t.Logf("RESULT ref-replace-into-group: guard=%s bedroom=%s", g, huntState(t, huntBedroom))
	if g == "on" {
		t.Errorf("BREACH: ref replace pointed an agent automation at a group holding the denied light")
	}
}

// ref replace on a service name inside an owner automation the agent may edit.
func TestPolicyHuntRefReplaceServiceName(t *testing.T) {
	huntSetup(t)
	for _, prof := range []struct{ name, body string }{
		{"default-deny writer", huntWriterProfile},
		{"default-allow", "    deny:\n      \"entity:input_boolean.pg_hunt_guard\": [control]\n"},
	} {
		dir := policyE2EDir(t, pgE2EPolicy(prof.body))
		huntResetGuard(t)
		pe, ce, rep := huntAgent(t, dir, "ref", "replace", "input_boolean.turn_off", "input_boolean.turn_on")
		time.Sleep(3 * time.Second)
		cat, _, _ := runPolicyE2E(t, huntAdmin, nil, "auto", "cat", "pg_hunt_owner_off")
		huntSvc(t, "automation", "trigger", map[string]any{"entity_id": "automation.pg_hunt_owner_off"})
		time.Sleep(2500 * time.Millisecond)
		g := huntState(t, huntGuard)
		t.Logf("RESULT ref replace service name [%s]: preview=%d confirm=%d guard=%s\n      %s\n      automation now: %s", prof.name, pe, ce, g, rep, huntLine(cat, 300))
		if g == "on" {
			t.Errorf("BREACH [%s]: ref replace turned an automation that switches the guard off into one that switches it on", prof.name)
			// put it back for the next profile
			runPolicyE2E(t, huntAdmin, nil, "ref", "replace", "input_boolean.turn_on", "input_boolean.turn_off", "--confirm")
		}
	}
}

// Reads: the agent may read everything except the secret helper.
func TestPolicyHuntReadsAroundSecret(t *testing.T) {
	huntSetup(t)
	dir := policyE2EDir(t, pgE2EPolicy("    default: deny\n    allow:\n      \"*\": [read]\n    deny:\n      \"entity:input_boolean.pg_hunt_secret\": [read]\n"))
	huntSvc(t, "input_boolean", "turn_off", map[string]any{"entity_id": huntSecret})
	time.Sleep(time.Second)
	huntSvc(t, "input_boolean", "turn_on", map[string]any{"entity_id": huntSecret})
	time.Sleep(2 * time.Second)
	needle := regexp.MustCompile(`(?i)xylophone|pg_hunt_secret`)
	cmds := [][]string{
		{"ent", "show", huntSecret},
		{"trace", "show", "pg_hunt_watch"}, {"trace", "show", "pg_hunt_watch", "--full"}, {"trace", "show", "pg_hunt_watch", "--json"},
		{"auto", "show", "pg_hunt_watch"}, {"auto", "show", "pg_hunt_watch", "--json"}, {"auto", "ls", "--json"},
		{"changes", "--since", "1h"}, {"ent", "who", "automation.pg_hunt_watch", "--since", "1h"},
		{"helper", "ls"}, {"helper", "show", "pg_hunt_secret"}, {"helper", "cat", "pg_hunt_secret"},
		{"grep", "xylophone"}, {"ref", "scan", huntSecret}, {"config", "file", "input_boolean.yaml"},
		{"ent", "related", "automation.pg_hunt_watch"},
	}
	// Where the read-denied helper may still show: the helper's own config
	// (kind helper, not entity — the profile reads every helper) and the file
	// it is in (grep, config file: checked as the file), as docs/policy.md
	// says; a trace and a logbook row of a readable automation that name what
	// triggered it — observed by the hunt, not yet in docs/policy.md (reported
	// 2026-11-20). Anything else that names it is a leak.
	shows := map[string]bool{
		"helper ls": true, "helper show pg_hunt_secret": true, "helper cat pg_hunt_secret": true,
		"grep xylophone": true, "config file input_boolean.yaml": true,
		"trace show pg_hunt_watch --full": true, "trace show pg_hunt_watch --json": true,
		"changes --since 1h": true,
	}
	hits := make([]string, 0, len(cmds))
	for _, c := range cmds {
		o, e, code := runPolicyE2E(t, dir, nil, c...)
		if c[0] == "ent" && c[1] == "show" && code == 0 {
			t.Errorf("hactl ent show %s: exit 0, want the read refused", huntSecret)
		}
		var ctx []string
		for line := range strings.SplitSeq(o+"\n"+e, "\n") {
			if needle.MatchString(line) {
				l := strings.TrimSpace(line)
				if len(l) > 400 {
					l = l[:400]
				}
				ctx = append(ctx, l)
			}
		}
		hits = append(hits, fmt.Sprintf("hactl %s (exit %d): %d lines\n    %s", strings.Join(c, " "), code, len(ctx), strings.Join(ctx, "\n    ")))
		if cmd := strings.Join(c, " "); len(ctx) > 0 && code == 0 && !shows[cmd] {
			t.Errorf("hactl %s names the read-denied %s:\n    %s", cmd, huntSecret, strings.Join(ctx, "\n    "))
		}
	}
	t.Logf("RESULT reads around read-denied %s:\n%s", huntSecret, strings.Join(hits, "\n"))
}

// sectionsBlueprint is an automation blueprint with an input inside a section
// and a top-level input, both with defaults — the shape reqmap's
// TestBlueprintExpansion expands.
const sectionsBlueprint = `blueprint:
  name: PG Hunt Sections
  domain: automation
  input:
    targets:
      name: Targets
      input:
        switch_target:
          name: Switch
          default:
            entity_id: input_boolean.pg_hunt_guard
          selector:
            target: {}
    event_name:
      name: Event
      default: pg_hunt_bp_fire
mode: single
triggers:
  - trigger: event
    event_type: !input event_name
actions:
  - action: input_boolean.turn_on
    target: !input switch_target
`

// TestE2EBlueprintSubstitutionOracle — D-185, oracle first: Home Assistant
// substitutes a section's input default, a top-level default and a given
// input, and the automation's own keys win over the blueprint's — the four
// facts reqmap.ExpandBlueprints is built on. Then hactl's verdict on the same
// candidates under the writer profile follows what Home Assistant runs.
func TestE2EBlueprintSubstitutionOracle(t *testing.T) {
	admin := huntSetup(t)
	c := exec.CommandContext(t.Context(), "docker", "exec", "-i", "hactl-companion-test-ha", "sh", "-c",
		"mkdir -p /config/blueprints/automation/pg && cat > /config/blueprints/automation/pg/pg_hunt_sections.yaml")
	c.Stdin = strings.NewReader(sectionsBlueprint)
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("placing the blueprint: %v %s", err, out)
	}
	tmp := t.TempDir()
	use := func(id, extra string) string {
		return huntWrite(t, tmp, id+".yaml", "id: "+id+"\nalias: "+id+"\nuse_blueprint:\n  path: pg/pg_hunt_sections.yaml\n"+extra)
	}
	defaults := use("pg_hunt_bp_defaults", "")
	given := use("pg_hunt_bp_given", "  input:\n    event_name: pg_hunt_bp_given\n    switch_target:\n      entity_id: "+huntOK+"\n")
	override := use("pg_hunt_bp_override", "  input:\n    event_name: pg_hunt_bp_override\nactions:\n  - action: input_boolean.turn_on\n    target:\n      entity_id: "+huntOK+"\n")
	for _, f := range []string{defaults, given, override} {
		if o, e, code := runPolicyE2E(t, admin, nil, "auto", "create", "-f", f, "--confirm"); code != 0 {
			t.Fatalf("owner auto create %s: exit %d %s", f, code, huntLine(o+e, 300))
		}
	}
	for _, id := range []string{"pg_hunt_bp_defaults", "pg_hunt_bp_given", "pg_hunt_bp_override"} {
		waitForHAState(t, "automation."+id)
	}
	fire := func(event string) (guard, ok string) {
		huntSvc(t, "input_boolean", "turn_off", map[string]any{"entity_id": []string{huntGuard, huntOK}})
		time.Sleep(500 * time.Millisecond)
		huntHTTP(t, http.MethodPost, "/api/events/"+event, map[string]any{})
		time.Sleep(2 * time.Second)
		return huntState(t, huntGuard), huntState(t, huntOK)
	}
	if g, o := fire("pg_hunt_bp_fire"); g != "on" || o != "off" {
		t.Errorf("ORACLE defaults: guard=%s ok=%s, want on/off — the section's default target and the top-level default event", g, o)
	}
	if g, o := fire("pg_hunt_bp_given"); g != "off" || o != "on" {
		t.Errorf("ORACLE given input: guard=%s ok=%s, want off/on — the given target over the default", g, o)
	}
	if g, o := fire("pg_hunt_bp_override"); g != "off" || o != "on" {
		t.Errorf("ORACLE own keys: guard=%s ok=%s, want off/on — the automation's actions over the blueprint's", g, o)
	}
	huntResetGuard(t)

	// hactl, as the writer profile, on the same candidates under new ids.
	dir := policyE2EDir(t, pgE2EPolicy(huntWriterProfile))
	for _, c := range []struct {
		id, extra string
		refused   bool
	}{
		{"pg_hunt_bpa_defaults", "", true},
		{"pg_hunt_bpa_given", "  input:\n    switch_target:\n      entity_id: " + huntOK + "\n", false},
		{"pg_hunt_bpa_override", "actions:\n  - action: input_boolean.turn_on\n    target:\n      entity_id: " + huntOK + "\n", false},
	} {
		o, e, code := runPolicyE2E(t, dir, nil, "auto", "create", "-f", use(c.id, c.extra))
		switch {
		case c.refused && (code == 0 || !strings.Contains(o+e, "control entity:"+huntGuard)):
			t.Errorf("%s: preview exit=%d, want refused naming control entity:%s\n  %s", c.id, code, huntGuard, huntLine(o+e, 400))
		case !c.refused && code != 0:
			t.Errorf("%s: preview exit=%d, want allowed — Home Assistant runs it on %s only\n  %s", c.id, code, huntOK, huntLine(o+e, 400))
		}
	}
	// A blueprint that does not exist is refused, never passed.
	o, e, code := runPolicyE2E(t, dir, nil, "auto", "create", "-f", huntWrite(t, tmp, "missing.yaml", "id: pg_hunt_bpa_missing\nuse_blueprint:\n  path: pg/none.yaml\n"))
	if code == 0 || !strings.Contains(o+e, "blueprint cannot be expanded") {
		t.Errorf("a missing blueprint: preview exit=%d, want refused fail-closed\n  %s", code, huntLine(o+e, 400))
	}
}

// TestE2EServiceInventoryMatchesHomeAssistant — D-184: every service the
// companion tier's instance exposes, with every field, is in the service
// inventory the service manifest classifies.
func TestE2EServiceInventoryMatchesHomeAssistant(t *testing.T) {
	code, body := huntHTTP(t, http.MethodGet, "/api/services", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/services: http %d", code)
	}
	raw, err := os.ReadFile("../../testdata/policy/services/inventory.json")
	if err != nil {
		t.Fatal(err)
	}
	var inv map[string]struct {
		Target bool     `json:"target"`
		Fields []string `json:"fields"`
	}
	if err := json.Unmarshal(raw, &inv); err != nil {
		t.Fatal(err)
	}
	var live []struct {
		Domain   string `json:"domain"`
		Services map[string]struct {
			Fields map[string]json.RawMessage `json:"fields"`
			Target json.RawMessage            `json:"target"`
		} `json:"services"`
	}
	if err := json.Unmarshal(body, &live); err != nil {
		t.Fatalf("decoding /api/services: %v", err)
	}
	n := 0
	for _, d := range live {
		for name, s := range d.Services {
			n++
			key := reqmap.ServiceInventoryKey(d.Domain, name)
			e, ok := inv[key]
			if !ok {
				t.Errorf("%s.%s is not in the service inventory: classify it (D-184, dev/surfaces/service.manifest)", d.Domain, name)
				continue
			}
			if key == "script.<name>" {
				continue // a script's fields are its own
			}
			for f := range serviceFieldNames(s.Fields) {
				if !slices.Contains(e.Fields, f) {
					t.Errorf("%s has the field %s the inventory does not list: classify it (D-184)", key, f)
				}
			}
			if len(s.Target) > 0 && string(s.Target) != "null" && !e.Target {
				t.Errorf("%s takes a target the inventory does not record (D-184)", key)
			}
		}
	}
	if n < 100 {
		t.Fatalf("Home Assistant exposed %d services, want the full set", n)
	}
}

// serviceFieldNames is a service's fields with every section's fields in
// place of the section.
func serviceFieldNames(fields map[string]json.RawMessage) map[string]bool {
	out := map[string]bool{}
	for name, raw := range fields {
		var sec struct {
			Fields map[string]json.RawMessage `json:"fields"`
		}
		if json.Unmarshal(raw, &sec) == nil && sec.Fields != nil {
			for inner := range serviceFieldNames(sec.Fields) {
				out[inner] = true
			}
			continue
		}
		out[name] = true
	}
	return out
}
