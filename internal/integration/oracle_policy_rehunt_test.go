//go:build integration

package integration

// Oracles for the 2026-11-20 re-hunt fixes (D-188..D-195). Each test asks the
// Home Assistant the rig boots what the enforcement code then relies on, so a
// Home Assistant that changes its mind fails here first:
//
//   - the template constructs hactl calls safe cannot read a state (D-188);
//   - every template name hactl classifies exists in Home Assistant as that
//     kind of name (D-188);
//   - which reload removes which runtime-created entity (D-193);
//   - which entity homeassistant.set_location writes (D-191);
//   - every service field that may name an entity is one hactl refuses a
//     template it cannot fold in (D-189).
//
// Home Assistant's entity-glob semantics are recorded from its own
// entityfilter in testdata/policy/services/entity_globs.json (D-192).

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/hatest"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

const (
	oracleSecretID    = "sensor.pg_oracle_secret" //nolint:gosec // G101: an entity id the oracle plants, not a credential
	oracleSecretState = "QuokkaState42"           //nolint:gosec // G101: a marker state, not a credential
	oracleSecretName  = "Wombat Marker"
)

func oracleRender(t *testing.T, inst *hatest.Instance, tpl string) (int, string) {
	t.Helper()
	code, body := pgHTTP(t, inst, http.MethodPost, "/api/template", map[string]any{"template": tpl})
	return code, string(body)
}

// TestOracleTemplateNamesExist: every global, filter and test hactl
// classifies — safe or not — is one Home Assistant defines under that kind.
// A name hactl calls safe that Home Assistant does not have would be a guess;
// one HA defines and hactl does not classify is unsafe by default (fail
// closed), so this direction is the one to prove.
func TestOracleTemplateNamesExist(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	table := reqmap.TemplateNameTable()
	if len(table["global"]) == 0 || len(table["filter"]) == 0 || len(table["test"]) == 0 {
		t.Fatalf("template name table is empty: %v", table)
	}
	for _, kind := range []string{"global", "filter", "test"} {
		names := make([]string, 0, len(table[kind]))
		for n := range table[kind] {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			var probe string
			switch kind {
			case "global":
				probe = fmt.Sprintf("{{ %s is defined }}", n)
			case "filter":
				probe = fmt.Sprintf("{{ %q is filter }}", n)
			case "test":
				probe = fmt.Sprintf("{{ %q is test }}", n)
			}
			code, out := oracleRender(t, inst, probe)
			if code != http.StatusOK || strings.TrimSpace(out) != "True" {
				t.Errorf("hactl classifies %s %q (safe=%v), but Home Assistant answers %q (http %d) to %s",
					kind, n, table[kind][n], out, code, probe)
			}
		}
	}
}

// TestOracleSafeTemplateConstructsReadNoState: a template made only of the
// constructs hactl calls safe, handed an entity id, never renders the
// entity's state or attributes; the unsafe controls do. Errors are fine — an
// error reads nothing.
func TestOracleSafeTemplateConstructsReadNoState(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	code, body := pgHTTP(t, inst, http.MethodPost, "/api/states/"+oracleSecretID,
		map[string]any{"state": oracleSecretState, "attributes": map[string]any{"friendly_name": oracleSecretName}})
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("setting %s: http %d %s", oracleSecretID, code, body)
	}
	leaks := func(out string) bool {
		return strings.Contains(out, "Quokka") || strings.Contains(out, "Wombat")
	}
	// The controls: each unsafe construct does read it — or the probe could
	// not tell a safe construct from a broken one.
	for _, tpl := range []string{
		"{{ states('" + oracleSecretID + "') }}",
		"{{ state_attr('" + oracleSecretID + "', 'friendly_name') }}",
		"{{ '" + oracleSecretID + "' | states }}",
		"{{ states.sensor.pg_oracle_secret.state }}",
		"{{ expand('" + oracleSecretID + "') | map(attribute='name') | join }}",
		"{{ ['" + oracleSecretID + "'] | select('is_state', '" + oracleSecretState + "') | map('state_attr', 'friendly_name') | join }}",
	} {
		if _, out := oracleRender(t, inst, tpl); !leaks(out) {
			t.Errorf("control %s did not read the marker state (got %q): the probe cannot tell", tpl, out)
		}
	}
	table := reqmap.TemplateNameTable()
	var probes []string
	id := "'" + oracleSecretID + "'"
	for n, safe := range table["global"] {
		if !safe {
			continue
		}
		probes = append(probes, fmt.Sprintf("{{ %s(%s) }}", n, id), fmt.Sprintf("{{ %s(%s, %s) }}", n, id, id), fmt.Sprintf("{{ %s }}", n))
	}
	for n, safe := range table["filter"] {
		if !safe {
			continue
		}
		probes = append(probes, fmt.Sprintf("{{ %s | %s }}", id, n), fmt.Sprintf("{{ [%s] | %s }}", id, n),
			fmt.Sprintf("{{ %s | %s(%s) }}", id, n, id), fmt.Sprintf("{{ {%s: 1} | %s }}", id, n))
	}
	for n, safe := range table["test"] {
		if !safe {
			continue
		}
		probes = append(probes, fmt.Sprintf("{{ %s is %s }}", id, n), fmt.Sprintf("{{ [%s] | select(%q) | list }}", id, n))
	}
	sort.Strings(probes)
	for _, tpl := range probes {
		if _, out := oracleRender(t, inst, tpl); leaks(out) {
			t.Errorf("safe template %s rendered the marker entity's state: %q", tpl, out)
		}
	}
	t.Logf("%d safe-construct probes rendered no state", len(probes))
}

func oracleStatus(t *testing.T, inst *hatest.Instance, id string) int {
	t.Helper()
	code, _ := pgHTTP(t, inst, http.MethodGet, "/api/states/"+id, nil)
	return code
}

func oracleRuntimeEntities(t *testing.T, inst *hatest.Instance) {
	t.Helper()
	pgCallService(t, inst, "group", "set", map[string]any{"object_id": "pg_oracle_rt", "entities": []string{"light.bed_light"}})
	pgCallService(t, inst, "scene", "create", map[string]any{"scene_id": "pg_oracle_rt", "entities": map[string]any{"light.bed_light": "on"}})
	time.Sleep(time.Second)
	if oracleStatus(t, inst, "group.pg_oracle_rt") != http.StatusOK || oracleStatus(t, inst, "scene.pg_oracle_rt") != http.StatusOK {
		t.Fatal("runtime group/scene not created")
	}
}

// TestOracleReloadsRemoveRuntimeEntities: which service removes the group
// group.set made and the scene scene.create made (D-193). scene.reload and
// homeassistant.reload_all remove the scene; no reload removes the group
// (observed on HA 2026.11.2: group.reload keeps it); the other domains'
// reloads remove neither.
func TestOracleReloadsRemoveRuntimeEntities(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	cases := []struct {
		svc          string
		group, scene bool // removed
	}{
		{"input_boolean.reload", false, false},
		{"automation.reload", false, false},
		{"template.reload", false, false},
		{"group.reload", false, false},
		{"scene.reload", false, true},
		{"homeassistant.reload_all", false, true},
	}
	for _, c := range cases {
		oracleRuntimeEntities(t, inst)
		d, s, _ := strings.Cut(c.svc, ".")
		pgCallService(t, inst, d, s, map[string]any{})
		time.Sleep(3 * time.Second)
		g := oracleStatus(t, inst, "group.pg_oracle_rt") == http.StatusNotFound
		sc := oracleStatus(t, inst, "scene.pg_oracle_rt") == http.StatusNotFound
		if g != c.group || sc != c.scene {
			t.Errorf("%s: runtime group removed=%v (want %v), runtime scene removed=%v (want %v)", c.svc, g, c.group, sc, c.scene)
		}
		if reqmap.ReloadRemovesRuntime(d+"."+s) != (c.group || c.scene) {
			t.Errorf("%s: hactl's reload table says removes=%v, Home Assistant removed group=%v scene=%v",
				c.svc, reqmap.ReloadRemovesRuntime(d+"."+s), g, sc)
		}
	}
}

// TestOracleRestartRemovesRuntimeEntities: a restart forgets what group.set
// and scene.create made — nothing stores them. A dedicated instance, so the
// restart disturbs no other test.
func TestOracleRestartRemovesRuntimeEntities(t *testing.T) {
	inst := hatest.Start(t, hatest.WithFixture("policyenforce"))
	oracleRuntimeEntities(t, inst)
	code, body := pgHTTP(t, inst, http.MethodPost, "/api/services/homeassistant/restart", map[string]any{})
	t.Logf("restart: http %d %s", code, huntOneLine(string(body), 120))
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	time.Sleep(10 * time.Second)
	for {
		if ctx.Err() != nil {
			t.Fatal("Home Assistant did not come back after the restart")
		}
		c, b := pgHTTP(t, inst, http.MethodGet, "/api/config", nil)
		if c == http.StatusOK && strings.Contains(string(b), `"state":"RUNNING"`) {
			break
		}
		time.Sleep(3 * time.Second)
	}
	time.Sleep(3 * time.Second)
	if oracleStatus(t, inst, "group.pg_oracle_rt") != http.StatusNotFound || oracleStatus(t, inst, "scene.pg_oracle_rt") != http.StatusNotFound {
		t.Errorf("after homeassistant.restart the runtime group is %d and the runtime scene %d; hactl maps restart as removing both",
			oracleStatus(t, inst, "group.pg_oracle_rt"), oracleStatus(t, inst, "scene.pg_oracle_rt"))
	}
	for _, svc := range []string{"homeassistant.restart", "homeassistant.stop"} {
		if !reqmap.ReloadRemovesRuntime(svc) {
			t.Errorf("hactl's reload table does not map %s as removing runtime entities", svc)
		}
	}
}

// TestOracleSetLocationWritesZoneHome: homeassistant.set_location moves
// zone.home — the one entity it writes, named by no field (D-191).
func TestOracleSetLocationWritesZoneHome(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	before := pgStateOf(t, inst, "zone.home").Attributes
	pgCallService(t, inst, "homeassistant", "set_location", map[string]any{"latitude": 11.25, "longitude": 22.5})
	time.Sleep(1500 * time.Millisecond)
	after := pgStateOf(t, inst, "zone.home").Attributes
	t.Cleanup(func() {
		pgCallService(t, inst, "homeassistant", "set_location", map[string]any{"latitude": before["latitude"], "longitude": before["longitude"]})
	})
	if fmt.Sprint(after["latitude"]) != "11.25" || fmt.Sprint(after["longitude"]) != "22.5" {
		t.Errorf("set_location did not move zone.home: before %v/%v, after %v/%v", before["latitude"], before["longitude"], after["latitude"], after["longitude"])
	}
	if got := reqmap.FixedEntitiesOf("homeassistant.set_location"); len(got) != 1 || got[0] != "zone.home" {
		t.Errorf("hactl maps homeassistant.set_location to %v, Home Assistant moves zone.home", got)
	}
}

// TestOracleTemplateFieldsAreDispositioned: every field of every service the
// rig's Home Assistant offers whose selector picks entities (or what resolves
// to them) is one hactl refuses an unfoldable template in, and every
// free-form `object` field is that or exempt with a reason (D-189). A new
// Home Assistant field that may name an entity fails here before an agent can
// template an entity id into it.
func TestOracleTemplateFieldsAreDispositioned(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	code, body := pgHTTP(t, inst, http.MethodGet, "/api/services", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/services: http %d", code)
	}
	var doms []struct {
		Domain   string `json:"domain"`
		Services map[string]struct {
			Fields map[string]json.RawMessage `json:"fields"`
		} `json:"services"`
	}
	if err := json.Unmarshal(body, &doms); err != nil {
		t.Fatal(err)
	}
	naming := map[string]bool{"entity": true, "statistic": true, "area": true, "device": true, "floor": true, "label": true, "target": true}
	seen := 0
	var walk func(svc string, fields map[string]json.RawMessage)
	walk = func(svc string, fields map[string]json.RawMessage) {
		for name, raw := range fields {
			var fd struct {
				Selector map[string]any             `json:"selector"`
				Fields   map[string]json.RawMessage `json:"fields"`
			}
			_ = json.Unmarshal(raw, &fd)
			if fd.Fields != nil {
				walk(svc, fd.Fields)
			}
			for kind := range fd.Selector {
				switch {
				case naming[kind]:
					seen++
					if !reqmap.EntityNamingField(svc, name) {
						t.Errorf("%s field %q has a %s selector, but hactl does not treat it as naming entities", svc, name, kind)
					}
				case kind == "object":
					seen++
					if !reqmap.EntityNamingField(svc, name) && reqmap.TemplateFieldExemption(svc, name) == "" {
						t.Errorf("%s field %q is free-form data (object selector), neither naming entities nor exempt", svc, name)
					}
				}
			}
		}
	}
	for _, d := range doms {
		for s, sv := range d.Services {
			walk(d.Domain+"."+s, sv.Fields)
		}
	}
	if seen < 30 {
		t.Fatalf("only %d entity or object fields seen: the probe cannot tell", seen)
	}
}
