package haapi

import "sort"

// ---------------------------------------------------------------------------
// Which service data will Home Assistant accept?
//
// Measured against a live instance 2026-07-31, POST /api/services/<d>/<s>:
//
//	{"entity_id":"input_boolean.x"}                     → 200
//	{"entity_id":"input_boolean.x","bogus_key_xyz":1}   → 400
//	{"target":{"entity_id":"input_boolean.x"}}          → 400
//	{"topic":…,"payload":…,"qos":0}                     → 200  (mqtt.publish)
//	{"topic":…,"payload":…,"publish_options":{"qos":0}} → 400  (same service)
//	{"entity_id":"not_an_entity_id"}                    → 400
//	{"entity_id":"input_boolean.nonexistent"}           → 200, body []
//	{"bogus_key_xyz":1} on script.<name>                → 200, the script ran
//
// Four rules fall out of that, and every one of them was a measurement rather
// than a reading of HA's source:
//
//  1. HA validates service data with PREVENT_EXTRA, so an undeclared key is a
//     400 — `target` included. The `target:` wrapper is script/automation YAML
//     syntax that HA's script engine flattens before it ever calls the service;
//     the REST endpoint takes the flattened form. It is a very plausible
//     payload for an agent to construct, which is finding #42.
//  2. A service's declared `fields` are a TREE, not a list. Seven services on
//     the reference instance group fields into UI sections (`{collapsed,
//     fields}`), and the section name is not a data key while its leaves are —
//     `mqtt.publish` takes `qos` and refuses `publish_options`. A rule reading
//     only the top level would have refused half of `light.turn_on`'s payloads.
//  3. The five target selectors are accepted only by a service that publishes
//     a `target` (they come from HA's entity-service schema, not from the
//     service). `homeassistant.check_config` answers 400 to an entity_id.
//  4. A service that publishes NEITHER fields NOR a target says nothing about
//     what it takes, and 89 of the reference instance's 434 do. `script.<name>`
//     is the load-bearing case: it accepts arbitrary keys as script variables.
//     Refusing there would be H-2's dishonesty pointing the other way — a
//     preview stricter than the confirmed run — so an unpublished schema
//     refuses nothing.
//
// ---------------------------------------------------------------------------

// TargetSelectorFields are the five keys Home Assistant's entity-service schema
// accepts on top of a service's own fields (helpers/target.py TargetSelection).
// All five were verified accepted on an entity service and `entity_id` refused
// on a service with no target.
var TargetSelectorFields = []string{"area_id", "device_id", "entity_id", "floor_id", "label_id"}

// EntityMatchSentinels are the two entity_id values that are not entity ids.
// `none` matches nothing; `all` is HA's ENTITY_MATCH_ALL and reaches every
// entity of the domain, which is the one genuinely wide blast radius a service
// call has (helpers/service.py `_resolve_entity_service_call_entities`).
const (
	EntityMatchAll  = "all"
	EntityMatchNone = "none"
)

// ServiceDescriptor is what GET /api/services publishes about one service.
// Its zero value is a legitimate answer — a service may document neither
// fields nor a target — so it declares no identity.
type ServiceDescriptor struct {
	Fields map[string]ServiceField `json:"fields"`
	Target map[string]any          `json:"target"`
}

// ServiceField is one entry of a service's `fields`. It is either a field or a
// section grouping further fields, and the discriminator is the presence of a
// nested `fields` object: on the reference instance every section carried
// exactly `{collapsed, fields}` or `{fields}` and no section also carried a
// `selector`, so the two are never ambiguous.
type ServiceField struct {
	Fields map[string]ServiceField `json:"fields"`
}

// PublishesSchema reports whether HA says anything at all about what this
// service accepts. When it does not, nothing may be refused on its behalf.
func (d *ServiceDescriptor) PublishesSchema() bool {
	return len(d.Fields) > 0 || d.Target != nil
}

// AcceptedFields is the sorted set of data keys Home Assistant will accept:
// every leaf of the declared field tree, plus the five target selectors when
// the service publishes a target. Empty when the service publishes no schema.
func (d *ServiceDescriptor) AcceptedFields() []string {
	if !d.PublishesSchema() {
		return nil
	}
	seen := map[string]bool{}
	var walk func(fields map[string]ServiceField)
	walk = func(fields map[string]ServiceField) {
		for name, f := range fields {
			if len(f.Fields) > 0 {
				// A section: its name is a UI grouping, its leaves are the keys.
				walk(f.Fields)
				continue
			}
			seen[name] = true
		}
	}
	walk(d.Fields)
	if d.Target != nil {
		for _, name := range TargetSelectorFields {
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// UnknownFields returns the keys of data that Home Assistant will refuse,
// sorted. It returns nothing when the service publishes no schema — see rule 4
// above; the caller must not turn that into a refusal.
func (d *ServiceDescriptor) UnknownFields(data map[string]any) []string {
	accepted := d.AcceptedFields()
	if len(accepted) == 0 {
		return nil
	}
	ok := make(map[string]bool, len(accepted))
	for _, name := range accepted {
		ok[name] = true
	}
	var unknown []string
	for key := range data {
		if !ok[key] {
			unknown = append(unknown, key)
		}
	}
	sort.Strings(unknown)
	return unknown
}

// TargetsAnything reports whether data carries any of the five selectors, and
// whether it is the ENTITY_MATCH_ALL sentinel.
//
// A service call with no selector at all reaches NOTHING: HA's
// `TargetSelection` is empty, `has_any_target` is false and the extraction
// returns before it looks at a single entity. Finding #44 read the silence the
// other way round — "a targetless call broadcasts to the domain" — and the
// preview owes the measured answer, not either guess.
func TargetsAnything(data map[string]any) (targeted, matchAll bool) {
	for _, name := range TargetSelectorFields {
		value, ok := data[name]
		if !ok {
			continue
		}
		if s, isString := value.(string); isString && s == EntityMatchNone {
			// `none` is HA's "match nothing" sentinel, not a target.
			continue
		}
		if list, isList := value.([]any); isList && len(list) == 0 {
			continue
		}
		targeted = true
		if s, isString := value.(string); isString && name == "entity_id" && s == EntityMatchAll {
			matchAll = true
		}
	}
	return targeted, matchAll
}

// MalformedEntityIDs returns the entity_id values in data that Home Assistant
// will refuse as ids, in the order given. `all` and `none` are HA's own
// sentinels and are not ids. A value that is neither a string nor a list of
// strings is left alone: that is a type error HA reports in its own words.
func MalformedEntityIDs(data map[string]any) []string {
	var bad []string
	check := func(id string) {
		if id == EntityMatchAll || id == EntityMatchNone {
			return
		}
		if !ValidEntityID(id) {
			bad = append(bad, id)
		}
	}
	switch value := data["entity_id"].(type) {
	case string:
		check(value)
	case []any:
		for _, item := range value {
			if id, ok := item.(string); ok {
				check(id)
			}
		}
	}
	return bad
}
