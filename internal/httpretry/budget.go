package httpretry

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// Budget is the time one logical request may take: the first attempt and every
// retry ShouldRetry grants (INVARIANTS.md H-23, docs/decisions.md D-34).
//
// It exists because the retry loops bounded each attempt and nothing bounded
// the request. An attempt carries the caller's --timeout through the shared
// http.Client, so an answer that arrives inside it — a reverse proxy's 504
// after 90 seconds — is a 5xx, and a 5xx GET retries with the full --timeout
// again. Against openresty in front of Home Assistant that made `ent hist
// --since 60d --timeout 180s` take 272s, three attempts each inside the bound,
// while the same command at the default 30s stopped at 30.02s because a
// client-side timeout is not retried (#155). WSClient.Connect already ran its
// retry inside the budget; the REST transports did not.
//
// The rule is D-34's for the WebSocket, applied to REST: a retry starts only if
// the backoff leaves budget, and it is given only what remains.
type Budget struct {
	total    time.Duration
	deadline time.Time // zero: unbounded
}

// NewBudget starts the clock on a request bounded by total. Zero or less is no
// bound — an http.Client constructed without a timeout; the flag itself refuses
// 0s (H-25).
func NewBudget(total time.Duration) Budget {
	if total <= 0 {
		return Budget{}
	}
	return Budget{total: total, deadline: time.Now().Add(total)}
}

// Fits reports whether a retry that first waits backoff would still start
// inside the budget.
func (b Budget) Fits(backoff time.Duration) bool {
	return b.deadline.IsZero() || time.Until(b.deadline) > backoff
}

// Bind returns req bound to what is left of the budget. The first attempt does
// not need it — the http.Client's own Timeout is the whole budget then, and
// keeps its own wording when it fires — but every retry does, because the
// client's Timeout restarts with each Do.
func (b Budget) Bind(req *http.Request) (*http.Request, context.CancelFunc) {
	if b.deadline.IsZero() {
		return req, func() {}
	}
	ctx, cancel := context.WithDeadline(req.Context(), b.deadline)
	return req.WithContext(ctx), cancel
}

// Cut reports whether err is the budget ending a retry that Bind bound — and not
// the caller's own context, which is a cancellation and says so itself.
func (b Budget) Cut(caller context.Context, err error) bool {
	return !b.deadline.IsZero() && err != nil && caller.Err() == nil &&
		errors.Is(err, context.DeadlineExceeded)
}

// Stopped reports the last answer the server gave, marked as final because the
// budget ended the retries. The answer is what the caller needs — it is what a
// retry was trying to improve on — and it stays reachable through errors.As, so
// a 504 is still a 504 to haapi.HTTPStatus.
func (b Budget) Stopped(last error) error {
	return &StoppedError{Last: last, Total: b.total}
}

// StoppedError is a request whose retries the caller's --timeout ended.
type StoppedError struct {
	Last  error
	Total time.Duration
}

func (e *StoppedError) Error() string {
	return fmt.Sprintf("%v (no further retry within --timeout %s)", e.Last, e.Total)
}

func (e *StoppedError) Unwrap() error { return e.Last }
