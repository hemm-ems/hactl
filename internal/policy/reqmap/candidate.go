package reqmap

import (
	"fmt"
	"maps"
	"regexp"
	"sort"
	"strings"
)

// What a written automation, script or template would do (W-1, E-3; D-56 as
// refined by D-133, D-134, D-137). A candidate is the body hactl is about to
// write. Writing an automation that unlocks the door is unlocking the door
// later, so the candidate is read for:
//
//   - its actions: every mapping, at any depth, whose `action:` (or legacy
//     `service:`) names a service. Each is a service call, checked exactly as
//     `svc call` checks one (the caller maps it through the service-call row);
//   - its device actions (`device_id` + `domain` + `type`, no `action:`): the
//     entity they name — by its registry uuid, as Home Assistant offers it,
//     or by its entity_id; both validate (D-137) — needs `control`;
//   - every other live entity it mentions, as a whole token in any string —
//     a trigger, a condition, a state template — which needs `read` (D-133);
//   - what it cannot read off the candidate: a service name or a target that
//     is a template. Each is undecidable and refuses the write (fail-closed).
//
// The whole candidate is read, never only what changed (D-134).

// CandidateAction is one service call a candidate's action would make, with
// the data it would send: the action's `data` plus its `target` keys and any
// legacy top-level target key, as a `svc call` would send them.
type CandidateAction struct {
	Domain, Service string
	Data            map[string]any
	// Path locates the action in the candidate, for a refusal.
	Path string
}

// CandidateUse is everything a candidate would do or read.
type CandidateUse struct {
	Actions []CandidateAction
	// Switch is every entity a device action would switch.
	Switch []string
	// Read is every other live entity the candidate mentions. It never holds
	// an entity Switch holds.
	Read []string
	// Undecidable is what the candidate does that cannot be read off it, as
	// `fail-closed: …` rules with the path they were found at.
	Undecidable []CandidateUndecidable
}

// CandidateUndecidable is one part of a candidate whose effect hactl cannot
// compute.
type CandidateUndecidable struct {
	Path, Rule string
}

// Rules a candidate is refused with when its effect cannot be read off it.
const (
	RuleActionServiceTemplate  = "fail-closed: action service is a template"
	RuleActionTargetTemplate   = "fail-closed: action target is a template"
	RuleDeviceActionUnresolved = "fail-closed: device action target unresolved"
	RuleCandidateUnreadable    = "fail-closed: candidate does not parse"
)

// entityToken finds entity-id-shaped tokens in a string: `<domain>.<object>`
// bounded by anything that cannot be part of an id — the companion's refscan
// rule, so a template's `states('lock.front')` names lock.front and
// `lock.front_2` does not name lock.front.
var entityToken = regexp.MustCompile(`(?i)[a-z0-9_]+\.[a-z0-9_]+`)

func isTemplate(s string) bool { return strings.Contains(s, "{{") || strings.Contains(s, "{%") }

// CandidateUses reads a candidate. live is the live entity set; reg the
// registries (a device action's uuid is resolved through them, D-137). A nil
// reg makes a device action undecidable, never unchecked; a nil live finds no
// mention, which the caller must refuse rather than pass.
func CandidateUses(candidate any, live *LiveStates, reg *Registries) CandidateUse {
	w := &candWalker{live: live, reg: reg, read: map[string]bool{}, sw: map[string]bool{}}
	if live != nil {
		w.li = newLiveIndex(live.EntityIDs)
	}
	w.walk(candidate, "")
	var u CandidateUse
	u.Actions = w.actions
	u.Undecidable = w.undecidable
	for id := range w.sw {
		u.Switch = append(u.Switch, id)
	}
	for id := range w.read {
		if !w.sw[id] {
			u.Read = append(u.Read, id)
		}
	}
	sort.Strings(u.Switch)
	sort.Strings(u.Read)
	return u
}

type candWalker struct {
	live        *LiveStates
	li          *liveIndex
	reg         *Registries
	actions     []CandidateAction
	undecidable []CandidateUndecidable
	read, sw    map[string]bool
}

func (w *candWalker) fail(path, rule string) {
	w.undecidable = append(w.undecidable, CandidateUndecidable{Path: pathOrRoot(path), Rule: rule})
}

func pathOrRoot(p string) string {
	if p == "" {
		return "/"
	}
	return p
}

func (w *candWalker) walk(v any, path string) {
	switch t := v.(type) {
	case map[string]any:
		if w.action(t, path) || w.sceneShorthand(t, path) || w.deviceAction(t, path) {
			return
		}
		for _, k := range sortedMapKeys(t) {
			w.mention(k)
			w.walk(t[k], path+"/"+k)
		}
	case []any:
		for i, e := range t {
			w.walk(e, fmt.Sprintf("%s/%d", path, i))
		}
	case string:
		w.mention(t)
	}
}

// mention records every live entity a string names as a whole token, and an
// entity registry uuid it is exactly (a device trigger or condition names its
// entity that way).
func (w *candWalker) mention(s string) {
	if id := w.byUUID(s); id != "" {
		w.read[id] = true
		return
	}
	if w.li == nil {
		return
	}
	for _, tok := range entityToken.FindAllString(s, -1) {
		if id, ok := w.li.byLower[strings.ToLower(tok)]; ok {
			w.read[id] = true
		}
	}
}

func (w *candWalker) byUUID(s string) string {
	if w.reg == nil || s == "" {
		return ""
	}
	for _, e := range w.reg.Entities {
		if e.ID != "" && strings.EqualFold(e.ID, s) {
			return e.EntityID
		}
	}
	return ""
}

// action handles a mapping with `action:` (or legacy `service:`). It reports
// whether the mapping was one.
func (w *candWalker) action(m map[string]any, path string) bool {
	key, name := actionName(m)
	if key == "" {
		if _, ok := m["service_template"]; ok {
			w.fail(path+"/service_template", RuleActionServiceTemplate)
			return true
		}
		return false
	}
	if isTemplate(name) {
		w.fail(path+"/"+key, RuleActionServiceTemplate)
		return true
	}
	domain, service, ok := strings.Cut(name, ".")
	if !ok || domain == "" || service == "" || strings.ContainsAny(service, ". ") {
		// Not a service: `action:` and `service:` also appear as plain values
		// — an event's data (`event_data: {action: OPEN_GARAGE}`, the
		// call_service event's `service: turn_on`). Home Assistant refuses a
		// real action with such a name when it validates the candidate.
		return false
	}
	data, templated := callData(m)
	if templated {
		w.fail(path, RuleActionTargetTemplate)
		return true
	}
	// Literal entity ids inside the data's templates (a message reading a
	// sensor) are mentions: read, as everywhere else (D-133). The call's own
	// data check gives control on every string that IS an entity id (D-73).
	walkData(data, func(s string) {
		if isTemplate(s) {
			w.mention(s)
		}
	})
	w.actions = append(w.actions, CandidateAction{Domain: strings.ToLower(domain), Service: strings.ToLower(service), Data: data, Path: pathOrRoot(path)})
	return true
}

// actionName is the service an action mapping names, and the key it is
// named under; "" when the mapping names none.
func actionName(m map[string]any) (key, name string) {
	for _, k := range []string{"action", "service"} {
		if s, ok := m[k].(string); ok {
			return k, strings.TrimSpace(s)
		}
	}
	return "", ""
}

// callData is the data a `svc call` of the action would send: its `data`
// (and legacy `data_template`), its `target` keys and any legacy top-level
// target key. templated reports a target that is, or holds, a template —
// including a whole `data` or `target` given as one template string.
func callData(m map[string]any) (data map[string]any, templated bool) {
	data = map[string]any{}
	for _, dk := range []string{"data", "data_template", "target"} {
		switch d := m[dk].(type) {
		case map[string]any:
			maps.Copy(data, d)
		case string:
			templated = true // the whole of it is a template: its target may be anything
		}
	}
	for _, k := range targetKeys {
		if v, ok := m[k]; ok {
			data[k] = v
		}
	}
	for _, k := range targetKeys {
		if v, ok := data[k]; ok && holdsTemplate(v) {
			templated = true
		}
	}
	return data, templated
}

func holdsTemplate(v any) bool {
	found := false
	walkData(v, func(s string) {
		if isTemplate(s) {
			found = true
		}
	})
	return found
}

// sceneShorthand handles `scene: scene.x`, the action form of scene.turn_on.
func (w *candWalker) sceneShorthand(m map[string]any, path string) bool {
	s, ok := m["scene"].(string)
	if !ok {
		return false
	}
	if isTemplate(s) {
		w.fail(path+"/scene", RuleActionTargetTemplate)
		return true
	}
	w.actions = append(w.actions, CandidateAction{Domain: "scene", Service: "turn_on", Data: map[string]any{"entity_id": s}, Path: pathOrRoot(path)})
	return true
}

// deviceAction handles a device action: `device_id`, `domain` and `type`,
// with neither `trigger`/`platform` (a device trigger) nor `condition` (a
// device condition). Its entity (D-137) needs control; one without an entity
// switches the device, so every entity of the device does.
func (w *candWalker) deviceAction(m map[string]any, path string) bool {
	dev, hasDev := m["device_id"].(string)
	_, hasDomain := m["domain"].(string)
	_, hasType := m["type"].(string)
	if !hasDev || !hasDomain || !hasType {
		return false
	}
	for _, k := range []string{"trigger", "platform", "condition"} {
		if _, ok := m[k]; ok {
			return false
		}
	}
	if raw, ok := m["entity_id"]; ok {
		s, isStr := raw.(string)
		switch {
		case !isStr || isTemplate(s):
			w.fail(path+"/entity_id", RuleDeviceActionUnresolved)
		case w.byUUID(s) != "":
			w.sw[w.byUUID(s)] = true
		case w.li != nil && len(w.li.names(s)) == 1:
			w.sw[w.li.names(s)[0]] = true
		default:
			w.fail(path+"/entity_id", RuleDeviceActionUnresolved)
		}
		return true
	}
	if w.reg == nil {
		w.fail(path, RuleDeviceActionUnresolved)
		return true
	}
	n := 0
	for _, e := range w.reg.Entities {
		if e.DeviceID != "" && e.DeviceID == dev {
			w.sw[e.EntityID] = true
			n++
		}
	}
	if n == 0 {
		w.fail(path, RuleDeviceActionUnresolved)
	}
	return true
}

func sortedMapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
