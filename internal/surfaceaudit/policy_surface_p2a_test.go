// Stage-2a tests for the H-29 closure gate's extractor (PolicySurface), written
// from the contract (dev/surfaces/policy.manifest header, INVARIANTS.md H-29,
// D-80, D-81) before the extractor exists.
//
// WIRING (integrated 2026-11-01). Untagged now, against the real
// surfaceaudit.PolicySurface. History: built only with `-tags p2a_contract`; until the extractor lands they
// compile against internal/surfaceaudit/policy_stub_p2a.go (`-tags p2a_stub`)
// and fail:
//
//	go test -tags 'p2a_contract p2a_stub' ./internal/surfaceaudit/ -run Policy
//
// The integrator deletes the stub and this build line. The API assumed is
// documented on the stub: PolicySurface(root string, commands []Site).
package surfaceaudit_test

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/surfaceaudit"
)

// ---------------------------------------------------------------------------
// A miniature product tree, in the shapes the real one uses: sendCommand and a
// direct conn.WriteJSON in haapi, SupervisorAPI, the auth handshake, the
// haapi.Client request helpers, companion.Endpoints and the companion.Client
// request helpers.
// ---------------------------------------------------------------------------

const fixtureWebsocket = `package haapi

import (
	"context"
	"encoding/json"
	"maps"
)

type wsConn interface {
	WriteJSON(v any) error
}

type WSClient struct{ conn wsConn }

func (ws *WSClient) LabelRegistryCreate(ctx context.Context, name string) error {
	_, err := ws.sendCommand(ctx, "config/label_registry/create", map[string]any{"name": name})
	return err
}

func (ws *WSClient) TraceGet(ctx context.Context, domain string) error {
	_ = ctx
	msg := map[string]any{
		"id":     1,
		"type":   "trace/get",
		"domain": domain,
	}
	return ws.conn.WriteJSON(msg)
}

func (ws *WSClient) SupervisorAPI(ctx context.Context, endpoint, method string, data map[string]any) (json.RawMessage, error) {
	if method == "" {
		method = "get"
	}
	params := map[string]any{"endpoint": endpoint, "method": method}
	if data != nil {
		params["data"] = data
	}
	return ws.sendCommand(ctx, "supervisor/api", params)
}

func (ws *WSClient) IngressSession(ctx context.Context) error {
	_, err := ws.SupervisorAPI(ctx, "/ingress/session", "post", nil)
	return err
}

func (ws *WSClient) sendCommand(ctx context.Context, cmdType string, params map[string]any) (json.RawMessage, error) {
	_ = ctx
	msg := map[string]any{
		"id":   1,
		"type": cmdType,
	}
	maps.Copy(msg, params)
	return nil, ws.conn.WriteJSON(msg)
}

func authenticate(conn wsConn, token string) error {
	authMsg := map[string]string{
		"type":         "auth",
		"access_token": token,
	}
	return conn.WriteJSON(authMsg)
}
`

const fixtureClient = `package haapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
)

type Client struct {
	baseURL string
	http    *http.Client
}

func (c *Client) GetStates(ctx context.Context) ([]byte, error) {
	return c.doGet(ctx, "/api/states")
}

func (c *Client) RenderTemplate(ctx context.Context, tpl string) ([]byte, error) {
	return c.doPost(ctx, "/api/template", map[string]string{"template": tpl})
}

func (c *Client) doGet(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *Client) doDelete(ctx context.Context, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+path, nil)
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *Client) doPost(ctx context.Context, path string, body any) ([]byte, error) {
	encoded, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	return c.do(req)
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()
	return nil, nil
}
`

const fixtureContract = `package companion

type Endpoint struct {
	Method   string
	Path     string
	Response any
}

var Endpoints = []Endpoint{
	{"GET", "/v1/health", nil},
	{"DELETE", "/v1/config/script", nil},
}
`

const fixtureContractWithRestart = `package companion

type Endpoint struct {
	Method   string
	Path     string
	Response any
}

var Endpoints = []Endpoint{
	{"GET", "/v1/health", nil},
	{"DELETE", "/v1/config/script", nil},
	{"POST", "/v1/ha/restart", nil},
}
`

const fixtureCompanionClient = `package companion

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

type Client struct {
	base string
	http *http.Client
}

func (c *Client) Health(ctx context.Context) ([]byte, error) {
	return c.doGet(ctx, "/v1/health", nil)
}

func (c *Client) DeleteScriptDef(ctx context.Context, id string) ([]byte, error) {
	q := url.Values{"id": {id}}
	return c.doDelete(ctx, "/v1/config/script", q)
}

func (c *Client) route(p string) string { return c.base + p }

func (c *Client) doGet(ctx context.Context, path string, query url.Values) ([]byte, error) {
	return c.send(ctx, http.MethodGet, path, query, "")
}

func (c *Client) doDelete(ctx context.Context, path string, query url.Values) ([]byte, error) {
	return c.send(ctx, http.MethodDelete, path, query, "")
}

func (c *Client) doPostBody(ctx context.Context, path string, query url.Values, content, contentType string) ([]byte, error) {
	_ = contentType
	return c.send(ctx, http.MethodPost, path, query, content)
}

func (c *Client) send(ctx context.Context, method, path string, query url.Values, content string) ([]byte, error) {
	u := c.route(path)
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, strings.NewReader(content))
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	_ = resp.Body.Close()
	return nil, nil
}
`

// fixtureKeys are the sites the baseline tree holds, in manifest grammar.
var fixtureKeys = []string{
	"ws auth",
	"ws config/label_registry/create",
	"ws trace/get",
	"ws supervisor/api POST /ingress/session",
	"rest GET /api/states",
	"rest POST /api/template",
	"companion GET /v1/health",
	"companion DELETE /v1/config/script",
	"cmd hactl label create",
}

var fixtureCensus = []surfaceaudit.Site{{Key: "hactl label create", File: "cobra tree", Note: "writes; --confirm gated"}}

// policyFixture lays out the baseline tree plus extra files, and a policy
// manifest dispositioning exactly the baseline's sites.
func policyFixture(t *testing.T, extra map[string]string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"internal/haapi/websocket.go":    fixtureWebsocket,
		"internal/haapi/client.go":       fixtureClient,
		"internal/companion/contract.go": fixtureContract,
		"internal/companion/client.go":   fixtureCompanionClient,
	}
	maps.Copy(files, extra)
	var manifest strings.Builder
	manifest.WriteString("# fixture policy manifest\n#ceiling 20\n")
	for _, k := range fixtureKeys {
		manifest.WriteString(k + " = debt: a fixture row whose reason is long enough to be one\n")
	}
	files[filepath.Join(surfaceaudit.ManifestDir, "policy.manifest")] = manifest.String()
	for rel, body := range files {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if body == "" {
			continue // an empty entry deletes a baseline file
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func policyKeys(s surfaceaudit.Surface) []string {
	keys := make([]string, 0, len(s.Sites))
	for _, site := range s.Sites {
		keys = append(keys, site.Key)
	}
	sort.Strings(keys)
	return keys
}

func checkPolicyFixture(t *testing.T, root string, census []surfaceaudit.Site) (surfaceaudit.Surface, surfaceaudit.Result) {
	t.Helper()
	s, err := surfaceaudit.PolicySurface(root, census)
	if err != nil {
		t.Fatalf("deriving the policy surface of the fixture: %v", err)
	}
	if s.Name != "policy" {
		t.Fatalf("surface name %q, want policy — it is the manifest's basename", s.Name)
	}
	m, err := surfaceaudit.LoadManifest(root, s.Name)
	if err != nil {
		t.Fatalf("loading the fixture manifest: %v", err)
	}
	return s, surfaceaudit.Check(s, m, func(string) bool { return true })
}

func unclassifiedKeys(r surfaceaudit.Result) []string {
	out := make([]string, 0, len(r.Unclassified))
	for _, s := range r.Unclassified {
		out = append(out, s.Key)
	}
	sort.Strings(out)
	return out
}

// TestPolicyExtractorDerivesTheFixtureExactly — on a tree whose every site the
// manifest names, the extractor finds exactly those sites and the gate is
// green. This is the "before" of every watched-to-fail case below: without it,
// a red gate after an injection could be red for any reason.
func TestPolicyExtractorDerivesTheFixtureExactly(t *testing.T) {
	root := policyFixture(t, nil)
	s, res := checkPolicyFixture(t, root, fixtureCensus)
	want := slices.Clone(fixtureKeys)
	sort.Strings(want)
	if got := policyKeys(s); !slices.Equal(got, want) {
		t.Errorf("derived sites\n  %s\nwant\n  %s", strings.Join(got, "\n  "), strings.Join(want, "\n  "))
	}
	if res.Failed() {
		t.Errorf("the baseline fixture must pass the gate:%s", res.Report())
	}
	// supervisor/api is a generic proxy: keyed on (method, endpoint), never on
	// the type alone — and the generic sender's own message is no site.
	for _, k := range policyKeys(s) {
		if k == "ws supervisor/api" || strings.Contains(k, "cmdType") {
			t.Errorf("site %q is the generic sender, not a request hactl sends", k)
		}
	}
}

// TestPolicyGateFailsOnANewSite — H-29's "watched to fail": a request site
// added without a manifest row turns the gate red as unclassified, naming
// exactly that site, for every family the contract derives.
func TestPolicyGateFailsOnANewSite(t *testing.T) {
	for _, tc := range []struct {
		name   string
		extra  map[string]string
		census []surfaceaudit.Site
		want   string // exact key, or a regexp when it starts with ^
	}{
		{
			name: "a new sendCommand type",
			extra: map[string]string{"internal/haapi/injected.go": `package haapi

import "context"

func (ws *WSClient) WipeEntityRegistry(ctx context.Context) error {
	_, err := ws.sendCommand(ctx, "config/entity_registry/remove_all", nil)
	return err
}
`},
			want: "ws config/entity_registry/remove_all",
		},
		{
			name: "a new direct WriteJSON",
			extra: map[string]string{"internal/haapi/injected.go": `package haapi

func (ws *WSClient) SubscribeEverything() error {
	msg := map[string]any{"id": 9, "type": "subscribe_events"}
	return ws.conn.WriteJSON(msg)
}
`},
			want: "ws subscribe_events",
		},
		{
			name: "a new supervisor endpoint",
			extra: map[string]string{"internal/haapi/injected.go": `package haapi

import "context"

func (ws *WSClient) RestartAddon(ctx context.Context, slug string) error {
	_, err := ws.SupervisorAPI(ctx, "/addons/"+slug+"/restart", "post", nil)
	return err
}
`},
			want: "^ws supervisor/api POST /addons/\\{[a-z_]+\\}/restart$",
		},
		{
			name: "a new REST path",
			extra: map[string]string{"internal/haapi/injected.go": `package haapi

import "context"

func (c *Client) FireEvent(ctx context.Context) ([]byte, error) {
	return c.doPost(ctx, "/api/events/hactl_fired", nil)
}
`},
			want: "rest POST /api/events/hactl_fired",
		},
		{
			name: "a new REST path built by concatenation",
			extra: map[string]string{"internal/haapi/injected.go": `package haapi

import (
	"context"
	"net/url"
)

func (c *Client) DeleteThing(ctx context.Context, thingID string) ([]byte, error) {
	return c.doDelete(ctx, "/api/things/"+url.PathEscape(thingID))
}
`},
			want: "^rest DELETE /api/things/\\{[a-z_]+\\}$",
		},
		{
			name: "a new companion endpoint",
			extra: map[string]string{
				"internal/companion/contract.go": fixtureContractWithRestart,
				"internal/companion/injected.go": `package companion

import "context"

func (c *Client) RestartHA(ctx context.Context) ([]byte, error) {
	return c.doPostBody(ctx, "/v1/ha/restart", nil, "", "text/plain")
}
`},
			want: "companion POST /v1/ha/restart",
		},
		{
			name:   "a new write command",
			census: append(slices.Clone(fixtureCensus), surfaceaudit.Site{Key: "hactl thing wipe", File: "cobra tree", Note: "writes"}),
			want:   "cmd hactl thing wipe",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			census := tc.census
			if census == nil {
				census = fixtureCensus
			}
			_, res := checkPolicyFixture(t, policyFixture(t, tc.extra), census)
			if !res.Failed() {
				t.Fatalf("a site nobody dispositioned passed the gate:%s", res.Report())
			}
			got := unclassifiedKeys(res)
			if len(got) != 1 {
				t.Fatalf("want exactly one unclassified site (%s), got %v", tc.want, got)
			}
			if strings.HasPrefix(tc.want, "^") {
				if !regexp.MustCompile(tc.want).MatchString(got[0]) {
					t.Errorf("unclassified site %q does not match %s", got[0], tc.want)
				}
			} else if got[0] != tc.want {
				t.Errorf("unclassified site %q, want %q", got[0], tc.want)
			}
			if !strings.Contains(res.Report(), got[0]+" = debt:") {
				t.Errorf("the report must print the manifest line to add:%s", res.Report())
			}
		})
	}
}

// TestPolicyGateFailsOnACompanionPathOutsideEndpoints — the contract's "scan
// that no companion.Client method builds a path outside" companion.Endpoints:
// such a method is a request the endpoint list does not know, so it must not
// pass silently — either the extractor surfaces it as a site (unclassified
// here) or the derivation refuses.
func TestPolicyGateFailsOnACompanionPathOutsideEndpoints(t *testing.T) {
	root := policyFixture(t, map[string]string{"internal/companion/injected.go": `package companion

import "context"

func (c *Client) Sneak(ctx context.Context) ([]byte, error) {
	return c.doPostBody(ctx, "/v1/undeclared", nil, "", "text/plain")
}
`})
	s, err := surfaceaudit.PolicySurface(root, fixtureCensus)
	if err != nil {
		return // refused at derivation: the property holds
	}
	m, mErr := surfaceaudit.LoadManifest(root, "policy")
	if mErr != nil {
		t.Fatal(mErr)
	}
	res := surfaceaudit.Check(s, m, func(string) bool { return true })
	if !res.Failed() || !slices.Contains(unclassifiedKeys(res), "companion POST /v1/undeclared") {
		t.Errorf("a companion request outside companion.Endpoints passed silently; sites %v", policyKeys(s))
	}
}

// TestPolicyGateFailsOnAStaleRow — a row whose site is gone fails the gate
// (D-81: deleting a dead client method deletes its row with it).
func TestPolicyGateFailsOnAStaleRow(t *testing.T) {
	without := strings.Replace(fixtureClient, `func (c *Client) RenderTemplate(ctx context.Context, tpl string) ([]byte, error) {
	return c.doPost(ctx, "/api/template", map[string]string{"template": tpl})
}
`, "", 1)
	if without == fixtureClient {
		t.Fatal("the fixture edit did not apply")
	}
	_, res := checkPolicyFixture(t, policyFixture(t, map[string]string{"internal/haapi/client.go": without}), fixtureCensus)
	if !res.Failed() {
		t.Fatalf("a row naming a deleted site passed the gate:%s", res.Report())
	}
	stale := strings.Join(res.Stale, "\n")
	if len(res.Stale) != 1 || !strings.Contains(stale, `"rest POST /api/template"`) {
		t.Errorf("want exactly the template row stale, got %v", res.Stale)
	}
}

// TestPolicyExtractorLeavesOutTestInfrastructure — internal/hatest and
// internal/companiontest_discovery are test infrastructure and no site
// (manifest header). A token-minting WS write there must not reach the surface.
func TestPolicyExtractorLeavesOutTestInfrastructure(t *testing.T) {
	infra := `package hatest

type conn interface{ WriteJSON(v any) error }

func mintToken(c conn) error {
	return c.WriteJSON(map[string]any{"id": 1, "type": "auth/long_lived_access_token", "client_name": "e2e"})
}
`
	fake := `package companiontest_discovery

type conn interface{ WriteJSON(v any) error }

func reply(c conn) error {
	return c.WriteJSON(map[string]string{"type": "auth_ok", "ha_version": "x"})
}
`
	_, res := checkPolicyFixture(t, policyFixture(t, map[string]string{
		"internal/hatest/hatest.go":                           infra,
		"internal/companiontest_discovery/fake_supervisor.go": fake,
	}), fixtureCensus)
	if res.Failed() {
		t.Errorf("test infrastructure reached the policy surface:%s", res.Report())
	}
}

// TestPolicySurfaceRefusesAnEmptyCommandCensus — the command half is handed
// in; an empty census means the cobra walk stopped matching, and a derivation
// that accepted it would pass forever while proving nothing.
func TestPolicySurfaceRefusesAnEmptyCommandCensus(t *testing.T) {
	if _, err := surfaceaudit.PolicySurface(policyFixture(t, nil), nil); err == nil {
		t.Error("an empty write-command census derived a policy surface")
	}
}

// TestPolicyExtractorFindsEveryFamily — on the real tree, one anchor per leg
// the contract derives, including the five sites a hook in sendCommand alone
// would miss (the four direct WriteJSON sends and auth), and none of the
// sites the contract leaves out or D-81 deletes.
func TestPolicyExtractorFindsEveryFamily(t *testing.T) {
	census := []surfaceaudit.Site{{Key: "hactl area create", File: "cobra tree", Note: "writes"}}
	s, err := surfaceaudit.PolicySurface(repoRoot(t), census)
	if err != nil {
		t.Fatalf("deriving the policy surface: %v", err)
	}
	keys := policyKeys(s)
	for _, want := range []string{
		"ws config/entity_registry/update",
		"ws trace/list",
		"ws trace/get",
		"ws validate_config",
		"ws system_log/list",
		"ws auth",
		"ws supervisor/api GET /addons",
		"ws supervisor/api GET /addons/{slug}/info",
		"ws supervisor/api POST /ingress/session",
		"rest POST /api/services/{domain}/{service}",
		"rest GET /api/history/period/{start}",
		"rest DELETE /api/config/config_entries/entry/{entry_id}",
		"rest GET /api/config/config_entries/options/flow/{flow_id}",
		"companion POST /v1/ref/replace",
		"companion GET /v1/health",
		"cmd hactl area create",
	} {
		if !slices.Contains(keys, want) {
			t.Errorf("the policy extractor does not find %q", want)
		}
	}
	for _, k := range keys {
		switch {
		case k == "ws supervisor/api", k == "ws auth/long_lived_access_token", k == "ws auth_ok", k == "ws auth_required":
			t.Errorf("site %q is no product request", k)
		case strings.Contains(k, "hassio_ingress"):
			t.Errorf("site %q is keyed by a wire URL, not the logical route", k)
		case strings.Contains(k, "?"):
			t.Errorf("site %q keeps its query string; the query is not part of the key", k)
		case !inPolicyGrammar(k):
			t.Errorf("site %q is in none of the manifest's key grammars", k)
		}
	}
	for _, dead := range deadRoutes {
		if slices.Contains(keys, dead) {
			t.Errorf("site %q is still sent, but D-81 deletes the method that sends it", dead)
		}
	}
}

// inPolicyGrammar reports whether a key starts with one of the manifest's four
// site families.
func inPolicyGrammar(k string) bool {
	for _, p := range []string{"ws ", "rest ", "companion ", "cmd "} {
		if strings.HasPrefix(k, p) {
			return true
		}
	}
	return false
}

// TestDeadClientMethodsAreDeleted — D-81: the eleven client methods with no
// product caller are deleted, and companion.Endpoints loses their entries.
func TestDeadClientMethodsAreDeleted(t *testing.T) {
	for _, tc := range []struct {
		typ     reflect.Type
		methods []string
	}{
		{reflect.TypeFor[*haapi.Client](), []string{"GetAutomationConfig", "GetEvents", "StartConfigFlow", "ServiceExists"}},
		{reflect.TypeFor[*companion.Client](), []string{"WriteConfigFile", "ListTemplates", "WriteTemplate",
			"ListScriptDefs", "ListAutomationDefs", "UpdateHelper", "ReloadDomain"}},
	} {
		for _, m := range tc.methods {
			if _, ok := tc.typ.MethodByName(m); ok {
				t.Errorf("%s.%s still exists; D-81 deletes it", tc.typ, m)
			}
		}
	}
	for _, e := range companion.Endpoints {
		key := "companion " + e.Method + " " + e.Path
		if slices.Contains(deadRoutes, key) {
			t.Errorf("companion.Endpoints still lists %s; D-81 deletes it with its method", key)
		}
	}
}
