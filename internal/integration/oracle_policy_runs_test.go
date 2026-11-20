//go:build integration

package integration

// Oracles for the 2026-11-20 acceptance fixes. Each asks the Home Assistant
// the policyrun fixture boots what the enforcement code then relies on:
//
//   - D-207: a start runs what the stored config says — script.turn_on and a
//     per-script service run the script, a script step that starts another
//     script runs that one too (both forms), homeassistant.turn_on on a script
//     entity runs it, and automation.trigger runs the actions without the
//     conditions unless skip_condition is false;
//   - D-211: homeassistant.turn_on|turn_off|toggle reach only the entities of
//     a domain that has the service of that name — Home Assistant skips every
//     other domain, a group's members included — and `entity_id: all` is
//     every entity of such a domain.

import (
	"encoding/json"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/hatest"
)

var (
	policyRunOnce sync.Once
	policyRunHA   *hatest.Instance
)

// getPolicyRunHA is the policyrun fixture's instance, shared by this file's
// oracles (each restores what it changes before it asserts).
func getPolicyRunHA(t *testing.T) *hatest.Instance {
	t.Helper()
	policyRunOnce.Do(func() {
		opts := []hatest.Option{hatest.WithFixture("policyrun")}
		if img := os.Getenv("HACTL_HA_IMAGE"); img != "" {
			opts = append(opts, hatest.WithImage(img))
		}
		policyRunHA = hatest.StartShared(t, opts...)
		waitForRunning(t, policyRunHA)
	})
	if policyRunHA == nil {
		t.Fatal("policyrun HA instance unavailable")
	}
	return policyRunHA
}

// runOracleWait polls entityID until its state is want, or fails.
func runOracleWait(t *testing.T, inst *hatest.Instance, entityID, want, why string) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		if got := pgStateOf(t, inst, entityID).State; got == want {
			return
		} else if time.Now().After(deadline) {
			t.Fatalf("%s: %s is %q, want %q", why, entityID, got, want)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// runOracleStays asserts entityID is still want a while after a call.
func runOracleStays(t *testing.T, inst *hatest.Instance, entityID, want, why string) {
	t.Helper()
	time.Sleep(2 * time.Second)
	if got := pgStateOf(t, inst, entityID).State; got != want {
		t.Errorf("%s: %s is %q, want it to stay %q", why, entityID, got, want)
	}
}

func runOracleReset(t *testing.T, inst *hatest.Instance) {
	t.Helper()
	for _, b := range []string{"outer_ran", "inner_ran", "inner2_ran", "auto_ran", "gate"} {
		pgCallService(t, inst, "input_boolean", "turn_off", map[string]any{"entity_id": "input_boolean.pg_run_" + b})
		runOracleWait(t, inst, "input_boolean.pg_run_"+b, "off", "reset")
	}
}

// TestOracleRunsStartWhatTheyName (D-207): every start hactl checks against
// the stored config runs exactly that config, a nested start included.
func TestOracleRunsStartWhatTheyName(t *testing.T) {
	inst := getPolicyRunHA(t)

	runOracleReset(t, inst)
	pgCallService(t, inst, "script", "turn_on", map[string]any{"entity_id": "script.pg_run_outer"})
	runOracleWait(t, inst, "input_boolean.pg_run_outer_ran", "on", "script.turn_on runs the script")
	runOracleWait(t, inst, "input_boolean.pg_run_inner_ran", "on", "a step script.<x> starts that script")

	runOracleReset(t, inst)
	pgCallService(t, inst, "script", "pg_run_via_turn_on", map[string]any{})
	runOracleWait(t, inst, "input_boolean.pg_run_inner2_ran", "on", "a step script.turn_on starts the script it targets")

	runOracleReset(t, inst)
	pgCallService(t, inst, "homeassistant", "turn_on", map[string]any{"entity_id": "script.pg_run_inner"})
	runOracleWait(t, inst, "input_boolean.pg_run_inner_ran", "on", "homeassistant.turn_on on a script entity runs it")

	runOracleReset(t, inst)
	pgCallService(t, inst, "automation", "trigger", map[string]any{"entity_id": "automation.pg_run_gated"})
	runOracleWait(t, inst, "input_boolean.pg_run_auto_ran", "on", "automation.trigger runs the actions past a failing condition")

	runOracleReset(t, inst)
	pgCallService(t, inst, "automation", "trigger", map[string]any{"entity_id": "automation.pg_run_gated", "skip_condition": false})
	runOracleStays(t, inst, "input_boolean.pg_run_auto_ran", "off", "automation.trigger with skip_condition: false checks the conditions")

	runOracleReset(t, inst)
	pgCallService(t, inst, "input_boolean", "turn_on", map[string]any{"entity_id": "input_boolean.pg_run_gate"})
	pgCallService(t, inst, "automation", "trigger", map[string]any{"entity_id": "automation.pg_run_gated", "skip_condition": false})
	runOracleWait(t, inst, "input_boolean.pg_run_auto_ran", "on", "automation.trigger with skip_condition: false runs the actions when the conditions hold")
	runOracleReset(t, inst)
}

// runOracleServices is GET /api/services as domain → service names.
func runOracleServices(t *testing.T, inst *hatest.Instance) map[string]map[string]bool {
	t.Helper()
	code, body := pgHTTP(t, inst, http.MethodGet, "/api/services", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/services: %d %s", code, body)
	}
	var list []struct {
		Domain   string                     `json:"domain"`
		Services map[string]json.RawMessage `json:"services"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]map[string]bool{}
	for _, d := range list {
		out[d.Domain] = map[string]bool{}
		for s := range d.Services {
			out[d.Domain][s] = true
		}
	}
	return out
}

// runOracleStates is every live state, entity id → state.
func runOracleStates(t *testing.T, inst *hatest.Instance) map[string]string {
	t.Helper()
	code, body := pgHTTP(t, inst, http.MethodGet, "/api/states", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/states: %d %s", code, body)
	}
	var list []struct {
		EntityID string `json:"entity_id"`
		State    string `json:"state"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, s := range list {
		out[s.EntityID] = s.State
	}
	return out
}

// runOracleDomains are the demo domains the reach oracle calls into: some
// with a turn_off service, some without. media_player is left out: a demo
// player that does not support turn_off makes Home Assistant refuse the whole
// call (400 "does not support action media_player.turn_off", observed on HA
// 2026.11) — an entity's own features narrow the reach further, which hactl
// does not model (a superset).
var runOracleDomains = []string{"light", "switch", "fan", "climate", "cover", "lock", "alarm_control_panel"}

// TestOracleHomeassistantTurnOffReach (D-211): homeassistant.turn_off on
// entities of many domains changes only those of a domain that has
// <domain>.turn_off; a lock, an alarm panel and a cover keep their state.
func TestOracleHomeassistantTurnOffReach(t *testing.T) {
	inst := getPolicyRunHA(t)
	services := runOracleServices(t, inst)
	// Each domain with turn_off needs one entity that is on, or its row
	// shows nothing: the demo fans start off.
	pgCallService(t, inst, "fan", "turn_on", map[string]any{"entity_id": "fan.living_room_fan"})
	runOracleWait(t, inst, "fan.living_room_fan", "on", "setup")
	before := runOracleStates(t, inst)

	var ids []string
	for id := range before {
		d, _, _ := strings.Cut(id, ".")
		for _, want := range runOracleDomains {
			if d == want {
				ids = append(ids, id)
			}
		}
	}
	sort.Strings(ids)
	pgCallService(t, inst, "homeassistant", "turn_off", map[string]any{"entity_id": ids})
	time.Sleep(4 * time.Second)
	after := runOracleStates(t, inst)

	changed := map[string]bool{}
	for _, id := range ids {
		d, _, _ := strings.Cut(id, ".")
		t.Logf("%-45s has %s.turn_off=%v  %q -> %q", id, d, services[d]["turn_off"], before[id], after[id])
		if before[id] != after[id] {
			changed[d] = true
			if !services[d]["turn_off"] {
				t.Errorf("%s changed (%q -> %q) although Home Assistant has no %s.turn_off", id, before[id], after[id], d)
			}
		}
	}
	for _, d := range runOracleDomains {
		if services[d]["turn_off"] && !changed[d] {
			t.Errorf("no %s entity changed although %s.turn_off exists — the oracle shows nothing for this domain", d, d)
		}
	}
	for _, d := range []string{"cover", "lock", "alarm_control_panel"} {
		if services[d]["turn_off"] {
			t.Errorf("Home Assistant now has %s.turn_off: the affected-domain set hactl relies on changed", d)
		}
	}
}

// TestOracleHomeassistantGroupAndAll (D-211): a group's members are filtered
// the same way (a lock in a group keeps its state), and `entity_id: all` is
// refused by Home Assistant (400, observed on HA 2026.11) and changes nothing
// — hactl still maps it as every live entity of an affected domain, a
// superset.
func TestOracleHomeassistantGroupAndAll(t *testing.T) {
	inst := getPolicyRunHA(t)

	pgCallService(t, inst, "light", "turn_on", map[string]any{"entity_id": "light.kitchen_lights"})
	pgCallService(t, inst, "lock", "unlock", map[string]any{"entity_id": "lock.kitchen_door"})
	runOracleWait(t, inst, "light.kitchen_lights", "on", "setup")
	runOracleWait(t, inst, "lock.kitchen_door", "unlocked", "setup")
	pgCallService(t, inst, "homeassistant", "turn_off", map[string]any{"entity_id": "group.pg_run_mixed"})
	runOracleWait(t, inst, "light.kitchen_lights", "off", "homeassistant.turn_off on a group turns off its light")
	runOracleStays(t, inst, "lock.kitchen_door", "unlocked", "homeassistant.turn_off on a group leaves its lock")

	pgCallService(t, inst, "homeassistant", "toggle", map[string]any{"entity_id": "lock.kitchen_door"})
	runOracleStays(t, inst, "lock.kitchen_door", "unlocked", "homeassistant.toggle leaves a lock")

	pgCallService(t, inst, "light", "turn_on", map[string]any{"entity_id": "light.kitchen_lights"})
	runOracleWait(t, inst, "light.kitchen_lights", "on", "setup")
	code, body := pgHTTP(t, inst, http.MethodPost, "/api/services/homeassistant/turn_off", map[string]any{"entity_id": "all"})
	t.Logf("homeassistant.turn_off entity_id all: %d %s", code, strings.TrimSpace(string(body))[:min(200, len(strings.TrimSpace(string(body))))])
	if code != http.StatusBadRequest {
		t.Errorf("homeassistant.turn_off all: %d, want Home Assistant's 400", code)
	}
	runOracleStays(t, inst, "light.kitchen_lights", "on", "homeassistant.turn_off all is refused, the light stays on")
	runOracleStays(t, inst, "lock.kitchen_door", "unlocked", "homeassistant.turn_off all leaves a lock")
}
