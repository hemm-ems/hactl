package manual

import (
	"strings"
	"testing"
)

// Guardrail: every heading the taxonomy maps must exist verbatim in
// docs/manual.md (Rule 11 in docs/llm-tuning.md). A renamed heading otherwise
// silently drops its section from delivery.
func TestMappedHeadingsExist(t *testing.T) {
	s := Sections()
	for _, h := range CoreHeadings {
		if _, ok := s[h]; !ok {
			t.Errorf("core heading not found in manual: %s", h)
		}
	}
	for family, headings := range FamilySections {
		for _, h := range headings {
			if _, ok := s[h]; !ok {
				t.Errorf("family %s: heading not found in manual: %s", family, h)
			}
		}
	}
}

func TestAliasTargetsExist(t *testing.T) {
	for alias, family := range Aliases {
		if _, ok := FamilySections[family]; !ok {
			t.Errorf("alias %s points to unknown family %s", alias, family)
		}
	}
}

func TestFamiliesListMatchesMap(t *testing.T) {
	list := Families()
	if len(list) != len(FamilySections) {
		t.Fatalf("Families() has %d entries, FamilySections has %d", len(list), len(FamilySections))
	}
	for _, f := range list {
		if _, ok := FamilySections[f]; !ok {
			t.Errorf("Families() lists unknown family %s", f)
		}
	}
}

// The core block is the per-session cold-start budget (~1.4k tokens). Fail if
// manual edits push it well outside that envelope.
func TestCoreTextSize(t *testing.T) {
	n := len(CoreText())
	if n < 4096 || n > 8192 {
		t.Errorf("CoreText() is %d bytes, want 4096..8192 (~1-2k tokens)", n)
	}
}

func TestSectionsShape(t *testing.T) {
	s := Sections()
	if !strings.HasPrefix(s[PreambleKey], "# hactl Manual") {
		t.Errorf("preamble does not start with the manual title: %.40q", s[PreambleKey])
	}
	sec, ok := s["## Quick routing"]
	if !ok {
		t.Fatal("no ## Quick routing section")
	}
	if !strings.HasPrefix(sec, "## Quick routing\n") {
		t.Errorf("section does not start with its heading line: %.40q", sec)
	}
	if strings.Contains(sec, "## Mental model") {
		t.Error("section leaks into the next heading")
	}
}

func TestFamilyText(t *testing.T) {
	full, headings := FamilyText("auto", nil)
	if len(headings) != len(FamilySections["auto"]) {
		t.Fatalf("auto: got %d headings, want %d", len(headings), len(FamilySections["auto"]))
	}
	if !strings.Contains(full, "### Write path (automations)") {
		t.Error("auto text missing reference section")
	}

	skip := map[string]bool{}
	for _, h := range headings[:len(headings)-1] {
		skip[h] = true
	}
	rest, restHeadings := FamilyText("auto", skip)
	if len(restHeadings) != 1 || restHeadings[0] != headings[len(headings)-1] {
		t.Errorf("skip did not reduce to the last heading: %v", restHeadings)
	}
	if strings.Contains(rest, headings[0]) {
		t.Error("skipped heading still present in text")
	}

	// Inverted 2026-07-23: this used to assert `ref` delivers nothing, pinning
	// the gap as if it were the design. It was a defect — `ref scan|replace|
	// validate` are real commands whose family how-to never reached a caller.
	if text, hs := FamilyText("ref", nil); text == "" || len(hs) == 0 {
		t.Error("ref delivers no manual section; its commands would arrive unexplained")
	}
	if text, hs := FamilyText("nosuch", nil); text != "" || len(hs) != 0 {
		t.Errorf("unknown family should be empty, got %q %v", text, hs)
	}
}

// Every family must carry at least one manual section, or its commands arrive
// with no how-to at all — the state `ref` was in until 2026-07-23. Delivery is
// silent when a family maps to nothing, so only a test makes it visible.
func TestEveryFamilyHasSections(t *testing.T) {
	for _, f := range Families() {
		if len(FamilySections[f]) == 0 {
			t.Errorf("family %q maps to no manual section: add one in docs/manual.md and map it here", f)
		}
	}
}

func TestFamilyFor(t *testing.T) {
	cases := map[string]struct {
		family string
		ok     bool
	}{
		"auto":     {"auto", true},
		"trace":    {"auto", true},
		"rollback": {"auto", true},
		"cc":       {"log", true},
		"issues":   {"health", true},
		"changes":  {"health", true},
		"area":     {"label", true},
		"floor":    {"label", true},
		"ref":      {"ref", true},
		"setup":    {"", false}, // exempt on the CLI, not aliased like tools.py
		"rtfm":     {"", false},
		"nosuch":   {"", false},
	}
	for top, want := range cases {
		got, ok := FamilyFor(top)
		if got != want.family || ok != want.ok {
			t.Errorf("FamilyFor(%s) = %q,%v; want %q,%v", top, got, ok, want.family, want.ok)
		}
	}
}

func TestNotesStartWithMarker(t *testing.T) {
	// dev/tuning/inject_tokens.py identifies injected blocks by this prefix.
	for name, note := range map[string]string{
		"CoreNote":   CoreNote,
		"FullNote":   FullNote,
		"FamilyNote": FamilyNote("auto"),
	} {
		if !strings.HasPrefix(note, "[hactl manual") {
			t.Errorf("%s must start with %q: %.30q", name, "[hactl manual", note)
		}
	}
}
