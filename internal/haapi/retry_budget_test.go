package haapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// slowGateway answers every request with 504, but only after delay — a reverse
// proxy that gave up on Home Assistant. It is the shape #155 was found against:
// openresty answering 504 after about 90s, each attempt inside --timeout 180s,
// three attempts, 272s in total.
func slowGateway(t *testing.T, delay time.Duration) (string, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.WriteHeader(http.StatusGatewayTimeout)
		_, _ = w.Write([]byte("<html>504 Gateway Time-out</html>"))
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &calls
}

// TestRetryStaysInsideTheCallerTimeout — #155, the REST half of D-34.
//
// A 5xx GET is retried, and each attempt used to get the whole --timeout
// again, so the command as a whole was bounded by three times the flag plus
// the backoff. D-34 decided this for the WebSocket connect already: two
// attempts do not entitle a command to twice the time the caller allowed. A
// retry starts only if budget remains, and it gets only what remains.
//
// The two rows are the two ways a retry can meet the bound: the backoff alone
// no longer fits (the retry is not started), and the retry starts but the
// budget ends while it waits for an answer (it is cut). Either way the caller
// learns what the server said — a 504 — not a client-side timeout, because
// that is the fact a retry was trying to improve on.
func TestRetryStaysInsideTheCallerTimeout(t *testing.T) {
	for _, tc := range []struct {
		name    string
		delay   time.Duration
		timeout time.Duration
		calls   int32
	}{
		// 400ms answer, 200ms left, a 500ms backoff does not fit.
		{"backoff does not fit", 400 * time.Millisecond, 600 * time.Millisecond, 1},
		// 300ms answer, 500ms backoff, the retry starts with 200ms left and is cut.
		{"retry is cut at the bound", 300 * time.Millisecond, time.Second, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withTimeout(t, tc.timeout)
			url, calls := slowGateway(t, tc.delay)

			start := time.Now()
			_, err := New(url, "token").GetStates(context.Background())
			elapsed := time.Since(start)

			if err == nil {
				t.Fatal("a 504 must surface as an error")
			}
			// Generous against a loaded machine, and far below the 3·delay plus
			// 1.5s of backoff the defect produced.
			if ceiling := tc.timeout + 400*time.Millisecond; elapsed > ceiling {
				t.Errorf("GetStates took %v with --timeout %v: the retry ran outside the caller's bound", elapsed, tc.timeout)
			}
			if got := calls.Load(); got != tc.calls {
				t.Errorf("%d attempts, want %d", got, tc.calls)
			}
			if status, ok := HTTPStatus(err); !ok || status != http.StatusGatewayTimeout {
				t.Errorf("HTTPStatus(err) = %d, %v; want 504 — the server's answer, not the client's timeout: %v", status, ok, err)
			}
			if !strings.Contains(err.Error(), "--timeout") {
				t.Errorf("the error does not say the retry was stopped by --timeout: %v", err)
			}
		})
	}
}

// TestRetryInsideTheBudgetIsUnchanged is the control: a fast 5xx with plenty
// of budget left is still retried the full three times and says nothing about
// --timeout. Without it, deleting the retry would satisfy the test above.
func TestRetryInsideTheBudgetIsUnchanged(t *testing.T) {
	withTimeout(t, 10*time.Second)
	url, calls := slowGateway(t, 0)

	_, err := New(url, "token").GetStates(context.Background())
	if err == nil {
		t.Fatal("a 504 must surface as an error")
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("%d attempts, want 3", got)
	}
	if strings.Contains(err.Error(), "--timeout") {
		t.Errorf("a retry that fitted is reported as stopped by --timeout: %v", err)
	}
}
