//go:build integration

package integration

import (
	"encoding/json"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// Oracle for the default Lovelace dashboard's states (D-3 / D-6, defects
// D67 / D71).
//
// hactl could not truthfully classify the DEFAULT dashboard: `ref replace`
// gated it on a `mode` field `lovelace/info` does not emit (D71), and
// `dash show` had no honest answer for the auto-generated state (D67).
// The facts below were probed against a live HA before implementing (TC-1),
// and these tests keep asserting them against every HA image the tier runs,
// so a future HA that changes any of them fails loudly here rather than
// silently re-opening the defects.
//
// Captured 2026-07-27 from HA 2026.7.4
// (ghcr.io/home-assistant/home-assistant:stable):
//
//  1. Fresh instance (auto-generated default):
//     lovelace/config            → success=false,
//     error={"code":"config_not_found","message":"No config found."}
//     lovelace/info              → {"resource_mode":"storage"} — NO `mode` key
//     lovelace/dashboards/list   → [] — the default is not listed
//
//  2. After lovelace/config/save:
//     lovelace/config            → the stored document, verbatim
//     lovelace/info              → {"resource_mode":"storage"} — identical to
//     the fresh state, so it carries zero signal about the default dashboard
//     lovelace/dashboards/list   → [] — still not listed
//     lovelace/config/delete     → success, and lovelace/config answers
//     config_not_found again (the auto-generated state is restorable)
//
//  3. YAML mode (configuration.yaml `lovelace: {mode: yaml}` + ui-lovelace.yaml,
//     fixture testdata/fixtures/lovelace-yaml):
//     lovelace/config            → the ui-lovelace.yaml content as JSON, both
//     with no url_path and with url_path "lovelace" (same document)
//     lovelace/info              → {"resource_mode":"yaml"}
//     lovelace/config/save       → success=false,
//     error={"code":"error","message":"Not supported"} — retrievable, not writable
//     lovelace/dashboards/list   → the default IS listed:
//     [{"title":"Overview",...,"mode":"yaml","filename":"ui-lovelace.yaml",
//     "url_path":"lovelace"}] — and "lovelace" is a slug no user-created
//     dashboard can take (lovelace/dashboards/create requires a hyphen).
// ============================================================================

// rawWS is a minimal raw WebSocket session against HA, used where the oracle
// needs the unfiltered wire envelope (haapi decodes into structs and would
// hide an unknown or absent field — the exact blindness D71 came from).
type rawWS struct {
	conn   *websocket.Conn
	nextID int64
}

// rawWSEnvelope is the full HA WS result envelope, kept raw.
type rawWSEnvelope struct {
	ID      int64           `json:"id"`
	Type    string          `json:"type"`
	Success bool            `json:"success"`
	Result  json.RawMessage `json:"result"`
	Error   json.RawMessage `json:"error"`
}

// errCode extracts the error code from a failed envelope.
func (e rawWSEnvelope) errCode(t *testing.T) string {
	t.Helper()
	var werr struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(e.Error, &werr); err != nil {
		t.Fatalf("parsing error envelope %s: %v", e.Error, err)
	}
	return werr.Code
}

func dialRawWS(t *testing.T, baseURL, token string) *rawWS {
	t.Helper()
	u, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parsing HA URL: %v", err)
	}
	u.Scheme = "ws"
	u.Path = "/api/websocket"
	conn, resp, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		t.Fatalf("dialing HA websocket: %v", err)
	}
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	t.Cleanup(func() { _ = conn.Close() })

	var hello map[string]any
	if err := conn.ReadJSON(&hello); err != nil {
		t.Fatalf("reading auth_required: %v", err)
	}
	if err := conn.WriteJSON(map[string]string{"type": "auth", "access_token": token}); err != nil {
		t.Fatalf("sending auth: %v", err)
	}
	var authResp map[string]any
	if err := conn.ReadJSON(&authResp); err != nil {
		t.Fatalf("reading auth response: %v", err)
	}
	if authResp["type"] != "auth_ok" {
		t.Fatalf("auth failed: %v", authResp)
	}
	return &rawWS{conn: conn}
}

// send issues one WS command and returns the raw envelope.
func (r *rawWS) send(t *testing.T, msg map[string]any) rawWSEnvelope {
	t.Helper()
	r.nextID++
	msg["id"] = r.nextID
	if err := r.conn.WriteJSON(msg); err != nil {
		t.Fatalf("sending %v: %v", msg["type"], err)
	}
	var env rawWSEnvelope
	if err := r.conn.ReadJSON(&env); err != nil {
		t.Fatalf("reading %v response: %v", msg["type"], err)
	}
	return env
}

// mustSucceed fails the test when the envelope carries an error.
func (r *rawWS) mustSucceed(t *testing.T, msg map[string]any) rawWSEnvelope {
	t.Helper()
	cmdType := msg["type"]
	env := r.send(t, msg)
	if !env.Success {
		t.Fatalf("%v failed: %s", cmdType, env.Error)
	}
	return env
}

// assertInfoCarriesNoModeField pins the wire fact D71 grew from: whatever
// state the default dashboard is in, `lovelace/info` answers WITHOUT a `mode`
// key. The moment HA starts emitting one, this fails and the classification
// in internal/cmd/dash.go deserves a fresh look.
func assertInfoCarriesNoModeField(t *testing.T, ws *rawWS, state string) {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{"type": "lovelace/info"})
	var info map[string]any
	if err := json.Unmarshal(env.Result, &info); err != nil {
		t.Fatalf("parsing lovelace/info: %v", err)
	}
	if _, hasMode := info["mode"]; hasMode {
		t.Errorf("lovelace/info now emits a `mode` field in the %s state (%s) — the D71 classification "+
			"assumption changed, revisit classifyDefaultDashboard", state, env.Result)
	}
	if _, hasResourceMode := info["resource_mode"]; !hasResourceMode {
		t.Errorf("lovelace/info no longer emits resource_mode in the %s state: %s", state, env.Result)
	}
}

// deleteDefaultDashboardConfig restores the auto-generated state.
// WS lovelace/config/delete succeeds even when no config is stored.
func deleteDefaultDashboardConfig(t *testing.T, inst *hatest.Instance) {
	t.Helper()
	ws := dialRawWS(t, inst.URL(), inst.Token())
	ws.mustSucceed(t, map[string]any{"type": "lovelace/config/delete"})
}

// storeDefaultDashboardConfig saves cfg as the default dashboard's stored
// config and registers a cleanup that restores the auto-generated state.
func storeDefaultDashboardConfig(t *testing.T, inst *hatest.Instance, cfg map[string]any) {
	t.Helper()
	ws := dialRawWS(t, inst.URL(), inst.Token())
	ws.mustSucceed(t, map[string]any{"type": "lovelace/config/save", "config": cfg})
	t.Cleanup(func() { deleteDefaultDashboardConfig(t, inst) })
}

// TestOracleLovelaceDefaultDashboardStates walks the default dashboard through
// auto-generated → stored → auto-generated on a live HA and asserts the wire
// facts classifyDefaultDashboard is built on.
func TestOracleLovelaceDefaultDashboardStates(t *testing.T) {
	ws := dialRawWS(t, ha.URL(), ha.Token())

	// --- State A: auto-generated ---
	// Make the state explicit rather than assumed: delete always succeeds.
	ws.mustSucceed(t, map[string]any{"type": "lovelace/config/delete"})

	envFresh := ws.send(t, map[string]any{"type": "lovelace/config"})
	if envFresh.Success {
		t.Fatalf("auto-generated state: lovelace/config unexpectedly succeeded: %s", envFresh.Result)
	}
	if code := envFresh.errCode(t); code != "config_not_found" {
		t.Fatalf("auto-generated state answers code %q, want config_not_found — the classification key changed", code)
	}
	assertInfoCarriesNoModeField(t, ws, "auto-generated")

	listFresh := ws.mustSucceed(t, map[string]any{"type": "lovelace/dashboards/list"})
	var freshEntries []map[string]any
	if err := json.Unmarshal(listFresh.Result, &freshEntries); err != nil {
		t.Fatalf("parsing dashboards/list: %v", err)
	}
	for _, e := range freshEntries {
		if e["url_path"] == "lovelace" {
			t.Errorf("a storage-capable default is now listed under url_path 'lovelace' — the yaml-default "+
				"detection in dashboardScanTargets would misfire: %s", listFresh.Result)
		}
	}

	// --- Transition: store a config ---
	cfg := map[string]any{"views": []any{map[string]any{
		"title": "Oracle", "path": "oracle",
		"cards": []any{map[string]any{"type": "markdown", "content": "stored by the oracle"}},
	}}}
	ws.mustSucceed(t, map[string]any{"type": "lovelace/config/save", "config": cfg})
	t.Cleanup(func() { deleteDefaultDashboardConfig(t, ha) })

	// --- State B: stored ---
	envStored := ws.mustSucceed(t, map[string]any{"type": "lovelace/config"})
	var storedDoc, wantDoc map[string]any
	if err := json.Unmarshal(envStored.Result, &storedDoc); err != nil {
		t.Fatalf("parsing stored config: %v", err)
	}
	wantJSON, _ := json.Marshal(cfg)
	if err := json.Unmarshal(wantJSON, &wantDoc); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(storedDoc, wantDoc) {
		t.Errorf("stored state must return the saved document:\n got:  %s\n want: %s", envStored.Result, wantJSON)
	}
	// lovelace/info is identical in both states — the fact that made the old
	// `info.Mode == "storage"` gate vacuous rather than merely wrong.
	assertInfoCarriesNoModeField(t, ws, "stored")

	// --- Reset: delete restores the auto-generated state ---
	ws.mustSucceed(t, map[string]any{"type": "lovelace/config/delete"})
	envAfter := ws.send(t, map[string]any{"type": "lovelace/config"})
	if envAfter.Success {
		t.Fatalf("after config/delete, lovelace/config still answers a document: %s", envAfter.Result)
	}
	if code := envAfter.errCode(t); code != "config_not_found" {
		t.Errorf("after config/delete the error code is %q, want config_not_found", code)
	}
}

// TestOracleLovelaceYamlModeDefault pins the third state: a default dashboard
// pinned to YAML mode is retrievable but not writable, and — unlike the other
// two states — it appears in lovelace/dashboards/list itself, under the
// reserved slug "lovelace". hactl's honest answers on top of that wire:
// `dash show` renders the views summary, and `dash grep` reports the
// dashboard exactly once (not once as "(default)" and again as "lovelace").
func TestOracleLovelaceYamlModeDefault(t *testing.T) {
	inst := hatest.Start(t, hatest.WithFixture("lovelace-yaml"))
	ws := dialRawWS(t, inst.URL(), inst.Token())

	// Retrievable: no url_path and url_path "lovelace" answer the same document.
	envNoPath := ws.mustSucceed(t, map[string]any{"type": "lovelace/config"})
	envByPath := ws.mustSucceed(t, map[string]any{"type": "lovelace/config", "url_path": "lovelace"})
	var docNoPath, docByPath map[string]any
	if err := json.Unmarshal(envNoPath.Result, &docNoPath); err != nil {
		t.Fatalf("parsing yaml-mode config: %v", err)
	}
	if err := json.Unmarshal(envByPath.Result, &docByPath); err != nil {
		t.Fatalf("parsing yaml-mode config by url_path: %v", err)
	}
	if !reflect.DeepEqual(docNoPath, docByPath) {
		t.Errorf("yaml default answers different documents with and without url_path:\n %s\n %s",
			envNoPath.Result, envByPath.Result)
	}
	if !strings.Contains(string(envNoPath.Result), "YamlProbe") {
		t.Errorf("yaml-mode config does not carry the fixture's view: %s", envNoPath.Result)
	}

	// Not writable: save is refused.
	envSave := ws.send(t, map[string]any{"type": "lovelace/config/save",
		"config": map[string]any{"views": []any{}}})
	if envSave.Success {
		t.Fatal("lovelace/config/save succeeded for a yaml-mode default — the not-writable assumption changed")
	}

	// Listed as the reserved slug, mode yaml.
	envList := ws.mustSucceed(t, map[string]any{"type": "lovelace/dashboards/list"})
	var entries []map[string]any
	if err := json.Unmarshal(envList.Result, &entries); err != nil {
		t.Fatalf("parsing dashboards/list: %v", err)
	}
	var yamlDefault map[string]any
	for _, e := range entries {
		if e["url_path"] == "lovelace" {
			yamlDefault = e
		}
	}
	if yamlDefault == nil {
		t.Fatalf("yaml default is not listed under url_path 'lovelace': %s", envList.Result)
	}
	if yamlDefault["mode"] != "yaml" {
		t.Errorf("listed yaml default has mode %v, want yaml", yamlDefault["mode"])
	}

	// hactl on top of it: `dash show` renders the summary of the yaml config…
	out := runHactlDir(t, inst.Dir(), "dash", "show")
	if !strings.Contains(out, "YamlProbe") {
		t.Errorf("dash show did not render the yaml default's view:\n%s", out)
	}

	// …and `dash grep` reports the one dashboard once. Before the
	// dashboardScanTargets dedupe it appeared twice: once as the "(default)"
	// pseudo-target and once as the listed "lovelace" entry.
	grep := runHactlDir(t, inst.Dir(), "dash", "grep", "yaml mode oracle")
	if got := strings.Count(grep, "views[0].cards[0].content"); got != 1 {
		t.Errorf("dash grep reported the yaml default %d time(s), want exactly 1:\n%s", got, grep)
	}
}
