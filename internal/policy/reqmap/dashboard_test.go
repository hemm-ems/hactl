package reqmap_test

import (
	"slices"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// TestDiffDashboard — D-66 as refined: a save is checked at the finest address
// the diff against the stored config allows, and as the whole dashboard
// wherever it does not.
func TestDiffDashboard(t *testing.T) {
	const stored = `{"title":"Home","views":[{"path":"home","cards":[1]},{"title":"pathless","cards":[]},{"path":"secret","cards":[2]}]}`
	for _, c := range []struct {
		name, url, stored, proposed string
		want                        []string
		wantErr                     bool
	}{
		{"identical configs need no check", "d-a", stored, stored, nil, false},
		{"one view's content changed", "d-a", stored,
			`{"title":"Home","views":[{"path":"home","cards":[1,2]},{"title":"pathless","cards":[]},{"path":"secret","cards":[2]}]}`,
			[]string{"edit dashboard:d-a/views/home"}, false},
		{"a view added and one removed", "d-a", stored,
			`{"title":"Home","views":[{"path":"home","cards":[1]},{"title":"pathless","cards":[]},{"path":"new"}]}`,
			[]string{"create dashboard:d-a/views/new", "delete dashboard:d-a/views/secret"}, false},
		{"a non-view key changed is the whole dashboard", "d-a", stored,
			`{"title":"Away","views":[{"path":"home","cards":[1]},{"title":"pathless","cards":[]},{"path":"secret","cards":[2]}]}`,
			[]string{"edit dashboard:d-a"}, false},
		{"a path-less view changed is the whole dashboard", "d-a", stored,
			`{"title":"Home","views":[{"path":"home","cards":[1]},{"title":"pathless","cards":[9]},{"path":"secret","cards":[2]}]}`,
			[]string{"edit dashboard:d-a"}, false},
		{"reordering views is the whole dashboard", "d-a", stored,
			`{"title":"Home","views":[{"path":"secret","cards":[2]},{"title":"pathless","cards":[]},{"path":"home","cards":[1]}]}`,
			[]string{"edit dashboard:d-a"}, false},
		{"a view renamed is a delete and a create", "d-a",
			`{"views":[{"path":"a","cards":[]}]}`, `{"views":[{"path":"b","cards":[]}]}`,
			[]string{"create dashboard:d-a/views/b", "delete dashboard:d-a/views/a"}, false},
		{"two views sharing a path is the whole dashboard", "d-a",
			`{"views":[{"path":"a"}]}`, `{"views":[{"path":"a"},{"path":"a","x":1}]}`,
			[]string{"edit dashboard:d-a"}, false},
		{"views that are not a list is the whole dashboard", "d-a",
			`{"views":[{"path":"a"}]}`, `{"views":{"path":"a"}}`,
			[]string{"edit dashboard:d-a"}, false},
		{"no stored config is the whole dashboard", "lovelace", "", `{"views":[{"path":"a"}]}`,
			[]string{"edit dashboard:"}, false},
		{"a null stored config is the whole dashboard", "", "null", `{"views":[]}`,
			[]string{"edit dashboard:"}, false},
		// D-126: Home Assistant's "nothing is stored" is an empty config, so a
		// first save creates its views; anything outside them is the whole.
		{"nothing stored: each view is a create", "d-a", string(reqmap.EmptyDashboard),
			`{"views":[{"path":"one","cards":[]},{"path":"two","cards":[]}]}`,
			[]string{"create dashboard:d-a/views/one", "create dashboard:d-a/views/two"}, false},
		{"nothing stored: a title is the whole dashboard", "d-a", string(reqmap.EmptyDashboard),
			`{"title":"New","views":[{"path":"one"}]}`,
			[]string{"edit dashboard:d-a", "create dashboard:d-a/views/one"}, false},
		{"nothing stored: a path-less view is the whole dashboard", "d-a", string(reqmap.EmptyDashboard),
			`{"views":[{"title":"no path"}]}`,
			[]string{"edit dashboard:d-a"}, false},
		// D-126 (corrected): turning "nothing stored" into a stored config is a
		// change of the dashboard even when the proposal brings no view.
		{"nothing stored: an empty object is the whole dashboard", "d-a", string(reqmap.EmptyDashboard),
			`{}`, []string{"edit dashboard:d-a"}, false},
		{"nothing stored: an empty views list is the whole dashboard", "d-a", string(reqmap.EmptyDashboard),
			`{"views":[]}`, []string{"edit dashboard:d-a"}, false},
		{"nothing stored: null views is the whole dashboard", "lovelace", string(reqmap.EmptyDashboard),
			`{"views":null}`, []string{"edit dashboard:"}, false},
		{"a view change on the default dashboard", "lovelace",
			`{"views":[{"path":"a","x":1}]}`, `{"views":[{"path":"a","x":2}]}`,
			[]string{"edit dashboard:/views/a"}, false},
		{"a view path the grammar refuses is unmappable", "d-a",
			`{"views":[]}`, `{"views":[{"path":"a\u200bb"}]}`, nil, true},
		{"a proposal that is not an object is unmappable", "d-a", stored, `[1]`, nil, true},
	} {
		var st []byte
		if c.stored != "" {
			st = []byte(c.stored)
		}
		got, err := reqmap.DiffDashboard(c.url, st, []byte(c.proposed))
		if (err != nil) != c.wantErr {
			t.Errorf("%s: err %v, want error %v", c.name, err, c.wantErr)
			continue
		}
		s := make([]string, 0, len(got))
		for _, ch := range got {
			s = append(s, ch.String())
		}
		if !slices.Equal(s, c.want) {
			t.Errorf("%s:\n  got  %q\n  want %q", c.name, s, c.want)
		}
	}
}

// TestDashboardSaveMapping — the websocket save reaches the diff only through
// the stored config this invocation read for the same url_path (D-72).
func TestDashboardSaveMapping(t *testing.T) {
	facts := &reqmap.Facts{Dashboards: map[string]reqmap.StoredDashboard{
		"d-a": {Config: []byte(`{"views":[{"path":"a","x":1}]}`)},
		"":    {Config: nil},
	}}
	for _, v := range []vector{
		{
			name: "the stored config of the same url_path is diffed", facts: facts,
			req:  ws("lovelace/config/save", map[string]any{"url_path": "d-a", "config": map[string]any{"views": []any{map[string]any{"path": "a", "x": 2}}}}),
			disp: reqmap.Write, row: "ws lovelace/config/save", checks: []string{"edit dashboard:d-a/views/a"},
		},
		{
			name: "another dashboard's stored config is never used", facts: facts,
			req:  ws("lovelace/config/save", map[string]any{"url_path": "d-b", "config": map[string]any{"views": []any{map[string]any{"path": "a", "x": 1}}}}),
			disp: reqmap.Write, row: "ws lovelace/config/save", checks: []string{"edit dashboard:d-b"},
		},
		{
			// D-126: read, and nothing is stored — diffed against an empty
			// config, so the first save's views are creates.
			name: "a dashboard HA holds no config for is diffed against an empty config", facts: facts,
			req:  ws("lovelace/config/save", map[string]any{"config": map[string]any{"views": []any{map[string]any{"path": "home"}}}}),
			disp: reqmap.Write, row: "ws lovelace/config/save", checks: []string{"create dashboard:/views/home"},
		},
		{
			name: "a dashboard HA holds no config for, saved with a title", facts: facts,
			req:  ws("lovelace/config/save", map[string]any{"config": map[string]any{"title": "Home", "views": []any{}}}),
			disp: reqmap.Write, row: "ws lovelace/config/save", checks: []string{"edit dashboard:"},
		},
		{
			// D-126 (corrected): an empty first save still replaces what users see.
			name: "a dashboard HA holds no config for, saved with no view", facts: facts,
			req:  ws("lovelace/config/save", map[string]any{"config": map[string]any{"views": []any{}}}),
			disp: reqmap.Write, row: "ws lovelace/config/save", checks: []string{"edit dashboard:"},
		},
		{
			name: "a save without a config is refused",
			req:  ws("lovelace/config/save", map[string]any{"url_path": "d-a"}),
			disp: reqmap.Unmappable, row: "ws lovelace/config/save", refused: "fail-closed: unmappable request",
		},
	} {
		checkVector(t, v)
	}
}
