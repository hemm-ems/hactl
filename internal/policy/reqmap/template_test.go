package reqmap

import (
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// D-188: the safe-construct allowlist, read by a real Jinja parser.
func TestTemplateUnsafeConstructs(t *testing.T) {
	env := &tplEnv{vars: map[string]*tplVar{"this": {}, "trigger": {}, "e": {value: "x", folded: true}, "bad": {unsafe: "states"}}}
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
		{"{{ states('lock.a') }}", []string{"states"}},
		{"{{ states.lock.a.state }}", []string{"states"}},
		{"{{ state_attr('input_boolean.pg_hunt_' ~ 'secret','friendly_name') }}", []string{"state_attr"}},
		{"{{ is_state('lock.a', 'on') }}", []string{"is_state"}},
		{"{{ 'lock.a' | states }}", []string{"filter states"}},
		{"{{ 'lock.a' is is_state('on') }}", []string{"test is_state"}},
		{"{{ ['lock.a'] | select('is_state', 'on') | list }}", []string{"test is_state (through select)"}},
		{"{{ ['lock.a'] | map('state_attr', 'x') | list }}", []string{"filter state_attr (through map)"}},
		{"{{ ['lock.a'] | selectattr('x', 'is_state_attr', 'y') | list }}", []string{"test is_state_attr (through selectattr)"}},
		{"{{ ['lock.a'] | map(e) | list }}", []string{"filter map with a computed name"}},
		{"{{ states.input_boolean | selectattr('object_id','search','secr') | map(attribute='name') | join(',') }}", []string{"states"}},
		{"{{ expand('group.x') }}", []string{"expand"}},
		{"{{ area_entities('kitchen') }}", []string{"area_entities"}},
		{"{{ distance('zone.home') }}", []string{"distance"}},
		{"{{ bad }}", []string{"variable bad (from states)"}},
		{"{{ my_custom_fn() }}", []string{"name my_custom_fn"}},
		{"{{ 1 | my_filter }}", []string{"filter my_filter"}},
		{"{% macro m() %}{{ 1 }}{% endmacro %}{{ m() }}", []string{"name m", "tag macro", "tag endmacro"}},
		{"{% from 'x.jinja' import y %}{{ y }}", []string{"name y", "tag from"}},
		{"{% if false %}{% set states = 1 %}{% endif %}{{ states('lock.a') }}", []string{"states"}},
		{"{% set x = states('lock.a') %}{{ x }}", []string{"states"}},
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
		"e": "input_boolean.pg_hunt_guard", "f": "{{ 'input_boolean.' ~ 'x' }}", "g": "{{ f ~ '_2' }}", "n": "{{ now() }}"}}}})
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

func TestTemplateVariablesPropagate(t *testing.T) {
	env := newTplEnv(map[string]any{
		"variables": map[string]any{"s": "{{ state_attr('x.y', 'z') }}", "t": "{{ s ~ '!' }}", "u": "{{ t }}"},
		"actions":   []any{map[string]any{"variables": map[string]any{"ok": "{{ now() }}"}}},
	})
	for name, want := range map[string]string{"s": "state_attr", "t": "variable s (from state_attr)", "u": "variable t (from variable s (from state_attr))", "ok": ""} {
		if got := env.vars[name].unsafe; got != want {
			t.Errorf("variable %s unsafe %q, want %q", name, got, want)
		}
	}
	for _, tc := range []struct {
		doc  any
		want string
	}{
		{map[string]any{"triggers": []any{map[string]any{"trigger": "event", "event_type": "state_changed"}}}, "state_changed"},
		{map[string]any{"trigger": []any{map[string]any{"platform": "event", "event_type": []any{"x", "{{ y }}"}}}}, "{{ y }}"},
		{map[string]any{"triggers": []any{map[string]any{"trigger": "geo_location"}}}, "geo_location"},
		{map[string]any{"triggers": []any{map[string]any{"trigger": "event", "event_type": "my_event"}}}, ""},
		{map[string]any{"triggers": []any{map[string]any{"trigger": "state", "entity_id": "lock.a"}}}, ""},
	} {
		got := newTplEnv(tc.doc).vars["trigger"].unsafe
		if (tc.want == "") != (got == "") || !strings.Contains(got, tc.want) {
			t.Errorf("%v: trigger unsafe %q, want containing %q", tc.doc, got, tc.want)
		}
	}
}
