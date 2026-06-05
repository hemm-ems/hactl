package companion

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestLogs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/logs" {
			t.Errorf("path = %q, want /v1/logs", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("component") != "wireguard" {
			t.Errorf("component = %q, want wireguard", q.Get("component"))
		}
		if q.Get("level") != "warning" {
			t.Errorf("level = %q, want warning", q.Get("level"))
		}
		if q.Get("since") != "1h" {
			t.Errorf("since = %q, want 1h", q.Get("since"))
		}
		if q.Get("limit") != "5" {
			t.Errorf("limit = %q, want 5", q.Get("limit"))
		}
		_ = json.NewEncoder(w).Encode(LogsResponse{Entries: []LogEntry{
			{Ts: 1780696789, Level: "WARNING", Name: "companion.wg_monitor", Message: "stale handshake on wg0"},
		}})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	res, err := c.Logs(context.Background(), LogsParams{
		Component: "wireguard",
		Level:     "warning",
		Since:     "1h",
		Limit:     5,
	})
	if err != nil {
		t.Fatalf("Logs: %v", err)
	}
	if len(res.Entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(res.Entries))
	}
	if res.Entries[0].Message != "stale handshake on wg0" {
		t.Errorf("message = %q", res.Entries[0].Message)
	}
}

func TestLogsOmitsEmptyParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want empty when no params set", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(LogsResponse{})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	if _, err := c.Logs(context.Background(), LogsParams{}); err != nil {
		t.Fatalf("Logs: %v", err)
	}
}
