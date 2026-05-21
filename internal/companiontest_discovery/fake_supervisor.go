//go:build companion_discovery

// Package companiontest_discovery exercises hactl's companion-discovery code
// path end-to-end against a real Companion HTTP service and an in-process
// Fake-Supervisor that speaks the subset of the HA WebSocket protocol that
// hactl uses for Supervisor discovery and ingress-URL signing.
//
// The Fake intentionally does not emulate the full HA WS API. Calls that
// hactl never makes are not implemented and will return a typed error if
// invoked, so missing coverage surfaces as a clear test failure rather than
// silent drift.
package companiontest_discovery

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ingressPrefix is the deterministic ingress-URL prefix the Fake returns from
// /addons/<slug>/info. Stays stable across runs so tests can assert on it.
const ingressPrefix = "/api/hassio_ingress/fakeid/"

// addonSlug is the Supervisor-style installed slug (repo-prefixed) the Fake
// reports for the companion. Mirrors how a real HA installation would name it.
const addonSlug = "4f607318_hactl_companion"

type fakeSupervisor struct {
	server   *http.Server
	listener net.Listener
	upgrader websocket.Upgrader
	proxy    *httputil.ReverseProxy

	// requireSession enforces presence of a valid ingress_session cookie on
	// every ingress-proxy request. Off by default (PR 2 era); flipped on by
	// tests that exercise the Ingress auth flow.
	requireSession bool
	// sessionCounter is incremented per /ingress/session call so each issued
	// token is distinct — used by tests to assert re-auth on 401.
	sessionCounter atomic.Int64

	mu             sync.Mutex
	wsRequests     []wsRequest
	httpHits       atomic.Int64
	issuedSessions map[string]bool // tracks which session tokens we've handed out
}

type wsRequest struct {
	Type     string         `json:"type"`
	Endpoint string         `json:"endpoint,omitempty"`
	Method   string         `json:"method,omitempty"`
	Slug     string         `json:"slug,omitempty"`
	Path     string         `json:"path,omitempty"`
	Raw      map[string]any `json:"-"`
}

// startFakeSupervisor binds a listener on a free port, proxies HTTP requests
// under ingressPrefix to companionURL, and returns the URL clients should use
// as their HA base.
func startFakeSupervisor(companionURL string) (*fakeSupervisor, error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("listening: %w", err)
	}

	companionParsed, err := url.Parse(companionURL)
	if err != nil {
		_ = listener.Close()
		return nil, fmt.Errorf("parsing companion URL: %w", err)
	}

	f := &fakeSupervisor{
		listener: listener,
		upgrader: websocket.Upgrader{
			CheckOrigin: func(*http.Request) bool { return true },
		},
		proxy:          httputil.NewSingleHostReverseProxy(companionParsed),
		issuedSessions: make(map[string]bool),
	}

	// Strip the ingress prefix before forwarding and inject the header the
	// Companion's auth middleware looks for. This mirrors what HA's Ingress
	// proxy does in production.
	originalDirector := f.proxy.Director
	f.proxy.Director = func(req *http.Request) {
		req.URL.Path = strings.TrimPrefix(req.URL.Path, strings.TrimRight(ingressPrefix, "/"))
		if req.URL.Path == "" {
			req.URL.Path = "/"
		}
		req.Header.Set("X-Ingress-Path", strings.TrimRight(ingressPrefix, "/"))
		originalDirector(req)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/api/websocket", f.handleWS)
	mux.HandleFunc(ingressPrefix, f.handleIngress)

	f.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := f.server.Serve(listener); err != nil && err != http.ErrServerClosed {
			slog.Error("fake supervisor server", "error", err)
		}
	}()

	return f, nil
}

// BaseURL is the http://… URL clients should pass as HA_URL.
func (f *fakeSupervisor) BaseURL() string {
	return "http://" + f.listener.Addr().String()
}

// Shutdown stops the HTTP server and frees the port.
func (f *fakeSupervisor) Shutdown() error {
	return f.server.Close()
}

// WSRequests returns a copy of all WS messages received since boot. Useful for
// asserting wire-format-pin tests.
func (f *fakeSupervisor) WSRequests() []wsRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]wsRequest, len(f.wsRequests))
	copy(out, f.wsRequests)
	return out
}

// HTTPHits returns how many HTTP requests landed on the ingress proxy.
func (f *fakeSupervisor) HTTPHits() int64 {
	return f.httpHits.Load()
}

func (f *fakeSupervisor) handleIngress(w http.ResponseWriter, r *http.Request) {
	f.httpHits.Add(1)
	if f.requireSession {
		cookie, err := r.Cookie("ingress_session")
		f.mu.Lock()
		known := err == nil && f.issuedSessions[cookie.Value]
		f.mu.Unlock()
		if !known {
			http.Error(w, "fake supervisor: missing or unknown ingress_session cookie", http.StatusUnauthorized)
			return
		}
	}
	f.proxy.ServeHTTP(w, r)
}

// SetRequireSession toggles enforcement of an ingress_session cookie on the
// ingress proxy. Tests that exercise the Ingress auth flow turn this on;
// default-off keeps the discovery-only tests independent of the auth path.
func (f *fakeSupervisor) SetRequireSession(on bool) {
	f.requireSession = on
}

// InvalidateSessions wipes the set of accepted session tokens. Tests use
// this to simulate Supervisor expiring a session mid-flight — the next
// request must 401 and trigger a fresh /ingress/session fetch.
func (f *fakeSupervisor) InvalidateSessions() {
	f.mu.Lock()
	f.issuedSessions = make(map[string]bool)
	f.mu.Unlock()
}

func (f *fakeSupervisor) handleWS(w http.ResponseWriter, r *http.Request) {
	conn, err := f.upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("fake supervisor: ws upgrade", "error", err)
		return
	}
	defer conn.Close() //nolint:errcheck

	if err := conn.WriteJSON(map[string]string{
		"type":       "auth_required",
		"ha_version": "2026.5.0-fake",
	}); err != nil {
		return
	}

	var authMsg map[string]any
	if err := conn.ReadJSON(&authMsg); err != nil {
		return
	}
	// Accept any token.
	if err := conn.WriteJSON(map[string]string{
		"type":       "auth_ok",
		"ha_version": "2026.5.0-fake",
	}); err != nil {
		return
	}

	for {
		var msg map[string]any
		if err := conn.ReadJSON(&msg); err != nil {
			return
		}

		f.recordWS(msg)
		resp := f.dispatch(msg)
		if err := conn.WriteJSON(resp); err != nil {
			return
		}
	}
}

func (f *fakeSupervisor) recordWS(msg map[string]any) {
	rec := wsRequest{Raw: msg}
	if v, ok := msg["type"].(string); ok {
		rec.Type = v
	}
	if v, ok := msg["endpoint"].(string); ok {
		rec.Endpoint = v
	}
	if v, ok := msg["method"].(string); ok {
		rec.Method = v
	}
	if v, ok := msg["slug"].(string); ok {
		rec.Slug = v
	}
	if v, ok := msg["path"].(string); ok {
		rec.Path = v
	}
	f.mu.Lock()
	f.wsRequests = append(f.wsRequests, rec)
	f.mu.Unlock()
}

func (f *fakeSupervisor) dispatch(msg map[string]any) map[string]any {
	id := msg["id"]
	cmdType, _ := msg["type"].(string)

	switch cmdType {
	case "supervisor/api":
		return f.dispatchSupervisorAPI(id, msg)
	case "hassio/api", "hassio/addon/info":
		// Legacy command names HA Core has never registered (verified against
		// 2026.4.4 — the registered namespace is `supervisor/*`). Mimic HA's
		// "Unknown command." response so regressions to the wrong name fail
		// loudly in tests rather than silently working only against the fake.
		return errResp(id, "unknown_command",
			fmt.Sprintf("Unknown command: %s (use supervisor/api)", cmdType))
	default:
		return errResp(id, "unknown_command", "Unknown command: "+cmdType)
	}
}

func (f *fakeSupervisor) dispatchSupervisorAPI(id any, msg map[string]any) map[string]any {
	endpoint, _ := msg["endpoint"].(string)
	method, _ := msg["method"].(string)
	if method == "" {
		method = "get"
	}

	switch {
	case endpoint == "/addons" && strings.EqualFold(method, "get"):
		return okResp(id, map[string]any{
			"addons": []map[string]any{
				{
					"slug":    addonSlug,
					"name":    "hactl companion",
					"state":   "started",
					"version": "2026.5.11",
					"ingress": true,
				},
				{
					"slug":  "core_zwave_js",
					"name":  "Z-Wave JS",
					"state": "started",
				},
			},
		})
	case endpoint == "/addons/"+addonSlug+"/info" && strings.EqualFold(method, "get"):
		return okResp(id, map[string]any{
			"slug":        addonSlug,
			"name":        "hactl companion",
			"state":       "started",
			"version":     "2026.5.11",
			"ingress":     true,
			"ingress_url": ingressPrefix,
		})
	case endpoint == "/info" && strings.EqualFold(method, "get"):
		return okResp(id, map[string]any{
			"supervisor":    "2025.05.0-fake",
			"homeassistant": "2026.5.0-fake",
			"hassos":        nil,
		})
	case endpoint == "/ingress/session" && strings.EqualFold(method, "post"):
		n := f.sessionCounter.Add(1)
		tok := fmt.Sprintf("fake-sess-%d", n)
		f.mu.Lock()
		f.issuedSessions[tok] = true
		f.mu.Unlock()
		return okResp(id, map[string]any{"session": tok})
	default:
		return errResp(id, "not_found",
			fmt.Sprintf("fake supervisor: supervisor/api endpoint %q method %q not implemented", endpoint, method))
	}
}

func okResp(id any, result map[string]any) map[string]any {
	resBytes, _ := json.Marshal(result)
	return map[string]any{
		"id":      id,
		"type":    "result",
		"success": true,
		"result":  json.RawMessage(resBytes),
	}
}

func errResp(id any, code, message string) map[string]any {
	return map[string]any{
		"id":      id,
		"type":    "result",
		"success": false,
		"error": map[string]string{
			"code":    code,
			"message": message,
		},
	}
}
