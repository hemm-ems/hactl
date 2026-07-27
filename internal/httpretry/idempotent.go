// Package httpretry holds the one definition of "is this request safe to send
// twice", shared by every HTTP client in this repository.
//
// It exists because the answer was previously written down twice. The companion
// client gated its retries on method idempotency and carried a comment stating
// the rule; the Home Assistant client retried any method on 5xx with no method
// check at all. INVARIANTS.md H-1 states the rule as a universal — "a create is
// never silently duplicated" — and cited only the companion's test, so nothing
// ever compared the two implementations. `hactl svc call notify.x --confirm`
// against a Home Assistant that delivered the notification and then raised
// would send it three times.
//
// One predicate, imported by both, is the structural fix. A second client
// cannot now disagree with the first without deleting an import.
package httpretry

import (
	"errors"
	"net"
	"net/http"
	"syscall"
)

// IsIdempotent reports whether re-sending a request of this method is safe when
// the server may already have acted on it.
//
// POST and PATCH are the exclusions: both can create or accumulate. Everything
// else in HTTP's safe/idempotent set may be repeated, which is what makes a 5xx
// retry legitimate for a read.
func IsIdempotent(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodPut, http.MethodDelete, http.MethodOptions, http.MethodTrace:
		return true
	default:
		return false
	}
}

// NeverSent reports whether a transport error occurred before the request was
// put on the wire (a dial / connection-refused failure), which makes a retry
// safe even for a non-idempotent method: the server cannot have acted on it.
//
// The distinction matters in both directions. A dial failure is the only
// transport error we can prove the server never saw; a timeout or a reset
// mid-flight may mean the request arrived and the response was lost, and
// retrying that is exactly the duplicate this package exists to prevent.
func NeverSent(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return true
	}
	return false
}

// ShouldRetry is the whole policy, in one place.
//
//   - A transport error retries for an idempotent method, and for any method
//     when the request provably never left the client.
//   - A 5xx means the server received the request and may have acted on it, so
//     only an idempotent method retries.
//   - Anything else does not retry. (A caller with its own extra case — the
//     companion's signed-401 re-sign — layers it on top rather than editing
//     this.)
func ShouldRetry(err error, status int, method string) bool {
	idempotent := IsIdempotent(method)
	if err != nil {
		return idempotent || NeverSent(err)
	}
	return status >= 500 && idempotent
}
