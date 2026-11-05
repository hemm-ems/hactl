package reqmap_test

import (
	"net/url"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// TestQueryParameters — a row addressed by ?id= reads the id from the path's
// query and from Request.Query alike, and a repeated id is refused rather than
// resolved. Which of two values the companion route reads is unknown and not
// probed: the refusal is right for either answer, so no answer would change
// the code or this test.
func TestQueryParameters(t *testing.T) {
	const row = "companion DELETE /v1/config/template"
	withQuery := func(path string, q url.Values) reqmap.Request {
		r := comp("DELETE", path, "")
		r.Query = q
		return r
	}
	for _, v := range []vector{
		{
			name: "an id carried only in Request.Query maps",
			req:  withQuery("/v1/config/template", url.Values{"id": {"t1"}}),
			disp: reqmap.Write, row: row,
			checks: []string{"delete template:t1"},
		},
		{
			name: "an id given twice in the path is refused",
			req:  comp("DELETE", "/v1/config/template?id=a&id=b", ""),
			disp: reqmap.Unmappable, row: row,
			refused: "fail-closed: unmappable request " + row,
		},
		{
			name: "an id in both the path and Request.Query is refused",
			req:  withQuery("/v1/config/template?id=a", url.Values{"id": {"b"}}),
			disp: reqmap.Unmappable, row: row,
			refused: "fail-closed: unmappable request " + row,
		},
	} {
		checkVector(t, v)
	}
}

// TestNonStringParametersAreRefused — a websocket parameter the mapper reads
// as a string is refused when it is anything else, never read as absent: an
// absent url_path is the default dashboard, an absent supervisor method is
// GET. Whether Home Assistant would coerce a number is beside the point; the
// mapper cannot tell what it addresses.
func TestNonStringParametersAreRefused(t *testing.T) {
	stored := &reqmap.Facts{Dashboards: map[string]reqmap.StoredDashboard{"": {Config: []byte(`{}`)}}}
	for _, v := range []vector{
		{
			name:  "a numeric url_path on a dashboard save",
			req:   ws("lovelace/config/save", map[string]any{"url_path": 123, "config": map[string]any{}}),
			facts: stored,
			disp:  reqmap.Unmappable, row: "ws lovelace/config/save",
			refused: "fail-closed: unmappable request ws lovelace/config/save",
		},
		{
			name: "a numeric supervisor method",
			req:  ws("supervisor/api", map[string]any{"endpoint": "/addons", "method": 7}),
			disp: reqmap.Unmappable, row: "supervisor/api",
			refused: "fail-closed: unmappable request ws supervisor/api",
		},
		{
			name: "a numeric entity_id",
			req:  ws("config/entity_registry/update", map[string]any{"entity_id": 7, "name": "x"}),
			disp: reqmap.Unmappable, row: "ws config/entity_registry/update",
			refused: "fail-closed: unmappable request ws config/entity_registry/update",
		},
		{
			name: "a numeric new_entity_id",
			req:  ws("config/entity_registry/update", map[string]any{"entity_id": "light.kitchen", "new_entity_id": 7}),
			disp: reqmap.Unmappable, row: "ws config/entity_registry/update",
			refused: "fail-closed: unmappable request ws config/entity_registry/update",
		},
		{
			name: "an empty url_path on a dashboard create is no default dashboard",
			req:  ws("lovelace/dashboards/create", map[string]any{"url_path": "", "title": "x"}),
			disp: reqmap.Unmappable, row: "ws lovelace/dashboards/create",
			refused: "fail-closed: unmappable request ws lovelace/dashboards/create",
		},
	} {
		checkVector(t, v)
	}
	// A read is not refused in phase 2, but a numeric url_path still does not
	// map: it is never read as the default dashboard.
	got := reqmap.Classify(ws("lovelace/config", map[string]any{"url_path": 123}), nil)
	if got.Disposition != reqmap.Unmappable {
		t.Errorf("lovelace/config with a numeric url_path: %s %q, want unmappable", got.Disposition, got.Strings())
	}
}

// TestTemplateBlockWithAScalarItem — an item of a template block that is not a
// mapping has no unique_id to name it: the create is refused, never mapped by
// the items that do have one (D-78).
func TestTemplateBlockWithAScalarItem(t *testing.T) {
	checkVector(t, vector{
		name: "a scalar item in a sensor block",
		req:  comp("POST", "/v1/config/template", "sensor:\n  - unique_id: a\n    state: '1'\n  - 7\n"),
		disp: reqmap.Unmappable, row: "companion POST /v1/config/template",
		refused: "fail-closed: template entity without unique_id",
	})
}
