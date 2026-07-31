package haapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// H-23 — every connection hactl opens is bounded by the caller's --timeout, and
// a redirect that moves the origin is reported rather than followed.
//
// Live-fire #73 and #76. The three transports (REST, WebSocket, companion HTTP)
// were built independently and only the REST one carried the flag: `companion
// status --timeout 1s` against a blackholed host took 10.02s and exited 0,
// while `health --timeout 1s` and `ent ls --timeout 1s` against the identical
// host aborted at 1.01s. The WS client had no overall bound at all — a constant
// 5s dial, attempted twice.

// hangingHost is a listener that completes the TCP handshake and then answers
// nothing, ever. It is the deterministic form of the blackholed host #73 was
// reported against: an unroutable address answers differently on every network
// (i/o timeout here, EHOSTUNREACH there), and what the rule is about is the
// bound, not which syscall reports the stall.
func hangingHost(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			// Held open and never written to. Closing it would make the peer
			// fail fast, which is the opposite of the condition under test.
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()
	return "http://" + ln.Addr().String()
}

// withTimeout sets the process-wide per-request bound the --timeout flag writes
// to, and restores it. The flag is a package variable rather than a field
// because every client construction in the product reads it; that is also why
// the surface derived in internal/surfaceaudit is keyed on constructions.
func withTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := DefaultTimeout
	DefaultTimeout = d
	t.Cleanup(func() { DefaultTimeout = prev })
}

// TestWSConnectHonoursTheCallerTimeout — finding #73.
//
// The assertion is an upper bound on wall time, not an error string: what the
// caller asked for is "come back within a second", and every way of not doing
// that is the same defect. The margin is generous on purpose — this must fail
// for the reported reason (a hardcoded multi-second constant) and never for a
// slow machine.
func TestWSConnectHonoursTheCallerTimeout(t *testing.T) {
	withTimeout(t, 500*time.Millisecond)
	ws := NewWSClient(hangingHost(t), "token")

	start := time.Now()
	err := ws.Connect(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("Connect against a host that never answers must fail")
	}
	if elapsed > 2*time.Second {
		t.Errorf("Connect took %v with --timeout 500ms: the caller's bound never reached the transport", elapsed)
	}
}

// TestHTTPClientIsBoundedByTheCallerTimeout — the HTTP half of H-23, and the
// reason both HTTP callers now build their client here.
//
// The REST client already honoured the flag, which is exactly why the defect
// was invisible: `health` and `ent ls` aborted at 1.01s and nothing suggested a
// third transport existed that did not. The bound is asserted end to end
// against a server that never answers rather than by reading the field back,
// because a Timeout that is set and then overridden by a longer dial deadline
// is set and wrong.
func TestHTTPClientIsBoundedByTheCallerTimeout(t *testing.T) {
	withTimeout(t, 500*time.Millisecond)

	start := time.Now()
	_, err := New(hangingHost(t), "token").GetConfig(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a GET against a host that never answers must fail")
	}
	if elapsed > 2*time.Second {
		t.Errorf("the request took %v with --timeout 500ms", elapsed)
	}
}

// TestWSConnectRemembersWhyItFailed — the half of finding #75 that lives here.
//
// The companion's discovery reason was derived from a nil client rather than
// from the error that produced it, so an authentication failure and a blackholed
// host both rendered as "unreachable". The classification has to be able to ask,
// which means the client has to remember.
func TestWSConnectRemembersWhyItFailed(t *testing.T) {
	withTimeout(t, 5*time.Second)
	ws := NewWSClient(hangingHost(t), "token")
	connErr := ws.Connect(context.Background())
	if connErr == nil {
		t.Fatal("Connect against a host that never answers must fail")
	}
	if ws.Connected() {
		t.Error("a client whose Connect failed reports itself connected")
	}
	if !errors.Is(ws.ConnectError(), connErr) {
		t.Errorf("ConnectError() = %v, want the error Connect returned (%v)", ws.ConnectError(), connErr)
	}
}

// TestWSConnectReportsAnInvalidTokenAsAuth — HA answers `auth_invalid` on the
// open socket, which is not a transport failure at all. #75 rendered it as
// "unreachable" and told the reader to check their network.
func TestWSConnectReportsAnInvalidTokenAsAuth(t *testing.T) {
	srv := startWSTestServerWithAuth(t, false)
	defer srv.Close()

	ws := NewWSClient(srv.URL, "wrong")
	err := ws.Connect(context.Background())
	if err == nil {
		t.Fatal("Connect with a token HA rejects must fail")
	}
	var ae *AuthError
	if !errors.As(err, &ae) {
		t.Fatalf("Connect error is %T (%v), want *AuthError so a caller can tell it from a network failure", err, err)
	}
}

// startWSTestServerWithAuth answers the auth handshake with auth_ok or
// auth_invalid, and nothing else.
func startWSTestServerWithAuth(t *testing.T, ok bool) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.WriteJSON(map[string]string{"type": "auth_required", "ha_version": "2026.7"})
		var authMsg map[string]string
		_ = c.ReadJSON(&authMsg)
		if ok {
			_ = c.WriteJSON(map[string]string{"type": "auth_ok", "ha_version": "2026.7"})
			return
		}
		_ = c.WriteJSON(map[string]string{"type": "auth_invalid", "message": "Invalid access token or password"})
	}))
}

// redirectingOrigin serves a 301 to another origin for every path, which is what
// a reverse proxy fronting Home Assistant does when HA_URL names the http port.
func redirectingOrigin(t *testing.T, to string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, to+r.URL.Path, http.StatusMovedPermanently)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// TestRESTRefusesARedirectThatMovesTheOrigin — finding #76.
//
// `hactl health` against an http URL the server 301s to https returned real
// version, state, location and timezone — the REST client followed the redirect
// with the credentials intact — beside `errors: -1` and `companion: not found
// (unreachable)`, because a WebSocket cannot follow a redirect at all. Exit 0,
// and nothing in the output named the scheme. Half an answer is the one shape
// this must not be.
func TestRESTRefusesARedirectThatMovesTheOrigin(t *testing.T) {
	withTimeout(t, 5*time.Second)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"2026.7.4","state":"RUNNING"}`))
	}))
	defer upstream.Close()

	c := New(redirectingOrigin(t, upstream.URL), "token")
	_, err := c.GetConfig(context.Background())
	if err == nil {
		t.Fatal("a REST call that silently landed on another origin reported success")
	}
	var re *RedirectError
	if !errors.As(err, &re) {
		t.Fatalf("error is %T (%v), want *RedirectError naming the origin to configure", err, err)
	}
	if re.Redirect != upstream.URL {
		t.Errorf("RedirectError names %q; the server sent us to %q", re.Redirect, upstream.URL)
	}
}

// TestRESTFollowsARedirectWithinTheSameOrigin is the boundary. A trailing-slash
// or path redirect from the same host is the server tidying its own URL space,
// not a different instance, and refusing those would break Ingress.
func TestRESTFollowsARedirectWithinTheSameOrigin(t *testing.T) {
	withTimeout(t, 5*time.Second)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/config/", http.StatusMovedPermanently)
	})
	mux.HandleFunc("/api/config/", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"version":"2026.7.4","state":"RUNNING"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	body, err := New(srv.URL, "token").GetConfig(context.Background())
	if err != nil {
		t.Fatalf("a same-origin redirect must be followed: %v", err)
	}
	if len(body) == 0 {
		t.Error("the followed redirect returned no body")
	}
}

// TestWSConnectReportsARedirectAsTheSchemeProblemItIs — the other half of #76.
//
// The WS half of that instance failed with `websocket: bad handshake`, which
// says nothing about the cause. gorilla hands back the 301 response it got; the
// evidence was there and discarded.
func TestWSConnectReportsARedirectAsTheSchemeProblemItIs(t *testing.T) {
	withTimeout(t, 5*time.Second)
	upstream := startWSTestServerWithAuth(t, true)
	defer upstream.Close()

	ws := NewWSClient(redirectingOrigin(t, upstream.URL), "token")
	err := ws.Connect(context.Background())
	if err == nil {
		t.Fatal("a WS dial that was redirected elsewhere must fail")
	}
	var re *RedirectError
	if !errors.As(err, &re) {
		t.Fatalf("error is %T (%v), want *RedirectError — the 301 response carried the cause", err, err)
	}
}
