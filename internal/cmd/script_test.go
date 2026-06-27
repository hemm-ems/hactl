package cmd

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizeScriptYAML_UIStyle(t *testing.T) {
	c, err := normalizeScriptYAML([]byte("alias: Test\nsequence: []\nmode: single\n"), "kino_start")
	if err != nil {
		t.Fatalf("normalizeScriptYAML: %v", err)
	}
	if c.ID != "kino_start" {
		t.Errorf("ID = %q, want kino_start", c.ID)
	}
	if _, ok := c.Config["sequence"]; !ok {
		t.Error("normalized config missing sequence")
	}
	if c.Content == "" || c.Content[0] == 'k' {
		t.Errorf("content looks wrapped, got %q", c.Content)
	}
}

func TestNormalizeScriptYAML_WrapperStyle(t *testing.T) {
	c, err := normalizeScriptYAML([]byte("kino_start:\n  alias: Test\n  sequence: []\n"), "script.kino_start")
	if err != nil {
		t.Fatalf("normalizeScriptYAML: %v", err)
	}
	if c.ID != "kino_start" {
		t.Errorf("ID = %q, want kino_start", c.ID)
	}
	if _, wrapped := c.Config["kino_start"]; wrapped {
		t.Error("wrapper key leaked into normalized config")
	}
}

func TestNormalizeScriptYAML_WrapperIDMismatch(t *testing.T) {
	_, err := normalizeScriptYAML([]byte("other_script:\n  alias: Test\n  sequence: []\n"), "kino_start")
	if err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestNormalizeScriptYAML_MissingSequence(t *testing.T) {
	_, err := normalizeScriptYAML([]byte("alias: Test\nmode: single\n"), "kino_start")
	if err == nil {
		t.Fatal("expected missing sequence error")
	}
}

func TestNormalizeScriptYAML_SingleKnownUIKey(t *testing.T) {
	c, err := normalizeScriptYAML([]byte("sequence: []\n"), "kino_start")
	if err != nil {
		t.Fatalf("normalizeScriptYAML: %v", err)
	}
	if _, ok := c.Config["sequence"]; !ok {
		t.Error("sequence key missing")
	}
}

func TestScriptApplyDryRunNormalizesWrapperAndDoesNotBackup(t *testing.T) {
	var putBody string
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/config/script" {
			t.Fatalf("unexpected companion path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("id"); got != "kino_start" {
			t.Errorf("id = %q, want kino_start", got)
		}
		switch r.Method {
		case http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]string{
				"id":      "kino_start",
				"content": "kino_start:\n  alias: Old\n  sequence: []\n",
			})
		case http.MethodPut:
			if got := r.URL.Query().Get("dry_run"); got != "true" {
				t.Errorf("dry_run = %q, want true", got)
			}
			buf := new(bytes.Buffer)
			if _, err := buf.ReadFrom(r.Body); err != nil {
				t.Fatalf("reading PUT body: %v", err)
			}
			putBody = buf.String()
			_ = json.NewEncoder(w).Encode(map[string]string{"status": "dry_run", "diff": "diff"})
		default:
			t.Fatalf("unexpected method: %s", r.Method)
		}
	}))
	defer companionSrv.Close()

	haSrv := httptest.NewServer(http.NotFoundHandler())
	defer haSrv.Close()

	dir := t.TempDir()
	env := "HA_URL=" + haSrv.URL + "\nHA_TOKEN=test\nCOMPANION_URL=" + companionSrv.URL + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	yamlPath := filepath.Join(dir, "script.yaml")
	if err := os.WriteFile(yamlPath, []byte("kino_start:\n  alias: New\n  sequence: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "--dir", dir, "script", "apply", "script.kino_start", "-f", yamlPath}, &out); err != nil {
		t.Fatalf("script apply dry-run: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "dry-run") {
		t.Errorf("output missing dry-run: %s", out.String())
	}
	if strings.Contains(putBody, "kino_start:") {
		t.Errorf("PUT body still wrapped: %q", putBody)
	}
	if !strings.Contains(putBody, "alias: New") {
		t.Errorf("PUT body missing normalized content: %q", putBody)
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "backups")); err == nil && len(entries) > 0 {
		t.Fatalf("dry-run created backups: %v", entries)
	}
}
