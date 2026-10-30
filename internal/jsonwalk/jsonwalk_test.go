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

func TestPathTerminalKey(t *testing.T) {
	cases := []struct {
		path Path
		want string
	}{
		{Path{}, ""},
		{Path{"entity"}, "entity"},
		{Path{"views", 0, "cards", 2, "entity"}, "entity"},
		{Path{"entities", 0}, "entities"},          // bare list item → enclosing key
		{Path{"trigger", 0, "entity_id", 1}, "entity_id"}, // list-valued entity_id
		{Path{0, "a"}, "a"},
		{Path{0}, ""}, // pure index path has no enclosing key
	}
	for _, c := range cases {
		if got := c.path.TerminalKey(); got != c.want {
			t.Errorf("Path%v.TerminalKey() = %q, want %q", []any(c.path), got, c.want)
		}
	}
}

func TestFindMatchingEqualityFindsEveryWholeValue(t *testing.T) {
	root := decode(t, `{
		"views": [
			{"cards": [{"entity": "light.kitchen"}, {"entity": "light.hall"}]},
			{"badges": ["light.kitchen"], "title": "light.kitchen is not a leaf key"}
		],
		"name": "light.kitchen"
	}`)

	var found []string
	FindMatching(root, equals("light.kitchen"), func(p Path, _ string) {
		found = append(found, p.String())
	})
	sort.Strings(found)

	want := []string{
		"name",
		"views[0].cards[0].entity",
		"views[1].badges[0]",
	}
	if !reflect.DeepEqual(found, want) {
		t.Errorf("FindMatching(equals) paths = %v, want %v", found, want)
	}
}

func TestFindMatchingEquality_NoMatch(t *testing.T) {
	root := decode(t, `{"a": "x", "b": ["y", "z"]}`)
	var n int
	FindMatching(root, equals("missing"), func(Path, string) { n++ })
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
	gotPaths := make([]string, 0, len(changed))
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

// TestFindMatchingOffersValuesNeverKeys pins the predicate walk `grep` threads
// through: every string leaf is offered once, in walk order, with its value;
// map keys and non-string leaves never are.
func TestFindMatchingOffersValuesNeverKeys(t *testing.T) {
	root := decode(t, `{"views":[{"title":"Küche","cards":[{"entity":"sensor.balkony_p_ac","show_state":true,"n":3}]}],
		"balkony_key":"x"}`)
	var offered []string
	var hits []string
	FindMatching(root, func(v string) bool {
		offered = append(offered, v)
		return v != "x"
	}, func(p Path, v string) {
		hits = append(hits, p.String()+"="+v)
	})
	if want := []string{"x", "sensor.balkony_p_ac", "Küche"}; !reflect.DeepEqual(offered, want) {
		t.Errorf("offered %v, want every string leaf in walk order and no key: %v", offered, want)
	}
	want := []string{"views[0].cards[0].entity=sensor.balkony_p_ac", "views[0].title=Küche"}
	if !reflect.DeepEqual(hits, want) {
		t.Errorf("hits %v, want %v", hits, want)
	}
}

// equals is the predicate the exact-value commands pass (scanDashboards).
func equals(target string) func(string) bool {
	return func(s string) bool { return s == target }
}

// TestFindMatchingWithEqualityIsWholeValue: the exact search is the predicate
// walk with string equality, so a fragment, a case variant and a key are not
// hits (D-48, AC-7).
func TestFindMatchingWithEqualityIsWholeValue(t *testing.T) {
	root := decode(t, `{"a":"sensor.x","b":["sensor.x","sensor.x_2","SENSOR.X"],"sensor.x":"key"}`)
	var got []string
	FindMatching(root, equals("sensor.x"), func(p Path, _ string) { got = append(got, p.String()) })
	if want := []string{"a", "b[0]"}; !reflect.DeepEqual(got, want) {
		t.Errorf("FindMatching(equals) = %v, want %v", got, want)
	}
}
