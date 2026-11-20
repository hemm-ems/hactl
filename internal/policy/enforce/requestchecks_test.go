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

func checkStrings(cs []reqmap.Check) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.String())
	}
	return out
}

// TestRequestChecksIsTheRequestLayersMapping (D-76, D-88, R-12): a delete
// cascade planned through RequestChecks is exactly what the request layer
// checks when the same request is sent — enumerated from the same registry
// answer, asked once — and a holder the registries name is in it.
func TestRequestChecksIsTheRequestLayersMapping(t *testing.T) {
	f := &fakeFetcher{reg: &reqmap.Registries{
		Entities: []reqmap.RegistryEntity{{EntityID: "light.a", Labels: []string{"night"}}, {EntityID: "light.b"}},
		Devices:  []reqmap.RegistryDevice{{ID: "d1", Labels: []string{"night"}, ConfigEntries: []string{}}},
		Areas:    []reqmap.RegistryArea{{ID: "hall", Labels: []string{"night", "x"}}},
	}, complete: true}
	rec := &recorder{}
	s, _ := newSession(t, "version: 1\nactive: a\nprofiles:\n  a: {}\n", f, withRecorder(rec))
	req := reqmap.Request{Transport: reqmap.WS, Type: "config/label_registry/delete", Params: map[string]any{"label_id": "night"}}

	planned, denial := s.RequestChecks(context.Background(), req)
	if denial != nil {
		t.Fatalf("a mapped write was not planned: %+v", denial)
	}
	// The classifier's order: by verb (edit before delete), then target.
	want := []string{"edit area:hall", "edit device:d1/registry/labels", "edit entity:light.a/registry/labels", "delete label:night"}
	if got := checkStrings(planned); !slices.Equal(got, want) {
		t.Errorf("plan = %q, want %q", got, want)
	}
	if err := s.Check(context.Background(), req); err != nil {
		t.Fatalf("the planned request was refused: %v", err)
	}
	if !slices.Equal(rec.requests, want) {
		t.Errorf("request layer checked %v, the plan %q", rec.requests, want)
	}
	if n := strings.Count(strings.Join(f.asked, "\n"), "registries"); n != 1 {
		t.Errorf("registries asked %d times (%q); the plan and the request share one answer", n, f.asked)
	}
}

// TestRequestChecksWithoutAnAnswerIsASuperset (D-76, D-88): registries that
// cannot be read, or a device list that does not say which entries a device
// belongs to, never shrink a cascade — it is checked on every id of the kind.
func TestRequestChecksWithoutAnAnswerIsASuperset(t *testing.T) {
	for name, f := range map[string]*fakeFetcher{
		"no answer":           {regErr: context.DeadlineExceeded},
		"incomplete registry": {reg: &reqmap.Registries{Devices: []reqmap.RegistryDevice{{ID: "d1"}}}, complete: false},
	} {
		s, _ := newSession(t, "version: 1\nactive: a\nprofiles:\n  a: {}\n", f)
		req := reqmap.Request{Transport: reqmap.REST, Method: "DELETE", Path: "/api/config/config_entries/entry/e1"}
		planned, denial := s.RequestChecks(context.Background(), req)
		if denial != nil {
			t.Fatalf("%s: %+v", name, denial)
		}
		want := []string{"delete config_entry:e1", "delete device:*", "delete entity:*"}
		if got := checkStrings(planned); !slices.Equal(got, want) {
			t.Errorf("%s: plan = %q, want %q", name, got, want)
		}
	}
}

// TestRequestChecksRefusesWhatItCannotPlan: a request that is not a write, or
// whose id the grammar refuses (D-69), cannot be planned — the plan carries a
// fail-closed denial naming why, never an empty set of checks.
func TestRequestChecksRefusesWhatItCannotPlan(t *testing.T) {
	s, _ := newSession(t, "version: 1\nactive: a\nprofiles:\n  a: {}\n", &fakeFetcher{reg: &reqmap.Registries{}, complete: true})
	for _, tc := range []struct {
		req  reqmap.Request
		rule string
	}{
		{reqmap.Request{Transport: reqmap.WS, Type: "config/label_registry/delete", Params: map[string]any{"label_id": "a\u200bb"}}, "fail-closed: unmappable"},
		{reqmap.Request{Transport: reqmap.WS, Type: "config/label_registry/list"}, "fail-closed: unmapped request ws config/label_registry/list"},
		{reqmap.Request{Transport: reqmap.WS, Type: "no/such_type"}, "fail-closed: unmapped request ws no/such_type"},
	} {
		planned, denial := s.RequestChecks(context.Background(), tc.req)
		if denial == nil || len(planned) != 0 || !strings.HasPrefix(denial.Rule, tc.rule) {
			t.Errorf("%s: checks %q, denial %+v; want a denial %q…", tc.req.Type, checkStrings(planned), denial, tc.rule)
		}
	}
}

func withRecorder(r enforce.Recorder) func(*enforce.Options) {
	return func(o *enforce.Options) { o.Recorder = r }
}

// TestRequestChecksReloadConfigEntryNeedsCompleteRegistries (D-200): a
// config entry reload names the entities it resets from the registries —
// only from a complete read; an incomplete one refuses.
func TestRequestChecksReloadConfigEntryNeedsCompleteRegistries(t *testing.T) {
	reg := &reqmap.Registries{Entities: []reqmap.RegistryEntity{
		{EntityID: "light.a", ConfigEntryID: "ce1"}, {EntityID: "light.b", ConfigEntryID: "ce2"},
	}}
	req := reqmap.Request{Transport: reqmap.REST, Method: "POST", Path: "/api/services/homeassistant/reload_config_entry",
		Body: []byte(`{"entry_id":"ce1"}`)}
	s, _ := newSession(t, "version: 1\nactive: a\nprofiles:\n  a: {}\n", &fakeFetcher{reg: reg, complete: true, live: []string{"light.a", "light.b"}})
	planned, denial := s.RequestChecks(context.Background(), req)
	if want := []string{"control entity:light.a", "control service:homeassistant.reload_config_entry"}; denial != nil || !slices.Equal(checkStrings(planned), want) {
		t.Errorf("complete registries: plan %q, denial %+v; want %q", checkStrings(planned), denial, want)
	}
	s, _ = newSession(t, "version: 1\nactive: a\nprofiles:\n  a: {}\n", &fakeFetcher{reg: reg, complete: false, live: []string{"light.a", "light.b"}})
	planned, denial = s.RequestChecks(context.Background(), req)
	if denial == nil || !strings.Contains(denial.Rule, "config entry reach unknown") {
		t.Errorf("incomplete registries: plan %q, denial %+v; want refused, config entry reach unknown", checkStrings(planned), denial)
	}
}

// TestTemplateEvalReadsTheLiveStates (D-198): `tpl eval` resolves what a
// template reads against the live states — `states.light` reads each live
// light, so a denied one refuses even where system:template is allowed.
func TestTemplateEvalReadsTheLiveStates(t *testing.T) {
	const pol = `version: 1
active: a
profiles:
  a:
    default: deny
    allow:
      "entity:*": [read]
      "system:template": [read]
    deny:
      "entity:light.b": [read]
`
	s, _ := newSession(t, pol, &fakeFetcher{reg: &reqmap.Registries{}, complete: true, live: []string{"light.a", "light.b"}})
	req := reqmap.Request{Transport: reqmap.REST, Method: "POST", Path: "/api/template",
		Body: []byte(`{"template":"{{ states.light | count }}"}`)}
	err := s.Check(context.Background(), req)
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) || !slices.ContainsFunc(ref.Denied, func(d enforce.Denial) bool { return d.Resource == "entity:light.b" }) {
		t.Errorf("tpl eval over a denied live light: %v, want refused naming entity:light.b", err)
	}
	req.Body = []byte(`{"template":"{{ states('light.a') }}"}`)
	if err := s.Check(context.Background(), req); err != nil {
		t.Errorf("tpl eval of an allowed state: %v", err)
	}
}
