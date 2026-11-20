//go:build integration

package integration

// The policy re-hunt of 2026-11-20 (D-188..D-196), kept as the live
// acceptance of its fixes: each test drives the agent's hactl against the
// rig's Home Assistant and reads the verdict back from Home Assistant over raw
// REST/WS, never from hactl's own answer. A route the policy stops logs
// "HELD"; one that gets through fails the test with "BREACH" — the hunt's
// assertions, unchanged.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

func rhPolicy(body string) string { return huntPolicy(body) }

// rhBoth runs the preview and then --confirm and returns a one-line account.
func rhBoth(t *testing.T, dir string, args ...string) (int, int, string) {
	t.Helper()
	pre, pc := huntRun(t, dir, args...)
	out, oc := huntRun(t, dir, append(append([]string{}, args...), "--confirm")...)
	return pc, oc, fmt.Sprintf("preview exit=%d: %s\n      confirm exit=%d: %s", pc, huntOneLine(pre, 400), oc, huntOneLine(out, 400))
}

func rhVerdict(t *testing.T, name string, breach bool, detail string) {
	t.Helper()
	if breach {
		t.Errorf("BREACH %s\n      %s", name, detail)
		return
	}
	t.Logf("HELD %s\n      %s", name, detail)
}

// rhLightOff switches the ceiling light off as the owner.
func rhLightOff(t *testing.T) {
	t.Helper()
	pgCallService(t, getPolicyEnforceHA(t), "light", "turn_off", map[string]any{"entity_id": "light.ceiling_lights"})
	time.Sleep(700 * time.Millisecond)
}

// ---------------------------------------------------------------------------
// 1. Service names in another case. HA lowercases domain and service.
// ---------------------------------------------------------------------------

const rhCaseProfile = `    deny:
      "entity:light.ceiling_lights": [read, write]
      "entity:lock.*": [read, write]
      "entity:group.pg_rh_secure": [write]
      "entity:scene.pg_rh_secure": [write]
      "script:pg_policy_denied/run": [control]
`

func TestPolicyRehuntServiceNameCase(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), rhPolicy(rhCaseProfile))
	const ceiling = "light.ceiling_lights"

	// Oracle: does HA accept an uppercase service name?
	rhLightOff(t)
	code, body := pgHTTP(t, inst, http.MethodPost, "/api/services/LIGHT/TURN_ON", map[string]any{"entity_id": ceiling})
	time.Sleep(700 * time.Millisecond)
	t.Logf("ORACLE POST /api/services/LIGHT/TURN_ON: http %d %s → ceiling %s", code, huntOneLine(string(body), 120), pgStateOf(t, inst, ceiling).State)
	code, body = pgHTTP(t, inst, http.MethodPost, "/api/services/Conversation/Process", map[string]any{"text": "what time is it", "language": "en"})
	t.Logf("ORACLE POST /api/services/Conversation/Process: http %d %s", code, huntOneLine(string(body), 160))

	// conversation.process, any case
	for _, svc := range []string{"conversation.process", "CONVERSATION.PROCESS", "Conversation.Process", "conversation.Process"} {
		rhLightOff(t)
		pc, oc, rep := rhBoth(t, dir, "svc", "call", svc, "--data", `{"text":"turn on the ceiling lights","language":"en"}`)
		time.Sleep(1500 * time.Millisecond)
		after := pgStateOf(t, inst, ceiling).State
		rhVerdict(t, "svc call "+svc+" (opaque) → ceiling "+after, after == "on", fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	}

	// group.set / scene.create / group.remove on a write-denied group/scene
	pgCallService(t, inst, "group", "set", map[string]any{"object_id": "pg_rh_secure", "name": "PG RH Secure", "entities": []string{"lock.front_door"}})
	pgCallService(t, inst, "scene", "create", map[string]any{"scene_id": "pg_rh_secure", "entities": map[string]any{"lock.front_door": "locked"}})
	time.Sleep(time.Second)
	for _, svc := range []string{"group.set", "GROUP.SET", "Group.Set"} {
		g0 := fmt.Sprint(pgStateOf(t, inst, "group.pg_rh_secure").Attributes["entity_id"])
		pc, oc, rep := rhBoth(t, dir, "svc", "call", svc, "--data", `{"object_id":"pg_rh_secure","entities":["light.bed_light"]}`)
		time.Sleep(time.Second)
		g1 := fmt.Sprint(pgStateOf(t, inst, "group.pg_rh_secure").Attributes["entity_id"])
		rhVerdict(t, "svc call "+svc+" on write-denied group.pg_rh_secure: members "+g0+" → "+g1, g0 != g1, fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
		if g0 != g1 {
			pgCallService(t, inst, "group", "set", map[string]any{"object_id": "pg_rh_secure", "entities": []string{"lock.front_door"}})
		}
	}
	for _, svc := range []string{"scene.create", "SCENE.CREATE"} {
		s0 := fmt.Sprint(pgStateOf(t, inst, "scene.pg_rh_secure").Attributes["entity_id"])
		pc, oc, rep := rhBoth(t, dir, "svc", "call", svc, "--data", `{"scene_id":"pg_rh_secure","entities":{"light.bed_light":"on"}}`)
		time.Sleep(time.Second)
		s1 := fmt.Sprint(pgStateOf(t, inst, "scene.pg_rh_secure").Attributes["entity_id"])
		rhVerdict(t, "svc call "+svc+" on write-denied scene.pg_rh_secure: members "+s0+" → "+s1, s0 != s1, fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	}
	for _, svc := range []string{"GROUP.REMOVE"} {
		pc, oc, rep := rhBoth(t, dir, "svc", "call", svc, "--data", `{"object_id":"pg_rh_secure"}`)
		time.Sleep(time.Second)
		st, _ := pgHTTP(t, inst, http.MethodGet, "/api/states/group.pg_rh_secure", nil)
		rhVerdict(t, "svc call "+svc+" on write-denied group.pg_rh_secure → GET state "+strconv.Itoa(st), st == http.StatusNotFound, fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	}

	// recorder.purge_entities by domain, any case
	start := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	hist := func() int {
		_, b := pgHTTP(t, inst, http.MethodGet, "/api/history/period/"+start+"?filter_entity_id=lock.front_door", nil)
		var rows [][]any
		_ = json.Unmarshal(b, &rows)
		n := 0
		for _, r := range rows {
			n += len(r)
		}
		return n
	}
	for _, svc := range []string{"RECORDER.PURGE_ENTITIES", "Recorder.purge_entities"} {
		pgCallService(t, inst, "lock", "unlock", map[string]any{"entity_id": "lock.front_door"})
		time.Sleep(time.Second)
		pgCallService(t, inst, "lock", "lock", map[string]any{"entity_id": "lock.front_door"})
		time.Sleep(3 * time.Second)
		b := hist()
		pc, oc, rep := rhBoth(t, dir, "svc", "call", svc, "--data", `{"domains":["lock"],"keep_days":0}`)
		time.Sleep(6 * time.Second)
		a := hist()
		rhVerdict(t, fmt.Sprintf("svc call %s domains=[lock] → lock history %d → %d", svc, b, a), a < b, fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	}

	// script run deny via an uppercase script service
	lastTrig := func() string {
		return fmt.Sprint(pgStateOf(t, inst, "script.pg_policy_denied").Attributes["last_triggered"])
	}
	for _, c := range []struct {
		svc, data string
	}{
		{"script.turn_on", `{"entity_id":"script.pg_policy_denied"}`},
		{"SCRIPT.TURN_ON", `{"entity_id":"script.pg_policy_denied"}`},
		{"SCRIPT.PG_POLICY_DENIED", `{}`},
		{"Script.pg_policy_denied", `{}`},
		{"HOMEASSISTANT.TURN_ON", `{"entity_id":"script.pg_policy_denied"}`},
	} {
		b := lastTrig()
		pc, oc, rep := rhBoth(t, dir, "svc", "call", c.svc, "--data", c.data)
		time.Sleep(1500 * time.Millisecond)
		a := lastTrig()
		rhVerdict(t, "svc call "+c.svc+" "+c.data+" (script:pg_policy_denied/run denied) last_triggered "+b+" → "+a, a != b, fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	}
}

// ---------------------------------------------------------------------------
// 2. A service that deletes an entity, under a deny of [delete] only.
// ---------------------------------------------------------------------------

func TestPolicyRehuntDeleteVerbOnServices(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), rhPolicy(`    deny:
      "entity:scene.pg_rh_del": [delete]
      "entity:group.pg_rh_del": [delete]
`))
	pgCallService(t, inst, "scene", "create", map[string]any{"scene_id": "pg_rh_del", "entities": map[string]any{"light.bed_light": "on"}})
	pgCallService(t, inst, "group", "set", map[string]any{"object_id": "pg_rh_del", "entities": []string{"light.bed_light"}})
	time.Sleep(time.Second)
	for _, r := range []string{"entity:scene.pg_rh_del", "entity:group.pg_rh_del"} {
		o, c := huntRun(t, dir, "policy", "check", r, "delete")
		t.Logf("policy check %s delete: exit=%d %s", r, c, huntOneLine(o, 200))
	}
	pc, oc, rep := rhBoth(t, dir, "svc", "call", "scene.delete", "--data", `{"entity_id":"scene.pg_rh_del"}`)
	time.Sleep(time.Second)
	st, _ := pgHTTP(t, inst, http.MethodGet, "/api/states/scene.pg_rh_del", nil)
	rhVerdict(t, fmt.Sprintf("scene.delete on delete-denied scene.pg_rh_del → GET state http %d", st), st == http.StatusNotFound, fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	pc, oc, rep = rhBoth(t, dir, "svc", "call", "group.remove", "--data", `{"object_id":"pg_rh_del"}`)
	time.Sleep(time.Second)
	st, _ = pgHTTP(t, inst, http.MethodGet, "/api/states/group.pg_rh_del", nil)
	rhVerdict(t, fmt.Sprintf("group.remove on delete-denied group.pg_rh_del → GET state http %d", st), st == http.StatusNotFound, fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
}

// ---------------------------------------------------------------------------
// 3. homeassistant.set_location changes zone.home, which the profile denies
//    writing. The service takes no target.
// ---------------------------------------------------------------------------

func TestPolicyRehuntSetLocation(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), rhPolicy(`    deny:
      "entity:zone.home": [write]
`))
	o, c := huntRun(t, dir, "policy", "check", "entity:zone.home", "control")
	t.Logf("policy check entity:zone.home control: exit=%d %s", c, huntOneLine(o, 200))
	b := pgStateOf(t, inst, "zone.home").Attributes["latitude"]
	pc, oc, rep := rhBoth(t, dir, "svc", "call", "homeassistant.set_location", "--data", `{"latitude":1.5,"longitude":2.5}`)
	time.Sleep(1500 * time.Millisecond)
	a := pgStateOf(t, inst, "zone.home").Attributes["latitude"]
	rhVerdict(t, fmt.Sprintf("homeassistant.set_location with write-denied zone.home: latitude %v → %v", b, a), fmt.Sprint(a) != fmt.Sprint(b), fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	pgCallService(t, inst, "homeassistant", "set_location", map[string]any{"latitude": 52.0, "longitude": 13.0})
}

// ---------------------------------------------------------------------------
// 4. Comma-separated entity ids in fields that are not the target.
// ---------------------------------------------------------------------------

const rhCommaProfile = `    default: deny
    allow:
      "entity:*": [read]
      "entity:light.*": [control]
      "entity:media_player.*": [control]
      "entity:tts.*": [control]
      "entity:group.*": [control]
      "service:light.*": [control]
      "service:group.*": [control]
      "service:tts.*": [control]
      "service:media_player.*": [control]
      "service:homeassistant.turn_on": [control]
    deny:
      "entity:light.ceiling_lights": [write]
      "entity:media_player.bedroom": [write]
`

func TestPolicyRehuntCommaInDataFields(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), rhPolicy(rhCommaProfile))
	// group.set entities as one comma string
	for _, c := range []struct{ name, data string }{
		{"list (baseline)", `{"object_id":"pg_rh_cg1","entities":["light.bed_light","light.ceiling_lights"]}`},
		{"comma string", `{"object_id":"pg_rh_cg2","entities":"light.bed_light, light.ceiling_lights"}`},
		{"comma string add_entities", `{"object_id":"pg_rh_cg3","add_entities":"light.bed_light,light.ceiling_lights"}`},
	} {
		pc, oc, rep := rhBoth(t, dir, "svc", "call", "group.set", "--data", c.data)
		time.Sleep(time.Second)
		var obj struct {
			ObjectID string `json:"object_id"`
		}
		_ = json.Unmarshal([]byte(c.data), &obj)
		st, body := pgHTTP(t, inst, http.MethodGet, "/api/states/group."+obj.ObjectID, nil)
		has := st == 200 && strings.Contains(string(body), "light.ceiling_lights")
		rhVerdict(t, "group.set "+c.name+" puts control-denied light.ceiling_lights into a group: "+strconv.FormatBool(has), has, fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
		if has {
			// what the agent can do with it now
			rhLightOff(t)
			o, c2 := huntRun(t, dir, "svc", "call", "homeassistant.turn_on", "--data", `{"entity_id":"group.`+obj.ObjectID+`"}`)
			t.Logf("   follow-up svc call homeassistant.turn_on group.%s: exit=%d %s", obj.ObjectID, c2, huntOneLine(o, 300))
		}
	}

	// tts: which tts entities exist, which media players
	_, sb := pgHTTP(t, inst, http.MethodGet, "/api/states", nil)
	var states []struct {
		EntityID string `json:"entity_id"`
		State    string `json:"state"`
	}
	_ = json.Unmarshal(sb, &states)
	var tts, mps []string
	for _, s := range states {
		if strings.HasPrefix(s.EntityID, "tts.") {
			tts = append(tts, s.EntityID)
		}
		if strings.HasPrefix(s.EntityID, "media_player.") {
			mps = append(mps, s.EntityID+"="+s.State)
		}
	}
	t.Logf("tts entities %v, media players %v", tts, mps)
	bed := func() string {
		a := pgStateOf(t, inst, "media_player.bedroom").Attributes
		return fmt.Sprint(a["media_content_id"])
	}
	type ttsCase struct{ name, svc, data string }
	var cases []ttsCase
	if len(tts) > 0 {
		cases = append(cases,
			ttsCase{"tts.speak list (baseline)", "tts.speak", `{"entity_id":"` + tts[0] + `","media_player_entity_id":["media_player.walkman","media_player.bedroom"],"message":"rehunt list"}`},
			ttsCase{"tts.speak comma", "tts.speak", `{"entity_id":"` + tts[0] + `","media_player_entity_id":"media_player.walkman, media_player.bedroom","message":"rehunt comma"}`},
		)
	}
	cases = append(cases, ttsCase{"tts.demo_say comma entity_id", "tts.demo_say", `{"entity_id":"media_player.walkman, media_player.bedroom","message":"rehunt legacy"}`})
	for _, c := range cases {
		b := bed()
		pc, oc, rep := rhBoth(t, dir, "svc", "call", c.svc, "--data", c.data)
		time.Sleep(2 * time.Second)
		a := bed()
		rhVerdict(t, c.name+": control-denied media_player.bedroom media_content_id "+b+" → "+a, a != b, fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	}
}

// ---------------------------------------------------------------------------
// 5. recorder.purge_entities globs and string forms.
// ---------------------------------------------------------------------------

func TestPolicyRehuntPurgeGlobs(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), rhPolicy(huntDefaultAllowProfile))
	start := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	hist := func() int {
		_, b := pgHTTP(t, inst, http.MethodGet, "/api/history/period/"+start+"?filter_entity_id=lock.front_door", nil)
		var rows [][]any
		_ = json.Unmarshal(b, &rows)
		n := 0
		for _, r := range rows {
			n += len(r)
		}
		return n
	}
	for _, data := range []string{
		`{"entity_globs":["l?ck.*"],"keep_days":0}`,
		`{"entity_globs":["[l]ock.*"],"keep_days":0}`,
		`{"entity_globs":["lock.front_doo[r]"],"keep_days":0}`,
		`{"entity_globs":"lock.*","keep_days":0}`,
		`{"domains":"lock","keep_days":0}`,
		`{"entity_globs":["LOCK.*"],"keep_days":0}`,
	} {
		pgCallService(t, inst, "lock", "unlock", map[string]any{"entity_id": "lock.front_door"})
		time.Sleep(time.Second)
		pgCallService(t, inst, "lock", "lock", map[string]any{"entity_id": "lock.front_door"})
		time.Sleep(3 * time.Second)
		b := hist()
		pc, oc, rep := rhBoth(t, dir, "svc", "call", "recorder.purge_entities", "--data", data)
		time.Sleep(6 * time.Second)
		a := hist()
		rhVerdict(t, fmt.Sprintf("recorder.purge_entities %s → read+write-denied lock history %d → %d", data, b, a), a < b, fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	}
}

// ---------------------------------------------------------------------------
// 6. Reads through templates HA renders on the agent's behalf.
// ---------------------------------------------------------------------------

const rhReadProfile = `    default: deny
    allow:
      "*": [read]
      "entity:light.bed_light": [control]
      "service:logbook.log": [control]
      "service:system_log.write": [control]
    deny:
      "entity:lock.*": [read]
      "entity:light.ceiling_lights": [read]
`

func TestPolicyRehuntTemplateReads(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), rhPolicy(rhReadProfile))
	lockState := pgStateOf(t, inst, "lock.front_door").State
	t.Logf("owner view: lock.front_door is %q", lockState)
	o, c := huntRun(t, dir, "ent", "show", "lock.front_door")
	t.Logf("agent ent show lock.front_door: exit=%d %s", c, huntOneLine(o, 200))

	// tpl eval under "*": [read]
	o, c = huntRun(t, dir, "policy", "check", "system:template", "read")
	t.Logf("policy check system:template read: exit=%d %s", c, huntOneLine(o, 300))
	o, c = huntRun(t, dir, "tpl", "eval", "RHT-{{ states('lock.front_door') }}")
	rhVerdict(t, "tpl eval of read-denied lock.front_door under \"*\": [read]", c == 0 && strings.Contains(o, "RHT-"+lockState), fmt.Sprintf("exit=%d %s", c, huntOneLine(o, 300)))

	// logbook.log renders its message template
	msg := "RHLEAK1-{{ states('lock.front' ~ '_door') }}-{{ state_attr('light.ceiling' ~ '_lights','friendly_name') }}"
	data, _ := json.Marshal(map[string]any{"name": "RH", "message": msg, "entity_id": "light.bed_light"})
	pc, oc, rep := rhBoth(t, dir, "svc", "call", "logbook.log", "--data", string(data))
	time.Sleep(2 * time.Second)
	start := time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339)
	_, lb := pgHTTP(t, inst, http.MethodGet, "/api/logbook/"+start+"?entity=light.bed_light", nil)
	m := regexp.MustCompile(`RHLEAK1-[^"]*`).FindString(string(lb))
	t.Logf("ORACLE logbook row message: %q (svc call pc=%d oc=%d)\n      %s", m, pc, oc, rep)
	for _, args := range [][]string{{"changes", "--since", "15m"}, {"changes", "--since", "15m", "--json"}, {"ent", "who", "light.bed_light", "--since", "15m"}} {
		out, code := huntRun(t, dir, args...)
		got := regexp.MustCompile(`RHLEAK1-[^"\s|]*`).FindString(out)
		rhVerdict(t, "logbook.log template → hactl "+strings.Join(args, " ")+" shows "+fmt.Sprintf("%q", got),
			strings.Contains(got, "RHLEAK1-"+lockState) || strings.Contains(strings.ToLower(out), "ceiling lights"),
			fmt.Sprintf("exit=%d", code))
	}

	// system_log.write renders its message template; log needs system:logs (granted by "*")
	msg2 := "RHLEAK2-{{ states('lock.front' ~ '_door') }}"
	data2, _ := json.Marshal(map[string]any{"message": msg2, "level": "error", "logger": "rehunt"})
	pc, oc, rep = rhBoth(t, dir, "svc", "call", "system_log.write", "--data", string(data2))
	time.Sleep(2 * time.Second)
	out, code := huntRun(t, dir, "log", "--errors", "--since", "15m")
	got := regexp.MustCompile(`RHLEAK2-\S*`).FindString(out)
	rhVerdict(t, "system_log.write template → hactl log shows "+fmt.Sprintf("%q", got), strings.Contains(got, "RHLEAK2-"+lockState),
		fmt.Sprintf("svc pc=%d oc=%d, log exit=%d\n      %s", pc, oc, code, rep))
}

// ---------------------------------------------------------------------------
// 7. Registry edits that put an entity where a script looks, by routes the
//    F5 test does not use: clearing an entity's own area (falls back to its
//    device's), deleting its area, a label created after the script, a floor,
//    a device label.
// ---------------------------------------------------------------------------

const rhReachProfile = `    default: deny
    allow:
      "entity:*": [read]
      "device:*": [read]
      "area:*": [read, create, delete, edit]
      "label:*": [read, create, delete]
      "floor:*": [read]
      "entity:light.*": [control]
      "entity:*/registry/area_id": [edit]
      "entity:*/registry/labels": [edit]
      "device:*/registry/labels": [edit]
      "device:*/registry/area_id": [edit]
      "service:light.*": [control]
    deny:
      "entity:light.ceiling_lights": [control]
`

func rhOwnerScript(t *testing.T, id string, target map[string]any) {
	t.Helper()
	inst := getPolicyEnforceHA(t)
	code, body := pgHTTP(t, inst, http.MethodPost, "/api/config/script/config/"+id, map[string]any{
		"alias":    id,
		"sequence": []any{map[string]any{"action": "light.turn_on", "target": target}},
	})
	if code != 200 {
		t.Fatalf("owner script %s: http %d %s", id, code, body)
	}
	for range 30 {
		if st, _ := pgHTTP(t, inst, http.MethodGet, "/api/states/script."+id, nil); st == 200 {
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("script.%s never appeared", id)
}

func rhRunScript(t *testing.T, id string) string {
	t.Helper()
	inst := getPolicyEnforceHA(t)
	rhLightOff(t)
	pgCallService(t, inst, "script", "turn_on", map[string]any{"entity_id": "script." + id})
	time.Sleep(2 * time.Second)
	return pgStateOf(t, inst, "light.ceiling_lights").State
}

func TestPolicyRehuntReachRoutes(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), rhPolicy(rhReachProfile))
	ws := dialRawWS(t, inst.URL(), inst.Token())
	const ceiling = "light.ceiling_lights"
	reg := pgEntityRegistry(t, ws)
	dev := reg[ceiling].DeviceID
	if dev == "" {
		t.Fatal("ceiling light has no device")
	}
	devArea := pgCreateArea(t, ws, "PG RH Dev Area")
	ownArea := pgCreateArea(t, ws, "PG RH Own Area")
	ws.mustSucceed(t, map[string]any{"type": "config/device_registry/update", "device_id": dev, "area_id": devArea})
	pgUpdateEntity(t, ws, ceiling, map[string]any{"area_id": ownArea})
	rhOwnerScript(t, "pg_rh_area_script", map[string]any{"area_id": devArea})
	time.Sleep(time.Second)
	t.Logf("setup: ceiling own area %s, device area %s; script targets %s; owner run now → ceiling %s", ownArea, devArea, devArea, rhRunScript(t, "pg_rh_area_script"))

	// (a) ent set-area --clear: falls back to the device's (targeted) area
	pc, oc, rep := rhBoth(t, dir, "ent", "set-area", ceiling, "--clear")
	got := rhRunScript(t, "pg_rh_area_script")
	rhVerdict(t, "ent set-area --clear moves control-denied ceiling into the script's area (device area); owner script run → ceiling "+got, got == "on", fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))

	// (b) area delete of the entity's own area
	pgUpdateEntity(t, ws, ceiling, map[string]any{"area_id": ownArea})
	time.Sleep(time.Second)
	pc, oc, rep = rhBoth(t, dir, "area", "delete", ownArea)
	got = rhRunScript(t, "pg_rh_area_script")
	rhVerdict(t, "area delete of ceiling's own area (falls back to the targeted device area); owner script run → ceiling "+got, got == "on", fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))

	// reset: device out of the targeted area
	ws.mustSucceed(t, map[string]any{"type": "config/device_registry/update", "device_id": dev, "area_id": nil})
	pgUpdateEntity(t, ws, ceiling, map[string]any{"area_id": nil})

	// (c) a label created after the script that targets it
	rhOwnerScript(t, "pg_rh_label_script", map[string]any{"label_id": "pg_rh_later"})
	pc, oc, rep = rhBoth(t, dir, "label", "create", "pg_rh_later")
	t.Logf("agent label create pg_rh_later: pc=%d oc=%d %s", pc, oc, rep)
	labels := pgLabels(t, ws)
	if _, ok := labels["pg_rh_later"]; !ok {
		t.Logf("label ids now: %v", labels)
	}
	pc, oc, rep = rhBoth(t, dir, "ent", "set-label", ceiling, "pg_rh_later")
	got = rhRunScript(t, "pg_rh_label_script")
	rhVerdict(t, "label created after the script, then ent set-label ceiling; owner script run → ceiling "+got, got == "on", fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	pgUpdateEntity(t, ws, ceiling, map[string]any{"labels": []string{}})

	// (d) a device label the script targets
	lab2 := pgCreateLabel(t, ws, "pg_rh_devlab")
	rhOwnerScript(t, "pg_rh_devlab_script", map[string]any{"label_id": lab2})
	pc, oc, rep = rhBoth(t, dir, "device", "set-label", dev, lab2)
	got = rhRunScript(t, "pg_rh_devlab_script")
	rhVerdict(t, "device set-label on ceiling's device with a targeted label; owner script run → ceiling "+got, got == "on", fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	ws.mustSucceed(t, map[string]any{"type": "config/device_registry/update", "device_id": dev, "labels": []string{}})

	// (e) a floor the script targets; the agent creates an area on it and moves the light there
	env := ws.mustSucceed(t, map[string]any{"type": "config/floor_registry/create", "name": "PG RH Floor"})
	var fl struct {
		FloorID string `json:"floor_id"`
	}
	_ = json.Unmarshal(env.Result, &fl)
	rhOwnerScript(t, "pg_rh_floor_script", map[string]any{"floor_id": fl.FloorID})
	pc, oc, rep = rhBoth(t, dir, "area", "create", "PG RH On Floor", "--floor", fl.FloorID)
	t.Logf("agent area create --floor: pc=%d oc=%d %s", pc, oc, rep)
	var onFloor string
	for id, a := range pgAreas(t, ws) {
		if a.Name == "PG RH On Floor" {
			onFloor = id
		}
	}
	if onFloor != "" {
		pc, oc, rep = rhBoth(t, dir, "device", "set-area", dev, onFloor)
		got = rhRunScript(t, "pg_rh_floor_script")
		rhVerdict(t, "device set-area into an area on the targeted floor; owner script run → ceiling "+got, got == "on", fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	}
}

// ---------------------------------------------------------------------------
// 5b. fnmatch negation: HA translates entity_globs with Python fnmatch, where
//     "[!a]" means "not a"; Go's path.Match reads "[!a]" as the class {!, a}.
// ---------------------------------------------------------------------------

func TestPolicyRehuntPurgeGlobNegation(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), rhPolicy(huntDefaultAllowProfile))
	start := time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339)
	hist := func() int {
		_, b := pgHTTP(t, inst, http.MethodGet, "/api/history/period/"+start+"?filter_entity_id=lock.front_door", nil)
		var rows [][]any
		_ = json.Unmarshal(b, &rows)
		n := 0
		for _, r := range rows {
			n += len(r)
		}
		return n
	}
	for _, data := range []string{
		`{"entity_globs":["[!a]ock.front_door"],"keep_days":0}`,
		`{"entity_globs":["lock.front_doo[!a]"],"keep_days":0}`,
	} {
		pgCallService(t, inst, "lock", "unlock", map[string]any{"entity_id": "lock.front_door"})
		time.Sleep(time.Second)
		pgCallService(t, inst, "lock", "lock", map[string]any{"entity_id": "lock.front_door"})
		time.Sleep(3 * time.Second)
		b := hist()
		pc, oc, rep := rhBoth(t, dir, "svc", "call", "recorder.purge_entities", "--data", data)
		time.Sleep(6 * time.Second)
		a := hist()
		rhVerdict(t, fmt.Sprintf("recorder.purge_entities %s → read+write-denied lock history %d → %d", data, b, a), a < b, fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	}
}

// ---------------------------------------------------------------------------
// 8. Reload services that drop entities a call never names: group.set and
//    scene.create entities live only until the reload. Since D-199 the
//    reload needs no delete on them — a stated limit: the reload runs.
// ---------------------------------------------------------------------------

func TestPolicyRehuntReloadDrops(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), rhPolicy(`    deny:
      "entity:group.pg_rh_keep": [write]
      "entity:scene.pg_rh_keep": [write]
      "entity:zone.home": [write]
`))
	pgCallService(t, inst, "group", "set", map[string]any{"object_id": "pg_rh_keep", "entities": []string{"light.bed_light"}})
	pgCallService(t, inst, "scene", "create", map[string]any{"scene_id": "pg_rh_keep", "entities": map[string]any{"light.bed_light": "on"}})
	time.Sleep(time.Second)
	for _, c := range []struct{ svc, id string }{{"group.reload", "group.pg_rh_keep"}, {"scene.reload", "scene.pg_rh_keep"}} {
		b, _ := pgHTTP(t, inst, http.MethodGet, "/api/states/"+c.id, nil)
		pc, oc, rep := rhBoth(t, dir, "svc", "call", c.svc)
		time.Sleep(2 * time.Second)
		a, _ := pgHTTP(t, inst, http.MethodGet, "/api/states/"+c.id, nil)
		if pc != 0 || oc != 0 {
			t.Errorf("over-refusal: %s with write-denied %s refused; a reload needs no delete (D-199)\n      %s", c.svc, c.id, rep)
		}
		t.Logf("%s with write-denied %s: GET state %d → %d (the stated limit, D-199)", c.svc, c.id, b, a)
	}
	// homeassistant.set_location under default deny with the homeassistant services allowed
	dir2 := pgInstanceDir(t, inst.URL(), inst.Token(), rhPolicy(`    default: deny
    allow:
      "entity:*": [read]
      "entity:light.*": [control]
      "service:homeassistant.*": [control]
    deny:
      "entity:zone.home": [write]
`))
	b := pgStateOf(t, inst, "zone.home").Attributes["latitude"]
	pc, oc, rep := rhBoth(t, dir2, "svc", "call", "homeassistant.set_location", "--data", `{"latitude":3.5,"longitude":4.5}`)
	time.Sleep(1500 * time.Millisecond)
	a := pgStateOf(t, inst, "zone.home").Attributes["latitude"]
	rhVerdict(t, fmt.Sprintf("[default deny + service:homeassistant.*] homeassistant.set_location, zone.home write-denied: latitude %v → %v", b, a), fmt.Sprint(a) != fmt.Sprint(b), fmt.Sprintf("pc=%d oc=%d %s", pc, oc, rep))
	pgCallService(t, inst, "homeassistant", "set_location", map[string]any{"latitude": 52.0, "longitude": 13.0})
}

// ---------------------------------------------------------------------------
// 9. D-196: an owner's automation triggered by a read-denied entity records
//    that entity's state in its trace and its logbook context. The agent may
//    read the trace; what it shows of the denied entity is masked in
//    `trace show`, `--full` and `--json` alike.
// ---------------------------------------------------------------------------

func TestPolicyRehuntTraceMasksDeniedStates(t *testing.T) {
	// The oracle rig: the policy rig loads no automations.yaml, so an
	// automation written there never becomes an entity.
	inst, _ := getOracleHA(t)
	const secret = "sensor.pg_rh_mask_secret" //nolint:gosec // G101: an entity id the agent may not read, not a credential
	setSecret := func(state string) {
		t.Helper()
		code, body := pgHTTP(t, inst, http.MethodPost, "/api/states/"+secret,
			map[string]any{"state": state, "attributes": map[string]any{"friendly_name": "Pangolin Mask"}})
		if code != http.StatusOK && code != http.StatusCreated {
			t.Fatalf("set %s: http %d %s", secret, code, body)
		}
	}
	setSecret("QuokkaMaskA")
	code, body := pgHTTP(t, inst, http.MethodPost, "/api/config/automation/config/pg_rh_mask", map[string]any{
		"id": "pg_rh_mask", "alias": "pg_rh_mask",
		"triggers": []any{map[string]any{"trigger": "state", "entity_id": secret}},
		"actions":  []any{map[string]any{"event": "pg_rh_mask_done"}},
	})
	if code != http.StatusOK {
		t.Fatalf("owner automation: http %d %s", code, body)
	}
	t.Cleanup(func() { pgHTTP(t, inst, http.MethodDelete, "/api/config/automation/config/pg_rh_mask", nil) })
	for i := 0; ; i++ {
		if st, _ := pgHTTP(t, inst, http.MethodGet, "/api/states/automation.pg_rh_mask", nil); st == http.StatusOK {
			break
		}
		if i == 30 {
			t.Fatal("the owner's automation automation.pg_rh_mask never appeared")
		}
		time.Sleep(500 * time.Millisecond)
	}
	setSecret("QuokkaMaskB")
	time.Sleep(3 * time.Second)

	dir := pgInstanceDir(t, inst.URL(), inst.Token(), rhPolicy(`    deny:
      "entity:`+secret+`": [read]
`))
	// The owner's view holds the secret: the fixture is live.
	ownerDir := pgInstanceDir(t, inst.URL(), inst.Token(), "")
	if out, _ := huntRun(t, ownerDir, "trace", "show", "automation.pg_rh_mask", "--full"); !strings.Contains(out, "QuokkaMaskB") {
		t.Fatalf("the owner's trace does not hold the secret state — the fixture did not run:\n%s", huntOneLine(out, 600))
	}
	for _, args := range [][]string{
		{"trace", "show", "automation.pg_rh_mask"},
		{"trace", "show", "automation.pg_rh_mask", "--full"},
		{"trace", "show", "automation.pg_rh_mask", "--json"},
		{"changes", "--since", "15m"},
	} {
		out, c := huntRun(t, dir, args...)
		leak := strings.Contains(out, "QuokkaMask") || strings.Contains(out, "Pangolin Mask")
		rhVerdict(t, fmt.Sprintf("agent %s (read-denied trigger entity) exit=%d prints its state or name=%v", strings.Join(args, " "), c, leak),
			leak, huntOneLine(out, 400))
	}
}
