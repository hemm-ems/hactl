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

// configEntriesServer serves HA's config-entry list plus whatever the caller
// adds, and records every non-list request — so a test can prove a dry run
// resolved its target (a GET) without performing the write (a DELETE).
func configEntriesServer(t *testing.T, entries string, extra http.HandlerFunc) (*httptest.Server, *[]string) {
	t.Helper()
	var writes []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/config/config_entries/entry" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(entries))
			return
		}
		writes = append(writes, r.Method+" "+r.URL.Path)
		if extra != nil {
			extra(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, &writes
}

const oneConfigEntry = `[{"entry_id":"abc123","domain":"mqtt","title":"MQTT","state":"loaded","supports_options":true}]`

// TestConfigDeleteDryRun verifies that without --confirm the entry is resolved
// but nothing is written.
//
// This test used to assert that a dry run makes NO request at all, which is
// what let it plan the removal of an entry_id HA has never heard of. Deleting
// a config entry takes every entity it owns with it; the preview has to name
// something real.
func TestConfigDeleteDryRun(t *testing.T) {
	srv, writes := configEntriesServer(t, oneConfigEntry, nil)

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
	// The domain is the witness that the preview read HA's answer rather than
	// echoing the argument.
	if !strings.Contains(out, "mqtt") {
		t.Errorf("expected the resolved entry's domain in output, got: %q", out)
	}
	if len(*writes) != 0 {
		t.Errorf("dry-run performed a write: %v", *writes)
	}
}

// TestConfigDeleteDryRunRefusesUnknownEntry is the inverted assertion: an
// entry_id HA does not have must fail the preview, not plan a removal.
func TestConfigDeleteDryRunRefusesUnknownEntry(t *testing.T) {
	srv, writes := configEntriesServer(t, oneConfigEntry, nil)

	dir := t.TempDir()
	writeEnv(t, dir, srv.URL)

	flagConfigConfirm = false
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"config", "delete", "no_such_entry", "--dir", dir})

	if err := rootCmd.Execute(); err == nil {
		t.Fatalf("dry-run planned a delete for an entry HA does not have:\n%s", buf.String())
	}
	if len(*writes) != 0 {
		t.Errorf("refused dry-run still performed a write: %v", *writes)
	}
}

// TestConfigDeleteConfirm verifies that --confirm issues a DELETE to the
// correct config-entry endpoint.
func TestConfigDeleteConfirm(t *testing.T) {
	var gotMethod, gotPath string
	srv, _ := configEntriesServer(t, oneConfigEntry, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"require_restart": false}`))
	})

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
