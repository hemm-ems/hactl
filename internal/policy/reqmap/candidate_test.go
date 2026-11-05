package reqmap_test

import (
	"slices"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

func candidateDoc(t *testing.T, src string) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal([]byte(src), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

var candLive = &reqmap.LiveStates{EntityIDs: []string{
	"binary_sensor.door", "light.hall", "light.kitchen", "lock.front", "sensor.temp", "script.pg_x", "input_boolean.guard",
}}

var candReg = &reqmap.Registries{Entities: []reqmap.RegistryEntity{
	{ID: "0eba1f20e56510b59c0d5cffbbf681c7", EntityID: "light.kitchen", DeviceID: "dev1"},
	{ID: "aa11", EntityID: "light.hall", DeviceID: "dev2"},
	{ID: "bb22", EntityID: "sensor.temp", DeviceID: "dev2"},
}}

func actionsOf(u reqmap.CandidateUse) []string {
	out := make([]string, 0, len(u.Actions))
	for _, a := range u.Actions {
		out = append(out, a.Domain+"."+a.Service+"@"+a.Path)
	}
	return out
}

func rulesOf(u reqmap.CandidateUse) []string {
	out := make([]string, 0, len(u.Undecidable))
	for _, x := range u.Undecidable {
		out = append(out, x.Rule+"@"+x.Path)
	}
	return out
}

// TestCandidateUsesReadsActionsAtAnyDepth — every action, nested in choose,
// if, parallel, repeat and sequence, is one service call; the trigger's and
// the condition's entities are mentions (read, D-133).
func TestCandidateUsesReadsActionsAtAnyDepth(t *testing.T) {
	u := reqmap.CandidateUses(candidateDoc(t, `
id: pg_a
triggers:
  - trigger: state
    entity_id: binary_sensor.door
conditions:
  - condition: template
    value_template: "{{ states('sensor.temp') | float > 3 }}"
actions:
  - choose:
      - conditions: []
        sequence:
          - action: lock.unlock
            target: {entity_id: lock.front}
  - if: []
    then:
      - parallel:
          - action: light.turn_on
            data: {brightness: 10}
            entity_id: light.hall
  - repeat:
      count: 2
      sequence:
        - service: script.pg_x
  - scene: scene.evening
  - event: pg_fire
    event_data: {action: OPEN_GARAGE}
`), candLive, candReg)
	wantActions := []string{
		"lock.unlock@/actions/0/choose/0/sequence/0",
		"light.turn_on@/actions/1/then/0/parallel/0",
		"script.pg_x@/actions/2/repeat/sequence/0",
		"scene.turn_on@/actions/3",
	}
	if got := actionsOf(u); !slices.Equal(got, wantActions) {
		t.Errorf("actions = %q, want %q", got, wantActions)
	}
	if want := []string{"binary_sensor.door", "sensor.temp"}; !slices.Equal(u.Read, want) {
		t.Errorf("read = %q, want %q", u.Read, want)
	}
	if len(u.Undecidable) != 0 {
		t.Errorf("undecidable = %q, want none (an event's data `action: OPEN_GARAGE` is no service)", rulesOf(u))
	}
	if got := u.Actions[0].Data["entity_id"]; got != "lock.front" {
		t.Errorf("target not passed as call data: %v", u.Actions[0].Data)
	}
}

// TestCandidateUsesRefusesWhatItCannotRead — a templated service name or
// target cannot be decided off the candidate: fail-closed, never unchecked.
func TestCandidateUsesRefusesWhatItCannotRead(t *testing.T) {
	u := reqmap.CandidateUses(candidateDoc(t, `
sequence:
  - action: "{{ 'lock.unlock' }}"
  - action: light.turn_on
    target:
      entity_id: "{{ expand('group.x') | map(attribute='entity_id') | list }}"
  - action: light.turn_on
    data_template: "{{ {'entity_id': 'lock.front'} }}"
  - action: notify.mobile
    data:
      message: "door is {{ states('binary_sensor.door') }}"
`), candLive, candReg)
	want := []string{
		reqmap.RuleActionServiceTemplate + "@/sequence/0/action",
		reqmap.RuleActionTargetTemplate + "@/sequence/1",
		reqmap.RuleActionTargetTemplate + "@/sequence/2",
	}
	if got := rulesOf(u); !slices.Equal(got, want) {
		t.Errorf("undecidable = %q, want %q", got, want)
	}
	// A template in an action's data that is not its target is a mention.
	if !slices.Equal(u.Read, []string{"binary_sensor.door"}) {
		t.Errorf("read = %q, want the notify message's sensor", u.Read)
	}
}

// TestCandidateUsesResolvesDeviceActions — D-137: HA offers the registry
// uuid, and accepts the entity_id too; an unresolvable one is refused. A
// device trigger names its entity by uuid as well: a mention.
func TestCandidateUsesResolvesDeviceActions(t *testing.T) {
	u := reqmap.CandidateUses(candidateDoc(t, `
triggers:
  - trigger: device
    device_id: dev2
    domain: sensor
    type: value
    entity_id: bb22
actions:
  - device_id: dev1
    domain: light
    type: turn_on
    entity_id: 0eba1f20e56510b59c0d5cffbbf681c7
  - device_id: dev2
    domain: light
    type: turn_off
    entity_id: light.hall
  - device_id: dev9
    domain: light
    type: turn_off
    entity_id: ffff
`), candLive, candReg)
	if want := []string{"light.hall", "light.kitchen"}; !slices.Equal(u.Switch, want) {
		t.Errorf("switch = %q, want %q", u.Switch, want)
	}
	if want := []string{"sensor.temp"}; !slices.Equal(u.Read, want) {
		t.Errorf("read = %q, want %q", u.Read, want)
	}
	if want := []string{reqmap.RuleDeviceActionUnresolved + "@/actions/2/entity_id"}; !slices.Equal(rulesOf(u), want) {
		t.Errorf("undecidable = %q, want %q", rulesOf(u), want)
	}
	// Without registries a uuid cannot be resolved: refused, never unchecked.
	if got := reqmap.CandidateUses(candidateDoc(t, "actions:\n  - {device_id: dev1, domain: light, type: turn_on, entity_id: 0eba1f20e56510b59c0d5cffbbf681c7}\n"), candLive, nil); len(got.Undecidable) != 1 {
		t.Errorf("unread registries: undecidable = %q, want one refusal", rulesOf(got))
	}
}

// TestCandidateUsesIsCanonical — the answer is a function of the candidate,
// never of map iteration order: the same candidate gives the same answer
// every time, in sorted order.
func TestCandidateUsesIsCanonical(t *testing.T) {
	src := `
actions:
  - action: light.turn_on
    target: {entity_id: [light.hall, light.kitchen], area_id: x}
    data: {brightness: 1, color_name: "{{ states('sensor.temp') }}"}
triggers:
  - {trigger: state, entity_id: [lock.front, binary_sensor.door, input_boolean.guard]}
`
	first := reqmap.CandidateUses(candidateDoc(t, src), candLive, candReg)
	for range 50 {
		got := reqmap.CandidateUses(candidateDoc(t, src), candLive, candReg)
		if !slices.Equal(got.Read, first.Read) || !slices.Equal(got.Switch, first.Switch) || !slices.Equal(actionsOf(got), actionsOf(first)) {
			t.Fatalf("answer changed between runs: %+v vs %+v", got, first)
		}
	}
	if !slices.IsSorted(first.Read) {
		t.Errorf("read not sorted: %q", first.Read)
	}
	if want := []string{"binary_sensor.door", "input_boolean.guard", "lock.front", "sensor.temp"}; !slices.Equal(first.Read, want) {
		t.Errorf("read = %q, want %q", first.Read, want)
	}
}
