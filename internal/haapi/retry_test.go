package haapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// countingServer answers every request with status and records how many
// requests of each method it saw.
func countingServer(t *testing.T, status int) (*Client, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(`{"message":"boom"}`))
	}))
	t.Cleanup(srv.Close)
	return New(srv.URL, "token"), &calls
}

// TestPostNotRetriedOn5xx is the defect this file exists for.
//
// doWithRetry retried on any 5xx with no method check, so a service call that
// Home Assistant executed and then failed to report on was sent three times.
// `hactl svc call notify.mobile_app --confirm` against an HA that delivers the
// push and *then* raises produced three notifications; the same shape applies
// to lock.unlock, cover.open_cover and counter.increment.
//
// The rule is INVARIANTS.md H-1, which was enforced only against the companion
// client while this one — the one that actually talks to Home Assistant — was
// never checked.
func TestPostNotRetriedOn5xx(t *testing.T) {
	client, calls := countingServer(t, http.StatusInternalServerError)

	if err := client.CallService(context.Background(), "notify", "mobile_app", map[string]any{"message": "hi"}); err == nil {
		t.Fatal("a 500 from HA must surface as an error")
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("CallService issued %d POSTs against a 5xx; want exactly 1 — HA may already have run the service", got)
	}
}

// TestNonIdempotentWritesAreIssuedOnce covers the rest of the POST surface that
// routed through the same helper.
func TestNonIdempotentWritesAreIssuedOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Client) error
	}{
		{"CallService", func(c *Client) error {
			return c.CallService(context.Background(), "light", "turn_on", nil)
		}},
		{"CallServiceWithResponse", func(c *Client) error {
			_, err := c.CallServiceWithResponse(context.Background(), "calendar", "get_events", nil)
			return err
		}},
		{"UpdateAutomationConfig", func(c *Client) error {
			return c.UpdateAutomationConfig(context.Background(), "auto1", map[string]any{"alias": "x"})
		}},
		{"RenderTemplate", func(c *Client) error {
			_, err := c.RenderTemplate(context.Background(), "{{ 1 }}")
			return err
		}},
		{"StartConfigFlow", func(c *Client) error {
			_, err := c.StartConfigFlow(context.Background(), "mqtt")
			return err
		}},
		{"StartOptionsFlow", func(c *Client) error {
			_, err := c.StartOptionsFlow(context.Background(), "entry1")
			return err
		}},
		{"StepFlow", func(c *Client) error {
			_, err := c.StepFlow(context.Background(), "flow1", false, json.RawMessage(`{"k":"v"}`))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, calls := countingServer(t, http.StatusInternalServerError)
			if err := tc.call(client); err == nil {
				t.Fatal("a 500 must surface as an error")
			}
			if got := calls.Load(); got != 1 {
				t.Errorf("%s issued %d requests against a 5xx; want exactly 1", tc.name, got)
			}
		})
	}
}

// TestGetStillRetriedOn5xx is the negative control. Without it, deleting the
// retry entirely would satisfy the test above.
func TestGetStillRetriedOn5xx(t *testing.T) {
	client, calls := countingServer(t, http.StatusInternalServerError)

	if _, err := client.GetStates(context.Background()); err == nil {
		t.Fatal("a 500 from HA must surface as an error")
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("GetStates issued %d requests against a 5xx; want 3 — a read is safe to repeat and the retry is worth having", got)
	}
}
