package cmd

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeEnv(t *testing.T, dir, haURL string) {
	t.Helper()
	content := "HA_URL=" + haURL + "\nHA_TOKEN=test\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestConfigDeleteDryRun verifies that without --confirm, no request is made
// and the dry-run notice is printed.
func TestConfigDeleteDryRun(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeEnv(t, dir, srv.URL)

	flagConfigConfirm = false
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"config", "delete", "abc123", "--dir", dir})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("config delete dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
	if !strings.Contains(out, "abc123") {
		t.Errorf("expected entry_id in output, got: %q", out)
	}
	if called {
		t.Error("dry-run made an HTTP request; it must not")
	}
}

// TestConfigDeleteConfirm verifies that --confirm issues a DELETE to the
// correct config-entry endpoint.
func TestConfigDeleteConfirm(t *testing.T) {
	var gotMethod, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"require_restart": false}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	writeEnv(t, dir, srv.URL)

	flagConfigConfirm = true
	defer func() { flagConfigConfirm = false }()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"config", "delete", "abc123", "--confirm", "--dir", dir})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("config delete --confirm failed: %v", err)
	}

	if gotMethod != http.MethodDelete {
		t.Errorf("expected DELETE, got %q", gotMethod)
	}
	if want := "/api/config/config_entries/entry/abc123"; gotPath != want {
		t.Errorf("expected path %q, got %q", want, gotPath)
	}
	if out := buf.String(); !strings.Contains(out, "deleted config entry") {
		t.Errorf("expected success message, got: %q", out)
	}
}
