package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// companionEnv starts a stub companion server with the given handler plus a
// dummy HA server, and points flagDir at a temp .env that wires COMPANION_URL
// to the stub. connectCompanion tolerates the (failing) HA WS dial and uses
// COMPANION_URL directly, so the stub only needs to answer the tested endpoint.
func companionEnv(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	companionSrv := httptest.NewServer(handler)
	t.Cleanup(companionSrv.Close)
	haSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(haSrv.Close)

	dir := t.TempDir()
	env := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=tok\nCOMPANION_URL=%s\n", haSrv.URL, companionSrv.URL)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	withFlagDir(t, dir)
}

// jsonResp writes v as a JSON response body.
func jsonResp(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	b, _ := json.Marshal(v)
	_, _ = w.Write(b)
}

func TestRunAutoCat(t *testing.T) {
	const wantYAML = "alias: Test\ntrigger: []\n"
	companionEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config/automation" {
			jsonResp(w, map[string]string{"id": r.URL.Query().Get("id"), "content": wantYAML})
		}
	})
	var buf bytes.Buffer
	if err := runAutoCat(context.Background(), &buf, "abc123"); err != nil {
		t.Fatalf("runAutoCat: %v", err)
	}
	if buf.String() != wantYAML {
		t.Errorf("expected verbatim YAML (no header) %q, got %q", wantYAML, buf.String())
	}
}

func TestRunScriptCat(t *testing.T) {
	const wantYAML = "welcome_home:\n  sequence: []\n"
	companionEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config/script" {
			jsonResp(w, map[string]string{"id": r.URL.Query().Get("id"), "content": wantYAML})
		}
	})
	var buf bytes.Buffer
	if err := runScriptCat(context.Background(), &buf, "welcome_home"); err != nil {
		t.Fatalf("runScriptCat: %v", err)
	}
	if buf.String() != wantYAML {
		t.Errorf("expected verbatim YAML %q, got %q", wantYAML, buf.String())
	}
}

func TestRunTplCat(t *testing.T) {
	const wantYAML = "- sensor:\n    - name: Foo\n      state: \"{{ 1 }}\"\n"
	companionEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config/template" {
			jsonResp(w, map[string]string{"unique_id": r.URL.Query().Get("id"), "content": wantYAML})
		}
	})
	var buf bytes.Buffer
	if err := runTplCat(context.Background(), &buf, "foo"); err != nil {
		t.Fatalf("runTplCat: %v", err)
	}
	if buf.String() != wantYAML {
		t.Errorf("expected verbatim YAML %q, got %q", wantYAML, buf.String())
	}
}

func TestRunHelperCat(t *testing.T) {
	const wantYAML = "guest_mode:\n  name: Guest Mode\n"
	companionEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config/helper" {
			jsonResp(w, map[string]string{"id": r.URL.Query().Get("id"), "domain": "input_boolean", "content": wantYAML})
		}
	})
	var buf bytes.Buffer
	if err := runHelperCat(context.Background(), &buf, "guest_mode"); err != nil {
		t.Fatalf("runHelperCat: %v", err)
	}
	// cat prints pure content with no id/domain header (unlike `helper show`).
	if buf.String() != wantYAML {
		t.Errorf("expected verbatim YAML with no header, got %q", buf.String())
	}
	if strings.Contains(buf.String(), "domain:") && !strings.HasPrefix(buf.String(), "guest_mode:") {
		t.Errorf("unexpected header in cat output: %q", buf.String())
	}
}

func TestRunConfigFiles(t *testing.T) {
	companionEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config/files" {
			jsonResp(w, map[string][]string{"files": {"configuration.yaml", "automations.yaml"}})
		}
	})
	var buf bytes.Buffer
	if err := runConfigFiles(context.Background(), &buf); err != nil {
		t.Fatalf("runConfigFiles: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "configuration.yaml") || !strings.Contains(out, "automations.yaml") {
		t.Errorf("missing file paths in table: %q", out)
	}
}

func TestRunConfigFile_ResolveAndRaw(t *testing.T) {
	const content = "input_boolean: !include input_booleans.yaml\n"
	var gotResolve string
	companionEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config/file" {
			gotResolve = r.URL.Query().Get("resolve")
			jsonResp(w, map[string]string{"path": r.URL.Query().Get("path"), "content": content})
		}
	})

	var buf bytes.Buffer
	if err := runConfigFile(context.Background(), &buf, "configuration.yaml"); err != nil {
		t.Fatalf("runConfigFile (resolved): %v", err)
	}
	if gotResolve != "true" {
		t.Errorf("default should request resolve=true, got %q", gotResolve)
	}
	if buf.String() != content {
		t.Errorf("content mismatch: %q", buf.String())
	}

	flagConfigFileRaw = true
	t.Cleanup(func() { flagConfigFileRaw = false })
	buf.Reset()
	if err := runConfigFile(context.Background(), &buf, "configuration.yaml"); err != nil {
		t.Fatalf("runConfigFile (raw): %v", err)
	}
	if gotResolve != "false" {
		t.Errorf("--raw should request resolve=false, got %q", gotResolve)
	}
}

func TestRunConfigBlock(t *testing.T) {
	const content = "alias: Test\ntrigger: []\n"
	var gotPath, gotID string
	companionEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config/block" {
			gotPath = r.URL.Query().Get("path")
			gotID = r.URL.Query().Get("id")
			jsonResp(w, map[string]string{"path": gotPath, "id": gotID, "content": content})
		}
	})
	var buf bytes.Buffer
	if err := runConfigBlock(context.Background(), &buf, "automations.yaml", "auto_1"); err != nil {
		t.Fatalf("runConfigBlock: %v", err)
	}
	if gotPath != "automations.yaml" || gotID != "auto_1" {
		t.Errorf("params not forwarded: path=%q id=%q", gotPath, gotID)
	}
	if buf.String() != content {
		t.Errorf("content mismatch: %q", buf.String())
	}
}
