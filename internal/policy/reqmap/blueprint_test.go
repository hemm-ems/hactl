package reqmap_test

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// sectionsBlueprint has an input inside a section, with a default, and one
// at the top with a default (the shape TestE2EBlueprintSubstitutionOracle
// asks Home Assistant about).
const sectionsBlueprint = `blueprint:
  name: PG Hunt Sections
  domain: automation
  input:
    targets:
      name: Targets
      input:
        switch_target:
          name: Switch
          default:
            entity_id: input_boolean.pg_hunt_guard
          selector:
            target: {}
    event_name:
      name: Event
      default: pg_hunt_bp_fire
mode: single
triggers:
  - trigger: event
    event_type: !input event_name
actions:
  - action: input_boolean.turn_on
    target: !input switch_target
`

func bpFiles(t *testing.T) map[string]string {
	t.Helper()
	motion, err := os.ReadFile("testdata/blueprints/motion_light.yaml")
	if err != nil {
		t.Fatal(err)
	}
	return map[string]string{ //nolint:gosec // G101: blueprint paths, not credentials
		"blueprints/automation/homeassistant/motion_light.yaml": string(motion),
		"blueprints/automation/pg/sections.yaml":                sectionsBlueprint,
		"blueprints/automation/pg/visible.yaml":                 "blueprint:\n  name: v\n  domain: automation\n  input:\n    a:\n      default: 1\n      visible: \"{{ true }}\"\nactions: []\n",
		"blueprints/automation/pg/secret.yaml":                  "blueprint:\n  name: s\n  domain: automation\n  input: {}\nactions:\n  - action: light.turn_on\n    target:\n      entity_id: !secret porch\n",
		"blueprints/automation/pg/undeclared.yaml":              "blueprint:\n  name: u\n  domain: automation\n  input: {}\nactions:\n  - action: light.turn_on\n    target: !input nowhere\n",
		"blueprints/automation/pg/required.yaml":                "blueprint:\n  name: r\n  domain: automation\n  input:\n    must: {name: Must}\nactions:\n  - action: light.turn_on\n    target: !input must\n",
		"blueprints/automation/pg/empty.yaml":                   "",
		"blueprints/automation/pg/notbp.yaml":                   "actions: []\n",
		"blueprints/automation/pg/two.yaml":                     "blueprint:\n  name: t\n  domain: automation\n  input:\n    b: {}\n    a: {}\n    c: {}\nactions: []\n",
		"blueprints/script/pg/s.yaml":                           "blueprint:\n  name: s\n  domain: script\n  input:\n    t: {default: {entity_id: light.kitchen}}\nsequence:\n  - action: light.turn_on\n    target: !input t\n",
	}
}

func bpReader(files map[string]string) reqmap.BlueprintReader {
	return func(loc string) ([]byte, error) {
		if c, ok := files[loc]; ok {
			return []byte(c), nil
		}
		return nil, errors.New("404 not found")
	}
}

func yamlDoc(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := yaml.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}

// TestBlueprintExpansion — D-185: a use_blueprint candidate is checked as
// what Home Assistant runs, and what cannot be expanded is refused.
func TestBlueprintExpansion(t *testing.T) {
	files := bpFiles(t)
	live := &reqmap.LiveStates{EntityIDs: []string{
		"light.pg_hunt_bedroom", "light.pg_hunt_hall", "input_boolean.pg_hunt_ok", "input_boolean.pg_hunt_guard", "light.kitchen",
	}}
	for _, c := range []struct {
		name, domain, candidate string
		switches                []string // services' targets, as "<service> <entity>"
		refused                 string   // a substring of the undecidable rule
	}{
		{
			name:      "motion_light's light_target is what turn_on and turn_off switch (F4)",
			domain:    "automation",
			candidate: "id: x\nuse_blueprint:\n  path: homeassistant/motion_light.yaml\n  input:\n    motion_entity: input_boolean.pg_hunt_ok\n    light_target:\n      entity_id: light.pg_hunt_bedroom\n",
			switches:  []string{"light.turn_off light.pg_hunt_bedroom", "light.turn_on light.pg_hunt_bedroom"},
		},
		{
			name:      "a section's input takes its default when none is given",
			domain:    "automation",
			candidate: "id: x\nuse_blueprint:\n  path: pg/sections.yaml\n",
			switches:  []string{"input_boolean.turn_on input_boolean.pg_hunt_guard"},
		},
		{
			name:      "a given input overrides the default",
			domain:    "automation",
			candidate: "id: x\nuse_blueprint:\n  path: pg/sections.yaml\n  input:\n    switch_target:\n      entity_id: input_boolean.pg_hunt_ok\n",
			switches:  []string{"input_boolean.turn_on input_boolean.pg_hunt_ok"},
		},
		{
			name:      "the candidate's own keys override the blueprint's",
			domain:    "automation",
			candidate: "id: x\nuse_blueprint:\n  path: pg/sections.yaml\nactions:\n  - action: light.turn_on\n    target:\n      entity_id: light.kitchen\n",
			switches:  []string{"light.turn_on light.kitchen"},
		},
		{
			name:      "a script blueprint is read from the script folder, at any depth",
			domain:    "script",
			candidate: "pg_s:\n  use_blueprint:\n    path: pg/s.yaml\n",
			switches:  []string{"light.turn_on light.kitchen"},
		},
		{name: "a missing file", domain: "automation", candidate: "use_blueprint: {path: pg/none.yaml}\n", refused: "unreadable"},
		{name: "a path with ..", domain: "automation", candidate: "use_blueprint: {path: ../script/pg/s.yaml}\n", refused: ".. segment"},
		{name: "an absolute path", domain: "automation", candidate: "use_blueprint: {path: /etc/passwd}\n", refused: "absolute"},
		{name: "a blueprint of another domain", domain: "script", candidate: "x:\n  use_blueprint: {path: homeassistant/motion_light.yaml}\n", refused: "unreadable"},
		{name: "a visible: condition", domain: "automation", candidate: "use_blueprint: {path: pg/visible.yaml}\n", refused: "visible"},
		{name: "a tag other than !input", domain: "automation", candidate: "use_blueprint: {path: pg/secret.yaml}\n", refused: "!secret"},
		{name: "an !input naming no declared input", domain: "automation", candidate: "use_blueprint: {path: pg/undeclared.yaml}\n", refused: "names no declared input"},
		{name: "a required input without value or default", domain: "automation", candidate: "use_blueprint: {path: pg/required.yaml}\n", refused: "neither a value nor a default"},
		{name: "an empty file", domain: "automation", candidate: "use_blueprint: {path: pg/empty.yaml}\n", refused: "is empty"},
		{name: "a file with no blueprint section", domain: "automation", candidate: "use_blueprint: {path: pg/notbp.yaml}\n", refused: "no blueprint section"},
		{name: "the first missing input by name", domain: "automation", candidate: "use_blueprint: {path: pg/two.yaml}\n", refused: "input a has neither"},
		{name: "no path", domain: "automation", candidate: "use_blueprint: {input: {}}\n", refused: "no path"},
		{name: "no domain known", domain: "", candidate: "use_blueprint: {path: pg/sections.yaml}\n", refused: "domain"},
	} {
		t.Run(c.name, func(t *testing.T) {
			out, und := reqmap.ExpandBlueprints(yamlDoc(t, c.candidate), c.domain, bpReader(files))
			if c.refused != "" {
				if len(und) == 0 || !strings.Contains(und[0].Rule, c.refused) || !strings.HasPrefix(und[0].Rule, reqmap.RuleBlueprintUnexpandable) {
					t.Fatalf("undecidable %+v, want a blueprint refusal containing %q", und, c.refused)
				}
				return
			}
			if len(und) > 0 {
				t.Fatalf("undecidable %+v, want an expansion", und)
			}
			use := reqmap.CandidateUses(out, live, &reqmap.Registries{})
			var got []string
			for _, a := range use.Actions {
				tgt := a.Data["entity_id"]
				got = append(got, a.Domain+"."+a.Service+" "+strings.TrimSpace(strings.Trim(strings.ReplaceAll(strings.ReplaceAll(stringify(tgt), "[", ""), "]", ""), " ")))
			}
			slices.Sort(got)
			if !slices.Equal(got, c.switches) {
				t.Errorf("actions %q, want %q", got, c.switches)
			}
		})
	}
	// Without a reader every blueprint is refused.
	if _, und := reqmap.ExpandBlueprints(yamlDoc(t, "use_blueprint: {path: pg/sections.yaml}\n"), "automation", nil); len(und) == 0 {
		t.Error("a nil reader expanded a blueprint, want it refused")
	}
}

// TestBlueprintExpansionIsCanonical — the expansion walks Go maps (the
// candidate, the declared inputs, the blueprint body); what it refuses, and
// in which order, is the same on every run.
func TestBlueprintExpansionIsCanonical(t *testing.T) {
	read := bpReader(bpFiles(t))
	const cand = `b:
  use_blueprint: {path: pg/two.yaml}
a:
  use_blueprint: {path: pg/visible.yaml}
c:
  use_blueprint: {path: pg/undeclared.yaml}
d:
  use_blueprint: {path: pg/sections.yaml, input: {event_name: x}}
`
	var first string
	for i := range 50 {
		out, und := reqmap.ExpandBlueprints(yamlDoc(t, cand), "automation", read)
		got := fmt.Sprintf("%+v %v", und, out)
		if i == 0 {
			first = got
			if len(und) != 3 || !strings.HasPrefix(und[0].Path, "/a/") {
				t.Fatalf("undecidable %+v, want three, /a first", und)
			}
			continue
		}
		if got != first {
			t.Fatalf("run %d differs:\n%s\nvs\n%s", i, got, first)
		}
	}
}

func stringify(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		var parts []string
		for _, e := range t {
			parts = append(parts, stringify(e))
		}
		return strings.Join(parts, ",")
	}
	return ""
}
