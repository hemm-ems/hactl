package enforce

import (
	"context"
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// targeting is whether something that targets a container can switch an
// entity of one domain through it (D-186 as refined by D-212), and what: by
// names the targeter, "" when hactl could not ask.
type targeting struct {
	targeted bool
	by       string
}

// via renders the targeter after a refusal's rule.
func (t targeting) via(c container) string {
	if t.by == "" {
		return ""
	}
	return "because " + t.by + " targets " + c.String()
}

// targeter is one automation or script that names a container: by its run
// (stored config address) and its name.
type targeter struct {
	run  reqmap.Run
	name string
}

// askTargeted decides whether anything that targets c can switch an entity
// of domain (D-212). Home Assistant's search/related and the config grep
// (D-186) find the automations and scripts that name c; each one's stored
// config is read, and c counts as targeted for domain when an action whose
// target or data names c can affect domain (actionAffects). An answer hactl
// cannot get, a config it cannot read, a templated target or name, and a
// grep hit — under any key — outside every automation and script (a template
// entity) count for every domain.
func (s *Session) askTargeted(ctx context.Context, c container, domain string) targeting {
	tf, ok := s.fetcher.(TargetFetcher)
	if !ok || s.fetcher == nil {
		return targeting{targeted: true} // cannot ask: counts as targeting
	}
	s.gather(ctx, reqmap.Request{}, needs{live: true, registries: true, services: true})
	rel, err := tf.Related(own(ctx), c.kind, c.id)
	if err != nil {
		return targeting{targeted: true}
	}
	var found []targeter
	for _, id := range rel["automation"] {
		found = append(found, s.automationTargeter(id))
	}
	for _, id := range rel["script"] {
		found = append(found, s.scriptTargeter(id))
	}
	res, err := tf.ConfigGrep(own(ctx), c.id)
	switch {
	case errors.Is(err, ErrNoCompanion):
	case err != nil:
		return targeting{targeted: true}
	default:
		if res.Truncated {
			return targeting{targeted: true}
		}
		// Every hit counts, under any key: a `variables:` value a templated
		// target reads (`lab: x` … `label_id: "{{ lab }}"`) names c outside
		// every target key (found by the rehunt, TestE2EPolicyRehuntLabelTargetForms).
		for _, h := range res.Hits {
			owners, placed := s.grepOwners(ctx, h)
			if !placed {
				return targeting{targeted: true} // a template entity, or a hit hactl cannot place
			}
			found = append(found, owners...)
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].name < found[j].name })
	found = slices.CompactFunc(found, func(a, b targeter) bool { return a.name == b.name })
	for _, t := range found {
		if s.configTargets(ctx, t.run, c, domain) {
			return targeting{targeted: true, by: t.name}
		}
	}
	return targeting{}
}

// automationTargeter is an automation entity as a run: its config id, else
// its entity id (D-78).
func (s *Session) automationTargeter(entityID string) targeter {
	s.mu.Lock()
	defer s.mu.Unlock()
	cid := ""
	if s.cache.liveRead && s.cache.live != nil {
		cid = s.cache.live.AutomationIDs[entityID]
	}
	if cid == "" {
		cid = entityID
	}
	return targeter{run: reqmap.Run{Kind: "automation", ID: cid, EntityID: entityID}, name: entityID}
}

// scriptTargeter is a script entity as a run: its config key, the registry
// unique_id (D-84); "" when the registries do not hold it.
func (s *Session) scriptTargeter(entityID string) targeter {
	s.mu.Lock()
	defer s.mu.Unlock()
	uid := ""
	if s.cache.regRead && s.cache.registries != nil {
		for _, e := range s.cache.registries.Entities {
			if e.EntityID == entityID {
				uid = e.UniqueID
			}
		}
	}
	return targeter{run: reqmap.Run{Kind: "script", ID: uid, EntityID: entityID}, name: entityID}
}

// grepOwners places a config grep hit in the automations and scripts it lies
// in (reqmap.RefChangeOwners, D-77); placed is false for a hit in anything
// else — a template entity — or one hactl cannot place.
func (s *Session) grepOwners(ctx context.Context, h ConfigGrepHit) ([]targeter, bool) {
	cf, ok := s.fetcher.(ConfigFileFetcher)
	if !ok || h.Path == "" {
		return nil, false
	}
	content, err := cf.ConfigFile(own(ctx), h.Location)
	if err != nil {
		return nil, false
	}
	owners, err := reqmap.RefChangeOwners(h.Location, content, h.Path, "", "")
	if err != nil || len(owners) == 0 {
		return nil, false
	}
	var out []targeter
	for _, o := range owners {
		switch o.Kind {
		case policy.KindAutomation:
			out = append(out, s.automationByConfigID(o.ID))
		case policy.KindScript:
			out = append(out, s.scriptByKey(o.ID))
		default:
			return nil, false
		}
	}
	return out, true
}

func (s *Session) automationByConfigID(cid string) targeter {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := "automation:" + cid
	if s.cache.liveRead && s.cache.live != nil {
		for e, id := range s.cache.live.AutomationIDs {
			if id == cid && (name == "automation:"+cid || e < name) {
				name = e
			}
		}
	}
	return targeter{run: reqmap.Run{Kind: "automation", ID: cid}, name: name}
}

func (s *Session) scriptByKey(key string) targeter {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := "script." + key
	if s.cache.regRead && s.cache.registries != nil {
		for _, e := range s.cache.registries.Entities {
			if e.UniqueID == key && strings.HasPrefix(e.EntityID, "script.") {
				name = e.EntityID
			}
		}
	}
	return targeter{run: reqmap.Run{Kind: "script", ID: key, EntityID: name}, name: name}
}

// configTargets reads a targeter's stored config and reports whether it can
// switch an entity of domain through c: a config it cannot read, a template
// naming c, a blueprint it cannot expand, and an action naming c that can
// affect domain all do.
func (s *Session) configTargets(ctx context.Context, r reqmap.Run, c container, domain string) bool {
	doc, err := s.storedConfig(ctx, r)
	if err != nil {
		return true
	}
	doc, unexpanded := reqmap.ExpandBlueprints(doc, r.Kind, s.blueprintReader(ctx))
	if len(unexpanded) > 0 || templateNames(doc, c.id) {
		return true
	}
	s.mu.Lock()
	var live *reqmap.LiveStates
	if s.cache.liveRead {
		live = s.cache.live
	}
	services := s.cache.services
	s.mu.Unlock()
	hit := false
	walkSteps(doc, func(service string, step map[string]any) {
		if !hit && stepNames(step, c.id) && actionAffects(service, domain, live, services) {
			hit = true
		}
	})
	return hit
}

// templateNames reports whether a template string anywhere in v names id.
func templateNames(v any, id string) bool {
	switch t := v.(type) {
	case map[string]any:
		for k, x := range t {
			if templateNames(k, id) || templateNames(x, id) {
				return true
			}
		}
	case []any:
		for _, x := range t {
			if templateNames(x, id) {
				return true
			}
		}
	case string:
		return isTemplate(t) && strings.Contains(t, id)
	}
	return false
}

// walkSteps visits every action step of a config: a mapping with a string
// `action` (or the old `service`) — triggers and conditions have none.
func walkSteps(v any, visit func(service string, step map[string]any)) {
	switch t := v.(type) {
	case map[string]any:
		for _, k := range []string{"action", "service"} {
			if svc, ok := t[k].(string); ok {
				visit(svc, t)
				break
			}
		}
		for _, x := range t {
			walkSteps(x, visit)
		}
	case []any:
		for _, x := range t {
			walkSteps(x, visit)
		}
	}
}

// stepNames reports whether an action step's target selectors — at its top
// level, under `target` or under `data` — name id, as one value or in a list.
// A templated selector, or a templated `target`/`data` as a whole, may name
// any id: a variable can hold it (`label_id: "{{ lab }}"`).
func stepNames(step map[string]any, id string) bool {
	in := func(m map[string]any) bool {
		for k := range targetKeys {
			switch v := m[k].(type) {
			case string:
				if namesID(v, id) {
					return true
				}
			case []any:
				for _, x := range v {
					if s, ok := x.(string); ok && namesID(s, id) {
						return true
					}
				}
			}
		}
		return false
	}
	if in(step) {
		return true
	}
	for _, k := range []string{"target", "data"} {
		switch m := step[k].(type) {
		case map[string]any:
			if in(m) {
				return true
			}
		case string:
			if isTemplate(m) {
				return true
			}
		}
	}
	return false
}

func isTemplate(v string) bool { return strings.Contains(v, "{{") || strings.Contains(v, "{%") }

// namesID reports whether a selector value names id: the value itself, or
// one of its comma-separated parts.
func namesID(v, id string) bool {
	if isTemplate(v) {
		return true
	}
	for p := range strings.SplitSeq(v, ",") {
		if strings.TrimSpace(p) == id {
			return true
		}
	}
	return false
}

// actionAffects reports whether an action of service can switch an entity of
// domain (D-212): a service of that domain; homeassistant.turn_on|turn_off|
// toggle when the domain has the service (D-211; an unread service list:
// yes); any other service of a domain that holds no live entity (an
// integration's own, homeassistant.*); a templated service name. An unread
// live set: yes.
func actionAffects(service, domain string, live *reqmap.LiveStates, services map[string]map[string]bool) bool {
	if strings.Contains(service, "{{") || strings.Contains(service, "{%") {
		return true
	}
	d, svc, ok := strings.Cut(strings.ToLower(strings.TrimSpace(service)), ".")
	if !ok {
		return true
	}
	if d == domain {
		return true
	}
	if d == "homeassistant" && (svc == "turn_on" || svc == "turn_off" || svc == "toggle") {
		return services == nil || services[domain][svc]
	}
	if live == nil {
		return true
	}
	return !slices.ContainsFunc(live.EntityIDs, func(e string) bool { return strings.HasPrefix(e, d+".") })
}
