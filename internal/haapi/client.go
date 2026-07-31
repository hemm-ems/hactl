package haapi

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
	"strings"
	"time"

	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/httpretry"
)

// Client is a REST client for the Home Assistant API.
//
// HA REST API source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/api/__init__.py
// Config API source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/__init__.py
// Repairs API source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/repairs/websocket_api.py
// Logbook API source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/logbook/__init__.py
type Client struct {
	httpClient *http.Client
	baseURL    string
	token      string
}

// DefaultTimeout is the per-request timeout applied to new clients.
// The root command overrides it from the global --timeout flag.
var DefaultTimeout = 30 * time.Second

// DialTimeout bounds connection establishment separately from DefaultTimeout,
// so an unreachable host fails in seconds instead of consuming the full
// request timeout (slow queries stay covered by DefaultTimeout).
const DialTimeout = 5 * time.Second

// New creates a new HA API client.
func New(baseURL, token string) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		token:   token,
		httpClient: &http.Client{
			Timeout: DefaultTimeout,
			Transport: &http.Transport{
				Proxy:       http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{Timeout: DialTimeout}).DialContext,
			},
		},
	}
}

// GetAPIStatus calls GET /api/ and returns the raw JSON response body.
func (c *Client) GetAPIStatus(ctx context.Context) ([]byte, error) {
	return c.doGet(ctx, "/api/")
}

// GetConfig calls GET /api/config and returns the raw JSON body.
func (c *Client) GetConfig(ctx context.Context) ([]byte, error) {
	return c.doGet(ctx, "/api/config")
}

// GetStates calls GET /api/states and returns the raw JSON body.
func (c *Client) GetStates(ctx context.Context) ([]byte, error) {
	return c.doGet(ctx, "/api/states")
}

// GetConfigEntries calls GET /api/config/config_entries/entry and returns the raw JSON body.
func (c *Client) GetConfigEntries(ctx context.Context) ([]byte, error) {
	return c.doGet(ctx, "/api/config/config_entries/entry")
}

// GetErrorLog calls GET /api/error_log and returns the raw text body.
func (c *Client) GetErrorLog(ctx context.Context) ([]byte, error) {
	return c.doGet(ctx, "/api/error_log")
}

// GetState calls GET /api/states/<entity_id> and returns the raw JSON body.
func (c *Client) GetState(ctx context.Context, entityID string) ([]byte, error) {
	return c.doGet(ctx, "/api/states/"+entityID)
}

// GetAutomationConfig calls GET /api/config/automation/config/<id> and returns the raw JSON body.
func (c *Client) GetAutomationConfig(ctx context.Context, automationID string) ([]byte, error) {
	return c.doGet(ctx, "/api/config/automation/config/"+automationID)
}

// RenderTemplate calls POST /api/template with the given template string.
func (c *Client) RenderTemplate(ctx context.Context, template string) (string, error) {
	body := map[string]string{"template": template}
	data, err := c.doPost(ctx, "/api/template", body)
	if err != nil {
		return "", err
	}
	return string(data), nil
}


// ServiceDomain is one entry of GET /api/services: a domain and the services
// it registers, keyed by service name.
type ServiceDomain struct {
	Services map[string]ServiceDescriptor `json:"services"`
	Domain   string                       `json:"domain"`
}

// ErrServiceNotRegistered is LookupService's answer when Home Assistant
// answered, and the answer was "no such service". It is a sentinel rather than
// a nil descriptor so the caller cannot conflate it with "we could not ask",
// which is the distinction the probe exists to preserve.
var ErrServiceNotRegistered = errors.New("service not registered in Home Assistant")

// ServiceExists reports whether Home Assistant has a service registered under
// domain.service. It is LookupService with the descriptor dropped, kept for
// callers that only need the yes/no (the integration tiers probing for a
// `<domain>.reload`).
func (c *Client) ServiceExists(ctx context.Context, domain, service string) (bool, error) {
	_, err := c.LookupService(ctx, domain, service)
	if errors.Is(err, ErrServiceNotRegistered) {
		return false, nil
	}
	return err == nil, err
}

// LookupService returns what Home Assistant publishes about domain.service, or
// nil when HA has no such service registered.
//
// It exists so a dry run can refuse a call --confirm cannot make. `svc call`
// used to preview any string containing a dot without contacting HA at all, so
// "would call: light.turn_onn" was the artifact a human approved before
// --confirm — and --confirm then failed with HA's 400. The manual routes
// "turn X on" through the preview as the verification step; it verified only
// that the argument had a dot in it.
//
// The descriptor rather than a bool, because the same fetch answers the second
// half of the same question: `svc call automation.trigger --data
// '{"target":{"entity_id":[…]}}'` previewed cleanly and 400'd on --confirm,
// because HA validates service data with PREVENT_EXTRA and `target` is not a
// field of that service. What HA accepts is exactly what HA publishes here —
// hactl does not re-derive the rule, it asks (H-2).
//
// A failure to reach /api/services is reported as a transport error and a
// missing service as ErrServiceNotRegistered, so the caller can distinguish
// "HA says no such service" from "we could not ask".
func (c *Client) LookupService(ctx context.Context, domain, service string) (*ServiceDescriptor, error) {
	body, err := c.doGet(ctx, "/api/services")
	if err != nil {
		return nil, err
	}
	var domains []ServiceDomain
	if unmarshalErr := json.Unmarshal(body, &domains); unmarshalErr != nil {
		return nil, fmt.Errorf("parsing /api/services: %w", unmarshalErr)
	}
	if degErr := degeneracy.Check("/api/services", &domains); degErr != nil {
		return nil, degErr
	}
	// A live Home Assistant always registers services, so an empty list is not
	// the answer "no such service" — it is the absence of an answer, and
	// reporting it as the former would refuse calls that work (H-7). The
	// caller warns and proceeds unverified.
	if len(domains) == 0 {
		return nil, errors.New("/api/services returned no domains")
	}
	for _, d := range domains {
		if d.Domain != domain {
			continue
		}
		desc, ok := d.Services[service]
		if !ok {
			return nil, fmt.Errorf("%s.%s: %w", domain, service, ErrServiceNotRegistered)
		}
		return &desc, nil
	}
	return nil, fmt.Errorf("%s.%s: %w", domain, service, ErrServiceNotRegistered)
}

// ServiceStateChange is one entry of the array POST /api/services answers with:
// a state Home Assistant attributed to this very call, by context.
type ServiceStateChange struct {
	EntityID string `json:"entity_id"`
	State    string `json:"state"`
}

// CallService calls POST /api/services/<domain>/<service> with optional service
// data and returns the state changes HA attributed to the call.
//
// The body used to be discarded (`_, err := c.doPost(…)`), which is why every
// confirmed `svc call` reported the same unqualified success whether it had
// changed an entity or matched nothing at all. HA collects the state-change
// events carrying this call's own context and answers with them, so the one
// signal that distinguishes the two was already on the wire and in hand.
//
// The return is deliberately not an answer to "did it match anything": HA
// reports zero changes both for a call that matched nothing and for one that
// fired asynchronously (`automation.trigger` on a real automation answers `[]`,
// verified against a live instance 2026-07-31, because the automation's own
// state change carries a child context). The caller must not upgrade it.
func (c *Client) CallService(ctx context.Context, domain, service string, data any) ([]ServiceStateChange, error) {
	if data == nil {
		data = map[string]any{}
	}
	body, err := c.doPost(ctx, "/api/services/"+domain+"/"+service, data)
	if err != nil {
		return nil, err
	}
	var changed []ServiceStateChange
	if unmarshalErr := json.Unmarshal(body, &changed); unmarshalErr != nil {
		// The call succeeded; only the report of what it touched is unreadable.
		// Failing here would turn a completed write into an error, which is the
		// one thing a caller must never be told about a mutation that happened.
		slog.Warn("could not read the state changes Home Assistant reported for the call",
			"service", domain+"."+service, "error", unmarshalErr)
		return nil, nil
	}
	// Best-effort echo, the same posture as `script apply`'s state read: Check
	// poisons an entity_id-less record in place so the marker reaches the
	// reader, and the write itself is not retracted over its own receipt.
	_ = degeneracy.Check("/api/services/"+domain+"/"+service, &changed)
	return changed, nil
}

// CallServiceWithResponse calls POST /api/services/<domain>/<service>?return_response=true
// and returns the raw JSON response body (the service_response array from HA).
func (c *Client) CallServiceWithResponse(ctx context.Context, domain, service string, data any) ([]byte, error) {
	if data == nil {
		data = map[string]any{}
	}
	return c.doPost(ctx, "/api/services/"+domain+"/"+service+"?return_response=true", data)
}

// GetEvents calls GET /api/events and returns the raw JSON body.
func (c *Client) GetEvents(ctx context.Context) ([]byte, error) {
	return c.doGet(ctx, "/api/events")
}

// GetLogbook calls GET /api/logbook/<startTime> and returns the raw JSON body.
func (c *Client) GetLogbook(ctx context.Context, startTime, endTime string) ([]byte, error) {
	return c.GetLogbookFiltered(ctx, startTime, endTime, "")
}

// GetLogbookFiltered calls GET /api/logbook/<startTime>?end_time=...&entity=<id>
// and returns the raw JSON body. An empty entityID disables entity filtering
// (equivalent to GetLogbook). Pass a single entity ID; HA also supports a
// comma-separated list — entity is mutually exclusive with context_id per
// homeassistant/components/logbook/rest_api.py.
func (c *Client) GetLogbookFiltered(ctx context.Context, startTime, endTime, entityID string) ([]byte, error) {
	path := "/api/logbook/" + url.PathEscape(startTime)
	params := url.Values{}
	if endTime != "" {
		params.Set("end_time", endTime)
	}
	if entityID != "" {
		params.Set("entity", entityID)
	}
	if q := params.Encode(); q != "" {
		path += "?" + q
	}
	return c.doGet(ctx, path)
}

// HTTPStatusError is returned for a non-2xx HA response. It carries the parsed
// status code so callers can branch on the status (e.g. 404 → the integration
// ships no diagnostics platform) via HTTPStatus/errors.As instead of matching on
// the message text, which embeds up to maxLen bytes of the response body.
type HTTPStatusError struct {
	Method string
	Path   string
	Body   string // trimmed, length-capped response body ("" when empty)
	Status int
}

// Error renders the status line, appending HA's error detail — e.g.
// {"message": "Expected ... for dictionary value @ data['advanced']"} on a 400 —
// when the body is non-empty, instead of swallowing it behind a bare status.
func (e *HTTPStatusError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("%s %s: %d %s", e.Method, e.Path, e.Status, http.StatusText(e.Status))
	}
	return fmt.Sprintf("%s %s: %d %s: %s", e.Method, e.Path, e.Status, http.StatusText(e.Status), e.Body)
}

// HTTPStatus reports the HTTP status code carried by err when err (or anything
// it wraps) is an *HTTPStatusError. The second return is false otherwise, so a
// transport error is not mistaken for status 0.
func HTTPStatus(err error) (int, bool) {
	var se *HTTPStatusError
	if errors.As(err, &se) {
		return se.Status, true
	}
	return 0, false
}

// NewHTTPStatusError builds an *HTTPStatusError from a non-2xx response,
// trimming and length-capping the body.
//
// Exported because the companion client raises the same kind of failure from
// its own transport and used to report it as a bare fmt.Errorf: identical text,
// no type, so a caller wanting "was that a 404?" had to match on the message —
// the thing HTTPStatus exists to stop. One constructor keeps the body cap in
// one place too.
func NewHTTPStatusError(method, path string, status int, body []byte) error {
	const maxLen = 500
	msg := strings.TrimSpace(string(body))
	if len(msg) > maxLen {
		msg = msg[:maxLen] + "…"
	}
	return &HTTPStatusError{Method: method, Path: path, Status: status, Body: msg}
}

func httpStatusError(method, path string, status int, body []byte) error {
	return NewHTTPStatusError(method, path, status, body)
}

func (c *Client) doGet(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	return c.doWithRetry(req)
}

func (c *Client) doDelete(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	return c.doWithRetry(req)
}

func (c *Client) doPost(ctx context.Context, path string, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding request body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	return c.doWithRetry(req)
}

// doPostOnce is like doPost but does not retry on 5xx server errors.
// Use for operations where retrying is harmful (e.g. config flow start that
// hangs when the integration fails to load).
func (c *Client) doPostOnce(ctx context.Context, path string, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding request body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	return c.doOnce(req)
}

// doOnce executes a request without retry. Returns immediately on error or non-2xx.
func (c *Client) doOnce(req *http.Request) ([]byte, error) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := c.httpClient.Do(req) //nolint:gosec // URL is constructed from user-provided baseURL by design
	duration := time.Since(start)

	if err != nil {
		slog.Debug("HTTP request failed", "method", req.Method, "error", err, "duration", duration) //nolint:gosec // structured log
		return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
	}

	respBody, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	slog.Debug("HTTP request", "method", req.Method, "status", resp.StatusCode, "duration", duration) //nolint:gosec // structured log

	if readErr != nil {
		return nil, fmt.Errorf("reading response body: %w", readErr)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, httpStatusError(req.Method, req.URL.Path, resp.StatusCode, respBody)
	}

	return respBody, nil
}

func (c *Client) doWithRetry(req *http.Request) ([]byte, error) {
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")

	var bodyBytes []byte
	if req.Body != nil {
		var err error
		bodyBytes, err = io.ReadAll(req.Body)
		if err != nil {
			return nil, fmt.Errorf("reading request body: %w", err)
		}
		_ = req.Body.Close()
	}

	maxAttempts := 3
	backoff := func(attempt int) time.Duration {
		return 500 * time.Millisecond << attempt // 500ms, 1s
	}

	for attempt := range maxAttempts {
		if bodyBytes != nil {
			req.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		}

		start := time.Now()
		resp, err := c.httpClient.Do(req) //nolint:gosec // URL is constructed from user-provided baseURL by design
		duration := time.Since(start)

		if err != nil {
			// Transport errors (refused, DNS failure, timeout) are not
			// retried: an unreachable HA doesn't heal in 500ms, and
			// retrying compounds the stall. Only 5xx responses retry.
			slog.Debug("HTTP request failed", "method", req.Method, "error", err, "duration", duration) //nolint:gosec // structured log
			return nil, fmt.Errorf("%s %s: %w", req.Method, req.URL.Path, err)
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()

		slog.Debug("HTTP request", "method", req.Method, "status", resp.StatusCode, "duration", duration) //nolint:gosec // structured log

		// A 5xx means Home Assistant received the request and may already have
		// acted on it, so only an idempotent method may be re-sent (H-1). This
		// check used to be absent: every POST retried, so `svc call --confirm`
		// against an HA that delivered a notification and *then* raised sent it
		// three times, and `config flow-step --confirm` could create three
		// config entries. The companion client had the rule and this one did
		// not, which is why the predicate now lives in one shared package.
		if resp.StatusCode >= 500 && attempt < maxAttempts-1 && httpretry.IsIdempotent(req.Method) {
			slog.Debug("retrying request due to server error", "method", req.Method, "status", resp.StatusCode, "attempt", attempt+1) //nolint:gosec // structured log
			time.Sleep(backoff(attempt))
			continue
		}

		if readErr != nil {
			return nil, fmt.Errorf("reading response body: %w", readErr)
		}

		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, httpStatusError(req.Method, req.URL.Path, resp.StatusCode, respBody)
		}

		return respBody, nil
	}

	// unreachable
	return nil, fmt.Errorf("%s %s: max retries exceeded", req.Method, req.URL.Path)
}
