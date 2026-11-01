package reqmap

import (
	"slices"
	"strings"
	"testing"
)

func ownerStrings(os []TypedOwner) []string {
	out := make([]string, 0, len(os))
	for _, o := range os {
		s := string(o.Kind) + ":" + o.ID
		if o.NewID != "" {
			s += "->" + o.NewID
		}
		out = append(out, s)
	}
	slices.Sort(out)
	return out
}

// TestRefChangeOwners (D-77, R-3): a ref/replace change is placed in the
// typed entry its path runs through, by the entry's shape, in every layout the
// companion's walk renders a path for; an id or unique_id the change rewrites
// is a rename (E-5) with the new id worked out as the companion rewrites it.
func TestRefChangeOwners(t *testing.T) {
	const automations = `- id: prod_heating
  alias: Heating
  triggers:
    - trigger: state
      entity_id: sensor.old
      id: sensor.old
  actions:
    - action: light.turn_on
      target:
        entity_id: sensor.old
- alias: no id here
  triggers: [{trigger: state, entity_id: sensor.old}]
  actions: []
- id: '1700000000'
  use_blueprint:
    path: x.yaml
    input:
      sensor: sensor.old
`
	const scripts = `wake_up:
  alias: Wake
  sequence:
    - action: light.turn_on
      target: {entity_id: sensor.old}
blueprinted:
  use_blueprint: {path: y.yaml, input: {s: sensor.old}}
`
	const templates = `- sensor:
    - name: A
      unique_id: tpl_a
      state: "{{ states('sensor.old') }}"
    - name: B
      state: "{{ states('sensor.old') }}"
- trigger:
    - trigger: state
      entity_id: sensor.old
  sensor:
    - name: C
      unique_id: tpl_c
      state: "1"
    - name: D
      unique_id: tpl_d
      state: "2"
  binary_sensor:
    name: E
    unique_id: tpl_e
    state: "{{ is_state('sensor.old', 'on') }}"
`
	const config = `homeassistant:
  name: Home
automation:
  - id: inline_auto
    triggers: [{trigger: state, entity_id: sensor.old}]
    actions: []
automation ui: !include automations.yaml
script:
  inline_script:
    sequence:
      - action: light.turn_on
        target: {entity_id: sensor.old}
template:
  - sensor:
      - unique_id: inline_tpl
        state: "{{ states('sensor.old') }}"
template legacy:
  sensor:
    unique_id: labelled_tpl
    state: "{{ states('sensor.old') }}"
sensor:
  - platform: template
    sensors:
      x:
        unique_id: legacy_platform
        value_template: "{{ states('sensor.old') }}"
`
	const scenes = `- id: '42'
  name: Evening
  entities:
    light.x:
      state: sensor.old
`
	const oneAutomation = `id: dir_list_auto
triggers: [{trigger: state, entity_id: sensor.old}]
actions: []
`
	const oneScript = `alias: Named
sequence:
  - action: x.y
    data: {e: sensor.old}
`
	const dotted = `- id: a
  actions:
    - data:
        light.x: sensor.old
- id: b
  actions:
    - data:
        light:
          x: sensor.old
`
	cases := []struct {
		name, location, content, path, old, new string
		want                                    []string
	}{
		{"automation action", "automations.yaml", automations, "[0].actions[0].target.entity_id", "sensor.old", "sensor.new", []string{"automation:prod_heating"}},
		{"automation trigger id is not the entry id", "automations.yaml", automations, "[0].triggers[0].id", "sensor.old", "sensor.new", []string{"automation:prod_heating"}},
		{"automation id rewritten is a rename", "automations.yaml", automations, "[0].id", "prod_heating", "test_heating", []string{"automation:prod_heating->test_heating"}},
		{"id-less automation has no owner", "automations.yaml", automations, "[1].triggers[0].entity_id", "sensor.old", "sensor.new", nil},
		{"blueprint automation", "automations.yaml", automations, "[2].use_blueprint.input.sensor", "sensor.old", "sensor.new", []string{"automation:1700000000"}},
		{"script in scripts.yaml", "scripts.yaml", scripts, "wake_up.sequence[0].target.entity_id", "sensor.old", "sensor.new", []string{"script:wake_up"}},
		{"blueprint script", "scripts.yaml", scripts, "blueprinted.use_blueprint.input.s", "sensor.old", "sensor.new", []string{"script:blueprinted"}},
		{"template entity", "template.yaml", templates, "[0].sensor[0].state", "sensor.old", "sensor.new", []string{"template:tpl_a"}},
		{"template entity without unique_id", "template.yaml", templates, "[0].sensor[1].state", "sensor.old", "sensor.new", nil},
		{"trigger block's shared trigger lies in every entity", "template.yaml", templates, "[1].trigger[0].entity_id", "sensor.old", "sensor.new", []string{"template:tpl_c", "template:tpl_d", "template:tpl_e"}},
		{"single-mapping entity domain", "template.yaml", templates, "[1].binary_sensor.state", "sensor.old", "sensor.new", []string{"template:tpl_e"}},
		{"unique_id rewritten is a rename", "template.yaml", templates, "[0].sensor[0].unique_id", "tpl_a", "tpl_z", []string{"template:tpl_a->tpl_z"}},
		{"inline automation", "configuration.yaml", config, "automation[0].triggers[0].entity_id", "sensor.old", "sensor.new", []string{"automation:inline_auto"}},
		{"inline script", "configuration.yaml", config, "script.inline_script.sequence[0].target.entity_id", "sensor.old", "sensor.new", []string{"script:inline_script"}},
		{"inline template", "configuration.yaml", config, "template[0].sensor[0].state", "sensor.old", "sensor.new", []string{"template:inline_tpl"}},
		{"labelled template key, one block", "configuration.yaml", config, "template legacy.sensor.state", "sensor.old", "sensor.new", []string{"template:labelled_tpl"}},
		{"legacy platform template is not a template entity", "configuration.yaml", config, "sensor[0].sensors.x.value_template", "sensor.old", "sensor.new", nil},
		{"scene has no typed owner", "scenes.yaml", scenes, "[0].entities.light.x.state", "sensor.old", "sensor.new", nil},
		{"!include_dir_list automation file", "automations/a.yaml", oneAutomation, "triggers[0].entity_id", "sensor.old", "sensor.new", []string{"automation:dir_list_auto"}},
		{"!include_dir_named script file", "scripts/named_one.yaml", oneScript, "sequence[0].data.e", "sensor.old", "sensor.new", []string{"script:named_one"}},
		{"a dotted key matching two ways places both", "automations.yaml", dotted, "[0].actions[0].data.light.x", "sensor.old", "sensor.new", []string{"automation:a"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RefChangeOwners(tc.location, []byte(tc.content), tc.path, tc.old, tc.new)
			if err != nil {
				t.Fatal(err)
			}
			if g := ownerStrings(got); !slices.Equal(g, tc.want) {
				t.Errorf("owners = %q, want %q", g, tc.want)
			}
		})
	}
}

// TestRefChangeOwnersAmbiguousPathIsASuperset: a path that walks the tree two
// ways (a key holding a dot) gets the owners of both walks — more checks,
// never fewer.
func TestRefChangeOwnersAmbiguousPathIsASuperset(t *testing.T) {
	const doc = `script:
  a.b:
    sequence: [{action: x.y, data: {e: sensor.old}}]
  a:
    b:
      sequence: [{action: x.y, data: {e: sensor.old}}]
`
	got, err := RefChangeOwners("configuration.yaml", []byte(doc), "script.a.b.sequence[0].data.e", "sensor.old", "sensor.new")
	if err != nil {
		t.Fatal(err)
	}
	if g := ownerStrings(got); !slices.Equal(g, []string{"script:a", "script:a.b"}) {
		t.Errorf("owners = %q", g)
	}
}

// TestRefChangeOwnersFailsWhenItCannotPlace: a file that does not parse, or a
// path the file does not hold (it changed after the dry run), is an error the
// plan refuses on — never "no owner".
func TestRefChangeOwnersFailsWhenItCannotPlace(t *testing.T) {
	for name, tc := range map[string]struct{ content, path string }{
		"unparseable":   {"- id: [unclosed", "[0].id"},
		"empty file":    {"", "[0].id"},
		"path not held": {"- id: a\n  actions: []\n", "[3].actions[0].entity_id"},
		"empty path":    {"- id: a\n", ""},
	} {
		if _, err := RefChangeOwners("automations.yaml", []byte(tc.content), tc.path, "a", "b"); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
}

// TestRefChangeOwnersFollowsMergeKeysAndAliases: a path through a `<<` merge
// or an alias resolves as the companion's loaded tree does.
func TestRefChangeOwnersFollowsMergeKeysAndAliases(t *testing.T) {
	const doc = `- &base
  id: one
  triggers: [{trigger: state, entity_id: sensor.old}]
  actions: []
- <<: *base
  id: two
`
	got, err := RefChangeOwners("automations.yaml", []byte(doc), "[1].triggers[0].entity_id", "sensor.old", "sensor.new")
	if err != nil {
		t.Fatal(err)
	}
	if g := ownerStrings(got); !slices.Equal(g, []string{"automation:two"}) {
		t.Errorf("owners = %q", g)
	}
}

// TestReplaceTokenMatchesTheCompanionsBoundaries: whole tokens only, Unicode
// word characters counting as word characters, as Python's `\b` does.
func TestReplaceTokenMatchesTheCompanionsBoundaries(t *testing.T) {
	for _, tc := range []struct{ s, old, new, want string }{
		{"prod_heating", "prod_heating", "test_heating", "test_heating"},
		{"prod_heating_v2", "prod_heating", "x", "prod_heating_v2"},
		{"a.prod_heating-b", "prod_heating", "x", "a.x-b"},
		{"äprod_heating", "prod_heating", "x", "äprod_heating"},
		{"{{ prod_heating }}{{ prod_heating }}", "prod_heating", "x", "{{ x }}{{ x }}"},
		{"sensor.old", "", "x", "sensor.old"},
	} {
		if got := ReplaceToken(tc.s, tc.old, tc.new); got != tc.want {
			t.Errorf("ReplaceToken(%q, %q, %q) = %q, want %q", tc.s, tc.old, tc.new, got, tc.want)
		}
	}
	if strings.Contains(ReplaceToken("x", "x", "y"), "x") {
		t.Error("a whole-string token was not replaced")
	}
}
