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
		facts: &reqmap.Facts{Live: liveStates, Registries: &reqmap.Registries{}},
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:lock.back_door", "control entity:lock.front_door", "control service:lock.lock"},
	},
	{
		name:  "ALL in any case of a platform service is every live entity of every domain (R-1)",
		req:   rest("POST", "/api/services/zwave_js/refresh_value", `{"entity_id":"ALL"}`),
		facts: &reqmap.Facts{Live: liveStates, Registries: &reqmap.Registries{}},
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
		facts: &reqmap.Facts{Live: liveStates, Registries: &reqmap.Registries{}},
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:light.kitchen", "control entity:lock.front_door", "control service:scene.apply"},
	},
	{
		name:  "an entity named as a string value at any depth needs control",
		req:   rest("POST", "/api/services/notify/notify", `{"message":"x","data":{"actions":[{"target":"Lock.Front_Door"}]}}`),
		facts: &reqmap.Facts{Live: liveStates, Registries: &reqmap.Registries{}},
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:lock.front_door", "control service:notify.notify"},
	},
	{
		name:  "data naming no live entity is the service check alone",
		req:   rest("POST", "/api/services/notify/notify", `{"message":"light.nonexistent"}`),
		facts: &reqmap.Facts{Live: liveStates, Registries: &reqmap.Registries{}},
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control service:notify.notify"},
	},
	{
		name:  "script.turn_on runs every script in its reach, by the registry unique_id (D-84)",
		req:   rest("POST", "/api/services/script/turn_on", `{"entity_id":["script.morning"]}`),
		facts: svcFacts([]string{"script.morning"}, nil),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:script.morning", "control script:morning_routine (deny only)", "control script:morning_routine/run", "control service:script.turn_on"},
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
			"control script:morning_routine (deny only)", "control script:morning_routine/run",
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
		name:  "a comma-separated list of entities in the data names each of them (D-73)",
		req:   rest("POST", "/api/services/scene/create", `{"scene_id":"s","snapshot_entities":"light.kitchen, lock.front_door"}`),
		facts: &reqmap.Facts{Live: liveStates, Registries: &reqmap.Registries{}},
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:light.kitchen", "control entity:lock.front_door", "control service:scene.create"},
	},
	{
		name: "entity_id: all on a homeassistant service is every live entity, even with a live homeassistant.* entity (R-1)",
		req:  rest("POST", "/api/services/homeassistant/turn_off", `{"entity_id":"all"}`),
		facts: &reqmap.Facts{Live: &reqmap.LiveStates{EntityIDs: []string{
			"homeassistant.update_available", "light.kitchen", "lock.front_door",
		}}, Registries: &reqmap.Registries{}},
		disp: reqmap.Write, row: svcRow,
		checks: []string{
			"control entity:homeassistant.update_available", "control entity:light.kitchen", "control entity:lock.front_door",
			"control service:homeassistant.turn_off", "control service:light.turn_off", "control service:lock.turn_off",
		},
	},
	{
		name: "a script's run is looked up by its exact entity id, never a near-colliding one (D-84)",
		req:  rest("POST", "/api/services/script/turn_on", `{"entity_id":"script.morning"}`),
		facts: &reqmap.Facts{
			Live:  &reqmap.LiveStates{EntityIDs: []string{"script.good_morning", "script.morning"}},
			Reach: &reqmap.Reach{EntityIDs: []string{"script.morning"}},
			Registries: &reqmap.Registries{Entities: []reqmap.RegistryEntity{
				{EntityID: "script.good_morning", UniqueID: "gm"},
				{EntityID: "script.morning", UniqueID: "m"},
			}},
		},
		disp: reqmap.Write, row: svcRow,
		checks: []string{"control entity:script.morning", "control script:m (deny only)", "control script:m/run", "control service:script.turn_on"},
	},
	{
		name: "a script's run is looked up by its exact entity id, in either registry order",
		req:  rest("POST", "/api/services/script/turn_on", `{"entity_id":"script.good_morning"}`),
		facts: &reqmap.Facts{
			Live:  &reqmap.LiveStates{EntityIDs: []string{"script.good_morning", "script.morning"}},
			Reach: &reqmap.Reach{EntityIDs: []string{"script.good_morning"}},
			Registries: &reqmap.Registries{Entities: []reqmap.RegistryEntity{
				{EntityID: "script.morning", UniqueID: "m"},
				{EntityID: "script.good_morning", UniqueID: "gm"},
			}},
		},
		disp: reqmap.Write, row: svcRow,
		checks: []string{"control entity:script.good_morning", "control script:gm (deny only)", "control script:gm/run", "control service:script.turn_on"},
	},
	{
		name: "a light group forwards the call to its members: each needs control (D-106)",
		req:  rest("POST", "/api/services/light/turn_on", `{"entity_id":"light.all_lights"}`),
		facts: &reqmap.Facts{
			Live: &reqmap.LiveStates{
				EntityIDs: []string{"light.all_lights", "light.sofa", "light.desk"},
				Members:   map[string][]string{"light.all_lights": {"light.sofa", "light.desk"}},
			},
			Reach:      &reqmap.Reach{EntityIDs: []string{"light.all_lights"}},
			Registries: &reqmap.Registries{},
		},
		disp: reqmap.Write, row: svcRow,
		checks: []string{"control entity:light.all_lights", "control entity:light.desk", "control entity:light.sofa", "control service:light.turn_on"},
	},
	{
		name: "a scene forwards to every entity it sets, across domains, and groups of groups recurse once each (D-106)",
		req:  rest("POST", "/api/services/scene/turn_on", `{"entity_id":"scene.leave_home"}`),
		facts: &reqmap.Facts{
			Live: &reqmap.LiveStates{
				EntityIDs: []string{"scene.leave_home", "light.all_lights", "light.sofa", "lock.front_door"},
				Members: map[string][]string{
					"scene.leave_home": {"light.all_lights", "lock.front_door"},
					"light.all_lights": {"light.sofa", "light.all_lights"},
				},
			},
			Reach:      &reqmap.Reach{EntityIDs: []string{"scene.leave_home"}},
			Registries: &reqmap.Registries{},
		},
		disp: reqmap.Write, row: svcRow,
		checks: []string{
			"control entity:light.all_lights", "control entity:light.sofa", "control entity:lock.front_door",
			"control entity:scene.leave_home", "control service:scene.turn_on",
		},
	},
	{
		name: "a member an entity in the data names is forwarded to as well (D-106)",
		req:  rest("POST", "/api/services/scene/apply", `{"entities":{"light.all_lights":"on"}}`),
		facts: &reqmap.Facts{
			Live: &reqmap.LiveStates{
				EntityIDs: []string{"light.all_lights", "light.sofa"},
				Members:   map[string][]string{"light.all_lights": {"light.sofa"}},
			},
			Registries: &reqmap.Registries{},
		},
		disp: reqmap.Write, row: svcRow,
		checks: []string{"control entity:light.all_lights", "control entity:light.sofa", "control service:scene.apply"},
	},
	{
		name: "the typed address of every reached automation, script, helper and template entity is checked for denies (D-107)",
		req:  rest("POST", "/api/services/homeassistant/turn_off", `{"label_id":"prod"}`),
		facts: &reqmap.Facts{
			Live: &reqmap.LiveStates{
				EntityIDs: []string{
					"automation.heating_control", "automation.no_id", "script.prod_disarm", "input_boolean.alarm_armed",
					"input_boolean.renamed", "lock.front_door", "sensor.not_a_helper",
				},
				AutomationIDs: map[string]string{"automation.heating_control": "prod_heating"},
			},
			Reach: &reqmap.Reach{EntityIDs: []string{
				"automation.heating_control", "automation.no_id", "script.prod_disarm", "input_boolean.alarm_armed",
				"input_boolean.renamed", "lock.front_door", "sensor.not_a_helper",
			}},
			Registries: &reqmap.Registries{Entities: []reqmap.RegistryEntity{
				{EntityID: "script.prod_disarm", UniqueID: "prod_disarm", Platform: "script"},
				{EntityID: "input_boolean.alarm_armed", UniqueID: "alarm_armed", Platform: "input_boolean"},
				{EntityID: "input_boolean.renamed", UniqueID: "guest_mode", Platform: "input_boolean"},
				{EntityID: "lock.front_door", UniqueID: "blk-front_door_lock", Platform: "template"},
				{EntityID: "sensor.not_a_helper", UniqueID: "x", Platform: "mqtt"},
			}},
		},
		disp: reqmap.Write, row: svcRow,
		checks: []string{
			"control automation:prod_heating (deny only)",
			"control entity:automation.heating_control", "control entity:automation.no_id",
			"control entity:input_boolean.alarm_armed", "control entity:input_boolean.renamed",
			"control entity:lock.front_door", "control entity:script.prod_disarm", "control entity:sensor.not_a_helper",
			"control helper:input_boolean/alarm_armed (deny only)", "control helper:input_boolean/guest_mode (deny only)",
			"control helper:input_boolean/renamed (deny only)",
			"control script:prod_disarm (deny only)",
			"control service:automation.turn_off", "control service:homeassistant.turn_off", "control service:input_boolean.turn_off",
			"control service:lock.turn_off", "control service:script.turn_off", "control service:sensor.turn_off",
			"control template:blk-front_door_lock (deny only)", "control template:front_door_lock (deny only)",
		},
	},
	{
		name: "without the registries a script, helper or template address cannot be read: kind-wide, denies only (D-107)",
		req:  rest("POST", "/api/services/input_boolean/turn_off", `{"entity_id":"input_boolean.alarm_armed"}`),
		facts: &reqmap.Facts{
			Live:  &reqmap.LiveStates{EntityIDs: []string{"input_boolean.alarm_armed"}},
			Reach: &reqmap.Reach{EntityIDs: []string{"input_boolean.alarm_armed"}},
		},
		disp: reqmap.Write, row: svcRow,
		checks: []string{
			"control entity:input_boolean.alarm_armed", "control helper:* (deny only)",
			"control service:input_boolean.turn_off", "control template:* (deny only)",
		},
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
		{"every value of a list selector is asked", `{"entity_id":["light.a","light.b"],"device_id":["d1","d2"]}`,
			map[string]any{"entity_id": []any{"light.a", "light.b"}, "device_id": []any{"d1", "d2"}, "expand_group": true}},
		{"a list at the top level and under target keeps every value of both", `{"entity_id":["light.a","light.b"],"target":{"entity_id":["light.c","light.d"]}}`,
			map[string]any{"entity_id": []any{"light.a", "light.b", "light.c", "light.d"}, "expand_group": true}},
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
		rest("POST", "/api/foo/bar/baz", "{}"),
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
