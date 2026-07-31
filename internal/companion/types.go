package companion

// HealthResponse is the response from GET /v1/health.
type HealthResponse struct {
	Status  string `json:"status"`
	Version string `json:"version"`
}

// StatusResponse is the response from GET /v1/status.
type StatusResponse struct {
	Version             string `json:"version"`
	SupervisorReachable bool   `json:"supervisor_reachable"`
	HasHACLI            bool   `json:"has_ha_cli"`
	ConfigWritable      bool   `json:"config_writable"`
	IngressActive       bool   `json:"ingress_active"`
	AuthMode            string `json:"auth_mode"`
}

// ConfigFilesResponse is the response from GET /v1/config/files.
type ConfigFilesResponse struct {
	Files []string `json:"files"`
}

// ConfigFileResponse is the response from GET /v1/config/file.
type ConfigFileResponse struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ConfigBlockResponse is the response from GET /v1/config/block.
type ConfigBlockResponse struct {
	Path    string `json:"path"`
	ID      string `json:"id"`
	Content string `json:"content"`
}

// ConfigWriteResponse is the response from PUT /v1/config/file.
type ConfigWriteResponse struct {
	Status string `json:"status"`
	Diff   string `json:"diff,omitempty"`
	// Validated reports whether the companion re-validated HA's config after the
	// write (false when validation was skipped or unavailable). Decoded so the
	// field-conformance contract (T3) stays whole rather than ignore-listed.
	Validated bool   `json:"validated,omitempty"`
	Backup    string `json:"backup,omitempty"`
}

// RelatedEntityResponse is the response from GET /v1/related/entity.
// Stale and StaleRefs are populated only when the request passes stale=true and
// the entity is absent from the on-disk registry snapshot.
type RelatedEntityResponse struct {
	EntityID  string               `json:"entity_id"`
	Stale     bool                 `json:"stale"`
	Related   []RelatedEntityEntry `json:"related"`
	StaleRefs []StaleRef           `json:"stale_refs"`
}

// StaleRef is one config location where a stale entity_id is still referenced.
type StaleRef struct {
	Location     string `json:"location"`      // config-relative file, e.g. "automations.yaml"
	Path         string `json:"path"`          // path within that file, e.g. "[3].trigger[0].entity_id"
	MatchedValue string `json:"matched_value"` // the literal entity_id found
}

// RelatedEntityEntry is one related entity edge returned by the companion graph.
type RelatedEntityEntry struct {
	EntityID     string `json:"entity_id"`
	Relationship string `json:"relationship"`
	Detail       string `json:"detail"`
}

// RefScanResponse is the response from GET /v1/ref/scan.
type RefScanResponse struct {
	Target  string        `json:"target"`
	Hits    []RefScanHit  `json:"hits"`
	Skipped []SkippedFile `json:"skipped,omitempty"`
}

// SkippedFile is one config file a companion walk could not read, so its result
// covers less than the config does — a renamed `!include` target, a file the
// path guard refuses, an `!include_dir_*` naming a directory that is not there.
//
// Decoded on every route that reports it, like Validated and Reloaded before it,
// so the field-level contract (H-13) stays whole rather than ignore-listed: a
// documented field no struct decodes is the D45 shape, and the ignore list is
// empty by design. No command renders it yet — a caller certifying something
// about the *whole* config (no dangling references, a completed rename) must
// read any entry here as "cannot certify", and giving that its own reporting is
// a separate change from this one.
type SkippedFile struct {
	Location string `json:"location"`
	Reason   string `json:"reason"`
}

// RefScanHit is one literal reference found in a config file, reported against
// the file it actually lives in (mirrors the Go jsonwalk hit shape).
type RefScanHit struct {
	Location     string `json:"location"`
	Path         string `json:"path"`
	MatchedValue string `json:"matched_value"`
}

// RefEntitiesResponse is the response from GET /v1/ref/entities: every
// entity_id-shaped leaf across the config !include graph, unfiltered.
type RefEntitiesResponse struct {
	Entities []RefEntity   `json:"entities"`
	Skipped  []SkippedFile `json:"skipped,omitempty"`
}

// RefEntity is one entity_id-shaped value found in a config file. Unlike
// RefScanHit it carries Key — the nearest enclosing mapping key — so the caller
// can tell a true entity position (entity_id/entity) from a same-shaped service
// name (service: light.turn_on).
type RefEntity struct {
	Location     string `json:"location"`
	Path         string `json:"path"`
	Key          string `json:"key"`
	MatchedValue string `json:"matched_value"`
}

// RefReplaceResponse is the response from POST /v1/ref/replace.
type RefReplaceResponse struct {
	Status  string        `json:"status"` // "dry_run" | "applied"
	Changes []RefChange   `json:"changes"`
	Skipped []SkippedFile `json:"skipped,omitempty"`
}

// RefChange is one literal rewritten (or, in dry-run, that would be rewritten)
// in a config file.
type RefChange struct {
	Location string `json:"location"`
	Path     string `json:"path"`
	Before   string `json:"before"`
	After    string `json:"after"`
}

// TemplateDefinition represents a template sensor definition.
type TemplateDefinition struct {
	UniqueID          string `json:"unique_id"`
	Name              string `json:"name"`
	Domain            string `json:"domain"`
	State             string `json:"state"`
	UnitOfMeasurement string `json:"unit_of_measurement,omitempty"`
	DeviceClass       string `json:"device_class,omitempty"`
	// Trigger is true when the entity lives in a trigger-based `template:` block.
	Trigger bool `json:"trigger,omitempty"`
}

// TemplatesResponse is the response from GET /v1/config/templates.
type TemplatesResponse struct {
	Templates []TemplateDefinition `json:"templates"`
}

// TemplateResponse is the response from GET /v1/config/template.
type TemplateResponse struct {
	UniqueID string `json:"unique_id"`
	Content  string `json:"content"`
	// Trigger is true when the entry is trigger-based (Content is then the whole
	// block rather than the bare entity item).
	Trigger bool `json:"trigger,omitempty"`
}

// TemplateCreateResponse is the response from POST /v1/config/template.
type TemplateCreateResponse struct {
	Status   string `json:"status"`
	UniqueID string `json:"unique_id"`
	Reloaded bool   `json:"reloaded"` // false when HA never loaded the new definition
	// ReloadError carries HA's own reason when Reloaded is false — its HTTP
	// status plus a bounded excerpt of the body, or the transport error class.
	// Absent on success. Decoded because a bare `reloaded: false` sends an
	// operator hunting for a reason the companion already had.
	ReloadError string `json:"reload_error,omitempty"`
}

// ScriptDefinition represents a script definition.
type ScriptDefinition struct {
	ID     string `json:"id"`
	Alias  string `json:"alias"`
	Mode   string `json:"mode"`
	Fields []any  `json:"fields,omitempty"`
}

// ScriptsResponse is the response from GET /v1/config/scripts.
type ScriptsResponse struct {
	Scripts []ScriptDefinition `json:"scripts"`
}

// ScriptResponse is the response from GET /v1/config/script.
type ScriptResponse struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// ScriptCreateResponse is the response from POST /v1/config/script.
type ScriptCreateResponse struct {
	Status   string `json:"status"`
	ID       string `json:"id"`
	Reloaded bool   `json:"reloaded"` // false when HA never loaded the new definition
	// ReloadError carries HA's own reason when Reloaded is false — its HTTP
	// status plus a bounded excerpt of the body, or the transport error class.
	// Absent on success. Decoded because a bare `reloaded: false` sends an
	// operator hunting for a reason the companion already had.
	ReloadError string `json:"reload_error,omitempty"`
}

// AutomationDefinition represents an automation definition.
type AutomationDefinition struct {
	ID          string `json:"id"`
	Alias       string `json:"alias"`
	Mode        string `json:"mode,omitempty"`
	Description string `json:"description,omitempty"`
}

// AutomationsResponse is the response from GET /v1/config/automations.
type AutomationsResponse struct {
	Automations []AutomationDefinition `json:"automations"`
}

// AutomationResponse is the response from GET /v1/config/automation.
type AutomationResponse struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// AutomationCreateResponse is the response from POST /v1/config/automation.
type AutomationCreateResponse struct {
	Status   string `json:"status"`
	ID       string `json:"id"`
	EntityID string `json:"entity_id"` // live entity_id, empty if HA never confirmed it
	Reloaded bool   `json:"reloaded"`
	// ReloadError carries HA's own reason when Reloaded is false — its HTTP
	// status plus a bounded excerpt of the body, or the transport error class.
	// Absent on success. Decoded because a bare `reloaded: false` sends an
	// operator hunting for a reason the companion already had.
	ReloadError string `json:"reload_error,omitempty"`
}

// CheckConfigResponse is the response from POST /v1/ha/check-config. Valid is a
// pointer so a companion that omits it (<= 2026.6.7, which returns only
// {"status":"ok"}) is distinguishable from one that reports valid=false.
type CheckConfigResponse struct {
	Status string `json:"status"`
	Valid  *bool  `json:"valid"`
	Errors string `json:"errors"`
}

// ConfigDeleteResponse is the acknowledgement shape shared by the PUT (update)
// and DELETE endpoints for templates, scripts, automations and helpers. Callers
// today consult only the transport error (success/failure); Diff and Reloaded
// are decoded so the field-level contract (T3) stays whole and — critically —
// so the `reloaded` flag is never again silently dropped, which is exactly the
// defect (D45) that let a "written but HA never reloaded" write read as success.
// Diff is populated only by the PUT dry-run responses; Reloaded only by the
// endpoints HA reloads after mutating.
type ConfigDeleteResponse struct {
	Status   string `json:"status"`
	Diff     string `json:"diff,omitempty"`
	Reloaded bool   `json:"reloaded,omitempty"`
	// ReloadError carries HA's own reason when Reloaded is false — its HTTP
	// status plus a bounded excerpt of the body, or the transport error class.
	// Absent on success. Decoded because a bare `reloaded: false` sends an
	// operator hunting for a reason the companion already had.
	ReloadError string `json:"reload_error,omitempty"`
}

// HelperDefinition represents a helper entity definition.
type HelperDefinition struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
	Icon   string `json:"icon,omitempty"`
}

// HelpersResponse is the response from GET /v1/config/helpers.
type HelpersResponse struct {
	Helpers []HelperDefinition `json:"helpers"`
}

// HelperResponse is the response from GET /v1/config/helper.
type HelperResponse struct {
	ID      string `json:"id"`
	Domain  string `json:"domain"`
	Content string `json:"content"`
	// Source is "yaml" for a helper defined in a file the companion manages and
	// "storage" for one created in HA's UI. It is what makes `helper show`
	// honest about a definition nothing here can edit, and what lets
	// `helper delete`'s preview refuse a target its --confirm cannot delete.
	Source string `json:"source"`
}

// WiringResponse is the response from GET /v1/config/wiring: whether a create
// for a domain would reach a file Home Assistant reads. Reason is the companion's
// own refusal text, so a preview built on it explains a failure exactly as the
// confirmed run would.
type WiringResponse struct {
	Domain string `json:"domain"`
	Wired  bool   `json:"wired"`
	File   string `json:"file,omitempty"`
	Reason string `json:"reason,omitempty"`
}

// HelperCreateResponse is the response from POST /v1/config/helper.
type HelperCreateResponse struct {
	Status        string `json:"status"`
	ID            string `json:"id"`
	EntityID      string `json:"entity_id"`
	Reloaded      bool   `json:"reloaded"`
	EntityCreated bool   `json:"entity_created"`
	// ReloadError carries HA's own reason when Reloaded is false — its HTTP
	// status plus a bounded excerpt of the body, or the transport error class.
	// Absent on success. Decoded because a bare `reloaded: false` sends an
	// operator hunting for a reason the companion already had.
	ReloadError string `json:"reload_error,omitempty"`
}

// --- WireGuard ---

// WireGuardStatusResponse is the response from GET /v1/wireguard/status.
// When the tunnel is inactive only Tunnel and State are populated.
type WireGuardStatusResponse struct {
	Tunnel    string            `json:"tunnel"`
	State     string            `json:"state"` // "active" | "inactive"
	Interface *WireGuardIface   `json:"interface,omitempty"`
	Peers     []WireGuardPeer   `json:"peers,omitempty"`
	Monitor   *WireGuardMonitor `json:"monitor,omitempty"`
}

// WireGuardIface holds the local interface details of an active tunnel.
type WireGuardIface struct {
	PublicKey     string `json:"public_key,omitempty"`
	ListeningPort int    `json:"listening_port,omitempty"`
}

// WireGuardPeer holds per-peer details from `wg show <tunnel> dump`. The
// *Secs/*Bytes fields are the raw numerics; the string fields are humanized.
type WireGuardPeer struct {
	PublicKey           string `json:"public_key,omitempty"`
	Endpoint            string `json:"endpoint,omitempty"`
	AllowedIPs          string `json:"allowed_ips,omitempty"`
	LatestHandshake     string `json:"latest_handshake,omitempty"`
	LatestHandshakeSecs *int   `json:"latest_handshake_secs,omitempty"`
	TransferRx          string `json:"transfer_rx,omitempty"`
	TransferTx          string `json:"transfer_tx,omitempty"`
	TransferRxBytes     int64  `json:"transfer_rx_bytes,omitempty"`
	TransferTxBytes     int64  `json:"transfer_tx_bytes,omitempty"`
}

// WireGuardMonitor reflects the companion's live dyndns re-resolution monitor.
type WireGuardMonitor struct {
	Running              bool              `json:"running"`
	Hostnames            []string          `json:"hostnames,omitempty"`
	Healthy              bool              `json:"healthy,omitempty"`
	Resolved             map[string]string `json:"resolved,omitempty"`
	LastCheckSecsAgo     *int              `json:"last_check_secs_ago,omitempty"`
	LastReresolveSecsAgo *int              `json:"last_reresolve_secs_ago,omitempty"`
	Attempt              int               `json:"attempt,omitempty"`
	NextRetrySecs        *int              `json:"next_retry_secs,omitempty"`
	LastError            string            `json:"last_error,omitempty"`
}

// WireGuardActionResponse is the response from config/start/stop.
type WireGuardActionResponse struct {
	Status string `json:"status"`
	Tunnel string `json:"tunnel"`
}

// --- Logs ---

// LogsResponse is the response from GET /v1/logs.
type LogsResponse struct {
	Entries []LogEntry `json:"entries"`
}

// LogEntry is a single captured companion log record. Ts is epoch seconds.
type LogEntry struct {
	Ts      float64 `json:"ts"`
	Level   string  `json:"level"`
	Name    string  `json:"name"`
	Message string  `json:"message"`
}

// LogsParams are the optional filters for GET /v1/logs.
type LogsParams struct {
	Component string // friendly alias (e.g. "wireguard") or logger-name substring
	Level     string // minimum severity
	Since     string // relative duration like "30m", "24h"
	Limit     int    // 0 = no limit
}
