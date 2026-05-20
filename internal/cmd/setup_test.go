package cmd

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRootCmd_HasSetup verifies that "setup" is a registered subcommand.
func TestRootCmd_HasSetup(t *testing.T) {
	cmd, _, err := rootCmd.Find([]string{"setup"})
	if err != nil || cmd == nil || cmd.Name() != "setup" {
		t.Fatalf("rootCmd missing 'setup' subcommand: cmd=%v err=%v", cmd, err)
	}
}

// TestSetup_WritesDotEnv verifies that runSetup writes .env when HA is reachable.
func TestSetup_WritesDotEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintln(w, `{"message":"API running."}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dir := t.TempDir()
	old := flagDir
	flagDir = dir
	defer func() { flagDir = old }()

	input := strings.NewReader(srv.URL + "\n" + "test-token-abc\n")
	out := &bytes.Buffer{}

	if err := runSetup(context.Background(), out, input); err != nil {
		t.Fatalf("runSetup failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".env"))
	if err != nil {
		t.Fatalf(".env not written: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, srv.URL) {
		t.Errorf(".env missing HA_URL %q: %s", srv.URL, content)
	}
	if !strings.Contains(content, "test-token-abc") {
		t.Errorf(".env missing HA_TOKEN: %s", content)
	}
}

// TestSetup_RejectsBadURL verifies that runSetup returns an error and writes no
// .env when the given HA URL is not reachable.
func TestSetup_RejectsBadURL(t *testing.T) {
	dir := t.TempDir()
	old := flagDir
	flagDir = dir
	defer func() { flagDir = old }()

	// Use a port that is never listening.
	input := strings.NewReader("http://127.0.0.1:19999\n" + "sometoken\n")
	out := &bytes.Buffer{}

	if err := runSetup(context.Background(), out, input); err == nil {
		t.Fatal("expected error on unreachable URL, got nil")
	}

	if _, statErr := os.Stat(filepath.Join(dir, ".env")); statErr == nil {
		t.Error(".env must not be written on connection failure")
	}
}

// TestSetup_RejectsBadToken verifies that runSetup returns an error and writes no
// .env when the HA server responds with 401.
func TestSetup_RejectsBadToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	dir := t.TempDir()
	old := flagDir
	flagDir = dir
	defer func() { flagDir = old }()

	input := strings.NewReader(srv.URL + "\n" + "bad-token\n")
	out := &bytes.Buffer{}

	if err := runSetup(context.Background(), out, input); err == nil {
		t.Fatal("expected error on 401, got nil")
	}

	if _, statErr := os.Stat(filepath.Join(dir, ".env")); statErr == nil {
		t.Error(".env must not be written on auth failure")
	}
}
