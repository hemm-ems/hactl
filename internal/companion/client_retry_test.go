package companion

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"syscall"
	"testing"
)

func TestShouldRetry_Idempotency(t *testing.T) {
	transportErr := errors.New("boom")
	dialErr := &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}

	cases := []struct {
		name   string
		err    error
		status int
		signed bool
		method string
		want   bool
	}{
		{"GET transport error retries", transportErr, 0, false, http.MethodGet, true},
		{"PUT transport error retries", transportErr, 0, false, http.MethodPut, true},
		{"DELETE transport error retries", transportErr, 0, false, http.MethodDelete, true},
		{"POST generic transport error does NOT retry", transportErr, 0, false, http.MethodPost, false},
		{"POST connection-refused retries (never sent)", dialErr, 0, false, http.MethodPost, true},
		{"GET 5xx retries", nil, 503, false, http.MethodGet, true},
		{"POST 5xx does NOT retry", nil, 503, false, http.MethodPost, false},
		{"POST signed 401 retries", nil, http.StatusUnauthorized, true, http.MethodPost, true},
		{"GET 200 does not retry", nil, 200, false, http.MethodGet, false},
		{"unsigned 401 does not retry", nil, http.StatusUnauthorized, false, http.MethodGet, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldRetry(tc.err, tc.status, tc.signed, tc.method); got != tc.want {
				t.Errorf("shouldRetry(%v, %d, %v, %s) = %v, want %v", tc.err, tc.status, tc.signed, tc.method, got, tc.want)
			}
		})
	}
}

func TestNeverSent(t *testing.T) {
	if !neverSent(&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}) {
		t.Error("dial ECONNREFUSED should count as never-sent")
	}
	if !neverSent(syscall.ECONNREFUSED) {
		t.Error("bare ECONNREFUSED should count as never-sent")
	}
	if neverSent(errors.New("read: connection reset by peer")) {
		t.Error("a generic transport error must not count as never-sent")
	}
	if neverSent(nil) {
		t.Error("nil is not a transport error")
	}
}

// TestPostNotRetriedOn5xx proves a non-idempotent create is issued exactly once
// even when the server 5xxs — retrying could duplicate the create.
func TestPostNotRetriedOn5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "t")
	if _, err := c.CreateScriptDef(context.Background(), "probe:\n  alias: x\n"); err == nil {
		t.Fatal("expected error from 500")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("POST create issued %d times, want exactly 1 (no retry on 5xx)", got)
	}
}

// TestGetRetriedOn5xx confirms idempotent reads still retry (recovering after a
// transient 5xx).
func TestGetRetriedOn5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if atomic.AddInt32(&calls, 1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"files":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "t")
	if _, err := c.ListConfigFiles(context.Background()); err != nil {
		t.Fatalf("GET should recover after one 5xx: %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("GET issued %d times, want 2 (one retry)", got)
	}
}

// TestContentTypePerEndpoint pins the Content-Type each write endpoint sends so
// it matches the OpenAPI spec (json for ref-replace + helpers, text/plain else).
func TestContentTypePerEndpoint(t *testing.T) {
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen[r.Method+" "+r.URL.Path] = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","changes":[]}`))
	}))
	defer srv.Close()

	c := New(srv.URL, "t")
	ctx := context.Background()
	_, _ = c.RefReplace(ctx, "a", "b", true)
	_, _ = c.CreateHelper(ctx, "x:\n  name: y\n", "input_boolean")
	_, _ = c.UpdateHelper(ctx, "x", "name: y\n")
	_, _ = c.CreateTemplate(ctx, "x", "sensor")
	_, _ = c.WriteConfigFile(ctx, "a.yaml", "x: 1\n", true)

	wantJSON := []string{"POST /v1/ref/replace", "POST /v1/config/helper", "PUT /v1/config/helper"}
	for _, key := range wantJSON {
		if seen[key] != mimeJSON {
			t.Errorf("%s Content-Type = %q, want %q", key, seen[key], mimeJSON)
		}
	}
	wantText := []string{"POST /v1/config/template", "PUT /v1/config/file"}
	for _, key := range wantText {
		if seen[key] != mimeText {
			t.Errorf("%s Content-Type = %q, want %q", key, seen[key], mimeText)
		}
	}
}
