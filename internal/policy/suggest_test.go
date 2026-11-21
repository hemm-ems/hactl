package policy

import (
	"strings"
	"testing"
)

func TestEditDistance(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"write", "write", 0},
		{"wirte", "write", 2}, // a transposition is two edits
		{"allows", "allow", 1},
		{"registy", "registry", 1}, //nolint:misspell // the typo is the input
		{"lables", "labels", 2},
		{"entiy", "entity", 1},
		{"", "read", 4},
		{"wöhnzimmer", "wohnzimmer", 1}, // by rune, not by byte
	} {
		if got := editDistance(tc.a, tc.b); got != tc.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
		if got := editDistance(tc.b, tc.a); got != tc.want {
			t.Errorf("editDistance(%q, %q) = %d, want %d (symmetric)", tc.b, tc.a, got, tc.want)
		}
	}
}

// TestDidYouMean (D-117): the nearest word within two edits, a tie to the
// first in the given order, nothing farther away.
func TestDidYouMean(t *testing.T) {
	verbs := []string{"read", "control", "edit", "create", "delete", "write"}
	for _, tc := range []struct {
		unknown string
		words   []string
		want    string
	}{
		{"wirte", verbs, ` — did you mean "write"?`},
		{"delet", verbs, ` — did you mean "delete"?`},
		{"frobnicate", verbs, ""},
		{"rea", verbs, ` — did you mean "read"?`},
		// "ab" is one edit from both; the first in order wins.
		{"ab", []string{"abc", "abd"}, ` — did you mean "abc"?`},
		{"ab", []string{"abd", "abc"}, ` — did you mean "abd"?`},
		// Distance 3 is too far, whatever the order.
		{"xyz", []string{"abc"}, ""},
		{"x", nil, ""},
	} {
		if got := didYouMean(tc.unknown, tc.words); got != tc.want {
			t.Errorf("didYouMean(%q, %v) = %q, want %q", tc.unknown, tc.words, got, tc.want)
		}
	}
}

// TestBrokenFileSuggestsPerPosition (D-117): each position a broken file can
// misspell — verb, top-level key, profile key, selector key, kind,
// sub-resource word, registry field — suggests from its own words only.
func TestBrokenFileSuggestsPerPosition(t *testing.T) {
	const head = "version: 1\nactive: agent\nprofiles:\n  agent:\n"
	for _, tc := range []struct {
		name, content string
		line          int
		want          string
	}{
		{"verb", head + "    deny:\n      \"entity:lock.*\": [delte]\n", 6, `did you mean "delete"?`},
		{"top-level key", "version: 1\nactive: agent\nprofile:\n  agent: {}\n", 3, `did you mean "profiles"?`},
		{"profile key", head + "    defualt: deny\n", 5, `did you mean "default"?`}, //nolint:misspell // the typo is the input
		{"selector key", head + "    allow:\n      \"entity:*\": [control]\n    where:\n      \"entity:*\": {lable: x}\n", 8, `did you mean "label"?`},
		{"kind", head + "    deny:\n      \"scirpt:*\": [edit]\n", 6, `did you mean "script"?`}, //nolint:misspell // the typo is the input
		{"sub word", head + "    deny:\n      \"automation:*/confg\": [edit]\n", 6, `did you mean "config"?`},
		{"registry field", head + "    deny:\n      \"device:*/registry/areaid\": [edit]\n", 6, `did you mean "area_id"?`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ps := loadProblems(t, tc.content)
			if !hasProblem(ps, tc.line, " — "+tc.want) {
				t.Errorf("problems = %+v, want one at line %d ending %q", ps, tc.line, tc.want)
			}
			for _, p := range ps {
				if p.Line == tc.line && !strings.HasSuffix(p.Message, tc.want) {
					t.Errorf("the suggestion is not the message's end: %q", p.Message)
				}
			}
		})
	}
	// Too far: no suggestion at all.
	for _, content := range []string{
		head + "    deny:\n      \"entity:lock.*\": [frobnicate]\n",
		head + "    nonsense: deny\n",
		head + "    deny:\n      \"gizmo:*\": [edit]\n",
	} {
		for _, p := range loadProblems(t, content) {
			if strings.Contains(p.Message, "did you mean") {
				t.Errorf("a word three or more edits from any valid one got a suggestion: %q", p.Message)
			}
		}
	}
}
