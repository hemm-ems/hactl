package haapi

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGetHistory_Success(t *testing.T) {
	const histJSON = `[[{"entity_id":"sensor.temp","state":"21.5","last_changed":"2026-01-01T10:00:00+00:00"}]]`

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		// Check path starts with /api/history/period/
		if got := r.URL.Path; got == "" {
			t.Error("empty path")
		}
		// Check filter_entity_id param
		if got := r.URL.Query().Get("filter_entity_id"); got != "sensor.temp" {
			t.Errorf("filter_entity_id = %q, want 'sensor.temp'", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, histJSON)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetHistory(context.Background(), "sensor.temp", "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(body) != histJSON {
		t.Fatalf("body = %q, want %q", string(body), histJSON)
	}
}

func TestGetHistory_EndTimeParam(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("end_time"); got != "2026-01-02T00:00:00Z" {
			t.Errorf("end_time = %q, want '2026-01-02T00:00:00Z'", got)
		}
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	_, err := c.GetHistory(context.Background(), "sensor.x", "2026-01-01T00:00:00Z", "2026-01-02T00:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetHistory_NoEndTime(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("end_time"); got != "" {
			t.Errorf("end_time should be absent, got %q", got)
		}
		_, _ = fmt.Fprint(w, `[]`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	_, err := c.GetHistory(context.Background(), "sensor.x", "2026-01-01T00:00:00Z", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
