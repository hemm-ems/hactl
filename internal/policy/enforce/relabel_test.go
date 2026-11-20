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
}
