//go:build integration

package integration

// Oracles for the policy hunt 3 fixes (D-198..D-206). Each asks Home
// Assistant what the enforcement code relies on:
//
//   - homeassistant.reload_config_entry re-creates the entry's entities in
//     their start-up state (D-200);
//   - trigger.to_state carries the triggering entity's whole state — its
//     name and attributes (D-201);
//   - a state condition with `attribute:` compares that attribute (D-202);
//   - a Jinja string literal decodes as hactl decodes it (D-205).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/hatest"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
	"gopkg.in/yaml.v3"
)

// TestOracleReloadConfigEntryResetsItsEntities: reloading the demo entry
// turns the light the owner switched off back on and locks the lock again —
// the entry's entities are re-created as they start (D-200). hactl maps the
// service.
func TestOracleReloadConfigEntryResetsItsEntities(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	demo := demoEntryID(t, inst)
	pgCallService(t, inst, "light", "turn_off", map[string]any{"entity_id": "light.ceiling_lights"})
	pgCallService(t, inst, "lock", "unlock", map[string]any{"entity_id": "lock.front_door"})
	time.Sleep(time.Second)
	if l, k := pgStateOf(t, inst, "light.ceiling_lights").State, pgStateOf(t, inst, "lock.front_door").State; l != "off" || k != "unlocked" {
		t.Fatalf("setup: light %s, lock %s", l, k)
	}
	pgCallService(t, inst, "homeassistant", "reload_config_entry", map[string]any{"entry_id": demo})
	var l, k string
	for range 30 {
		time.Sleep(time.Second)
		l, k = pgStateOf(t, inst, "light.ceiling_lights").State, pgStateOf(t, inst, "lock.front_door").State
		if l == "on" && k == "locked" {
			break
		}
	}
	if l != "on" || k != "locked" {
		t.Errorf("after reload_config_entry: light.ceiling_lights %s (want on), lock.front_door %s (want locked)", l, k)
	}
	if got := reqmap.ServiceClassOf("homeassistant.reload_config_entry"); got != reqmap.ServiceMapped {
		t.Errorf("hactl classes homeassistant.reload_config_entry %s, want mapped (D-200)", got)
	}
}

// h3oSource plants a sensor the owner's automations watch.
func h3oSource(t *testing.T, inst *hatest.Instance, id, state, color string) {
	t.Helper()
	code, body := pgHTTP(t, inst, http.MethodPost, "/api/states/"+id,
		map[string]any{"state": state, "attributes": map[string]any{"friendly_name": "Numbat Source", "color": color}})
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("set %s: http %d %s", id, code, body)
	}
}

// h3oAutomation writes an owner automation from YAML and waits for it.
func h3oAutomation(t *testing.T, inst *hatest.Instance, id, src string) any {
	t.Helper()
	var doc map[string]any
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	doc["id"], doc["alias"] = id, id
	code, body := pgHTTP(t, inst, http.MethodPost, "/api/config/automation/config/"+id, doc)
	if code != http.StatusOK {
		t.Fatalf("automation %s: http %d %s", id, code, body)
	}
	t.Cleanup(func() { pgHTTP(t, inst, http.MethodDelete, "/api/config/automation/config/"+id, nil) })
	waitForState(t, inst, "automation."+id)
	return doc
}

// h3oLogbook is every logbook message about id since start that matches re.
func h3oLogbook(t *testing.T, inst *hatest.Instance, id string, start time.Time, re *regexp.Regexp) []string {
	t.Helper()
	_, lb := pgHTTP(t, inst, http.MethodGet, "/api/logbook/"+start.UTC().Format(time.RFC3339)+"?entity="+id, nil)
	var rows []struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(lb, &rows)
	var out []string
	for _, r := range rows {
		if m := re.FindString(r.Message); m != "" {
			out = append(out, m)
		}
	}
	return out
}

func h3oReads(rs []reqmap.TemplateRead) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		out = append(out, r.Check.String())
	}
	slices.Sort(out)
	return out
}

// TestOracleTriggerToStateCarriesAttributes: a state trigger hands the
// automation the entity's whole new state — trigger.to_state.name is the
// friendly_name attribute, .attributes every attribute (D-201); hactl
// checks the template as reading exactly those.
func TestOracleTriggerToStateCarriesAttributes(t *testing.T) {
	inst, _ := getOracleHA(t)
	const src = "sensor.pg_h3o_tostate"
	h3oSource(t, inst, src, "S1", "teal")
	doc := h3oAutomation(t, inst, "pg_h3o_tostate", `
triggers:
  - trigger: state
    entity_id: `+src+`
actions:
  - action: logbook.log
    data:
      name: H3O
      entity_id: `+src+`
      message: "H3OTS-{{ trigger.to_state.name }}|{{ trigger.to_state.attributes.color }}|{{ trigger.to_state.state }}"
`)
	start := time.Now().Add(-time.Minute)
	h3oSource(t, inst, src, "S2", "teal")
	var got []string
	for range 20 {
		time.Sleep(time.Second)
		if got = h3oLogbook(t, inst, src, start, regexp.MustCompile(`H3OTS-.*`)); len(got) > 0 {
			break
		}
	}
	if !slices.Contains(got, "H3OTS-Numbat Source|teal|S2") {
		t.Errorf("logbook %q, want H3OTS-Numbat Source|teal|S2", got)
	}
	u := reqmap.CandidateUses(doc, &reqmap.LiveStates{EntityIDs: []string{src}}, nil)
	want := []string{"read entity:" + src + "/attr/color", "read entity:" + src + "/attr/friendly_name", "read entity:" + src + "/state"}
	if r := h3oReads(u.TemplateReads); fmt.Sprint(r) != fmt.Sprint(want) {
		t.Errorf("hactl reads %q, want %q", r, want)
	}
}

// TestOracleAttributeConditionComparesTheAttribute: a state condition with
// `attribute:` passes on the attribute's value, not the state's (D-202);
// hactl checks it as reading that attribute.
func TestOracleAttributeConditionComparesTheAttribute(t *testing.T) {
	inst, _ := getOracleHA(t)
	const src = "sensor.pg_h3o_attrcond"
	h3oSource(t, inst, src, "teal", "red")
	doc := h3oAutomation(t, inst, "pg_h3o_attrcond", `
triggers:
  - trigger: event
    event_type: pg_h3o_attrcond_fire
conditions:
  - condition: state
    entity_id: `+src+`
    attribute: color
    state: teal
actions:
  - action: logbook.log
    data:
      name: H3O
      entity_id: `+src+`
      message: "H3OAC-{{ trigger.event.data.n }}"
`)
	start := time.Now().Add(-time.Minute)
	pgHTTP(t, inst, http.MethodPost, "/api/events/pg_h3o_attrcond_fire", map[string]any{"n": 1}) // state teal, color red: fails
	time.Sleep(2 * time.Second)
	h3oSource(t, inst, src, "red", "teal")
	time.Sleep(time.Second)
	pgHTTP(t, inst, http.MethodPost, "/api/events/pg_h3o_attrcond_fire", map[string]any{"n": 2}) // color teal: passes
	var got []string
	for range 20 {
		time.Sleep(time.Second)
		if got = h3oLogbook(t, inst, src, start, regexp.MustCompile(`H3OAC-\d+`)); len(got) > 0 {
			break
		}
	}
	if fmt.Sprint(got) != "[H3OAC-2]" {
		t.Errorf("logbook %q, want only H3OAC-2: the condition compares the attribute", got)
	}
	u := reqmap.CandidateUses(doc, &reqmap.LiveStates{EntityIDs: []string{src}}, nil)
	if r := h3oReads(u.Compared); fmt.Sprint(r) != "[read entity:"+src+"/attr/color]" {
		t.Errorf("hactl compares %q, want the color attribute", r)
	}
}

// TestOracleJinjaStringEscapes: every string literal hactl decodes, Home
// Assistant renders to the same string (D-205). One hactl refuses is
// recorded, not asserted: refusing is fail-closed.
func TestOracleJinjaStringEscapes(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	lits := []string{
		`'gu\x61rd'`, `'guard'`, `'gu\U00000061rd'`, `'gu\141rd'`, `'a\x2cb'`, `'\1234'`, `'\0'`,
		`'a\nb\tc\rd'`, `'\a\b\f\v'`, `'\\'`, `'\''`, `"\""`, `'\q'`, `'\.'`, "'a\\\nb'", "'a\r\nb'", "'a\rb'",
		`'é'`, `'é'`, `'\x41\x42'`, `'\7777'`,
		// refused by hactl
		`'\N{LATIN SMALL LETTER A}'`, `'\ud800'`, `'\é'`, `'\x6'`, `'\U00110000'`,
	}
	for _, lit := range lits {
		code, body := oracleRender(t, inst, "{{ "+lit+" | tojson }}")
		want, err := reqmap.DecodeJinjaString(lit)
		if err != nil {
			t.Logf("hactl refuses %q (%v); Home Assistant: http %d %s", lit, err, code, huntOneLine(body, 120))
			continue
		}
		var got string
		if code != http.StatusOK || json.Unmarshal([]byte(strings.TrimSpace(body)), &got) != nil {
			t.Errorf("%q: Home Assistant http %d %q; hactl decodes it to %q", lit, code, body, want)
			continue
		}
		if got != want {
			t.Errorf("%q: Home Assistant %q, hactl %q", lit, got, want)
		}
	}
}
