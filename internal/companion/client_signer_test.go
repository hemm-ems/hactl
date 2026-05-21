package companion

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

var errSessionFailed = errors.New("simulated ingress session failure")

// fakeIngressAuth records IngressSession calls and returns deterministic
// session tokens so tests can verify cookie wiring and re-auth behavior.
type fakeIngressAuth struct {
	calls       atomic.Int64
	failNthCall int    // 0 = never; 1-based index of call to fail
	failWith    error
	tokenPrefix string // defaults to "fake-sess"
}

func (f *fakeIngressAuth) IngressSession(_ context.Context) (string, error) {
	n := f.calls.Add(1)
	if f.failNthCall > 0 && int64(f.failNthCall) == n {
		return "", f.failWith
	}
	prefix := f.tokenPrefix
	if prefix == "" {
		prefix = "fake-sess"
	}
	return prefix + "-" + strconvI64(n), nil
}

func strconvI64(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}

func TestClient_AttachesIngressSessionCookie(t *testing.T) {
	var cookieVal string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("ingress_session")
		if err != nil {
			http.Error(w, "missing ingress_session cookie", http.StatusUnauthorized)
			return
		}
		cookieVal = c.Value
		_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
	}))
	defer srv.Close()

	auth := &fakeIngressAuth{}
	c := New(srv.URL+"/api/hassio_ingress/abc", "tok").WithIngressAuth(auth)

	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if cookieVal != "fake-sess-1" {
		t.Errorf("cookie value = %q, want fake-sess-1", cookieVal)
	}
	if auth.calls.Load() != 1 {
		t.Errorf("IngressSession calls = %d, want 1", auth.calls.Load())
	}
}

func TestClient_DoesNotFetchSessionForDirectURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("ingress_session"); err == nil {
			t.Error("direct URL should not carry ingress_session cookie")
		}
		_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
	}))
	defer srv.Close()

	auth := &fakeIngressAuth{}
	c := New(srv.URL, "tok").WithIngressAuth(auth)

	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if auth.calls.Load() != 0 {
		t.Errorf("IngressSession unexpectedly invoked for direct URL: %d calls", auth.calls.Load())
	}
}

func TestClient_CachesSessionAcrossRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("ingress_session")
		if err != nil {
			http.Error(w, "missing", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok","version":"` + c.Value + `"}`))
	}))
	defer srv.Close()

	auth := &fakeIngressAuth{}
	c := New(srv.URL+"/api/hassio_ingress/abc", "tok").WithIngressAuth(auth)

	for range 3 {
		if _, err := c.Health(context.Background()); err != nil {
			t.Fatalf("Health: %v", err)
		}
	}
	if auth.calls.Load() != 1 {
		t.Errorf("IngressSession calls = %d, want 1 (token cached across requests)", auth.calls.Load())
	}
}

func TestClient_RefreshesSessionOnUnauthorized(t *testing.T) {
	var first, second string
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		i := n.Add(1)
		c, _ := r.Cookie("ingress_session")
		val := ""
		if c != nil {
			val = c.Value
		}
		switch i {
		case 1:
			first = val
			http.Error(w, "expired", http.StatusUnauthorized)
		case 2:
			second = val
			_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
		default:
			t.Errorf("unexpected 3rd request")
		}
	}))
	defer srv.Close()

	auth := &fakeIngressAuth{}
	c := New(srv.URL+"/api/hassio_ingress/x", "tok").WithIngressAuth(auth)

	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health after refresh: %v", err)
	}
	if auth.calls.Load() != 2 {
		t.Errorf("IngressSession calls = %d, want 2 (one initial, one refresh after 401)", auth.calls.Load())
	}
	if first == "" || second == "" || first == second {
		t.Errorf("retries should use distinct sessions, got %q then %q", first, second)
	}
}

func TestClient_ReturnsErrorAfterMaxRetries(t *testing.T) {
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		http.Error(w, "always 401", http.StatusUnauthorized)
	}))
	defer srv.Close()

	auth := &fakeIngressAuth{}
	c := New(srv.URL+"/api/hassio_ingress/x", "tok").WithIngressAuth(auth)

	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("Health should fail after exhausting retries")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401, got: %v", err)
	}
	if n.Load() != 3 {
		t.Errorf("requests = %d, want 3 (max attempts)", n.Load())
	}
}

func TestClient_IngressAuthFailurePropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server should not be hit when session fetch fails")
	}))
	defer srv.Close()

	auth := &fakeIngressAuth{failNthCall: 1, failWith: errSessionFailed}
	c := New(srv.URL+"/api/hassio_ingress/x", "tok").WithIngressAuth(auth)

	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("Health should fail when session fetch errors")
	}
	if !strings.Contains(err.Error(), "fetching ingress session") {
		t.Errorf("error should wrap session failure, got: %v", err)
	}
}
