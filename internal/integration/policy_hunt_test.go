//go:build integration

package integration

// The policy hunt of 2026-11-19, kept as regression tests (D-184, D-187).
// Written blind to the enforcement code: every expectation comes from
// docs/policy.md and the manual's policy reference; every verdict is read back
// from Home Assistant over raw HTTP/WS (pgHTTP, dialRawWS), never through
// hactl. A "BREACH" error is a finding the hunt made: F1 (group.set,
// group.remove, scene.create by object id), F2 (conversation.process), F3
// (recorder.purge_entities by domain).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

func huntRun(t *testing.T, dir string, args ...string) (string, int) {
	t.Helper()
	out, err := runHactlDirErr(t, dir, args...)
	if err != nil {
		out += "\n[err] " + err.Error()
	}
	return out, pgExitCode(err)
}

func huntPolicy(body string) string {
	return "version: 1\nactive: agent\nprofiles:\n  agent:\n" + body
}

func huntOneLine(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " | ")
	if len(s) > n {
		s = s[:n]
	}
	return s
}

// huntSetLight switches the ceiling light, the hunt's allowed light.
func huntSetLight(t *testing.T, svc string) {
	t.Helper()
	pgCallService(t, getPolicyEnforceHA(t), "light", svc, map[string]any{"entity_id": "light.ceiling_lights"})
	time.Sleep(700 * time.Millisecond)
}

func huntRegistryUUID(t *testing.T, ws *rawWS, entityID string) string {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{"type": "config/entity_registry/list"})
	var rows []struct {
		ID       string `json:"id"`
		EntityID string `json:"entity_id"`
	}
	if err := json.Unmarshal(env.Result, &rows); err != nil {
		t.Fatal(err)
	}
	for _, r := range rows {
		if r.EntityID == entityID {
			return r.ID
		}
	}
	t.Fatalf("no registry row for %s", entityID)
	return ""
}

// ---------------------------------------------------------------------------
// svc call: target spellings Home Assistant accepts but a policy may not read.
// Profile: default deny; lights switchable except the ceiling lights.
// ---------------------------------------------------------------------------

const huntLightsProfile = `    default: deny
    allow:
      "entity:*": [read]
      "entity:light.*": [control]
      "entity:scene.*": [control]
      "service:light.*": [control]
      "service:homeassistant.turn_on": [control]
      "service:scene.*": [control]
      "service:group.*": [control]
    deny:
      "entity:light.ceiling_lights": [read, write]
      "entity:lock.*": [read, write]
      "entity:group.pg_hunt_secure": [write]
      "entity:scene.pg_hunt_secure": [write]
`

func TestPolicyHuntSvcTargetSpellings(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), huntPolicy(huntLightsProfile))
	ws := dialRawWS(t, inst.URL(), inst.Token())
	const ceiling = "light.ceiling_lights"
	uuid := huntRegistryUUID(t, ws, ceiling)

	// Oracle first: does HA itself accept a registry uuid as entity_id?
	huntSetLight(t, "turn_off")
	code, body := pgHTTP(t, inst, http.MethodPost, "/api/services/light/turn_on", map[string]any{"entity_id": uuid})
	time.Sleep(700 * time.Millisecond)
	t.Logf("ORACLE light.turn_on entity_id=<uuid>: http %d %s → ceiling now %s", code, huntOneLine(string(body), 120), pgStateOf(t, inst, ceiling).State)
	code, _ = pgHTTP(t, inst, http.MethodPost, "/api/services/light/turn_off", map[string]any{"entity_id": "light.bed_light, " + ceiling})
	time.Sleep(700 * time.Millisecond)
	t.Logf("ORACLE light.turn_off comma string: http %d → ceiling now %s", code, pgStateOf(t, inst, ceiling).State)

	cases := []struct {
		name string
		args []string
	}{
		{"baseline lowercase", []string{"svc", "call", "light.turn_on", "--data", `{"entity_id":"light.ceiling_lights"}`}},
		{"comma string", []string{"svc", "call", "light.turn_on", "--data", `{"entity_id":"light.bed_light, light.ceiling_lights"}`}},
		{"comma no space", []string{"svc", "call", "light.turn_on", "--data", `{"entity_id":"light.bed_light,light.ceiling_lights"}`}},
		{"uppercase", []string{"svc", "call", "light.turn_on", "--data", `{"entity_id":"LIGHT.CEILING_LIGHTS"}`}},
		{"registry uuid", []string{"svc", "call", "light.turn_on", "--data", `{"entity_id":"` + uuid + `"}`}},
		{"registry uuid list", []string{"svc", "call", "light.turn_on", "--data", `{"entity_id":["light.bed_light","` + uuid + `"]}`}},
		{"ha.turn_on uuid", []string{"svc", "call", "homeassistant.turn_on", "--data", `{"entity_id":"` + uuid + `"}`}},
		{"scene.apply uppercase key", []string{"svc", "call", "scene.apply", "--data", `{"entities":{"LIGHT.CEILING_LIGHTS":"on"}}`}},
		{"scene.apply lowercase key", []string{"svc", "call", "scene.apply", "--data", `{"entities":{"light.ceiling_lights":"on"}}`}},
	}
	for _, c := range cases {
		huntSetLight(t, "turn_off")
		before := pgStateOf(t, inst, ceiling).State
		pre, pcode := huntRun(t, dir, c.args...)
		out, ocode := huntRun(t, dir, append(append([]string{}, c.args...), "--confirm")...)
		time.Sleep(900 * time.Millisecond)
		after := pgStateOf(t, inst, ceiling).State
		verdict := "held"
		if before != after {
			verdict = "BREACH"
			t.Errorf("%s: denied %s went %s → %s through hactl", c.name, ceiling, before, after)
		}
		t.Logf("RESULT %-26s %s  preview exit=%d confirm exit=%d  ceiling %s→%s\n  preview: %s\n  confirm: %s",
			c.name, verdict, pcode, ocode, before, after, huntOneLine(pre, 300), huntOneLine(out, 300))
	}
}

// group.set / group.remove / scene.create name their target by object_id or
// scene_id — not an entity id. Profile allows the group and scene services
// and denies writing one group and one scene.
func TestPolicyHuntGroupAndSceneByObjectID(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), huntPolicy(huntLightsProfile))

	// Owner setup: a group and a dynamic scene the profile denies writing.
	pgCallService(t, inst, "group", "set", map[string]any{"object_id": "pg_hunt_secure", "name": "PG Hunt Secure", "entities": []string{"lock.front_door", "lock.kitchen_door"}})
	pgCallService(t, inst, "scene", "create", map[string]any{"scene_id": "pg_hunt_secure", "entities": map[string]any{"lock.front_door": "locked"}})
	time.Sleep(time.Second)
	g0 := pgStateOf(t, inst, "group.pg_hunt_secure")
	s0 := pgStateOf(t, inst, "scene.pg_hunt_secure")
	t.Logf("setup: group members %v, scene members %v", g0.Attributes["entity_id"], s0.Attributes["entity_id"])

	// policy check confirms the profile denies writing both.
	o, c := huntRun(t, dir, "policy", "check", "entity:group.pg_hunt_secure", "write")
	t.Logf("policy check group write: exit=%d %s", c, huntOneLine(o, 200))

	steps := []struct {
		name string
		args []string
	}{
		{"group.set rewrites denied group", []string{"svc", "call", "group.set", "--data", `{"object_id":"pg_hunt_secure","entities":["light.bed_light"]}`}},
		{"scene.create overwrites denied scene", []string{"svc", "call", "scene.create", "--data", `{"scene_id":"pg_hunt_secure","entities":{"light.bed_light":"on"}}`}},
	}
	for _, s := range steps {
		pre, pc := huntRun(t, dir, s.args...)
		out, oc := huntRun(t, dir, append(append([]string{}, s.args...), "--confirm")...)
		t.Logf("RESULT %s: preview exit=%d confirm exit=%d\n  preview: %s\n  confirm: %s", s.name, pc, oc, huntOneLine(pre, 300), huntOneLine(out, 300))
		if pc == 0 || oc == 0 || !strings.Contains(out, "control entity:") {
			t.Errorf("%s: preview exit=%d confirm exit=%d, want both refused naming the group or scene (D-184)", s.name, pc, oc)
		}
	}
	time.Sleep(time.Second)
	g1 := pgStateOf(t, inst, "group.pg_hunt_secure")
	s1 := pgStateOf(t, inst, "scene.pg_hunt_secure")
	if fmt.Sprint(g0.Attributes["entity_id"]) != fmt.Sprint(g1.Attributes["entity_id"]) {
		t.Errorf("BREACH group.pg_hunt_secure (deny write) members %v → %v", g0.Attributes["entity_id"], g1.Attributes["entity_id"])
	}
	if fmt.Sprint(s0.Attributes["entity_id"]) != fmt.Sprint(s1.Attributes["entity_id"]) {
		t.Errorf("BREACH scene.pg_hunt_secure (deny write) members %v → %v", s0.Attributes["entity_id"], s1.Attributes["entity_id"])
	}

	// group.remove deletes the denied group outright.
	pre, pc := huntRun(t, dir, "svc", "call", "group.remove", "--data", `{"object_id":"pg_hunt_secure"}`)
	out, oc := huntRun(t, dir, "svc", "call", "group.remove", "--data", `{"object_id":"pg_hunt_secure"}`, "--confirm")
	time.Sleep(time.Second)
	st, _ := pgHTTP(t, inst, http.MethodGet, "/api/states/group.pg_hunt_secure", nil)
	t.Logf("RESULT group.remove: preview exit=%d confirm exit=%d → GET state http %d\n  preview: %s\n  confirm: %s", pc, oc, st, huntOneLine(pre, 300), huntOneLine(out, 300))
	if st == http.StatusNotFound {
		t.Errorf("BREACH group.pg_hunt_secure (deny write) deleted via group.remove")
	}
	if pc == 0 || oc == 0 || !strings.Contains(out, "entity:group.pg_hunt_secure") {
		t.Errorf("group.remove: preview exit=%d confirm exit=%d, want both refused naming entity:group.pg_hunt_secure (D-184)", pc, oc)
	}
}

// ---------------------------------------------------------------------------
// A profile without `default: deny` ("deny the risky things, allow the rest",
// like the docs' `default` profile): services that act on entities named in
// natural language or by domain.
// ---------------------------------------------------------------------------

const huntDefaultAllowProfile = `    deny:
      "entity:light.ceiling_lights": [read, write]
      "entity:lock.*": [read, write]
      "entity:cover.garage_door": [read, write]
`

func TestPolicyHuntConversationProcess(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), huntPolicy(huntDefaultAllowProfile))
	const ceiling = "light.ceiling_lights"

	huntSetLight(t, "turn_off")
	before := pgStateOf(t, inst, ceiling).State
	o, c := huntRun(t, dir, "svc", "call", "light.turn_on", "--data", `{"entity_id":"light.ceiling_lights"}`, "--confirm")
	t.Logf("direct light.turn_on on denied light: exit=%d %s", c, huntOneLine(o, 200))

	args := []string{"svc", "call", "conversation.process", "--data", `{"text":"turn on the ceiling lights","language":"en"}`, "--return"}
	pre, pc := huntRun(t, dir, args...)
	out, oc := huntRun(t, dir, append(args, "--confirm")...)
	time.Sleep(time.Second)
	after := pgStateOf(t, inst, ceiling).State
	t.Logf("RESULT conversation.process write: preview exit=%d confirm exit=%d ceiling %s→%s\n  preview: %s\n  confirm: %s", pc, oc, before, after, huntOneLine(pre, 300), huntOneLine(out, 600))
	if before != after {
		t.Errorf("BREACH denied %s switched %s→%s via conversation.process", ceiling, before, after)
	}
	if pc == 0 || oc == 0 || !strings.Contains(out, "fail-closed: this service reaches what its call does not name") {
		t.Errorf("conversation.process: preview exit=%d confirm exit=%d, want both refused as opaque (D-184)", pc, oc)
	}

	// Read: ask Assist about a denied cover.
	gd := pgStateOf(t, inst, "cover.garage_door").State
	out, oc = huntRun(t, dir, "svc", "call", "conversation.process", "--data", `{"text":"is the garage door open","language":"en"}`, "--return", "--confirm")
	t.Logf("RESULT conversation.process read (garage door is %q): exit=%d\n  %s", gd, oc, huntOneLine(out, 800))
	out2, oc2 := huntRun(t, dir, "svc", "call", "conversation.process", "--data", `{"text":"is the ceiling lights on","language":"en"}`, "--return", "--confirm")
	t.Logf("RESULT conversation.process read (ceiling is %q): exit=%d\n  %s", pgStateOf(t, inst, ceiling).State, oc2, huntOneLine(out2, 800))
	o3, _ := huntRun(t, dir, "ent", "show", "cover.garage_door")
	t.Logf("ent show cover.garage_door under the same profile: %s", huntOneLine(o3, 200))
	// Asking Assist about read-denied entities is refused the same way: its
	// answer would read them.
	if oc == 0 || oc2 == 0 {
		t.Errorf("conversation.process questions about read-denied entities: exit %d and %d, want both refused (D-184)", oc, oc2)
	}
	// The exact allow is the way back: an owner who means it allows the
	// service by its name.
	allowed := pgInstanceDir(t, inst.URL(), inst.Token(), huntPolicy(huntDefaultAllowProfile+"    allow:\n      \"service:conversation.process\": [control]\n"))
	if o4, c4 := huntRun(t, allowed, "svc", "call", "conversation.process", "--data", `{"text":"what time is it","language":"en"}`, "--return"); c4 != 0 {
		t.Errorf("conversation.process allowed by its exact name: preview exit=%d, want allowed\n  %s", c4, huntOneLine(o4, 300))
	}
}

func TestPolicyHuntRecorderPurgeDeniedHistory(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), huntPolicy(huntDefaultAllowProfile))
	start := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	hist := func() int {
		_, b := pgHTTP(t, inst, http.MethodGet, "/api/history/period/"+start+"?filter_entity_id=lock.front_door,lock.kitchen_door", nil)
		var rows [][]any
		_ = json.Unmarshal(b, &rows)
		n := 0
		for _, r := range rows {
			n += len(r)
		}
		return n
	}
	// generate some lock history as the owner
	pgCallService(t, inst, "lock", "unlock", map[string]any{"entity_id": "lock.front_door"})
	time.Sleep(time.Second)
	pgCallService(t, inst, "lock", "lock", map[string]any{"entity_id": "lock.front_door"})
	time.Sleep(3 * time.Second)
	b := hist()
	o, c := huntRun(t, dir, "ent", "hist", "lock.front_door")
	t.Logf("agent ent hist lock.front_door: exit=%d %s", c, huntOneLine(o, 200))
	args := []string{"svc", "call", "recorder.purge_entities", "--data", `{"domains":["lock"],"keep_days":0}`}
	pre, pc := huntRun(t, dir, args...)
	out, oc := huntRun(t, dir, append(args, "--confirm")...)
	time.Sleep(6 * time.Second)
	a := hist()
	t.Logf("RESULT recorder.purge_entities domains=[lock]: preview exit=%d confirm exit=%d lock history rows %d → %d\n  preview: %s\n  confirm: %s", pc, oc, b, a, huntOneLine(pre, 300), huntOneLine(out, 300))
	if a < b {
		t.Errorf("BREACH history of read+write-denied locks deleted via recorder.purge_entities (%d → %d rows)", b, a)
	}
	if pc == 0 || oc == 0 || !strings.Contains(out, "entity:lock.") {
		t.Errorf("recorder.purge_entities domains=[lock]: preview exit=%d confirm exit=%d, want both refused naming a lock (D-184)", pc, oc)
	}
}

// ---------------------------------------------------------------------------
// Reads: a profile that may read everything except the ceiling lights and the
// locks. Every read command's output is searched for the denied ids, names
// and a state value only the denied entity carries.
// ---------------------------------------------------------------------------

func TestPolicyHuntReadSweep(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	policy := huntPolicy(`    default: deny
    allow:
      "*": [read]
    deny:
      "entity:light.ceiling_lights": [read]
      "entity:lock.*": [read]
`)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
	ws := dialRawWS(t, inst.URL(), inst.Token())
	reg := pgEntityRegistry(t, ws)
	ceilDev := reg["light.ceiling_lights"].DeviceID
	bedDev := reg["light.bed_light"].DeviceID
	// Owner activity on denied entities, so logbook/history/who have rows.
	for _, s := range []string{"turn_on", "turn_off", "turn_on"} {
		huntSetLight(t, s)
	}
	pgCallService(t, inst, "lock", "unlock", map[string]any{"entity_id": "lock.kitchen_door"})
	// Put the denied light and a readable one in one area.
	area := pgCreateArea(t, ws, "PG Hunt Read Area")
	pgUpdateEntity(t, ws, "light.ceiling_lights", map[string]any{"area_id": area})
	pgUpdateEntity(t, ws, "light.bed_light", map[string]any{"area_id": area})
	time.Sleep(2 * time.Second)

	needles := regexp.MustCompile(`(?i)ceiling_lights|ceiling lights|lock\.front_door|lock\.kitchen_door|front door|kitchen door`)
	cmds := [][]string{
		{"health"}, {"health", "--json"}, {"issues"},
		{"changes", "--since", "1h"}, {"changes", "--since", "1h", "--json"},
		{"ent", "ls"}, {"ent", "ls", "--json"}, {"ent", "ls", "--pattern", "ceiling"}, {"ent", "ls", "--area", "PG Hunt"},
		{"ent", "show", "light.ceiling_light"}, {"ent", "show", "light.ceiling_lightz"}, {"ent", "show", "lock.front_dor"},
		{"ent", "show", "light.pg_policy_all_lights", "--full"}, {"ent", "show", "light.pg_policy_all_lights", "--json"},
		{"ent", "hist", "light.pg_policy_all_lights", "--attr", "entity_id"},
		{"ent", "related", "light.bed_light"}, {"ent", "related", "light.bed_light", "--json"},
		{"ent", "who", "light.bed_light", "--since", "1h"}, {"ent", "who", "light.pg_policy_all_lights", "--since", "1h"},
		{"ent", "anomalies", "light.pg_policy_all_lights"},
		{"device", "ls"}, {"device", "ls", "--json"}, {"device", "show", ceilDev}, {"device", "show", bedDev}, {"device", "show", bedDev, "--json"},
		{"area", "ls"}, {"area", "ls", "--json"}, {"label", "ls"}, {"floor", "ls"},
		{"auto", "ls"}, {"auto", "show", "pg_policy_prod_heating"}, {"script", "ls"}, {"helper", "ls"},
		{"dash", "ls"}, {"log", "--errors", "--warnings", "--unique"}, {"energy", "show"},
		{"config", "entries"}, {"config", "entries", "--json"},
		{"svc", "call", "light.turn_on", "--data", `{"area_id":"` + area + `"}`},
		{"svc", "call", "light.turn_on", "--data", `{"area_id":"` + area + `"}`, "--json"},
		{"ref", "validate"},
		{"cache", "status"},
	}
	// What docs/policy.md says still shows (D-183, D-187): a group's
	// entity_id attribute, a device's own name, a refusal naming what it
	// refused. A read-denied name anywhere else is a leak.
	documented := func(cmd string) bool {
		for _, p := range []string{
			"ent show light.pg_policy_all_lights ", // the group's entity_id attribute (D-183)
			"ent hist light.pg_policy_all_lights ", // the same attribute's history (D-183)
			"device ls", "device show " + ceilDev,  // a device's own name (D-183)
			"svc call light.turn_on ", // a refusal names what it refused (D-187)
		} {
			if strings.HasPrefix(cmd+" ", p) {
				return true
			}
		}
		return false
	}
	var hits []string
	for _, c := range cmds {
		out, code := huntRun(t, dir, c...)
		var ctx []string
		for line := range strings.SplitSeq(out, "\n") {
			if needles.MatchString(line) {
				ctx = append(ctx, strings.TrimSpace(line))
			}
		}
		if len(ctx) > 0 {
			cmd := strings.Join(c, " ")
			hits = append(hits, fmt.Sprintf("hactl %s (exit %d):\n    %s", cmd, code, strings.Join(ctx, "\n    ")))
			if !documented(cmd) {
				t.Errorf("hactl %s names a read-denied entity outside the documented limits:\n    %s", cmd, strings.Join(ctx, "\n    "))
			}
		}
	}
	t.Logf("RESULT read sweep — lines naming a read-denied entity (%d commands with hits):\n%s", len(hits), strings.Join(hits, "\n"))
}

// TestServiceInventoryMatchesHomeAssistant — D-184: every service this
// instance exposes, with every field, is in the service inventory the service
// manifest classifies (TestServiceSurfaceIsClosed). A service or a field Home
// Assistant adds fails here until someone classifies it.
func TestServiceInventoryMatchesHomeAssistant(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	code, body := pgHTTP(t, inst, http.MethodGet, "/api/services", nil)
	if code != http.StatusOK {
		t.Fatalf("GET /api/services: http %d", code)
	}
	checkServiceInventory(t, body)
}

func checkServiceInventory(t *testing.T, body []byte) {
	t.Helper()
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
			for f := range flattenServiceFields(s.Fields) {
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

// flattenServiceFields is a service's fields with every section's fields in
// place of the section (Home Assistant nests advanced fields in one).
func flattenServiceFields(fields map[string]json.RawMessage) map[string]bool {
	out := map[string]bool{}
	for name, raw := range fields {
		var sec struct {
			Fields map[string]json.RawMessage `json:"fields"`
		}
		if json.Unmarshal(raw, &sec) == nil && sec.Fields != nil {
			for inner := range flattenServiceFields(sec.Fields) {
				out[inner] = true
			}
			continue
		}
		out[name] = true
	}
	return out
}
