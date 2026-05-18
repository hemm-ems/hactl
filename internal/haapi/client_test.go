package haapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestGetConfig_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"version":"2025.1"}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := string(body); got != `{"version":"2025.1"}` {
		t.Fatalf("body = %q, want %q", got, `{"version":"2025.1"}`)
	}
}

func TestGetConfig_AuthHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := r.Header.Get("Authorization")
		if got != "Bearer test-token" {
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	_, err := c.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetConfig_Unauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	c := New(srv.URL, "bad-token")
	_, err := c.GetConfig(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if want := "401"; !contains(err.Error(), want) {
		t.Fatalf("error = %q, want it to contain %q", err, want)
	}
}

func TestGetErrorLog_Success(t *testing.T) {
	const logText = "2025-01-15 12:00:00 ERROR (MainThread) [homeassistant] something broke"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprint(w, logText)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetErrorLog(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := string(body); got != logText {
		t.Fatalf("body = %q, want %q", got, logText)
	}
}

func TestRenderTemplate_Success(t *testing.T) {
	const tpl = "{{ states('sensor.temperature') }}"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", r.Method)
		}
		if r.URL.Path != "/api/template" {
			t.Errorf("path = %s, want /api/template", r.URL.Path)
		}

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("reading body: %v", err)
		}
		var payload map[string]string
		if err := json.Unmarshal(raw, &payload); err != nil {
			t.Fatalf("unmarshaling body: %v", err)
		}
		if payload["template"] != tpl {
			t.Errorf("template = %q, want %q", payload["template"], tpl)
		}

		_, _ = fmt.Fprint(w, "42.0")
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	result, err := c.RenderTemplate(context.Background(), tpl)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "42.0" {
		t.Fatalf("result = %q, want %q", result, "42.0")
	}
}

func TestRetry_ServerError(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry test in short mode")
	}

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprint(w, `{"ok":true}`)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	body, err := c.GetConfig(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := string(body); got != `{"ok":true}` {
		t.Fatalf("body = %q, want %q", got, `{"ok":true}`)
	}
	if n := calls.Load(); n < 2 {
		t.Fatalf("server called %d times, want >= 2", n)
	}
}

func TestRetry_MaxRetriesExceeded(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping retry test in short mode")
	}

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok")
	_, err := c.GetConfig(context.Background())
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("server called %d times, want 3", n)
	}
}

// contains reports whether s contains substr.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
