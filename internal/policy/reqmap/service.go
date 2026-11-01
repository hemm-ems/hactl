package reqmap

import (
	"errors"
	"slices"
	"sort"
	"strings"

	"github.com/hemm-ems/hactl/internal/policy"
)

// targetKeys are the target selectors of a service call (D-59). They are read
// at the top level of the data — the form Home Assistant's REST API accepts —
// and, as a superset, under a `target` mapping too.
var targetKeys = []string{"entity_id", "device_id", "area_id", "floor_id", "label_id"}

// scriptStartServices are the services of the script domain that start the
// scripts in their target; every other script.<x> but turn_off and reload is
// the per-script service of the script whose config key is <x> (D-84).
var scriptStartServices = map[string]bool{"turn_on": true, "toggle": true}

var scriptNonRunServices = map[string]bool{"turn_off": true, "reload": true}

// homeassistantFanOut are the homeassistant services that call each domain's
// own service of the same name, so each domain in the reach needs it too
// (D-59 as refined, R-2).
var homeassistantFanOut = map[string]bool{"turn_on": true, "turn_off": true, "toggle": true}

// selectors collects the target selectors of service data. A selector given
// both at the top level and under `target` keeps both values.
func selectors(data map[string]any) map[string][]any {
	out := map[string][]any{}
	collect := func(m map[string]any) {
		for _, k := range targetKeys {
			v, ok := m[k]
			if !ok || v == nil {
				continue
			}
			switch t := v.(type) {
			case []any:
				out[k] = append(out[k], t...)
			default:
				out[k] = append(out[k], t)
			}
		}
	}
	collect(data)
	if t, ok := data["target"].(map[string]any); ok {
		collect(t)
	}
	return out
}

// isAll reports whether a value is Home Assistant's `all` (in any case).
func isAll(v any) bool {
	s, ok := v.(string)
	return ok && strings.EqualFold(strings.TrimSpace(s), "all")
}

// reachParams is the extract_from_target question for a set of selectors, or
// nil when only `all` (which is never sent, D-59) or nothing is selected.
func reachParams(sel map[string][]any) map[string]any {
	params := map[string]any{}
	for _, k := range targetKeys {
		var vals []any
		for _, v := range sel[k] {
			if k == "entity_id" && isAll(v) {
				continue
			}
			vals = append(vals, v)
		}
		if len(vals) > 0 {
			params[k] = vals
		}
	}
	if len(params) == 0 {
		return nil
	}
	params["expand_group"] = true
	return params
}

// ReachQuery returns the `extract_from_target` parameters the policy check
// asks Home Assistant for a service call (a REST POST /api/services/… request),
// with `expand_group: true` (D-59); nil when the call names no target, or only
// `entity_id: all`, which is never sent and is classified locally. A request
// that is not a service call, or whose body is not a JSON object, is an error.
func ReachQuery(req Request) (map[string]any, error) {
	if req.Transport != REST || upper(req.Method) != "POST" {
		return nil, errors.New("not a service call")
	}
	path, _, _ := strings.Cut(req.Path, "?")
	segs, err := checkWholePath(path)
	if err != nil {
		return nil, err
	}
	if len(segs) != 4 || segs[0] != "api" || segs[1] != "services" || segs[2] == "" || segs[3] == "" {
		return nil, errors.New("not a service call")
	}
	data, err := jsonObject(req.Body)
	if err != nil {
		return nil, err
	}
	return reachParams(selectors(data)), nil
}

// liveIndex is the live entity set, by lower-cased id.
type liveIndex struct {
	byLower  map[string]string
	byDomain map[string][]string
	all      []string
}

func newLiveIndex(ids []string) *liveIndex {
	li := &liveIndex{byLower: map[string]string{}, byDomain: map[string][]string{}}
	for _, id := range ids {
		li.byLower[strings.ToLower(id)] = id
		d, _, _ := strings.Cut(id, ".")
		li.byDomain[d] = append(li.byDomain[d], id)
		li.all = append(li.all, id)
	}
	return li
}

// names reports the live entity a string names, matching case-insensitively
// (Home Assistant lower-cases entity ids it validates). A comma-separated list
// names every entity in it.
func (li *liveIndex) names(s string) []string {
	var out []string
	for part := range strings.SplitSeq(s, ",") {
		if id, ok := li.byLower[strings.ToLower(strings.TrimSpace(part))]; ok {
			out = append(out, id)
		}
	}
	return out
}

// walkData visits every mapping key and every string value of data, at any
// depth (D-73).
func walkData(v any, visit func(string)) {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			visit(k)
			walkData(t[k], visit)
		}
	case []any:
		for _, e := range t {
			walkData(e, visit)
		}
	case string:
		visit(t)
	}
}

// mapServiceCall maps POST /api/services/<domain>/<service> (E-1, E-2, D-59,
// D-73):
//
//   - control service:<domain>.<service>, always;
//   - control entity:<e> for the reach: a service is one domain's entity
//     service only if its domain is not `homeassistant` and has live
//     entities; then the reach is Home Assistant's extract_from_target
//     answer restricted to that domain, otherwise the whole answer.
//     `entity_id: all` (any case) is every live entity of the domain, or of
//     every domain. The availability restriction D-59 observed is not
//     applied: an unavailable entity is checked too — a superset;
//   - control entity:<e> for every mapping key and string value of the data,
//     at any depth, that names a live entity;
//   - homeassistant.turn_on|turn_off|toggle: control service:<d>.<service>
//     for every domain d in the reach;
//   - every service that can start a script: control script:<id>/run for
//     every script it starts — <x> itself for script.<x>, the entity
//     registry unique_id of each script entity in the reach otherwise (the
//     config key, D-84); a script entity whose unique_id this invocation did
//     not read is checked as every script's run (kind-wide, an over-deny).
//
// No answer, or an error answer, to the reach question, and a call naming a
// target or data without the live set, is `fail-closed: reach unknown`.
func mapServiceCall(c *call) (mapping, error) {
	domain, service := c.vars["domain"], c.vars["service"]
	svc, err := check(policy.Control, policy.KindService, domain+"."+service, "")
	if err != nil {
		return mapping{}, err
	}
	data, err := jsonObject(c.req.Body)
	if err != nil {
		return mapping{}, err
	}
	cs := &cascade{out: []Check{svc}}
	cs.out = append(cs.out, perScriptRun(domain, service)...)
	sel := selectors(data)
	if len(sel) == 0 && len(data) == 0 {
		// No selector and no entity in the data: the service check alone.
		return mapping{checks: cs.out}, nil
	}

	f := c.fact()
	if f.Live == nil {
		return mapping{}, refuse("reach unknown", "the live entity set was not read in this invocation, so %s.%s's reach cannot be computed", domain, service)
	}
	live := newLiveIndex(f.Live.EntityIDs)
	ids, err := serviceReach(domain, service, sel, live, f.Reach)
	if err != nil {
		return mapping{}, err
	}
	for _, id := range ids {
		cs.add(policy.Control, policy.KindEntity, id, "")
	}
	walkData(data, func(s string) {
		for _, id := range live.names(s) {
			cs.add(policy.Control, policy.KindEntity, id, "")
		}
	})
	if domain == "homeassistant" && homeassistantFanOut[service] {
		for _, d := range domainsOf(ids) {
			cs.add(policy.Control, policy.KindService, d+"."+service, "")
		}
	}
	if (domain == "script" || domain == "homeassistant") && scriptStartServices[service] {
		cs.addScriptRuns(ids, f.Registries)
	}
	return mapping{checks: cs.out}, cs.err
}

// serviceReach is the entity set a service call acts on, sorted: `all`
// expanded locally, every other selector from Home Assistant's answer,
// restricted to the service's domain when it is one domain's entity service
// (D-59 as refined, R-1).
func serviceReach(domain, service string, sel map[string][]any, live *liveIndex, answer *Reach) ([]string, error) {
	entityService := domain != "homeassistant" && len(live.byDomain[domain]) > 0
	reach := map[string]bool{}
	pool := live.all
	if entityService {
		pool = live.byDomain[domain]
	}
	if slices.ContainsFunc(sel["entity_id"], isAll) {
		for _, id := range pool {
			reach[id] = true
		}
	}
	if reachParams(sel) != nil {
		switch {
		case answer == nil:
			return nil, refuse("reach unknown", "extract_from_target was not asked for %s.%s's target", domain, service)
		case answer.Err != nil:
			return nil, refuse("reach unknown", "extract_from_target answered with an error: %v", answer.Err)
		}
		for _, id := range answer.EntityIDs {
			if !entityService || strings.HasPrefix(id, domain+".") {
				reach[id] = true
			}
		}
	}
	ids := make([]string, 0, len(reach))
	for id := range reach {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// domainsOf returns the distinct domains of sorted entity ids, in order.
func domainsOf(ids []string) []string {
	var out []string
	for _, id := range ids {
		d, _, _ := strings.Cut(id, ".")
		if len(out) == 0 || out[len(out)-1] != d {
			out = append(out, d)
		}
	}
	return out
}

// addScriptRuns checks script:<unique_id>/run for every script entity in the
// reach; one whose unique_id this invocation did not read is checked as every
// script's run.
func (cs *cascade) addScriptRuns(ids []string, reg *Registries) {
	for _, id := range ids {
		if !strings.HasPrefix(id, "script.") {
			continue
		}
		uid := scriptUniqueID(reg, id)
		if uid == "" {
			cs.out = append(cs.out, kindWide(policy.Control, policy.KindScript, "run"))
			continue
		}
		cs.add(policy.Control, policy.KindScript, uid, "run")
	}
}

// perScriptRun is script.<x>'s own run check: the per-script service keeps
// the config key after a registry rename, so <x> is the address (D-84).
func perScriptRun(domain, service string) []Check {
	if domain != "script" || scriptStartServices[service] || scriptNonRunServices[service] {
		return nil
	}
	ch, err := check(policy.Control, policy.KindScript, service, "run")
	if err != nil {
		// service already parsed as part of service:<domain>.<service>,
		// whose charset is stricter than a script id's.
		return []Check{kindWide(policy.Control, policy.KindScript, "run")}
	}
	return []Check{ch}
}

// scriptUniqueID is a script entity's registry unique_id — its config key
// (D-84) — or "" when the registries were not read or do not hold it.
func scriptUniqueID(reg *Registries, entityID string) string {
	if reg == nil {
		return ""
	}
	for _, e := range reg.Entities {
		if e.EntityID == entityID {
			return e.UniqueID
		}
	}
	return ""
}
