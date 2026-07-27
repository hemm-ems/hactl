package httpretry_test

import (
	"errors"
	"net"
	"net/http"
	"syscall"
	"testing"

	"github.com/hemm-ems/hactl/internal/httpretry"
)

func TestIsIdempotent(t *testing.T) {
	for _, tc := range []struct {
		method string
		want   bool
	}{
		{http.MethodGet, true},
		{http.MethodHead, true},
		{http.MethodPut, true},
		{http.MethodDelete, true},
		{http.MethodOptions, true},
		{http.MethodPost, false},
		{http.MethodPatch, false},
	} {
		if got := httpretry.IsIdempotent(tc.method); got != tc.want {
			t.Errorf("IsIdempotent(%s) = %v, want %v", tc.method, got, tc.want)
		}
	}
}

// TestNeverSent moved here with the function it tests. A dial failure is the
// only transport error we can prove the server never saw.
func TestNeverSent(t *testing.T) {
	if !httpretry.NeverSent(&net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}) {
		t.Error("dial ECONNREFUSED should count as never-sent")
	}
	if !httpretry.NeverSent(syscall.ECONNREFUSED) {
		t.Error("bare ECONNREFUSED should count as never-sent")
	}
	if httpretry.NeverSent(errors.New("read: connection reset by peer")) {
		t.Error("a generic transport error must not count as never-sent — the request may have arrived and the response been lost")
	}
	if httpretry.NeverSent(nil) {
		t.Error("nil is not a transport error")
	}
}

// TestShouldRetry is the policy's truth table.
//
// The row that matters is {POST, 500}: it is the one the Home Assistant client
// got wrong, and it is why `svc call --confirm` could deliver a notification
// three times.
func TestShouldRetry(t *testing.T) {
	dial := &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	reset := errors.New("read: connection reset by peer")

	for _, tc := range []struct {
		name   string
		err    error
		status int
		method string
		want   bool
	}{
		{"GET 500 retries", nil, 500, http.MethodGet, true},
		{"DELETE 503 retries", nil, 503, http.MethodDelete, true},
		{"POST 500 does NOT retry — the server may already have acted", nil, 500, http.MethodPost, false},
		{"PATCH 500 does NOT retry", nil, 500, http.MethodPatch, false},
		{"POST dial failure retries — provably never sent", dial, 0, http.MethodPost, true},
		{"POST mid-flight reset does NOT retry", reset, 0, http.MethodPost, false},
		{"GET mid-flight reset retries", reset, 0, http.MethodGet, true},
		{"POST 404 does not retry", nil, 404, http.MethodPost, false},
		{"GET 404 does not retry", nil, 404, http.MethodGet, false},
		{"GET 200 does not retry", nil, 200, http.MethodGet, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := httpretry.ShouldRetry(tc.err, tc.status, tc.method); got != tc.want {
				t.Errorf("ShouldRetry(%v, %d, %s) = %v, want %v", tc.err, tc.status, tc.method, got, tc.want)
			}
		})
	}
}
