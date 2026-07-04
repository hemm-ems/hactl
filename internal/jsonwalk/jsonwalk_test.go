package jsonwalk

import (
	"encoding/json"
	"reflect"
	"sort"
	"testing"
)

func decode(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatalf("decode %q: %v", s, err)
	}
	return v
}

func TestPathString(t *testing.T) {
	cases := []struct {
		path Path
		want string
	}{
		{Path{}, ""},
		{Path{"entity"}, "entity"},
		{Path{"views", 0, "cards", 2, "entity"}, "views[0].cards[2].entity"},
		{Path{0, "a"}, "[0].a"},
		{Path{"a", "b", "c"}, "a.b.c"},
	}
	for _, c := range cases {
		if got := c.path.String(); got != c.want {
			t.Errorf("Path%v.String() = %q, want %q", []any(c.path), got, c.want)
		}
	}
}

func TestFindString(t *testing.T) {
	root := decode(t, `{
		"views": [
			{"cards": [{"entity": "light.kitchen"}, {"entity": "light.hall"}]},
			{"badges": ["light.kitchen"], "title": "light.kitchen is not a leaf key"}
		],
		"name": "light.kitchen"
	}`)

	var found []string
	FindString(root, "light.kitchen", func(p Path) {
		found = append(found, p.String())
	})
	sort.Strings(found)

	want := []string{
		"name",
		"views[0].cards[0].entity",
		"views[1].badges[0]",
	}
	if !reflect.DeepEqual(found, want) {
		t.Errorf("FindString paths = %v, want %v", found, want)
	}
}

func TestFindString_NoMatch(t *testing.T) {
	root := decode(t, `{"a": "x", "b": ["y", "z"]}`)
	var n int
	FindString(root, "missing", func(Path) { n++ })
	if n != 0 {
		t.Errorf("expected 0 matches, got %d", n)
	}
}

func TestReplace(t *testing.T) {
	root := decode(t, `{
		"views": [
			{"cards": [{"entity": "sensor.old"}, {"entity": "sensor.keep"}]}
		],
		"nested": {"list": ["sensor.old", "sensor.old"]}
	}`)

	result, changed := Replace(root, "sensor.old", "sensor.new")

	// input untouched
	if !reflect.DeepEqual(root, decode(t, `{
		"views": [{"cards": [{"entity": "sensor.old"}, {"entity": "sensor.keep"}]}],
		"nested": {"list": ["sensor.old", "sensor.old"]}
	}`)) {
		t.Fatalf("Replace mutated its input: %#v", root)
	}

	// result rewritten
	want := decode(t, `{
		"views": [{"cards": [{"entity": "sensor.new"}, {"entity": "sensor.keep"}]}],
		"nested": {"list": ["sensor.new", "sensor.new"]}
	}`)
	if !reflect.DeepEqual(result, want) {
		t.Errorf("Replace result = %#v, want %#v", result, want)
	}

	// changed paths reported (deterministic order)
	var gotPaths []string
	for _, p := range changed {
		gotPaths = append(gotPaths, p.String())
	}
	sort.Strings(gotPaths)
	wantPaths := []string{
		"nested.list[0]",
		"nested.list[1]",
		"views[0].cards[0].entity",
	}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Errorf("changed paths = %v, want %v", gotPaths, wantPaths)
	}
}

func TestReplace_NoChange(t *testing.T) {
	root := decode(t, `{"a": "x", "b": [1, 2, true, null]}`)
	result, changed := Replace(root, "missing", "new")
	if len(changed) != 0 {
		t.Errorf("expected no changed paths, got %v", changed)
	}
	if !reflect.DeepEqual(result, root) {
		t.Errorf("result should equal input when nothing matched")
	}
}

func TestWalk_VisitsEveryNode(t *testing.T) {
	root := decode(t, `{"a": 1, "b": ["x", {"c": true}]}`)
	var paths []string
	Walk(root, func(p Path, _ any) {
		paths = append(paths, p.String())
	})
	sort.Strings(paths)
	// root ("") + a + b + b[0] + b[1] + b[1].c
	want := []string{"", "a", "b", "b[0]", "b[1]", "b[1].c"}
	if !reflect.DeepEqual(paths, want) {
		t.Errorf("Walk paths = %v, want %v", paths, want)
	}
}
