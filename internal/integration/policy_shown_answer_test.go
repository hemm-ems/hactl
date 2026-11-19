//go:build integration

package integration

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// ============================================================================
// D-182 against a real Home Assistant: what a write command prints from the
// answer to its own write — a service call's changed states and its
// service_response — follows the read rules, while the write itself happens.
// HA is the oracle, asked over plain HTTP (pgHTTP), never through hactl.
// ============================================================================

// TestPolicyShownAnswerHidesADeniedChangedState — a light switched on by its
// device (the caller names no entity) is reported by HA as changed; under a
// profile that may control but not read it, `svc call --confirm` switches it
// on and prints neither its id, in prose nor under --json, and the trailer
// counts it.
func TestPolicyShownAnswerHidesADeniedChangedState(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	light := rig.Lights[0]
	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "entity:%s": [read]
`, pgProfile, pgProfile, light)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
	data := fmt.Sprintf(`{"device_id":%q}`, rig.DevOf[light])

	for _, asJSON := range []bool{false, true} {
		if status, body := pgHTTP(t, inst, http.MethodPost, "/api/services/light/turn_off", map[string]any{"entity_id": light}); status != http.StatusOK {
			t.Fatalf("oracle: turning %s off: %d %s", light, status, body)
		}
		args := []string{"svc", "call", "light.turn_on", "--data", data}
		if asJSON {
			args = append(args, "--json")
		}
		assertPolicyAllowedPreview(t, dir, "svc", "call", "light.turn_on", "--data", data)
		stdout, stderr, err := pgReads(t, dir, append(args, "--confirm")...)
		if err != nil {
			t.Fatalf("%v --confirm: %v\n%s%s", args, err, stdout, stderr)
		}
		if strings.Contains(stdout+stderr, light) {
			t.Errorf("%v --confirm prints the read-denied %s:\n%s%s", args, light, stdout, stderr)
		}
		trailer := pgLastLine(stdout)
		if asJSON {
			trailer = pgLastLine(stderr)
			var doc map[string]any
			if jerr := json.Unmarshal([]byte(stdout), &doc); jerr != nil {
				t.Errorf("--json output is no longer one document: %v\n%s", jerr, stdout)
			}
		}
		if !strings.HasPrefix(trailer, "hidden by policy: 1") {
			t.Errorf("%v --confirm: no trailer counting the hidden state (last line %q)", args, trailer)
		}
		if st := pgStateOf(t, inst, light).State; st != "on" {
			t.Errorf("oracle: %s is %q after the call, want on — the write did not happen", light, st)
		}
	}
}

// TestPolicyShownAnswerDropsADeniedServiceResponse — weather.get_forecasts
// answers per entity. The oracle first shows the shape HA answers with
// return_response (changed_states beside service_response, keyed by entity
// id); then `svc call --return --confirm` on every weather entity, under a
// profile that may not read one of them, prints the others' forecasts and
// not that one.
func TestPolicyShownAnswerDropsADeniedServiceResponse(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	status, body := pgHTTP(t, inst, http.MethodGet, "/api/states", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/states: %d", status)
	}
	var states []struct {
		EntityID string `json:"entity_id"`
	}
	if err := json.Unmarshal(body, &states); err != nil {
		t.Fatal(err)
	}
	var weather []string
	for _, s := range states {
		if strings.HasPrefix(s.EntityID, "weather.") {
			weather = append(weather, s.EntityID)
		}
	}
	sort.Strings(weather)
	if len(weather) < 2 {
		t.Fatalf("the demo instance has %d weather entities, need 2 — the case would prove nothing", len(weather))
	}
	denied, shown := weather[0], weather[1]

	// Oracle: the shape of a return_response answer.
	status, body = pgHTTP(t, inst, http.MethodPost, "/api/services/weather/get_forecasts?return_response=true",
		map[string]any{"entity_id": weather, "type": "daily"})
	if status != http.StatusOK {
		t.Fatalf("oracle: get_forecasts: %d %s", status, body)
	}
	var shape struct {
		ChangedStates   []any                      `json:"changed_states"`
		ServiceResponse map[string]json.RawMessage `json:"service_response"`
	}
	if err := json.Unmarshal(body, &shape); err != nil || shape.ChangedStates == nil {
		t.Fatalf("oracle: return_response answer is not {changed_states, service_response}: %v\n%s", err, body)
	}
	if _, ok := shape.ServiceResponse[denied]; !ok {
		t.Fatalf("oracle: service_response is not keyed by entity id:\n%s", body)
	}

	policy := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "entity:%s": [read]
`, pgProfile, pgProfile, denied)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
	args := []string{"svc", "call", "weather.get_forecasts", "--data", `{"entity_id":"all","type":"daily"}`, "--return"}
	assertPolicyAllowedPreview(t, dir, args...)
	stdout, stderr, err := pgReads(t, dir, append(args, "--confirm")...)
	if err != nil {
		t.Fatalf("--return --confirm: %v\n%s%s", err, stdout, stderr)
	}
	if strings.Contains(stdout+stderr, denied) {
		t.Errorf("--return prints the read-denied %s:\n%s", denied, stdout)
	}
	if !strings.Contains(stdout, shown) {
		t.Errorf("--return lost the readable %s:\n%s", shown, stdout)
	}
	if !strings.HasPrefix(pgLastLine(stdout), "hidden by policy: ") {
		t.Errorf("no trailer counting the dropped response:\n%s", stdout)
	}
}
