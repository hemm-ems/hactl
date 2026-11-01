package surfaceaudit_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
)

func sourceRequestKeys(t *testing.T, files map[string]string) []string {
	t.Helper()
	sites, err := surfaceaudit.PolicyRequestsFromSource(files)
	if err != nil {
		t.Fatalf("extracting: %v", err)
	}
	keys := make([]string, 0, len(sites))
	for _, s := range sites {
		keys = append(keys, s.Key)
	}
	return keys
}

// TestPolicyRequestExtractorSeesEverySendShape — the request half of H-29's
// surface derives a site from each way the product sends a request, and keys a
// concatenated path by the classifier row of the same shape. A send it misses
// is a request no policy line names; this fixture is the extractor watched to
// see each shape, including the five direct WriteJSON sends a hook in
// sendCommand alone would miss.
func TestPolicyRequestExtractorSeesEverySendShape(t *testing.T) {
	got := sourceRequestKeys(t, map[string]string{
		"internal/haapi/ws.go": `package haapi
func (ws *WSClient) Remove(ctx context.Context, id string) error {
	_, err := ws.sendCommand(ctx, "config/entity_registry/remove", map[string]any{"entity_id": id})
	return err
}
func (ws *WSClient) Brand(ctx context.Context) error {
	_, err := ws.sendCommand(ctx, "config/brand_new_type", nil)
	return err
}
func (ws *WSClient) SupervisorAPI(ctx context.Context, endpoint, method string, data map[string]any) (any, error) {
	return ws.sendCommand(ctx, "supervisor/api", map[string]any{"endpoint": endpoint})
}
func (ws *WSClient) sendCommand(ctx context.Context, cmdType string, params map[string]any) (any, error) {
	msg := map[string]any{"type": cmdType}
	return nil, ws.conn.WriteJSON(msg)
}
func (ws *WSClient) TraceList(ctx context.Context) error {
	msg := map[string]any{"id": 1, "type": "trace/list"}
	return ws.conn.WriteJSON(msg)
}
func (c *Client) Step(ctx context.Context, flowID string, options bool) error {
	path := "/api/config/config_entries/flow/" + flowID
	if options {
		path = "/api/config/config_entries/options/flow/" + flowID
	}
	_, err := c.doPost(ctx, path, nil)
	return err
}
func (c *Client) Call(ctx context.Context, d, s string) error {
	_, err := c.doPost(ctx, "/api/services/"+d+"/"+s+"?return_response=true", nil)
	return err
}
var pkgSend = func(ctx context.Context, ws *WSClient) error {
	_, err := ws.sendCommand(ctx, "probe/pkgvar", nil)
	return err
}
func (c *Client) Things(ctx context.Context, id string) error {
	p := "/api/things"
	p += "/" + id
	_, err := c.doDelete(ctx, p)
	return err
}
func (c *Client) Fire(ctx context.Context) error {
	_, err := c.doPost(ctx, "/api/events/hactl_fired?origin=cli", nil)
	return err
}
func (c *Client) Hist(ctx context.Context, start string) error {
	path := "/api/logbook/" + url.PathEscape(start)
	path += "?" + q
	_, err := c.doGet(ctx, path)
	return err
}
`,
		"internal/companion/disc.go": `package companion
func Discover(ctx context.Context, ws *haapi.WSClient, slug string) {
	ws.SupervisorAPI(ctx, "/addons/"+slug+"/info", "get", nil)
	ws.SupervisorAPI(ctx, "/addons/"+slug+"/restart", "post", nil)
}
func (c *Client) Del(ctx context.Context, id string) {
	c.doDelete(ctx, "/v1/config/script", nil)
}
`,
	})
	want := []string{
		"companion DELETE /v1/config/script",
		"rest DELETE /api/things",
		"rest DELETE /api/things/{id}",
		"rest GET /api/logbook/{start}",
		"rest POST /api/config/config_entries/flow/{flow_id}",
		"rest POST /api/config/config_entries/options/flow/{flow_id}",
		"rest POST /api/events/hactl_fired",
		"rest POST /api/services/{domain}/{service}",
		"ws config/brand_new_type",
		"ws config/entity_registry/remove",
		"ws probe/pkgvar",
		"ws supervisor/api GET /addons/{slug}/info",
		"ws supervisor/api POST /addons/{slug}/restart",
		"ws trace/list",
	}
	if !slices.Equal(got, want) {
		t.Errorf("extracted\n  %q\nwant\n  %q", got, want)
	}
}

// TestPolicyRequestExtractorFollowsABuildersHelper — a request builder may
// delegate to a helper (`doGet` → `send` → http.NewRequest): a helper only the
// builders call builds no request of its own, so the builders' callers stay the
// sites and the extractor does not refuse.
func TestPolicyRequestExtractorFollowsABuildersHelper(t *testing.T) {
	sites, err := surfaceaudit.PolicyRequestsFromSource(map[string]string{"internal/companion/x.go": `package companion
func (c *Client) Health(ctx context.Context) ([]byte, error) { return c.doGet(ctx, "/v1/health", nil) }
func (c *Client) doGet(ctx context.Context, path string, q url.Values) ([]byte, error) { return c.send(ctx, "GET", path) }
func (c *Client) send(ctx context.Context, method, path string) ([]byte, error) {
	_, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	return nil, err
}
`})
	if err != nil {
		t.Fatalf("a builder's own helper was refused: %v", err)
	}
	if len(sites) != 1 || sites[0].Key != "companion GET /v1/health" {
		t.Errorf("sites %v, want exactly companion GET /v1/health", sites)
	}
}

// TestPolicyRequestExtractorRefusesWhatItCannotName — a send whose request no
// site can name is an error, never a silent omission: a dynamic websocket type
// outside the generic sender, a direct WriteJSON of no literal type, an HTTP
// request built outside the request builders, and one built in a builder's
// helper that a non-builder calls too.
func TestPolicyRequestExtractorRefusesWhatItCannotName(t *testing.T) {
	for name, src := range map[string]map[string]string{
		"a dynamic websocket type": {"internal/haapi/x.go": `package haapi
func (ws *WSClient) Any(ctx context.Context, typ string) { ws.sendCommand(ctx, typ, nil) }
`},
		"a direct WriteJSON of no literal type": {"internal/haapi/x.go": `package haapi
func (ws *WSClient) Raw(t string) error { return ws.conn.WriteJSON(map[string]any{"type": t}) }
`},
		"an HTTP request built outside the builders": {"internal/cmd/x.go": `package cmd
func sneak(ctx context.Context) { http.NewRequestWithContext(ctx, "POST", "http://ha/api/services/lock/unlock", nil) }
`},
		"a builder's helper that a non-builder also calls": {"internal/companion/x.go": `package companion
func (c *Client) doGet(ctx context.Context, path string, q url.Values) ([]byte, error) { return c.send(ctx, "GET", path) }
func (c *Client) Bypass(ctx context.Context) ([]byte, error) { return c.send(ctx, "POST", "/v1/anything") }
func (c *Client) send(ctx context.Context, method, path string) ([]byte, error) {
	_, err := http.NewRequestWithContext(ctx, method, c.base+path, nil)
	return nil, err
}
`},
		"a WriteJSON in a function building two message types": {"internal/haapi/x.go": `package haapi
func (ws *WSClient) Either(sub bool) error {
	msg := map[string]any{"id": 1, "type": "trace/list"}
	if sub {
		msg = map[string]any{"id": 1, "type": "subscribe_events"}
	}
	return ws.conn.WriteJSON(msg)
}
`},
		"a SupervisorAPI call whose method is not a literal": {"internal/companion/x.go": `package companion
func Probe(ctx context.Context, ws *haapi.WSClient, m string) { ws.SupervisorAPI(ctx, "/addons", m, nil) }
`},
		"http.Post outside the builders": {"internal/cmd/x.go": `package cmd
func sneak() { http.Post("http://ha/api/services/lock/unlock", "application/json", nil) }
`},
		"http.Get outside the builders": {"internal/cmd/x.go": `package cmd
func sneak() { http.Get("http://ha/api/states") }
`},
		"net/http under another name": {"internal/haapi/x.go": `package haapi
import nethttp "net/http"
func (c *Client) Stop(ctx context.Context) error {
	req, _ := nethttp.NewRequestWithContext(ctx, "POST", c.baseURL+"/api/services/homeassistant/stop", nil)
	_, err := (&nethttp.Client{}).Do(req)
	return err
}
`},
		"a dot import of net/http": {"internal/haapi/x.go": `package haapi
import . "net/http"
func (c *Client) Stop(ctx context.Context) error {
	_, err := NewRequestWithContext(ctx, "POST", c.baseURL+"/api/services/homeassistant/stop", nil)
	return err
}
`},
		"an http.Request built as a composite literal": {"internal/haapi/x.go": `package haapi
func (c *Client) Stop(ctx context.Context, u *url.URL) error {
	req := (&http.Request{Method: http.MethodPost, URL: u, Header: http.Header{}}).WithContext(ctx)
	_, err := c.doOnce(req)
	return err
}
`},
		"an http.Request from new": {"internal/haapi/x.go": `package haapi
func (c *Client) Stop(ctx context.Context) error {
	req := new(http.Request)
	_, err := c.doOnce(req)
	return err
}
`},
		"a send in a package-level func literal": {"internal/haapi/x.go": `package haapi
var probeSend = func(ctx context.Context, ws *WSClient) (json.RawMessage, error) {
	return ws.sendCommand(ctx, typeOf(ctx), nil)
}
`},
		"a method value of sendCommand": {"internal/haapi/x.go": `package haapi
func (ws *WSClient) Probe(ctx context.Context) error {
	send := ws.sendCommand
	_, err := send(ctx, "probe/methval", nil)
	return err
}
`},
		"a method expression of SupervisorAPI": {"internal/haapi/x.go": `package haapi
func Probe(ctx context.Context, ws *WSClient) {
	f := (*WSClient).SupervisorAPI
	f(ws, ctx, "/addons/x/restart", "post", nil)
}
`},
		"a method value of a request builder": {"internal/haapi/x.go": `package haapi
func (c *Client) Probe(ctx context.Context) {
	post := c.doPost
	post(ctx, "/api/events/x", nil)
}
`},
		"a websocket send other than WriteJSON": {"internal/haapi/x.go": `package haapi
func (ws *WSClient) Raw() error {
	return ws.conn.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf(` + "`" + `{"id":%d,"type":"probe/raw"}` + "`" + `, ws.nextID.Add(1))))
}
`},
		"a websocket NextWriter": {"internal/haapi/x.go": `package haapi
func (ws *WSClient) Raw() error {
	w, err := ws.conn.NextWriter(1)
	if err != nil {
		return err
	}
	_, err = w.Write([]byte("{}"))
	return err
}
`},
		"the websocket's underlying connection": {"internal/haapi/x.go": `package haapi
func (ws *WSClient) Raw() error {
	_, err := ws.conn.UnderlyingConn().Write([]byte("{}"))
	return err
}
`},
		"a WriteJSON message whose type is rewritten after the literal": {"internal/haapi/x.go": `package haapi
func (ws *WSClient) Mutated() error {
	msg := map[string]any{"id": 1, "type": "trace/list"}
	msg["type"] = "probe/mutated_write"
	return ws.conn.WriteJSON(msg)
}
`},
		"a WriteJSON message with a map copied in after the literal": {"internal/haapi/x.go": `package haapi
func (ws *WSClient) Merged(extra map[string]any) error {
	msg := map[string]any{"id": 1, "type": "trace/list"}
	maps.Copy(msg, extra)
	return ws.conn.WriteJSON(msg)
}
`},
		"a WriteJSON message given a key the source does not spell": {"internal/haapi/x.go": `package haapi
func (ws *WSClient) Keyed(k string, v any) error {
	msg := map[string]any{"id": 1, "type": "trace/list"}
	msg[k] = v
	return ws.conn.WriteJSON(msg)
}
`},
	} {
		_, err := surfaceaudit.PolicyRequestsFromSource(src)
		if err == nil {
			t.Errorf("%s: extracted without complaint; it must refuse to stay silent", name)
			continue
		}
		if !strings.Contains(err.Error(), "requests no site can name") {
			t.Errorf("%s: error %q does not say what it is", name, err)
		}
	}
}
