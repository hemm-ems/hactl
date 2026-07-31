package haapi

import (
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// INVARIANTS.md H-23 — every connection hactl opens is bounded by the caller's
// `--timeout`, and a redirect that moves the origin is reported rather than
// followed.
//
// hactl opens three kinds of connection: REST to Home Assistant, a WebSocket to
// Home Assistant, and HTTP to the companion. They were built independently and
// only the REST one carried the flag. `companion status --timeout 1s` against a
// host that never answers took 10.02s and exited 0, with `--timeout 3s` and
// `--timeout 20s` landing on the same 10.02s, while `health --timeout 1s` and
// `ent ls --timeout 1s` against the identical host aborted at 1.01s (#73). The
// documented meaning of the flag — "per-request timeout for HA/companion API
// calls" — held for two of the three transports.
//
// This file is where the three agree. A transport constructed anywhere else is
// a site the surface derived by internal/surfaceaudit.TransportSurface reports,
// so the fourth transport cannot be added without saying what bounds it.

// handshakeTimeout bounds the WebSocket upgrade separately from the TCP dial,
// for the same reason DialTimeout exists: a host that accepts the connection and
// then answers nothing stalls after the dial has already succeeded, which is
// the shape a hung reverse proxy takes.
const handshakeTimeout = 10 * time.Second

// dialBound is how long connection establishment may take.
//
// It is the smaller of the two: DialTimeout so an unreachable host fails in
// seconds rather than consuming the whole request budget, and DefaultTimeout so
// a caller who asked for less than that gets it. A bound larger than the
// caller's own is not a bound.
func dialBound() time.Duration { return min(DialTimeout, DefaultTimeout) }

// handshakeBound is dialBound for the WebSocket upgrade.
func handshakeBound() time.Duration { return min(handshakeTimeout, DefaultTimeout) }

// RedirectError is returned when the server answers with a redirect to an origin
// other than the configured one.
//
// hactl talks to the URL it was configured with. Following the redirect is what
// a browser does, and it is why `hactl health` against an http:// URL that a
// reverse proxy 301s to https:// returned a real HA version, state, location and
// timezone beside `errors: -1` and `companion: not found (unreachable)` at exit
// 0: the REST client followed it with the credentials intact and the WebSocket —
// which cannot follow a redirect at all, there being no such thing in the
// protocol — did not (#76). Half the product worked and nothing said why.
//
// So the redirect is a fact about the configuration, reported once, in the same
// words by both transports, naming the line to change.
type RedirectError struct {
	// Configured is the origin from .env: scheme://host[:port].
	Configured string
	// Redirect is the origin the server pointed at instead.
	Redirect string
}

func (e *RedirectError) Error() string {
	return fmt.Sprintf(
		"%s redirects to %s — hactl talks to the URL it was configured with, and a WebSocket cannot follow a redirect at all; set HA_URL=%s in .env",
		e.Configured, e.Redirect, e.Redirect)
}

// AuthError is returned when Home Assistant rejected the token on the WebSocket
// auth handshake.
//
// It is typed because it is not a transport failure and must not be classified
// as one: the socket opened, HA answered, and the answer was "no". Rendering it
// as "unreachable" told the reader to check their network and Ingress while the
// error one line above said `authentication failed: Invalid access token or
// password` (#75).
type AuthError struct{ Message string }

func (e *AuthError) Error() string { return "authentication failed: " + e.Message }

// Origin reduces a URL to the part that decides whether two requests reach the
// same instance: scheme, host and port. Path, query and credentials are not part
// of it — a redirect from /api/config to /api/config/ is the server tidying its
// own URL space, and refusing those would break Ingress, which is served under a
// path prefix.
func Origin(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" {
		return ""
	}
	scheme := u.Scheme
	switch scheme {
	// A WebSocket URL and the http URL it was derived from name the same
	// origin; the comparison would otherwise report every WS dial as moved.
	case "ws":
		scheme = "http"
	case "wss":
		scheme = "https"
	}
	return scheme + "://" + strings.ToLower(u.Host)
}

// maxRedirects is Go's own default hop limit, restated because installing a
// CheckRedirect replaces it.
const maxRedirects = 10

// originGuard builds the CheckRedirect policy for a client configured to talk to
// baseURL. Same-origin redirects are followed to the same depth the standard
// library allows; anything that moves the origin ends the request with a
// *RedirectError.
func originGuard(baseURL string) func(*http.Request, []*http.Request) error {
	configured := Origin(baseURL)
	return func(req *http.Request, via []*http.Request) error {
		if got := Origin(req.URL.String()); configured != "" && got != configured {
			return &RedirectError{Configured: configured, Redirect: got}
		}
		if len(via) >= maxRedirects {
			return fmt.Errorf("stopped after %d redirects", maxRedirects)
		}
		return nil
	}
}

// HTTPClient is the http.Client every HTTP caller in the product uses: the HA
// REST client here and the companion client in internal/companion, which is the
// other origin a hactl process talks to.
//
// baseURL is what the client was configured with, and it is a parameter rather
// than being read back off the client because the redirect policy needs to know
// where the caller MEANT to go, which a request in mid-redirect no longer says.
func HTTPClient(baseURL string) *http.Client {
	return &http.Client{
		Timeout: DefaultTimeout,
		Transport: &http.Transport{
			Proxy:       http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{Timeout: dialBound()}).DialContext,
		},
		CheckRedirect: originGuard(baseURL),
	}
}

// redirectFromHandshake reads the cause out of a failed WebSocket upgrade.
//
// gorilla returns the HTTP response alongside ErrBadHandshake, and on a redirect
// that response is the 3xx with its Location — the evidence #76 needed, thrown
// away at the call site because the response was assigned to `_`. A WebSocket
// client cannot follow the redirect (the protocol has no such step), so the only
// question is whether it says so.
func redirectFromHandshake(baseURL string, resp *http.Response) *RedirectError {
	if resp == nil || resp.StatusCode < 300 || resp.StatusCode > 399 {
		return nil
	}
	loc := resp.Header.Get("Location")
	if loc == "" {
		return nil
	}
	// A Location header that does not parse is not evidence of anything, so the
	// parse failure is the same answer as "no redirect here": let the caller
	// report the handshake error it already has.
	target, _ := url.Parse(loc)
	if target == nil {
		return nil
	}
	if base, baseErr := url.Parse(baseURL); baseErr == nil {
		target = base.ResolveReference(target)
	}
	configured, got := Origin(baseURL), Origin(target.String())
	if configured == "" || got == "" || configured == got {
		return nil
	}
	return &RedirectError{Configured: configured, Redirect: got}
}
