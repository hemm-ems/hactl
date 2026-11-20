package reqmap

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// readsOf renders an analysis' reads as `<check>[ whole]`, sorted.
func readsOf(rs []tplRead) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		s := r.check.String()
		if r.check.Whole {
			s += " whole"
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

func tplTestFacts() (*LiveStates, *Registries) {
	live := &LiveStates{
		EntityIDs: []string{"sun.sun", "light.a", "light.b", "group.g", "lock.front", "zone.home", "person.p", "sensor.l", "input_boolean.pg_hunt_guard"},
		Members:   map[string][]string{"group.g": {"light.a"}},
	}
	reg := &Registries{
		Entities: []RegistryEntity{
			{ID: "uuid-light-a", EntityID: "light.a", DeviceID: "dev1"},
			{EntityID: "lock.front", AreaID: "kitchen"},
			{EntityID: "sensor.l", Labels: []string{"lbl"}},
		},
		Devices: []RegistryDevice{{ID: "dev1", AreaID: "kitchen"}},
		Areas:   []RegistryArea{{ID: "kitchen", FloorID: "ground"}},
	}
	return live, reg
}

// D-198, D-201, D-205: what a template reads, stated per construct.
func TestTemplateReads(t *testing.T) {
	live, reg := tplTestFacts()
	env := newTplEnv(map[string]any{
		"triggers": []any{map[string]any{"trigger": "state", "entity_id": "lock.front"}},
	}, live, reg)
	const (
		sunState = "read entity:sun.sun/state"
		lockAttr = "read entity:lock.front/attr/battery"
		lockName = "read entity:lock.front/attr/friendly_name"
	)
	cases := []struct {
		tpl     string
		reads   []string
		unknown []string
	}{
		// the hunt's over-refusals: each needs only what it reads
		{"{{ is_state('sun.sun', 'below_horizon') }}", []string{sunState}, nil},
		{"Sun is {{ states('sun.sun') }}", []string{sunState}, nil},
		{"Lights still on: {{ states.light | selectattr('state','eq','on') | map(attribute='name') | join(', ') }}",
			[]string{"read entity:light.a whole", "read entity:light.b whole"}, nil},
		{"{{ now().hour }}", nil, nil},
		{"{{ 255 if now().hour < 20 else 80 }}", nil, nil},
		// the state object, by what follows it
		{"{{ state_attr('lock.front', 'battery') }}", []string{lockAttr}, nil},
		{"{{ is_state_attr('lock.front', 'battery', 3) }}", []string{lockAttr}, nil},
		{"{{ states.lock.front.attributes.battery }}", []string{lockAttr}, nil},
		{"{{ states['lock.front'].attributes['battery'] }}", []string{lockAttr}, nil},
		{"{{ states.lock.front.attributes.get('battery') }}", []string{lockAttr}, nil},
		{"{{ states.lock.front.attributes.items() | list }}", []string{"read entity:lock.front/attr whole"}, nil},
		{"{{ states.lock.front.attributes }}", []string{"read entity:lock.front/attr whole"}, nil},
		{"{{ states.lock.front.name }}", []string{lockName}, nil},
		{"{{ states.lock.front.last_changed }}", []string{"read entity:lock.front/state"}, nil},
		{"{{ states.lock.front.entity_id }}", []string{"read entity:lock.front"}, nil},
		{"{{ states.lock.front }}", []string{"read entity:lock.front whole"}, nil},
		{"{{ states.lock.front.context.id }}", []string{"read entity:lock.front whole"}, nil},
		{"{{ state_attr('lock.front', x) }}", []string{"read entity:lock.front/attr whole"}, []string{"name x"}},
		{"{{ states('sun.sun', with_unit=True) }}", []string{"read entity:sun.sun whole"}, nil},
		{"{{ states('LOCK.Front') }}", []string{"read entity:lock.front/state"}, nil},
		{"{{ states('not an id') }}", nil, nil},
		// filter and test forms, per element
		{"{{ 'sun.sun' | states }}", []string{sunState}, nil},
		{"{{ 'sun.sun' is is_state('below_horizon') }}", []string{sunState}, nil},
		{"{{ ['light.a', 'light.b'] | select('is_state', 'on') | list }}", []string{"read entity:light.a/state", "read entity:light.b/state"}, nil},
		{"{{ ['lock.front'] | map('state_attr', 'battery') | list }}", []string{lockAttr}, nil},
		// whole domains, every state
		{"{% for s in states.light %}{{ s.state }}{% endfor %}", []string{"read entity:light.a whole", "read entity:light.b whole"}, nil},
		{"{{ states | count }}", []string{"read entity:group.g whole", "read entity:input_boolean.pg_hunt_guard whole", "read entity:light.a whole",
			"read entity:light.b whole", "read entity:lock.front whole", "read entity:person.p whole", "read entity:sensor.l whole",
			"read entity:sun.sun whole", "read entity:zone.home whole"}, nil},
		// expand: each named entity and its members, whole
		{"{{ expand('group.g') | map(attribute='name') | list }}", []string{"read entity:group.g whole", "read entity:light.a whole"}, nil},
		{"{{ expand(states.light) | list }}", []string{"read entity:light.a whole", "read entity:light.b whole"}, nil},
		{"{{ expand('zone.home') | list }}", []string{"read entity:person.p whole", "read entity:zone.home whole"}, nil},
		// registry lookups by id
		{"{{ area_entities('kitchen') }}", []string{"read entity:light.a", "read entity:lock.front"}, nil},
		{"{{ expand(area_entities('kitchen')) | list }}", []string{"read entity:light.a whole", "read entity:lock.front whole"}, nil},
		{"{{ floor_entities('ground') | map('states') | list }}", []string{"read entity:light.a", "read entity:light.a/state", "read entity:lock.front", "read entity:lock.front/state"}, nil},
		{"{{ label_entities('lbl') }}", []string{"read entity:sensor.l"}, nil},
		{"{{ device_entities('dev1') }}", []string{"read entity:light.a"}, nil},
		{"{{ area_devices('kitchen') }}", []string{"read device:dev1"}, nil},
		{"{{ floor_areas('ground') }}", []string{"read area:kitchen"}, nil},
		{"{{ area_entities('Kitchen Table') }}", nil, []string{"area_entities with a name or an unknown area"}},
		{"{{ label_entities('Some Label') }}", nil, []string{"label_entities with a name or an unused label"}},
		// the trigger object (D-201)
		{"{{ trigger.to_state.name }}", []string{lockName}, nil},
		{"{{ trigger.to_state.attributes.friendly_name }}", []string{lockName}, nil},
		{"{{ trigger.from_state.state }}", []string{"read entity:lock.front/state"}, nil},
		{"{{ trigger.to_state }}", []string{"read entity:lock.front whole"}, nil},
		{"{{ trigger }}", []string{"read entity:lock.front whole"}, nil},
		{"{{ trigger['to_state'] }}", []string{"read entity:lock.front whole"}, nil},
		{"{{ trigger.id }} {{ trigger.platform }} {{ trigger.for }}", nil, nil},
		{"{{ wait.trigger.to_state.state }}", []string{"read entity:lock.front/state"}, nil},
		{"{{ wait.completed }}", nil, nil},
		{"{{ states(trigger.entity_id) }}", []string{"read entity:lock.front/state"}, nil},
		// escapes (D-205) and folding (D-189)
		{"{{ states('input_boolean.pg_hunt_gu\\x61rd') }}", []string{"read entity:input_boolean.pg_hunt_guard/state"}, nil},
		{"{{ states('input_boolean.' ~ 'pg_hunt_guard') }}", []string{"read entity:input_boolean.pg_hunt_guard/state"}, nil},
		// unknowable
		{"{{ states(states('sun.sun')) }}", []string{sunState}, []string{"states with a computed entity id"}},
		{"{{ closest(states.person) }}", []string{"read entity:person.p whole"}, []string{"closest"}},
		{"{{ device_attr('dev1', 'name') }}", nil, []string{"device_attr"}},
		{"{{ ['lock.front'] | selectattr('entity_id', 'is_state', 'on') | list }}", nil, []string{"test is_state (through selectattr)"}},
		{"{% set states = 1 %}{{ states('sun.sun') }}", nil, []string{"states (rebound)"}},
		{"{{ x | map('states') | list }}", nil, []string{"name x", "filter states (through map) over a computed list"}},
	}
	for _, c := range cases {
		tpl, err := parseJinja(c.tpl)
		if err != nil {
			t.Errorf("%s: %v", c.tpl, err)
			continue
		}
		a := env.analyse(tpl)
		if got := readsOf(a.reads); fmt.Sprint(got) != fmt.Sprint(c.reads) && (len(got) != 0 || len(c.reads) != 0) {
			t.Errorf("%s:\n  reads %q\n  want  %q", c.tpl, got, c.reads)
		}
		if fmt.Sprint(a.out) != fmt.Sprint(c.unknown) && (len(a.out) != 0 || len(c.unknown) != 0) {
			t.Errorf("%s: unknowable %q, want %q", c.tpl, a.out, c.unknown)
		}
	}
}

// D-201: the trigger object hactl cannot read is unknowable; an unsafe event
// makes every use unknowable (D-203).
func TestTemplateReadsTriggerKinds(t *testing.T) {
	live, reg := tplTestFacts()
	cases := []struct {
		trig    map[string]any
		tpl     string
		reads   []string
		unknown []string
	}{
		{map[string]any{"trigger": "template", "value_template": "{{ true }}"}, "{{ trigger.to_state.state }}", nil, []string{"trigger.to_state (a trigger hactl cannot read)"}},
		{map[string]any{"trigger": "my_custom_kind"}, "{{ trigger }}", nil, []string{"trigger (a trigger hactl cannot read)"}},
		{map[string]any{"trigger": "time", "at": "07:00"}, "{{ trigger.to_state.state }}", nil, nil},
		{map[string]any{"trigger": "device", "device_id": "dev1", "domain": "light", "type": "turned_on", "entity_id": "uuid-light-a"}, "{{ trigger.to_state.state }}", []string{"read entity:light.a/state"}, nil},
		{map[string]any{"trigger": "device", "device_id": "dev1", "domain": "light", "type": "x"}, "{{ trigger.to_state.state }}", []string{"read entity:light.a/state"}, nil},
		{map[string]any{"trigger": "zone", "entity_id": "person.p", "zone": "zone.home", "event": "enter"}, "{{ trigger.zone.name }}", []string{"read entity:zone.home whole"}, nil},
		{map[string]any{"trigger": "event", "event_type": "call_service"}, "{{ trigger.event.data.service_data }}", nil, []string{"trigger (an event trigger on call_service)"}},
		{map[string]any{"trigger": "event", "event_type": "my_event"}, "{{ trigger.event.data.x }}", nil, nil},
		{map[string]any{"trigger": "state", "entity_id": "lock.front, light.a"}, "{{ trigger.to_state.state }}", []string{"read entity:light.a/state", "read entity:lock.front/state"}, nil},
	}
	for _, c := range cases {
		env := newTplEnv(map[string]any{"triggers": []any{c.trig}}, live, reg)
		tpl, err := parseJinja(c.tpl)
		if err != nil {
			t.Fatal(err)
		}
		a := env.analyse(tpl)
		if got := readsOf(a.reads); fmt.Sprint(got) != fmt.Sprint(c.reads) && (len(got) != 0 || len(c.reads) != 0) {
			t.Errorf("%v %s: reads %q, want %q", c.trig, c.tpl, got, c.reads)
		}
		if fmt.Sprint(a.out) != fmt.Sprint(c.unknown) && (len(a.out) != 0 || len(c.unknown) != 0) {
			t.Errorf("%v %s: unknowable %q, want %q", c.trig, c.tpl, a.out, c.unknown)
		}
	}
}

// D-203: the code's list is the decided one, exactly.
func TestStateEventTypesAreTheDecidedList(t *testing.T) {
	want := []string{"*", "call_service", "core_config_updated", "device_registry_updated", "entity_registry_updated", "logbook_entry", "state_changed", "state_reported"}
	if got := slices.Sorted(maps.Keys(stateEventTypes)); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("stateEventTypes %v, decided %v", got, want)
	}
}

// D-205: a string literal is decoded as jinja2 decodes it (Python's
// unicode-escape after backslashreplace); what hactl cannot decode exactly
// does not parse.
func TestJinjaStringEscapes(t *testing.T) {
	ok := []struct{ src, want string }{
		{`'gu\x61rd'`, "guard"},
		{`'gu\u0061rd'`, "guard"},
		{`'gu\U00000061rd'`, "guard"},
		{`'gu\141rd'`, "guard"},
		{`'a\x2cb'`, "a,b"},
		{`'\1234'`, "S4"},
		{`'\0'`, "\x00"},
		{`'a\nb\tc\rd'`, "a\nb\tc\rd"},
		{`'\a\b\f\v'`, "\a\b\f\v"},
		{`'\\'`, `\`},
		{`'\''`, `'`},
		{`"\""`, `"`},
		{`'\q'`, `\q`},
		{`'\.'`, `\.`},
		{"'a\\\nb'", "ab"},
		{"'a\r\nb'", "a\nb"},
		{"'a\rb'", "a\nb"},
		{`'\u00e9'`, "é"},
		{`'é'`, "é"},
	}
	for _, c := range ok {
		got, _, err := lexString(c.src, 0)
		if err != nil || got != c.want {
			t.Errorf("%q: %q (%v), want %q", c.src, got, err, c.want)
		}
	}
	for _, src := range []string{`'\x6'`, `'\x6g'`, `'\u006'`, `'\U0000006'`, `'\U00110000'`, `'\ud800'`, `'\N{LATIN SMALL LETTER A}'`, `'\é'`, `'abc`} {
		if _, _, err := lexString(src, 0); !errors.Is(err, errJinja) {
			t.Errorf("%q decoded (err %v)", src, err)
		}
	}
}

// D-198: tpl eval is checked as what its template reads; nothing for a
// template that reads no state; `read system:template` only for what hactl
// cannot name.
func TestTemplateEvalChecks(t *testing.T) {
	live, reg := tplTestFacts()
	cases := []struct {
		tpl    string
		checks []string
		ok     bool
	}{
		{"{{ now().hour }}", nil, true},
		{"plain text", nil, true},
		{"{{ states('sun.sun') }}", []string{"read entity:sun.sun/state"}, true},
		{"{{ states.lock.front }}", []string{"read entity:lock.front whole"}, true},
		{"{{ states(states('sun.sun')) }}", nil, false},
		{"{{ trigger.to_state }}", nil, false},
		{"{{ device_attr('x', 'y') }}", nil, false},
		{"{{ states('sun.sun' }}", nil, false},
	}
	for _, c := range cases {
		got, ok := TemplateEvalChecks(c.tpl, live, reg)
		var strs []string
		for _, ch := range got {
			s := ch.String()
			if ch.Whole {
				s += " whole"
			}
			strs = append(strs, s)
		}
		if ok != c.ok || fmt.Sprint(strs) != fmt.Sprint(c.checks) && (len(strs) != 0 || len(c.checks) != 0) {
			t.Errorf("%s: %q (%v), want %q (%v)", c.tpl, strs, ok, c.checks, c.ok)
		}
	}
	// Without the live states, a domain's entities cannot be named.
	if _, ok := TemplateEvalChecks("{{ states.light | list }}", nil, nil); ok {
		t.Error("states.light without the live set: named")
	}
	r := Classify(Request{Transport: REST, Method: "POST", Path: "/api/template", Body: []byte(`{"template":"{{ is_state('sun.sun','below_horizon') }}"}`)},
		&Facts{Live: live, Registries: reg})
	if r.Disposition != Read || len(r.Checks) != 1 || r.Checks[0].String() != "read entity:sun.sun/state" {
		t.Errorf("POST /api/template is_state: %v %v", r.Disposition, r.Checks)
	}
	r = Classify(Request{Transport: REST, Method: "POST", Path: "/api/template", Body: []byte(`{"template":"{{ now().hour }}"}`)}, nil)
	if r.Disposition != Read || len(r.Checks) != 0 {
		t.Errorf("POST /api/template now().hour: %v %v", r.Disposition, r.Checks)
	}
	// What hactl cannot name, and a body it cannot decode, read system:template.
	for _, body := range []string{`{"template":"{{ device_attr('a','b') }}"}`, `{"template":5}`, `{}`, `not json`} {
		r = Classify(Request{Transport: REST, Method: "POST", Path: "/api/template", Body: []byte(body)}, nil)
		if len(r.Checks) != 1 || r.Checks[0].String() != "read system:template" {
			t.Errorf("POST /api/template %s: %v %v", body, r.Disposition, r.Checks)
		}
	}
}

func candidateOf(t *testing.T, src string) any {
	t.Helper()
	var doc any
	if err := yaml.Unmarshal([]byte(src), &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func templateReadsOf(rs []TemplateRead) []string {
	out := make([]string, 0, len(rs))
	for _, r := range rs {
		s := r.Path + " " + r.Check.String()
		if r.Check.Whole {
			s += " whole"
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// D-202: a template-free condition or trigger reads what it compares.
func TestCandidateUsesReadsWhatConditionsCompare(t *testing.T) {
	live, reg := tplTestFacts()
	u := CandidateUses(candidateOf(t, `
triggers:
  - trigger: state
    entity_id: [lock.front]
    attribute: battery
  - trigger: numeric_state
    entity_id: sensor.l
    above: 3
  - trigger: zone
    entity_id: person.p
    zone: zone.home
conditions:
  - condition: state
    entity_id: lock.front
    attribute: friendly_name
    state: Xylophone
  - condition: or
    conditions:
      - condition: state
        entity_id: uuid-light-a
        state: "on"
  - condition: device
    device_id: dev1
    domain: light
    type: is_on
    entity_id: uuid-light-a
actions: []
`), live, reg)
	want := []string{
		"/conditions/0 read entity:lock.front/attr/friendly_name",
		"/conditions/1/conditions/0 read entity:light.a/state",
		"/conditions/2 read entity:light.a/state",
		"/triggers/0 read entity:lock.front/attr/battery",
		"/triggers/1 read entity:sensor.l/state",
		"/triggers/2 read entity:person.p whole",
		"/triggers/2 read entity:zone.home whole",
	}
	if got := templateReadsOf(u.Compared); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("compared\n  %q\nwant\n  %q", got, want)
	}
}

// D-204: rebinding a runtime name is refused, wherever it happens.
func TestCandidateUsesRefusesReservedRebinds(t *testing.T) {
	cases := map[string]string{
		"variables":         "actions:\n  - variables: {trigger: 0}\n",
		"trigger_variables": "trigger_variables: {this: 1}\n",
		"fields":            "fields: {wait: {}}\nsequence: []\n",
		"response_variable": "actions:\n  - action: x.y\n    response_variable: trigger\n",
		"for":               "actions:\n  - action: logbook.log\n    data: {message: \"{% for trigger in [] %}{% endfor %}x\"}\n",
		"set":               "actions:\n  - action: logbook.log\n    data: {message: \"{% set repeat = 1 %}x\"}\n",
		"with":              "actions:\n  - action: logbook.log\n    data: {message: \"{% with context = 1 %}x{% endwith %}\"}\n",
		"namespace attr":    "actions:\n  - action: logbook.log\n    data: {message: \"{% set trigger.x = 1 %}x\"}\n",
		"macro argument":    "actions:\n  - action: logbook.log\n    data: {message: \"{% macro m(trigger) %}{% endmacro %}x\"}\n",
		"import as":         "actions:\n  - action: logbook.log\n    data: {message: \"{% import 'x' as wait %}x\"}\n",
	}
	for name, src := range cases {
		u := CandidateUses(candidateOf(t, src), &LiveStates{}, nil)
		found := false
		for _, d := range u.Undecidable {
			found = found || d.Rule == RuleReservedRebind
		}
		if !found {
			t.Errorf("%s: not refused (%v)", name, u.Undecidable)
		}
	}
	// Not a rebind: a loop variable of another name, a keyword argument.
	for _, src := range []string{
		"actions:\n  - action: logbook.log\n    data: {message: \"{% for t in [] %}{{ t }}{% endfor %}{{ dict(trigger=1) }}\"}\n",
		"actions:\n  - variables: {triggered: 1}\n",
	} {
		u := CandidateUses(candidateOf(t, src), &LiveStates{}, nil)
		for _, d := range u.Undecidable {
			if d.Rule == RuleReservedRebind {
				t.Errorf("%q refused as a rebind", src)
			}
		}
	}
}

// Hunt 3 (2026-11-20), ported from its pre-screen: what CandidateUses says of
// each route the live hunt took. Each breach route is now unknowable,
// refused or checked as the read it makes.
func TestCandidateUsesHunt3Routes(t *testing.T) {
	live := &LiveStates{EntityIDs: []string{
		"input_boolean.pg_hunt_guard", "input_boolean.pg_hunt_ok", "input_boolean.pg_hunt_secret",
		"light.pg_hunt_hall", "light.pg_hunt_bedroom",
	}}
	unknowable := func(u CandidateUse, construct string) bool {
		for _, x := range u.Templates {
			if strings.Contains(x.Construct, construct) {
				return true
			}
		}
		return false
	}
	refused := func(u CandidateUse, rule string) bool {
		for _, d := range u.Undecidable {
			if d.Rule == rule {
				return true
			}
		}
		return false
	}
	cases := []struct {
		name, src string
		ok        func(CandidateUse) bool
	}{
		{"wait_trigger", `
triggers: [{trigger: event, event_type: pg_h3_fire}]
actions:
  - wait_for_trigger: [{trigger: event, event_type: state_changed}]
  - action: logbook.log
    data: {name: H3, message: "{{ wait.trigger.event.data.new_state.name }}"}
`, func(u CandidateUse) bool { return unknowable(u, "an event trigger on state_changed") }},
		{"respvar_trigger", `
triggers: [{trigger: event, event_type: state_changed}]
actions:
  - action: logbook.log
    data: {name: H3, message: "{{ trigger.event.data.new_state.name }}"}
  - if: "{{ false }}"
    then:
      - action: input_boolean.turn_on
        target: {entity_id: input_boolean.pg_hunt_ok}
        response_variable: trigger
`, func(u CandidateUse) bool {
			return refused(u, RuleReservedRebind) && unknowable(u, "an event trigger on state_changed")
		}},
		{"vars_trigger", `
triggers: [{trigger: event, event_type: state_changed}]
actions:
  - action: logbook.log
    data: {name: H3, message: "{{ trigger.event.data.new_state.name }}"}
  - variables: {trigger: 0}
`, func(u CandidateUse) bool {
			return refused(u, RuleReservedRebind) && unknowable(u, "an event trigger on state_changed")
		}},
		{"for_bound_trigger", `
triggers: [{trigger: event, event_type: state_changed}]
actions:
  - action: logbook.log
    data: {name: H3, message: "{% for trigger in [] %}{% endfor %}{{ trigger.event.data.new_state.name }}"}
`, func(u CandidateUse) bool { return refused(u, RuleReservedRebind) }},
		{"baseline_state_changed", `
triggers: [{trigger: event, event_type: state_changed}]
actions:
  - action: logbook.log
    data: {name: H3, message: "{{ trigger.event.data.new_state.name }}"}
`, func(u CandidateUse) bool { return unknowable(u, "an event trigger on state_changed") }},
		{"call_service", `
triggers: [{trigger: event, event_type: call_service}]
actions:
  - action: logbook.log
    data: {name: H3, message: "{{ trigger.event.data.service_data }}"}
`, func(u CandidateUse) bool { return unknowable(u, "an event trigger on call_service") }},
		{"escape_scene", `
triggers: [{trigger: event, event_type: pg_h3_fire}]
actions:
  - action: scene.apply
    data:
      entities: "{{ {'input_boolean.pg_hunt_gu\\x61rd': 'on'} }}"
`, func(u CandidateUse) bool {
			if len(u.Actions) != 1 {
				return false
			}
			ents, _ := u.Actions[0].Data["entities"].(map[string]any)
			_, named := ents["input_boolean.pg_hunt_guard"]
			return named
		}},
		{"to_state_name", `
triggers: [{trigger: state, entity_id: input_boolean.pg_hunt_secret}]
sensor:
  - name: s
    state: "{{ trigger.to_state.name }}"
`, func(u CandidateUse) bool {
			return slices.Contains(templateReadsOf(u.TemplateReads), "/sensor/0/state read entity:input_boolean.pg_hunt_secret/attr/friendly_name")
		}},
	}
	for _, c := range cases {
		if u := CandidateUses(candidateOf(t, c.src), live, nil); !c.ok(u) {
			t.Errorf("%s: actions=%v undecidable=%v templates=%v reads=%v", c.name, u.Actions, u.Undecidable, u.Templates, templateReadsOf(u.TemplateReads))
		}
	}
}

// TestTemplateReadsFoldLiteralForLoops (D-210): a for variable over a list
// that folds names each of its values, so `states(e)` in the loop reads what
// the list names; a loop over something computed, a variable another target
// rebinds, and a tuple target stay unknowable.
func TestTemplateReadsFoldLiteralForLoops(t *testing.T) {
	live, reg := tplTestFacts()
	env := newTplEnv(map[string]any{"variables": map[string]any{"my_locks": []any{"lock.front"}}}, live, reg)
	const (
		sunState  = "read entity:sun.sun/state"
		lockState = "read entity:lock.front/state"
		lockAttr  = "read entity:lock.front/attr/battery"
		computed  = "states with a computed entity id"
	)
	cases := []struct {
		tpl     string
		reads   []string
		unknown []string
	}{
		{"{% for e in ['lock.front', 'sun.sun'] %}{{ states(e) }}{% endfor %}", []string{lockState, sunState}, nil},
		{"{% for e in ['LOCK.Front'] %}{{ state_attr(e, 'battery') }}{% endfor %}", []string{lockAttr}, nil},
		{"{% for e in ['light.a', 'light.b'] %}{% if is_state(e, 'on') %}on{% endif %}{% endfor %}",
			[]string{"read entity:light.a/state", "read entity:light.b/state"}, nil},
		{"{% for e in ['lock.front'] + ['sun.sun'] %}{{ states(e) }}{% endfor %}", []string{lockState, sunState}, nil},
		{"{% for e in my_locks %}{{ states(e) }}{% endfor %}", []string{lockState}, nil},
		{"{% for e in [] %}{{ states(e) }}{% endfor %}", nil, nil},
		// not folded
		{"{% for e in xs %}{{ states(e) }}{% endfor %}", nil, []string{"states with a computed entity id", "name xs"}},
		{"{% for e in ['lock.front'] %}{% endfor %}{% set e = x %}{{ states(e) }}", nil, []string{computed, "name x"}},
		{"{% for k, v in [['lock.front', 1]] %}{{ states(k) }}{% endfor %}", nil, []string{computed}},
		{"{% for e in ['lock.front', 1] %}{{ states(e) }}{% endfor %}", nil, []string{computed}},
	}
	for _, c := range cases {
		tpl, err := parseJinja(c.tpl)
		if err != nil {
			t.Errorf("%s: %v", c.tpl, err)
			continue
		}
		a := env.analyse(tpl)
		if got := readsOf(a.reads); fmt.Sprint(got) != fmt.Sprint(c.reads) && (len(got) != 0 || len(c.reads) != 0) {
			t.Errorf("%s:\n  reads %q\n  want  %q", c.tpl, got, c.reads)
		}
		if fmt.Sprint(a.out) != fmt.Sprint(c.unknown) && (len(a.out) != 0 || len(c.unknown) != 0) {
			t.Errorf("%s: unknowable %q, want %q", c.tpl, a.out, c.unknown)
		}
	}
}
