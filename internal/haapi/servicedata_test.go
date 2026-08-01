package haapi

import (
	"encoding/json"
	"strings"
	"testing"
)

// liveDescriptors are four services copied verbatim out of a real instance's
// GET /api/services (2026-07-31, HA 2026.7.4). They are the four shapes the
// rule has to tell apart, and each one is a service whose behaviour under
// --confirm was measured against that instance rather than reasoned about:
//
//	automation.trigger  fields + target      `target:` wrapper → 400
//	input_boolean.toggle no fields + target   `bogus` → 400, `label_id` → 200
//	mqtt.publish        SECTIONED fields      `qos` → 200, `publish_options` → 400
//	script.pg_core      no fields, no target  `bogus` → 200, the script ran
const liveDescriptors = `{
  "automation.trigger": {"fields": {"skip_condition": {"selector": {"boolean": null}}},
                         "target": {"entity": [{"domain": ["automation"]}]}},
  "input_boolean.toggle": {"fields": {}, "target": {"entity": [{"domain": ["input_boolean"]}]}},
  "mqtt.publish": {"fields": {"topic": {"selector": {"text": null}},
                              "payload": {"selector": {"text": null}},
                              "publish_options": {"collapsed": true,
                                                  "fields": {"evaluate_payload": {"selector": {"boolean": null}},
                                                             "qos": {"selector": {"select": null}},
                                                             "retain": {"selector": {"boolean": null}},
                                                             "message_expiry_interval": {"selector": {"number": null}}}}}},
  "script.pg_core_script": {"fields": {}, "response": {"optional": true}}
}`

func liveDescriptor(t *testing.T, key string) *ServiceDescriptor {
	t.Helper()
	var all map[string]ServiceDescriptor
	if err := json.Unmarshal([]byte(liveDescriptors), &all); err != nil {
		t.Fatalf("decoding the captured registry: %v", err)
	}
	desc, ok := all[key]
	if !ok {
		t.Fatalf("no captured descriptor for %s", key)
	}
	return &desc
}

// TestAcceptedFieldsFlattensSectionsAndIsSorted pins both halves of the rule
// the preview refuses on, and the sortedness the maprange surface requires.
//
// The section case is the one that would have made the fix worse than the
// defect: `mqtt.publish` groups four of its six fields under a `publish_options`
// UI section, and a rule reading only the top level would have refused
// `--data '{"topic":…,"qos":0}'` — which the instance answers 200 — while
// accepting `publish_options`, which it answers 400.
func TestAcceptedFieldsFlattensSectionsAndIsSorted(t *testing.T) {
	cases := []struct {
		service string
		want    string
	}{
		{"automation.trigger", "area_id, device_id, entity_id, floor_id, label_id, skip_condition"},
		{"input_boolean.toggle", "area_id, device_id, entity_id, floor_id, label_id"},
		{"mqtt.publish", "evaluate_payload, message_expiry_interval, payload, qos, retain, topic"},
		{"script.pg_core_script", ""},
	}
	for _, tc := range cases {
		t.Run(tc.service, func(t *testing.T) {
			got := strings.Join(liveDescriptor(t, tc.service).AcceptedFields(), ", ")
			if got != tc.want {
				t.Errorf("AcceptedFields() = %q, want %q", got, tc.want)
			}
			// Run again: a map walk that leaked its iteration order would show
			// up here rather than as a flaky user-visible refusal.
			if again := strings.Join(liveDescriptor(t, tc.service).AcceptedFields(), ", "); again != got {
				t.Errorf("AcceptedFields() is not deterministic: %q then %q", got, again)
			}
		})
	}
}

// TestUnknownFieldsRefusesWhatHomeAssistantRefuses walks the payloads that were
// measured against a live instance, in both directions. The false-refusal half
// is the important one: H-2 makes a preview that is stricter than the confirmed
// run the same defect pointing the other way.
func TestUnknownFieldsRefusesWhatHomeAssistantRefuses(t *testing.T) {
	cases := []struct {
		name    string
		service string
		data    string
		want    string // sorted, comma-joined; "" = HA accepts this payload
	}{
		{"the target wrapper, finding #42", "automation.trigger",
			`{"target":{"entity_id":["automation.x"]}}`, "target"},
		{"an undeclared key", "input_boolean.toggle",
			`{"entity_id":"input_boolean.x","bogus_key_xyz":1}`, "bogus_key_xyz"},
		{"a declared field", "automation.trigger",
			`{"entity_id":"automation.x","skip_condition":true}`, ""},
		{"every target selector", "input_boolean.toggle",
			`{"entity_id":"a.b","device_id":"d","area_id":"a","label_id":"l","floor_id":"f"}`, ""},
		{"a field nested in a section", "mqtt.publish",
			`{"topic":"t","payload":"p","qos":0}`, ""},
		{"the section itself", "mqtt.publish",
			`{"topic":"t","payload":"p","publish_options":{"qos":0}}`, "publish_options"},
		{"a service that publishes no schema takes anything", "script.pg_core_script",
			`{"bogus_key_xyz":1}`, ""},
		{"a selector on a service with no target", "script.pg_core_script",
			`{"entity_id":"script.x"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var data map[string]any
			if err := json.Unmarshal([]byte(tc.data), &data); err != nil {
				t.Fatalf("decoding the payload: %v", err)
			}
			got := strings.Join(liveDescriptor(t, tc.service).UnknownFields(data), ", ")
			if got != tc.want {
				t.Errorf("UnknownFields(%s) = %q, want %q", tc.data, got, tc.want)
			}
		})
	}
}

// TestTargetsAnythingSeesTheTwoExtremes covers finding #44's actual shape. HA
// selects NOTHING for a targeted service with no selector (helpers/target.py
// returns before it looks at an entity) and EVERYTHING for `entity_id: all`
// (helpers/service.py `target_all_entities`). The finding read the first as
// the second.
func TestTargetsAnythingSeesTheTwoExtremes(t *testing.T) {
	cases := []struct {
		name               string
		data               string
		targeted, matchAll bool
	}{
		{"no data at all", `{}`, false, false},
		{"data with no selector", `{"skip_condition":true}`, false, false},
		{"one entity", `{"entity_id":"light.kitchen"}`, true, false},
		{"an empty list is not a target", `{"entity_id":[]}`, false, false},
		{"HA's none sentinel is not a target", `{"entity_id":"none"}`, false, false},
		{"HA's all sentinel is every entity", `{"entity_id":"all"}`, true, true},
		{"an area is a target", `{"area_id":"kitchen"}`, true, false},
		{"a label is a target", `{"label_id":"outdoor"}`, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var data map[string]any
			if err := json.Unmarshal([]byte(tc.data), &data); err != nil {
				t.Fatalf("decoding the payload: %v", err)
			}
			targeted, matchAll := TargetsAnything(data)
			if targeted != tc.targeted || matchAll != tc.matchAll {
				t.Errorf("TargetsAnything(%s) = (%v, %v), want (%v, %v)",
					tc.data, targeted, matchAll, tc.targeted, tc.matchAll)
			}
		})
	}
}

// TestMalformedEntityIDsMatchesHomeAssistantsRefusal — an entity_id HA answers
// 400 to is refused in the preview; one it answers 200 to (a well-formed id
// that names nothing) is not. The second half is not an oversight: a service
// call against an absent entity is a legitimate no-op, so refusing it would
// make the preview stricter than the confirmed run.
func TestMalformedEntityIDsMatchesHomeAssistantsRefusal(t *testing.T) {
	cases := []struct {
		data string
		want string
	}{
		{`{"entity_id":"not_an_entity_id"}`, "not_an_entity_id"},
		{`{"entity_id":"input_boolean.NOPE!"}`, "input_boolean.NOPE!"},
		{`{"entity_id":["light.a","light B"]}`, "light B"},
		{`{"entity_id":"input_boolean.absent_but_well_formed"}`, ""},
		{`{"entity_id":"all"}`, ""},
		{`{"entity_id":"none"}`, ""},
		{`{"entity_id":[]}`, ""},
		{`{"area_id":"kitchen"}`, ""},
		{`{"entity_id":42}`, ""}, // a type error is HA's to report, in its own words
	}
	for _, tc := range cases {
		t.Run(tc.data, func(t *testing.T) {
			var data map[string]any
			if err := json.Unmarshal([]byte(tc.data), &data); err != nil {
				t.Fatalf("decoding the payload: %v", err)
			}
			got := strings.Join(MalformedEntityIDs(data), ", ")
			if got != tc.want {
				t.Errorf("MalformedEntityIDs(%s) = %q, want %q", tc.data, got, tc.want)
			}
		})
	}
}

// TestValidEntityIDMirrorsHomeAssistantsRegex is the unit half of the mirror;
// TestOracleEntityIDRule (integration) is the half that asks HA itself.
func TestValidEntityIDMirrorsHomeAssistantsRegex(t *testing.T) {
	valid := []string{"a.b", "1.2", "sensor.time", "binary_sensor.door_1", "input_boolean.pg_w5_renamed"}
	invalid := []string{
		"", "nodomain", "a.", ".b", "a.b.c",
		"input_boolean.pg w5 bad", "input_boolean.PG_w5_Bad!", "input_boolean.pg_w5_🔥bad",
		"_a.b", "a_.b", "a.b_", "a._b", "a__b.c", "a.b__c", "ä.b",
	}
	for _, id := range valid {
		if !ValidEntityID(id) {
			t.Errorf("ValidEntityID(%q) = false, want true", id)
		}
	}
	for _, id := range invalid {
		if ValidEntityID(id) {
			t.Errorf("ValidEntityID(%q) = true, want false", id)
		}
	}
}
