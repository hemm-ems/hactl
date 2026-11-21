package reqmap_test

import (
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// fieldLive is a live set with a group, a scene, locks and a shopping list.
var fieldLive = &reqmap.LiveStates{EntityIDs: []string{
	"group.secure", "scene.secure", "light.kitchen", "lock.front_door", "lock.back_door",
	"todo.shopping_list", "sensor.power",
}}

// fieldRegistries holds the shopping list's platform.
var fieldRegistries = &reqmap.Registries{Entities: []reqmap.RegistryEntity{
	{EntityID: "todo.shopping_list", UniqueID: "sl", Platform: "shopping_list"},
	{EntityID: "light.kitchen", UniqueID: "u-kitchen", Platform: "hue"},
}}

func fieldFacts() *reqmap.Facts {
	return &reqmap.Facts{Live: fieldLive, Registries: fieldRegistries}
}

// entryLive and entryRegistries: two config entries — ce-hue owns
// light.kitchen directly and light.porch and sensor.hue_bridge through the
// bridge device; ce-other owns lock.front_door.
var entryLive = &reqmap.LiveStates{EntityIDs: []string{"light.kitchen", "light.porch", "sensor.hue_bridge", "lock.front_door"}}

func entryFacts(reach ...string) *reqmap.Facts {
	return &reqmap.Facts{Live: entryLive, Reach: &reqmap.Reach{EntityIDs: reach}, Registries: &reqmap.Registries{
		Entities: []reqmap.RegistryEntity{
			{EntityID: "light.kitchen", ConfigEntryID: "ce-hue"},
			{EntityID: "light.porch", DeviceID: "bridge"},
			{EntityID: "sensor.hue_bridge", DeviceID: "bridge", ConfigEntryID: "ce-hue"},
			{EntityID: "lock.front_door", DeviceID: "lockdev", ConfigEntryID: "ce-other"},
		},
		Devices: []reqmap.RegistryDevice{{ID: "bridge", AreaID: "garden", ConfigEntries: []string{"ce-hue"}}, {ID: "lockdev", ConfigEntries: []string{"ce-other"}}},
	}}
}

// serviceFieldVectors — D-184: fields that address an entity by another name
// than its entity id (the policy hunt of 2026-11-19, F1 and F3).
var serviceFieldVectors = []vector{
	{
		name:  "group.set's object_id is the group it rewrites (F1)",
		req:   rest("POST", "/api/services/group/set", `{"object_id":"secure","entities":["light.kitchen"]}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:group.secure", "control entity:light.kitchen", "control service:group.set"},
	},
	{
		name:  "group.remove's object_id is the group it deletes, also when the call names nothing else (F1)",
		req:   rest("POST", "/api/services/group/remove", `{"object_id":"secure"}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:group.secure", "control service:group.remove", "delete entity:group.secure"},
	},
	{
		name:  "group.set creating a group that is not live yet is checked on the id it will have",
		req:   rest("POST", "/api/services/group/set", `{"object_id":"New_One"}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:group.new_one", "control service:group.set"},
	},
	{
		name:  "scene.create's scene_id is the scene it overwrites (F1)",
		req:   rest("POST", "/api/services/scene/create", `{"scene_id":"secure","entities":{"light.kitchen":"on"}}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:light.kitchen", "control entity:scene.secure", "control service:scene.create"},
	},
	{
		name:  "recorder.purge_entities' domains are every live entity of each domain (F3)",
		req:   rest("POST", "/api/services/recorder/purge_entities", `{"domains":["lock"],"keep_days":0}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:lock.back_door", "control entity:lock.front_door",
			"control service:recorder.purge_entities"},
	},
	{
		name:  "recorder.purge_entities' entity_globs are every live entity a glob matches (F3)",
		req:   rest("POST", "/api/services/recorder/purge_entities", `{"entity_globs":"lock.front*"}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:lock.front_door", "control service:recorder.purge_entities"},
	},
	{
		name:  "an unclosed [ is a literal [, as Python reads it (D-192): it matches no entity id",
		req:   rest("POST", "/api/services/recorder/purge_entities", `{"entity_globs":["lock.[front"]}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control service:recorder.purge_entities"},
	},
	{
		name:  "a negated set is Python's [!…], not path.Match's [^…] (D-192, B4)",
		req:   rest("POST", "/api/services/recorder/purge_entities", `{"entity_globs":["[!a]ock.front_door"]}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:lock.front_door", "control service:recorder.purge_entities"},
	},
	{
		name:  "scene.delete deletes the scene it targets: delete as well as control (D-194)",
		req:   rest("POST", "/api/services/scene/delete", `{"entity_id":"scene.secure"}`),
		facts: &reqmap.Facts{Live: fieldLive, Registries: fieldRegistries, Reach: &reqmap.Reach{EntityIDs: []string{"scene.secure"}}},
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:scene.secure", "control service:scene.delete", "delete entity:scene.secure"},
	},
	// D-199 (refining D-193): a reload or a restart removes the runtime
	// scenes and groups but needs no delete on them — the service check
	// alone, a stated limit.
	{
		name:  "scene.reload: the service check alone (D-199)",
		req:   rest("POST", "/api/services/scene/reload", `{}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control service:scene.reload"},
	},
	{
		name:  "homeassistant.restart: the service check alone (D-199)",
		req:   rest("POST", "/api/services/homeassistant/restart", `{}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control service:homeassistant.restart"},
	},
	{
		name:  "homeassistant.reload_all: the service check alone (D-199)",
		req:   rest("POST", "/api/services/homeassistant/reload_all", `{}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control service:homeassistant.reload_all"},
	},
	{
		name:  "homeassistant.stop: the service check alone (D-199)",
		req:   rest("POST", "/api/services/homeassistant/stop", `{}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control service:homeassistant.stop"},
	},
	// D-200: reload_config_entry resets every entity of the entries it
	// reloads.
	{
		name:  "reload_config_entry by entry_id: control on every entity of the entry, also through its devices (D-200)",
		req:   rest("POST", "/api/services/homeassistant/reload_config_entry", `{"entry_id":"ce-hue"}`),
		facts: entryFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:light.kitchen", "control entity:light.porch", "control entity:sensor.hue_bridge", "control service:homeassistant.reload_config_entry"},
	},
	{
		name:  "reload_config_entry by an entity: every entity of its entry (D-200)",
		req:   rest("POST", "/api/services/homeassistant/reload_config_entry", `{"entity_id":"light.kitchen"}`),
		facts: entryFacts("light.kitchen"),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:light.kitchen", "control entity:light.porch", "control entity:sensor.hue_bridge", "control service:homeassistant.reload_config_entry"},
	},
	{
		name:  "reload_config_entry by an area: every entity of the entries of its devices (D-200)",
		req:   rest("POST", "/api/services/homeassistant/reload_config_entry", `{"area_id":"garden"}`),
		facts: entryFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:light.kitchen", "control entity:light.porch", "control entity:sensor.hue_bridge", "control service:homeassistant.reload_config_entry"},
	},
	{
		name:  "reload_config_entry without the registries is refused (D-200)",
		req:   rest("POST", "/api/services/homeassistant/reload_config_entry", `{"entry_id":"ce-hue"}`),
		facts: &reqmap.Facts{Live: entryLive},
		disp:  reqmap.Unmappable, row: svcRow, refused: "fail-closed: config entry reach unknown",
	},
	{
		name:  "group.reload keeps the groups group.set made: the service check alone (D-193)",
		req:   rest("POST", "/api/services/group/reload", `{}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control service:group.reload"},
	},
	{
		name:  "homeassistant.reload_core_config may move zone.home to configuration.yaml's location (D-191)",
		req:   rest("POST", "/api/services/homeassistant/reload_core_config", `{}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:zone.home", "control service:homeassistant.reload_core_config"},
	},
	{
		name:  "homeassistant.set_location moves zone.home, which no field names (D-191)",
		req:   rest("POST", "/api/services/homeassistant/set_location", `{"latitude":1.5,"longitude":2.5}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:zone.home", "control service:homeassistant.set_location"},
	},
	{
		name:  "recorder.purge, even with no data at all, deletes every entity's history",
		req:   rest("POST", "/api/services/recorder/purge", `{}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:group.secure", "control entity:light.kitchen", "control entity:lock.back_door",
			"control entity:lock.front_door", "control entity:scene.secure", "control entity:sensor.power",
			"control entity:todo.shopping_list", "control service:recorder.purge"},
	},
	{
		name:  "recorder.purge without the live set is refused, never sent unchecked",
		req:   rest("POST", "/api/services/recorder/purge", `{}`),
		facts: &reqmap.Facts{},
		disp:  reqmap.Unmappable, row: svcRow,
		refused: "fail-closed: reach unknown",
	},
	{
		name:  "recorder.get_statistics reads each entity it is asked for; an external statistic is no entity",
		req:   rest("POST", "/api/services/recorder/get_statistics?return_response", `{"statistic_ids":["sensor.power","sensor:external"],"period":"hour","start_time":"2026-11-01T00:00:00"}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"read entity:sensor.power/stats", "control entity:sensor.power", "control service:recorder.get_statistics"},
	},
	{
		name:  "shopping_list services act on the shopping list's own entity, which they never name",
		req:   rest("POST", "/api/services/shopping_list/add_item", `{"name":"milk"}`),
		facts: fieldFacts(),
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:todo.shopping_list", "control service:shopping_list.add_item"},
	},
	{
		name:  "without the registries a shopping_list service reaches every todo entity",
		req:   rest("POST", "/api/services/shopping_list/clear_completed_items", `{}`),
		facts: &reqmap.Facts{Live: fieldLive},
		disp:  reqmap.Write, row: svcRow,
		checks: []string{"control entity:todo.shopping_list", "control service:shopping_list.clear_completed_items"},
	},
}

// TestServiceFieldsByOtherNames — D-184's hand-mapped fields.
func TestServiceFieldsByOtherNames(t *testing.T) {
	for _, v := range serviceFieldVectors {
		checkVector(t, v)
	}
}

// TestServiceClasses — the classes D-184 gives: an opaque service by exact
// name and by domain, a mapped one, the per-script service, and the generic
// default.
func TestServiceClasses(t *testing.T) {
	for name, want := range map[string]reqmap.ServiceClass{
		"conversation.process":         reqmap.ServiceOpaque,
		"shell_command.open_garage":    reqmap.ServiceOpaque,
		"python_script.anything":       reqmap.ServiceOpaque,
		"group.set":                    reqmap.ServiceMapped,
		"shopping_list.add_item":       reqmap.ServiceMapped,
		"homeassistant.turn_on":        reqmap.ServiceMapped,
		"light.turn_on":                reqmap.ServiceTargetOnly,
		"conversation.reload":          reqmap.ServiceTargetOnly,
		"zwave_js.clear_lock_usercode": reqmap.ServiceTargetOnly,
	} {
		if got := reqmap.ServiceClassOf(name); got != want {
			t.Errorf("ServiceClassOf(%q) = %s, want %s", name, got, want)
		}
	}
}
