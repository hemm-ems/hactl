package glob

import "testing"

func TestMatch(t *testing.T) {
	cases := []struct {
		s, pattern string
		want       bool
	}{
		{"light.kitchen", "light.kitchen", true},
		{"Light.Kitchen", "light.KITCHEN", true}, // D-2: case-insensitive
		{"light.kitchen_2", "light.kitchen", false},
		{"light.kitchen", "light.*", true},
		{"binary_sensor.door", "sensor.*", false},
		{"sensor.a1", "sensor.?1", true},
		{"sensor.a12", "sensor.?1", false},
		{"", "*", true},
		{"", "", true},
		{"x", "", false},
		{"abc", "a**c", true},
		{"packages/x", "*", true}, // Match itself knows nothing of segments
	}
	for _, c := range cases {
		if got := Match(c.s, c.pattern); got != c.want {
			t.Errorf("Match(%q, %q) = %v, want %v", c.s, c.pattern, got, c.want)
		}
	}
}

func TestMatchQualified(t *testing.T) {
	cases := []struct {
		s, pattern string
		want       bool
	}{
		{"light.kitchen_lamp", "kitchen*", true}, // D-28 second anchor
		{"light.kitchen_lamp", "light.kitchen*", true},
		{"binary_sensor.x", "sensor.*", false}, // the dot keeps its selecting power
		{"input_boolean.anwesenheit_flur", "anwesen*", true},
		{"light.kitchen_2", "light.kitchen", false},
		{"nodot", "nodot", true},
		{"nodot", "no*", true},
	}
	for _, c := range cases {
		if got := MatchQualified(c.s, c.pattern); got != c.want {
			t.Errorf("MatchQualified(%q, %q) = %v, want %v", c.s, c.pattern, got, c.want)
		}
	}
}

func TestHasWildcard(t *testing.T) {
	for p, want := range map[string]bool{"a*": true, "a?": true, "abc": false, "": false} {
		if got := HasWildcard(p); got != want {
			t.Errorf("HasWildcard(%q) = %v, want %v", p, got, want)
		}
	}
}
