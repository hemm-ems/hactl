package companion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
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
	httpClient   *http.Client
	baseURL      string
	token        string
	ingressAuth  IngressAuth
	sessionMu    sync.Mutex
	ingressToken string // cached session, refreshed on 401
}

// New creates a new companion API client.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
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
	return &r, json.Unmarshal(data, &r)
}

// Status calls GET /v1/status.
func (c *Client) Status(ctx context.Context) (*StatusResponse, error) {
	data, err := c.doGet(ctx, "/v1/status", nil)
	if err != nil {
		return nil, err
	}
	var r StatusResponse
	return &r, json.Unmarshal(data, &r)
}

// ListConfigFiles calls GET /v1/config/files.
func (c *Client) ListConfigFiles(ctx context.Context) (*ConfigFilesResponse, error) {
	data, err := c.doGet(ctx, "/v1/config/files", nil)
	if err != nil {
		return nil, err
	}
	var r ConfigFilesResponse
	return &r, json.Unmarshal(data, &r)
}

// ReadConfigFile calls GET /v1/config/file?path=<path>&resolve=<resolve>.
func (c *Client) ReadConfigFile(ctx context.Context, path string) (*ConfigFileResponse, error) {
	q := url.Values{"path": {path}, "resolve": {"true"}}
	data, err := c.doGet(ctx, "/v1/config/file", q)
	if err != nil {
		return nil, err
	}
	var r ConfigFileResponse
	return &r, json.Unmarshal(data, &r)
}

// ReadConfigFileRaw calls GET /v1/config/file?path=<path>&resolve=false.
func (c *Client) ReadConfigFileRaw(ctx context.Context, path string) (*ConfigFileResponse, error) {
	q := url.Values{"path": {path}, "resolve": {"false"}}
	data, err := c.doGet(ctx, "/v1/config/file", q)
	if err != nil {
		return nil, err
	}
	var r ConfigFileResponse
	return &r, json.Unmarshal(data, &r)
}

// ReadConfigBlock calls GET /v1/config/block?path=<path>&id=<id>.
func (c *Client) ReadConfigBlock(ctx context.Context, path, id string) (*ConfigBlockResponse, error) {
	q := url.Values{"path": {path}, "id": {id}}
	data, err := c.doGet(ctx, "/v1/config/block", q)
	if err != nil {
		return nil, err
	}
	var r ConfigBlockResponse
	return &r, json.Unmarshal(data, &r)
}

// WriteConfigFile calls PUT /v1/config/file?path=<path>&dry_run=<dryRun>.
func (c *Client) WriteConfigFile(ctx context.Context, path, content string, dryRun bool) (*ConfigWriteResponse, error) {
	q := url.Values{
		"path":    {path},
		"dry_run": {strconv.FormatBool(dryRun)},
	}
	data, err := c.doPut(ctx, "/v1/config/file", q, content)
	if err != nil {
		return nil, err
	}
	var r ConfigWriteResponse
	return &r, json.Unmarshal(data, &r)
}

// --- Template CRUD ---

// ListTemplates calls GET /v1/config/templates.
func (c *Client) ListTemplates(ctx context.Context) (*TemplatesResponse, error) {
	data, err := c.doGet(ctx, "/v1/config/templates", nil)
	if err != nil {
		return nil, err
	}
	var r TemplatesResponse
	return &r, json.Unmarshal(data, &r)
}

// GetTemplate calls GET /v1/config/template?id=<id>.
func (c *Client) GetTemplate(ctx context.Context, id string) (*TemplateResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doGet(ctx, "/v1/config/template", q)
	if err != nil {
		return nil, err
	}
	var r TemplateResponse
	return &r, json.Unmarshal(data, &r)
}

// WriteTemplate calls PUT /v1/config/template?id=<id>&dry_run=<dryRun>.
func (c *Client) WriteTemplate(ctx context.Context, id, content string, dryRun bool) (*ConfigDeleteResponse, error) {
	q := url.Values{
		"id":      {id},
		"dry_run": {strconv.FormatBool(dryRun)},
	}
	data, err := c.doPut(ctx, "/v1/config/template", q, content)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, json.Unmarshal(data, &r)
}

// CreateTemplate calls POST /v1/config/template?domain=<domain>.
func (c *Client) CreateTemplate(ctx context.Context, content, domain string) (*TemplateCreateResponse, error) {
	q := url.Values{}
	if domain != "" {
		q.Set("domain", domain)
	}
	data, err := c.doPostBody(ctx, "/v1/config/template", q, content)
	if err != nil {
		return nil, err
	}
	var r TemplateCreateResponse
	return &r, json.Unmarshal(data, &r)
}

// DeleteTemplate calls DELETE /v1/config/template?id=<id>.
func (c *Client) DeleteTemplate(ctx context.Context, id string) (*ConfigDeleteResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doDelete(ctx, "/v1/config/template", q)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, json.Unmarshal(data, &r)
}

// --- Script CRUD ---

// ListScriptDefs calls GET /v1/config/scripts.
func (c *Client) ListScriptDefs(ctx context.Context) (*ScriptsResponse, error) {
	data, err := c.doGet(ctx, "/v1/config/scripts", nil)
	if err != nil {
		return nil, err
	}
	var r ScriptsResponse
	return &r, json.Unmarshal(data, &r)
}

// GetScriptDef calls GET /v1/config/script?id=<id>.
func (c *Client) GetScriptDef(ctx context.Context, id string) (*ScriptResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doGet(ctx, "/v1/config/script", q)
	if err != nil {
		return nil, err
	}
	var r ScriptResponse
	return &r, json.Unmarshal(data, &r)
}

// WriteScriptDef calls PUT /v1/config/script?id=<id>&dry_run=<dryRun>.
func (c *Client) WriteScriptDef(ctx context.Context, id, content string, dryRun bool) (*ConfigDeleteResponse, error) {
	q := url.Values{
		"id":      {id},
		"dry_run": {strconv.FormatBool(dryRun)},
	}
	data, err := c.doPut(ctx, "/v1/config/script", q, content)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, json.Unmarshal(data, &r)
}

// CreateScriptDef calls POST /v1/config/script.
func (c *Client) CreateScriptDef(ctx context.Context, content string) (*ScriptCreateResponse, error) {
	data, err := c.doPostBody(ctx, "/v1/config/script", nil, content)
	if err != nil {
		return nil, err
	}
	var r ScriptCreateResponse
	return &r, json.Unmarshal(data, &r)
}

// DeleteScriptDef calls DELETE /v1/config/script?id=<id>.
func (c *Client) DeleteScriptDef(ctx context.Context, id string) (*ConfigDeleteResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doDelete(ctx, "/v1/config/script", q)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, json.Unmarshal(data, &r)
}

// --- Automation CRUD ---

// ListAutomationDefs calls GET /v1/config/automations.
func (c *Client) ListAutomationDefs(ctx context.Context) (*AutomationsResponse, error) {
	data, err := c.doGet(ctx, "/v1/config/automations", nil)
	if err != nil {
		return nil, err
	}
	var r AutomationsResponse
	return &r, json.Unmarshal(data, &r)
}

// GetAutomationDef calls GET /v1/config/automation?id=<id>.
func (c *Client) GetAutomationDef(ctx context.Context, id string) (*AutomationResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doGet(ctx, "/v1/config/automation", q)
	if err != nil {
		return nil, err
	}
	var r AutomationResponse
	return &r, json.Unmarshal(data, &r)
}

// WriteAutomationDef calls PUT /v1/config/automation?id=<id>&dry_run=<dryRun>.
func (c *Client) WriteAutomationDef(ctx context.Context, id, content string, dryRun bool) (*ConfigDeleteResponse, error) {
	q := url.Values{
		"id":      {id},
		"dry_run": {strconv.FormatBool(dryRun)},
	}
	data, err := c.doPut(ctx, "/v1/config/automation", q, content)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, json.Unmarshal(data, &r)
}

// CreateAutomationDef calls POST /v1/config/automation.
func (c *Client) CreateAutomationDef(ctx context.Context, content string) (*AutomationCreateResponse, error) {
	data, err := c.doPostBody(ctx, "/v1/config/automation", nil, content)
	if err != nil {
		return nil, err
	}
	var r AutomationCreateResponse
	return &r, json.Unmarshal(data, &r)
}

// DeleteAutomationDef calls DELETE /v1/config/automation?id=<id>.
func (c *Client) DeleteAutomationDef(ctx context.Context, id string) (*ConfigDeleteResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doDelete(ctx, "/v1/config/automation", q)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, json.Unmarshal(data, &r)
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
	return &r, json.Unmarshal(data, &r)
}

// GetHelper calls GET /v1/config/helper?id=<id>.
func (c *Client) GetHelper(ctx context.Context, id string) (*HelperResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doGet(ctx, "/v1/config/helper", q)
	if err != nil {
		return nil, err
	}
	var r HelperResponse
	return &r, json.Unmarshal(data, &r)
}

// CreateHelper calls POST /v1/config/helper?domain=<domain>.
func (c *Client) CreateHelper(ctx context.Context, content, domain string) (*HelperCreateResponse, error) {
	q := url.Values{"domain": {domain}}
	data, err := c.doPostBody(ctx, "/v1/config/helper", q, content)
	if err != nil {
		return nil, err
	}
	var r HelperCreateResponse
	return &r, json.Unmarshal(data, &r)
}

// UpdateHelper calls PUT /v1/config/helper?id=<id>.
func (c *Client) UpdateHelper(ctx context.Context, id, content string) (*ConfigDeleteResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doPut(ctx, "/v1/config/helper", q, content)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, json.Unmarshal(data, &r)
}

// DeleteHelper calls DELETE /v1/config/helper?id=<id>.
func (c *Client) DeleteHelper(ctx context.Context, id string) (*ConfigDeleteResponse, error) {
	q := url.Values{"id": {id}}
	data, err := c.doDelete(ctx, "/v1/config/helper", q)
	if err != nil {
		return nil, err
	}
	var r ConfigDeleteResponse
	return &r, json.Unmarshal(data, &r)
}

// ReloadDomain calls POST /v1/ha/reload/<domain>.
func (c *Client) ReloadDomain(ctx context.Context, domain string) error {
	_, err := c.doPostBody(ctx, "/v1/ha/reload/"+domain, nil, "")
	return err
}

// --- WireGuard tunnel management ---

// WireGuardStatus calls GET /v1/wireguard/status?tunnel=<tunnel>.
func (c *Client) WireGuardStatus(ctx context.Context, tunnel string) (*WireGuardStatusResponse, error) {
	data, err := c.doGet(ctx, "/v1/wireguard/status", url.Values{"tunnel": {tunnel}})
	if err != nil {
		return nil, err
	}
	var r WireGuardStatusResponse
	return &r, json.Unmarshal(data, &r)
}

// WireGuardConfig calls POST /v1/wireguard/config?tunnel=<tunnel> with a raw
// `.conf` body (text/plain).
func (c *Client) WireGuardConfig(ctx context.Context, tunnel, conf string) (*WireGuardActionResponse, error) {
	data, err := c.doPostBody(ctx, "/v1/wireguard/config", url.Values{"tunnel": {tunnel}}, conf)
	if err != nil {
		return nil, err
	}
	var r WireGuardActionResponse
	return &r, json.Unmarshal(data, &r)
}

// WireGuardStart calls POST /v1/wireguard/start?tunnel=<tunnel>&auto_enable=<autoEnable>.
func (c *Client) WireGuardStart(ctx context.Context, tunnel string, autoEnable bool) (*WireGuardActionResponse, error) {
	q := url.Values{"tunnel": {tunnel}, "auto_enable": {strconv.FormatBool(autoEnable)}}
	data, err := c.doPostBody(ctx, "/v1/wireguard/start", q, "")
	if err != nil {
		return nil, err
	}
	var r WireGuardActionResponse
	return &r, json.Unmarshal(data, &r)
}

// WireGuardStop calls POST /v1/wireguard/stop?tunnel=<tunnel>&auto_disable=<autoDisable>.
func (c *Client) WireGuardStop(ctx context.Context, tunnel string, autoDisable bool) (*WireGuardActionResponse, error) {
	q := url.Values{"tunnel": {tunnel}, "auto_disable": {strconv.FormatBool(autoDisable)}}
	data, err := c.doPostBody(ctx, "/v1/wireguard/stop", q, "")
	if err != nil {
		return nil, err
	}
	var r WireGuardActionResponse
	return &r, json.Unmarshal(data, &r)
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

func (c *Client) doPostBody(ctx context.Context, path string, query url.Values, content string) ([]byte, error) {
	u := c.baseURL + path
	if query != nil {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")
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

func (c *Client) doPut(ctx context.Context, path string, query url.Values, content string) ([]byte, error) {
	u := c.baseURL + path
	if query != nil {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, u, strings.NewReader(content))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("Content-Type", "text/plain")
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
		if shouldRetry(err, status, hasIngressAuth) && attempt < len(backoffs) {
			slog.Warn("retrying companion request", "method", req.Method, "status", status, "attempt", attempt+1, "error", err) //nolint:gosec // method is a Go HTTP constant
			time.Sleep(backoffs[attempt])
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("%s %s: %d %s: %s", req.Method, req.URL.Path, status, http.StatusText(status), string(respBody))
		}
		return respBody, nil
	}
	return nil, fmt.Errorf("%s %s: max retries exceeded", req.Method, req.URL.Path)
}

// doOnce performs a single HTTP request attempt and returns the response
// body, status code, and any transport error. Either body+status or err is
// populated, never both.
func (c *Client) doOnce(req *http.Request) ([]byte, int, error) {
	start := time.Now()
	resp, err := c.httpClient.Do(req) //nolint:gosec // URL is operator-provided config (SSRF by design for a CLI tool)
	duration := time.Since(start)
	if err != nil {
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
// Transport errors and 5xx always retry; 401 retries only when the request
// was signed (likely an expired signature).
func shouldRetry(err error, status int, signed bool) bool {
	if err != nil {
		return true
	}
	if status >= 500 {
		return true
	}
	return signed && status == http.StatusUnauthorized
}
