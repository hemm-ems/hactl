package enforce

import (
	"context"

	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// RuleLiveUnread refuses a candidate whose mentions cannot be found because
// the live entity set could not be read.
const RuleLiveUnread = "fail-closed: live entities unread"

// CandidateChecks is what writing candidate — an automation, a script or a
// template entity, as the body hactl is about to send — would do (W-1, E-3;
// D-56 as refined by D-133, D-134, D-137):
//
//   - each action is checked exactly as a `svc call` of it would be
//     (ServiceCallChecks: the service, its reach, the entities its data names,
//     group and scene members, the scripts it starts);
//   - each entity a device action names needs control;
//   - every other live entity the candidate mentions needs read;
//   - what cannot be read off the candidate (a templated service name or
//     target, a device action's entity that does not resolve) refuses it.
//
// The command layer adds these to its plan and the request layer to the
// companion write that carries the candidate, through this one function, so
// both decide alike (R-12). With `indirect: unchecked` on the profile
// (D-135) nothing is added.
func (s *Session) CandidateChecks(ctx context.Context, candidate []byte) ([]reqmap.Check, []Denial) {
	if !s.pol.IndirectChecked() {
		return nil, nil
	}
	var doc any
	if err := yaml.Unmarshal(candidate, &doc); err != nil {
		return nil, []Denial{{Resource: "candidate", Verb: policy.Control.String(), Rule: reqmap.RuleCandidateUnreadable}}
	}
	s.gather(ctx, reqmap.Request{}, needs{live: true, registries: true})
	s.mu.Lock()
	var live *reqmap.LiveStates
	var reg *reqmap.Registries
	if s.cache.liveRead {
		live = s.cache.live
	}
	if s.cache.regRead {
		reg = s.cache.registries
	}
	s.mu.Unlock()
	if live == nil {
		return nil, []Denial{{Resource: "candidate", Verb: policy.Control.String(), Rule: RuleLiveUnread}}
	}
	use := reqmap.CandidateUses(doc, live, reg)

	var checks []reqmap.Check
	var denials []Denial
	for _, a := range use.Actions {
		cs, d := s.ServiceCallChecks(ctx, a.Domain, a.Service, a.Data)
		if d != nil {
			d.Resource += " (action at " + a.Path + ")"
			denials = append(denials, *d)
			continue
		}
		checks = append(checks, cs...)
	}
	for _, id := range use.Switch {
		if c, ok := entityCheck(policy.Control, id); ok {
			checks = append(checks, c)
		} else {
			denials = append(denials, Denial{Resource: "entity:" + id, Verb: policy.Control.String(), Rule: "fail-closed: unmappable resource entity:" + id})
		}
	}
	for _, id := range use.Read {
		if c, ok := entityCheck(policy.Read, id); ok {
			checks = append(checks, c)
		}
	}
	for _, u := range use.Undecidable {
		denials = append(denials, Denial{Resource: "candidate " + u.Path, Verb: policy.Control.String(), Rule: u.Rule})
	}
	return dedupeChecks(checks), denials
}

func entityCheck(v policy.Verb, id string) (reqmap.Check, bool) {
	r, err := policy.ParseResource("entity:" + id)
	if err != nil {
		return reqmap.Check{}, false
	}
	return reqmap.Check{Verb: v, Resource: r}, true
}

// dedupeChecks keeps the first of each identical check, in order.
func dedupeChecks(in []reqmap.Check) []reqmap.Check {
	seen := map[string]bool{}
	out := in[:0]
	for _, c := range in {
		k := c.String()
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, c)
	}
	return out
}
