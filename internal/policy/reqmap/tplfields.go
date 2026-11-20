package reqmap

import "strings"

// Which service data fields name entities, for a template hactl cannot fold
// (D-189). A template there could render to any entity, so it refuses the
// candidate; a template in any other field (a message, a brightness) is only
// read for the entities it mentions and the reads it makes (D-133, D-188).
//
// TestOracleTemplateFieldsAreDispositioned holds this to the rig's Home
// Assistant: every field whose selector is an entity, statistic, area,
// device, floor or label selector must name entities here, and every field
// whose selector is `object` (free-form data that may hold entity ids) must
// name entities here or be exempt below, with the reason.

// entityNamingFieldNames name entities (or what resolves to entities) in
// every service: the target keys, the fields Home Assistant's own services
// take entity ids in, and the object-id fields of the mapped services.
var entityNamingFieldNames = names(
	"entity_id", "device_id", "area_id", "floor_id", "label_id",
	"entities", "add_entities", "remove_entities", "snapshot_entities",
	"group_members", "media_player_entity_id", "media_player", "statistic_ids",
	"object_id", "scene_id", "domains", "entity_globs", "cleaning_area_id",
)

// exemptTemplateFields are the object-selector fields that name no entity,
// keyed `<domain>.<service>.<field>`, with the reason (an opaque service is
// D-184's class).
var exemptTemplateFields = map[string]string{
	"ai_task.evaluate.questions":            "an opaque service: it needs an allow by its exact name already",
	"ai_task.evaluate.state":                "an opaque service: it needs an allow by its exact name already",
	"ai_task.generate_data.structure":       "an opaque service: it needs an allow by its exact name already",
	"assist_satellite.ask_question.answers": "the answers offered to a person, not entities",
	"device_tracker.see.gps":                "an opaque service: it needs an allow by its exact name already",
	"light.toggle.hs_color":                 "a colour: numbers",
	"light.toggle.rgbw_color":               "a colour: numbers",
	"light.toggle.rgbww_color":              "a colour: numbers",
	"light.toggle.xy_color":                 "a colour: numbers",
	"light.turn_on.hs_color":                "a colour: numbers",
	"light.turn_on.rgbw_color":              "a colour: numbers",
	"light.turn_on.rgbww_color":             "a colour: numbers",
	"light.turn_on.xy_color":                "a colour: numbers",
	"notify.notify.data":                    "handed to the notify platform, which switches no entity",
	"notify.notify.target":                  "the notify platform's own recipients, not entities",
	"notify.persistent_notification.data":   "handed to the notification, which switches no entity",
	"recorder.get_statistics.units":         "unit conversions, not entities",
	"remote.delete_command.command":         "the remote's own command names",
	"remote.learn_command.command":          "the remote's own command names",
	"remote.send_command.command":           "the remote's own command names",
	"tts.cloud_say.options":                 "the speech engine's options",
	"tts.demo_say.options":                  "the speech engine's options",
	"tts.speak.options":                     "the speech engine's options",
	"vacuum.send_command.params":            "the vacuum's own command parameters",
}

// EntityNamingField reports whether field of service (`<domain>.<service>`)
// names entities: a template hactl cannot fold there refuses the candidate.
func EntityNamingField(service, field string) bool {
	return entityNamingFieldNames[strings.ToLower(field)]
}

// TemplateFieldExemption is why an object-selector field names no entity, ""
// when it is not exempt.
func TemplateFieldExemption(service, field string) string {
	return exemptTemplateFields[service+"."+field]
}
