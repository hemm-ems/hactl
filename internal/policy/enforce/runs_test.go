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

// storedFetcher is a fakeFetcher that also reads stored configs (D-207),
// keyed `<kind>:<id>`.
type storedFetcher struct {
	*fakeFetcher

	configs map[string]string
}

func (f storedFetcher) StoredConfig(_ context.Context, kind, id string) ([]byte, error) {
	f.asked = append(f.asked, "stored "+kind+":"+id)
	c, ok := f.configs[kind+":"+id]
	if !ok {
		return nil, errors.New("404 not found")
	}
	return []byte(c), nil
}

const runsPolicy = `version: 1
active: house
profiles:
  house:
    deny:
      "entity:lock.front_door": [control]
`

func runsFetcher() storedFetcher {
	ents := make([]reqmap.RegistryEntity, 0, 6)
	for _, uid := range []string{"goodnight", "inner", "allowed", "loop_a", "loop_b"} {
		ents = append(ents, reqmap.RegistryEntity{EntityID: "script." + uid, UniqueID: uid})
	}
	ents = append(ents, reqmap.RegistryEntity{EntityID: "script.unkeyed"})
	f := &fakeFetcher{
		reg: &reqmap.Registries{Entities: ents}, complete: true,
		live: []string{"light.hall", "lock.front_door", "automation.night_lockup", "script.goodnight",
			"script.inner", "script.allowed", "script.loop_a", "script.loop_b", "script.unkeyed"},
		autoIDs: map[string]string{"automation.night_lockup": "night_lockup"},
		reach:   map[string][]string{},
	}
	for _, id := range f.live {
		f.reach[`{"entity_id":["`+id+`"]}`] = []string{id}
	}
	return storedFetcher{f, map[string]string{
		// keyed by the script, as the companion returns it
		"script:goodnight": "goodnight:\n  alias: Goodnight\n  sequence:\n    - action: light.turn_off\n      target: {entity_id: light.hall}\n    - action: lock.lock\n      target: {entity_id: lock.front_door}\n",
		"script:allowed":   "sequence:\n  - action: light.turn_off\n    target: {entity_id: light.hall}\n",
		"script:inner":     "sequence:\n  - action: lock.lock\n    target: {entity_id: lock.front_door}\n",
		"automation:night_lockup": "id: night_lockup\ntriggers:\n  - trigger: time\n    at: '23:00'\nconditions:\n  - condition: state\n    entity_id: light.hall\n    state: 'off'\n" +
			"actions:\n  - action: script.turn_on\n    target: {entity_id: script.inner}\n",
		"script:loop_a": "sequence:\n  - action: script.loop_b\n  - action: light.turn_on\n    target: {entity_id: light.hall}\n",
		"script:loop_b": "sequence:\n  - action: script.turn_on\n    target: {entity_id: script.loop_a}\n",
	}}
}

func runCall(t *testing.T, body string, f enforce.Fetcher, domain, service, data string) error {
	t.Helper()
	s, _ := newSession(t, body, f)
	return s.Check(context.Background(), reqmap.Request{Transport: reqmap.REST, Method: "POST",
		Path: "/api/services/" + domain + "/" + service, Body: []byte(data)})
}

func deniedWith(err error, resource, via string) bool {
	var ref *enforce.DeniedError
	return errors.As(err, &ref) && slices.ContainsFunc(ref.Denied, func(d enforce.Denial) bool {
		return d.Resource == resource && d.Via == via
	})
}

// TestRunChecksExpandStoredConfig (D-207): a call that starts a script or
// runs an automation is checked as what the stored config does — through a
// per-script service, script.turn_on, homeassistant.turn_on on a script
// entity and automation.trigger, a nested start included — and the refusal
// names the run path. An allowed script still runs; a config that cannot be
// read refuses; `indirect: unchecked` and a profile denying nothing expand
// nothing.
func TestRunChecksExpandStoredConfig(t *testing.T) {
	const lock = "entity:lock.front_door"
	err := runCall(t, runsPolicy, runsFetcher(), "script", "goodnight", `{}`)
	if !deniedWith(err, lock, "run of script.goodnight: action at /sequence/1") {
		t.Errorf("script.goodnight: %v, want the lock refused with its run path", err)
	}
	if err == nil || !strings.Contains(err.Error()+refusalText(err), "(run of script.goodnight: action at /sequence/1)") {
		t.Errorf("the refusal text does not name the run path: %v %s", err, refusalText(err))
	}
	for _, c := range []struct{ domain, service, data string }{
		{"script", "turn_on", `{"entity_id":"script.goodnight"}`},
		{"script", "toggle", `{"entity_id":"script.goodnight"}`},
		{"homeassistant", "turn_on", `{"entity_id":"script.goodnight"}`},
	} {
		if cerr := runCall(t, runsPolicy, runsFetcher(), c.domain, c.service, c.data); !deniedWith(cerr, lock, "run of script.goodnight: action at /sequence/1") {
			t.Errorf("%s.%s: %v, want the lock refused", c.domain, c.service, cerr)
		}
	}
	err = runCall(t, runsPolicy, runsFetcher(), "automation", "trigger", `{"entity_id":"automation.night_lockup"}`)
	if !deniedWith(err, lock, "run of automation.night_lockup: action at /actions/0 → run of script.inner: action at /sequence/0") {
		t.Errorf("automation.trigger: %v, want the lock refused through script.inner", err)
	}
	if err := runCall(t, runsPolicy, runsFetcher(), "script", "turn_on", `{"entity_id":"script.allowed"}`); err != nil {
		t.Errorf("an allowed script: %v, want it to run", err)
	}

	// Unreadable: a script the registries hold no key for, a config the
	// companion does not have, a fetcher that cannot read configs.
	for name, c := range map[string]struct {
		f    enforce.Fetcher
		data string
	}{
		"no config key":   {runsFetcher(), `{"entity_id":"script.unkeyed"}`},
		"no such config":  {func() enforce.Fetcher { f := runsFetcher(); delete(f.configs, "script:allowed"); return f }(), `{"entity_id":"script.allowed"}`},
		"no companion":    {runsFetcher().fakeFetcher, `{"entity_id":"script.allowed"}`},
		"an empty config": {func() enforce.Fetcher { f := runsFetcher(); f.configs["script:allowed"] = ""; return f }(), `{"entity_id":"script.allowed"}`},
		"a config that does not parse": {func() enforce.Fetcher {
			f := runsFetcher()
			f.configs["script:allowed"] = "sequence: [\n"
			return f
		}(), `{"entity_id":"script.allowed"}`},
	} {
		err := runCall(t, runsPolicy, c.f, "script", "turn_on", c.data)
		if err == nil || !strings.Contains(refusalText(err), enforce.RuleStoredConfigUnreadable) {
			t.Errorf("%s: %v, want %s", name, err, enforce.RuleStoredConfigUnreadable)
		}
	}

	unchecked := strings.Replace(runsPolicy, "    deny:\n", "    indirect: unchecked\n    deny:\n", 1)
	if err := runCall(t, unchecked, runsFetcher(), "script", "goodnight", `{}`); err != nil {
		t.Errorf("indirect: unchecked: %v, want the run unexpanded", err)
	}
	f := runsFetcher()
	if err := runCall(t, "version: 1\nactive: a\nprofiles:\n  a: {}\n", f, "script", "goodnight", `{}`); err != nil {
		t.Errorf("a profile denying nothing: %v", err)
	}
	if slices.ContainsFunc(f.asked, func(a string) bool { return strings.HasPrefix(a, "stored ") }) {
		t.Errorf("a profile denying nothing read stored configs: %q", f.asked)
	}
}

// TestRunChecksCycleGuard (D-207): scripts that start each other are expanded
// once each, and what they do is still checked.
func TestRunChecksCycleGuard(t *testing.T) {
	f := runsFetcher()
	s, _ := newSession(t, runsPolicy, f)
	checks, denial := s.ServiceCallChecks(context.Background(), "script", "turn_on", map[string]any{"entity_id": "script.loop_a"})
	if denial != nil {
		t.Fatalf("a cycle: %+v", denial)
	}
	got := checkStrings(checks)
	for _, want := range []string{"control entity:light.hall", "control script:loop_b/run", "control script:loop_a/run"} {
		if !slices.Contains(got, want) {
			t.Errorf("checks %q lack %q", got, want)
		}
	}
	n := 0
	for _, a := range f.asked {
		if strings.HasPrefix(a, "stored ") {
			n++
		}
	}
	if n != 2 {
		t.Errorf("stored configs read %d times (%q), want loop_a and loop_b once each", n, f.asked)
	}
}

// refusalText is every denied line of a refusal.
func refusalText(err error) string {
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) {
		return ""
	}
	var b strings.Builder
	for _, d := range ref.Denied {
		b.WriteString(d.Text() + "\n")
	}
	return b.String()
}
