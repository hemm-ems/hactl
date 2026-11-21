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
//     target, a device action's entity that does not resolve) refuses it;
//   - what a template reads is checked as reads where hactl can name them
//     (D-198, D-201): `states('x')` needs `read entity:x/state`, a state
//     object read whole is refused by a deny on any part of the entity; a
//     construct whose reach hactl cannot name (D-188) needs `read
//     system:template` (D-190); a template that does not parse, or that
//     rebinds a runtime name (D-204), refuses it; a data field naming
//     entities that holds a template hactl cannot fold refuses it (D-189);
//   - what a state, numeric_state, zone or device condition or trigger
//     compares needs read (D-202);
//   - a `use_blueprint` is checked as what it expands to: the blueprint of
//     domain (automation, script, template) read through the fetcher, its
//     `!input`s substituted (D-185); one that cannot be expanded refuses it.
//
// The command layer adds these to its plan and the request layer to the
// companion write that carries the candidate, through this one function, so
// both decide alike (R-12). With `indirect: unchecked` on the profile
// (D-135) nothing is added.
func (s *Session) CandidateChecks(ctx context.Context, domain string, candidate []byte) ([]reqmap.Check, []Denial) {
	if !s.pol.IndirectChecked() {
		return nil, nil
	}
	var doc any
	if err := yaml.Unmarshal(candidate, &doc); err != nil {
		return nil, []Denial{{Resource: "candidate", Verb: policy.Control.String(), Rule: reqmap.RuleCandidateUnreadable}}
	}
	return s.candidateDoc(ctx, domain, doc, nil)
}

// candidateDoc is CandidateChecks of a parsed candidate. via, when set,
// renders a run path (D-207) from a place in the candidate ("" for none):
// every check and denial then carries it.
func (s *Session) candidateDoc(ctx context.Context, domain string, doc any, via func(path string) string) ([]reqmap.Check, []Denial) {
	if !s.pol.IndirectChecked() {
		return nil, nil
	}
	// A blueprint's actions are in its file: check what it expands to (D-185).
	doc, unexpanded := reqmap.ExpandBlueprints(doc, domain, s.blueprintReader(ctx))
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
		if via != nil {
			for i := range cs {
				cs[i].Via = joinVia(via("action at "+a.Path), cs[i].Via)
			}
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
	rc, rd := s.candidateReads(use)
	checks, denials = append(checks, rc...), append(denials, rd...)
	for _, u := range append(unexpanded, use.Undecidable...) {
		denials = append(denials, Denial{Resource: "candidate " + u.Path, Verb: policy.Control.String(), Rule: u.Rule})
	}
	if via != nil {
		applyVia(checks, denials, via(""))
	}
	return dedupeChecks(checks), denials
}

// applyVia gives every check without a run path, and every denial, the
// path outer (D-207).
func applyVia(checks []reqmap.Check, denials []Denial, outer string) {
	for i := range checks {
		if checks[i].Via == "" {
			checks[i].Via = outer
		}
	}
	for i := range denials {
		denials[i].Via = joinVia(outer, denials[i].Via)
	}
}

// candidateReads decides what a candidate's templates read and its
// conditions compare, so the refusal names the construct: a whole read is
// refused by a deny on any part (D-198); a construct whose reach hactl cannot
// name needs `read system:template` (D-188, D-190).
func (s *Session) candidateReads(use reqmap.CandidateUse) ([]reqmap.Check, []Denial) {
	var checks []reqmap.Check
	var denials []Denial
	for _, r := range use.TemplateReads {
		if d := s.candidateRead(r.Check, " — the template at "+r.Path+" uses "+r.Construct); d != nil {
			denials = append(denials, *d)
		} else {
			checks = append(checks, r.Check)
		}
	}
	for _, r := range use.Compared {
		if d := s.candidateRead(r.Check, " — the "+r.Construct+" at "+r.Path+" compares it"); d != nil {
			denials = append(denials, *d)
		} else {
			checks = append(checks, r.Check)
		}
	}
	// A construct whose reach hactl cannot name needs `read system:template`
	// (D-188, D-190).
	if len(use.Templates) > 0 {
		tpl := reqmap.Check{Verb: policy.Read, Resource: policy.Resource{Kind: policy.KindSystem, ID: "template"}}
		if v := s.decideRead(tpl); v.Allowed {
			checks = append(checks, tpl)
		} else {
			for _, u := range use.Templates {
				denials = append(denials, Denial{Resource: tpl.Resource.String(), Verb: policy.Read.String(),
					Rule: v.Rule + " — the template at " + u.Path + " uses " + u.Construct, Line: v.Line})
			}
		}
	}
	return checks, denials
}

// joinVia is a run path followed by the one it leads into.
func joinVia(outer, inner string) string {
	if inner == "" {
		return outer
	}
	return outer + " → " + inner
}

// candidateRead decides one read a candidate makes: nil when allowed, else
// the denial, its rule followed by why.
func (s *Session) candidateRead(c reqmap.Check, why string) *Denial {
	v := s.decideRead(c)
	switch {
	case !v.Allowed:
		d := denial(c, v)
		d.Rule += why
		return &d
	case c.Whole && v.Partial:
		ref, denied := s.wholeRead(c, v)
		if !denied {
			return nil
		}
		d := denial(c, v)
		d.Rule, d.Line = ref.Rule+why, ref.Line
		d.Partial = true
		return &d
	}
	return nil
}

// wholeRead decides a whole read whose verdict is partial (D-66, D-198):
// whether a denied part refuses it, and the rule that decided that part.
// For an entity's state object only the parts its live state holds count —
// `state` and each attribute it has, in that order, attributes by name
// (D-209). Anything else — an entity without a live state, live states not
// read or without their attribute names, a resource that is no state object
// — counts every denied part, as before: the first partial deny.
func (s *Session) wholeRead(c reqmap.Check, v policy.Verdict) (policy.RuleRef, bool) {
	first := policy.RuleRef{Rule: v.Rule, Line: v.Line}
	if len(v.PartialDenies) > 0 {
		first = v.PartialDenies[0]
	}
	if c.Resource.Kind != policy.KindEntity || c.Resource.Sub != "" || c.KindWide {
		return first, true
	}
	s.mu.Lock()
	var attrs []string
	held := false
	if s.cache.liveRead && s.cache.live != nil && s.cache.live.Attributes != nil {
		attrs, held = s.cache.live.Attributes[c.Resource.ID]
	}
	s.mu.Unlock()
	if !held {
		return first, true
	}
	parts := make([]string, 0, len(attrs)+1)
	parts = append(parts, "state")
	for _, a := range attrs {
		parts = append(parts, "attr/"+a)
	}
	for _, p := range parts {
		pv, ok := s.readVerdict(policy.KindEntity, c.Resource.ID, p)
		switch {
		case !ok:
			return first, true // a part hactl cannot address: as before
		case !pv.Allowed:
			return policy.RuleRef{Rule: pv.Rule, Line: pv.Line}, true
		}
	}
	return policy.RuleRef{}, false
}

// blueprintReader reads a blueprint file through the fetcher, as the
// session's own read; nil when the fetcher cannot read config files.
func (s *Session) blueprintReader(ctx context.Context) reqmap.BlueprintReader {
	cf, ok := s.fetcher.(ConfigFileFetcher)
	if !ok || s.fetcher == nil {
		return nil
	}
	return func(location string) ([]byte, error) {
		return cf.ConfigFile(own(ctx), location)
	}
}

func entityCheck(v policy.Verb, id string) (reqmap.Check, bool) {
	r, err := policy.ParseResource("entity:" + id)
	if err != nil {
		return reqmap.Check{}, false
	}
	return reqmap.Check{Verb: v, Resource: r}, true
}

// dedupeChecks keeps the first of each identical check, in order; a whole
// read of a duplicate makes the kept one whole (D-198).
func dedupeChecks(in []reqmap.Check) []reqmap.Check {
	seen := map[string]int{}
	out := in[:0]
	for _, c := range in {
		k := c.String()
		if i, ok := seen[k]; ok {
			out[i].Whole = out[i].Whole || c.Whole
			continue
		}
		seen[k] = len(out)
		out = append(out, c)
	}
	return out
}
