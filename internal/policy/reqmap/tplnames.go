package reqmap

// The names a Home Assistant template can use, classified (D-188). Recorded
// from Home Assistant 2026.11.2's TemplateEnvironment (its globals, filters
// and tests, unlimited); TestOracleTemplateNamesExist proves each name below
// is one the rig's Home Assistant defines under that kind, and
// TestOracleSafeTemplateConstructsReadNoState that a template made only of the
// safe ones reads no state.
//
// A name is safe when what it computes depends only on its arguments — a
// literal, a variable, the current time — and never on what Home Assistant
// holds: no state, attribute, registry entry, config entry or repair issue.
// A name in neither list (a custom template, a newer Home Assistant's
// function) is unsafe: fail closed.

var safeTemplateGlobals = names(
	"acos", "apply", "as_datetime", "as_function", "as_local", "as_timedelta", "as_timestamp",
	"asin", "atan", "atan2", "average", "bitwise_and", "bitwise_or", "bitwise_xor", "bool",
	"clamp", "combine", "cos", "cycler", "dict", "difference", "e", "flatten", "float", "iif",
	"int", "intersect", "is_number", "joiner", "lipsum", "log", "max", "md5", "median",
	"merge_response", "min", "namespace", "now", "pack", "pi", "range", "relative_time",
	"remap", "set", "sha1", "sha256", "sha512", "shuffle", "sin", "slugify", "sqrt",
	"statistical_mode", "strptime", "symmetric_difference", "tan", "tau", "time_since",
	"time_until", "timedelta", "timedelta_string", "today_at", "tuple", "typeof", "union",
	"unpack", "urlencode", "utcnow", "version", "wrap", "zip",
)

var unsafeTemplateGlobals = names(
	"area_devices", "area_entities", "area_id", "area_name", "areas", "closest",
	"config_entry_attr", "config_entry_id", "device_attr", "device_entities", "device_id",
	"device_name", "distance", "entity_name", "expand", "floor_areas", "floor_entities",
	"floor_id", "floor_name", "floors", "has_value", "integration_entities", "is_device_attr",
	"is_hidden_entity", "is_state", "is_state_attr", "issue", "issues", "label_areas",
	"label_description", "label_devices", "label_entities", "label_id", "label_name", "labels",
	"state_attr", "state_attr_translated", "state_translated", "states",
)

var safeTemplateFilters = names(
	"abs", "acos", "add", "apply", "as_datetime", "as_function", "as_local", "as_timedelta",
	"as_timestamp", "asin", "atan", "atan2", "attr", "average", "base64_decode",
	"base64_encode", "batch", "bitwise_and", "bitwise_or", "bitwise_xor", "bool", "capitalize",
	"center", "clamp", "combine", "contains", "cos", "count", "d", "default", "dictsort",
	"difference", "e", "escape", "filesizeformat", "first", "flatten", "float", "forceescape",
	"format", "from_hex", "from_json", "groupby", "iif", "indent", "int", "intersect",
	"is_defined", "is_number", "items", "join", "last", "length", "list", "log", "lower", "map",
	"max", "md5", "median", "min", "multiply", "ord", "ordinal", "pack", "pprint", "random",
	"regex_findall", "regex_findall_index", "regex_match", "regex_replace", "regex_search",
	"reject", "rejectattr", "relative_time", "remap", "replace", "reverse", "round", "safe",
	"select", "selectattr", "sha1", "sha256", "sha512", "shuffle", "sin", "slice", "slugify",
	"sort", "sqrt", "statistical_mode", "string", "striptags", "sum", "symmetric_difference",
	"tan", "time_since", "time_until", "timedelta_string", "timestamp_custom",
	"timestamp_local", "timestamp_utc", "title", "to_json", "today_at", "tojson", "trim",
	"truncate", "typeof", "union", "unique", "unpack", "upper", "urlencode", "urlize",
	"version", "wordcount", "wordwrap", "wrap", "xmlattr",
)

var unsafeTemplateFilters = names(
	"area_devices", "area_entities", "area_id", "area_name", "closest", "config_entry_attr",
	"config_entry_id", "device_attr", "device_entities", "device_id", "device_name",
	"entity_name", "expand", "floor_areas", "floor_entities", "floor_id", "floor_name",
	"has_value", "integration_entities", "issue", "label_areas", "label_description",
	"label_devices", "label_entities", "label_id", "label_name", "labels", "state_attr",
	"state_attr_translated", "state_translated", "states",
)

var safeTemplateTests = names(
	"!=", "<", "<=", "==", ">", ">=", "apply", "boolean", "callable", "contains", "datetime",
	"defined", "divisibleby", "eq", "equalto", "escaped", "even", "false", "filter", "float",
	"ge", "greaterthan", "gt", "in", "integer", "is_number", "iterable", "le", "lessthan",
	"list", "lower", "lt", "mapping", "match", "ne", "none", "number", "odd", "sameas",
	"search", "sequence", "set", "string", "string_like", "test", "true", "tuple", "undefined",
	"upper",
)

var unsafeTemplateTests = names(
	"has_value", "is_device_attr", "is_hidden_entity", "is_state", "is_state_attr",
)

func names(ns ...string) map[string]bool {
	m := make(map[string]bool, len(ns))
	for _, n := range ns {
		m[n] = true
	}
	return m
}

// TemplateNameTable is every template name hactl classifies, by kind
// ("global", "filter", "test"), mapped to whether it is safe.
func TemplateNameTable() map[string]map[string]bool {
	out := map[string]map[string]bool{"global": {}, "filter": {}, "test": {}}
	for kind, pair := range map[string][2]map[string]bool{
		"global": {safeTemplateGlobals, unsafeTemplateGlobals},
		"filter": {safeTemplateFilters, unsafeTemplateFilters},
		"test":   {safeTemplateTests, unsafeTemplateTests},
	} {
		for n := range pair[0] {
			out[kind][n] = true
		}
		for n := range pair[1] {
			out[kind][n] = false
		}
	}
	return out
}
