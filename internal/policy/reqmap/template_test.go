package reqmap

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// D-188 as refined by D-198: the safe-construct allowlist, read by a real
// Jinja parser; what reads state is unknowable only where hactl cannot name
// the read (no live states and no registries here).
func TestTemplateUnsafeConstructs(t *testing.T) {
	env := &tplEnv{vars: map[string]*tplVar{"this": {}, "trigger": {}, "e": {value: "x", folded: true}, "bad": {unsafe: "states"}},
		trig: trigFacts{known: true}}
	cases := []struct {
		tpl  string
		want []string // nil: safe
	}{
		{"{{ now() }}", nil},
		{"{{ now().hour > 7 and utcnow() < today_at('22:00') }}", nil},
		{"{{ as_timestamp(now()) - as_timestamp(as_datetime('2026-01-01')) > timedelta(hours=1).total_seconds() }}", nil},
		{"{{ 'a' ~ 'b' | upper }}", nil},
		{"{{ [1,2,3] | map('int') | select('odd') | list | length }}", nil},
		{"{{ trigger.to_state.state }} {{ this.attributes.friendly_name }}", nil},
		{"{% for x in range(3) %}{{ loop.index ~ x }}{% endfor %}", nil},
		{"{% set ns = namespace(a=1) %}{% set ns.a = 2 %}{{ ns.a }}", nil},
		{"{% if e == 'x' %}yes{% elif e is string %}s{% else %}no{% endif %}", nil},
		{"{{ x if e else 2 }}", []string{"name x"}},
		// D-198: a read hactl names is no unknowable construct.
		{"{{ states('lock.a') }}", nil},
		{"{{ states.lock.a.state }}", nil},
		{"{{ state_attr('input_boolean.pg_hunt_' ~ 'secret','friendly_name') }}", nil},
		{"{{ is_state('lock.a', 'on') }}", nil},
		{"{{ 'lock.a' | states }}", nil},
		{"{{ 'lock.a' is is_state('on') }}", nil},
		{"{{ ['lock.a'] | select('is_state', 'on') | list }}", nil},
		{"{{ ['lock.a'] | map('state_attr', 'x') | list }}", nil},
		{"{{ expand('group.x') }}", nil},
		{"{% set x = states('lock.a') %}{{ x }}", nil},
		// … and one it cannot name is.
		{"{{ states(states('lock.a')) }}", []string{"states with a computed entity id"}},
		{"{{ state_attr(e ~ now(), 'x') }}", []string{"state_attr with a computed entity id"}},
		{"{{ ['lock.a'] | selectattr('x', 'is_state_attr', 'y') | list }}", []string{"test is_state_attr (through selectattr)"}},
		{"{{ ['lock.a'] | map(e) | list }}", []string{"filter map with a computed name"}},
		{"{{ states.input_boolean | selectattr('object_id','search','secr') | map(attribute='name') | join(',') }}", []string{"states.input_boolean (live entities unread)"}},
		{"{{ area_entities('kitchen') }}", []string{"area_entities (registries unread)"}},
		{"{{ distance('zone.home') }}", []string{"distance"}},
		{"{{ trigger.to_state.state }}", nil},
		{"{{ bad }}", []string{"variable bad (from states)"}},
		{"{{ my_custom_fn() }}", []string{"name my_custom_fn"}},
		{"{{ 1 | my_filter }}", []string{"filter my_filter"}},
		{"{% macro m() %}{{ 1 }}{% endmacro %}{{ m() }}", []string{"name m", "tag macro", "tag endmacro"}},
		{"{% from 'x.jinja' import y %}{{ y }}", []string{"name y", "tag from"}},
		{"{% if false %}{% set states = 1 %}{% endif %}{{ states('lock.a') }}", []string{"states (rebound)"}},
		{"{% raw %}{{ states('lock.a') }}{% endraw %}", nil},
		{"{# {{ states('lock.a') }} #}{{ 1 }}", nil},
		{"{{ {'a': {'b': 1}} }}", nil},
		{"{{ '}}' ~ \"{{\" }}", nil},
		{"{%- set y = 3 -%}{{- y + 1 -}}", nil},
	}
	for _, c := range cases {
		tpl, err := parseJinja(c.tpl)
		if err != nil {
			t.Errorf("%s: %v", c.tpl, err)
			continue
		}
		got := env.unsafeIn(tpl)
		if fmt.Sprint(got) != fmt.Sprint(c.want) && (len(got) != 0 || len(c.want) != 0) {
			t.Errorf("%s: unsafe %q, want %q", c.tpl, got, c.want)
		}
	}
}

func TestTemplateThatDoesNotParseIsRefused(t *testing.T) {
	for _, s := range []string{"{{ states('a' }}", "{{ 1 +", "{% if x %}", "{% endif %}", "{{ 'abc }}", "{{ a b }}", "{% for x %}{% endfor %}", "{{ ] }}"} {
		if _, err := parseJinja(s); !errors.Is(err, errJinja) {
			t.Errorf("%q parsed (err %v)", s, err)
		}
	}
}

// D-189: what a template folds to.
func TestTemplateFolds(t *testing.T) {
	env := newTplEnv(map[string]any{"actions": []any{map[string]any{"variables": map[string]any{
		"e": "input_boolean.pg_hunt_guard", "f": "{{ 'input_boolean.' ~ 'x' }}", "g": "{{ f ~ '_2' }}", "n": "{{ now() }}"}}}}, nil, nil)
	cases := []struct {
		tpl  string
		want any
		ok   bool
	}{
		{"{{ {'input_boolean.pg_hunt_guard': 'on'} }}", map[string]any{"input_boolean.pg_hunt_guard": "on"}, true},
		{"{{ {('input_boolean.pg_hunt_' ~ 'guard'): 'on'} }}", map[string]any{"input_boolean.pg_hunt_guard": "on"}, true},
		{"{{ {e: 'on'} }}", map[string]any{"input_boolean.pg_hunt_guard": "on"}, true},
		{"{{ ['input_boolean.pg_hunt_' ~ 'guard'] }}", []any{"input_boolean.pg_hunt_guard"}, true},
		{"{{ g }}", "input_boolean.x_2", true},
		{"input_boolean.pg_hunt_{{ 'gua' + 'rd' }}", "input_boolean.pg_hunt_guard", true},
		{"{{ ('INPUT_BOOLEAN.X' | lower).replace('x','y') }}", nil, false},
		{"{{ 'INPUT_BOOLEAN.X' | lower | replace('x','y') }}", "input_boolean.y", true},
		{"{{ ['a','b'] | join('.') }}", "a.b", true},
		{"{{ n }}", nil, false},
		{"{{ trigger.entity_id }}", nil, false},
		{"[{{ \"'a.b'\" }}]", []any{"a.b"}, true},
		// D-205: escapes decoded as jinja2 decodes them.
		{"{{ 'input_boolean.pg_hunt_gu\\x61rd' }}", "input_boolean.pg_hunt_guard", true},
		{"{{ 'light.a\\x2clight.b' }}", "light.a,light.b", true},
	}
	for _, c := range cases {
		tpl, err := parseJinja(c.tpl)
		if err != nil {
			t.Fatalf("%s: %v", c.tpl, err)
		}
		got, ok := env.fold(tpl)
		if ok != c.ok || (ok && !reflect.DeepEqual(got, c.want)) {
			t.Errorf("%s: folded %#v (%v), want %#v (%v)", c.tpl, got, ok, c.want, c.ok)
		}
	}
}

// D-188, D-198: a variable assigned from an unknowable template is
// unknowable wherever used; one whose reads hactl names is not (its reads
// are checked where it is defined). D-203: the event types that make
// `trigger` unknowable, exactly.
func TestTemplateVariablesPropagate(t *testing.T) {
	env := newTplEnv(map[string]any{
		"variables": map[string]any{"s": "{{ state_attr(states('a.b'), 'z') }}", "t": "{{ s ~ '!' }}", "u": "{{ t }}", "r": "{{ state_attr('x.y', 'z') }}"},
		"actions":   []any{map[string]any{"variables": map[string]any{"ok": "{{ now() }}"}}},
	}, nil, nil)
	const s = "state_attr with a computed entity id"
	for name, want := range map[string]string{"s": s, "t": "variable s (from " + s + ")", "u": "variable t (from variable s (from " + s + "))", "r": "", "ok": ""} {
		if got := env.vars[name].unsafe; got != want {
			t.Errorf("variable %s unsafe %q, want %q", name, got, want)
		}
	}
	for _, tc := range []struct {
		doc  any
		want string
	}{
		{map[string]any{"triggers": []any{map[string]any{"trigger": "event", "event_type": "state_changed"}}}, "state_changed"},
		{map[string]any{"triggers": []any{map[string]any{"trigger": "event", "event_type": "state_reported"}}}, "state_reported"},
		{map[string]any{"triggers": []any{map[string]any{"trigger": "event", "event_type": "logbook_entry"}}}, "logbook_entry"},
		{map[string]any{"triggers": []any{map[string]any{"trigger": "event", "event_type": "call_service"}}}, "call_service"},
		{map[string]any{"triggers": []any{map[string]any{"trigger": "event", "event_type": "core_config_updated"}}}, "core_config_updated"},
		{map[string]any{"triggers": []any{map[string]any{"trigger": "event", "event_type": "entity_registry_updated"}}}, "entity_registry_updated"},
		{map[string]any{"triggers": []any{map[string]any{"trigger": "event", "event_type": "device_registry_updated"}}}, "device_registry_updated"},
		{map[string]any{"triggers": []any{map[string]any{"trigger": "event", "event_type": "*"}}}, "*"},
		{map[string]any{"actions": []any{map[string]any{"wait_for_trigger": []any{map[string]any{"trigger": "event", "event_type": "Call_Service"}}}}}, "Call_Service"},
		{map[string]any{"trigger": []any{map[string]any{"platform": "event", "event_type": []any{"x", "{{ y }}"}}}}, "{{ y }}"},
		{map[string]any{"triggers": []any{map[string]any{"trigger": "geo_location"}}}, "geo_location"},
		{map[string]any{"triggers": []any{map[string]any{"trigger": "event", "event_type": "my_event"}}}, ""},
		{map[string]any{"triggers": []any{map[string]any{"trigger": "state", "entity_id": "lock.a"}}}, ""},
	} {
		got := newTplEnv(tc.doc, nil, nil).trig.unsafe
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%v: trigger unsafe %q, want containing %q", tc.doc, got, tc.want)
		}
	}
}
