package enforce_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/enforce"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// targetFetcher answers search/related and the config grep from tables.
type targetFetcher struct {
	fakeFetcher

	related    map[string]map[string][]string // "<type>:<id>" → answer
	relatedErr error
	grep       map[string]enforce.ConfigGrepResult
	grepErr    error
	asks       []string
}

func (f *targetFetcher) Related(_ context.Context, itemType, itemID string) (map[string][]string, error) {
	f.asks = append(f.asks, "related "+itemType+":"+itemID)
	if f.relatedErr != nil {
		return nil, f.relatedErr
	}
	return f.related[itemType+":"+itemID], nil
}

func (f *targetFetcher) ConfigGrep(_ context.Context, id string) (enforce.ConfigGrepResult, error) {
	f.asks = append(f.asks, "grep "+id)
	if f.grepErr != nil {
		return enforce.ConfigGrepResult{}, f.grepErr
	}
	return f.grep[id], nil
}

const relabelPolicy = `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*": [read]
      "entity:*/registry/labels": [edit]
      "entity:*/registry/area_id": [edit]
      "device:*/registry": [edit]
      "entity:light.*": [control]
    deny:
      "entity:input_boolean.guard": [control]
`

func relabelRegistries() *reqmap.Registries {
	return &reqmap.Registries{
		Entities: []reqmap.RegistryEntity{
			{EntityID: "input_boolean.guard", Labels: []string{"old"}},
			{EntityID: "light.hall", DeviceID: "dev1", Labels: []string{"cosy"}},
			{EntityID: "input_boolean.dev_guard", DeviceID: "dev1"},
			{EntityID: "input_boolean.own_area", DeviceID: "dev1", AreaID: "kitchen"},
		},
		Devices: []reqmap.RegistryDevice{{ID: "dev1", AreaID: "hall"}},
		Areas:   []reqmap.RegistryArea{{ID: "hall"}, {ID: "kitchen"}, {ID: "garden", FloorID: "ground", Labels: []string{"outside"}}},
	}
}

func relabelStrings(cs []reqmap.Check) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.String())
	}
	slices.Sort(out)
	return out
}

// labels is a label list to set. (A plain `return &l` on the variadic
// parameter panics golangci-lint's newexpr analyzer, v2.11.4.)
func labels(l ...string) *[]string {
	out := append([]string{}, l...)
	return &out
}

// TestRegistryReachChecks — D-186 (F5): a registry edit that puts an entity
// under a container an automation, a script or a template entity targets
// needs control on that entity; pure tidying needs nothing more; an answer
// hactl cannot get counts as targeting.
func TestRegistryReachChecks(t *testing.T) {
	cosyTargeted := map[string]map[string][]string{"label:cosy": {"automation": {"automation.pg_label"}}}
	for _, c := range []struct {
		name       string
		change     enforce.RegistryChange
		related    map[string]map[string][]string
		relatedErr error
		grep       map[string]enforce.ConfigGrepResult
		grepErr    error
		want       []string
	}{
		{
			name:    "the guard joins a label an automation targets (F5)",
			change:  enforce.RegistryChange{EntityID: "input_boolean.guard", Labels: labels("old", "cosy")},
			related: cosyTargeted, grepErr: enforce.ErrNoCompanion,
			want: []string{"control entity:input_boolean.guard"},
		},
		{
			name:    "deleting an entity's own area drops it into its device's targeted area (D-195, B2)",
			change:  enforce.RegistryChange{DeletedArea: "kitchen"},
			related: map[string]map[string][]string{"area:hall": {"script": {"script.pg_hall"}}}, grepErr: enforce.ErrNoCompanion,
			want: []string{"control entity:input_boolean.own_area"},
		},
		{
			name:    "deleting an area nobody falls back from is the delete alone",
			change:  enforce.RegistryChange{DeletedArea: "hall"},
			related: map[string]map[string][]string{"area:hall": {"script": {"script.pg_hall"}}}, grepErr: enforce.ErrNoCompanion,
		},
		{
			name:    "deleting a floor or a label takes containers away, adds none",
			change:  enforce.RegistryChange{DeletedFloor: "ground"},
			related: map[string]map[string][]string{"area:garden": {"script": {"script.x"}}}, relatedErr: errors.New("asked anyway"),
		},
		{
			name:       "deleting a label adds no container",
			change:     enforce.RegistryChange{DeletedLabel: "cosy"},
			relatedErr: errors.New("asked anyway"),
		},
		{
			name:    "clearing an entity's own area drops it into its device's targeted area",
			change:  enforce.RegistryChange{EntityID: "input_boolean.own_area", AreaSet: true},
			related: map[string]map[string][]string{"area:hall": {"script": {"script.pg_hall"}}}, grepErr: enforce.ErrNoCompanion,
			want: []string{"control entity:input_boolean.own_area"},
		},
		{
			name:    "a label nothing targets is tidying",
			change:  enforce.RegistryChange{EntityID: "input_boolean.guard", Labels: labels("old", "fresh")},
			related: cosyTargeted, grepErr: enforce.ErrNoCompanion,
		},
		{
			name:    "removing a label adds no container",
			change:  enforce.RegistryChange{EntityID: "light.hall", Labels: labels()},
			related: cosyTargeted,
		},
		{
			name:       "search/related failing counts as targeting",
			change:     enforce.RegistryChange{EntityID: "input_boolean.guard", Labels: labels("fresh")},
			relatedErr: errors.New("unknown command"),
			want:       []string{"control entity:input_boolean.guard"},
		},
		{
			name:   "a template entity found by the grep under a target key",
			change: enforce.RegistryChange{EntityID: "input_boolean.guard", Labels: labels("fresh")},
			grep:   map[string]enforce.ConfigGrepResult{"fresh": {Hits: []enforce.ConfigGrepHit{{Location: "template.yaml", Key: "label_id", MatchedValue: "fresh"}}}},
			want:   []string{"control entity:input_boolean.guard"},
		},
		{
			name:   "a template entity found by the grep inside a template",
			change: enforce.RegistryChange{EntityID: "input_boolean.guard", Labels: labels("fresh")},
			grep:   map[string]enforce.ConfigGrepResult{"fresh": {Hits: []enforce.ConfigGrepHit{{Key: "value_template", MatchedValue: "{{ label_entities('fresh') }}"}}}},
			want:   []string{"control entity:input_boolean.guard"},
		},
		{
			name:   "a grep hit under no target key is no target",
			change: enforce.RegistryChange{EntityID: "input_boolean.guard", Labels: labels("fresh")},
			grep:   map[string]enforce.ConfigGrepResult{"fresh": {Hits: []enforce.ConfigGrepHit{{Key: "name", MatchedValue: "fresh"}}}},
		},
		{
			name:   "a truncated grep counts as targeting",
			change: enforce.RegistryChange{EntityID: "input_boolean.guard", Labels: labels("fresh")},
			grep:   map[string]enforce.ConfigGrepResult{"fresh": {Truncated: true}},
			want:   []string{"control entity:input_boolean.guard"},
		},
		{
			name:    "a failing grep counts as targeting",
			change:  enforce.RegistryChange{EntityID: "input_boolean.guard", Labels: labels("fresh")},
			grepErr: errors.New("502"),
			want:    []string{"control entity:input_boolean.guard"},
		},
		{
			name:    "an area brings its floor and its labels",
			change:  enforce.RegistryChange{EntityID: "input_boolean.guard", AreaSet: true, AreaID: "garden"},
			related: map[string]map[string][]string{"label:outside": {"script": {"script.pg_outside"}}},
			grepErr: enforce.ErrNoCompanion,
			want:    []string{"control entity:input_boolean.guard"},
		},
		{
			name:    "a floor an automation targets",
			change:  enforce.RegistryChange{EntityID: "input_boolean.guard", AreaSet: true, AreaID: "garden"},
			related: map[string]map[string][]string{"floor:ground": {"automation": {"automation.pg_floor"}}},
			grepErr: enforce.ErrNoCompanion,
			want:    []string{"control entity:input_boolean.guard"},
		},
		{
			name:    "a device's new area reaches its entities without an area of their own",
			change:  enforce.RegistryChange{DeviceID: "dev1", AreaSet: true, AreaID: "garden"},
			related: map[string]map[string][]string{"area:garden": {"automation": {"automation.pg_area"}}},
			grepErr: enforce.ErrNoCompanion,
			want:    []string{"control entity:input_boolean.dev_guard", "control entity:light.hall"},
		},
		{
			name:    "a device's new label reaches all its entities",
			change:  enforce.RegistryChange{DeviceID: "dev1", Labels: labels("cosy")},
			related: cosyTargeted, grepErr: enforce.ErrNoCompanion,
			want: []string{"control entity:input_boolean.dev_guard", "control entity:input_boolean.own_area"},
		},
		{
			name:    "a rename to an id an automation targets",
			change:  enforce.RegistryChange{EntityID: "input_boolean.guard", NewEntityID: "input_boolean.porch"},
			related: map[string]map[string][]string{"entity:input_boolean.porch": {"automation": {"automation.pg_porch"}}},
			grepErr: enforce.ErrNoCompanion,
			want:    []string{"control entity:input_boolean.guard"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := &targetFetcher{fakeFetcher: fakeFetcher{reg: relabelRegistries(), complete: true}, related: c.related, relatedErr: c.relatedErr, grep: c.grep, grepErr: c.grepErr}
			s, _ := newSession(t, relabelPolicy, f)
			checks, denials := s.RegistryReachChecks(context.Background(), c.change)
			if len(denials) != 0 {
				t.Fatalf("denials %+v", denials)
			}
			if got := relabelStrings(checks); !slices.Equal(got, append(make([]string, 0, len(c.want)), c.want...)) {
				t.Errorf("checks %q, want %q (asked %q)", got, c.want, f.asks)
			}
		})
	}

	// A label the entity already has is not asked about at all.
	f := &targetFetcher{fakeFetcher: fakeFetcher{reg: relabelRegistries(), complete: true}, related: cosyTargeted}
	s, _ := newSession(t, relabelPolicy, f)
	if checks, _ := s.RegistryReachChecks(context.Background(), enforce.RegistryChange{EntityID: "light.hall", Labels: labels("cosy")}); len(checks) != 0 || len(f.asks) != 0 {
		t.Errorf("an unchanged label: checks %v, asked %q — want nothing", checks, f.asks)
	}

	// A fetcher that cannot say what targets anything: every new container
	// counts as targeted.
	s, _ = newSession(t, relabelPolicy, &fakeFetcher{reg: relabelRegistries(), complete: true})
	if checks, _ := s.RegistryReachChecks(context.Background(), enforce.RegistryChange{EntityID: "input_boolean.guard", Labels: labels("fresh")}); len(checks) != 1 {
		t.Errorf("no target oracle: checks %v, want control on the guard", checks)
	}

	// Registries unread: refused fail-closed.
	s, _ = newSession(t, relabelPolicy, &targetFetcher{fakeFetcher: fakeFetcher{regErr: errors.New("down")}})
	if _, denials := s.RegistryReachChecks(context.Background(), enforce.RegistryChange{EntityID: "input_boolean.guard", Labels: labels("x")}); len(denials) != 1 || denials[0].Rule != enforce.RuleRegistriesUnread {
		t.Errorf("registries unread: denials %+v, want %s", denials, enforce.RuleRegistriesUnread)
	}

	// indirect: unchecked, and a profile that denies nothing: nothing added.
	for _, body := range []string{
		strings.Replace(relabelPolicy, "    default: deny\n", "    default: deny\n    indirect: unchecked\n", 1),
		"version: 1\nactive: agent\nprofiles:\n  agent:\n    default: allow\n",
	} {
		f := &targetFetcher{fakeFetcher: fakeFetcher{reg: relabelRegistries(), complete: true}, related: cosyTargeted}
		s, _ := newSession(t, body, f)
		if checks, denials := s.RegistryReachChecks(context.Background(), enforce.RegistryChange{EntityID: "input_boolean.guard", Labels: labels("cosy")}); len(checks)+len(denials) != 0 {
			t.Errorf("%q: checks %v denials %v, want none", body, checks, denials)
		}
	}
}

// TestRequestLayerChecksARelabel — D-186: the registry update itself is
// refused for what it makes reachable, without any command plan; the same
// update to a label nothing targets passes.
func TestRequestLayerChecksARelabel(t *testing.T) {
	newF := func() *targetFetcher {
		return &targetFetcher{fakeFetcher: fakeFetcher{reg: relabelRegistries(), complete: true},
			related: map[string]map[string][]string{"label:cosy": {"automation": {"automation.pg_label"}}}, grepErr: enforce.ErrNoCompanion}
	}
	upd := func(params map[string]any) reqmap.Request {
		return reqmap.Request{Transport: reqmap.WS, Type: "config/entity_registry/update", Params: params}
	}
	s, _ := newSession(t, relabelPolicy, newF())
	err := s.Check(context.Background(), upd(map[string]any{"entity_id": "input_boolean.guard", "labels": []any{"old", "cosy"}}))
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) || !slices.ContainsFunc(ref.Denied, func(d enforce.Denial) bool { return d.Resource == "entity:input_boolean.guard" && d.Verb == "control" }) {
		t.Errorf("labelling the guard into a targeted label: Check = %v, want a refusal naming control entity:input_boolean.guard", err)
	}
	s, _ = newSession(t, relabelPolicy, newF())
	if err := s.Check(context.Background(), upd(map[string]any{"entity_id": "input_boolean.guard", "labels": []any{"old", "fresh"}})); err != nil {
		t.Errorf("labelling the guard into an untargeted label: Check = %v, want allowed", err)
	}
	s, _ = newSession(t, relabelPolicy, newF())
	dev := reqmap.Request{Transport: reqmap.WS, Type: "config/device_registry/update", Params: map[string]any{"device_id": "dev1", "labels": []any{"cosy"}}}
	if err := s.Check(context.Background(), dev); !errors.As(err, &ref) || !slices.ContainsFunc(ref.Denied, func(d enforce.Denial) bool {
		return d.Resource == "entity:input_boolean.dev_guard" && d.Verb == "control"
	}) {
		t.Errorf("labelling a device whose entities include a control-denied one into a targeted label: Check = %v, want refused", err)
	}
	s, _ = newSession(t, strings.Replace(relabelPolicy, "    allow:\n", "    allow:\n      \"area:*\": [delete]\n", 1), &targetFetcher{fakeFetcher: fakeFetcher{reg: relabelRegistries(), complete: true},
		related: map[string]map[string][]string{"area:hall": {"script": {"script.pg_hall"}}}, grepErr: enforce.ErrNoCompanion})
	del := reqmap.Request{Transport: reqmap.WS, Type: "config/area_registry/delete", Params: map[string]any{"area_id": "kitchen"}}
	if err := s.Check(context.Background(), del); !errors.As(err, &ref) || !slices.ContainsFunc(ref.Denied, func(d enforce.Denial) bool {
		return d.Resource == "entity:input_boolean.own_area" && d.Verb == "control"
	}) {
		t.Errorf("deleting the area an entity falls back from into a targeted area: Check = %v, want refused on control entity:input_boolean.own_area", err)
	}
	if len(ref.Denied) != 1 {
		t.Errorf("deleting the kitchen: denied %+v, want only the fallen-back entity", ref.Denied)
	}
}

// affectFetcher is a targetFetcher that also reads stored configs, config
// files and the service list (D-212).
type affectFetcher struct {
	*targetFetcher

	configs  map[string]string
	files    map[string]string
	services map[string]map[string]bool
}

func (f affectFetcher) StoredConfig(_ context.Context, kind, id string) ([]byte, error) {
	f.asks = append(f.asks, "stored "+kind+":"+id)
	c, ok := f.configs[kind+":"+id]
	if !ok {
		return nil, errors.New("404 not found")
	}
	return []byte(c), nil
}

func (f affectFetcher) ConfigFile(_ context.Context, location string) ([]byte, error) {
	c, ok := f.files[location]
	if !ok {
		return nil, errors.New("404 not found")
	}
	return []byte(c), nil
}

func (f affectFetcher) Services(context.Context) (map[string]map[string]bool, error) {
	return f.services, nil
}

// TestRegistryReachNeedsAnAffectingAction (D-212): a registry edit that puts
// an entity under a container something targets needs control on it only if
// one of the targeting actions can affect the entity's domain — a lock under
// a label only homeassistant.turn_off targets is fine, under an area a
// lock.lock targets is not — and the check names the targeter.
func TestRegistryReachNeedsAnAffectingAction(t *testing.T) {
	const pol = `version: 1
active: house
profiles:
  house:
    deny:
      "entity:lock.front_door": [control]
`
	auto := func(id, actions string) string {
		return "id: " + id + "\ntriggers:\n  - trigger: time\n    at: '23:00'\nactions:\n" + actions
	}
	newF := func(withServices bool) affectFetcher {
		tf := &targetFetcher{
			fakeFetcher: fakeFetcher{
				reg: &reqmap.Registries{Entities: []reqmap.RegistryEntity{
					{EntityID: "lock.front_door"}, {EntityID: "light.hall"},
					{EntityID: "script.hall_lock", UniqueID: "hall_lock"},
				}}, complete: true,
				live: []string{"lock.front_door", "light.hall", "automation.downstairs_off", "automation.hallway_area_lock",
					"automation.lights_only", "automation.porch_watch", "automation.tpl", "automation.zw", "automation.gone", "script.hall_lock"},
				autoIDs: map[string]string{"automation.downstairs_off": "downstairs_off", "automation.hallway_area_lock": "hallway_area_lock",
					"automation.lights_only": "lights_only", "automation.porch_watch": "porch_watch", "automation.tpl": "tpl", "automation.zw": "zw"},
			},
			related: map[string]map[string][]string{
				"label:downstairs":  {"automation": {"automation.downstairs_off"}},
				"area:hallway":      {"automation": {"automation.hallway_area_lock"}},
				"label:lightsonly":  {"automation": {"automation.lights_only"}},
				"entity:lock.porch": {"automation": {"automation.porch_watch"}},
				"label:tpl":         {"automation": {"automation.tpl"}},
				"label:zw":          {"automation": {"automation.zw"}},
				"label:missing":     {"automation": {"automation.gone"}},
				"label:scripted":    {"script": {"script.hall_lock"}},
			},
			grep: map[string]enforce.ConfigGrepResult{
				"grepped": {Hits: []enforce.ConfigGrepHit{{Location: "automations.yaml", Path: "[0].actions[0].target.label_id", Key: "label_id", MatchedValue: "grepped"}}},
				"tplent":  {Hits: []enforce.ConfigGrepHit{{Location: "templates.yaml", Path: "[0].sensor[0].state", Key: "state", MatchedValue: "{{ label_entities('tplent') | count }}"}}},
			},
		}
		f := affectFetcher{targetFetcher: tf, configs: map[string]string{
			"automation:downstairs_off":    auto("downstairs_off", "  - action: homeassistant.turn_off\n    target: {label_id: downstairs}\n"),
			"automation:hallway_area_lock": auto("hallway_area_lock", "  - action: lock.lock\n    target: {area_id: [kitchen, hallway]}\n"),
			"automation:lights_only":       auto("lights_only", "  - action: light.turn_on\n    target: {label_id: lightsonly}\n  - action: notify.notify\n    data: {message: lightsonly}\n"),
			"automation:porch_watch": "id: porch_watch\ntriggers:\n  - trigger: state\n    entity_id: lock.porch\nconditions:\n  - condition: state\n    entity_id: lock.porch\n    state: locked\n" +
				"actions:\n  - action: light.turn_on\n    target: {entity_id: light.hall}\n",
			"automation:tpl":       auto("tpl", "  - action: light.turn_on\n    target:\n      label_id: \"{{ 'tpl' }}\"\n"),
			"automation:zw":        auto("zw", "  - action: zwave_js.set_value\n    target: {label_id: zw}\n"),
			"automation:grep_auto": auto("grep_auto", "  - action: homeassistant.turn_off\n    target: {label_id: grepped}\n"),
			"script:hall_lock":     "sequence:\n  - action: lock.lock\n    data: {label_id: scripted}\n",
		}, files: map[string]string{
			"automations.yaml": "- " + strings.ReplaceAll(auto("grep_auto", "  - action: homeassistant.turn_off\n    target: {label_id: grepped}\n"), "\n", "\n  "),
			"templates.yaml":   "- sensor:\n  - name: n\n    unique_id: n\n    state: \"{{ label_entities('tplent') | count }}\"\n",
		}}
		if withServices {
			f.services = map[string]map[string]bool{
				"homeassistant": {"turn_on": true, "turn_off": true, "toggle": true},
				"light":         {"turn_on": true, "turn_off": true, "toggle": true},
				"lock":          {"lock": true, "unlock": true},
			}
		}
		return f
	}
	const lock = "control entity:lock.front_door"
	for _, c := range []struct {
		name     string
		change   enforce.RegistryChange
		services bool
		want     string // "" = no check; else the Via
	}{
		{"homeassistant.turn_off on the label cannot switch a lock", enforce.RegistryChange{EntityID: "lock.front_door", Labels: labels("downstairs")}, true, ""},
		{"without a service list it can (a superset)", enforce.RegistryChange{EntityID: "lock.front_door", Labels: labels("downstairs")}, false, "because automation.downstairs_off targets label:downstairs"},
		{"lock.lock on the area, in a list", enforce.RegistryChange{EntityID: "lock.front_door", AreaSet: true, AreaID: "hallway"}, true, "because automation.hallway_area_lock targets area:hallway"},
		{"light.turn_on and notify data never switch a lock", enforce.RegistryChange{EntityID: "lock.front_door", Labels: labels("lightsonly")}, true, ""},
		{"a mention in a trigger or condition only", enforce.RegistryChange{EntityID: "lock.front_door", NewEntityID: "lock.porch"}, true, ""},
		{"a template naming the container", enforce.RegistryChange{EntityID: "lock.front_door", Labels: labels("tpl")}, true, "because automation.tpl targets label:tpl"},
		{"a service of a domain with no live entity", enforce.RegistryChange{EntityID: "lock.front_door", Labels: labels("zw")}, true, "because automation.zw targets label:zw"},
		{"a config hactl cannot read", enforce.RegistryChange{EntityID: "lock.front_door", Labels: labels("missing")}, true, "because automation.gone targets label:missing"},
		{"a script naming the label in its data", enforce.RegistryChange{EntityID: "lock.front_door", Labels: labels("scripted")}, true, "because script.hall_lock targets label:scripted"},
		{"a grep hit placed in an automation is read as it", enforce.RegistryChange{EntityID: "lock.front_door", Labels: labels("grepped")}, true, ""},
		{"a grep hit in a template entity counts", enforce.RegistryChange{EntityID: "lock.front_door", Labels: labels("tplent")}, true, "-"},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := newF(c.services)
			s, _ := newSession(t, pol, f)
			checks, denials := s.RegistryReachChecks(context.Background(), c.change)
			if len(denials) != 0 {
				t.Fatalf("denials %+v", denials)
			}
			switch {
			case c.want == "" && len(checks) != 0:
				t.Errorf("checks %q, want none (asked %q)", relabelStrings(checks), f.asks)
			case c.want != "" && (len(checks) != 1 || checks[0].String() != lock):
				t.Errorf("checks %q, want %s (asked %q)", relabelStrings(checks), lock, f.asks)
			case c.want != "" && c.want != "-" && checks[0].Via != c.want:
				t.Errorf("via %q, want %q", checks[0].Via, c.want)
			}
		})
	}

	// The refusal names the targeter after its rule.
	s, _ := newSession(t, pol, newF(true))
	err := s.Check(context.Background(), reqmap.Request{Transport: reqmap.WS, Type: "config/entity_registry/update",
		Params: map[string]any{"entity_id": "lock.front_door", "area_id": "hallway"}})
	if !strings.Contains(refusalText(err), `control entity:lock.front_door — deny "entity:lock.front_door" [control] (line 6) (because automation.hallway_area_lock targets area:hallway)`) {
		t.Errorf("refusal: %v\n%s", err, refusalText(err))
	}
}
