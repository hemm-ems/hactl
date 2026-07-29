//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// Oracle for `/api/states` attribute typing — the rig behind H-21, "a listing
// decodes only the entities it lists".
//
// `hactl auto ls` and `hactl script ls` decode ALL of /api/states into their own
// domain-typed struct and filter to `automation.`/`script.` afterwards, so an
// entity the command will discard can fail the command. A live instance
// (HA 2026.7.4) reported:
//
//	parsing states: json: cannot unmarshal number -1.7525 into Go struct field
//	automationAttributes.attributes.current of type int
//
// That instance is a third party's and is NOT reachable from this project. The
// entity that emitted -1.7525 there is therefore UNKNOWN and nothing here claims
// to know it. These tests ask a live HA the three questions the fix rests on,
// none of which need that instance.
//
// Captured 2026-07-29 against ghcr.io/home-assistant/home-assistant:stable,
// which reported MAJOR 2026 / MINOR 7 / PATCH "4" — the same version as the
// report — with the `domaindecode` fixture (default_config + demo + template +
// automations + scripts), 127 entities across 39 domains:
//
//  1. Attribute keys are NOT domain-scoped and NOT type-stable across domains.
//     In one response, from HA's own integrations alone, `max` is an integer on
//     `automation.*`/`script.*` (max_runs) and a fraction on `number.*`
//     (100.0). `current_temperature` is integral on `climate.hvac`, fractional
//     on `climate.heatpump` and null on `water_heater.*`. Eight further keys
//     (`min`, `max`, `step`, `humidity`, `current_humidity`, `min_temp`,
//     `max_temp`, `temperature`) are multi-typed the same way.
//
//  2. `current` on automation and script is HA's `Script.runs` property,
//     `return len(self._runs)` — a Python int by construction, observed as `0`
//     and as `2` with two runs held open, never fractional.
//
//  3. Among first-party entities in this instance, none of the six keys
//     automationAttributes/scriptAttributes decode collides BY TYPE: they are
//     shared across domains (`id` also on `person.*`, `mode` also on
//     `humidifier`/`number`/`text`, `friendly_name` on all 39 domains) but at
//     the same JSON type. This instance is a LOWER BOUND on what HA can emit,
//     never a census of it — see TestOracleStatesSixKeyDomainCensus for what it
//     can and cannot settle.
//
//  4. All six keys CAN be emitted at a colliding type. `template:` places five
//     of them (`current`, `id`, `mode`, `last_triggered`, `restored`) at any
//     JSON type the author picks; `friendly_name` it cannot, because HA core
//     overwrites it with the entity's name. The States API (POST
//     /api/states/<id>) stores and re-emits all six verbatim, `friendly_name`
//     included.
//
// `sensor.hactl_synthetic_collider*` are SYNTHETIC and named so they cannot be
// mistaken for an observation of the reporting instance. They cover a
// deliberate superset of it: one discarded entity carrying every key at a
// colliding type, which is stronger than whatever single key the real culprit
// carries.
// ============================================================================

var (
	domainDecodeOnce sync.Once
	domainDecodeHA   *hatest.Instance
)

// getDomainDecodeHA boots the domaindecode fixture once for all three oracles.
func getDomainDecodeHA(t *testing.T) *hatest.Instance {
	t.Helper()
	domainDecodeOnce.Do(func() {
		domainDecodeHA = hatest.StartShared(t, hatest.WithFixture("domaindecode"))
	})
	if domainDecodeHA == nil {
		t.Fatal("domaindecode HA instance unavailable")
	}
	return domainDecodeHA
}

// ddSyntheticMarker is the substring every entity this file invents carries, so
// the census can separate "what HA ships" from "what this test pushed in".
const ddSyntheticMarker = "hactl_synthetic_collider"

// ddEntity is a states record whose attributes stay RAW. Decoding them into
// typed fields is precisely the mistake under investigation, so the oracle
// refuses to do it: it reads the wire types, it does not impose any.
type ddEntity struct {
	EntityID   string                     `json:"entity_id"`
	State      string                     `json:"state"`
	Attributes map[string]json.RawMessage `json:"attributes"`
}

func (e ddEntity) domain() string { return strings.SplitN(e.EntityID, ".", 2)[0] }

func (e ddEntity) synthetic() bool { return strings.Contains(e.EntityID, ddSyntheticMarker) }

// ddJSONKind names the wire type of a raw value the way the fix has to think
// about it: `number/integral` and `number/fractional` are one JSON type but two
// different answers to "does this fit a Go int".
func ddJSONKind(raw json.RawMessage) string {
	s := strings.TrimSpace(string(raw))
	if s == "" {
		return "absent"
	}
	switch s[0] {
	case '"':
		return "string"
	case '{':
		return "object"
	case '[':
		return "array"
	case 't', 'f':
		return "bool"
	case 'n':
		return "null"
	default:
		if strings.ContainsAny(s, ".eE") {
			return "number/fractional"
		}
		return "number/integral"
	}
}

// ddStates reads /api/states with the attributes left raw.
func ddStates(t *testing.T, inst *hatest.Instance) []ddEntity {
	t.Helper()
	raw, err := haapi.New(inst.URL(), inst.Token()).GetStates(context.Background())
	if err != nil {
		t.Fatalf("GET /api/states: %v", err)
	}
	var out []ddEntity
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decoding /api/states into raw attributes: %v", err)
	}
	if len(out) == 0 {
		t.Fatal("/api/states returned no entities — the fixture did not load")
	}
	return out
}

// ddRequest issues a raw authenticated request against HA. The oracle needs the
// States API, which haapi does not wrap, and needs the response verbatim.
func ddRequest(t *testing.T, inst *hatest.Instance, method, path string, body any) (int, string) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		enc, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encoding %s %s body: %v", method, path, err)
		}
		rdr = bytes.NewReader(enc)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, inst.URL()+path, rdr)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+inst.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close() //nolint:errcheck // test-only response body
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s %s response: %v", method, path, err)
	}
	return resp.StatusCode, string(data)
}

// ddSixKeys is the union of the keys automationAttributes and scriptAttributes
// decode (auto.go, script.go). Kept as data because every question in this file
// is asked about the whole set, not about `current` alone.
var ddSixKeys = []string{"friendly_name", "last_triggered", "id", "mode", "current", "restored"}

// ddPushCollider creates a states record carrying all six keys at a type the Go
// field cannot hold, via HA's own States API, and removes it again afterwards.
//
// It exists because `template:` cannot reach `friendly_name`: HA core writes
// that attribute from the entity's name and overwrites whatever the config
// says. The States API writes the state machine directly, which is what every
// script, Node-RED flow and AppDaemon app that pushes a state also does.
func ddPushCollider(t *testing.T, inst *hatest.Instance) string {
	t.Helper()
	const entityID = "sensor." + ddSyntheticMarker + "_pushed"
	code, body := ddRequest(t, inst, http.MethodPost, "/api/states/"+entityID, map[string]any{
		"state": "ok",
		"attributes": map[string]any{
			"friendly_name":  9.75,
			"last_triggered": 1753800000,
			"id":             4711,
			"mode":           2.5,
			"current":        -1.7525,
			"restored":       "not-a-bool",
		},
	})
	if code != http.StatusCreated && code != http.StatusOK {
		t.Fatalf("POST /api/states/%s answered %d: %s", entityID, code, body)
	}
	t.Cleanup(func() {
		ddRequest(t, inst, http.MethodDelete, "/api/states/"+entityID, nil)
	})
	return entityID
}

// TestOracleStatesCarriesOneKeyAtTwoJSONTypes answers the first marker the fix
// rests on: can two entities in different domains carry the same attribute key
// at different JSON types in ONE /api/states response?
//
// It is asked twice, deliberately. First of HA alone, with every entity this
// file invents excluded — because a "yes" that needs a synthetic entity to
// produce would only prove this test can write YAML. Then of the synthetic
// collider, which is the superset the fix's acceptance criterion asks for.
func TestOracleStatesCarriesOneKeyAtTwoJSONTypes(t *testing.T) {
	inst := getDomainDecodeHA(t)

	// --- Half 1: HA's own integrations, no synthetic help. ---
	states := ddStates(t, inst)
	multiTyped := ddFirstPartyMultiTypedKeys(states)
	t.Logf("multi-typed attribute keys from HA's own integrations (%d):\n%s",
		len(multiTyped), strings.Join(multiTyped, "\n"))
	if len(multiTyped) == 0 {
		t.Fatal("no attribute key in this instance carries two JSON types — H-21's premise " +
			"(one key, two types, one payload) is not reproducible against HA's own entities, " +
			"and the diagnosis behind the filter-before-decode fix needs re-examining")
	}
	assertMaxSpansListedAndDiscarded(t, states)

	// --- Half 2: the synthetic collider, in the same payload as real
	// automations. This is the acceptance shape: one entity the listing
	// discards, carrying every key the listing decodes, at a colliding type. ---
	pushed := ddPushCollider(t, inst)
	assertColliderSitsBesideRealListings(t, ddStates(t, inst), pushed)
}

// ddFirstPartyMultiTypedKeys reports every attribute key that HA's own entities
// carry at two or more distinct non-null JSON types in one payload, formatted
// `key -> kind=ids | kind=ids`. Synthetic entities are excluded: a "yes" that
// needed one would only prove this test can write YAML.
func ddFirstPartyMultiTypedKeys(states []ddEntity) []string {
	kinds := map[string]map[string][]string{} // key -> wire kind -> entity ids
	for _, e := range states {
		if e.synthetic() {
			continue
		}
		for k, v := range e.Attributes {
			kind := ddJSONKind(v)
			if kind == "null" {
				continue // an absent value is not a second type
			}
			if kinds[k] == nil {
				kinds[k] = map[string][]string{}
			}
			if len(kinds[k][kind]) < 3 {
				kinds[k][kind] = append(kinds[k][kind], e.EntityID)
			}
		}
	}
	var out []string
	for k, byKind := range kinds {
		if len(byKind) < 2 {
			continue
		}
		var parts []string
		for kind, ids := range byKind {
			parts = append(parts, kind+"="+strings.Join(ids, ","))
		}
		sort.Strings(parts)
		out = append(out, fmt.Sprintf("%s -> %s", k, strings.Join(parts, " | ")))
	}
	sort.Strings(out)
	return out
}

// assertMaxSpansListedAndDiscarded names the nearest miss explicitly. `max` is a
// key automation and script entities themselves carry (max_runs, an integer)
// AND a key `number.*` carries as a fraction. hactl does not decode `max`
// today; the day automationAttributes grows a `Max int`, this stock payload
// breaks it. That is the whole of H-21 in one already-shipping key, with no
// third-party integration and nothing synthetic involved.
func assertMaxSpansListedAndDiscarded(t *testing.T, states []ddEntity) {
	t.Helper()
	kinds := map[string][]string{}
	listed, discarded := false, false
	for _, e := range states {
		v, ok := e.Attributes["max"]
		if e.synthetic() || !ok {
			continue
		}
		kind := ddJSONKind(v)
		kinds[kind] = append(kinds[kind], e.EntityID)
		isListed := e.domain() == "automation" || e.domain() == "script"
		switch {
		case isListed && kind == "number/integral":
			listed = true
		case !isListed && kind == "number/fractional":
			discarded = true
		}
	}
	if !listed || !discarded {
		t.Errorf("`max` no longer spans an integral value on a listed domain and a fractional one "+
			"elsewhere (integral-on-automation/script=%v, fractional-elsewhere=%v): %v — the "+
			"first-party instance of H-21 changed shape, re-derive it before trusting the law's "+
			"examples", listed, discarded, kinds)
	}
}

// ddColliderWantedKinds is the wire type each of the six keys must come back as
// on the pushed collider: in every case a type the corresponding Go field in
// automationAttributes/scriptAttributes cannot hold.
var ddColliderWantedKinds = map[string]string{
	"friendly_name":  "number/fractional", // field is string
	"last_triggered": "number/integral",   // field is string
	"id":             "number/integral",   // field is string
	"mode":           "number/fractional", // field is string
	"current":        "number/fractional", // field is int
	"restored":       "string",            // field is bool
}

// assertColliderSitsBesideRealListings checks the acceptance shape: the pushed
// collider carries every key at a colliding type, in the SAME response that
// carries the automations and scripts a listing would render.
func assertColliderSitsBesideRealListings(t *testing.T, states []ddEntity, pushed string) {
	t.Helper()
	byID := map[string]ddEntity{}
	haveAutomation, haveScript := false, false
	for _, e := range states {
		byID[e.EntityID] = e
		switch e.domain() {
		case "automation":
			haveAutomation = true
		case "script":
			haveScript = true
		}
	}
	if !haveAutomation || !haveScript {
		t.Fatalf("payload carries no automation (%v) / script (%v) entity, so it cannot show a "+
			"collision *within one listing's response*", haveAutomation, haveScript)
	}
	collider, ok := byID[pushed]
	if !ok {
		t.Fatalf("%s is not in /api/states after the States API accepted it", pushed)
	}
	for _, k := range ddSixKeys {
		v, present := collider.Attributes[k]
		if !present {
			t.Errorf("HA dropped %q from the pushed states record — the States API no longer "+
				"round-trips arbitrary attribute keys, so this rig cannot pose the six-key case", k)
			continue
		}
		if got := ddJSONKind(v); got != ddColliderWantedKinds[k] {
			t.Errorf("%s attribute %q came back as %s (%s), want %s — HA now coerces this key "+
				"and the collision it demonstrates is no longer reachable this way",
				pushed, k, got, v, ddColliderWantedKinds[k])
		}
	}
}

// TestOracleAutomationScriptCurrentIsIntegral answers the second marker: is
// `current` on automation/script entities integral by construction, or should
// the Go field be widened independently of the ordering fix?
//
// A sample alone cannot answer it — `current` is 0 on an idle instance, and 0 is
// the one value whose JSON form cannot distinguish an int producer from a float
// one. So the test does two things a fixture cannot: it drives `current` above
// zero on a real parallel automation and a real parallel script, and it reads
// the producing code out of the running container.
func TestOracleAutomationScriptCurrentIsIntegral(t *testing.T) {
	inst := getDomainDecodeHA(t)

	ddHoldParallelRunsOpen(t, inst)

	// Every automation and script in the payload, idle ones included: `current`
	// is present and integral on the wire.
	for _, e := range ddStates(t, inst) {
		if d := e.domain(); d != "automation" && d != "script" {
			continue
		}
		v, ok := e.Attributes["current"]
		if !ok {
			t.Errorf("%s carries no `current` attribute — the field automationAttributes/"+
				"scriptAttributes decode is no longer always emitted", e.EntityID)
			continue
		}
		if kind := ddJSONKind(v); kind != "number/integral" {
			t.Errorf("%s emits current=%s (%s) — HA does NOT keep this attribute integral, and "+
				"the `Current int` field needs widening as a change in its own right",
				e.EntityID, v, kind)
		}
	}

	// The mechanism, from the running HA's own source. A wire sample says what
	// happened; this says what can happen.
	ddAssertHASource(t, inst,
		"/usr/src/homeassistant/homeassistant/helpers/script.py",
		"def runs", "return len(self._runs)",
		"`current` is defined as the length of the open-runs list, i.e. a Python int by "+
			"construction; if that stops being a length it can stop being an integer")
	ddAssertHASource(t, inst,
		"/usr/src/homeassistant/homeassistant/components/automation/__init__.py",
		"def extra_state_attributes", "self.action_script.runs",
		"the automation entity's `current` is that same runs count")
	ddAssertHASource(t, inst,
		"/usr/src/homeassistant/homeassistant/components/script/__init__.py",
		"def extra_state_attributes", "script.runs",
		"the script entity's `current` is that same runs count")
}

// ddHoldParallelRunsOpen parks two concurrent runs in the fixture's parallel
// automation and parallel script, and blocks until HA reports `current` at two
// or more for both.
//
// `current` is 0 on an idle instance, and 0 is the one value whose JSON form
// cannot distinguish an integer producer from a float one — an oracle that only
// ever sees 0 has asked nothing. Firing the trigger EVENT rather than calling
// `automation.trigger` matters too: the service call blocks for the whole 600s
// delay the run is parked in, so it would time out rather than report.
func ddHoldParallelRunsOpen(t *testing.T, inst *hatest.Instance) {
	t.Helper()
	const (
		wantRuns  = 2
		parallel  = "automation.collider_parallel"
		parScript = "script.collider_parallel_script"
	)
	for range wantRuns {
		if code, body := ddRequest(t, inst, http.MethodPost,
			"/api/events/hactl_collider_fire", map[string]any{}); code != http.StatusOK {
			t.Fatalf("firing hactl_collider_fire answered %d: %s", code, body)
		}
		if code, body := ddRequest(t, inst, http.MethodPost, "/api/services/script/turn_on",
			map[string]any{"entity_id": parScript}); code != http.StatusOK {
			t.Fatalf("script.turn_on answered %d: %s", code, body)
		}
	}

	// The threshold is ">= wantRuns", not "== wantRuns": the runs stay parked, so
	// a second execution against the same shared container legitimately observes
	// a higher count. What matters is that the value is not zero.
	busy := map[string]json.RawMessage{}
	open := func(id string) int {
		n, err := strconv.Atoi(strings.TrimSpace(string(busy[id])))
		if err != nil {
			return -1
		}
		return n
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		for _, e := range ddStates(t, inst) {
			if e.EntityID == parallel || e.EntityID == parScript {
				busy[e.EntityID] = e.Attributes["current"]
			}
		}
		if open(parallel) >= wantRuns && open(parScript) >= wantRuns {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("`current` never reached %d with two runs held open (automation=%s, script=%s) — "+
				"the rig cannot observe the attribute above zero, which is the only value that "+
				"distinguishes an integer producer from a float one",
				wantRuns, busy[parallel], busy[parScript])
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// ddAssertHASource reads the running container's own Python and fails when the
// code that produces an attribute stops looking the way the Go type assumes.
func ddAssertHASource(t *testing.T, inst *hatest.Instance, path, pattern, want, why string) {
	t.Helper()
	code, out, err := inst.Exec(context.Background(), "grep", "-n", "-A", "8", pattern, path)
	if err != nil {
		t.Fatalf("reading %s from the running container: %v", path, err)
	}
	if code != 0 {
		t.Fatalf("grep %q %s exited %d: %s", pattern, path, code, out)
	}
	if !strings.Contains(out, want) {
		t.Errorf("%s no longer contains %q near %q — %s\n%s", path, want, pattern, why, out)
	}
}

// TestOracleStatesSixKeyDomainCensus answers the third marker: which of the six
// keys automationAttributes/scriptAttributes decode can collide with a
// differently-typed key from another domain — i.e. does the acceptance test need
// to cover one key or all six?
//
// HONESTY, because this is the marker most easily over-answered: a Docker HA
// with default_config + demo is a LOWER BOUND. It cannot enumerate what every
// integration on earth puts in `attributes`, so it can never license "key X is
// safe". What it CAN establish, and does below, is the two halves that decide
// the question anyway:
//
//   - the keys are not domain-private (`id`, `mode`, `friendly_name` all occur
//     outside automation/script), so nothing structurally reserves them; and
//   - HA will emit any of the six at a colliding type when asked, so every one
//     of them is reachable.
//
// Together those say: cover all six. Settling it the other way — "key X can
// never collide" — would need a guarantee HA does not offer, not a bigger
// fixture.
func TestOracleStatesSixKeyDomainCensus(t *testing.T) {
	inst := getDomainDecodeHA(t)
	states := ddStates(t, inst)

	// key -> "domain/kind" -> count, over HA's own entities only.
	census := map[string]map[string]int{}
	outsideListing := map[string][]string{}
	for _, k := range ddSixKeys {
		census[k] = map[string]int{}
	}
	firstParty := 0
	for _, e := range states {
		if e.synthetic() {
			continue
		}
		firstParty++
		for _, k := range ddSixKeys {
			v, ok := e.Attributes[k]
			if !ok {
				continue
			}
			census[k][e.domain()+"/"+ddJSONKind(v)]++
			if d := e.domain(); d != "automation" && d != "script" {
				outsideListing[k] = append(outsideListing[k], e.EntityID+"="+string(v))
			}
		}
	}
	for _, k := range ddSixKeys {
		var rows []string
		for dk, n := range census[k] {
			rows = append(rows, fmt.Sprintf("%s x%d", dk, n))
		}
		sort.Strings(rows)
		t.Logf("census %-15s %s", k, strings.Join(rows, " "))
	}
	t.Logf("census taken over %d first-party entities — a lower bound, not an enumeration", firstParty)

	// Half 1: the keys are not domain-private. Three of the six demonstrably
	// occur on entities `auto ls`/`script ls` discard, which is what makes the
	// decode-everything ordering reachable at all.
	for _, k := range []string{"friendly_name", "id", "mode"} {
		if len(outsideListing[k]) == 0 {
			t.Errorf("no entity outside automation/script carries %q in this instance — if that is "+
				"now true of HA in general, H-21's reachability argument rests on fewer keys than "+
				"it claims", k)
		} else {
			t.Logf("%q also occurs outside the listing: %s", k, strings.Join(outsideListing[k][:1], ""))
		}
	}
	// The other three were observed ONLY inside the listing here (last_triggered,
	// current) or not at all (restored). Recorded as an observation about this
	// instance, not as a property of HA. `restored` in particular is core,
	// domain-agnostic behaviour — homeassistant/helpers/entity.py sets
	// `ATTR_RESTORED: True` on any registry entity whose integration did not set
	// it up — so its absence here means the fixture has no such entity, NOT that
	// the key belongs to automations.
	for _, k := range []string{"last_triggered", "current", "restored"} {
		t.Logf("%q outside the listing in this instance: %d occurrence(s) — lower bound only",
			k, len(outsideListing[k]))
	}

	// Half 2: every one of the six is reachable at a colliding type. Five of them
	// through `template:`, a first-party integration driven by ordinary config;
	// `friendly_name` only through the States API, because HA core overwrites a
	// template-supplied friendly_name with the entity's name.
	var tmplCollider ddEntity
	for _, e := range states {
		if e.EntityID == "sensor."+ddSyntheticMarker {
			tmplCollider = e
		}
	}
	if tmplCollider.EntityID == "" {
		t.Fatalf("the template collider sensor.%s is missing — the fixture did not load", ddSyntheticMarker)
	}
	wantTemplate := map[string]string{
		"last_triggered": "number/integral",
		"id":             "number/integral",
		"mode":           "number/fractional",
		"current":        "number/fractional",
		"restored":       "string",
	}
	for k, want := range wantTemplate {
		got := ddJSONKind(tmplCollider.Attributes[k])
		if got != want {
			t.Errorf("template attribute %q emitted as %s (%s), want %s — a first-party integration "+
				"can no longer place this key at a colliding type, which narrows what the acceptance "+
				"test has to cover", k, got, tmplCollider.Attributes[k], want)
		}
	}
	// The exception, asserted rather than assumed: HA core wins on friendly_name.
	if got := ddJSONKind(tmplCollider.Attributes["friendly_name"]); got != "string" {
		t.Errorf("HA no longer overwrites a template-supplied friendly_name (got %s) — the "+
			"'template cannot reach friendly_name' note in this file's header is stale", got)
	}
	// …and the States API can place even that key at a colliding type. Proven in
	// TestOracleStatesCarriesOneKeyAtTwoJSONTypes; named here so the census's
	// conclusion is not read as "friendly_name is safe".
}
