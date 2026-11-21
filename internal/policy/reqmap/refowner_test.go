package reqmap

import (
	"fmt"
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

// TestRefChangeOwnersFollowsMergeKeysAndAliases (D-104): a path through a `<<`
// merge or an alias resolves as the companion's loaded tree does — and since
// the companion rewrites the shared node once, the change lies in every entry
// that shares it: the one holding the anchor and every one merging it in.
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
	if g := ownerStrings(got); !slices.Equal(g, []string{"automation:one", "automation:two"}) {
		t.Errorf("owners = %q, want both entries that share the merged triggers", g)
	}
}

// TestRefChangeOwnersPlacesAChangeInEveryEntryThatSharesIt (D-104): the
// live-fire reproduction — an anchor in an allowed automation, an alias of it
// in a denied one. The companion reports the change at the anchor's location
// only; the denied automation reads the rewritten value too, so it is an owner.
// Also the other way round (a path through the alias), an anchored scalar,
// a merge key bringing the anchor's whole mapping in, and an id rewritten
// through an alias (a rename of every entry sharing it, E-5).
func TestRefChangeOwnersPlacesAChangeInEveryEntryThatSharesIt(t *testing.T) {
	const anchoredTrigger = `- id: pg_test
  alias: PG test
  trigger: &temp_trigger
    - platform: state
      entity_id: sensor.old_temp
  action: []
- id: prod_heating
  alias: Prod heating
  trigger: *temp_trigger
  action: []
- id: unrelated
  trigger: [{platform: state, entity_id: sensor.old_temp}]
  action: []
`
	const anchoredScalar = `- id: a
  trigger: [{platform: state, entity_id: &e sensor.old_temp}]
  action: []
- id: b
  trigger: [{platform: state, entity_id: *e}]
  action: []
`
	const mergedMapping = `- id: a
  trigger:
    - &t {platform: state, entity_id: sensor.old_temp}
  action: []
- id: b
  trigger:
    - <<: *t
      for: "00:01:00"
  action: []
`
	const scripts = `common: &seq
  sequence:
    - action: light.turn_on
      target: {entity_id: sensor.old_temp}
other:
  <<: *seq
  alias: Other
`
	const sharedID = `- &x
  id: prod_heating
  trigger: []
  action: []
`
	for _, tc := range []struct {
		name, location, content, path, old, new string
		want                                    []string
	}{
		{"change at the anchor reaches the alias", "automations.yaml", anchoredTrigger, "[0].trigger[0].entity_id", "sensor.old_temp", "sensor.new_temp",
			[]string{"automation:pg_test", "automation:prod_heating"}},
		{"change through the alias reaches the anchor", "automations.yaml", anchoredTrigger, "[1].trigger[0].entity_id", "sensor.old_temp", "sensor.new_temp",
			[]string{"automation:pg_test", "automation:prod_heating"}},
		{"an unshared entry stays alone", "automations.yaml", anchoredTrigger, "[2].trigger[0].entity_id", "sensor.old_temp", "sensor.new_temp",
			[]string{"automation:unrelated"}},
		{"an anchored scalar", "automations.yaml", anchoredScalar, "[0].trigger[0].entity_id", "sensor.old_temp", "sensor.new_temp",
			[]string{"automation:a", "automation:b"}},
		{"a merge key bringing the anchor in", "automations.yaml", mergedMapping, "[0].trigger[0].entity_id", "sensor.old_temp", "sensor.new_temp",
			[]string{"automation:a", "automation:b"}},
		{"a script merging another's sequence", "scripts.yaml", scripts, "common.sequence[0].target.entity_id", "sensor.old_temp", "sensor.new_temp",
			[]string{"script:common", "script:other"}},
		{"an id shared by alias is renamed once per sharing entry", "automations.yaml", sharedID + "- *x\n", "[0].id", "prod_heating", "test_heating",
			[]string{"automation:prod_heating->test_heating"}},
	} {
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

// TestRefChangeOwnersRefusesWhatItCannotWalk (D-104): a file whose aliases
// nest so that the changed node is reached more ways than can be walked is an
// error — the plan refuses — never the anchor's own entry alone.
func TestRefChangeOwnersRefusesWhatItCannotWalk(t *testing.T) {
	var b strings.Builder
	b.WriteString("- id: a\n  trigger: &l0 [{platform: state, entity_id: sensor.old}]\n  action: []\n")
	b.WriteString("- id: b\n  x0: &l1 [*l0, *l0, *l0, *l0, *l0, *l0, *l0, *l0]\n")
	for i := 1; i < 6; i++ {
		fmt.Fprintf(&b, "  x%d: &l%d [*l%d, *l%d, *l%d, *l%d, *l%d, *l%d, *l%d, *l%d]\n", i, i+1, i, i, i, i, i, i, i, i)
	}
	b.WriteString("  trigger: [*l6]\n  action: []\n")
	if _, err := RefChangeOwners("automations.yaml", []byte(b.String()), "[0].trigger[0].entity_id", "sensor.old", "sensor.new"); err == nil {
		t.Error("a change reached 8^6 ways was placed; want an error (fail closed)")
	}
}

// TestRefChangeIncluderOwners (D-105): a change in a file that other files
// `!include` lies in the typed entries of every including file, through every
// include tag Home Assistant's loader resolves and up through includes of
// includes; a file of no include is no owner, and a fragment included where
// no typed entry is gets none.
func TestRefChangeIncluderOwners(t *testing.T) {
	const fragment = "- platform: state\n  entity_id: sensor.old_temp\n"
	files := map[string][]byte{
		"configuration.yaml": []byte("homeassistant:\n  name: x\nautomation: !include automations.yaml\nscript: !include_dir_named scripts\nsensor: !include sensors.yaml\ntemplate: !include_dir_merge_list templates\n"),
		"automations.yaml": []byte("- id: prod_heating\n  trigger: !include common/temp_trigger.yaml\n  action: []\n" +
			"- id: other\n  trigger: []\n  action: []\n"),
		"common/temp_trigger.yaml": []byte(fragment),
		"scripts/wake.yaml":        []byte("alias: Wake\nsequence: !include ../common/steps.yaml\n"),
		"common/steps.yaml":        []byte("- action: light.turn_on\n  target: {entity_id: sensor.old_temp}\n"),
		"sensors.yaml":             []byte("- platform: template\n  sensors: !include common/sensor_body.yaml\n"),
		"common/sensor_body.yaml":  []byte("x:\n  value_template: \"{{ states('sensor.old_temp') }}\"\n"),
		"templates/a.yaml":         []byte("- trigger: !include ../common/tpl_trigger.yaml\n  sensor:\n    - unique_id: tpl_a\n      state: \"1\"\n"),
		"common/tpl_trigger.yaml":  []byte(fragment),
		"unrelated.yaml":           []byte("- id: x\n  trigger: !include common/other.yaml\n  action: []\n"),
	}
	for _, tc := range []struct {
		name, location, path string
		want                 []string
	}{
		{"!include in an automation entry", "common/temp_trigger.yaml", "[0].entity_id", []string{"automation:prod_heating"}},
		{"!include in a dir-named script file", "common/steps.yaml", "[0].target.entity_id", []string{"script:wake"}},
		{"!include below no typed entry", "common/sensor_body.yaml", "x.value_template", nil},
		{"!include in a merged template block", "common/tpl_trigger.yaml", "[0].entity_id", []string{"template:tpl_a"}},
		{"a file included whole is placed through its includer as by its shape", "automations.yaml", "[1].action", []string{"automation:other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RefChangeIncluderOwners(tc.location, files[tc.location], tc.path, "sensor.old_temp", "sensor.new_temp", files)
			if err != nil {
				t.Fatal(err)
			}
			if g := ownerStrings(got); !slices.Equal(g, tc.want) {
				t.Errorf("owners = %q, want %q", g, tc.want)
			}
		})
	}
}

// TestRefChangeIncluderOwnersFailsClosed (D-105): a file that may include the
// changed one but does not parse, and includes nested past any real
// configuration, are errors the plan refuses on.
func TestRefChangeIncluderOwnersFailsClosed(t *testing.T) {
	frag := []byte("- platform: state\n  entity_id: sensor.old\n")
	broken := map[string][]byte{
		"f.yaml":           frag,
		"automations.yaml": []byte("- id: a\n  trigger: !include f.yaml\n  action: [unclosed\n"),
	}
	if _, err := RefChangeIncluderOwners("f.yaml", frag, "[0].entity_id", "sensor.old", "sensor.new", broken); err == nil {
		t.Error("an unparseable includer was skipped; want an error")
	}
	deep := map[string][]byte{"f0.yaml": frag}
	for i := 1; i <= maxIncludeDepth+1; i++ {
		deep[fmt.Sprintf("f%d.yaml", i)] = fmt.Appendf(nil, "- !include f%d.yaml\n", i-1)
	}
	if _, err := RefChangeIncluderOwners("f0.yaml", frag, "[0].entity_id", "sensor.old", "sensor.new", deep); err == nil {
		t.Error("includes nested past the bound were followed; want an error")
	}
	if _, err := RefChangeIncluderOwners("f.yaml", frag, "[3].entity_id", "sensor.old", "sensor.new", nil); err == nil {
		t.Error("a path the file does not hold was placed; want an error")
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
