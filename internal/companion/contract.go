package companion

// Endpoint describes one companion REST operation the client (client.go) calls,
// together with the Go value its JSON response is decoded into.
//
// It is the single source of truth for both contract sweeps, so a new client
// call is added here — and only here — to be covered by both:
//   - the (method,path) presence check against the vendored spec, in the
//     companion tier (internal/companiontest/contract_test.go); and
//   - the field-level struct↔spec conformance check, in the unit tier
//     (contract_conformance_test.go).
//
// Response holds a zero pointer to the decode target. It is nil for an operation
// whose response body the client intentionally discards (only the transport
// error is consulted) — see the reload endpoint below.
type Endpoint struct {
	Method   string
	Path     string
	Response any
}

// Endpoints is every companion operation the client calls, kept in lockstep with
// the methods on *Client. Keeping the decode target beside each (method,path)
// is what lets the conformance test reject a documented response field that no
// Go struct decodes — the class of drift (D45's dropped `reloaded`) that a
// path-only contract cannot see.
var Endpoints = []Endpoint{
	{"GET", "/v1/health", &HealthResponse{}},
	{"GET", "/v1/status", &StatusResponse{}},
	{"GET", "/v1/config/files", &ConfigFilesResponse{}},
	{"GET", "/v1/config/file", &ConfigFileResponse{}},
	{"GET", "/v1/config/block", &ConfigBlockResponse{}},
	{"GET", "/v1/config/wiring", &WiringResponse{}},
	{"GET", "/v1/related/entity", &RelatedEntityResponse{}},
	{"GET", "/v1/ref/scan", &RefScanResponse{}},
	{"GET", "/v1/ref/grep", &RefGrepResponse{}},
	{"GET", "/v1/ref/entities", &RefEntitiesResponse{}},
	{"POST", "/v1/ref/replace", &RefReplaceResponse{}},
	{"GET", "/v1/config/templates", &TemplatesResponse{}},
	{"GET", "/v1/config/template", &TemplateResponse{}},
	{"POST", "/v1/config/template", &TemplateCreateResponse{}},
	{"DELETE", "/v1/config/template", &ConfigDeleteResponse{}},
	{"GET", "/v1/config/script", &ScriptResponse{}},
	{"PUT", "/v1/config/script", &ConfigDeleteResponse{}},
	{"POST", "/v1/config/script", &ScriptCreateResponse{}},
	{"DELETE", "/v1/config/script", &ConfigDeleteResponse{}},
	{"GET", "/v1/config/automation", &AutomationResponse{}},
	{"PUT", "/v1/config/automation", &ConfigDeleteResponse{}},
	{"POST", "/v1/config/automation", &AutomationCreateResponse{}},
	{"DELETE", "/v1/config/automation", &ConfigDeleteResponse{}},
	{"GET", "/v1/config/helpers", &HelpersResponse{}},
	{"GET", "/v1/config/helper", &HelperResponse{}},
	{"POST", "/v1/config/helper", &HelperCreateResponse{}},
	{"DELETE", "/v1/config/helper", &ConfigDeleteResponse{}},
	{"POST", "/v1/ha/check-config", &CheckConfigResponse{}},
	{"POST", "/v1/wireguard/config", &WireGuardActionResponse{}},
	{"POST", "/v1/wireguard/start", &WireGuardActionResponse{}},
	{"POST", "/v1/wireguard/stop", &WireGuardActionResponse{}},
	{"GET", "/v1/wireguard/status", &WireGuardStatusResponse{}},
	{"GET", "/v1/logs", &LogsResponse{}},
}

// NeverCalledSpecPaths are the vendored spec's routes hactl deliberately never
// calls (D-81). Their client methods had no product caller and were deleted in
// phase 2 of the permission policy: a method nobody calls is a policy mapping
// nobody exercises. The routes stay in the companion and in the vendored spec,
// so H-3's spec drift is unaffected. A path listed here that the client calls
// again, or that leaves the spec, fails TestEndpointsCoverSpecPaths. It sits
// beside Endpoints so the unit tier and the companion tier
// (TestSpecPathCountMatchesClient) read one list, never two (TC-7).
var NeverCalledSpecPaths = map[string]string{
	"/v1/config/scripts":     "no command lists the companion's script definitions; one is read with GET /v1/config/script",
	"/v1/config/automations": "no command lists the companion's automation definitions; one is read with GET /v1/config/automation",
	"/v1/ha/reload/{domain}": "the companion reloads inside its own write routes, and the reloads hactl sends itself are HA service calls",
}
