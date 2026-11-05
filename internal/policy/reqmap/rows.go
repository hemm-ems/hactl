package reqmap

import (
	"sort"
	"strings"

	"github.com/hemm-ems/hactl/internal/policy"
)

// supervisorType is the websocket type of Home Assistant's generic Supervisor
// proxy, keyed on (method, endpoint) rather than on the type.
const supervisorType = "supervisor/api"

// row is one line of dev/surfaces/policy.manifest's request half.
type row struct {
	key       string
	transport Transport
	wsType    string   // WS rows
	method    string   // HTTP rows and supervisor rows
	tmpl      []string // HTTP and supervisor rows: path segments, `{name}` placeholders
	disp      Disposition
	mapFn     mapper
	why       string // Exempt rows: why no resource applies
}

// mutating is the row's own answer to "can this change anything".
func (r *row) mutating() bool { return r.disp == Write }

func wsRow(typ string, disp Disposition, fn mapper) *row {
	return &row{key: "ws " + typ, transport: WS, wsType: typ, disp: disp, mapFn: fn}
}

func supRow(method, endpoint string, why string) *row {
	return &row{
		key: "ws " + supervisorType + " " + method + " " + endpoint, transport: WS, wsType: supervisorType,
		method: method, tmpl: splitPath(endpoint), disp: Exempt, why: why,
	}
}

func restRow(method, tmpl string, disp Disposition, fn mapper) *row {
	return &row{key: "rest " + method + " " + tmpl, transport: REST, method: method, tmpl: splitPath(tmpl), disp: disp, mapFn: fn}
}

func compRow(method, route string, disp Disposition, fn mapper) *row {
	return &row{key: "companion " + method + " " + route, transport: Companion, method: method, tmpl: splitPath(route), disp: disp, mapFn: fn}
}

func exempt(r *row, why string) *row {
	r.disp, r.why = Exempt, why
	return r
}

// The row table. Keys are the manifest's, verbatim.
var (
	wsRows = indexWS(
		exempt(wsRow("auth", Exempt, nil), "the session handshake carries the token and no Home Assistant resource"),
		exempt(wsRow("extract_from_target", Exempt, nil), "the policy check's own oracle for a service call's reach: it reads the target's references and changes nothing, and it must never be gated by the call it serves"),

		wsRow("config/entity_registry/update", Write, mapEntityRegistryUpdate),
		wsRow("config/entity_registry/remove", Write, mapEntityRegistryRemove),
		wsRow("config/device_registry/update", Write, mapDeviceRegistryUpdate),
		wsRow("config/label_registry/create", Write, kindWideCreate("label")),
		wsRow("config/label_registry/delete", Write, mapLabelDelete),
		wsRow("config/area_registry/create", Write, kindWideCreate("area")),
		wsRow("config/area_registry/delete", Write, mapAreaDelete),
		wsRow("config/floor_registry/create", Write, kindWideCreate("floor")),
		wsRow("config/floor_registry/delete", Write, mapFloorDelete),
		wsRow("lovelace/config/save", Write, mapDashboardSave),
		wsRow("lovelace/dashboards/create", Write, mapDashboardCreate),
		wsRow("lovelace/dashboards/delete", Write, mapDashboardDelete),

		wsRow("trace/list", Read, readTraceList),
		wsRow("trace/get", Read, readTraceGet),
		wsRow("validate_config", Read, readSystem("check_config")),
		wsRow("system_log/list", Read, readSystem("logs")),
		wsRow("repairs/list_issues", Read, readSystem("logs")),
		wsRow("config/entity_registry/list", Read, filtered(FilterEntityRegistry)),
		wsRow("config/area_registry/list", Read, filtered(FilterAreas)),
		wsRow("config/label_registry/list", Read, filtered(FilterLabels)),
		wsRow("config/floor_registry/list", Read, filtered(FilterFloors)),
		exempt(wsRow("config/auth/list", Exempt, nil), "an auxiliary read: Home Assistant's users, read to render a user id as a name — nothing about an entity, device or config"),
		wsRow("config/device_registry/list", Read, filtered(FilterDeviceRegistry)),
		wsRow("lovelace/dashboards/list", Read, filtered(FilterDashboards)),
		wsRow("lovelace/config", Read, readDashboardConfig),
		exempt(wsRow("manifest/list", Exempt, nil), "an auxiliary read: the integrations' manifests — nothing about an entity, device or config"),
		exempt(wsRow("lovelace/resources", Exempt, nil), "an auxiliary read: the frontend's one global list of resource URLs — nothing about an entity, device, dashboard view or config"),
		wsRow("recorder/get_statistics_metadata", Read, readStatistics(FilterStatisticIDs)),
		wsRow("recorder/statistics_during_period", Read, readStatistics(FilterStatistics)),
		wsRow("recorder/list_statistic_ids", Read, filtered(FilterStatisticIDs)),
		wsRow("energy/get_prefs", Read, filtered(FilterEnergyPrefs)),
	)

	supervisorRows = []*row{
		supRow("GET", "/addons", "companion transport discovery — lists add-ons to find the companion"),
		supRow("GET", "/addons/{slug}/info", "companion transport discovery — the companion add-on's own ingress entry"),
		supRow("POST", "/ingress/session", "an Ingress auth session is transport plumbing — a mutating method that changes no Home Assistant resource"),
	}

	restRows = []*row{
		restRow("POST", "/api/services/{domain}/{service}", Write, mapServiceCall),
		restRow("DELETE", "/api/config/config_entries/entry/{entry_id}", Write, mapConfigEntryDelete),
		restRow("POST", "/api/config/config_entries/options/flow", Write, mapOptionsFlowStart),
		restRow("POST", "/api/config/config_entries/options/flow/{flow_id}", Write, mapOptionsFlowStep),
		restRow("DELETE", "/api/config/config_entries/options/flow/{flow_id}", Write, mapOptionsFlowStep),
		restRow("GET", "/api/config/config_entries/options/flow/{flow_id}", Write, mapOptionsFlowStep),
		restRow("POST", "/api/config/config_entries/flow", Write, mapConfigFlowStart),
		restRow("POST", "/api/config/config_entries/flow/{flow_id}", Write, mapConfigFlowStep),
		restRow("GET", "/api/config/config_entries/flow/{flow_id}", Write, mapConfigFlowStep),

		exempt(restRow("GET", "/api/config/config_entries/flow_handlers", Exempt, nil), "an auxiliary read: the integration catalog — the domains offering a config flow, nothing about an entity, device or config"),
		restRow("GET", "/api/config/config_entries/entry", Read, filtered(FilterConfigEntries)),
		restRow("GET", "/api/diagnostics/config_entry/{entry_id}", Read, readDiagnostics),
		restRow("GET", "/api/states", Read, filtered(FilterStates)),
		restRow("GET", "/api/states/{entity_id}", Read, readEntityState),
		restRow("GET", "/api/history/period/{start}", Read, readHistory),
		restRow("GET", "/api/logbook/{start}", Read, readLogbook),
		restRow("GET", "/api/error_log", Read, readSystem("logs")),
		restRow("GET", "/api/services", Read, filtered(FilterServices)),
		restRow("POST", "/api/template", Read, readSystem("template")),
		restRow("GET", "/api/config", Read, filtered(FilterCoreConfig)),
		exempt(restRow("GET", "/api/", Exempt, nil), "the API liveness probe answers \"API running\" and nothing about any Home Assistant resource"),
	}

	companionRows = []*row{
		exempt(compRow("GET", "/v1/health", Exempt, nil), "the companion's own liveness probe"),
		exempt(compRow("GET", "/v1/status", Exempt, nil), "the companion's capability flags"),
		exempt(compRow("GET", "/v1/config/wiring", Exempt, nil), "a layout probe; the create it asks about is checked on its own row"),

		compRow("POST", "/v1/ref/replace", Write, mapRefReplace),
		compRow("POST", "/v1/config/template", Write, mapTemplateCreate),
		compRow("DELETE", "/v1/config/template", Write, queryID(policy.Delete, policy.KindTemplate, "")),
		compRow("PUT", "/v1/config/script", Write, queryID(policy.Edit, policy.KindScript, "config")),
		compRow("POST", "/v1/config/script", Write, mapScriptCreate),
		compRow("DELETE", "/v1/config/script", Write, queryID(policy.Delete, policy.KindScript, "")),
		compRow("PUT", "/v1/config/automation", Write, mapAutomationPut),
		compRow("POST", "/v1/config/automation", Write, mapAutomationCreate),
		compRow("DELETE", "/v1/config/automation", Write, mapAutomationDelete),
		compRow("POST", "/v1/config/helper", Write, mapHelperCreate),
		compRow("DELETE", "/v1/config/helper", Write, mapHelperDelete),
		compRow("POST", "/v1/wireguard/config", Write, systemWrite(policy.Edit, "wireguard")),
		compRow("POST", "/v1/wireguard/start", Write, systemWrite(policy.Control, "wireguard")),
		compRow("POST", "/v1/wireguard/stop", Write, systemWrite(policy.Control, "wireguard")),

		compRow("POST", "/v1/ha/check-config", Read, readSystem("check_config")),
		compRow("GET", "/v1/config/files", Read, filtered(FilterFiles)),
		compRow("GET", "/v1/config/file", Read, readConfigFile),
		compRow("GET", "/v1/config/block", Read, readConfigBlock),
		compRow("GET", "/v1/related/entity", Read, readRelated),
		compRow("GET", "/v1/ref/scan", Read, readRefHits("hits")),
		compRow("GET", "/v1/ref/grep", Read, readRefHits("hits")),
		compRow("GET", "/v1/ref/entities", Read, readRefHits("entities")),
		compRow("GET", "/v1/config/templates", Read, filtered(FilterTemplates)),
		compRow("GET", "/v1/config/template", Read, readQueryID("template", "id", "config")),
		compRow("GET", "/v1/config/script", Read, readQueryID("script", "id", "config")),
		compRow("GET", "/v1/config/automation", Read, readQueryID("automation", "id", "config")),
		compRow("GET", "/v1/config/helpers", Read, filtered(FilterHelpers)),
		compRow("GET", "/v1/config/helper", Read, filtered(FilterHelper)),
		compRow("GET", "/v1/wireguard/status", Read, readSystem("wireguard")),
		compRow("GET", "/v1/logs", Read, readSystem("logs")),
	}
)

func indexWS(rows ...*row) map[string]*row {
	m := make(map[string]*row, len(rows))
	for _, r := range rows {
		if _, dup := m[r.wsType]; dup {
			panic("reqmap: websocket row " + r.key + " is declared twice")
		}
		m[r.wsType] = r
	}
	return m
}

// RowInfo describes one row of the table.
type RowInfo struct {
	// Key is the manifest key (`ws <type>`, `rest <METHOD> <template>`, …).
	Key string
	// Transport is the row's family.
	Transport Transport
	// Disposition is Write, Read or Exempt.
	Disposition Disposition
}

// Rows returns every row of the table, sorted by key. The closure gate holds it
// in lockstep with the request half of dev/surfaces/policy.manifest.
func Rows() []RowInfo {
	var out []RowInfo
	add := func(r *row) { out = append(out, RowInfo{Key: r.key, Transport: r.transport, Disposition: r.disp}) }
	for _, r := range wsRows {
		add(r)
	}
	for _, rs := range [][]*row{supervisorRows, restRows, companionRows} {
		for _, r := range rs {
			add(r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// MatchTemplate returns the key of the row whose path template has the same
// shape as tmpl — the same method, the same literal segments, a placeholder
// wherever tmpl has one (`{…}`, whatever its name). The closure gate uses it to
// key a path the source builds by concatenation: the source's names for the
// dynamic parts are its own, the row's are the contract's.
func MatchTemplate(transport Transport, method, tmpl string) (string, bool) {
	var rows []*row
	switch transport {
	case REST:
		rows = restRows
	case Companion:
		rows = companionRows
	case WS:
		rows = supervisorRows
	}
	segs := splitPath(strings.SplitN(tmpl, "?", 2)[0])
	for _, r := range rows {
		if r.method != upper(method) || len(r.tmpl) != len(segs) {
			continue
		}
		same := true
		for i, t := range r.tmpl {
			if isPlaceholder(t) != isPlaceholder(segs[i]) || (!isPlaceholder(t) && t != segs[i]) {
				same = false
				break
			}
		}
		if same {
			return r.key, true
		}
	}
	return "", false
}
