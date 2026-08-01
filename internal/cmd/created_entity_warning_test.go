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

// Finding #91: a reload that answered 200 was reported as a created entity.
//
// Home Assistant validates each template and script entry *during* the reload
// it was asked for. An entry that fails its per-domain schema is logged and
// skipped, and the reload service call still succeeds — so `reloaded: true`
// with no `reload_error` is exactly what a create that produced nothing looks
// like. `tpl create` said `created template "pg_w2_select1" (domain=select)`
// for an entity that never existed, and the only account of why was in HA's
// own log, which the caller had no reason to open because the command had
// reported success.
//
// The companion now polls for the entity after the reload and reports what it
// found, per entity. These cases prove hactl READS that: a field decoded and
// never rendered is the same defect one layer up, and it is the one this
// project keeps re-finding (`reformatted` was documented by twelve responses
// and read by none).
//
// The stub answers `reloaded: true` throughout — that is the whole point. A
// case that also failed the reload would pass against the old code, which
// already warned about a failed reload.

func startDroppedEntityCompanion(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body)) //nolint:gosec // body is a literal written by this test, not caller input
	}
	mux.HandleFunc("/v1/config/wiring", func(w http.ResponseWriter, r *http.Request) {
		domain := r.URL.Query().Get("domain")
		ok(w, fmt.Sprintf(`{"status":"ok","domain":%q,"wired":true,"file":"%s.yaml"}`, domain, domain))
	})
	// Reloaded, and no entity: HA took the file and dropped the entry.
	mux.HandleFunc("/v1/config/template", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, `{"status":"ok","unique_id":"pg_select","reloaded":true,`+
			`"entities":[{"unique_id":"pg_select","domain":"select","entity_id":null,"created":false}]}`)
	})
	mux.HandleFunc("/v1/config/script", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, `{"status":"ok","id":"wakeup","reloaded":true,"entity_id":"","entity_created":false}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestAReloadedWriteThatProducedNoEntitySaysSo(t *testing.T) {
	srv := startDroppedEntityCompanion(t)

	cases := []struct {
		name    string
		payload string
		args    []string
		// witness is a fragment only the new warning contains — never a word
		// the success line already carried, or the case would pass without it.
		witness string
	}{
		{
			name:    "tpl create",
			payload: "unique_id: pg_select\nname: PG Select\nstate: \"{{ 'a' }}\"\n",
			args:    []string{"tpl", "create", "--domain", "select"},
			witness: "registered no entity",
		},
		{
			name:    "script create",
			payload: "wakeup:\n  alias: Wake Up\n  sequence: []\n",
			args:    []string{"script", "create"},
			witness: "no script entity appeared",
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

			if text := run(); !strings.Contains(text, tc.witness) {
				t.Errorf("a create HA dropped reported no such thing — the caller is told it worked:\n%s", text)
			}

			raw := run("--json")
			var doc struct {
				Warnings []string `json:"warnings"`
			}
			_, body, found := strings.Cut(raw, "{")
			if !found {
				t.Fatalf("--json output carries no JSON document:\n%s", raw)
			}
			if err := json.Unmarshal([]byte("{"+body), &doc); err != nil {
				t.Fatalf("--json output is not JSON: %v\n%s", err, raw)
			}
			warned := false
			for _, warning := range doc.Warnings {
				if strings.Contains(warning, tc.witness) {
					warned = true
				}
			}
			if !warned {
				t.Errorf("a machine caller is told nothing about the dropped entity: %#v", doc.Warnings)
			}
		})
	}
}

// The converse, so the warning means something: a create whose entity HA really
// did register must stay silent. A warning that fires on success is a warning
// every caller learns to ignore.
func TestACreateThatProducedItsEntityStaysSilent(t *testing.T) {
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, body string) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body)) //nolint:gosec // body is a literal written by this test, not caller input
	}
	mux.HandleFunc("/v1/config/wiring", func(w http.ResponseWriter, r *http.Request) {
		domain := r.URL.Query().Get("domain")
		ok(w, fmt.Sprintf(`{"status":"ok","domain":%q,"wired":true,"file":"%s.yaml"}`, domain, domain))
	})
	mux.HandleFunc("/v1/config/template", func(w http.ResponseWriter, _ *http.Request) {
		ok(w, `{"status":"ok","unique_id":"room_temp","reloaded":true,`+
			`"entities":[{"unique_id":"room_temp","domain":"sensor","entity_id":"sensor.room_temp","created":true}]}`)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	env := fmt.Sprintf("HA_URL=http://127.0.0.1:19999\nHA_TOKEN=test\nCOMPANION_URL=%s\n", srv.URL)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	payload := filepath.Join(dir, "payload.yaml")
	if err := os.WriteFile(payload, []byte("unique_id: room_temp\nname: Room Temp\nstate: \"{{ 1 }}\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runCLI(t, "tpl", "create", "--file", payload, "--confirm", "--dir", dir)
	if err != nil {
		t.Fatalf("tpl create: %v\n%s", err, out)
	}
	if strings.Contains(out, "registered no entity") {
		t.Errorf("a create HA honoured was reported as dropped:\n%s", out)
	}
}
