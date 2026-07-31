package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/httpretry"
)

// Content-Type values sent per endpoint. Most write endpoints declare a
// text/plain body (the companion parses it as YAML); the ref-replace and helper
// endpoints declare application/json in the OpenAPI spec.
const (
	mimeText = "text/plain"
	mimeJSON = "application/json"
)

// IngressAuth obtains a Supervisor-issued Ingress session token. Used to
// authenticate HTTP calls to Ingress URLs (`/api/hassio_ingress/<addon>/…`)
// from outside the HA frontend — HA Core proxies straight to Supervisor for
// those routes, and Supervisor only honors its own session cookie.
type IngressAuth interface {
	IngressSession(ctx context.Context) (string, error)
}

// Client talks to the hactl-companion add-on API.
type Client struct {
	httpClient *http.Client
	baseURL    string
	// basePath is baseURL's path component, the transport prefix every route
	// is hung under. Kept so an error can name the route without the prefix —
	// see route.
	basePath     string
	token        string
	ingressAuth  IngressAuth
	sessionMu    sync.Mutex
	ingressToken string // cached session, refreshed on 401
}

// New creates a new companion API client.
func New(baseURL, token string) *Client {
	trimmed := strings.TrimRight(baseURL, "/")
	var basePath string
	if u, err := url.Parse(trimmed); err == nil {
		basePath = strings.TrimRight(u.Path, "/")
	}
	return &Client{
		baseURL:  trimmed,
		basePath: basePath,
		token:    token,
		httpClient: &http.Client{
			Timeout: haapi.DefaultTimeout,
			Transport: &http.Transport{
				Proxy:       http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{Timeout: haapi.DialTimeout}).DialContext,
			},
		},
	}
}

// WithIngressAuth attaches an IngressAuth source used to fetch the
// `ingress_session` cookie value for Ingress URL requests. Pass the HA WS
// client. Returns the same Client so callers can chain.
func (c *Client) WithIngressAuth(a IngressAuth) *Client {
	c.ingressAuth = a
	return c
}

// WithTimeout overrides the per-request timeout for subsequent calls.
// Returns the same Client so callers can chain. Use for endpoints slower
// than haapi.DefaultTimeout — check-config runs a full HA config
// validation, which can take well over 30s on a Pi.
func (c *Client) WithTimeout(d time.Duration) *Client {
	c.httpClient.Timeout = d
	return c
}

// decodeResponse unmarshals a companion response body and immediately guards it
// against an identity-less decode (H-14).
//
// Every client method decodes through here rather than calling json.Unmarshal
// directly, so guarding is the default rather than something the next endpoint's
// author has to remember. A renamed companion property does not fail
// json.Unmarshal — it leaves a zero value that renders as a plausible answer,
// which is the mechanism that made every automation run print PASS (D1).
// degeneracy.Check poisons the missing identity with degeneracy.Marker and
// returns an error carrying it, so both the rendered value and the failure
// message say UNPARSED.
func decodeResponse[T any](source string, data []byte, r *T) error {
	if err := json.Unmarshal(data, r); err != nil {
		return err
	}
	return degeneracy.Check("companion "+source, r)
}

// isIngressPath reports whether p is an HA Ingress URL path that requires
// signing rather than a bare bearer token.
func isIngressPath(p string) bool {
	return strings.HasPrefix(p, "/api/hassio_ingress/")
}

// Health calls GET /v1/health.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	data, err := c.doGet(ctx, "/v1/health", nil)
	if err != nil {
		return nil, err
	}
	var r HealthResponse
	return &r, decodeResponse("/v1/health", data, &r)
}

// Status calls GET /v1/status.
func (c *Client) Status(ctx context.Context) (*StatusResponse, error) {
	data, err := c.doGet(ctx, "/v1/status", nil)
	if err != nil {
		return nil, err
	}
	var r StatusResponse
	return &r, decodeResponse("/v1/status", data, &r)
}

// ListConfigFiles calls GET /v1/config/files.
func (c *Client) ListConfigFiles(ctx context.Context) (*ConfigFilesResponse, error) {
	data, err := c.doGet(ctx, "/v1/config/files", nil)
	if err != nil {
		return nil, err
	}
	var r ConfigFilesResponse
	return &r, decodeResponse("/v1/config/files", data, &r)
}

// ReadConfigFile calls GET /v1/config/file?path=<path>&resolve=<resolve>.
func (c *Client) ReadConfigFile(ctx context.Context, path string) (*ConfigFileResponse, error) {
	q := url.Values{"path": {path}, "resolve": {"true"}}
	data, err := c.doGet(ctx, "/v1/config/file", q)
	if err != nil {
		return nil, err
	}
	var r ConfigFileResponse
	return &r, decodeResponse("/v1/config/file", data, &r)
}

// ReadConfigFileRaw calls GET /v1/config/file?path=<path>&resolve=false.
func (c *Client) ReadConfigFileRaw(ctx context.Context, path string) (*ConfigFileResponse, error) {
	q := url.Values{"path": {path}, "resolve": {"false"}}
	data, err := c.doGet(ctx, "/v1/config/file", q)
	if err != nil {
		return nil, err
	}
	var r ConfigFileResponse
	return &r, decodeResponse("/v1/config/file", data, &r)
}

// ReadConfigBlock calls GET /v1/config/block?path=<path>&id=<id>.
func (c *Client) ReadConfigBlock(ctx context.Context, path, id string) (*ConfigBlockResponse, error) {
	q := url.Values{"path": {path}, "id": {id}}
	data, err := c.doGet(ctx, "/v1/config/block", q)
	if err != nil {
		return nil, err
	}
	var r ConfigBlockResponse
	return &r, decodeResponse("/v1/config/block", data, &r)
}

// WriteConfigFile calls PUT /v1/config/file?path=<path>&dry_run=<dryRun>.
func (c *Client) WriteConfigFile(ctx context.Context, path, content string, dryRun bool) (*ConfigWriteResponse, error) {
	q := url.Values{
		"path":    {path},
		"dry_run": {strconv.FormatBool(dryRun)},
	}
	data, err := c.doPut(ctx, "/v1/config/file", q, content, mimeText)
	if err != nil {
		return nil, err
	}
	var r ConfigWriteResponse
	return &r, decodeResponse("/v1/config/file", data, &r)
}

// RelatedEntity calls GET /v1/related/entity?entity_id=<entityID>[&stale=true].
// With stale=true a renamed/deleted entity returns 200 + StaleRefs instead of 404.
func (c *Client) RelatedEntity(ctx context.Context, entityID string, stale bool) (*RelatedEntityResponse, error) {
	q := url.Values{"entity_id": {entityID}}
	if stale {
		q.Set("stale", "true")
	}
	data, err := c.doGet(ctx, "/v1/related/entity", q)
	if err != nil {
		return nil, err
	}
	var r RelatedEntityResponse
	return &r, decodeResponse("/v1/related/entity", data, &r)
}

// RefScan calls GET /v1/ref/scan?target=<target> and returns every literal
// reference to target across the config file !include graph.
func (c *Client) RefScan(ctx context.Context, target string) (*RefScanResponse, error) {
	q := url.Values{"target": {target}}
	data, err := c.doGet(ctx, "/v1/ref/scan", q)
	if err != nil {
		return nil, err
	}
	var r RefScanResponse
	return &r, decodeResponse("/v1/ref/scan", data, &r)
}

// RefEntities calls GET /v1/ref/entities and returns every entity_id-shaped
// value across the config file !include graph, each tagged with its enclosing
// key. Unfiltered by design — the caller decides which keys are real entities.
func (c *Client) RefEntities(ctx context.Context) (*RefEntitiesResponse, error) {
	data, err := c.doGet(ctx, "/v1/ref/entities", nil)
	if err != nil {
		return nil, err
	}
	var r RefEntitiesResponse
	return &r, decodeResponse("/v1/ref/entities", data, &r)
}

// RefReplace calls POST /v1/ref/replace to rewrite oldVal to newVal across the
// config file !include graph. With dryRun the companion reports the changes
// without writing; otherwise it rewrites each owning file.
func (c *Client) RefReplace(ctx context.Context, oldVal, newVal string, dryRun bool) (*RefReplaceResponse, error) {
	body, err := json.Marshal(map[string]any{"old": oldVal, "new": newVal, "dry_run": dryRun})
	if err != nil {
		return nil, fmt.Errorf("encoding ref replace body: %w", err)
	}
	data, err := c.doPostBody(ctx, "/v1/ref/replace", nil, string(body), mimeJSON)
	if err != nil {
		return nil, err
	}
	var r RefReplaceResponse
	return &r, decodeResponse("/v1/ref/replace", data, &r)
}

// --- Template CRUD ---

// ListTemplates calls GET /v1/config/templates.
func (c *Client) ListTemplates(ctx context.Context) (*TemplatesResponse, error) {
	data, err := c.doGet(ctx, "/v1/config/templates", nil)
	if err != nil {
		return nil, err
	}
	var r TemplatesResponse
	return &r, decodeResponse("/v1/config/templates", data, &r)
}

// GetTemplate calls GET /v1/config/template?id=<id>.
func (c *Client) GetTemplate(ctx context.Context, id string) (*TemplateResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doGet(ctx, "/v1/config/template", q)
	if err != nil {
		return nil, err
	}
	var r TemplateResponse
	return &r, decodeResponse("/v1/config/template", data, &r)
}

// WriteTemplate calls PUT /v1/config/template?id=<id>&dry_run=<dryRun>.
func (c *Client) WriteTemplate(ctx context.Context, id, content string, dryRun bool) (*ConfigDeleteResponse, error) {
	q := url.Values{
		"id":      {id},
		"dry_run": {strconv.FormatBool(dryRun)},
	}
	data, err := c.doPut(ctx, "/v1/config/template", q, content, mimeText)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, decodeResponse("/v1/config/template", data, &r)
}

// CreateTemplate calls POST /v1/config/template?domain=<domain>.
func (c *Client) CreateTemplate(ctx context.Context, content, domain string) (*TemplateCreateResponse, error) {
	q := url.Values{}
	if domain != "" {
		q.Set("domain", domain)
	}
	data, err := c.doPostBody(ctx, "/v1/config/template", q, content, mimeText)
	if err != nil {
		return nil, err
	}
	var r TemplateCreateResponse
	return &r, decodeResponse("/v1/config/template", data, &r)
}

// DeleteTemplate calls DELETE /v1/config/template?id=<id>.
func (c *Client) DeleteTemplate(ctx context.Context, id string) (*ConfigDeleteResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doDelete(ctx, "/v1/config/template", q)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, decodeResponse("/v1/config/template", data, &r)
}

// --- Script CRUD ---

// ListScriptDefs calls GET /v1/config/scripts.
func (c *Client) ListScriptDefs(ctx context.Context) (*ScriptsResponse, error) {
	data, err := c.doGet(ctx, "/v1/config/scripts", nil)
	if err != nil {
		return nil, err
	}
	var r ScriptsResponse
	return &r, decodeResponse("/v1/config/scripts", data, &r)
}

// GetScriptDef calls GET /v1/config/script?id=<id>.
func (c *Client) GetScriptDef(ctx context.Context, id string) (*ScriptResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doGet(ctx, "/v1/config/script", q)
	if err != nil {
		return nil, err
	}
	var r ScriptResponse
	return &r, decodeResponse("/v1/config/script", data, &r)
}

// WriteScriptDef calls PUT /v1/config/script?id=<id>&dry_run=<dryRun>.
func (c *Client) WriteScriptDef(ctx context.Context, id, content string, dryRun bool) (*ConfigDeleteResponse, error) {
	q := url.Values{
		"id":      {id},
		"dry_run": {strconv.FormatBool(dryRun)},
	}
	data, err := c.doPut(ctx, "/v1/config/script", q, content, mimeText)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, decodeResponse("/v1/config/script", data, &r)
}

// CreateScriptDef calls POST /v1/config/script.
func (c *Client) CreateScriptDef(ctx context.Context, content string) (*ScriptCreateResponse, error) {
	data, err := c.doPostBody(ctx, "/v1/config/script", nil, content, mimeText)
	if err != nil {
		return nil, err
	}
	var r ScriptCreateResponse
	return &r, decodeResponse("/v1/config/script", data, &r)
}

// DeleteScriptDef calls DELETE /v1/config/script?id=<id>.
func (c *Client) DeleteScriptDef(ctx context.Context, id string) (*ConfigDeleteResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doDelete(ctx, "/v1/config/script", q)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, decodeResponse("/v1/config/script", data, &r)
}

// --- Automation CRUD ---

// ListAutomationDefs calls GET /v1/config/automations.
func (c *Client) ListAutomationDefs(ctx context.Context) (*AutomationsResponse, error) {
	data, err := c.doGet(ctx, "/v1/config/automations", nil)
	if err != nil {
		return nil, err
	}
	var r AutomationsResponse
	return &r, decodeResponse("/v1/config/automations", data, &r)
}

// GetAutomationDef calls GET /v1/config/automation?id=<id>.
func (c *Client) GetAutomationDef(ctx context.Context, id string) (*AutomationResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doGet(ctx, "/v1/config/automation", q)
	if err != nil {
		return nil, err
	}
	var r AutomationResponse
	return &r, decodeResponse("/v1/config/automation", data, &r)
}

// WriteAutomationDef calls PUT /v1/config/automation?id=<id>&dry_run=<dryRun>.
func (c *Client) WriteAutomationDef(ctx context.Context, id, content string, dryRun bool) (*ConfigDeleteResponse, error) {
	q := url.Values{
		"id":      {id},
		"dry_run": {strconv.FormatBool(dryRun)},
	}
	data, err := c.doPut(ctx, "/v1/config/automation", q, content, mimeText)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, decodeResponse("/v1/config/automation", data, &r)
}

// CreateAutomationDef calls POST /v1/config/automation.
func (c *Client) CreateAutomationDef(ctx context.Context, content string) (*AutomationCreateResponse, error) {
	data, err := c.doPostBody(ctx, "/v1/config/automation", nil, content, mimeText)
	if err != nil {
		return nil, err
	}
	var r AutomationCreateResponse
	return &r, decodeResponse("/v1/config/automation", data, &r)
}

// DeleteAutomationDef calls DELETE /v1/config/automation?id=<id>.
func (c *Client) DeleteAutomationDef(ctx context.Context, id string) (*ConfigDeleteResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doDelete(ctx, "/v1/config/automation", q)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, decodeResponse("/v1/config/automation", data, &r)
}

// GetWiring calls GET /v1/config/wiring?domain=<domain>: whether a create for
// that domain would reach a file HA reads, and if not, why not.
//
// The answer exists so a dry run can fail exactly where --confirm would (H-2)
// without hactl re-deriving the companion's include-vs-inline resolution in Go.
// Re-deriving it is the four-copy drift this seam already paid for once: the
// rules would be restated in a second language, and the preview would explain
// its refusal differently from the run it predicts.
func (c *Client) GetWiring(ctx context.Context, domain string) (*WiringResponse, error) {
	q := url.Values{"domain": {domain}}
	data, err := c.doGet(ctx, "/v1/config/wiring", q)
	if err != nil {
		return nil, err
	}
	var r WiringResponse
	return &r, decodeResponse("/v1/config/wiring", data, &r)
}

// --- Helper CRUD ---

// ListHelpers calls GET /v1/config/helpers[?domain=<domain>].
func (c *Client) ListHelpers(ctx context.Context, domain string) (*HelpersResponse, error) {
	q := url.Values{}
	if domain != "" {
		q.Set("domain", domain)
	}
	data, err := c.doGet(ctx, "/v1/config/helpers", q)
	if err != nil {
		return nil, err
	}
	var r HelpersResponse
	return &r, decodeResponse("/v1/config/helpers", data, &r)
}

// GetHelper calls GET /v1/config/helper?id=<id>.
func (c *Client) GetHelper(ctx context.Context, id string) (*HelperResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doGet(ctx, "/v1/config/helper", q)
	if err != nil {
		return nil, err
	}
	var r HelperResponse
	return &r, decodeResponse("/v1/config/helper", data, &r)
}

// CreateHelper calls POST /v1/config/helper?domain=<domain>.
func (c *Client) CreateHelper(ctx context.Context, content, domain string) (*HelperCreateResponse, error) {
	q := url.Values{"domain": {domain}}
	data, err := c.doPostBody(ctx, "/v1/config/helper", q, content, mimeJSON)
	if err != nil {
		return nil, err
	}
	var r HelperCreateResponse
	return &r, decodeResponse("/v1/config/helper", data, &r)
}

// UpdateHelper calls PUT /v1/config/helper?id=<id>.
func (c *Client) UpdateHelper(ctx context.Context, id, content string) (*ConfigDeleteResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doPut(ctx, "/v1/config/helper", q, content, mimeJSON)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, decodeResponse("/v1/config/helper", data, &r)
}

// DeleteHelper calls DELETE /v1/config/helper?id=<id>.
func (c *Client) DeleteHelper(ctx context.Context, id string) (*ConfigDeleteResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doDelete(ctx, "/v1/config/helper", q)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, decodeResponse("/v1/config/helper", data, &r)
}

// ReloadDomain calls POST /v1/ha/reload/<domain>.
func (c *Client) ReloadDomain(ctx context.Context, domain string) error {
	_, err := c.doPostBody(ctx, "/v1/ha/reload/"+domain, nil, "", mimeText)
	return err
}

// CheckConfig calls POST /v1/ha/check-config and reports whether the HA
// config on disk is valid. The error is non-nil only when the check could
// not be performed (companion or core API unreachable).
func (c *Client) CheckConfig(ctx context.Context) (valid bool, errors string, err error) {
	data, err := c.doPostBody(ctx, "/v1/ha/check-config", nil, "", mimeText)
	if err != nil {
		return false, "", err
	}
	var r CheckConfigResponse
	if err := decodeResponse("/v1/ha/check-config", data, &r); err != nil {
		return false, "", fmt.Errorf("parsing check-config response: %w", err)
	}
	if r.Valid == nil {
		// Companion <= 2026.6.7 returns {"status": "ok"} with no valid field
		// (and 502 for an invalid config, surfaced as err above).
		return r.Status == "ok", r.Errors, nil
	}
	return *r.Valid, r.Errors, nil
}

// --- WireGuard tunnel management ---

// WireGuardStatus calls GET /v1/wireguard/status?tunnel=<tunnel>.
func (c *Client) WireGuardStatus(ctx context.Context, tunnel string) (*WireGuardStatusResponse, error) {
	data, err := c.doGet(ctx, "/v1/wireguard/status", url.Values{"tunnel": {tunnel}})
	if err != nil {
		return nil, err
	}
	var r WireGuardStatusResponse
	return &r, decodeResponse("/v1/wireguard/status", data, &r)
}

// WireGuardConfig calls POST /v1/wireguard/config?tunnel=<tunnel> with a raw
// `.conf` body (text/plain).
func (c *Client) WireGuardConfig(ctx context.Context, tunnel, conf string) (*WireGuardActionResponse, error) {
	data, err := c.doPostBody(ctx, "/v1/wireguard/config", url.Values{"tunnel": {tunnel}}, conf, mimeText)
	if err != nil {
		return nil, err
	}
	var r WireGuardActionResponse
	return &r, decodeResponse("/v1/wireguard/config", data, &r)
}

// WireGuardStart calls POST /v1/wireguard/start?tunnel=<tunnel>.
func (c *Client) WireGuardStart(ctx context.Context, tunnel string) (*WireGuardActionResponse, error) {
	q := url.Values{"tunnel": {tunnel}}
	data, err := c.doPostBody(ctx, "/v1/wireguard/start", q, "", mimeText)
	if err != nil {
		return nil, err
	}
	var r WireGuardActionResponse
	return &r, decodeResponse("/v1/wireguard/start", data, &r)
}

// WireGuardStop calls POST /v1/wireguard/stop?tunnel=<tunnel>.
func (c *Client) WireGuardStop(ctx context.Context, tunnel string) (*WireGuardActionResponse, error) {
	q := url.Values{"tunnel": {tunnel}}
	data, err := c.doPostBody(ctx, "/v1/wireguard/stop", q, "", mimeText)
	if err != nil {
		return nil, err
	}
	var r WireGuardActionResponse
	return &r, decodeResponse("/v1/wireguard/stop", data, &r)
}

// --- Logs ---

// Logs calls GET /v1/logs with the given filters and returns recent companion
// log records from the add-on's in-memory ring buffer.
func (c *Client) Logs(ctx context.Context, p LogsParams) (*LogsResponse, error) {
	q := url.Values{}
	if p.Component != "" {
		q.Set("component", p.Component)
	}
	if p.Level != "" {
		q.Set("level", p.Level)
	}
	if p.Since != "" {
		q.Set("since", p.Since)
	}
	if p.Limit > 0 {
		q.Set("limit", strconv.Itoa(p.Limit))
	}
	data, err := c.doGet(ctx, "/v1/logs", q)
	if err != nil {
		return nil, err
	}
	var r LogsResponse
	return &r, decodeResponse("/v1/logs", data, &r)
}

// route strips the transport prefix from a request path, leaving the companion
// API route the caller actually asked for.
//
// Every failure used to be reported with the full URL path, which under
// Ingress reads
//
//	reading config file: GET /api/hassio_ingress/<43 chars>/v1/config/file: 404 …
//
// The segment in the middle is the add-on's Supervisor ingress token: stable
// across invocations, per install, and printed on every 404 a user might paste
// into a bug report (finding #23). It also tells the reader nothing — the route
// is `/v1/config/file` no matter how the request got there, and *that* is what
// distinguishes one failure from another.
//
// Derived by trimming baseURL's own path rather than by matching
// "/api/hassio_ingress/", so a companion reached through any other prefix — a
// reverse proxy, a direct port, whatever comes next — is named the same way
// without a second rule to remember. `companion status` still prints the full
// URL: that command's question IS the transport, and a user debugging
// discovery needs the address to curl.
func (c *Client) route(p string) string {
	if c.basePath == "" {
		return p
	}
	if trimmed := strings.TrimPrefix(p, c.basePath); trimmed != p && trimmed != "" {
		return trimmed
	}
	return p
}

// scrubTransportError rewrites the URL net/http embeds in its own error text.
//
// `*url.Error` renders as `Get "<the whole URL>": dial tcp …`, so a route-only
// wrapper around it would have printed the route and then the prefix anyway.
// The host stays — it is the address the caller configured and the useful part
// of a connection failure — and only the path is reduced to the route.
func (c *Client) scrubTransportError(err error) error {
	var ue *url.Error
	if !errors.As(err, &ue) {
		return err
	}
	u, parseErr := url.Parse(ue.URL)
	if parseErr != nil {
		// Cannot rewrite what will not parse; drop the URL rather than print it.
		return ue.Err
	}
	u.Path = c.route(u.Path)
	u.RawQuery = ""
	return &url.Error{Op: ue.Op, URL: u.String(), Err: ue.Err}
}

func (c *Client) doGet(ctx context.Context, path string, query url.Values) ([]byte, error) {
	u := c.baseURL + path
	if query != nil {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	return c.doWithRetry(req)
}

func (c *Client) doPostBody(ctx context.Context, path string, query url.Values, content, contentType string) ([]byte, error) {
	u := c.baseURL + path
	if query != nil {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	return c.doWithRetry(req)
}

func (c *Client) doDelete(ctx context.Context, path string, query url.Values) ([]byte, error) {
	u := c.baseURL + path
	if query != nil {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, u, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	return c.doWithRetry(req)
}

func (c *Client) doPut(ctx context.Context, path string, query url.Values, content, contentType string) ([]byte, error) {
	u := c.baseURL + path
	if query != nil {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, strings.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", contentType)
	return c.doWithRetry(req)
}

// applyIngressAuth attaches the cached Supervisor ingress session cookie to
// the request, fetching a fresh one if missing or if forceRefresh is true
// (which the retry loop sets on 401). No-op for non-Ingress URLs and when no
// IngressAuth source is wired up.
func (c *Client) applyIngressAuth(req *http.Request, forceRefresh bool) error {
	if c.ingressAuth == nil || !isIngressPath(req.URL.Path) {
		return nil
	}
	c.sessionMu.Lock()
	defer c.sessionMu.Unlock()
	if c.ingressToken == "" || forceRefresh {
		tok, err := c.ingressAuth.IngressSession(req.Context())
		if err != nil {
			return fmt.Errorf("fetching ingress session: %w", err)
		}
		c.ingressToken = tok
	}
	req.AddCookie(&http.Cookie{Name: "ingress_session", Value: c.ingressToken})
	return nil
}

func (c *Client) doWithRetry(req *http.Request) ([]byte, error) {
	req.Header.Set("Authorization", "Bearer "+c.token)

	bodyBytes, err := drainBody(req)
	if err != nil {
		return nil, err
	}

	hasIngressAuth := c.ingressAuth != nil && isIngressPath(req.URL.Path)
	originalHeader := req.Header.Clone()

	backoffs := []time.Duration{500 * time.Millisecond, 1 * time.Second}
	maxAttempts := len(backoffs) + 1

	for attempt := range maxAttempts {
		if bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}
		// Reset headers each attempt so retries don't accumulate stale cookies.
		req.Header = originalHeader.Clone()
		req.Header.Set("Authorization", "Bearer "+c.token)
		// On retry after a 401, force a fresh session token — the previous
		// one may have expired or been invalidated server-side.
		forceRefresh := attempt > 0
		if err := c.applyIngressAuth(req, forceRefresh); err != nil {
			return nil, err
		}

		respBody, status, err := c.doOnce(req)
		if shouldRetry(err, status, hasIngressAuth, req.Method) && attempt < len(backoffs) {
			slog.Warn("retrying companion request", "method", req.Method, "status", status, "attempt", attempt+1, "error", err) //nolint:gosec // method is a Go HTTP constant
			time.Sleep(backoffs[attempt])
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", req.Method, c.route(req.URL.Path), err)
		}
		if status < 200 || status >= 300 {
			// Typed, not fmt.Errorf: the text is the same, but a caller can now
			// ask haapi.HTTPStatus whether this was a 404 instead of matching
			// on a message that embeds the companion's response body.
			return nil, haapi.NewHTTPStatusError(req.Method, c.route(req.URL.Path), status, respBody)
		}
		return respBody, nil
	}
	return nil, fmt.Errorf("%s %s: max retries exceeded", req.Method, c.route(req.URL.Path))
}

// doOnce performs a single HTTP request attempt and returns the response
// body, status code, and any transport error. Either body+status or err is
// populated, never both.
func (c *Client) doOnce(req *http.Request) ([]byte, int, error) {
	start := time.Now()
	resp, err := c.httpClient.Do(req) //nolint:gosec // URL is operator-provided config (SSRF by design for a CLI tool)
	duration := time.Since(start)
	if err != nil {
		// Scrubbed here rather than at the call site: net/http's *url.Error
		// carries the full request URL in its own message, so wrapping it with
		// a clean route would have re-leaked the prefix one layer down — and
		// the slog line below would have leaked it whether anything wrapped it
		// or not.
		err = c.scrubTransportError(err)
		slog.Debug("companion request failed", "method", req.Method, "error", err, "duration", duration) //nolint:gosec // method is a Go HTTP constant
		return nil, 0, err
	}
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	slog.Debug("companion request", "method", req.Method, "status", resp.StatusCode, "duration", duration) //nolint:gosec // method is a Go HTTP constant
	if readErr != nil {
		return nil, resp.StatusCode, fmt.Errorf("reading response body: %w", readErr)
	}
	return body, resp.StatusCode, nil
}

// drainBody reads and closes req.Body so the bytes can be replayed on retry.
// Returns nil bytes (not an error) if there is no body.
func drainBody(req *http.Request) ([]byte, error) {
	if req.Body == nil {
		return nil, nil
	}
	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, fmt.Errorf("reading request body: %w", err)
	}
	_ = req.Body.Close()
	return body, nil
}

// shouldRetry decides whether the current failure warrants another attempt.
//
// Retries are gated on idempotency so a non-idempotent create (POST) is never
// silently duplicated. For POST, a transport error may mean the request reached
// the server but the response was lost — retrying would create a second
// automation/script/helper — so we retry only when the request provably never
// left the client (dial/connection-refused class). A 5xx means the server
// received the request, so only idempotent methods retry it. A signed 401
// (expired signature) is safe to retry for any method: the server rejected the
// request before acting on it.
func shouldRetry(err error, status int, signed bool, method string) bool {
	// The transport-error and 5xx halves are httpretry.ShouldRetry, shared with
	// the Home Assistant client so the two cannot drift apart again — they had,
	// and the HA client retried every POST on 5xx for months while this file
	// carried the comment explaining why that is wrong.
	if httpretry.ShouldRetry(err, status, method) {
		return true
	}
	if err != nil || status >= 500 {
		return false
	}
	// The one rule that is this client's alone: a signed 401 means the Ingress
	// session expired and the server rejected the request before acting on it,
	// so re-signing and re-sending is safe for any method.
	return signed && status == http.StatusUnauthorized
}
