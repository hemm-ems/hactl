package enforce_test

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/enforce"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

const candidatePolicy = `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*": [read]
      "automation:pg_*": [read, edit, create]
      "service:light.*": [control]
      "entity:light.*": [control]
    deny:
      "entity:lock.*": [read, write]
`

func candidateCheckStrings(cs []reqmap.Check) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.String())
	}
	slices.Sort(out)
	return out
}

// TestCandidateChecksAreTheServiceCallsItWouldMake — each action is checked
// as the svc call it makes (the service and its target), a mention as read
// (D-133).
func TestCandidateChecksAreTheServiceCallsItWouldMake(t *testing.T) {
	reg := &reqmap.Registries{Entities: []reqmap.RegistryEntity{
		{EntityID: "light.hall", Platform: "demo"}, {EntityID: "lock.front", Platform: "demo"}, {EntityID: "binary_sensor.door", Platform: "demo"},
	}}
	f := &fakeFetcher{reg: reg, complete: true, live: []string{"light.hall", "lock.front", "binary_sensor.door"}, reach: map[string][]string{
		`{"entity_id":["lock.front"]}`: {"lock.front"},
		`{"entity_id":["light.hall"]}`: {"light.hall"},
	}}
	s, _ := newSession(t, candidatePolicy, f)
	checks, denials := s.CandidateChecks(context.Background(), []byte(`
id: pg_a
triggers: [{trigger: state, entity_id: binary_sensor.door}]
actions:
  - action: light.turn_on
    target: {entity_id: light.hall}
  - action: lock.unlock
    target: {entity_id: lock.front}
`))
	if len(denials) != 0 {
		t.Fatalf("denials = %+v", denials)
	}
	want := []string{"control entity:light.hall", "control entity:lock.front", "control service:light.turn_on", "control service:lock.unlock", "read entity:binary_sensor.door"}
	if got := candidateCheckStrings(checks); !slices.Equal(got, want) {
		t.Errorf("checks = %q, want %q", got, want)
	}
}

// TestCandidateChecksRefusesAnUnreadableCandidate — a candidate that does not
// parse, or whose live set cannot be read, is refused: never "nothing found".
func TestCandidateChecksRefusesAnUnreadableCandidate(t *testing.T) {
	s, _ := newSession(t, candidatePolicy, &fakeFetcher{live: []string{"light.hall"}})
	_, denials := s.CandidateChecks(context.Background(), []byte("actions: [\n"))
	if len(denials) != 1 || denials[0].Rule != reqmap.RuleCandidateUnreadable {
		t.Errorf("unparsable candidate: denials = %+v, want %s", denials, reqmap.RuleCandidateUnreadable)
	}
	s2, _ := newSession(t, candidatePolicy, &fakeFetcher{liveErr: errors.New("down")})
	_, denials = s2.CandidateChecks(context.Background(), []byte("actions: []\n"))
	if len(denials) != 1 || denials[0].Rule != enforce.RuleLiveUnread {
		t.Errorf("live set unread: denials = %+v, want %s", denials, enforce.RuleLiveUnread)
	}
}

// TestCandidateChecksOffUnderIndirectUnchecked — D-135.
func TestCandidateChecksOffUnderIndirectUnchecked(t *testing.T) {
	body := strings.Replace(candidatePolicy, "    default: deny\n", "    default: deny\n    indirect: unchecked\n", 1)
	f := &fakeFetcher{live: []string{"lock.front"}}
	s, _ := newSession(t, body, f)
	checks, denials := s.CandidateChecks(context.Background(), []byte("actions: [{action: lock.unlock, target: {entity_id: lock.front}}]\n"))
	if len(checks) != 0 || len(denials) != 0 || len(f.asked) != 0 {
		t.Errorf("indirect: unchecked still checked: %v %v, asked %q", checks, denials, f.asked)
	}
}

// TestRequestLayerChecksTheCandidateOnItsOwn — the companion write that
// carries a candidate is refused for what the candidate would do, without any
// command plan: the request layer is the guarantee (H-29), the plan its
// preview. An automation that unlocks a denied lock is refused; the same
// automation switching an allowed light passes; a templated target is
// refused fail-closed.
func TestRequestLayerChecksTheCandidateOnItsOwn(t *testing.T) {
	reg := &reqmap.Registries{Entities: []reqmap.RegistryEntity{
		{EntityID: "light.hall", Platform: "demo"}, {EntityID: "lock.front", Platform: "demo"},
	}}
	newF := func() *fakeFetcher {
		return &fakeFetcher{reg: reg, complete: true, live: []string{"light.hall", "lock.front"}, reach: map[string][]string{
			`{"entity_id":["lock.front"]}`: {"lock.front"},
			`{"entity_id":["light.hall"]}`: {"light.hall"},
		}}
	}
	req := func(method, target string) reqmap.Request {
		path := "/v1/config/automation"
		if method == "PUT" {
			path += "?id=pg_a"
		}
		return reqmap.Request{Transport: reqmap.Companion, Method: method, Path: path,
			Body: []byte("id: pg_a\nalias: a\ntriggers: []\nactions:\n  - action: " + target + "\n")}
	}
	for _, method := range []string{"POST", "PUT"} {
		s, _ := newSession(t, candidatePolicy, newF())
		err := s.Check(context.Background(), req(method, "lock.unlock\n    target: {entity_id: lock.front}"))
		var ref *enforce.DeniedError
		if !errors.As(err, &ref) || !slices.ContainsFunc(ref.Denied, func(d enforce.Denial) bool { return d.Resource == "entity:lock.front" }) {
			t.Errorf("%s with an action on the denied lock: Check = %v, want a refusal naming entity:lock.front", method, err)
		}
		s, _ = newSession(t, candidatePolicy, newF())
		if err := s.Check(context.Background(), req(method, "light.turn_on\n    target: {entity_id: light.hall}")); err != nil {
			t.Errorf("%s with an allowed action: Check = %v, want allowed", method, err)
		}
		s, _ = newSession(t, candidatePolicy, newF())
		err = s.Check(context.Background(), req(method, "light.turn_on\n    target: {entity_id: \"{{ 'lock.front' }}\"}"))
		if !errors.As(err, &ref) || !strings.Contains(err.Error()+fmt.Sprint(ref.Denied), reqmap.RuleActionTargetTemplate) {
			t.Errorf("%s with a templated target: Check = %v, want %s", method, err, reqmap.RuleActionTargetTemplate)
		}
	}
}
