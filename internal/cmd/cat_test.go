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
//
// The HA half answers /api/states with an empty list rather than 404: the
// instance these tests describe is READABLE and simply holds no automation, so
// the reference falls through to the companion unchanged. A 404 there means
// "hactl could not read the instance", which every automation-reference command
// now reports instead of guessing (H-7, SPEC §2a) — and asserting on the
// companion's answer while HA is unreachable would be asserting on a path no
// real caller reaches.
func companionEnv(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	companionSrv := httptest.NewServer(handler)
	t.Cleanup(companionSrv.Close)
	haSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/states" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, "[]")
			return
		}
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

// autoCatStatesEnv wires flagDir at a temp .env pointing at both a stub
// companion server and an HA states server carrying a single automation
// entity, mirroring the issue #70 repro: config id "aufstehzeit_wochentag",
// alias "Aufstehzeit - Mo bis Fr", entity_id
// "automation.aufstehzeit_mo_bis_fr" (HA derives entity_id from the alias,
// not the config id).
func autoCatStatesEnv(t *testing.T, companionHandler http.HandlerFunc) {
	t.Helper()
	const entityID = "automation.aufstehzeit_mo_bis_fr"
	const configID = "aufstehzeit_wochentag"
	const alias = "Aufstehzeit - Mo bis Fr"

	statesJSON, _ := json.Marshal([]map[string]any{
		{
			"entity_id":  entityID,
			"state":      "on",
			"attributes": map[string]any{"id": configID, "friendly_name": alias},
		},
	})

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
	})

	companionSrv := httptest.NewServer(companionHandler)
	t.Cleanup(companionSrv.Close)

	envContent, err := os.ReadFile(filepath.Join(ts.dir, ".env"))
	if err != nil {
		t.Fatal(err)
	}
	envContent = fmt.Appendf(envContent, "COMPANION_URL=%s\n", companionSrv.URL)
	if err := os.WriteFile(filepath.Join(ts.dir, ".env"), envContent, 0o600); err != nil { //nolint:gosec // test fixture dir from t.TempDir(), not user input
		t.Fatal(err)
	}
	withFlagDir(t, ts.dir)
}

// autoCatConfigIDHandler answers /v1/config/automation only when id matches
// the fixture's config id "aufstehzeit_wochentag" — any other id (e.g. the
// entity object id or alias reaching the companion unresolved) 404s, which is
// exactly the bug #70 reports.
func autoCatConfigIDHandler(w http.ResponseWriter, r *http.Request) {
	const wantYAML = "alias: Aufstehzeit - Mo bis Fr\ntrigger: []\n"
	if r.URL.Path != "/v1/config/automation" || r.URL.Query().Get("id") != "aufstehzeit_wochentag" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	jsonResp(w, map[string]string{"id": r.URL.Query().Get("id"), "content": wantYAML})
}

// TestRunAutoCat_ByEntityObjectID covers the issue #70 repro: `auto ls`
// prints the entity object id, but the companion's config route keys on the
// config id — cat must resolve one to the other via /api/states.
func TestRunAutoCat_ByEntityObjectID(t *testing.T) {
	autoCatStatesEnv(t, autoCatConfigIDHandler)

	var buf bytes.Buffer
	if err := runAutoCat(context.Background(), &buf, "aufstehzeit_mo_bis_fr"); err != nil {
		t.Fatalf("runAutoCat by entity object id failed: %v", err)
	}
	if !strings.Contains(buf.String(), "alias:") {
		t.Errorf("expected automation YAML, got %q", buf.String())
	}
}

// TestRunAutoCat_ByFullEntityID covers the full "automation.<x>" form.
func TestRunAutoCat_ByFullEntityID(t *testing.T) {
	autoCatStatesEnv(t, autoCatConfigIDHandler)

	var buf bytes.Buffer
	if err := runAutoCat(context.Background(), &buf, "automation.aufstehzeit_mo_bis_fr"); err != nil {
		t.Fatalf("runAutoCat by full entity_id failed: %v", err)
	}
	if !strings.Contains(buf.String(), "alias:") {
		t.Errorf("expected automation YAML, got %q", buf.String())
	}
}

// TestRunAutoCat_ByAlias covers resolving by the human-readable alias
// (attributes.friendly_name).
func TestRunAutoCat_ByAlias(t *testing.T) {
	autoCatStatesEnv(t, autoCatConfigIDHandler)

	var buf bytes.Buffer
	if err := runAutoCat(context.Background(), &buf, "Aufstehzeit - Mo bis Fr"); err != nil {
		t.Fatalf("runAutoCat by alias failed: %v", err)
	}
	if !strings.Contains(buf.String(), "alias:") {
		t.Errorf("expected automation YAML, got %q", buf.String())
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

// TestConfigBlockRedirectsToTplCat is finding #24: `config block template.yaml
// posclock_jan` — a real unique_id in a real file — answered `Block not found:
// posclock_jan`, byte for byte what a typo gets, while the command's own
// --help promised "template.yaml blocks carry neither [id: nor alias:] — read
// those with 'tpl cat <unique_id>'".
//
// The referral is earned by asking the companion whether the id resolves as a
// template, not by matching the filename: a template split into its own file
// still gets it, and a typo inside template.yaml still does not.
func TestConfigBlockRedirectsToTplCat(t *testing.T) {
	const knownTemplate = "posclock_jan"

	companionEnv(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/config/block":
			// The id is deliberately NOT echoed back: the companion's real
			// message carries it, but reflecting a request parameter into a
			// response body is what gosec's taint analysis exists to stop, and
			// nothing here asserts on it.
			w.WriteHeader(http.StatusNotFound)
			_, _ = fmt.Fprint(w, `{"error": {"code": 404, "message": "Block not found"}}`)
		case "/v1/config/template":
			if r.URL.Query().Get("id") != knownTemplate {
				w.WriteHeader(http.StatusNotFound)
				_, _ = fmt.Fprint(w, `{"error": {"code": 404, "message": "Template not found"}}`)
				return
			}
			jsonResp(w, map[string]string{"unique_id": knownTemplate, "content": "name: Clock\n"})
		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	})

	t.Run("an addressable unique_id is named with the command that reads it", func(t *testing.T) {
		var buf bytes.Buffer
		err := runConfigBlock(context.Background(), &buf, "template.yaml", knownTemplate)
		if err == nil {
			t.Fatal("expected an error: template.yaml blocks carry no id or alias")
		}
		if !strings.Contains(err.Error(), "tpl cat "+knownTemplate) {
			t.Errorf("error does not steer to the command that can answer:\n%s", err.Error())
		}
		if !strings.Contains(err.Error(), "template.yaml") {
			t.Errorf("error does not name the file that was searched:\n%s", err.Error())
		}
		if buf.Len() != 0 {
			t.Errorf("a failed block read wrote to stdout: %q", buf.String())
		}
	})

	t.Run("a genuine typo keeps the original error", func(t *testing.T) {
		var buf bytes.Buffer
		err := runConfigBlock(context.Background(), &buf, "template.yaml", "posclock_jam")
		if err == nil {
			t.Fatal("expected an error for an id that exists nowhere")
		}
		if strings.Contains(err.Error(), "tpl cat") {
			t.Errorf("an id that resolves nowhere was sent to tpl cat anyway:\n%s", err.Error())
		}
		if !strings.Contains(err.Error(), "reading config block") {
			t.Errorf("the original failure was replaced rather than kept:\n%s", err.Error())
		}
	})
}

// TestConfigBlockDoesNotRedirectOnANonNotFound keeps the probe off every other
// failure: a 500 from the block route is not evidence about an id, and a
// referral there would be an invented explanation for an unrelated outage.
func TestConfigBlockDoesNotRedirectOnANonNotFound(t *testing.T) {
	var templateProbes int
	companionEnv(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/config/block":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/v1/config/template":
			templateProbes++
			jsonResp(w, map[string]string{"unique_id": "anything", "content": "name: X\n"})
		}
	})
	var buf bytes.Buffer
	err := runConfigBlock(context.Background(), &buf, "template.yaml", "anything")
	if err == nil {
		t.Fatal("expected the 500 to fail the command")
	}
	if strings.Contains(err.Error(), "tpl cat") {
		t.Errorf("a 500 was reported as an addressing problem:\n%s", err.Error())
	}
	if templateProbes != 0 {
		t.Errorf("the template route was probed %d time(s) for a failure that says nothing about the id", templateProbes)
	}
}
