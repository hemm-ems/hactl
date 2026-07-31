package companion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// testIngressToken stands in for the add-on's Supervisor ingress token: 43
// characters of URL-safe base64, and — the property that makes leaking it
// worth fixing — the same 43 characters on every invocation for the life of
// the install. Invented here; the real one belongs in nobody's repository.
const testIngressToken = "TESTINGRESSTOKENvvvvvvvvvvvvvvvvvvvvvvvvvvv"

// TestErrorNamesTheRouteNotTheURL is finding #23: every failing companion call
// printed the whole request path, so a `config file does_not_exist.yaml` typo
// answered
//
//	reading config file: GET /api/hassio_ingress/<43 chars>/v1/config/file: 404 …
//
// and put a stable per-install identifier into text a user pastes into bug
// reports. The route is what distinguishes one failure from another; the
// prefix is transport.
func TestErrorNamesTheRouteNotTheURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error": {"code": 404, "message": "File not found: nope.yaml"}}`))
	}))
	defer srv.Close()

	for _, tc := range []struct {
		name    string
		baseURL string
	}{
		{"ingress", srv.URL + "/api/hassio_ingress/" + testIngressToken},
		{"ingress with a trailing slash", srv.URL + "/api/hassio_ingress/" + testIngressToken + "/"},
		// A companion reached directly has no prefix to strip; the route is
		// already the whole path and must survive unchanged.
		{"direct", srv.URL},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := New(tc.baseURL, "tok")
			_, err := c.ReadConfigFile(context.Background(), "nope.yaml")
			if err == nil {
				t.Fatal("expected a 404 error")
			}
			msg := err.Error()
			if strings.Contains(msg, testIngressToken) {
				t.Errorf("error text carries the ingress token:\n%s", msg)
			}
			if strings.Contains(msg, "hassio_ingress") {
				t.Errorf("error text carries the transport prefix:\n%s", msg)
			}
			if !strings.Contains(msg, "GET /v1/config/file") {
				t.Errorf("error text no longer names the route it failed on:\n%s", msg)
			}
			// The companion's own explanation is the useful half and must not
			// be lost with the prefix.
			if !strings.Contains(msg, "File not found: nope.yaml") {
				t.Errorf("error text dropped the companion's reason:\n%s", msg)
			}
		})
	}
}

// TestTransportErrorNamesTheRouteToo covers the other two error branches in
// doWithRetry — a transport failure and retry exhaustion — because a rule that
// holds on one of three return paths is the shape of half a fix.
func TestTransportErrorNamesTheRouteToo(t *testing.T) {
	// A closed listener: the dial fails, so doOnce returns a transport error
	// rather than a status.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	dead := srv.URL
	srv.Close()

	c := New(dead+"/api/hassio_ingress/"+testIngressToken, "tok")
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("expected a transport error against a closed listener")
	}
	if strings.Contains(err.Error(), testIngressToken) {
		t.Errorf("transport error carries the ingress token:\n%s", err.Error())
	}
	if !strings.Contains(err.Error(), "/v1/health") {
		t.Errorf("transport error does not name the route:\n%s", err.Error())
	}
}
