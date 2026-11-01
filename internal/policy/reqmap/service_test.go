package reqmap_test

import (
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

var liveStates = &reqmap.LiveStates{EntityIDs: []string{
	"light.kitchen", "light.hall", "lock.front_door", "lock.back_door",
	"script.morning", "script.unregistered_uid", "scene.evening", "switch.fan",
}}

func svcFacts(reach []string, err error) *reqmap.Facts {
	f := &reqmap.Facts{Live: liveStates, Registries: registries}
	if reach != nil || err != nil {
		f.Reach = &reqmap.Reach{EntityIDs: reach, Err: err}
	}
	return f
}

const svcRow = "rest POST /api/services/{domain}/{service}"

// serviceVectors — E-1, E-2, D-59 as refined (R-1, R-2), D-73.
var serviceVectors = []vector{
	{
		name:  "an entity service is restricted to its own domain's part of the reach",
		req:   rest("POST", "/api/services/light/turn_on", `{"area_id":"kitchen"}`),
		facts: svcFacts([]string{"light.kitchen", "lock.front_door"}, nil),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:light.kitchen", "control service:light.turn_on"},
	},
	{
		name:  "a homeassistant service is checked against the whole reach, and fans out per domain (R-1, R-2)",
		req:   rest("POST", "/api/services/homeassistant/turn_off", `{"label_id":"night"}`),
		facts: svcFacts([]string{"light.kitchen", "lock.front_door"}, nil),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{
			"control entity:light.kitchen", "control entity:lock.front_door",
			"control service:homeassistant.turn_off", "control service:light.turn_off", "control service:lock.turn_off",
		},
	},
	{
		name:  "a platform service of a domain with no live entities is checked against the whole reach (R-1)",
		req:   rest("POST", "/api/services/zwave_js/clear_lock_usercode", `{"entity_id":"lock.front_door","code_slot":1}`),
		facts: svcFacts([]string{"lock.front_door"}, nil),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:lock.front_door", "control service:zwave_js.clear_lock_usercode"},
	},
	{
		name:  "entity_id: all of an entity service is every live entity of the domain, never asked",
		req:   rest("POST", "/api/services/lock/lock", `{"entity_id":"all"}`),
		facts: &reqmap.Facts{Live: liveStates},
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:lock.back_door", "control entity:lock.front_door", "control service:lock.lock"},
	},
	{
		name:  "ALL in any case of a platform service is every live entity of every domain (R-1)",
		req:   rest("POST", "/api/services/zwave_js/refresh_value", `{"entity_id":"ALL"}`),
		facts: &reqmap.Facts{Live: liveStates},
		disp:  reqmap.Write, row: svcRow,
		checks: []string{
			"control entity:light.hall", "control entity:light.kitchen", "control entity:lock.back_door", "control entity:lock.front_door",
			"control entity:scene.evening", "control entity:script.morning", "control entity:script.unregistered_uid", "control entity:switch.fan",
			"control service:zwave_js.refresh_value",
		},
	},
	{
		name:  "an entity named as a mapping key of the data needs control (scene.apply, D-73)",
		req:   rest("POST", "/api/services/scene/apply", `{"entities":{"lock.front_door":"unlocked","light.kitchen":{"state":"on"}}}`),
		facts: &reqmap.Facts{Live: liveStates},
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:light.kitchen", "control entity:lock.front_door", "control service:scene.apply"},
	},
	{
		name:  "an entity named as a string value at any depth needs control",
		req:   rest("POST", "/api/services/notify/notify", `{"message":"x","data":{"actions":[{"target":"Lock.Front_Door"}]}}`),
		facts: &reqmap.Facts{Live: liveStates},
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:lock.front_door", "control service:notify.notify"},
	},
	{
		name:  "data naming no live entity is the service check alone",
		req:   rest("POST", "/api/services/notify/notify", `{"message":"light.nonexistent"}`),
		facts: &reqmap.Facts{Live: liveStates},
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control service:notify.notify"},
	},
	{
		name:  "script.turn_on runs every script in its reach, by the registry unique_id (D-84)",
		req:   rest("POST", "/api/services/script/turn_on", `{"entity_id":["script.morning"]}`),
		facts: svcFacts([]string{"script.morning"}, nil),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:script.morning", "control script:morning_routine/run", "control service:script.turn_on"},
	},
	{
		name:  "a script entity with no unique_id read is checked as every script's run",
		req:   rest("POST", "/api/services/script/toggle", `{"entity_id":"script.unregistered_uid"}`),
		facts: svcFacts([]string{"script.unregistered_uid"}, nil),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:script.unregistered_uid", "control script:*/run", "control service:script.toggle"},
	},
	{
		name: "script.<x> runs <x> itself, no lookup",
		req:  rest("POST", "/api/services/script/morning_routine", ""),
		disp: reqmap.Write, row: svcRow,
		checks: []string{"control script:morning_routine/run", "control service:script.morning_routine"},
	},
	{
		name: "script.reload starts no script",
		req:  rest("POST", "/api/services/script/reload", ""),
		disp: reqmap.Write, row: svcRow,
		checks: []string{"control service:script.reload"},
	},
	{
		name:  "homeassistant.turn_on with an area also runs the scripts it reaches (R-2)",
		req:   rest("POST", "/api/services/homeassistant/turn_on", `{"area_id":"kitchen"}`),
		facts: svcFacts([]string{"light.kitchen", "script.morning"}, nil),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{
			"control entity:light.kitchen", "control entity:script.morning",
			"control script:morning_routine/run",
			"control service:homeassistant.turn_on", "control service:light.turn_on", "control service:script.turn_on",
		},
	},
	{
		name:  "selectors under `target` are read too",
		req:   rest("POST", "/api/services/light/turn_off", `{"target":{"floor_id":"ground"}}`),
		facts: svcFacts([]string{"light.hall"}, nil),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:light.hall", "control service:light.turn_off"},
	},
	{
		name:    "an error answer to the reach question refuses the call (D-73)",
		req:     rest("POST", "/api/services/light/turn_on", `{"area_id":"kitchen"}`),
		facts:   svcFacts(nil, errors.New("unknown_command")),
		disp:    reqmap.Unmappable,
		row:     svcRow,
		refused: "fail-closed: reach unknown",
	},
	{
		name:    "no answer to the reach question refuses the call",
		req:     rest("POST", "/api/services/light/turn_on", `{"device_id":"dev1"}`),
		facts:   &reqmap.Facts{Live: liveStates},
		disp:    reqmap.Unmappable,
		row:     svcRow,
		refused: "fail-closed: reach unknown",
	},
	{
		name:    "a call with a target but no live set refuses",
		req:     rest("POST", "/api/services/light/turn_on", `{"entity_id":"all"}`),
		disp:    reqmap.Unmappable,
		row:     svcRow,
		refused: "fail-closed: reach unknown",
	},
	{
		name:    "a service id the grammar refuses is unmappable",
		req:     rest("POST", "/api/services/light/turn-on", "{}"),
		disp:    reqmap.Unmappable,
		row:     svcRow,
		refused: "fail-closed: unmappable request",
	},
	{
		name:    "a body that is not a JSON object is unmappable",
		req:     rest("POST", "/api/services/light/turn_on", `["light.kitchen"]`),
		disp:    reqmap.Unmappable,
		row:     svcRow,
		refused: "fail-closed: unmappable request",
	},
}

// TestServiceCallMapping — POST /api/services/<d>/<s>: the service check
// always, the reach, the data's entities, the per-domain fan-out and every
// script the call starts.
func TestServiceCallMapping(t *testing.T) {
	for _, v := range serviceVectors {
		checkVector(t, v)
	}
}

// TestReachQuery — the question asked of extract_from_target: the selectors,
// expand_group true, `all` never sent.
func TestReachQuery(t *testing.T) {
	for _, c := range []struct {
		name, body string
		want       map[string]any
	}{
		{"an area", `{"area_id":"kitchen","brightness":3}`, map[string]any{"area_id": []any{"kitchen"}, "expand_group": true}},
		{"top level and target both", `{"entity_id":"light.a","target":{"entity_id":["light.b"],"label_id":"night"}}`,
			map[string]any{"entity_id": []any{"light.a", "light.b"}, "label_id": []any{"night"}, "expand_group": true}},
		{"all is never sent", `{"entity_id":"All"}`, nil},
		{"all beside an area keeps the area", `{"entity_id":"all","area_id":"x"}`, map[string]any{"area_id": []any{"x"}, "expand_group": true}},
		{"no target", `{"message":"hi"}`, nil},
		{"no body", ``, nil},
	} {
		got, err := reqmap.ReachQuery(rest("POST", "/api/services/light/turn_on", c.body))
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			gj, _ := json.Marshal(got)
			wj, _ := json.Marshal(c.want)
			t.Errorf("%s: ReachQuery = %s, want %s", c.name, gj, wj)
		}
	}
	for _, req := range []reqmap.Request{
		rest("GET", "/api/services/light/turn_on", ""),
		rest("POST", "/api/template", "{}"),
		rest("POST", "/api/services/light/../turn_on", "{}"),
		comp("POST", "/api/services/light/turn_on", "{}"),
		rest("POST", "/api/services/light/turn_on", "[]"),
	} {
		if _, err := reqmap.ReachQuery(req); err == nil {
			t.Errorf("ReachQuery(%s %s %s) answered; it is no service call to ask about", req.Transport, req.Method, req.Path)
		}
	}
}

// TestServiceChecksAreSortedAndUnique — an entity in the reach and in the data
// is one check, and the order is the verb order then the target.
func TestServiceChecksAreSortedAndUnique(t *testing.T) {
	got := reqmap.Classify(rest("POST", "/api/services/light/turn_on", `{"entity_id":"light.kitchen","x":"light.kitchen"}`),
		svcFacts([]string{"light.kitchen"}, nil))
	want := []string{"control entity:light.kitchen", "control service:light.turn_on"}
	if !slices.Equal(got.Strings(), want) {
		t.Errorf("got %q, want %q", got.Strings(), want)
	}
}
