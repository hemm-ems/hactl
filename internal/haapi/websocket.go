package haapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"sync"
	"sync/atomic"

	"github.com/gorilla/websocket"
)

// WSClient is a WebSocket client for the Home Assistant API.
//
// WS API source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/websocket_api/
// Auth flow: https://github.com/home-assistant/core/blob/dev/homeassistant/components/websocket_api/auth.py
type WSClient struct {
	conn    *websocket.Conn
	baseURL string
	token   string
	mu      sync.Mutex
	nextID  atomic.Int64
}

// NewWSClient creates a new WebSocket client for the given HA instance.
func NewWSClient(baseURL, token string) *WSClient {
	return &WSClient{
		baseURL: baseURL,
		token:   token,
	}
}

// Connect establishes the WebSocket connection and authenticates.
// Retries the connection once on failure.
func (ws *WSClient) Connect(ctx context.Context) error {
	err := ws.connect(ctx)
	if err != nil {
		slog.Warn("websocket connection failed, retrying once", "error", err)
		return ws.connect(ctx)
	}
	return nil
}

// Close closes the WebSocket connection.
func (ws *WSClient) Close() error {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if ws.conn != nil {
		return ws.conn.Close()
	}
	return nil
}

const errUnknown = "unknown error"

// TraceList returns all trace summaries for the given domain (e.g. "automation").
func (ws *WSClient) TraceList(ctx context.Context, domain string) (TraceListResult, error) {
	_ = ctx // context used for cancellation in future

	id := ws.nextID.Add(1)
	msg := map[string]any{
		"id":     id,
		"type":   "trace/list",
		"domain": domain,
	}

	ws.mu.Lock()
	err := ws.conn.WriteJSON(msg)
	ws.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("sending trace/list: %w", err)
	}

	var resp wsResponse
	if err := ws.conn.ReadJSON(&resp); err != nil {
		return nil, fmt.Errorf("reading trace/list response: %w", err)
	}
	if !resp.Success {
		errMsg := errUnknown
		if resp.Error != nil {
			errMsg = resp.Error.Message
		}
		return nil, fmt.Errorf("trace/list failed: %s", errMsg)
	}

	// HA returns a flat array of trace summaries: [{...}, {...}, ...].
	// Group them by "domain.item_id" to build the TraceListResult map.
	var flat []TraceSummary
	if err := json.Unmarshal(resp.Result, &flat); err != nil {
		return nil, fmt.Errorf("parsing trace list: %w", err)
	}

	result := make(TraceListResult, len(flat))
	for _, ts := range flat {
		key := ts.Domain + "." + ts.ItemID
		result[key] = append(result[key], ts)
	}

	return result, nil
}

// TraceGet returns the full trace detail for a specific trace run.
func (ws *WSClient) TraceGet(ctx context.Context, domain, itemID, runID string) (json.RawMessage, error) {
	_ = ctx

	id := ws.nextID.Add(1)
	msg := map[string]any{
		"id":      id,
		"type":    "trace/get",
		"domain":  domain,
		"item_id": itemID,
		"run_id":  runID,
	}

	ws.mu.Lock()
	err := ws.conn.WriteJSON(msg)
	ws.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("sending trace/get: %w", err)
	}

	var resp wsResponse
	if err := ws.conn.ReadJSON(&resp); err != nil {
		return nil, fmt.Errorf("reading trace/get response: %w", err)
	}
	if !resp.Success {
		errMsg := errUnknown
		if resp.Error != nil {
			errMsg = resp.Error.Message
		}
		return nil, fmt.Errorf("trace/get failed: %s", errMsg)
	}

	return resp.Result, nil
}

// CheckConfig calls the homeassistant/check_config service via WebSocket.
// Returns true if config is valid, false otherwise.
func (ws *WSClient) CheckConfig(ctx context.Context) (bool, error) {
	_ = ctx

	id := ws.nextID.Add(1)
	msg := map[string]any{
		"id":      id,
		"type":    "call_service",
		"domain":  "homeassistant",
		"service": "check_config",
	}

	ws.mu.Lock()
	err := ws.conn.WriteJSON(msg)
	ws.mu.Unlock()
	if err != nil {
		return false, fmt.Errorf("sending check_config: %w", err)
	}

	var resp wsResponse
	if err := ws.conn.ReadJSON(&resp); err != nil {
		return false, fmt.Errorf("reading check_config response: %w", err)
	}
	if !resp.Success {
		errMsg := errUnknown
		if resp.Error != nil {
			errMsg = resp.Error.Message
		}
		return false, fmt.Errorf("check_config failed: %s", errMsg)
	}

	return true, nil
}

// SystemLogList returns log entries from the system_log integration via WS.
func (ws *WSClient) SystemLogList(ctx context.Context) ([]SystemLogEntry, error) {
	_ = ctx

	id := ws.nextID.Add(1)
	msg := map[string]any{
		"id":   id,
		"type": "system_log/list",
	}

	ws.mu.Lock()
	err := ws.conn.WriteJSON(msg)
	ws.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("sending system_log/list: %w", err)
	}

	var resp wsResponse
	if err := ws.conn.ReadJSON(&resp); err != nil {
		return nil, fmt.Errorf("reading system_log/list response: %w", err)
	}
	if !resp.Success {
		errMsg := errUnknown
		if resp.Error != nil {
			errMsg = resp.Error.Message
		}
		return nil, fmt.Errorf("system_log/list failed: %s", errMsg)
	}

	var result []SystemLogEntry
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("parsing system log entries: %w", err)
	}

	return result, nil
}

// EntityRegistryList returns all entity registry entries via WS.
// WS command: config/entity_registry/list
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/entity_registry.py
func (ws *WSClient) EntityRegistryList(ctx context.Context) ([]EntityRegistryEntry, error) {
	result, err := ws.sendCommand(ctx, "config/entity_registry/list", nil)
	if err != nil {
		return nil, err
	}
	var entries []EntityRegistryEntry
	if err := json.Unmarshal(result, &entries); err != nil {
		return nil, fmt.Errorf("parsing entity registry: %w", err)
	}
	return entries, nil
}

// AreaRegistryList returns all area (room) entries via WS.
// WS command: config/area_registry/list
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/area_registry.py
func (ws *WSClient) AreaRegistryList(ctx context.Context) ([]AreaEntry, error) {
	result, err := ws.sendCommand(ctx, "config/area_registry/list", nil)
	if err != nil {
		return nil, err
	}
	var entries []AreaEntry
	if err := json.Unmarshal(result, &entries); err != nil {
		return nil, fmt.Errorf("parsing area registry: %w", err)
	}
	return entries, nil
}

// LabelRegistryList returns all label entries via WS.
// WS command: config/label_registry/list
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/label_registry.py
func (ws *WSClient) LabelRegistryList(ctx context.Context) ([]LabelEntry, error) {
	result, err := ws.sendCommand(ctx, "config/label_registry/list", nil)
	if err != nil {
		return nil, err
	}
	var entries []LabelEntry
	if err := json.Unmarshal(result, &entries); err != nil {
		return nil, fmt.Errorf("parsing label registry: %w", err)
	}
	return entries, nil
}

// FloorRegistryList returns all floor entries via WS.
// WS command: config/floor_registry/list
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/floor_registry.py
func (ws *WSClient) FloorRegistryList(ctx context.Context) ([]FloorEntry, error) {
	result, err := ws.sendCommand(ctx, "config/floor_registry/list", nil)
	if err != nil {
		return nil, err
	}
	var entries []FloorEntry
	if err := json.Unmarshal(result, &entries); err != nil {
		return nil, fmt.Errorf("parsing floor registry: %w", err)
	}
	return entries, nil
}

// UserList returns all HA user accounts via WS.
//
// WS command: config/auth/list (admin-only — non-admin tokens get APIError{Code:"unauthorized"}).
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/auth.py
func (ws *WSClient) UserList(ctx context.Context) ([]UserEntry, error) {
	result, err := ws.sendCommand(ctx, "config/auth/list", nil)
	if err != nil {
		return nil, err
	}
	var entries []UserEntry
	if err := json.Unmarshal(result, &entries); err != nil {
		return nil, fmt.Errorf("parsing user list: %w", err)
	}
	return entries, nil
}

// DeviceRegistryList returns all device registry entries via WS.
// WS command: config/device_registry/list
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/device_registry.py
func (ws *WSClient) DeviceRegistryList(ctx context.Context) ([]DeviceRegistryEntry, error) {
	result, err := ws.sendCommand(ctx, "config/device_registry/list", nil)
	if err != nil {
		return nil, err
	}
	var entries []DeviceRegistryEntry
	if err := json.Unmarshal(result, &entries); err != nil {
		return nil, fmt.Errorf("parsing device registry: %w", err)
	}
	return entries, nil
}

// EntityRegistryUpdate updates an entity registry entry (area, labels, etc.) via WS.
// WS command: config/entity_registry/update
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/entity_registry.py
func (ws *WSClient) EntityRegistryUpdate(ctx context.Context, entityID string, changes map[string]any) error {
	params := map[string]any{"entity_id": entityID}
	maps.Copy(params, changes)
	_, err := ws.sendCommand(ctx, "config/entity_registry/update", params)
	return err
}

// LabelRegistryCreate creates a new label in the HA label registry.
// WS command: config/label_registry/create
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/label_registry.py
func (ws *WSClient) LabelRegistryCreate(ctx context.Context, name, color, icon, description string) (*LabelEntry, error) {
	params := map[string]any{"name": name}
	if color != "" {
		params["color"] = color
	}
	if icon != "" {
		params["icon"] = icon
	}
	if description != "" {
		params["description"] = description
	}
	result, err := ws.sendCommand(ctx, "config/label_registry/create", params)
	if err != nil {
		return nil, err
	}
	var entry LabelEntry
	if err := json.Unmarshal(result, &entry); err != nil {
		return nil, fmt.Errorf("parsing created label: %w", err)
	}
	return &entry, nil
}

// LabelRegistryDelete deletes a label from the HA label registry.
// WS command: config/label_registry/delete
func (ws *WSClient) LabelRegistryDelete(ctx context.Context, labelID string) error {
	params := map[string]any{"label_id": labelID}
	_, err := ws.sendCommand(ctx, "config/label_registry/delete", params)
	return err
}

// AreaRegistryCreate creates a new area in the HA area registry.
// WS command: config/area_registry/create
func (ws *WSClient) AreaRegistryCreate(ctx context.Context, name, icon string, floorID string) (*AreaEntry, error) {
	params := map[string]any{"name": name}
	if icon != "" {
		params["icon"] = icon
	}
	if floorID != "" {
		params["floor_id"] = floorID
	}
	result, err := ws.sendCommand(ctx, "config/area_registry/create", params)
	if err != nil {
		return nil, err
	}
	var entry AreaEntry
	if err := json.Unmarshal(result, &entry); err != nil {
		return nil, fmt.Errorf("parsing created area: %w", err)
	}
	return &entry, nil
}

// AreaRegistryDelete deletes an area from the HA area registry.
// WS command: config/area_registry/delete
func (ws *WSClient) AreaRegistryDelete(ctx context.Context, areaID string) error {
	params := map[string]any{"area_id": areaID}
	_, err := ws.sendCommand(ctx, "config/area_registry/delete", params)
	return err
}

// FloorRegistryCreate creates a new floor in the HA floor registry.
// WS command: config/floor_registry/create
func (ws *WSClient) FloorRegistryCreate(ctx context.Context, name, icon string, level *int) (*FloorEntry, error) {
	params := map[string]any{"name": name}
	if icon != "" {
		params["icon"] = icon
	}
	if level != nil {
		params["level"] = *level
	}
	result, err := ws.sendCommand(ctx, "config/floor_registry/create", params)
	if err != nil {
		return nil, err
	}
	var entry FloorEntry
	if err := json.Unmarshal(result, &entry); err != nil {
		return nil, fmt.Errorf("parsing created floor: %w", err)
	}
	return &entry, nil
}

// FloorRegistryDelete deletes a floor from the HA floor registry.
// WS command: config/floor_registry/delete
func (ws *WSClient) FloorRegistryDelete(ctx context.Context, floorID string) error {
	params := map[string]any{"floor_id": floorID}
	_, err := ws.sendCommand(ctx, "config/floor_registry/delete", params)
	return err
}

// DashboardList returns all Lovelace dashboard entries.
// WS command: lovelace/dashboards/list
func (ws *WSClient) DashboardList(ctx context.Context) ([]LovelaceDashboard, error) {
	result, err := ws.sendCommand(ctx, "lovelace/dashboards/list", nil)
	if err != nil {
		return nil, err
	}
	var entries []LovelaceDashboard
	if err := json.Unmarshal(result, &entries); err != nil {
		return nil, fmt.Errorf("parsing dashboard list: %w", err)
	}
	return entries, nil
}

// DashboardConfig returns the parsed config for a dashboard.
// An empty urlPath targets the default dashboard.
// WS command: lovelace/config
func (ws *WSClient) DashboardConfig(ctx context.Context, urlPath string) (*LovelaceConfig, error) {
	raw, err := ws.DashboardConfigRaw(ctx, urlPath)
	if err != nil {
		return nil, err
	}
	var cfg LovelaceConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parsing dashboard config: %w", err)
	}
	return &cfg, nil
}

// DashboardConfigRaw returns the raw JSON config for a dashboard.
// An empty urlPath targets the default dashboard.
// WS command: lovelace/config
func (ws *WSClient) DashboardConfigRaw(ctx context.Context, urlPath string) (json.RawMessage, error) {
	params := map[string]any{}
	if urlPath != "" {
		params["url_path"] = urlPath
	}
	return ws.sendCommand(ctx, "lovelace/config", params)
}

// DashboardConfigSave saves a full dashboard config (storage mode only).
// An empty urlPath targets the default dashboard.
// WS command: lovelace/config/save
func (ws *WSClient) DashboardConfigSave(ctx context.Context, urlPath string, config json.RawMessage) error {
	var parsed any
	if err := json.Unmarshal(config, &parsed); err != nil {
		return fmt.Errorf("invalid config JSON: %w", err)
	}
	params := map[string]any{"config": parsed}
	if urlPath != "" {
		params["url_path"] = urlPath
	}
	_, err := ws.sendCommand(ctx, "lovelace/config/save", params)
	return err
}

// DashboardCreate creates a new storage-mode dashboard.
// WS command: lovelace/dashboards/create
func (ws *WSClient) DashboardCreate(ctx context.Context, p DashboardCreateParams) (*LovelaceDashboard, error) {
	params := map[string]any{
		"url_path":        p.URLPath,
		"title":           p.Title,
		"require_admin":   p.RequireAdmin,
		"show_in_sidebar": p.ShowInSidebar,
	}
	if p.Icon != "" {
		params["icon"] = p.Icon
	}
	result, err := ws.sendCommand(ctx, "lovelace/dashboards/create", params)
	if err != nil {
		return nil, err
	}
	var entry LovelaceDashboard
	if err := json.Unmarshal(result, &entry); err != nil {
		return nil, fmt.Errorf("parsing created dashboard: %w", err)
	}
	return &entry, nil
}

// DashboardDelete deletes a dashboard by its ID.
// WS command: lovelace/dashboards/delete
func (ws *WSClient) DashboardDelete(ctx context.Context, dashboardID string) error {
	params := map[string]any{"dashboard_id": dashboardID}
	_, err := ws.sendCommand(ctx, "lovelace/dashboards/delete", params)
	return err
}

// LovelaceInfo returns lovelace system information.
// WS command: lovelace/info
func (ws *WSClient) LovelaceInfo(ctx context.Context) (*LovelaceInfo, error) {
	result, err := ws.sendCommand(ctx, "lovelace/info", nil)
	if err != nil {
		return nil, err
	}
	var info LovelaceInfo
	if err := json.Unmarshal(result, &info); err != nil {
		return nil, fmt.Errorf("parsing lovelace info: %w", err)
	}
	return &info, nil
}

// IntegrationManifest holds the subset of an integration manifest we care about.
type IntegrationManifest struct {
	Domain    string `json:"domain"`
	Name      string `json:"name"`
	Version   string `json:"version,omitempty"`
	IsBuiltIn bool   `json:"is_built_in"`
}

// IntegrationManifestList returns all loaded integration manifests via WS.
// WS command: manifest/list
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/integration.py
func (ws *WSClient) IntegrationManifestList(ctx context.Context) ([]IntegrationManifest, error) {
	result, err := ws.sendCommand(ctx, "manifest/list", nil)
	if err != nil {
		return nil, err
	}
	var entries []IntegrationManifest
	if err := json.Unmarshal(result, &entries); err != nil {
		return nil, fmt.Errorf("parsing manifest list: %w", err)
	}
	return entries, nil
}

// SupervisorAPI invokes a Supervisor REST endpoint via the HA WS
// `supervisor/api` proxy. method is "get" / "post" / etc. data is the request
// body for write methods (nil for reads). Returns the raw `result` payload
// from Supervisor.
//
// Only available on HA OS / Supervised installations — HA Container has no
// Supervisor and HA Core returns `unknown_command` for `supervisor/api`.
//
// HA core ref: homeassistant/components/hassio/const.py — `WS_TYPE_API =
// "supervisor/api"`. The legacy name `hassio/api` does not exist; the
// component registers commands under the `supervisor/*` namespace.
func (ws *WSClient) SupervisorAPI(ctx context.Context, endpoint, method string, data map[string]any) (json.RawMessage, error) {
	if method == "" {
		method = "get"
	}
	params := map[string]any{
		"endpoint": endpoint,
		"method":   method,
	}
	if data != nil {
		params["data"] = data
	}
	return ws.sendCommand(ctx, "supervisor/api", params)
}

// IngressSession requests a new HA Ingress session token from Supervisor.
// Returns the opaque session string to set as `ingress_session` cookie on
// subsequent Ingress HTTP calls (`/api/hassio_ingress/<addonToken>/…`).
//
// HA's `/api/hassio_ingress/*` route is `requires_auth = False` at the Core
// level — Core proxies straight to Supervisor, and Supervisor enforces auth
// by checking the `ingress_session` cookie against sessions it issued.
// Long-lived tokens, signed URLs (`auth/sign_path`), and Bearer auth all
// fail because they never reach Supervisor's session validator.
//
// Sessions tie to the requesting user and expire after a short window
// (Supervisor refreshes them on access). Cache the token; on 401, request a
// new one and retry once.
//
// HA core ref: components/hassio/ingress.py (requires_auth=False),
// components/hassio/websocket_api.py:websocket_supervisor_api (injects
// session_data_user_id when endpoint == "/ingress/session").
func (ws *WSClient) IngressSession(ctx context.Context) (string, error) {
	result, err := ws.SupervisorAPI(ctx, "/ingress/session", "post", nil)
	if err != nil {
		return "", err
	}
	var resp struct {
		Session string `json:"session"`
	}
	if err := json.Unmarshal(result, &resp); err != nil {
		return "", fmt.Errorf("parsing /ingress/session response: %w", err)
	}
	if resp.Session == "" {
		return "", errors.New("/ingress/session returned empty session token")
	}
	return resp.Session, nil
}

// ResourceList returns all registered Lovelace resources.
// WS command: lovelace/resources
func (ws *WSClient) ResourceList(ctx context.Context) ([]LovelaceResource, error) {
	result, err := ws.sendCommand(ctx, "lovelace/resources", nil)
	if err != nil {
		return nil, err
	}
	var entries []LovelaceResource
	if err := json.Unmarshal(result, &entries); err != nil {
		return nil, fmt.Errorf("parsing resource list: %w", err)
	}
	return entries, nil
}

// sendCommand sends a generic WS command and returns the raw result.
func (ws *WSClient) sendCommand(ctx context.Context, cmdType string, params map[string]any) (json.RawMessage, error) {
	_ = ctx

	id := ws.nextID.Add(1)
	msg := map[string]any{
		"id":   id,
		"type": cmdType,
	}
	maps.Copy(msg, params)

	ws.mu.Lock()
	err := ws.conn.WriteJSON(msg)
	ws.mu.Unlock()
	if err != nil {
		return nil, fmt.Errorf("sending %s: %w", cmdType, err)
	}

	var resp wsResponse
	if err := ws.conn.ReadJSON(&resp); err != nil {
		return nil, fmt.Errorf("reading %s response: %w", cmdType, err)
	}
	if !resp.Success {
		return nil, newAPIError(cmdType, resp.Error)
	}

	return resp.Result, nil
}

// APIError is returned by WS commands when HA responds with success=false.
// Callers can errors.As to inspect Code (e.g. "unauthorized" for non-admin
// access to admin-only endpoints) without parsing the error string.
//
// Error() preserves the legacy "<cmdType> failed: <message>" format so
// existing substring assertions keep working.
type APIError struct {
	CmdType string
	Code    string
	Message string
}

func (e *APIError) Error() string {
	return e.CmdType + " failed: " + e.Message
}

func newAPIError(cmdType string, werr *wsError) *APIError {
	msg, code := errUnknown, ""
	if werr != nil {
		msg = werr.Message
		code = werr.Code
	}
	return &APIError{CmdType: cmdType, Code: code, Message: msg}
}

func (ws *WSClient) connect(ctx context.Context) error {
	u, err := url.Parse(ws.baseURL)
	if err != nil {
		return fmt.Errorf("parsing base URL: %w", err)
	}
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	u.Path = "/api/websocket"

	slog.Debug("connecting to HA websocket", "url", u.String())

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, u.String(), nil) //nolint:bodyclose // websocket upgrade; resp.Body not applicable
	if err != nil {
		return fmt.Errorf("connecting to websocket: %w", err)
	}

	// Read auth_required
	var authReq wsMessage
	if err := conn.ReadJSON(&authReq); err != nil {
		_ = conn.Close()
		return fmt.Errorf("reading auth_required: %w", err)
	}
	if authReq.Type != "auth_required" {
		_ = conn.Close()
		return fmt.Errorf("unexpected message type: %s", authReq.Type)
	}

	// Send auth
	authMsg := map[string]string{
		"type":         "auth",
		"access_token": ws.token,
	}
	if err := conn.WriteJSON(authMsg); err != nil {
		_ = conn.Close()
		return fmt.Errorf("sending auth: %w", err)
	}

	// Read auth response
	var authResp wsMessage
	if err := conn.ReadJSON(&authResp); err != nil {
		_ = conn.Close()
		return fmt.Errorf("reading auth response: %w", err)
	}
	if authResp.Type == "auth_invalid" {
		_ = conn.Close()
		return fmt.Errorf("authentication failed: %s", authResp.Message)
	}
	if authResp.Type != "auth_ok" {
		_ = conn.Close()
		return fmt.Errorf("unexpected auth response type: %s", authResp.Type)
	}

	slog.Debug("websocket authenticated", "ha_version", authResp.HAVersion)

	ws.mu.Lock()
	ws.conn = conn
	ws.mu.Unlock()

	return nil
}

// wsMessage is used for auth-phase messages.
type wsMessage struct {
	HAVersion string `json:"ha_version"`
	Type      string `json:"type"`
	Message   string `json:"message"`
}

// wsResponse is the standard command response envelope.
type wsResponse struct {
	Error   *wsError        `json:"error"`
	Type    string          `json:"type"`
	Result  json.RawMessage `json:"result"`
	ID      int64           `json:"id"`
	Success bool            `json:"success"`
}

type wsError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// SystemLogEntry holds a single entry from the system_log/list WS call.
type SystemLogEntry struct {
	Name          string   `json:"name"`
	Level         string   `json:"level"`
	Exception     string   `json:"exception"`
	Message       []string `json:"message"`
	Source        []any    `json:"source"`
	Timestamp     float64  `json:"timestamp"`
	FirstOccurred float64  `json:"first_occurred"`
	Count         int      `json:"count"`
}

// TraceListResult maps "domain.item_id" to a list of trace summaries.
type TraceListResult map[string][]TraceSummary

// TraceSummary holds one trace entry from trace/list.
type TraceSummary struct {
	Timestamp TraceSummaryTimestamp `json:"timestamp"`
	RunID     string                `json:"run_id"`
	Domain    string                `json:"domain"`
	ItemID    string                `json:"item_id"`
	LastStep  string                `json:"last_step"`
	State     string                `json:"state"`
	Execution string                `json:"script_execution"`
	Trigger   string                `json:"trigger"`
	Error     string                `json:"error"`
}

// TraceSummaryTimestamp holds start/finish times for a trace.
type TraceSummaryTimestamp struct {
	Start  string `json:"start"`
	Finish string `json:"finish"`
}
