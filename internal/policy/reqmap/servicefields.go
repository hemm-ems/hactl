package reqmap

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/hemm-ems/hactl/internal/policy"
)

// ServiceClass is how hactl reads a service call's reach (D-184).
type ServiceClass string

const (
	// ServiceTargetOnly reaches its target and the entities its data names —
	// the generic reach every service has (D-59, D-73, D-106).
	ServiceTargetOnly ServiceClass = "target-only"
	// ServiceMapped also reaches what hand-mapped fields address by another
	// name (a group's object_id, a purge's domains).
	ServiceMapped ServiceClass = "mapped"
	// ServiceOpaque reaches what its call does not name at all (a sentence, a
	// shell command): it needs an allow by its exact name while the profile
	// denies anything (enforce.OpaqueService).
	ServiceOpaque ServiceClass = "opaque"
)

// opaqueServices are the services whose reach no field of the call states
// (D-184): an exact name, or `<domain>.*` for every service of a domain.
var opaqueServices = []string{
	"ai_task.evaluate",
	"ai_task.generate_data",
	"ai_task.generate_image",
	"assist_satellite.start_conversation",
	"conversation.process",
	"demo.randomize_device_tracker_data",
	"device_tracker.see",
	"file.read_file",
	"pyscript.*",
	"python_script.*",
	"rest_command.*",
	"shell_command.*",
}

// fieldMapper adds what a mapped service reaches through its own fields.
type fieldMapper func(cs *cascade, data map[string]any, live *liveIndex, reg *Registries)

// mappedServices are the services with hand-mapped fields (D-184), keyed by
// `<domain>.<service>` or `<domain>.*`.
var mappedServices = map[string]fieldMapper{
	"group.set":               objectIDField("group", "object_id"),
	"group.remove":            removesObjectID("group", "object_id"),
	"scene.create":            objectIDField("scene", "scene_id"),
	"recorder.purge":          everyLiveEntity,
	"recorder.purge_entities": purgeEntitiesFields,
	"recorder.get_statistics": statisticIDsRead,
	"shopping_list.*":         platformEntities("shopping_list", "todo"),
	// Removing what a call names needs delete as well (D-194).
	"scene.delete": deletesTarget("scene"),
	// What a reload or a restart removes (D-193).
	"scene.reload":             dropsRuntime,
	"homeassistant.reload_all": dropsRuntime,
	"homeassistant.restart":    dropsRuntime,
	"homeassistant.stop":       dropsRuntime,
	// What a call writes that no field names (D-191).
	"homeassistant.set_location":       writesFixed,
	"homeassistant.reload_core_config": writesFixed,
}

// runtimeDropped is, per service, the domains whose runtime-created entities
// — what group.set and scene.create make, which nothing stores — the call
// removes (D-193). Observed by TestOracleReloadsRemoveRuntimeEntities and
// TestOracleRestartRemovesRuntimeEntities on HA 2026.11.2: scene.reload and
// homeassistant.reload_all remove the scenes, a restart the scenes and the
// groups; group.reload and every other reload neither. homeassistant.stop is
// not observed (it ends the rig's Home Assistant): it is mapped as the first
// half of a restart, after which neither exists.
var runtimeDropped = map[string][]string{
	"scene.reload":             {"scene"},
	"homeassistant.reload_all": {"scene"},
	"homeassistant.restart":    {"group", "scene"},
	"homeassistant.stop":       {"group", "scene"},
}

// ReloadRemovesRuntime reports whether a service removes runtime-created
// entities (D-193).
func ReloadRemovesRuntime(name string) bool { return len(runtimeDropped[name]) > 0 }

// dropsRuntime: a reload or restart removes every runtime-created entity of
// the domains it drops, so each live entity of those domains needs delete —
// every one, a superset: which were created at runtime is not on the live
// state, and one from configuration comes back as it was.
func dropsRuntime(cs *cascade, _ map[string]any, live *liveIndex, _ *Registries) {
	for _, d := range runtimeDropped[cs.service] {
		for _, id := range live.byDomain[d] {
			cs.add(policy.Delete, policy.KindEntity, id, "")
		}
	}
}

// fixedEntities is, per service, the entities it writes although no field
// names them (D-191): homeassistant.set_location moves the home zone
// (TestOracleSetLocationWritesZoneHome). homeassistant.reload_core_config
// re-reads the home's location from configuration.yaml and moves the home
// zone to it — not observed (the rigs keep the location in storage, so a
// reload moves nothing there): mapped as the superset, from Home Assistant's
// code path (both end in the core-config update zone.home follows).
var fixedEntities = map[string][]string{
	"homeassistant.set_location":       {"zone.home"},
	"homeassistant.reload_core_config": {"zone.home"},
}

// FixedEntitiesOf is what a service writes that no field names (D-191).
func FixedEntitiesOf(name string) []string { return fixedEntities[name] }

func writesFixed(cs *cascade, _ map[string]any, _ *liveIndex, _ *Registries) {
	for _, id := range fixedEntities[cs.service] {
		cs.add(policy.Control, policy.KindEntity, id, "")
	}
}

// deletesTarget: a service that deletes the entities of domain its target
// reaches (scene.delete) needs delete on each, beside the control every
// reached entity needs (D-194).
func deletesTarget(domain string) fieldMapper {
	return func(cs *cascade, _ map[string]any, _ *liveIndex, _ *Registries) {
		for _, id := range cs.reached {
			if strings.HasPrefix(id, domain+".") {
				cs.add(policy.Delete, policy.KindEntity, id, "")
			}
		}
	}
}

// handMappedElsewhere are the services mapServiceCall maps by hand outside
// mappedServices: the script runs (D-84) and the homeassistant fan-out (R-2).
// They are `mapped` in the service manifest too.
var handMappedElsewhere = []string{
	"homeassistant.toggle", "homeassistant.turn_off", "homeassistant.turn_on",
	"script.<name>", "script.toggle", "script.turn_on",
}

// lookupService finds name in a table keyed by exact names and `<domain>.*`.
func lookupService[V any](table map[string]V, name string) (V, bool) {
	if v, ok := table[name]; ok {
		return v, true
	}
	domain, _, _ := strings.Cut(name, ".")
	v, ok := table[domain+".*"]
	return v, ok
}

var opaqueSet = func() map[string]bool {
	m := make(map[string]bool, len(opaqueServices))
	for _, s := range opaqueServices {
		m[s] = true
	}
	return m
}()

// IsOpaqueService reports whether `<domain>.<service>` is opaque (D-184).
func IsOpaqueService(name string) bool {
	_, ok := lookupService(opaqueSet, name)
	return ok
}

// ServiceClassOf is the class hactl gives a service (D-184). A per-script
// service (`script.<x>`, any x but the script domain's own services) is
// `script.<name>`.
func ServiceClassOf(name string) ServiceClass {
	if IsOpaqueService(name) {
		return ServiceOpaque
	}
	if _, ok := lookupService(mappedServices, name); ok {
		return ServiceMapped
	}
	if slices.Contains(handMappedElsewhere, name) {
		return ServiceMapped
	}
	return ServiceTargetOnly
}

// ServiceTable lists every service the code classifies other than
// target-only, with its class — the keys the service manifest must hold even
// when no test instance exposes them (`shell_command.*`).
func ServiceTable() map[string]ServiceClass {
	out := map[string]ServiceClass{}
	for _, s := range opaqueServices {
		out[s] = ServiceOpaque
	}
	for s := range mappedServices {
		out[s] = ServiceMapped
	}
	for _, s := range handMappedElsewhere {
		out[s] = ServiceMapped
	}
	return out
}

// objectIDField maps a field holding an object id to `<domain>.<id>`: the
// entity the call creates, rewrites or removes (group.set, group.remove,
// scene.create). Home Assistant requires a slug there, so the id is checked as
// written, lower-cased.
func objectIDField(domain, field string) fieldMapper {
	return func(cs *cascade, data map[string]any, _ *liveIndex, _ *Registries) {
		v, ok := data[field]
		if !ok || v == nil {
			return
		}
		id := strings.ToLower(strings.TrimSpace(fmt.Sprint(v)))
		if id == "" {
			return
		}
		cs.add(policy.Control, policy.KindEntity, domain+"."+id, "")
	}
}

// removesObjectID is objectIDField for a call that removes the entity: it
// needs delete as well (D-194).
func removesObjectID(domain, field string) fieldMapper {
	control := objectIDField(domain, field)
	return func(cs *cascade, data map[string]any, live *liveIndex, reg *Registries) {
		control(cs, data, live, reg)
		if id := strings.ToLower(strings.TrimSpace(fmt.Sprint(data[field]))); data[field] != nil && id != "" {
			cs.add(policy.Delete, policy.KindEntity, domain+"."+id, "")
		}
	}
}

// everyLiveEntity: recorder.purge deletes the history of every entity.
func everyLiveEntity(cs *cascade, _ map[string]any, live *liveIndex, _ *Registries) {
	for _, id := range live.all {
		cs.add(policy.Control, policy.KindEntity, id, "")
	}
}

// purgeEntitiesFields: recorder.purge_entities `domains` and `entity_globs`
// name entities by domain and by glob; each live entity they match has its
// history deleted. A glob matches as Home Assistant matches it (D-192,
// fnmatchCase) — each glob as written, and each comma-separated part of it
// as well (a superset); one Python would refuse matches every live entity.
func purgeEntitiesFields(cs *cascade, data map[string]any, live *liveIndex, _ *Registries) {
	matched := map[string]bool{}
	for _, d := range stringList(data["domains"]) {
		for _, id := range live.byDomain[strings.ToLower(strings.TrimSpace(d))] {
			matched[id] = true
		}
	}
	globs := append(anyStrings(data["entity_globs"]), stringList(data["entity_globs"])...)
	for _, g := range globs {
		for _, id := range live.all {
			if m, ok := fnmatchCase(g, id); m || !ok {
				matched[id] = true
			}
		}
	}
	for _, id := range sortedKeys(matched) {
		cs.add(policy.Control, policy.KindEntity, id, "")
	}
}

// statisticIDsRead: recorder.get_statistics returns the statistics of each
// id it is asked for — the history of the entity an id names — so each needs
// what reading it through `recorder/statistics_during_period` needs. An
// external statistic (`domain:name`) is no entity.
func statisticIDsRead(cs *cascade, data map[string]any, _ *liveIndex, _ *Registries) {
	for _, id := range stringList(data["statistic_ids"]) {
		id = strings.TrimSpace(id)
		if id == "" || strings.Contains(id, ":") || !strings.Contains(id, ".") {
			continue
		}
		cs.add(policy.Read, policy.KindEntity, strings.ToLower(id), "stats")
	}
}

// platformEntities maps an integration's services that act on its own
// entities, which the call never names (shopping_list.add_item changes the
// shopping list's todo entity): every registry entity of that platform, or
// every live entity of fallback's domain when the registries were not read.
func platformEntities(platform, fallback string) fieldMapper {
	return func(cs *cascade, _ map[string]any, live *liveIndex, reg *Registries) {
		if reg == nil {
			for _, id := range live.byDomain[fallback] {
				cs.add(policy.Control, policy.KindEntity, id, "")
			}
			return
		}
		for _, e := range reg.Entities {
			if e.Platform == platform {
				cs.add(policy.Control, policy.KindEntity, e.EntityID, "")
			}
		}
	}
}

// stringList reads a field Home Assistant takes as one string or a list of
// them (cv.ensure_list); a comma-separated string names each part.
func stringList(v any) []string {
	var out []string
	add := func(s string) {
		for part := range strings.SplitSeq(s, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	switch t := v.(type) {
	case string:
		add(t)
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				add(s)
			}
		}
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ServiceInventoryKey is the key a service has in the service inventory and
// manifest (D-184): `<domain>.<service>`, with every per-script service
// (`script.<x>`, x not one of the script domain's own services) collapsed to
// `script.<name>`.
func ServiceInventoryKey(domain, service string) string {
	if domain == "script" {
		switch service {
		case "reload", "toggle", "turn_on", "turn_off":
		default:
			return "script.<name>"
		}
	}
	return domain + "." + service
}
