package surfaceaudit_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
)

func policyKeys(t *testing.T, files map[string]string) []string {
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
	got := policyKeys(t, map[string]string{
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
		"rest GET /api/logbook/{start}",
		"rest POST /api/config/config_entries/flow/{flow_id}",
		"rest POST /api/config/config_entries/options/flow/{flow_id}",
		"rest POST /api/services/{domain}/{service}",
		"ws config/brand_new_type",
		"ws config/entity_registry/remove",
		"ws supervisor/api GET /addons/{slug}/info",
		"ws supervisor/api POST /addons/{slug}/restart",
		"ws trace/list",
	}
	if !slices.Equal(got, want) {
		t.Errorf("extracted\n  %q\nwant\n  %q", got, want)
	}
}

// TestPolicyRequestExtractorRefusesWhatItCannotName — a send whose request no
// site can name is an error, never a silent omission: a dynamic websocket type
// outside the generic sender, a direct WriteJSON of no literal type, and an
// HTTP request built outside the request builders.
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
