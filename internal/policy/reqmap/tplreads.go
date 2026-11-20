package reqmap

import (
	"encoding/json"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/hemm-ems/hactl/internal/policy"
)

// What a template reads, stated per construct (D-198, D-201; decided by Jan
// 2026-11-20). A construct whose reach hactl can name is checked as the reads
// it makes: `states('x')` reads entity:x/state, `state_attr('x', 'a')`
// entity:x/attr/a, `states.light` the whole of every live light, and so on. A
// construct whose reach it cannot name — a computed entity id, an area given
// by name, a lookup, an unknown name, filter, test or tag — is unknowable:
// it needs `read system:template` (template.go, D-190).

// TemplateRead is one read a candidate makes beyond its mentions: what one
// of its templates reads (D-198) or what a condition or trigger compares
// (D-202), with the path it was found at and the construct that reads.
type TemplateRead struct {
	Path, Construct string
	Check           Check
}

type tplRead struct {
	construct string
	check     Check
}

// entityIDShape is an entity id as Home Assistant's state machine can hold
// one (a superset of valid_entity_id): a string of any other shape reads
// nothing.
var entityIDShape = regexp.MustCompile(`^[a-z0-9_]+\.[a-z0-9_]+$`)

// tplReserved are the names Home Assistant hands a candidate's templates at
// run time. A candidate that rebinds one is refused (D-204).
var tplReserved = map[string]bool{"trigger": true, "this": true, "wait": true, "repeat": true, "context": true}

// RuleReservedRebind refuses a candidate that assigns one of the runtime
// names (D-204): hactl does not analyse which binding a template sees.
const RuleReservedRebind = "fail-closed: template rebinds a reserved name"

// trigFacts is what `trigger` (and `wait.trigger`) hands a candidate's
// templates (D-201, D-203).
type trigFacts struct {
	// unsafe names why every use of `trigger` is unknowable: an event trigger
	// on an event that carries other entities' data, a geo_location trigger.
	unsafe string
	// known reports that entities is every entity whose state a trigger of
	// the candidate hands over; false when a template trigger or a trigger
	// kind hactl does not know may hand over any.
	known    bool
	entities []string
	// zones are the zones of zone triggers (`trigger.zone`).
	zones []string
}

// noStateTriggers are the trigger kinds that hand `trigger` no entity state.
var noStateTriggers = names(
	"time", "time_pattern", "sun", "event", "mqtt", "webhook", "homeassistant", "tag",
	"calendar", "conversation", "persistent_notification",
)

// stateEventTypes are the events whose data carries what a profile can deny
// reading (D-203, exactly): another entity's state (state_changed,
// state_reported), a state's logbook message (logbook_entry), a service
// call's data — ids, codes, values (call_service), the home location
// (core_config_updated), old registry values (entity_registry_updated,
// device_registry_updated), and every event (`*`). An event trigger on one —
// or on a templated event type — hands `trigger.event` that.
var stateEventTypes = map[string]bool{
	"state_changed": true, "state_reported": true, "logbook_entry": true, "call_service": true,
	"core_config_updated": true, "entity_registry_updated": true, "device_registry_updated": true,
	"*": true,
}

// triggerKind is the kind a mapping declares as a trigger (`trigger:` or
// legacy `platform:`), "" when it declares none.
func triggerKind(m map[string]any) string {
	if k, ok := m["trigger"].(string); ok {
		return k
	}
	k, _ := m["platform"].(string)
	return k
}

// triggerFacts reads every trigger of a candidate, at any depth: its
// `triggers:` and its `wait_for_trigger:`s alike.
func triggerFacts(doc any, reg *Registries) trigFacts {
	b := trigBuilder{f: trigFacts{known: true}, seen: map[string]bool{}, reg: reg}
	b.walk(doc)
	return b.f
}

// trigBuilder gathers trigFacts over a candidate's triggers.
type trigBuilder struct {
	f    trigFacts
	seen map[string]bool
	reg  *Registries
}

func (b *trigBuilder) walk(v any) {
	switch t := v.(type) {
	case map[string]any:
		b.trigger(t)
		for _, k := range slices.Sorted(maps.Keys(t)) {
			b.walk(t[k])
		}
	case []any:
		for _, e := range t {
			b.walk(e)
		}
	}
}

// add records the entities a trigger hands over; ok false makes them unknown.
func (b *trigBuilder) add(ids []string, ok bool) {
	b.f.known = b.f.known && ok
	for _, id := range ids {
		if !b.seen[id] {
			b.seen[id] = true
			b.f.entities = append(b.f.entities, id)
		}
	}
}

// note keeps the least reason by text: one candidate always names the same
// one.
func (b *trigBuilder) note(r string) {
	if b.f.unsafe == "" || r < b.f.unsafe {
		b.f.unsafe = r
	}
}

// trigger reads one mapping, if it declares a trigger.
func (b *trigBuilder) trigger(t map[string]any) {
	switch kind := triggerKind(t); kind {
	case "":
	case "event":
		for _, et := range anyStrings(t["event_type"]) {
			if isTemplate(et) || stateEventTypes[strings.ToLower(strings.TrimSpace(et))] {
				b.note("trigger (an event trigger on " + et + ")")
			}
		}
	case "geo_location":
		b.note("trigger (a geo_location trigger)")
	case "state", "numeric_state", "zone":
		b.add(configEntityIDs(t["entity_id"], b.reg))
		if kind == "zone" {
			zs, ok := configEntityIDs(t["zone"], b.reg)
			b.f.known = b.f.known && ok
			b.f.zones = append(b.f.zones, zs...)
		}
	case "device":
		b.add(deviceEntities(t, b.reg))
	default:
		if !noStateTriggers[kind] {
			b.f.known = false
		}
	}
}

// configEntityIDs reads an `entity_id` of a trigger or condition as Home
// Assistant validates it (cv.entity_ids): one id, a comma-separated string or
// a list; each an id (lower-cased) or an entity registry uuid. ok is false
// when one is a template or not a string.
func configEntityIDs(v any, reg *Registries) ([]string, bool) {
	var raw []string
	switch t := v.(type) {
	case nil:
		return nil, true
	case string:
		raw = strings.Split(t, ",")
	case []any:
		for _, e := range t {
			s, ok := e.(string)
			if !ok {
				return nil, false
			}
			raw = append(raw, s)
		}
	default:
		return nil, false
	}
	var out []string
	for _, s := range raw {
		s = strings.TrimSpace(s)
		switch {
		case s == "":
		case isTemplate(s):
			return nil, false
		case entityByUUID(reg, s) != "":
			out = append(out, entityByUUID(reg, s))
		default:
			out = append(out, strings.ToLower(s))
		}
	}
	return out, true
}

// deviceEntities is the entity a device trigger or condition names — by uuid
// or id — or, when it names none, every entity of its device.
func deviceEntities(m map[string]any, reg *Registries) ([]string, bool) {
	if raw, ok := m["entity_id"]; ok {
		return configEntityIDs(raw, reg)
	}
	dev, _ := m["device_id"].(string)
	if reg == nil || dev == "" || isTemplate(dev) {
		return nil, false
	}
	var out []string
	for _, e := range reg.Entities {
		if e.DeviceID == dev {
			out = append(out, e.EntityID)
		}
	}
	return out, true
}

func entityByUUID(reg *Registries, s string) string {
	if reg == nil || s == "" {
		return ""
	}
	for _, e := range reg.Entities {
		if e.ID != "" && strings.EqualFold(e.ID, s) {
			return e.EntityID
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// The analysis.

// read records a read, each once; a whole read wins over a part.
func (a *tplAnalysis) read(construct string, c Check) {
	k := c.String()
	if i, ok := a.seenRead[k]; ok {
		if c.Whole && !a.reads[i].check.Whole {
			a.reads[i] = tplRead{construct: construct, check: c}
		}
		return
	}
	a.seenRead[k] = len(a.reads)
	a.reads = append(a.reads, tplRead{construct: construct, check: c})
}

// readEntity records `read entity:<id>[/<sub>]`. An id that is not one reads
// nothing; an attribute name the address cannot carry reads every attribute.
func (a *tplAnalysis) readEntity(construct, id, sub string, whole bool) {
	if !entityIDShape.MatchString(id) {
		return
	}
	c, err := check(policy.Read, policy.KindEntity, id, sub)
	if err != nil {
		if c, err = check(policy.Read, policy.KindEntity, id, "attr"); err != nil {
			return
		}
		whole = true
	}
	c.Whole = whole
	a.read(construct, c)
}

func (a *tplAnalysis) readKind(construct string, kind policy.Kind, id string) {
	if c, err := check(policy.Read, kind, id, ""); err == nil {
		a.read(construct, c)
	}
}

// global reports whether n means the global of that name: neither assigned
// by the template nor a variable of the candidate.
func (a *tplAnalysis) global(n string) bool { return !a.bound[n] && a.env.vars[n] == nil }

// runtime reports whether n is the candidate's own runtime name (`trigger`,
// `wait`): a candidate defines them, `tpl eval` does not.
func (a *tplAnalysis) runtime(n string) bool {
	return (n == "trigger" || n == "wait") && !a.bound[n] && a.env.vars[n] != nil
}

// jStep is one step of an attribute chain: `.a` / `['a']` (named), `[x]`
// (idx), or a call.
type jStep struct {
	attr  string
	named bool
	item  bool // named by a subscript, never a method
	idx   jNode
	call  *jArgs
}

// flatten reads an attribute chain down to its root name.
func flatten(n jNode) (string, []jStep, bool) {
	switch t := n.(type) {
	case jName:
		return t.name, nil, true
	case jGetattr:
		r, s, ok := flatten(t.obj)
		return r, append(s, jStep{attr: t.attr, named: true}), ok
	case jGetitem:
		r, s, ok := flatten(t.obj)
		if c, isConst := t.idx.(jConst); isConst {
			if str, isStr := c.v.(string); isStr {
				return r, append(s, jStep{attr: str, named: true, item: true}), ok
			}
		}
		return r, append(s, jStep{idx: t.idx}), ok
	case jCall:
		r, s, ok := flatten(t.fn)
		args := t.args
		return r, append(s, jStep{call: &args}), ok
	}
	return "", nil, false
}

func (a *tplAnalysis) walkSteps(steps []jStep) {
	for _, s := range steps {
		if s.idx != nil {
			a.node(s.idx)
		}
		if s.call != nil {
			a.nodes(s.call.all())
		}
	}
}

// chain analyses an attribute access, subscript or call from its outermost
// node.
func (a *tplAnalysis) chain(n jNode) {
	if root, steps, ok := flatten(n); ok {
		switch {
		case root == "states" && a.global(root):
			a.statesChain(steps)
			return
		case root == "trigger" && a.runtime(root):
			a.triggerChain(steps, "trigger")
			return
		case root == "wait" && a.runtime(root):
			switch {
			case len(steps) > 0 && steps[0].named && steps[0].attr == "trigger":
				a.triggerChain(steps[1:], "wait.trigger")
			case len(steps) > 0 && steps[0].named:
				a.walkSteps(steps) // wait.completed, wait.remaining
			default:
				a.triggerChain(nil, "wait")
				a.walkSteps(steps)
			}
			return
		}
	}
	switch t := n.(type) {
	case jGetattr:
		a.node(t.obj)
	case jGetitem:
		a.node(t.obj)
		a.node(t.idx)
	case jCall:
		if fn, ok := t.fn.(jName); ok && unsafeTemplateGlobals[fn.name] && a.global(fn.name) {
			a.reading(fn.name, t.args.pos, t.args, fn.name)
			return
		}
		a.node(t.fn)
		a.nodes(t.args.all())
	}
}

// wholeLive reads the whole of every given entity.
func (a *tplAnalysis) wholeLive(construct string, ids []string) {
	for _, id := range ids {
		a.readEntity(construct, strings.ToLower(id), "", true)
	}
}

// liveOf is every live entity of domain ("" = every one); ok is false when the
// live set was not read.
func (a *tplAnalysis) liveOf(domain string) ([]string, bool) {
	if a.env.li == nil {
		return nil, false
	}
	if domain == "" {
		return a.env.li.all, true
	}
	return a.env.li.byDomain[domain], true
}

// statesChain: `states` and what follows it.
func (a *tplAnalysis) statesChain(steps []jStep) {
	if len(steps) == 0 {
		a.wholeDomain("states", "")
		return
	}
	s0 := steps[0]
	if s0.call != nil {
		a.reading("states", s0.call.pos, *s0.call, "states")
		a.walkSteps(steps[1:])
		return
	}
	key, ok := s0.attr, s0.named
	if !ok {
		if v, fok := a.env.foldExpr(s0.idx); fok {
			key, ok = v.(string)
		}
		if !ok {
			a.node(s0.idx)
			a.wholeDomain("states[…]", "")
			a.walkSteps(steps[1:])
			return
		}
	}
	if strings.Contains(key, ".") {
		a.stateObject("states['"+strings.ReplaceAll(key, "'", `\'`)+"']", strings.ToLower(key), steps[1:])
		return
	}
	construct := "states." + key
	if len(steps) == 1 {
		a.wholeDomain(construct, key)
		return
	}
	s1 := steps[1]
	obj, ok := s1.attr, s1.named
	if !ok && s1.idx != nil {
		if v, fok := a.env.foldExpr(s1.idx); fok {
			obj, ok = v.(string)
		}
	}
	if !ok {
		a.wholeDomain(construct, key)
		a.walkSteps(steps[1:])
		return
	}
	a.stateObject(construct+"."+obj, strings.ToLower(key+"."+obj), steps[2:])
}

// wholeDomain reads the whole of every live entity of domain ("" = all).
func (a *tplAnalysis) wholeDomain(construct, domain string) {
	ids, ok := a.liveOf(domain)
	if !ok {
		a.add(construct + " (live entities unread)")
		return
	}
	a.wholeLive(construct, ids)
}

// stateObject: what the steps after one entity's state object read
// (D-198): its state, one attribute, every attribute, its existence, or the
// whole of it.
func (a *tplAnalysis) stateObject(construct, id string, rest []jStep) {
	defer a.walkSteps(rest)
	if len(rest) == 0 || !rest[0].named {
		a.readEntity(construct, id, "", true)
		return
	}
	switch rest[0].attr {
	case "state", "last_changed", "last_updated", "last_reported":
		a.readEntity(construct, id, "state", false)
	case "entity_id", "domain", "object_id":
		a.readEntity(construct, id, "", false)
	case "name":
		a.readEntity(construct, id, "attr/friendly_name", false)
	case "attributes":
		a.attributes(construct, id, rest[1:])
	default:
		a.readEntity(construct, id, "", true)
	}
}

// attributes: `.attributes` and what follows it — one attribute (`.a`,
// `['a']`, `.get('a')`) or every one.
func (a *tplAnalysis) attributes(construct, id string, rest []jStep) {
	called := len(rest) > 1 && rest[1].call != nil
	switch {
	case len(rest) == 0 || !rest[0].named:
		a.readEntity(construct, id, "attr", true)
	case called && !rest[0].item && rest[0].attr == "get":
		args := rest[1].call
		if len(args.pos) > 0 {
			if c, ok := args.pos[0].(jConst); ok {
				if s, ok := c.v.(string); ok {
					a.readEntity(construct, id, "attr/"+s, false)
					return
				}
			}
			if v, ok := a.env.foldExpr(args.pos[0]); ok {
				if s, ok := v.(string); ok {
					a.readEntity(construct, id, "attr/"+s, false)
					return
				}
			}
		}
		a.readEntity(construct, id, "attr", true)
	case rest[0].item || !called:
		a.readEntity(construct, id, "attr/"+rest[0].attr, false)
	default:
		a.readEntity(construct, id, "attr", true)
	}
}

// triggerChain: `trigger` (or `wait.trigger`) and what follows it (D-201).
func (a *tplAnalysis) triggerChain(steps []jStep, construct string) {
	t := a.env.trig
	if t.unsafe != "" {
		a.add(t.unsafe)
		a.walkSteps(steps)
		return
	}
	if len(steps) == 0 || !steps[0].named {
		if !t.known {
			a.add(construct + " (a trigger hactl cannot read)")
		} else {
			a.wholeLive(construct, t.entities)
			a.wholeLive(construct, t.zones)
		}
		a.walkSteps(steps)
		return
	}
	switch steps[0].attr {
	case "to_state", "from_state":
		c := construct + "." + steps[0].attr
		if !t.known {
			a.add(c + " (a trigger hactl cannot read)")
			a.walkSteps(steps[1:])
			return
		}
		for _, id := range t.entities {
			a.stateObject(c, id, steps[1:])
		}
		if len(t.entities) == 0 {
			a.walkSteps(steps[1:])
		}
	case "zone":
		if !t.known {
			a.add(construct + ".zone (a trigger hactl cannot read)")
		} else {
			a.wholeLive(construct+".zone", t.zones)
		}
		a.walkSteps(steps[1:])
	default:
		a.walkSteps(steps[1:])
	}
}

// reading is a call of a reading function — as a global, a filter or a test
// (pos holds the filtered or tested value first). It returns the entity ids
// a registry function resolves to, ok false when it resolves to none hactl
// can name.
func (a *tplAnalysis) reading(fn string, pos []jNode, args jArgs, label string) ([]string, bool) {
	if len(args.dyn) > 0 || len(pos) == 0 {
		a.nodes(pos)
		a.nodes(args.kwVal)
		a.nodes(args.dyn)
		a.add(label + " with computed arguments")
		return nil, false
	}
	switch fn {
	case "states", "is_state", "has_value", "state_translated":
		ids, ok := a.ids(pos[0])
		a.nodes(pos[1:])
		a.nodes(args.kwVal)
		if !ok {
			a.add(label + " with a computed entity id")
			return nil, false
		}
		sub, whole := "state", false
		if fn == "states" && (len(pos) > 1 || len(args.kwName) > 0) {
			sub, whole = "", true // with_unit, rounded: the unit and precision too
		}
		for _, id := range ids {
			a.readEntity(label, id, sub, whole)
		}
	case "state_attr", "is_state_attr", "state_attr_translated":
		ids, ok := a.ids(pos[0])
		a.nodes(pos[1:])
		a.nodes(args.kwVal)
		if !ok {
			a.add(label + " with a computed entity id")
			return nil, false
		}
		sub, whole := "attr", true
		if len(pos) > 1 && len(args.kwName) == 0 {
			if v, fok := a.env.foldExpr(pos[1]); fok {
				if s, isStr := v.(string); isStr {
					sub, whole = "attr/"+s, false
				}
			}
		}
		for _, id := range ids {
			a.readEntity(label, id, sub, whole)
		}
	case "expand":
		a.nodes(args.kwVal)
		var all []string
		known := true
		for _, p := range pos {
			ids, ok := a.ids(p)
			known = known && ok
			all = append(all, ids...)
		}
		if !known {
			a.add(label + " with a computed entity id")
			return nil, false
		}
		a.expand(label, all)
	case "area_entities", "floor_entities", "label_entities", "device_entities",
		"area_devices", "label_devices", "floor_areas", "label_areas":
		return a.registryLookup(fn, pos, args, label)
	default:
		a.nodes(pos)
		a.nodes(args.kwVal)
		a.add(label)
		return nil, false
	}
	return nil, false
}

// expand reads the whole of each entity and, recursively, of every member a
// group's live `entity_id` attribute lists; a zone expands to the persons in
// it, read as every live person.
func (a *tplAnalysis) expand(label string, ids []string) {
	var members map[string][]string
	if a.env.live != nil {
		members = a.env.live.Members
	}
	for _, id := range forwardedTo(ids, members) {
		a.readEntity(label, strings.ToLower(id), "", true)
		if strings.HasPrefix(id, "zone.") {
			a.wholeDomain(label+" (a zone's persons)", "person")
		}
	}
}

// registryLookup: area_entities, floor_entities, label_entities,
// device_entities, area_devices, label_devices, floor_areas, label_areas with
// a registry id that folds. Each entity, device or area resolved is read —
// a superset of Home Assistant's answer. A name, or an id the registries do
// not hold, is unknowable (Home Assistant resolves names hactl cannot).
func (a *tplAnalysis) registryLookup(fn string, pos []jNode, args jArgs, label string) ([]string, bool) {
	unknowable := func(why string) ([]string, bool) {
		a.add(label + " " + why)
		return nil, false
	}
	if len(pos) != 1 || len(args.kwName) > 0 {
		a.nodes(pos)
		a.nodes(args.kwVal)
		return unknowable("with computed arguments")
	}
	v, ok := a.env.foldExpr(pos[0])
	id, isStr := v.(string)
	if !ok || !isStr {
		a.node(pos[0])
		return unknowable("with a computed argument")
	}
	reg := a.env.reg
	if reg == nil {
		return unknowable("(registries unread)")
	}
	areas, devices, entities, why := registryScope(fn, id, reg)
	if why != "" {
		return unknowable(why)
	}
	switch fn {
	case "area_devices", "label_devices":
		for _, d := range reg.Devices {
			if slices.Contains(areas, d.AreaID) && d.AreaID != "" {
				devices = append(devices, d.ID)
			}
		}
		for _, d := range dedupe(devices) {
			a.readKind(label, policy.KindDevice, d)
		}
		return nil, false
	case "floor_areas", "label_areas":
		for _, r := range dedupe(areas) {
			a.readKind(label, policy.KindArea, r)
		}
		return nil, false
	}
	entities = dedupe(append(entities, entitiesIn(reg, areas, devices)...))
	for _, e := range entities {
		a.readEntity(label, strings.ToLower(e), "", false)
	}
	return entities, true
}

// registryScope is what a registry function's id names directly: the areas,
// devices and entities it starts from. why is set when the id resolves to
// nothing hactl can name.
func registryScope(fn, id string, reg *Registries) (areas, devices, entities []string, why string) {
	switch fn {
	case "area_entities", "area_devices":
		if !slices.ContainsFunc(reg.Areas, func(r RegistryArea) bool { return r.ID == id }) {
			return nil, nil, nil, "with a name or an unknown area"
		}
		areas = []string{id}
	case "floor_entities", "floor_areas":
		for _, r := range reg.Areas {
			if r.FloorID == id {
				areas = append(areas, r.ID)
			}
		}
		if len(areas) == 0 {
			return nil, nil, nil, "with a name or a floor without areas"
		}
	case "label_entities", "label_devices", "label_areas":
		for _, r := range reg.Areas {
			if slices.Contains(r.Labels, id) {
				areas = append(areas, r.ID)
			}
		}
		for _, d := range reg.Devices {
			if slices.Contains(d.Labels, id) {
				devices = append(devices, d.ID)
			}
		}
		for _, e := range reg.Entities {
			if slices.Contains(e.Labels, id) {
				entities = append(entities, e.EntityID)
			}
		}
		if len(areas)+len(devices)+len(entities) == 0 {
			return nil, nil, nil, "with a name or an unused label"
		}
	case "device_entities":
		devices = []string{id}
	}
	return areas, devices, entities, ""
}

// entitiesIn is every entity of the areas (or of a device in one) and of the
// devices: a superset of each registry function's answer.
func entitiesIn(reg *Registries, areas, devices []string) []string {
	inArea := map[string]bool{}
	for _, r := range areas {
		inArea[r] = true
	}
	for _, d := range reg.Devices {
		if d.AreaID != "" && inArea[d.AreaID] {
			devices = append(devices, d.ID)
		}
	}
	var out []string
	for _, e := range reg.Entities {
		if (e.AreaID != "" && inArea[e.AreaID]) || (e.DeviceID != "" && slices.Contains(devices, e.DeviceID)) {
			out = append(out, e.EntityID)
		}
	}
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

// passThroughFilters return (a part of) the entity ids they are given.
var passThroughFilters = names("list", "unique", "sort", "reverse", "first", "last")

// ids evaluates n to the entity ids it names, recording what evaluating it
// reads: a folded string or list, `trigger.entity_id`, a registry function,
// `states.<domain>` and `states`, lists, `+`, conditionals and the filters
// that pass ids through. ok is false when hactl cannot name them.
func (a *tplAnalysis) ids(n jNode) ([]string, bool) {
	if v, ok := a.env.foldExpr(n); ok {
		return idsOf(v)
	}
	switch t := n.(type) {
	case jName:
		if t.name == "states" && a.global(t.name) {
			a.wholeDomain("states", "")
			return a.liveOf("")
		}
	case jList:
		return a.idsUnion(t.items...)
	case jBin:
		if t.op == "+" {
			return a.idsUnion(t.l, t.r)
		}
	case jCond:
		a.node(t.test)
		return a.idsUnion(t.a, t.b)
	case jGetattr, jGetitem:
		if ids, ok, named := a.idsOfPath(n); named {
			return ids, ok
		}
	case jCall:
		if fn, ok := t.fn.(jName); ok && a.global(fn.name) && registryEntityFns[fn.name] {
			return a.reading(fn.name, t.args.pos, t.args, fn.name)
		}
	case jFilter:
		if ids, ok, named := a.idsOfFilter(t); named {
			return ids, ok
		}
	}
	a.node(n)
	return nil, false
}

// idsOfPath: `trigger.entity_id` (or `wait.trigger.entity_id`) when the
// triggers are known, and `states.<domain>`. named is false for any other
// path.
func (a *tplAnalysis) idsOfPath(n jNode) (ids []string, ok, named bool) {
	root, steps, flat := flatten(n)
	if !flat {
		return nil, false, false
	}
	if root == "wait" && a.runtime(root) && len(steps) > 0 && steps[0].named && steps[0].attr == "trigger" {
		root, steps = "trigger", steps[1:]
	}
	switch {
	case root == "trigger" && a.runtime(root) && len(steps) == 1 && steps[0].named && steps[0].attr == "entity_id":
		if a.env.trig.unsafe != "" || !a.env.trig.known {
			return nil, false, false
		}
		return a.env.trig.entities, true, true
	case root == "states" && a.global(root) && len(steps) == 1 && steps[0].named && !strings.Contains(steps[0].attr, "."):
		a.wholeDomain("states."+steps[0].attr, steps[0].attr)
		ids, ok = a.liveOf(steps[0].attr)
		return ids, ok, true
	}
	return nil, false, false
}

// idsOfFilter: a registry function as a filter, and the filters that pass
// ids through. named is false for any other filter.
func (a *tplAnalysis) idsOfFilter(t jFilter) (ids []string, ok, named bool) {
	switch {
	case registryEntityFns[t.name]:
		ids, ok = a.reading(t.name, append([]jNode{t.arg}, t.args.pos...), t.args, "filter "+t.name)
	case passThroughFilters[t.name] && len(t.args.all()) == 0:
		ids, ok = a.ids(t.arg)
	case t.name == "map" && len(t.args.pos) == 0 && len(t.args.dyn) == 0 && len(t.args.kwName) == 1 &&
		t.args.kwName[0] == "attribute" && isConstString(t.args.kwVal[0], "entity_id"):
		ids, ok = a.ids(t.arg)
	default:
		return nil, false, false
	}
	return ids, ok, true
}

var registryEntityFns = names("area_entities", "floor_entities", "label_entities", "device_entities")

func isConstString(n jNode, s string) bool {
	c, ok := n.(jConst)
	return ok && c.v == s
}

func (a *tplAnalysis) idsUnion(ns ...jNode) ([]string, bool) {
	var out []string
	all := true
	for _, n := range ns {
		ids, ok := a.ids(n)
		all = all && ok
		out = append(out, ids...)
	}
	return out, all
}

// idsOf reads a folded value as entity ids, lower-cased as Home Assistant's
// state machine looks them up. A string that is not an id names nothing.
func idsOf(v any) ([]string, bool) {
	switch t := v.(type) {
	case string:
		return []string{strings.ToLower(t)}, true
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok {
				out = append(out, strings.ToLower(s))
			}
		}
		return out, true
	case map[string]any:
		return nil, false
	}
	return nil, true
}

// nameArgReads: map('states'), select('is_state', 'on'), … over a list hactl
// can name read per element; over another list, or through
// selectattr/rejectattr (the attribute's value is computed), unknowable.
func (a *tplAnalysis) nameArgReads(f jFilter, name string, isFilter bool, pos int) {
	label := "test " + name
	if isFilter {
		label = "filter " + name
	}
	label += " (through " + f.name + ")"
	rest := f.args.pos[pos+1:]
	if f.name == "selectattr" || f.name == "rejectattr" {
		a.node(f.arg)
		a.nodes(f.args.all())
		a.add(label)
		return
	}
	ids, ok := a.ids(f.arg)
	if !ok {
		a.nodes(f.args.all())
		a.add(label + " over a computed list")
		return
	}
	kw := jArgs{kwName: f.args.kwName, kwVal: f.args.kwVal}
	for _, id := range ids {
		a.reading(name, append([]jNode{jConst{v: id}}, rest...), kw, label)
	}
	if len(ids) == 0 {
		a.nodes(f.args.all())
	}
}

// TemplateEvalChecks is what rendering src through `POST /api/template`
// (`tpl eval`) reads (D-198): the reads hactl can name, decided as reads
// (a whole one refused by a denied part), and nothing for a template that
// reads no state. ok is false when the template does not parse or uses a
// construct whose reach hactl cannot name: it then needs `read
// system:template`. live and reg may be nil: what needs them is then
// unknowable. No runtime name is defined — `trigger` there is an unknown
// name.
func TemplateEvalChecks(src string, live *LiveStates, reg *Registries) ([]Check, bool) {
	if !isTemplate(src) {
		return nil, true
	}
	tpl, err := parseJinja(src)
	if err != nil {
		return nil, false
	}
	env := &tplEnv{vars: map[string]*tplVar{}, live: live, reg: reg, trig: trigFacts{known: true}}
	if live != nil {
		env.li = newLiveIndex(live.EntityIDs)
	}
	a := env.analyse(tpl)
	if len(a.out) > 0 {
		return nil, false
	}
	out := make([]Check, 0, len(a.reads))
	for _, r := range a.reads {
		out = append(out, r.check)
	}
	return out, true
}

// mapTemplateEval maps `POST /api/template`: what its template reads
// (TemplateEvalChecks), or `read system:template` (D-143, D-190) when hactl
// cannot name it.
func mapTemplateEval(c *call) (mapping, error) {
	var body struct {
		Template *string `json:"template"`
	}
	if json.Unmarshal(c.req.Body, &body) == nil && body.Template != nil {
		f := c.fact()
		if checks, ok := TemplateEvalChecks(*body.Template, f.Live, f.Registries); ok {
			return mapping{checks: checks}, nil
		}
	}
	return readSystem("template")(c)
}
