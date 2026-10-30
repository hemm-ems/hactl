package companion

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// TestRetryStaysInsideTheCallerTimeout — #155 for the companion transport,
// which retries through the same httpretry predicate and had the same gap: a
// GET the server 5xxs after a delay was retried with a fresh full --timeout per
// attempt. The haapi test of the same name carries the full rationale.
func TestRetryStaysInsideTheCallerTimeout(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-time.After(400 * time.Millisecond):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusGatewayTimeout)
	}))
	defer srv.Close()

	const timeout = 600 * time.Millisecond
	c := New(srv.URL, "t").WithTimeout(timeout)

	start := time.Now()
	_, err := c.ListConfigFiles(context.Background())
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("a 504 must surface as an error")
	}
	if ceiling := timeout + 400*time.Millisecond; elapsed > ceiling {
		t.Errorf("ListConfigFiles took %v with --timeout %v: the retry ran outside the caller's bound", elapsed, timeout)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("%d attempts, want 1 — 200ms left cannot hold a 500ms backoff", got)
	}
	if status, ok := haapi.HTTPStatus(err); !ok || status != http.StatusGatewayTimeout {
		t.Errorf("HTTPStatus(err) = %d, %v; want 504: %v", status, ok, err)
	}
	if !strings.Contains(err.Error(), "--timeout") {
		t.Errorf("the error does not say the retry was stopped by --timeout: %v", err)
	}
}
