//go:build livefire

package livefire

import "testing"

// The census is what tells the sweep whether it damaged the instance, so a
// census that silently reads nothing is worse than one that fails: every
// "nothing outside pg_* changed" comparison after it passes vacuously, and the
// run reports a clean house exactly when it has lost the ability to look.
func TestCensusRefusesAnEmptyListing(t *testing.T) {
	for _, tc := range []struct{ name, doc string }{
		{"empty array", `[]`},
		{"null", `null`},
		{"prose instead of json", "no areas\n"},
		{"an object, not a listing", `{"areas":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseCensusRows("name", tc.doc); err == nil {
				t.Errorf("census accepted %q — later damage comparisons would pass vacuously", tc.doc)
			}
		})
	}

	names, err := parseCensusRows("name", `[{"name":"Kitchen"},{"name":"Hall"}]`)
	if err != nil {
		t.Fatalf("census rejected a real listing: %v", err)
	}
	if len(names) != 2 || names[0] != "Hall" || names[1] != "Kitchen" {
		t.Errorf("census returned %v, want sorted [Hall Kitchen]", names)
	}
}
