package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"gopkg.in/yaml.v3"
)

// typedRig is one fake Home Assistant with the companion on the same server,
// holding the typed definitions the automation, script, template and helper
// write commands act on. It logs every HTTP request it answers in order, so a
// test can say exactly what a run sent — and that a refused plan sent no write.
//
// What it holds:
//
//	automation heating    config id `heating`, live as automation.heating
//	automation legacy     a YAML automation without `id:`, live as
//	                      automation.legacy; the companion's GET has no entry
//	                      for it (it matches config ids only)
//	script wakeup         registered as script.wakeup
//	template room_temp    registered as sensor.room_temp (platform template)
//	helper guest_mode     input_boolean, from YAML, registered
type typedRig struct {
	ts *cmdTestServer

	mu  sync.Mutex
	log []string
	// bodies holds every HTTP request's line and body, in order — what the
	// D-58 comparison holds two runs to beyond the golden's method and path.
	bodies []string
}

const (
	typedHeatingEntry = "id: heating\nalias: Heating\ntriggers: []\nactions: []\n"
	typedWakeupEntry  = "wakeup:\n  alias: Wake Up\n  sequence:\n  - delay: 00:00:01\n"
	typedRoomTemp     = "unique_id: room_temp\nname: Room Temp\nstate: '{{ 21 }}'\n"
	typedGuestMode    = "guest_mode:\n  name: Guest Mode\n"
)

func newTypedRig(t *testing.T) *typedRig {
	t.Helper()
	r := &typedRig{}
	handlers := map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			typedJSON(w, http.StatusOK, []map[string]any{
				{"entity_id": "automation.heating", "state": "on", "attributes": map[string]any{"id": "heating", "friendly_name": "Heating"}},
				{"entity_id": "automation.legacy", "state": "on", "attributes": map[string]any{"friendly_name": "Legacy"}},
				{"entity_id": "script.wakeup", "state": "off", "attributes": map[string]any{"friendly_name": "Wake Up"}},
			})
		},
		"/api/states/script.wakeup": func(w http.ResponseWriter, _ *http.Request) {
			typedJSON(w, http.StatusOK, map[string]any{"entity_id": "script.wakeup", "state": "off", "attributes": map[string]any{}})
		},
		"/api/services/script/reload": func(w http.ResponseWriter, _ *http.Request) {
			typedJSON(w, http.StatusOK, []any{})
		},
		"/v1/config/automation": typedAutomationRoute(map[string]string{"heating": typedHeatingEntry}),
		"/v1/config/script":     typedScriptRoute(map[string]string{"wakeup": typedWakeupEntry}),
		"/v1/config/template":   typedTemplateRoute(map[string]string{"room_temp": typedRoomTemp}),
		"/v1/config/helper":     typedHelperRoute,
		"/v1/config/wiring": func(w http.ResponseWriter, req *http.Request) {
			typedJSON(w, http.StatusOK, map[string]any{"domain": req.URL.Query().Get("domain"), "wired": true, "file": "helpers.yaml"})
		},
	}
	for path, h := range handlers {
		handlers[path] = r.logged(h)
	}

	ws := map[string]any{
		"validate_config": map[string]any{
			"triggers":   map[string]any{"valid": true, "error": nil},
			"conditions": map[string]any{"valid": true, "error": nil},
			"actions":    map[string]any{"valid": true, "error": nil},
		},
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "automation.heating", "platform": "automation", "unique_id": "heating"},
			{"entity_id": "script.wakeup", "platform": "script", "unique_id": "wakeup"},
			{"entity_id": "sensor.room_temp", "platform": "template", "unique_id": "room_temp"},
			{"entity_id": "input_boolean.guest_mode", "platform": "input_boolean", "unique_id": "guest_mode"},
		},
		"config/entity_registry/remove": map[string]any{},
	}
	r.ts = startCmdServer(t, ws, handlers)
	pointAtCompanion(t, r.ts)
	r.write(t, "backups/2026-11-01T09-00-00.000000_heating.yaml", typedHeatingEntry)
	return r
}

func typedJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// typedBodyKey is the id a create body declares: its `id:`, its `unique_id:`,
// a template block's first entity domain (in the companion's order), or its
// first key. The fake must answer the same way every run, or its echo is noise
// in the golden.
func typedBodyKey(req *http.Request) string {
	raw, _ := io.ReadAll(req.Body)
	var m map[string]any
	_ = yaml.Unmarshal(raw, &m)
	if id, ok := m["id"].(string); ok {
		return id
	}
	if id, ok := m["unique_id"].(string); ok {
		return id
	}
	for _, d := range tplEntityDomains {
		if _, ok := m[d]; ok {
			return d
		}
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	if len(keys) > 0 {
		return keys[0]
	}
	return ""
}

// typedDryRunOr answers a PUT: the companion's dry run, or the applied write.
func typedDryRunOr(w http.ResponseWriter, req *http.Request) {
	_, _ = io.Copy(io.Discard, req.Body)
	if req.URL.Query().Get("dry_run") == "true" {
		typedJSON(w, http.StatusOK, map[string]any{"status": "dry_run", "diff": ""})
		return
	}
	typedJSON(w, http.StatusOK, map[string]any{"status": "applied", "reloaded": true})
}

func typedAutomationRoute(entries map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := req.URL.Query().Get("id")
		c, known := entries[id]
		switch req.Method {
		case http.MethodGet:
			if known {
				typedJSON(w, http.StatusOK, map[string]string{"id": id, "content": c})
				return
			}
			http.Error(w, "Automation not found: "+id, http.StatusNotFound)
		case http.MethodPut:
			if !known {
				http.Error(w, "Automation not found: "+id, http.StatusNotFound)
				return
			}
			typedDryRunOr(w, req)
		case http.MethodPost:
			created := typedBodyKey(req)
			typedJSON(w, http.StatusCreated, map[string]any{"status": "created", "id": created, "entity_id": "automation." + created, "reloaded": true})
		case http.MethodDelete:
			// The companion falls back to the alias and then a live
			// entity_id, so an id-less YAML automation is deletable by either
			// — the fallback the policy refuses to rely on (D-78).
			if known || id == "automation.legacy" || id == "Legacy" {
				typedJSON(w, http.StatusOK, map[string]any{"status": "deleted", "reloaded": true})
				return
			}
			http.Error(w, "Automation not found: "+id, http.StatusNotFound)
		}
	}
}

func typedScriptRoute(entries map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := req.URL.Query().Get("id")
		switch req.Method {
		case http.MethodGet:
			if c, ok := entries[id]; ok {
				typedJSON(w, http.StatusOK, map[string]string{"id": id, "content": c})
				return
			}
			http.Error(w, "Script not found: "+id, http.StatusNotFound)
		case http.MethodPut:
			typedDryRunOr(w, req)
		case http.MethodPost:
			created := typedBodyKey(req)
			typedJSON(w, http.StatusCreated, map[string]any{"status": "created", "id": created, "entity_id": "script." + created, "reloaded": true, "entity_created": true})
		case http.MethodDelete:
			typedJSON(w, http.StatusOK, map[string]any{"status": "deleted", "reloaded": true})
		}
	}
}

func typedTemplateRoute(entries map[string]string) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		id := req.URL.Query().Get("id")
		switch req.Method {
		case http.MethodGet:
			if c, ok := entries[id]; ok {
				typedJSON(w, http.StatusOK, map[string]string{"unique_id": id, "content": c})
				return
			}
			http.Error(w, "Template not found: "+id, http.StatusNotFound)
		case http.MethodPost:
			typedJSON(w, http.StatusCreated, map[string]any{"status": "created", "unique_id": typedBodyKey(req), "reloaded": true})
		case http.MethodDelete:
			typedJSON(w, http.StatusOK, map[string]any{"status": "deleted", "reloaded": true})
		}
	}
}

func typedHelperRoute(w http.ResponseWriter, req *http.Request) {
	id := req.URL.Query().Get("id")
	switch req.Method {
	case http.MethodGet:
		if id == "guest_mode" {
			typedJSON(w, http.StatusOK, map[string]string{"id": id, "domain": "input_boolean", "content": typedGuestMode, "source": "yaml"})
			return
		}
		http.Error(w, "Helper not found: "+id, http.StatusNotFound)
	case http.MethodPost:
		created, d := typedBodyKey(req), req.URL.Query().Get("domain")
		typedJSON(w, http.StatusCreated, map[string]any{"status": "created", "id": created, "entity_id": d + "." + created, "reloaded": true, "entity_created": true})
	case http.MethodDelete:
		typedJSON(w, http.StatusOK, map[string]any{"status": "deleted", "reloaded": true})
	}
}

// logged records a request as `METHOD /path?query` (query keys sorted).
func (r *typedRig) logged(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		q := req.URL.Query()
		keys := make([]string, 0, len(q))
		for k := range q {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, k+"="+q.Get(k))
		}
		line := req.Method + " " + req.URL.Path
		if len(parts) > 0 {
			line += "?" + strings.Join(parts, "&")
		}
		raw, _ := io.ReadAll(req.Body)
		req.Body = io.NopCloser(strings.NewReader(string(raw)))
		r.mu.Lock()
		r.log = append(r.log, line)
		r.bodies = append(r.bodies, line+"\n"+req.Header.Get("Content-Type")+"\n"+string(raw))
		r.mu.Unlock()
		h(w, req)
	}
}

// write puts a file into the instance directory and returns its path.
func (r *typedRig) write(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(r.ts.dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// requests returns the HTTP log so far and clears it.
func (r *typedRig) requests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.log
	r.log = nil
	return out
}

// sentBodies returns every request's line, content type and body so far, and
// clears them.
func (r *typedRig) sentBodies() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.bodies
	r.bodies = nil
	return out
}

// sentWrites is what a run sent that changes something: a companion write
// that is not a dry run, a service call, and every entity registry remove.
func (r *typedRig) sentWrites(log []string) []string {
	var out []string
	for _, l := range log {
		method, rest, _ := strings.Cut(l, " ")
		switch {
		case strings.HasPrefix(rest, "/api/services/"):
			out = append(out, l)
		case strings.HasPrefix(rest, "/v1/config/") && method != http.MethodGet && !strings.Contains(rest, "dry_run=true"):
			out = append(out, l)
		}
	}
	if n := r.ts.commandCount("config/entity_registry/remove"); n > 0 {
		out = append(out, fmt.Sprintf("ws config/entity_registry/remove ×%d", n))
	}
	return out
}

// wsCounts is the websocket command count per type, sorted, for a golden.
func (r *typedRig) wsCounts() []string {
	r.ts.mu.Lock()
	defer r.ts.mu.Unlock()
	out := make([]string, 0, len(r.ts.cmdCounts))
	for k, n := range r.ts.cmdCounts {
		out = append(out, fmt.Sprintf("%s ×%d", k, n))
	}
	slices.Sort(out)
	return out
}

// resetWS forgets the websocket counts.
func (r *typedRig) resetWS() {
	r.ts.mu.Lock()
	defer r.ts.mu.Unlock()
	r.ts.cmdCounts = map[string]int{}
}
