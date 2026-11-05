package reqmap_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

func ws(typ string, params map[string]any) reqmap.Request {
	return reqmap.Request{Transport: reqmap.WS, Type: typ, Params: params}
}

func rest(method, path, body string) reqmap.Request {
	return reqmap.Request{Transport: reqmap.REST, Method: method, Path: path, Body: []byte(body)}
}

func comp(method, path, body string) reqmap.Request {
	return reqmap.Request{Transport: reqmap.Companion, Method: method, Path: path, Body: []byte(body)}
}

// vector is one request in, one classification out.
type vector struct {
	name   string
	req    reqmap.Request
	facts  *reqmap.Facts
	disp   reqmap.Disposition
	row    string   // expected row key ("" for unmapped)
	checks []string // expected `<verb> <target>` lines, in order (Write and ReadDeferred)
	// refused, when set, expects Refused() and a Rule with this prefix.
	refused string
}

func checkVector(t *testing.T, v vector) {
	t.Helper()
	got := reqmap.Classify(v.req, v.facts)
	if got.Disposition != v.disp {
		t.Errorf("%s: disposition %s, want %s (rule %q, detail %q)", v.name, got.Disposition, v.disp, got.Rule, got.Detail)
	}
	if got.Row != v.row {
		t.Errorf("%s: row %q, want %q", v.name, got.Row, v.row)
	}
	if v.disp == reqmap.Write || v.disp == reqmap.ReadDeferred {
		if s := got.Strings(); !slices.Equal(s, v.checks) {
			t.Errorf("%s: checks\n  got  %q\n  want %q", v.name, s, v.checks)
		}
	}
	if v.refused != "" {
		if !got.Refused() {
			t.Errorf("%s: not refused (disposition %s, mutating %v)", v.name, got.Disposition, got.Mutating)
		}
		if !strings.HasPrefix(got.Rule, v.refused) {
			t.Errorf("%s: rule %q, want prefix %q", v.name, got.Rule, v.refused)
		}
	} else if got.Refused() {
		t.Errorf("%s: refused (%s: %s), want not refused", v.name, got.Rule, got.Detail)
	}
}

var registries = &reqmap.Registries{
	Entities: []reqmap.RegistryEntity{
		{EntityID: "light.kitchen", UniqueID: "u-kitchen", ConfigEntryID: "E1", AreaID: "kitchen", Labels: []string{"night"}},
		{EntityID: "lock.front_door", UniqueID: "u-lock", ConfigEntryID: "E2", Labels: []string{"night", "secure"}},
		{EntityID: "script.morning", UniqueID: "morning_routine"},
		{EntityID: "script.unregistered_uid"},
	},
	Devices: []reqmap.RegistryDevice{
		{ID: "dev1", AreaID: "kitchen", Labels: []string{"night"}, ConfigEntries: []string{"E1", "E9"}},
		{ID: "dev2", ConfigEntries: []string{"E2"}},
	},
	Areas: []reqmap.RegistryArea{
		{ID: "kitchen", FloorID: "ground", Labels: []string{"night"}},
		{ID: "attic", FloorID: "top"},
	},
}

// writeVectors cover every Write row at least once.
var writeVectors = []vector{
	// --- websocket ---------------------------------------------------------
	{
		name: "entity registry update maps each changed key to its grammar field",
		req:  ws("config/entity_registry/update", map[string]any{"entity_id": "light.kitchen", "labels": []any{"a"}, "area_id": nil}),
		disp: reqmap.Write, row: "ws config/entity_registry/update",
		checks: []string{"edit entity:light.kitchen/registry/area_id", "edit entity:light.kitchen/registry/labels"},
	},
	{
		name: "new_entity_id is registry/entity_id plus a create of the new id (E-5)",
		req:  ws("config/entity_registry/update", map[string]any{"entity_id": "light.kitchen", "new_entity_id": "light.cooking"}),
		disp: reqmap.Write, row: "ws config/entity_registry/update",
		checks: []string{"edit entity:light.kitchen/registry/entity_id", "create entity:light.cooking"},
	},
	{
		name: "name, icon and disabled_by map 1:1",
		req:  ws("config/entity_registry/update", map[string]any{"entity_id": "light.kitchen", "name": "K", "icon": "mdi:x", "disabled_by": "user"}),
		disp: reqmap.Write, row: "ws config/entity_registry/update",
		checks: []string{"edit entity:light.kitchen/registry/disabled_by", "edit entity:light.kitchen/registry/icon", "edit entity:light.kitchen/registry/name"},
	},
	{
		name: "an entity registry key the grammar lacks is unmappable and refused",
		req:  ws("config/entity_registry/update", map[string]any{"entity_id": "light.kitchen", "hidden_by": "user"}),
		disp: reqmap.Unmappable, row: "ws config/entity_registry/update",
		refused: "fail-closed: unmappable request ws config/entity_registry/update",
	},
	{
		name: "an entity id the grammar refuses is unmappable, never canonicalised",
		req:  ws("config/entity_registry/update", map[string]any{"entity_id": "light.kitchen ", "labels": []any{}}),
		disp: reqmap.Unmappable, row: "ws config/entity_registry/update",
		refused: "fail-closed: unmappable request",
	},
	{
		name: "an entity registry update naming no field is still checked, as the registry entry whole",
		req:  ws("config/entity_registry/update", map[string]any{"entity_id": "light.kitchen"}),
		disp: reqmap.Write, row: "ws config/entity_registry/update",
		checks: []string{"edit entity:light.kitchen/registry"},
	},
	{
		name: "entity registry remove is delete entity",
		req:  ws("config/entity_registry/remove", map[string]any{"entity_id": "automation.x"}),
		disp: reqmap.Write, row: "ws config/entity_registry/remove",
		checks: []string{"delete entity:automation.x"},
	},
	{
		name: "device registry update maps area_id, labels, name_by_user",
		req:  ws("config/device_registry/update", map[string]any{"device_id": "dev1", "area_id": "kitchen", "labels": []any{}, "name_by_user": "x"}),
		disp: reqmap.Write, row: "ws config/device_registry/update",
		checks: []string{"edit device:dev1/registry/area_id", "edit device:dev1/registry/labels", "edit device:dev1/registry/name_by_user"},
	},
	{
		name: "a device registry update naming no field is still checked, as the registry entry whole",
		req:  ws("config/device_registry/update", map[string]any{"device_id": "dev1"}),
		disp: reqmap.Write, row: "ws config/device_registry/update",
		checks: []string{"edit device:dev1/registry"},
	},
	{
		name: "a device registry key the grammar lacks is refused",
		req:  ws("config/device_registry/update", map[string]any{"device_id": "dev1", "disabled_by": "user"}),
		disp: reqmap.Unmappable, row: "ws config/device_registry/update",
		refused: "fail-closed: unmappable request",
	},
	{
		name: "label create is a kind-wide create (D-75)",
		req:  ws("config/label_registry/create", map[string]any{"name": "Night"}),
		disp: reqmap.Write, row: "ws config/label_registry/create",
		checks: []string{"create label:*"},
	},
	{
		name:  "label delete enumerates every holder from the registries (D-76)",
		req:   ws("config/label_registry/delete", map[string]any{"label_id": "night"}),
		facts: &reqmap.Facts{Registries: registries},
		disp:  reqmap.Write, row: "ws config/label_registry/delete",
		checks: []string{
			"edit area:kitchen", "edit device:dev1/registry/labels",
			"edit entity:light.kitchen/registry/labels", "edit entity:lock.front_door/registry/labels",
			"delete label:night",
		},
	},
	{
		name: "label delete without the registries checks every holder kind-wide",
		req:  ws("config/label_registry/delete", map[string]any{"label_id": "night"}),
		disp: reqmap.Write, row: "ws config/label_registry/delete",
		checks: []string{"edit area:*", "edit device:*/registry/labels", "edit entity:*/registry/labels", "delete label:night"},
	},
	{
		name: "area create is a kind-wide create",
		req:  ws("config/area_registry/create", map[string]any{"name": "Kitchen", "floor_id": "ground"}),
		disp: reqmap.Write, row: "ws config/area_registry/create",
		checks: []string{"create area:*"},
	},
	{
		name:  "area delete enumerates the entities and devices whose own area it is",
		req:   ws("config/area_registry/delete", map[string]any{"area_id": "kitchen"}),
		facts: &reqmap.Facts{Registries: registries},
		disp:  reqmap.Write, row: "ws config/area_registry/delete",
		checks: []string{"edit device:dev1/registry/area_id", "edit entity:light.kitchen/registry/area_id", "delete area:kitchen"},
	},
	{
		name: "area delete without the registries is kind-wide",
		req:  ws("config/area_registry/delete", map[string]any{"area_id": "kitchen"}),
		disp: reqmap.Write, row: "ws config/area_registry/delete",
		checks: []string{"edit device:*/registry/area_id", "edit entity:*/registry/area_id", "delete area:kitchen"},
	},
	{
		name: "floor create is a kind-wide create",
		req:  ws("config/floor_registry/create", map[string]any{"name": "Ground"}),
		disp: reqmap.Write, row: "ws config/floor_registry/create",
		checks: []string{"create floor:*"},
	},
	{
		name:  "floor delete edits every area on the floor",
		req:   ws("config/floor_registry/delete", map[string]any{"floor_id": "ground"}),
		facts: &reqmap.Facts{Registries: registries},
		disp:  reqmap.Write, row: "ws config/floor_registry/delete",
		checks: []string{"edit area:kitchen", "delete floor:ground"},
	},
	{
		name: "floor delete without the registries edits every area",
		req:  ws("config/floor_registry/delete", map[string]any{"floor_id": "ground"}),
		disp: reqmap.Write, row: "ws config/floor_registry/delete",
		checks: []string{"edit area:*", "delete floor:ground"},
	},
	{
		name: "a dashboard save with no stored config read is the whole dashboard",
		req:  ws("lovelace/config/save", map[string]any{"url_path": "dash-a", "config": map[string]any{"views": []any{}}}),
		disp: reqmap.Write, row: "ws lovelace/config/save",
		checks: []string{"edit dashboard:dash-a"},
	},
	{
		name: "the default dashboard is dashboard: however it is named",
		req:  ws("lovelace/config/save", map[string]any{"url_path": "lovelace", "config": map[string]any{"title": "Home"}}),
		disp: reqmap.Write, row: "ws lovelace/config/save",
		checks: []string{"edit dashboard:"},
	},
	{
		name: "a dashboard save diffs per view against the stored config",
		req: ws("lovelace/config/save", map[string]any{"config": map[string]any{"views": []any{
			map[string]any{"path": "home", "cards": []any{"x"}},
			map[string]any{"path": "new"},
		}}}),
		facts: &reqmap.Facts{Dashboards: map[string]reqmap.StoredDashboard{"": {Config: []byte(
			`{"views":[{"path":"home","cards":[]},{"path":"gone"}]}`)}}},
		disp: reqmap.Write, row: "ws lovelace/config/save",
		checks: []string{"edit dashboard:/views/home", "create dashboard:/views/new", "delete dashboard:/views/gone"},
	},
	{
		name: "dashboard create takes the url_path sent",
		req:  ws("lovelace/dashboards/create", map[string]any{"url_path": "dash-b", "title": "B"}),
		disp: reqmap.Write, row: "ws lovelace/dashboards/create",
		checks: []string{"create dashboard:dash-b"},
	},
	{
		name:  "dashboard delete resolves the storage id through the list this invocation read",
		req:   ws("lovelace/dashboards/delete", map[string]any{"dashboard_id": "dash_b"}),
		facts: &reqmap.Facts{DashboardIDs: map[string]string{"dash_b": "dash-b"}},
		disp:  reqmap.Write, row: "ws lovelace/dashboards/delete",
		checks: []string{"delete dashboard:dash-b"},
	},
	{
		name:    "dashboard delete without the id-to-url_path read is refused",
		req:     ws("lovelace/dashboards/delete", map[string]any{"dashboard_id": "dash_b"}),
		disp:    reqmap.Unmappable,
		row:     "ws lovelace/dashboards/delete",
		refused: "fail-closed: dashboard id not resolved",
	},
	{
		name:    "dashboard delete of an id the list does not hold is refused",
		req:     ws("lovelace/dashboards/delete", map[string]any{"dashboard_id": "other"}),
		facts:   &reqmap.Facts{DashboardIDs: map[string]string{"dash_b": "dash-b"}},
		disp:    reqmap.Unmappable,
		row:     "ws lovelace/dashboards/delete",
		refused: "fail-closed: dashboard id not resolved",
	},

	// --- REST ----------------------------------------------------------------
	{
		name: "a target-less service call is the service check alone",
		req:  rest("POST", "/api/services/homeassistant/restart", ""),
		disp: reqmap.Write, row: "rest POST /api/services/{domain}/{service}",
		checks: []string{"control service:homeassistant.restart"},
	},
	{
		name:  "config entry delete deletes every entity and device naming the entry (D-76)",
		req:   rest("DELETE", "/api/config/config_entries/entry/E1", ""),
		facts: &reqmap.Facts{Registries: registries},
		disp:  reqmap.Write, row: "rest DELETE /api/config/config_entries/entry/{entry_id}",
		checks: []string{"delete config_entry:E1", "delete device:dev1", "delete entity:light.kitchen"},
	},
	{
		name: "config entry delete without the registries is kind-wide on entities and devices",
		req:  rest("DELETE", "/api/config/config_entries/entry/E1", ""),
		disp: reqmap.Write, row: "rest DELETE /api/config/config_entries/entry/{entry_id}",
		checks: []string{"delete config_entry:E1", "delete device:*", "delete entity:*"},
	},
	{
		name: "options flow start is edit on the entry's options (body handler = entry_id, D-83)",
		req:  rest("POST", "/api/config/config_entries/options/flow", `{"handler":"E1"}`),
		disp: reqmap.Write, row: "rest POST /api/config/config_entries/options/flow",
		checks: []string{"edit config_entry:E1/options"},
	},
	{
		name:    "options flow start without a handler is refused",
		req:     rest("POST", "/api/config/config_entries/options/flow", `{}`),
		disp:    reqmap.Unmappable,
		row:     "rest POST /api/config/config_entries/options/flow",
		refused: "fail-closed: unmappable request",
	},
	{
		name:  "an options flow step uses the handler this invocation holds",
		req:   rest("POST", "/api/config/config_entries/options/flow/F1", `{"x":1}`),
		facts: &reqmap.Facts{OptionsFlowHandlers: map[string]string{"F1": "E1"}},
		disp:  reqmap.Write, row: "rest POST /api/config/config_entries/options/flow/{flow_id}",
		checks: []string{"edit config_entry:E1/options"},
	},
	{
		name: "an options flow step without the handler is kind-wide on every entry's options",
		req:  rest("POST", "/api/config/config_entries/options/flow/F1", `{}`),
		disp: reqmap.Write, row: "rest POST /api/config/config_entries/options/flow/{flow_id}",
		checks: []string{"edit config_entry:*/options"},
	},
	{
		name:  "an options flow abort is the start's verb, not delete",
		req:   rest("DELETE", "/api/config/config_entries/options/flow/F1", ""),
		facts: &reqmap.Facts{OptionsFlowHandlers: map[string]string{"F1": "E1"}},
		disp:  reqmap.Write, row: "rest DELETE /api/config/config_entries/options/flow/{flow_id}",
		checks: []string{"edit config_entry:E1/options"},
	},
	{
		name: "the options flow GET runs the step, so it is the step's write (R-5)",
		req:  rest("GET", "/api/config/config_entries/options/flow/F1", ""),
		disp: reqmap.Write, row: "rest GET /api/config/config_entries/options/flow/{flow_id}",
		checks: []string{"edit config_entry:*/options"},
	},
	{
		name: "config flow start is a kind-wide create",
		req:  rest("POST", "/api/config/config_entries/flow", `{"handler":"mqtt"}`),
		disp: reqmap.Write, row: "rest POST /api/config/config_entries/flow",
		checks: []string{"create config_entry:*"},
	},
	{
		name: "a config flow step is kind-wide create and edit",
		req:  rest("POST", "/api/config/config_entries/flow/F2", `{}`),
		disp: reqmap.Write, row: "rest POST /api/config/config_entries/flow/{flow_id}",
		checks: []string{"edit config_entry:*", "create config_entry:*"},
	},
	{
		name: "the config flow GET is the step's write (R-5)",
		req:  rest("GET", "/api/config/config_entries/flow/F2", ""),
		disp: reqmap.Write, row: "rest GET /api/config/config_entries/flow/{flow_id}",
		checks: []string{"edit config_entry:*", "create config_entry:*"},
	},

	// --- companion -------------------------------------------------------------
	{
		name: "a ref/replace dry run plans from its response",
		req:  comp("POST", "/v1/ref/replace", `{"old":"light.a","new":"light.b","dry_run":true}`),
		disp: reqmap.Write, row: "companion POST /v1/ref/replace",
	},
	{
		name: "a ref/replace without dry_run is a dry run (the companion's default)",
		req:  comp("POST", "/v1/ref/replace", `{"old":"light.a","new":"light.b"}`),
		disp: reqmap.Write, row: "companion POST /v1/ref/replace",
	},
	{
		name:    "a confirmed ref/replace without a checked dry run is refused",
		req:     comp("POST", "/v1/ref/replace", `{"old":"light.a","new":"light.b","dry_run":false}`),
		disp:    reqmap.Unmappable,
		row:     "companion POST /v1/ref/replace",
		refused: "fail-closed: ref/replace without a checked dry run",
	},
	{
		name: "a confirmed ref/replace maps through the checked dry run of the same {old, new}",
		req:  comp("POST", "/v1/ref/replace", `{"old":"light.a","new":"light.b","dry_run":false}`),
		facts: &reqmap.Facts{RefReplacePlans: []reqmap.RefReplacePlan{
			{Old: "light.a", New: "light.x", Changes: []reqmap.PlanChange{{Location: "wrong.yaml"}}},
			{Old: "light.a", New: "light.b", Changes: []reqmap.PlanChange{
				{Location: "automations.yaml", Path: "[0].actions[0].entity_id", Owner: &reqmap.TypedOwner{Kind: policy.KindAutomation, ID: "a1"}},
				{Location: "scripts.yaml", Path: "s.sequence[0].entity_id"},
			}},
		}},
		disp: reqmap.Write, row: "companion POST /v1/ref/replace",
		// + control on the new id: a change lies in automation a1 (W-1).
		checks: []string{"control entity:light.b", "edit automation:a1/config", "edit file:automations.yaml", "edit file:scripts.yaml"},
	},
	{
		name: "a confirmed ref/replace under indirect: unchecked adds no control on the new id (D-135)",
		req:  comp("POST", "/v1/ref/replace", `{"old":"light.a","new":"light.b","dry_run":false}`),
		facts: &reqmap.Facts{IndirectUnchecked: true, RefReplacePlans: []reqmap.RefReplacePlan{
			{Old: "light.a", New: "light.b", Changes: []reqmap.PlanChange{
				{Location: "automations.yaml", Path: "[0].actions[0].entity_id", Owner: &reqmap.TypedOwner{Kind: policy.KindAutomation, ID: "a1"}},
			}},
		}},
		disp: reqmap.Write, row: "companion POST /v1/ref/replace",
		checks: []string{"edit automation:a1/config", "edit file:automations.yaml"},
	},
	{
		name: "template create takes every unique_id of a full block",
		req:  comp("POST", "/v1/config/template", "sensor:\n  - unique_id: t1\n    state: '1'\nbinary_sensor:\n  - unique_id: t2\n    state: 'on'\n"),
		disp: reqmap.Write, row: "companion POST /v1/config/template",
		checks: []string{"create template:t1", "create template:t2"},
	},
	{
		name: "template create of a bare item",
		req:  comp("POST", "/v1/config/template?domain=sensor", "unique_id: t3\nstate: '1'\n"),
		disp: reqmap.Write, row: "companion POST /v1/config/template",
		checks: []string{"create template:t3"},
	},
	{
		name:    "a template block entity without unique_id is refused (D-78)",
		req:     comp("POST", "/v1/config/template", "sensor:\n  - unique_id: t1\n  - state: '2'\n"),
		disp:    reqmap.Unmappable,
		row:     "companion POST /v1/config/template",
		refused: "fail-closed: template entity without unique_id",
	},
	{
		name:    "an empty template body decodes to nothing and is refused, never mapped as no checks",
		req:     comp("POST", "/v1/config/template", "  \n"),
		disp:    reqmap.Unmappable,
		row:     "companion POST /v1/config/template",
		refused: "fail-closed: unmappable request",
	},
	{
		name:    "a template body that is a list is refused",
		req:     comp("POST", "/v1/config/template", "- sensor: []\n"),
		disp:    reqmap.Unmappable,
		row:     "companion POST /v1/config/template",
		refused: "fail-closed: unmappable request",
	},
	{
		name:    "a ref/replace body without old and new is refused",
		req:     comp("POST", "/v1/ref/replace", `{"dry_run":false}`),
		disp:    reqmap.Unmappable,
		row:     "companion POST /v1/ref/replace",
		refused: "fail-closed: unmappable request",
	},
	{
		name:    "a ref/replace body that is not JSON is refused",
		req:     comp("POST", "/v1/ref/replace", `old: a`),
		disp:    reqmap.Unmappable,
		row:     "companion POST /v1/ref/replace",
		refused: "fail-closed: unmappable request",
	},
	{
		name: "template delete",
		req:  comp("DELETE", "/v1/config/template?id=t1", ""),
		disp: reqmap.Write, row: "companion DELETE /v1/config/template",
		checks: []string{"delete template:t1"},
	},
	{
		name: "script put is edit on its config, dry run or not",
		req:  comp("PUT", "/v1/config/script?id=morning_routine&dry_run=true", "morning_routine:\n  sequence: []\n"),
		disp: reqmap.Write, row: "companion PUT /v1/config/script",
		checks: []string{"edit script:morning_routine/config"},
	},
	{
		name: "script create takes the body's single top-level key",
		req:  comp("POST", "/v1/config/script", "evening:\n  sequence: []\n"),
		disp: reqmap.Write, row: "companion POST /v1/config/script",
		checks: []string{"create script:evening"},
	},
	{
		name:    "script create with two top-level keys is refused",
		req:     comp("POST", "/v1/config/script", "a:\n  sequence: []\nb:\n  sequence: []\n"),
		disp:    reqmap.Unmappable,
		row:     "companion POST /v1/config/script",
		refused: "fail-closed: unmappable request",
	},
	{
		name: "script delete",
		req:  comp("DELETE", "/v1/config/script?id=evening", ""),
		disp: reqmap.Write, row: "companion DELETE /v1/config/script",
		checks: []string{"delete script:evening"},
	},
	{
		name: "automation put with the same id is edit on its config",
		req:  comp("PUT", "/v1/config/automation?id=a1", "id: a1\nalias: A\n"),
		disp: reqmap.Write, row: "companion PUT /v1/config/automation",
		checks: []string{"edit automation:a1/config"},
	},
	{
		name: "automation put with a differing body id also creates it (E-5)",
		req:  comp("PUT", "/v1/config/automation?id=a1&dry_run=true", "id: a2\nalias: A\n"),
		disp: reqmap.Write, row: "companion PUT /v1/config/automation",
		checks: []string{"edit automation:a1/config", "create automation:a2"},
	},
	{
		name:    "automation put with an id-less body is refused (R-6)",
		req:     comp("PUT", "/v1/config/automation?id=a1", "alias: A\n"),
		disp:    reqmap.Unmappable,
		row:     "companion PUT /v1/config/automation",
		refused: "fail-closed: automation without a config id",
	},
	{
		name:    "automation put with a non-string id is refused (R-6)",
		req:     comp("PUT", "/v1/config/automation?id=a1", "id: 17\nalias: A\n"),
		disp:    reqmap.Unmappable,
		row:     "companion PUT /v1/config/automation",
		refused: "fail-closed: automation without a config id",
	},
	{
		name: "automation create takes the body id",
		req:  comp("POST", "/v1/config/automation", "id: a3\nalias: A\n"),
		disp: reqmap.Write, row: "companion POST /v1/config/automation",
		checks: []string{"create automation:a3"},
	},
	{
		name:  "automation delete of a config id a GET verified in this invocation",
		req:   comp("DELETE", "/v1/config/automation?id=a1", ""),
		facts: &reqmap.Facts{VerifiedAutomationIDs: map[string]bool{"a1": true}},
		disp:  reqmap.Write, row: "companion DELETE /v1/config/automation",
		checks: []string{"delete automation:a1"},
	},
	{
		name:    "automation delete of an unverified id is refused (alias fallback, D-78)",
		req:     comp("DELETE", "/v1/config/automation?id=Morning%20lights", ""),
		disp:    reqmap.Unmappable,
		row:     "companion DELETE /v1/config/automation",
		refused: "fail-closed: automation config id not verified",
	},
	{
		name: "helper create is helper:<domain>/<key>",
		req:  comp("POST", "/v1/config/helper?domain=input_boolean", `{"guest_mode":{"name":"Guest"}}`),
		disp: reqmap.Write, row: "companion POST /v1/config/helper",
		checks: []string{"create helper:input_boolean/guest_mode"},
	},
	{
		name: "helper delete with the resolved domain",
		req:  comp("DELETE", "/v1/config/helper?id=guest_mode&domain=input_boolean", ""),
		disp: reqmap.Write, row: "companion DELETE /v1/config/helper",
		checks: []string{"delete helper:input_boolean/guest_mode"},
	},
	{
		name:    "a domain-less helper delete is refused",
		req:     comp("DELETE", "/v1/config/helper?id=guest_mode", ""),
		disp:    reqmap.Unmappable,
		row:     "companion DELETE /v1/config/helper",
		refused: "fail-closed: unmappable request",
	},
	{
		name: "wireguard config is edit system:wireguard",
		req:  comp("POST", "/v1/wireguard/config?tunnel=wg0", "[Interface]\n"),
		disp: reqmap.Write, row: "companion POST /v1/wireguard/config",
		checks: []string{"edit system:wireguard"},
	},
	{
		name: "wireguard start is control",
		req:  comp("POST", "/v1/wireguard/start?tunnel=wg0", ""),
		disp: reqmap.Write, row: "companion POST /v1/wireguard/start",
		checks: []string{"control system:wireguard"},
	},
	{
		name: "wireguard stop is control",
		req:  comp("POST", "/v1/wireguard/stop?tunnel=wg0", ""),
		disp: reqmap.Write, row: "companion POST /v1/wireguard/stop",
		checks: []string{"control system:wireguard"},
	},
}

// TestClassifyWriteRequests — every write row maps its request to the
// (resource, verb) set the manifest's `map:` comment states.
func TestClassifyWriteRequests(t *testing.T) {
	for _, v := range writeVectors {
		checkVector(t, v)
	}
}

// readVectors cover every read-deferred and exempt row.
var readVectors = []vector{
	{name: "ws auth is exempt", req: ws("auth", map[string]any{"access_token": "x"}), disp: reqmap.Exempt, row: "ws auth"},
	{name: "the reach question is exempt", req: ws("extract_from_target", map[string]any{"target": map[string]any{"area_id": []any{"office"}}, "expand_group": true}), disp: reqmap.Exempt, row: "ws extract_from_target"},
	{name: "supervisor add-on list", req: ws("supervisor/api", map[string]any{"endpoint": "/addons", "method": "get"}), disp: reqmap.Exempt, row: "ws supervisor/api GET /addons"},
	{name: "supervisor add-on info", req: ws("supervisor/api", map[string]any{"endpoint": "/addons/abc_hactl/info", "method": "get"}), disp: reqmap.Exempt, row: "ws supervisor/api GET /addons/{slug}/info"},
	{name: "supervisor method defaults to get", req: ws("supervisor/api", map[string]any{"endpoint": "/addons"}), disp: reqmap.Exempt, row: "ws supervisor/api GET /addons"},
	{name: "ingress session is transport", req: ws("supervisor/api", map[string]any{"endpoint": "/ingress/session", "method": "post"}), disp: reqmap.Exempt, row: "ws supervisor/api POST /ingress/session"},

	{name: "trace list of a domain", req: ws("trace/list", map[string]any{"domain": "automation"}), disp: reqmap.ReadDeferred, row: "ws trace/list", checks: []string{"read automation:*/trace"}},
	{name: "trace get", req: ws("trace/get", map[string]any{"domain": "script", "item_id": "morning", "run_id": "r"}), disp: reqmap.ReadDeferred, row: "ws trace/get", checks: []string{"read script:morning/trace"}},
	{name: "validate_config is read system:check_config", req: ws("validate_config", map[string]any{"triggers": []any{}}), disp: reqmap.ReadDeferred, row: "ws validate_config", checks: []string{"read system:check_config"}},
	{name: "system log", req: ws("system_log/list", nil), disp: reqmap.ReadDeferred, row: "ws system_log/list", checks: []string{"read system:logs"}},
	{name: "repairs", req: ws("repairs/list_issues", nil), disp: reqmap.ReadDeferred, row: "ws repairs/list_issues", checks: []string{"read system:logs"}},
	{name: "entity registry list", req: ws("config/entity_registry/list", nil), disp: reqmap.ReadDeferred, row: "ws config/entity_registry/list", checks: []string{"read entity:*/registry"}},
	{name: "area list", req: ws("config/area_registry/list", nil), disp: reqmap.ReadDeferred, row: "ws config/area_registry/list", checks: []string{"read area:*"}},
	{name: "label list", req: ws("config/label_registry/list", nil), disp: reqmap.ReadDeferred, row: "ws config/label_registry/list", checks: []string{"read label:*"}},
	{name: "floor list", req: ws("config/floor_registry/list", nil), disp: reqmap.ReadDeferred, row: "ws config/floor_registry/list", checks: []string{"read floor:*"}},
	{name: "users", req: ws("config/auth/list", nil), disp: reqmap.ReadDeferred, row: "ws config/auth/list"},
	{name: "device list", req: ws("config/device_registry/list", nil), disp: reqmap.ReadDeferred, row: "ws config/device_registry/list", checks: []string{"read device:*/registry"}},
	{name: "dashboard list", req: ws("lovelace/dashboards/list", nil), disp: reqmap.ReadDeferred, row: "ws lovelace/dashboards/list", checks: []string{"read dashboard:*"}},
	{name: "dashboard config of the default", req: ws("lovelace/config", map[string]any{}), disp: reqmap.ReadDeferred, row: "ws lovelace/config", checks: []string{"read dashboard:"}},
	{name: "manifests", req: ws("manifest/list", nil), disp: reqmap.ReadDeferred, row: "ws manifest/list"},
	{name: "resources", req: ws("lovelace/resources", nil), disp: reqmap.ReadDeferred, row: "ws lovelace/resources", checks: []string{"read dashboard:/resources"}},
	{name: "statistics metadata skips external statistics", req: ws("recorder/get_statistics_metadata", map[string]any{"statistic_ids": []any{"sensor.power", "tibber:price"}}), disp: reqmap.ReadDeferred, row: "ws recorder/get_statistics_metadata", checks: []string{"read entity:sensor.power/stats"}},
	{name: "statistics during period", req: ws("recorder/statistics_during_period", map[string]any{"statistic_ids": []any{"sensor.power"}}), disp: reqmap.ReadDeferred, row: "ws recorder/statistics_during_period", checks: []string{"read entity:sensor.power/stats"}},
	{name: "statistic ids", req: ws("recorder/list_statistic_ids", nil), disp: reqmap.ReadDeferred, row: "ws recorder/list_statistic_ids", checks: []string{"read entity:*/stats"}},
	{name: "energy prefs", req: ws("energy/get_prefs", nil), disp: reqmap.ReadDeferred, row: "ws energy/get_prefs"},

	{name: "flow handlers", req: rest("GET", "/api/config/config_entries/flow_handlers", ""), disp: reqmap.ReadDeferred, row: "rest GET /api/config/config_entries/flow_handlers"},
	{name: "config entries", req: rest("GET", "/api/config/config_entries/entry", ""), disp: reqmap.ReadDeferred, row: "rest GET /api/config/config_entries/entry", checks: []string{"read config_entry:*"}},
	{name: "diagnostics", req: rest("GET", "/api/diagnostics/config_entry/E1", ""), disp: reqmap.ReadDeferred, row: "rest GET /api/diagnostics/config_entry/{entry_id}", checks: []string{"read config_entry:E1"}},
	{name: "states", req: rest("GET", "/api/states", ""), disp: reqmap.ReadDeferred, row: "rest GET /api/states", checks: []string{"read entity:*/state"}},
	{name: "one state", req: rest("GET", "/api/states/light.kitchen", ""), disp: reqmap.ReadDeferred, row: "rest GET /api/states/{entity_id}", checks: []string{"read entity:light.kitchen/attr", "read entity:light.kitchen/state"}},
	{name: "history", req: rest("GET", "/api/history/period/2026-11-01T00%3A00%3A00Z?filter_entity_id=sensor.a&end_time=x", ""), disp: reqmap.ReadDeferred, row: "rest GET /api/history/period/{start}", checks: []string{"read entity:sensor.a/history"}},
	{name: "logbook of every entity", req: rest("GET", "/api/logbook/2026-11-01", ""), disp: reqmap.ReadDeferred, row: "rest GET /api/logbook/{start}", checks: []string{"read entity:*/history"}},
	{name: "error log", req: rest("GET", "/api/error_log", ""), disp: reqmap.ReadDeferred, row: "rest GET /api/error_log", checks: []string{"read system:logs"}},
	{name: "services", req: rest("GET", "/api/services", ""), disp: reqmap.ReadDeferred, row: "rest GET /api/services", checks: []string{"read service:*"}},
	{name: "tpl eval is a POST that reads (E-6)", req: rest("POST", "/api/template", `{"template":"{{ 1 }}"}`), disp: reqmap.ReadDeferred, row: "rest POST /api/template"},
	{name: "core config", req: rest("GET", "/api/config", ""), disp: reqmap.ReadDeferred, row: "rest GET /api/config"},
	{name: "API probe", req: rest("GET", "/api/", ""), disp: reqmap.Exempt, row: "rest GET /api/"},

	{name: "companion health", req: comp("GET", "/v1/health", ""), disp: reqmap.Exempt, row: "companion GET /v1/health"},
	{name: "companion status", req: comp("get", "/v1/status", ""), disp: reqmap.Exempt, row: "companion GET /v1/status"},
	{name: "companion wiring", req: comp("GET", "/v1/config/wiring?domain=input_boolean", ""), disp: reqmap.Exempt, row: "companion GET /v1/config/wiring"},
	{name: "check-config is a POST that reads", req: comp("POST", "/v1/ha/check-config", ""), disp: reqmap.ReadDeferred, row: "companion POST /v1/ha/check-config", checks: []string{"read system:check_config"}},
	{name: "file list", req: comp("GET", "/v1/config/files", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/config/files", checks: []string{"read file:*"}},
	{name: "one file", req: comp("GET", "/v1/config/file?path=packages/x.yaml", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/config/file", checks: []string{"read file:packages/x.yaml"}},
	{name: "one block", req: comp("GET", "/v1/config/block?path=automations.yaml&id=a1", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/config/block", checks: []string{"read file:automations.yaml/block/a1"}},
	{name: "related", req: comp("GET", "/v1/related/entity?entity_id=light.kitchen", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/related/entity", checks: []string{"read entity:light.kitchen"}},
	{name: "ref scan", req: comp("GET", "/v1/ref/scan?target=light.kitchen", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/ref/scan", checks: []string{"read file:*"}},
	{name: "ref grep", req: comp("GET", "/v1/ref/grep?term=x", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/ref/grep", checks: []string{"read file:*"}},
	{name: "ref entities", req: comp("GET", "/v1/ref/entities", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/ref/entities", checks: []string{"read file:*"}},
	{name: "template list", req: comp("GET", "/v1/config/templates", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/config/templates", checks: []string{"read template:*"}},
	{name: "template read", req: comp("GET", "/v1/config/template?id=t1", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/config/template", checks: []string{"read template:t1/config"}},
	{name: "script read", req: comp("GET", "/v1/config/script?id=s1", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/config/script", checks: []string{"read script:s1/config"}},
	{name: "automation read", req: comp("GET", "/v1/config/automation?id=a1", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/config/automation", checks: []string{"read automation:a1/config"}},
	{name: "helpers", req: comp("GET", "/v1/config/helpers?domain=input_boolean", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/config/helpers", checks: []string{"read helper:*"}},
	{name: "helper read is response-side", req: comp("GET", "/v1/config/helper?id=x", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/config/helper"},
	{name: "wireguard status", req: comp("GET", "/v1/wireguard/status?tunnel=wg0", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/wireguard/status", checks: []string{"read system:wireguard"}},
	{name: "companion logs", req: comp("GET", "/v1/logs", ""), disp: reqmap.ReadDeferred, row: "companion GET /v1/logs", checks: []string{"read system:logs"}},
}

// TestClassifyReadAndExemptRequests — every read row is a read and every
// exempt row exempt: a write check never refuses them (D-74), and none is
// mutating.
func TestClassifyReadAndExemptRequests(t *testing.T) {
	for _, v := range readVectors {
		checkVector(t, v)
		if got := reqmap.Classify(v.req, v.facts); got.Mutating {
			t.Errorf("%s: a %s row reported mutating", v.name, got.Disposition)
		}
	}
}

// TestEveryRowHasAVector — the two tables above name every row, so a row added
// to the classifier without a vector fails here rather than shipping unasserted.
func TestEveryRowHasAVector(t *testing.T) {
	covered := map[string]bool{}
	for _, vs := range [][]vector{writeVectors, readVectors, serviceVectors} {
		for _, v := range vs {
			if v.disp != reqmap.Unmappable && v.disp != reqmap.Unmapped {
				covered[v.row] = true
			}
		}
	}
	rows := reqmap.Rows()
	if len(rows) < 80 {
		t.Fatalf("Rows() returned %d rows; the table has lost rows", len(rows))
	}
	for _, r := range rows {
		if !covered[r.Key] {
			t.Errorf("row %q (%s) has no vector that maps it", r.Key, r.Disposition)
		}
	}
}

// TestRowDispositionsAreWhatTheRowSays — a Write row is mutating, a read or
// exempt row is not; nothing else may appear in the table.
func TestRowDispositionsAreWhatTheRowSays(t *testing.T) {
	for _, r := range reqmap.Rows() {
		switch r.Disposition {
		case reqmap.Write, reqmap.ReadDeferred, reqmap.Exempt:
		default:
			t.Errorf("row %q has disposition %s", r.Key, r.Disposition)
		}
		prefix := string(r.Transport) + " "
		if !strings.HasPrefix(r.Key, prefix) {
			t.Errorf("row %q does not start with its transport %q", r.Key, prefix)
		}
	}
}

// TestUnmappedRequests — a request no row names is refused if it can change
// anything (every websocket type, every method but GET) and allowed otherwise
// until phase 3 (D-74, E-9).
func TestUnmappedRequests(t *testing.T) {
	for _, v := range []vector{
		{name: "an unknown websocket type", req: ws("config/entity_registry/frobnicate", nil), disp: reqmap.Unmapped, refused: "fail-closed: unmapped request ws config/entity_registry/frobnicate"},
		{name: "a websocket read nobody mapped is still refused", req: ws("search/related", nil), disp: reqmap.Unmapped, refused: "fail-closed: unmapped request ws search/related"},
		{name: "a supervisor pair nobody mapped", req: ws("supervisor/api", map[string]any{"endpoint": "/addons/x/restart", "method": "post"}), disp: reqmap.Unmapped, refused: "fail-closed: unmapped request ws supervisor/api POST /addons/x/restart"},
		{name: "a supervisor GET nobody mapped is a websocket message, so refused", req: ws("supervisor/api", map[string]any{"endpoint": "/host/info", "method": "get"}), disp: reqmap.Unmapped, refused: "fail-closed: unmapped request"},
		{name: "a REST POST nobody mapped", req: rest("POST", "/api/events/my_event", "{}"), disp: reqmap.Unmapped, refused: "fail-closed: unmapped request rest POST /api/events/my_event"},
		{name: "a REST DELETE nobody mapped", req: rest("DELETE", "/api/states/light.x", ""), disp: reqmap.Unmapped, refused: "fail-closed: unmapped request"},
		{name: "a companion PUT the deleted client method sent", req: comp("PUT", "/v1/config/file?path=a.yaml", "x: 1"), disp: reqmap.Unmapped, refused: "fail-closed: unmapped request companion PUT /v1/config/file"},
		{name: "a companion reload hactl never calls", req: comp("POST", "/v1/ha/reload/automation", ""), disp: reqmap.Unmapped, refused: "fail-closed: unmapped request"},
		{name: "a companion helper PUT hactl never calls", req: comp("PUT", "/v1/config/helper?id=x", "{}"), disp: reqmap.Unmapped, refused: "fail-closed: unmapped request"},
		{name: "an unknown transport", req: reqmap.Request{Transport: "smoke-signal"}, disp: reqmap.Unmapped, refused: "fail-closed: unmapped request"},
	} {
		checkVector(t, v)
	}
	got := reqmap.Classify(rest("GET", "/api/events", ""), nil)
	if got.Disposition != reqmap.Unmapped || got.Mutating || got.Refused() {
		t.Errorf("an unmapped GET: %+v, want unmapped, not mutating, not refused (phase 2)", got)
	}
	if !strings.HasPrefix(got.Rule, "fail-closed: unmapped request rest GET /api/events") {
		t.Errorf("an unmapped GET still names what it is: %q", got.Rule)
	}
	// A companion route is never matched as an HA REST path, nor the reverse.
	if got := reqmap.Classify(rest("GET", "/v1/health", ""), nil); got.Disposition != reqmap.Unmapped {
		t.Errorf("an HA REST request on a companion route mapped as %s (%q)", got.Disposition, got.Row)
	}
	if got := reqmap.Classify(comp("POST", "/api/services/light/turn_on", "{}"), nil); got.Disposition != reqmap.Unmapped || !got.Refused() {
		t.Errorf("a companion request on an HA REST path: %+v, want unmapped and refused", got)
	}
}

// TestWholePathRule — R-8: dot segments, encoded slashes and a placeholder that
// would have to span more or fewer than one segment make a request
// unmappable; never canonicalised.
func TestWholePathRule(t *testing.T) {
	for _, v := range []vector{
		{name: "a dot-dot segment in a write", req: rest("DELETE", "/api/config/config_entries/entry/..", ""), disp: reqmap.Unmappable, row: "rest DELETE /api/config/config_entries/entry/{entry_id}", refused: "fail-closed: unmappable request"},
		{name: "a dot segment", req: rest("POST", "/api/services/./turn_on", "{}"), disp: reqmap.Unmappable, row: "rest POST /api/services/{domain}/{service}", refused: "fail-closed: unmappable request"},
		{name: "an encoded dot-dot", req: rest("POST", "/api/services/%2E%2E/turn_on", "{}"), disp: reqmap.Unmappable, row: "rest POST /api/services/{domain}/{service}", refused: "fail-closed: unmappable request"},
		{name: "an encoded slash", req: rest("POST", "/api/services/light%2Fx/turn_on", "{}"), disp: reqmap.Unmappable, row: "rest POST /api/services/{domain}/{service}", refused: "fail-closed: unmappable request"},
		{name: "a lower-case encoded slash", req: rest("POST", "/api/services/light%2fx/turn_on", "{}"), disp: reqmap.Unmappable, row: "rest POST /api/services/{domain}/{service}", refused: "fail-closed: unmappable request"},
		{name: "an encoded backslash", req: rest("POST", "/api/config/config_entries/flow/a%5Cb", "{}"), disp: reqmap.Unmappable, row: "rest POST /api/config/config_entries/flow/{flow_id}", refused: "fail-closed: unmappable request"},
		{name: "a placeholder absorbing two segments", req: rest("POST", "/api/services/light/turn_on/extra", "{}"), disp: reqmap.Unmappable, row: "rest POST /api/services/{domain}/{service}", refused: "fail-closed: unmappable request"},
		{name: "a placeholder absorbing none", req: rest("POST", "/api/services/light", "{}"), disp: reqmap.Unmappable, row: "rest POST /api/services/{domain}/{service}", refused: "fail-closed: unmappable request"},
		{name: "an empty placeholder", req: rest("POST", "/api/config/config_entries/options/flow/", "{}"), disp: reqmap.Unmappable, row: "rest POST /api/config/config_entries/options/flow/{flow_id}", refused: "fail-closed: unmappable request"},
		{name: "a GET that runs a flow step keeps its write row's mutating answer", req: rest("GET", "/api/config/config_entries/flow/..", ""), disp: reqmap.Unmappable, row: "rest GET /api/config/config_entries/flow/{flow_id}", refused: "fail-closed: unmappable request"},
		{name: "a supervisor endpoint with a dot-dot slug", req: ws("supervisor/api", map[string]any{"endpoint": "/addons/../info", "method": "get"}), disp: reqmap.Unmappable, row: "ws supervisor/api GET /addons/{slug}/info", refused: "fail-closed: unmappable request"},
		{name: "a path without a leading slash", req: rest("POST", "api/services/light/turn_on", "{}"), disp: reqmap.Unmappable, row: "rest POST /api/services/{domain}/{service}", refused: "fail-closed: unmappable request"},
		{name: "a raw backslash in a write", req: rest("POST", `/api/config/config_entries/flow/a\b`, "{}"), disp: reqmap.Unmappable, row: "rest POST /api/config/config_entries/flow/{flow_id}", refused: "fail-closed: unmappable request"},
		// A path that breaks R-8 and resembles no row keeps its method's own
		// answer: anything but GET is mutating, so it is refused, never sent
		// as an unmapped read.
		{name: "a POST that breaks R-8 and resembles no row", req: rest("POST", "/api/events/../services/light/turn_on", "{}"), disp: reqmap.Unmappable, refused: "fail-closed: unmappable request rest POST"},
		{name: "a DELETE with an encoded slash that resembles no row", req: rest("DELETE", "/api/foo%2Fbar", ""), disp: reqmap.Unmappable, refused: "fail-closed: unmappable request rest DELETE"},
		{name: "a supervisor endpoint that breaks R-8 and resembles no row", req: ws("supervisor/api", map[string]any{"endpoint": "/foo/../bar", "method": "get"}), disp: reqmap.Unmappable, refused: "fail-closed: unmappable request ws supervisor/api"},
	} {
		checkVector(t, v)
	}
	// An unmappable read is not refused in phase 2 (reads are not enforced),
	// but it is still unmappable.
	got := reqmap.Classify(rest("GET", "/api/states/..", ""), nil)
	if got.Disposition != reqmap.Unmappable || got.Refused() {
		t.Errorf("GET /api/states/..: %+v, want unmappable and not refused", got)
	}
	// The query is not part of the path rule: `?return_response=true` keeps
	// the row.
	if got := reqmap.Classify(rest("POST", "/api/services/notify/notify?return_response=true", `{"message":"x"}`), &reqmap.Facts{Live: &reqmap.LiveStates{}}); got.Row != "rest POST /api/services/{domain}/{service}" || got.Disposition != reqmap.Write {
		t.Errorf("a query string changed the row: %+v", got)
	}
}

// TestMatchTemplate — the gate keys a concatenated source path by its shape;
// the placeholder names are the row's, not the source's.
func TestMatchTemplate(t *testing.T) {
	for _, c := range []struct {
		tr           reqmap.Transport
		method, tmpl string
		want         string
		ok           bool
	}{
		{reqmap.REST, "POST", "/api/services/{d}/{s}?return_response=true", "rest POST /api/services/{domain}/{service}", true},
		{reqmap.REST, "get", "/api/states/{entityID}", "rest GET /api/states/{entity_id}", true},
		{reqmap.REST, "GET", "/api/states", "rest GET /api/states", true},
		{reqmap.REST, "GET", "/api/{x}", "", false},
		{reqmap.REST, "DELETE", "/api/states/{e}", "", false},
		{reqmap.WS, "GET", "/addons/{s}/info", "ws supervisor/api GET /addons/{slug}/info", true},
		{reqmap.Companion, "PUT", "/v1/config/script", "companion PUT /v1/config/script", true},
		{reqmap.Companion, "PUT", "/v1/config/file", "", false},
	} {
		got, ok := reqmap.MatchTemplate(c.tr, c.method, c.tmpl)
		if got != c.want || ok != c.ok {
			t.Errorf("MatchTemplate(%s, %s, %s) = %q, %v; want %q, %v", c.tr, c.method, c.tmpl, got, ok, c.want, c.ok)
		}
	}
}

// TestKindWideChecksRenderAsStar — D-75: a kind-wide check is reported as
// `<kind>:*` and is flagged, never passed off as a concrete resource.
func TestKindWideChecksRenderAsStar(t *testing.T) {
	got := reqmap.Classify(ws("config/area_registry/create", map[string]any{"name": "x"}), nil)
	if len(got.Checks) != 1 {
		t.Fatalf("area create: %d checks, want 1", len(got.Checks))
	}
	c := got.Checks[0]
	if !c.KindWide || c.Target() != "area:*" || c.Resource.Kind != policy.KindArea || c.Resource.ID != "" || c.Verb != policy.Create {
		t.Errorf("area create check = %+v (%s), want a kind-wide create area:*", c, c.Target())
	}
	got = reqmap.Classify(rest("GET", "/api/config/config_entries/options/flow/F", ""), nil)
	if len(got.Checks) != 1 || got.Checks[0].Target() != "config_entry:*/options" || !got.Checks[0].KindWide {
		t.Errorf("options flow GET without a handler = %q, want kind-wide config_entry:*/options", got.Strings())
	}
}

// TestRefReplaceChecks — D-77, R-3: every location is an edit on the file, a
// typed owner adds an edit on its config, and a rewritten id is a rename: edit
// the old, create the new.
func TestRefReplaceChecks(t *testing.T) {
	got, err := reqmap.RefReplaceChecks([]reqmap.PlanChange{
		{Location: "automations.yaml", Path: "[0].id", Owner: &reqmap.TypedOwner{Kind: policy.KindAutomation, ID: "old_light", NewID: "new_light"}},
		{Location: "automations.yaml", Path: "[1].actions[0].entity_id", Owner: &reqmap.TypedOwner{Kind: policy.KindAutomation, ID: "a2", NewID: "a2"}},
		{Location: "template.yaml", Path: "[0].sensor[0].unique_id", Owner: &reqmap.TypedOwner{Kind: policy.KindTemplate, ID: "t_old", NewID: "t_new"}},
		{Location: "packages/x.yaml", Path: "input_boolean.x.name"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s := make([]string, 0, len(got))
	for _, c := range got {
		s = append(s, c.String())
	}
	want := []string{
		"edit automation:a2/config", "edit automation:old_light/config",
		"edit file:automations.yaml", "edit file:packages/x.yaml", "edit file:template.yaml",
		"edit template:t_old/config",
		"create automation:new_light", "create template:t_new",
	}
	if !slices.Equal(s, want) {
		t.Errorf("RefReplaceChecks\n  got  %q\n  want %q", s, want)
	}
	if _, err := reqmap.RefReplaceChecks([]reqmap.PlanChange{{Location: "../secrets.yaml"}}); err == nil {
		t.Error("a location with a dot-dot segment mapped; it must be unmappable (D-67)")
	}
	// The owner kind is checked by name, not left to the grammar: a helper's
	// or a file's `<id>/config` parses, so only the kind check refuses it.
	for _, owner := range []reqmap.TypedOwner{
		{Kind: policy.KindEntity, ID: "light.x"},
		{Kind: policy.KindHelper, ID: "input_boolean/x"},
		{Kind: policy.KindFile, ID: "x"},
	} {
		if got, err := reqmap.RefReplaceChecks([]reqmap.PlanChange{{Location: "a.yaml", Owner: &owner}}); err == nil {
			t.Errorf("a %s as a typed owner mapped to %v; only automation, script and template own file entries", owner.Kind, got)
		}
	}
}

// TestRefReplaceUsesTheLatestPlan — two checked dry runs of the same {old,
// new}: the confirmed run is mapped through the later one, which is what the
// file looked like last (D-77).
func TestRefReplaceUsesTheLatestPlan(t *testing.T) {
	plans := []reqmap.RefReplacePlan{
		{Old: "a", New: "b", Changes: []reqmap.PlanChange{{Location: "x.yaml"}}},
		{Old: "a", New: "b", Changes: []reqmap.PlanChange{{Location: "y.yaml"}}},
	}
	got := reqmap.Classify(comp("POST", "/v1/ref/replace", `{"old":"a","new":"b","dry_run":false}`), &reqmap.Facts{RefReplacePlans: plans})
	if want := []string{"edit file:y.yaml"}; got.Disposition != reqmap.Write || !slices.Equal(got.Strings(), want) {
		t.Errorf("confirmed ref/replace after two dry runs: %s %q, want write %q", got.Disposition, got.Strings(), want)
	}
}

// TestLocationsOutsidePlan — what a confirmed ref/replace wrote outside its
// checked dry run is reported after the fact (D-77).
func TestLocationsOutsidePlan(t *testing.T) {
	checked := []reqmap.PlanChange{{Location: "a.yaml"}, {Location: "b.yaml"}}
	written := []reqmap.PlanChange{{Location: "b.yaml"}, {Location: "z.yaml"}, {Location: "c.yaml"}, {Location: "z.yaml"}}
	if got := reqmap.LocationsOutsidePlan(checked, written); !slices.Equal(got, []string{"c.yaml", "z.yaml"}) {
		t.Errorf("LocationsOutsidePlan = %q, want [c.yaml z.yaml]", got)
	}
	if got := reqmap.LocationsOutsidePlan(checked, checked); len(got) != 0 {
		t.Errorf("a confirmed run inside its plan reported %q", got)
	}
}

// TestADryRunIsMappedAsTheRealWrite — D-74: a companion dry run is checked like
// the write it previews; the flag is never what decides.
func TestADryRunIsMappedAsTheRealWrite(t *testing.T) {
	for _, path := range []string{
		"/v1/config/automation?id=a1&dry_run=true",
		"/v1/config/automation?id=a1&dry_run=false",
		"/v1/config/automation?id=a1",
	} {
		got := reqmap.Classify(comp("PUT", path, "id: a1\n"), nil)
		if !slices.Equal(got.Strings(), []string{"edit automation:a1/config"}) {
			t.Errorf("PUT %s: %q, want the write's own check", path, got.Strings())
		}
	}
	plan := []reqmap.RefReplacePlan{{Old: "a", New: "b", Changes: []reqmap.PlanChange{{Location: "x.yaml"}}}}
	dry := reqmap.Classify(comp("POST", "/v1/ref/replace", `{"old":"a","new":"b","dry_run":true}`), &reqmap.Facts{RefReplacePlans: plan})
	wet := reqmap.Classify(comp("POST", "/v1/ref/replace", `{"old":"a","new":"b","dry_run":false}`), &reqmap.Facts{RefReplacePlans: plan})
	if !slices.Equal(dry.Strings(), wet.Strings()) || len(wet.Checks) == 0 {
		t.Errorf("ref/replace dry run %q and confirmed %q differ", dry.Strings(), wet.Strings())
	}
	if !dry.PlanFromResponse || wet.PlanFromResponse {
		t.Errorf("PlanFromResponse: dry %v, confirmed %v; want true, false", dry.PlanFromResponse, wet.PlanFromResponse)
	}
	bad := reqmap.Classify(comp("POST", "/v1/ref/replace", `{"old":"a","new":"b","dry_run":"no"}`), nil)
	if !bad.Refused() {
		t.Errorf("a non-boolean dry_run was not refused: %+v", bad)
	}
}
