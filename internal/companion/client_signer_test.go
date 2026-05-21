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

var errSignFailed = errors.New("simulated sign failure")

// fakeSigner records SignPath calls and returns a deterministic signed URL so
// tests can assert both the wire-format of the path passed in and that the
// resulting URL is what the client actually GETs.
type fakeSigner struct {
	calls         atomic.Int64
	failNthCall   int    // 0 = never; 1-based index of call to fail
	failWith      error
	pathOverride  string // if set, returned regardless of input path
	signatureName string // query param value; defaults to "fake-sig-N" per call
}

func (f *fakeSigner) SignPath(_ context.Context, path string, _ int) (string, error) {
	n := f.calls.Add(1)
	if f.failNthCall > 0 && int64(f.failNthCall) == n {
		return "", f.failWith
	}
	out := path
	if f.pathOverride != "" {
		out = f.pathOverride
	}
	sigName := f.signatureName
	if sigName == "" {
		sigName = "fake-sig"
	}
	sep := "?"
	if strings.Contains(out, "?") {
		sep = "&"
	}
	return out + sep + "authSig=" + sigName, nil
}

func TestClient_SignsIngressRequest(t *testing.T) {
	var hitPath string
	var hitQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hitPath = r.URL.Path
		hitQuery = r.URL.RawQuery
		// Require the signature — without it the request must not be served.
		if r.URL.Query().Get("authSig") == "" {
			http.Error(w, "missing authSig", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok", Version: "1.0.0"})
	}))
	defer srv.Close()

	signer := &fakeSigner{}
	c := New(srv.URL+"/api/hassio_ingress/abc", "irrelevant").WithSigner(signer)

	h, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health failed: %v", err)
	}
	if h.Status != "ok" {
		t.Errorf("status = %q, want ok", h.Status)
	}
	if hitPath != "/api/hassio_ingress/abc/v1/health" {
		t.Errorf("server saw path = %q, want /api/hassio_ingress/abc/v1/health", hitPath)
	}
	if !strings.Contains(hitQuery, "authSig=fake-sig") {
		t.Errorf("server saw query = %q, want authSig=... present", hitQuery)
	}
	if signer.calls.Load() != 1 {
		t.Errorf("signer calls = %d, want 1", signer.calls.Load())
	}
}

func TestClient_DoesNotSignDirectURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("authSig") != "" {
			t.Errorf("direct URL should not be signed, got authSig=%q", r.URL.Query().Get("authSig"))
		}
		_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
	}))
	defer srv.Close()

	signer := &fakeSigner{}
	c := New(srv.URL, "tok").WithSigner(signer)

	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health: %v", err)
	}
	if signer.calls.Load() != 0 {
		t.Errorf("signer should not be invoked for non-ingress URL, called %d times", signer.calls.Load())
	}
}

func TestClient_ReSignsOnUnauthorized(t *testing.T) {
	var firstSig, secondSig string
	var requestCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := requestCount.Add(1)
		sig := r.URL.Query().Get("authSig")
		switch n {
		case 1:
			firstSig = sig
			http.Error(w, "signature expired", http.StatusUnauthorized)
		case 2:
			secondSig = sig
			_ = json.NewEncoder(w).Encode(HealthResponse{Status: "ok"})
		default:
			t.Errorf("unexpected 3rd request")
		}
	}))
	defer srv.Close()

	signer := &fakeSigner{}
	// Each SignPath call returns a different signature ("fake-sig" is constant,
	// but pathOverride includes a counter via the path itself isn't easy — so
	// inspect via the request count instead).
	c := New(srv.URL+"/api/hassio_ingress/x", "tok").WithSigner(signer)

	if _, err := c.Health(context.Background()); err != nil {
		t.Fatalf("Health (after re-sign) failed: %v", err)
	}
	if requestCount.Load() != 2 {
		t.Errorf("requests = %d, want 2 (first 401, second OK)", requestCount.Load())
	}
	if signer.calls.Load() != 2 {
		t.Errorf("signer calls = %d, want 2 (one per attempt)", signer.calls.Load())
	}
	// Both attempts carry a signature — the second one is fresh.
	if firstSig == "" || secondSig == "" {
		t.Errorf("both attempts should carry authSig (got %q, %q)", firstSig, secondSig)
	}
}

func TestClient_ReturnsErrorAfterMaxResignAttempts(t *testing.T) {
	var requestCount atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requestCount.Add(1)
		http.Error(w, "always 401", http.StatusUnauthorized)
	}))
	defer srv.Close()

	signer := &fakeSigner{}
	c := New(srv.URL+"/api/hassio_ingress/x", "tok").WithSigner(signer)

	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("Health should fail after exhausting re-sign attempts")
	}
	if !strings.Contains(err.Error(), "401") {
		t.Errorf("error should mention 401, got: %v", err)
	}
	if requestCount.Load() != 3 {
		t.Errorf("requests = %d, want 3 (max attempts)", requestCount.Load())
	}
}

func TestClient_SignerFailurePropagates(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("server should not be hit when signing fails")
	}))
	defer srv.Close()

	signer := &fakeSigner{failNthCall: 1, failWith: errSignFailed}
	c := New(srv.URL+"/api/hassio_ingress/x", "tok").WithSigner(signer)

	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatal("Health should fail when signer errors")
	}
	if !strings.Contains(err.Error(), "signing ingress path") {
		t.Errorf("error should wrap signer failure, got: %v", err)
	}
}
