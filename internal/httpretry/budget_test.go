package httpretry_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/httpretry"
)

func TestBudgetFits(t *testing.T) {
	b := httpretry.NewBudget(200 * time.Millisecond)
	if !b.Fits(50 * time.Millisecond) {
		t.Error("a 50ms backoff should fit in a fresh 200ms budget")
	}
	if b.Fits(time.Second) {
		t.Error("a 1s backoff must not fit in a 200ms budget — the retry would start outside it")
	}
	if !httpretry.NewBudget(0).Fits(time.Hour) {
		t.Error("a client without a timeout has no budget to exceed")
	}
}

// TestBudgetBindEndsTheRetryAtTheDeadline — the retry is given what remains,
// not a fresh --timeout.
func TestBudgetBindEndsTheRetryAtTheDeadline(t *testing.T) {
	b := httpretry.NewBudget(50 * time.Millisecond)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://example.invalid/", nil)
	bound, cancel := b.Bind(req)
	defer cancel()

	select {
	case <-bound.Context().Done():
	case <-time.After(2 * time.Second):
		t.Fatal("the bound request outlived its budget")
	}
	if !b.Cut(req.Context(), bound.Context().Err()) {
		t.Error("the budget's own deadline should count as a cut")
	}
}

// TestBudgetCutIsNotTheCallersCancellation — a caller that cancels is told it
// cancelled, not that its --timeout ran out.
func TestBudgetCutIsNotTheCallersCancellation(t *testing.T) {
	b := httpretry.NewBudget(time.Hour)
	caller, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	<-caller.Done()
	if b.Cut(caller, caller.Err()) {
		t.Error("the caller's own deadline was reported as the budget ending the retry")
	}
	if httpretry.NewBudget(0).Cut(context.Background(), context.DeadlineExceeded) {
		t.Error("an unbounded budget cannot cut anything")
	}
}

type gatewayError struct{ code int }

func (e *gatewayError) Error() string { return "504 Gateway Timeout" }

func TestBudgetStoppedKeepsTheServersAnswer(t *testing.T) {
	err := httpretry.NewBudget(3 * time.Minute).Stopped(&gatewayError{504})
	var se *gatewayError
	if !errors.As(err, &se) || se.code != 504 {
		t.Errorf("the server's answer is no longer reachable through errors.As: %v", err)
	}
	if !strings.Contains(err.Error(), "504 Gateway Timeout") || !strings.Contains(err.Error(), "--timeout 3m0s") {
		t.Errorf("Stopped should name both the answer and the bound: %q", err)
	}
}
