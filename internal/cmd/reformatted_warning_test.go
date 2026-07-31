package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The companion splices one entry's lines and, when it cannot, re-serializes
// the whole file and reports `reformatted: true` (companion C-14). hactl has to
// pass that on: the whole point of the flag is that a caller keeping config in
// git can tell "your entry changed" from "the file was rewritten", and a field
// decoded but never rendered is the D45 shape that let `reloaded: false` read
// as success.
//
// Each case drives a create against a companion stub that reports the fallback,
// and requires the warning in the human output AND in `warnings` under --json —
// the two audiences H-10 separates.

// startReformattingCompanion stands up a companion stub whose writes all report
// the whole-file fallback, and whose wiring probe answers "wired" so a
// `helper create` preview does not refuse before reaching the write.
func startReformattingCompanion(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
	mux.HandleFunc("/v1/config/wiring", func(w http.ResponseWriter, r *http.Request) {
		domain := r.URL.Query().Get("domain")
		ok(w, fmt.Sprintf(`{"status":"ok","domain":%q,"wired":true,"file":"%s.yaml"}`, domain, domain))
	})
	mux.HandleFunc("/v1/config/automation", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, `{"status":"ok","id":"morning","entity_id":"automation.morning","reloaded":true,"reformatted":true}`)
	})
	mux.HandleFunc("/v1/config/script", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, `{"status":"ok","id":"wakeup","reloaded":true,"reformatted":true}`)
	})
	mux.HandleFunc("/v1/config/helper", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, `{"status":"ok","id":"guest_mode","domain":"input_boolean","entity_id":"input_boolean.guest_mode",`+
			`"reloaded":true,"entity_created":true,"reformatted":true}`)
	})
	mux.HandleFunc("/v1/config/template", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, `{"status":"ok","unique_id":"room_temp","reloaded":true,"reformatted":true}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestConfirmedWriteReportsAWholeFileRewrite(t *testing.T) {
	srv := startReformattingCompanion(t)

	cases := []struct {
		name    string
		payload string
		args    []string
	}{
		{
			"auto create",
			"id: morning\nalias: Morning\ntriggers: []\nconditions: []\nactions: []\n",
			[]string{"auto", "create"},
		},
		{
			"script create",
			"wakeup:\n  alias: Wake Up\n  sequence: []\n",
			[]string{"script", "create"},
		},
		{
			"helper create",
			"guest_mode:\n  name: Guest Mode\n",
			[]string{"helper", "create", "input_boolean"},
		},
		{
			"tpl create",
			"unique_id: room_temp\nname: Room Temp\nstate: \"{{ 1 }}\"\n",
			[]string{"tpl", "create"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			env := fmt.Sprintf("HA_URL=http://127.0.0.1:19999\nHA_TOKEN=test\nCOMPANION_URL=%s\n", srv.URL)
			if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
				t.Fatal(err)
			}
			payload := filepath.Join(dir, "payload.yaml")
			if err := os.WriteFile(payload, []byte(tc.payload), 0o600); err != nil {
				t.Fatal(err)
			}

			run := func(extra ...string) string {
				t.Helper()
				args := append(append([]string{}, tc.args...), "--file", payload, "--confirm", "--dir", dir)
				args = append(args, extra...)
				out, err := runCLI(t, args...)
				if err != nil {
					t.Fatalf("%v: %v\noutput: %s", args, err, out)
				}
				return out
			}

			if text := run(); !strings.Contains(text, "re-serialized") {
				t.Errorf("human output does not report the whole-file rewrite:\n%s", text)
			}

			raw := run("--json")
			var doc struct {
				Warnings []string `json:"warnings"`
			}
			body := raw[strings.Index(raw, "{"):]
			if err := json.Unmarshal([]byte(body), &doc); err != nil {
				t.Fatalf("--json output is not JSON: %v\n%s", err, raw)
			}
			found := false
			for _, warning := range doc.Warnings {
				if strings.Contains(warning, "re-serialized") {
					found = true
				}
			}
			if !found {
				t.Errorf("warnings do not carry the whole-file rewrite: %#v", doc.Warnings)
			}
		})
	}
}

// A write the companion spliced must stay silent — otherwise the warning says
// nothing and every caller learns to ignore it.
func TestASurgicalWriteReportsNoRewrite(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/config/automation", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","id":"morning","entity_id":"automation.morning","reloaded":true}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	env := fmt.Sprintf("HA_URL=http://127.0.0.1:19999\nHA_TOKEN=test\nCOMPANION_URL=%s\n", srv.URL)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(dir, "payload.yaml")
	if err := os.WriteFile(payload, []byte("id: morning\nalias: Morning\ntriggers: []\nconditions: []\nactions: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "auto", "create", "--file", payload, "--confirm", "--dir", dir)
	if err != nil {
		t.Fatalf("%v\noutput: %s", err, out)
	}
	if strings.Contains(out, "re-serialized") {
		t.Errorf("a spliced write warned about a rewrite that did not happen:\n%s", out)
	}
}
