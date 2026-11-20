//go:build companion

package companiontest

// The 2026-11-20 acceptance, live: the owner's house — a goodnight script and
// a night lock-up automation that lock the front door, a hallway automation
// that reports where Anna is, a downstairs label switched off by
// homeassistant.turn_off, a hallway area an automation locks — under the
// acceptance profiles P_house and P_lights. Every verdict is read back from
// Home Assistant (REST) or from what the agent's hactl prints.
//
//   - D-207: starting something that exists is checked like writing it — the
//     goodnight script and the night lock-up automation are refused for the
//     lock they reach, the refusal names the run; an allowed script still
//     runs;
//   - D-211/D-212: labelling the front door downstairs is no reach (its
//     targeter only calls homeassistant.turn_off, which skips locks); moving
//     it into the hallway is, and the refusal names the targeter;
//   - D-208: `trace show --full` of the hallway automation masks the value
//     its template computed from Anna's latitude; an unrelated trace is
//     unchanged, and a profile denying no read sees the value.

import (
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	accLock       = "lock.pg_acc_front_door"
	accLocked     = "input_boolean.pg_acc_locked"
	accAllowedRan = "input_boolean.pg_acc_allowed_ran"
	accHall       = "light.pg_acc_hall"
	accAnna       = "person.pg_acc_anna"
	accNightAuto  = "automation.pg_acc_night_lock_up" // alias "PG Acc night lock-up"; config id pg_acc_night_lockup
	accArea       = "pg_acc_hallway"
	accLabel      = "pg_acc_downstairs"
	accWhere      = "52.37,4.89"
)

// P_house: everything except the front door lock, alarms, and where anyone is.
const accHouse = `    deny:
      "entity:lock.pg_acc_front_door": [control]
      "entity:alarm_control_panel.*": [control]
      "entity:*/attr/latitude": [read]
      "entity:*/attr/longitude": [read]
`

// P_lights: read everything but location; lights, covers, automations, scripts.
const accLights = `    default: deny
    allow:
      "*": [read]
      "entity:light.*": [control]
      "entity:cover.*": [control]
      "automation:*": [read, edit, create]
      "script:*": [read, edit, create]
      "service:automation.reload": [control]
      "service:script.reload": [control]
    deny:
      "entity:*/attr/latitude": [read]
      "entity:*/attr/longitude": [read]
`

// P_open: the front door only — a profile that denies no read.
const accOpen = `    deny:
      "entity:lock.pg_acc_front_door": [control]
`

var (
	accOnce  sync.Once
	accAdmin string
)

func accSetup(t *testing.T) string {
	t.Helper()
	accOnce.Do(func() {
		admin, err := createE2EInstanceDir(haURL, haToken, compURL, companionToken)
		if err != nil {
			t.Fatal(err)
		}
		accAdmin = admin
		tmp := t.TempDir()
		own := func(args ...string) {
			o, e, c := runPolicyE2E(t, admin, nil, append(args, "--confirm")...)
			if c != 0 {
				t.Fatalf("owner %v: exit %d %s", args, c, huntLine(o+" "+e, 400))
			}
		}
		for _, h := range []string{"pg_acc_locked", "pg_acc_allowed_ran", "pg_acc_hall_on"} {
			own("helper", "create", "input_boolean", "-f", huntWrite(t, tmp, h+".yaml", h+":\n  name: "+h+"\n"))
		}
		own("tpl", "create", "-f", huntWrite(t, tmp, "acc_tpl.yaml", `lock:
  - name: PG Acc Front Door
    unique_id: pg_acc_front_door
    state: "{{ 'locked' if is_state('input_boolean.pg_acc_locked', 'on') else 'unlocked' }}"
    lock:
      - action: input_boolean.turn_on
        target:
          entity_id: input_boolean.pg_acc_locked
    unlock:
      - action: input_boolean.turn_off
        target:
          entity_id: input_boolean.pg_acc_locked
light:
  - name: PG Acc Hall
    unique_id: pg_acc_hall
    state: "{{ is_state('input_boolean.pg_acc_hall_on', 'on') }}"
    turn_on:
      - action: input_boolean.turn_on
        target:
          entity_id: input_boolean.pg_acc_hall_on
    turn_off:
      - action: input_boolean.turn_off
        target:
          entity_id: input_boolean.pg_acc_hall_on
`))
		waitForHAState(t, accLock)
		waitForHAState(t, accHall)
		own("script", "create", "-f", huntWrite(t, tmp, "goodnight.yaml", `pg_acc_goodnight:
  alias: PG Acc Goodnight
  sequence:
    - action: light.turn_off
      target:
        entity_id: light.pg_acc_hall
    - action: lock.lock
      target:
        entity_id: lock.pg_acc_front_door
  mode: single
`))
		own("script", "create", "-f", huntWrite(t, tmp, "allowed.yaml", `pg_acc_allowed:
  alias: PG Acc Allowed
  sequence:
    - action: input_boolean.turn_on
      target:
        entity_id: input_boolean.pg_acc_allowed_ran
`))
		own("label", "create", accLabel)
		own("area", "create", "PG Acc Hallway")
		autos := map[string]string{
			"night": `id: pg_acc_night_lockup
alias: PG Acc night lock-up
triggers:
  - trigger: event
    event_type: pg_acc_never
actions:
  - action: lock.lock
    target:
      entity_id: lock.pg_acc_front_door
`,
			"hallway": `id: pg_acc_hallway_motion
alias: pg_acc_hallway_motion
triggers:
  - trigger: event
    event_type: pg_acc_hallway_fire
conditions:
  - condition: state
    entity_id: lock.pg_acc_front_door
    state: unlocked
variables:
  anna_where: "{{ state_attr('person.pg_acc_anna', 'latitude') }},{{ state_attr('person.pg_acc_anna', 'longitude') }}"
actions:
  - action: logbook.log
    data:
      name: PG Acc
      message: "Hallway: Anna at {{ anna_where }}"
mode: queued
`,
			"report": `id: pg_acc_lights_report
alias: pg_acc_lights_report
triggers:
  - trigger: event
    event_type: pg_acc_report_fire
actions:
  - action: logbook.log
    data:
      name: PG Acc
      message: "lights: {{ 40 + 2 }}"
`,
			"downstairs": `id: pg_acc_downstairs_off
alias: pg_acc_downstairs_off
triggers:
  - trigger: event
    event_type: pg_acc_never
actions:
  - action: homeassistant.turn_off
    target:
      label_id: pg_acc_downstairs
`,
			"area": `id: pg_acc_hallway_area_lock
alias: pg_acc_hallway_area_lock
triggers:
  - trigger: event
    event_type: pg_acc_never
actions:
  - action: lock.lock
    target:
      area_id: pg_acc_hallway
  - action: light.turn_off
    target:
      area_id: pg_acc_hallway
`,
		}
		for _, k := range []string{"night", "hallway", "report", "downstairs", "area"} {
			own("auto", "create", "-f", huntWrite(t, tmp, k+".yaml", autos[k]))
		}
		waitForHAState(t, "script.pg_acc_goodnight")
		waitForHAState(t, "script.pg_acc_allowed")
		waitForHAState(t, accNightAuto)
		waitForHAState(t, "automation.pg_acc_hallway_motion")
		waitForHAState(t, "automation.pg_acc_lights_report")
		waitForHAState(t, "automation.pg_acc_downstairs_off")
		waitForHAState(t, "automation.pg_acc_hallway_area_lock")
		// Anna, where she is.
		if code, b := huntHTTP(t, http.MethodPost, "/api/states/"+accAnna, map[string]any{
			"state": "home", "attributes": map[string]any{"friendly_name": "PG Acc Anna", "latitude": 52.37, "longitude": 4.89},
		}); code != http.StatusOK && code != http.StatusCreated {
			t.Fatalf("setting %s: %d %s", accAnna, code, b)
		}
	})
	if accAdmin == "" {
		t.Fatal("acceptance fixture unavailable")
	}
	return accAdmin
}

// accUnlock leaves the front door unlocked, the state every refusal keeps.
func accUnlock(t *testing.T) {
	t.Helper()
	huntSvc(t, "input_boolean", "turn_off", map[string]any{"entity_id": accLocked})
	accWait(t, accLock, "unlocked")
}

func accWait(t *testing.T, id, want string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for huntState(t, id) != want {
		if time.Now().After(deadline) {
			t.Fatalf("%s is %q, want %q", id, huntState(t, id), want)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// accRefused runs args as preview and --confirm and asserts both are refused
// with every want in the report, and that the front door stays unlocked.
func accRefused(t *testing.T, dir string, args []string, want ...string) {
	t.Helper()
	accUnlock(t)
	for _, a := range [][]string{args, append(append([]string{}, args...), "--confirm")} {
		o, e, c := runPolicyE2E(t, dir, nil, a...)
		if c != 1 || !strings.Contains(o, "policy_denied") {
			t.Errorf("hactl %v: exit %d, want a policy refusal\n%s\n%s", a, c, o, e)
			continue
		}
		for _, w := range want {
			if !strings.Contains(o, w) {
				t.Errorf("hactl %v: report misses %q:\n%s", a, w, o)
			}
		}
	}
	time.Sleep(2 * time.Second)
	if s := huntState(t, accLock); s != "unlocked" {
		t.Errorf("hactl %v: the front door is %q after the refusal", args, s)
	}
}

// TestE2EPolicyAcceptance20261120Runs (D-207): what an existing script or
// automation does is checked when the agent starts it.
func TestE2EPolicyAcceptance20261120Runs(t *testing.T) {
	accSetup(t)
	house := policyE2EDir(t, pgE2EPolicy(accHouse))
	lights := policyE2EDir(t, pgE2EPolicy(accLights))

	const lockDenied = "control entity:lock.pg_acc_front_door"
	const viaGoodnight = "run of script.pg_acc_goodnight: action at /sequence/1"
	accRefused(t, house, []string{"script", "run", "pg_acc_goodnight"}, lockDenied, viaGoodnight)
	accRefused(t, house, []string{"svc", "call", "script.pg_acc_goodnight"}, lockDenied, viaGoodnight)
	accRefused(t, house, []string{"svc", "call", "script.turn_on", "-d", `{"entity_id":"script.pg_acc_goodnight"}`}, lockDenied, viaGoodnight)
	accRefused(t, house, []string{"svc", "call", "homeassistant.turn_on", "-d", `{"entity_id":"script.pg_acc_goodnight"}`}, lockDenied, viaGoodnight)
	accRefused(t, house, []string{"svc", "call", "automation.trigger", "-d", `{"entity_id":"` + accNightAuto + `"}`},
		lockDenied, "run of "+accNightAuto+": action at /actions/0")
	// P_lights refuses them too: it allows no script run and no trigger, so
	// the start is refused by its own checks, the stored config unread.
	accRefused(t, lights, []string{"script", "run", "pg_acc_goodnight"}, "control script:pg_acc_goodnight/run — default: deny")
	accRefused(t, lights, []string{"svc", "call", "automation.trigger", "-d", `{"entity_id":"` + accNightAuto + `"}`},
		"control service:automation.trigger — default: deny")

	// An allowed script still runs.
	huntSvc(t, "input_boolean", "turn_off", map[string]any{"entity_id": accAllowedRan})
	accWait(t, accAllowedRan, "off")
	if o, e, c := runPolicyE2E(t, house, nil, "script", "run", "pg_acc_allowed", "--confirm"); c != 0 {
		t.Fatalf("script run pg_acc_allowed under P_house: exit %d\n%s\n%s", c, o, e)
	}
	accWait(t, accAllowedRan, "on")
}

// TestE2EPolicyAcceptance20261120Relabel (D-211, D-212): a registry edit
// needs control only where a targeting action can switch the entity.
func TestE2EPolicyAcceptance20261120Relabel(t *testing.T) {
	admin := accSetup(t)
	house := policyE2EDir(t, pgE2EPolicy(accHouse))
	// The owner takes the lock off the label and out of any area, before and
	// after (a cleanup cannot run hactl: the test's context is gone by then).
	reset := func() {
		runPolicyE2E(t, admin, nil, "ent", "set-label", accLock, "--remove", accLabel, "--confirm")
		runPolicyE2E(t, admin, nil, "ent", "set-area", accLock, "--clear", "--confirm")
		runPolicyE2E(t, admin, nil, "ent", "set-area", accHall, "--clear", "--confirm")
	}
	reset()
	defer reset()

	// Downstairs: only homeassistant.turn_off targets it, and that skips locks.
	o, e, c := runPolicyE2E(t, house, nil, "ent", "set-label", accLock, accLabel)
	if c != 0 || strings.Contains(o, "policy_denied") {
		t.Fatalf("ent set-label %s %s under P_house: exit %d, want the plain registry edit\n%s\n%s", accLock, accLabel, c, o, e)
	}
	if co, ce, cc := runPolicyE2E(t, house, nil, "ent", "set-label", accLock, accLabel, "--confirm"); cc != 0 {
		t.Fatalf("ent set-label --confirm: exit %d\n%s\n%s", cc, co, ce)
	}
	o, _, _ = runPolicyE2E(t, admin, nil, "ent", "show", accLock, "--json")
	if !strings.Contains(o, accLabel) {
		t.Errorf("the label did not land on %s: %s", accLock, huntLine(o, 400))
	}

	// The hallway: lock.lock targets the area — a reach, named by its targeter.
	o, e, c = runPolicyE2E(t, house, nil, "ent", "set-area", accLock, accArea)
	t.Logf("ent set-area %s %s under P_house: exit %d\n%s", accLock, accArea, c, o)
	if c != 1 || !strings.Contains(o, "control entity:"+accLock) ||
		!strings.Contains(o, "because automation.pg_acc_hallway_area_lock targets area:"+accArea) {
		t.Errorf("ent set-area %s %s under P_house: exit %d, want refused naming the targeter\n%s\n%s", accLock, accArea, c, o, e)
	}
	// A light may go there: P_house lets it be switched.
	if lo, le, lc := runPolicyE2E(t, house, nil, "ent", "set-area", accHall, accArea, "--confirm"); lc != 0 {
		t.Errorf("ent set-area %s %s under P_house: exit %d\n%s\n%s", accHall, accArea, lc, lo, le)
	}
}

// TestE2EPolicyAcceptance20261120TraceValues (D-208): a value an automation's
// template computed from a denied attribute does not show in its trace.
func TestE2EPolicyAcceptance20261120TraceValues(t *testing.T) {
	accSetup(t)
	house := policyE2EDir(t, pgE2EPolicy(accHouse))
	lights := policyE2EDir(t, pgE2EPolicy(accLights))
	open := policyE2EDir(t, pgE2EPolicy(accOpen))
	accUnlock(t)
	huntHTTP(t, http.MethodPost, "/api/events/pg_acc_hallway_fire", map[string]any{})
	huntHTTP(t, http.MethodPost, "/api/events/pg_acc_report_fire", map[string]any{})
	time.Sleep(3 * time.Second)

	// The profile that denies no read sees the value: the trace holds it
	// (Home Assistant renders "lat,long" to a list, [52.37, 4.89]).
	full := []string{"trace", "show", "pg_acc_hallway_motion", "--full"}
	o, e, c := runPolicyE2E(t, open, nil, full...)
	if c != 0 || !strings.Contains(o, "52.37") {
		t.Fatalf("hactl %v under P_open: exit %d, want the trace with 52.37 (the fixture shows nothing otherwise)\n%s\n%s", full, c, o, e)
	}
	denied := func(s string) bool { return strings.Contains(s, "<denied>") || strings.Contains(s, `\u003cdenied\u003e`) }
	for name, dir := range map[string]string{"P_house": house, "P_lights": lights} {
		for _, args := range [][]string{full, {"trace", "show", "pg_acc_hallway_motion", "--json"}, {"trace", "show", "pg_acc_hallway_motion"}} {
			ho, he, hc := runPolicyE2E(t, dir, nil, args...)
			if hc != 0 || strings.Contains(ho+he, "52.37") || strings.Contains(ho+he, "4.89") {
				t.Errorf("hactl %v under %s: exit %d, want no value of Anna's position\n%s\n%s", args, name, hc, ho, he)
			}
		}
		fo, _, _ := runPolicyE2E(t, dir, nil, full...)
		if !denied(fo) || !strings.Contains(fo, `"anna_where"`) || !strings.Contains(fo, `"service_data"`) {
			t.Errorf("hactl %v under %s: want the values <denied>, their keys kept\n%s", full, name, fo)
		}
	}
	// An unrelated trace is unchanged.
	args := []string{"trace", "show", "pg_acc_lights_report", "--full"}
	want, _, c := runPolicyE2E(t, open, nil, args...)
	if c != 0 || !strings.Contains(want, "lights: 42") {
		t.Fatalf("hactl %v under P_open: exit %d\n%s", args, c, want)
	}
	if got, ge, gc := runPolicyE2E(t, house, nil, args...); gc != 0 || got != want {
		t.Errorf("hactl %v under P_house: exit %d, the unrelated trace changed\n got %s\nwant %s\n%s", args, gc, got, want, ge)
	}
}

// TestE2EPolicyAcceptance20261120TemplateReads (D-209): a deny on every
// latitude refuses only templates that read one — not one listing the names
// of the lights that are on, which hold no latitude.
func TestE2EPolicyAcceptance20261120TemplateReads(t *testing.T) {
	accSetup(t)
	house := policyE2EDir(t, pgE2EPolicy(accHouse))
	huntSvc(t, "light", "turn_on", map[string]any{"entity_id": accHall})
	accWait(t, accHall, "on")

	lights := "{{ states.light | selectattr('state', 'eq', 'on') | map(attribute='name') | join(', ') }}"
	o, e, c := runPolicyE2E(t, house, nil, "tpl", "eval", lights)
	if c != 0 || !strings.Contains(o, "PG Acc Hall") {
		t.Errorf("tpl eval (names of the lights on) under P_house: exit %d, want the names\n%s\n%s", c, o, e)
	}
	for _, tpl := range []string{
		"{{ states.person | map(attribute='attributes.latitude') | list }}",
		"{{ state_attr('" + accAnna + "', 'latitude') }}",
		"{{ states." + accAnna + " }}",
	} {
		o, e, c := runPolicyE2E(t, house, nil, "tpl", "eval", tpl)
		if c != 1 || !strings.Contains(o+e, "policy_denied") || strings.Contains(o+e, "52.37") {
			t.Errorf("tpl eval %q under P_house: exit %d, want refused\n%s\n%s", tpl, c, o, e)
		}
	}
}
