package realdata_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/hemm-ems/hactl/internal/realdata"
)

// SPEC-realdata-fixture.md A4 (TC-8): a document containing each sensitive
// category, fed to the sanitizer, and a failure if the output still carries it.
//
// The categories are the ones actually found in the snapshot (§11): coordinates,
// an external hostname, IPv4s, MACs, plaintext device passwords that are not
// `!secret`-tagged, an API key, and personal first names — several of which are
// used as entity and area IDENTIFIERS rather than only as display names, which
// is why the identifier path is tested and not just the name path.
func TestSanitizerRemovesEverySensitiveCategory(t *testing.T) {
	var s realdata.Sanitizer

	for _, c := range []struct {
		category string
		value    string
	}{
		{"a personal first name used as a display name", "Gritta"},
		{"a child's name used as an area name", "Lasse"},
		{"a display name carrying a personal name", "Sender für Lasse:"},
		{"a plaintext WiFi password", "hunter2-not-a-real-one"},
		{"an API key", "b17e4f2c9a0d4e1fb6c3a8d5e2f10987"},
		{"an external hostname", "ha.someones-domain.example"},
	} {
		t.Run(c.category, func(t *testing.T) {
			if got := s.Name(c.value); strings.Contains(got, c.value) {
				t.Errorf("Name(%q) = %q — the source value survived", c.value, got)
			}
		})
	}

	// Identifiers are the sharper half: an area named after a child is a
	// display name, but its area_id is a slug of that name and travels into
	// every automation that references it.
	for _, id := range []string{"radio_station_lasse", "bett_gritta", "handy_jan_80_geladen_prognose"} {
		if got := s.Identifier(id); got == id {
			t.Errorf("Identifier(%q) returned the source unchanged", id)
		}
		for _, personal := range []string{"lasse", "gritta", "jan"} {
			if strings.Contains(s.Identifier(id), personal) {
				t.Errorf("Identifier(%q) = %q — it still carries %q", id, s.Identifier(id), personal)
			}
		}
	}
}

// A5: same input, byte-identical output. Asserted across two independent
// Sanitizer values, not two calls on one, because a cache would make the second
// call trivially agree with the first while a fresh run — which is what a
// regeneration is — could still differ.
func TestSanitizerIsDeterministicAcrossRuns(t *testing.T) {
	sources := []string{"Anwesenheit Flur", "Sender für Lasse:", "KWL Mode", "Solltemperatur"}
	ids := []string{"anwesenheit_flur", "radio_station_schlafzimmer", "kwl_mode"}

	var first, second realdata.Sanitizer
	for _, src := range sources {
		if a, b := first.Name(src), second.Name(src); a != b {
			t.Errorf("Name(%q) is not deterministic: %q vs %q", src, a, b)
		}
	}
	for _, id := range ids {
		if a, b := first.Identifier(id), second.Identifier(id); a != b {
			t.Errorf("Identifier(%q) is not deterministic: %q vs %q", id, a, b)
		}
	}
	// And stable within a run: the same source must not map to two values, or
	// a helper's id and the automations referencing it would drift apart.
	if a, b := first.Identifier("kwl_mode"), first.Identifier("kwl_mode"); a != b {
		t.Errorf("Identifier is not stable within a run: %q vs %q", a, b)
	}
}

// The shapes the fixture exists to carry have to survive the replacement, or
// the derivative is safe and useless. §11: identifier lengths at realistic
// magnitude, and non-ASCII that a byte-versus-rune cut can be caught on.
func TestSanitizerPreservesTheShapesTheFixtureExistsFor(t *testing.T) {
	var s realdata.Sanitizer

	long := "handy_gritta_80_geladen_prognose_zusatzliche_lange_kennung_xy"
	if got := s.Identifier(long); len([]rune(got)) != len([]rune(long)) {
		t.Errorf("Identifier(%q) is %d runes, want %d — identifier length is a shape (§11: 67 chars vs the rig's ~20)",
			long, len([]rune(got)), len([]rune(long)))
	}
	if got := s.Identifier(long); !legalSlug(got) {
		t.Errorf("Identifier(%q) = %q is not a legal Home Assistant slug", long, got)
	}

	umlaut := "Sender für Lasse:"
	got := s.Name(umlaut)
	if !anyNonASCII(got) {
		t.Errorf("Name(%q) = %q lost its non-ASCII — finding #14 is a byte-versus-rune cut and a pure-ASCII fixture cannot express it", umlaut, got)
	}
	if len([]rune(got)) != len([]rune(umlaut)) {
		t.Errorf("Name(%q) is %d runes, want %d", umlaut, len([]rune(got)), len([]rune(umlaut)))
	}
	if !utf8Clean(got) {
		t.Errorf("Name(%q) = %q was cut on a byte boundary and is not valid UTF-8", umlaut, got)
	}
}

// The leak gate has to fail on a tree that leaks, or its silence on the real
// fixture means nothing. This is the gate's own TC-8.
func TestLeakGateFlagsATreeThatLeaks(t *testing.T) {
	source := t.TempDir()
	writeFile(t, source, "configuration.yaml", `homeassistant:
  latitude: 48.137154
  longitude: 11.576124
  name: Someones House
sensor:
  - platform: rest
    resource: http://ha.someones-domain.example/api
    password: hunter2-not-a-real-one
`)

	literals, err := realdata.SensitiveLiterals(source)
	if err != nil {
		t.Fatalf("extracting: %v", err)
	}
	for _, want := range []string{"48.137154", "11.576124", "Someones House", "hunter2-not-a-real-one"} {
		if _, found := literals[want]; !found {
			t.Errorf("SensitiveLiterals did not extract %q — the derived gate is blind to it", want)
		}
	}

	leaky := t.TempDir()
	writeFile(t, leaky, "template.yaml", "- sensor:\n  - name: Someones House\n    state: '48.137154'\n")
	leaks, err := realdata.Contains(leaky, literals)
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	if len(leaks) < 2 {
		t.Errorf("the derived gate found %d leaks in a tree that carries two: %v", len(leaks), leaks)
	}

	clean := t.TempDir()
	writeFile(t, clean, "template.yaml", "- sensor:\n  - name: Hallway Setpoint\n    state: '1'\n")
	if leaks, err = realdata.Contains(clean, literals); err != nil || len(leaks) != 0 {
		t.Errorf("the derived gate reported %v on a clean tree (err=%v)", leaks, err)
	}
}

// The shape gate is the half that does not need the source, so it is tested
// without one — on values no capture ever contained.
func TestShapeGateRefusesRealWorldShapesItHasNeverSeen(t *testing.T) {
	leaky := t.TempDir()
	writeFile(t, leaky, "esphome.yaml", `wifi:
  address: 84.112.9.14
  bssid: 3C:22:FB:1A:9E:04
zone:
  latitude: 48.137154
`)
	leaks, err := realdata.ShapeLeaks(leaky)
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	found := map[string]bool{}
	for _, l := range leaks {
		found[l.Value] = true
	}
	for _, want := range []string{"84.112.9.14", "3C:22:FB:1A:9E:04", "48.137154"} {
		if !found[want] {
			t.Errorf("ShapeLeaks did not flag %q — a leak from a future capture would pass", want)
		}
	}

	// And it must stay quiet on the values the fixture is allowed to carry, or
	// it is a gate nobody can keep green.
	clean := t.TempDir()
	writeFile(t, clean, "configuration.yaml", `homeassistant:
  latitude: 52.520
  longitude: 13.405
mqtt:
  broker: 192.168.1.10
rest:
  resource: http://192.0.2.7/api
device:
  connections: [["mac", "02:00:5e:11:22:33"]]
version: 2026.7.4
`)
	if leaks, err = realdata.ShapeLeaks(clean); err != nil || len(leaks) != 0 {
		t.Errorf("ShapeLeaks reported %v on a tree carrying only documentation values (err=%v)", leaks, err)
	}
}

func writeFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func legalSlug(s string) bool {
	if s == "" || s[0] == '_' || s[len(s)-1] == '_' {
		return false
	}
	for _, r := range s {
		if !unicode.IsLower(r) && !unicode.IsDigit(r) && r != '_' {
			return false
		}
	}
	return true
}

func anyNonASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return true
		}
	}
	return false
}

func utf8Clean(s string) bool { return strings.ToValidUTF8(s, "�") == s }

// H-16 applied to the gate's own report: the finding list is ordered by where
// the leak is, not by how Go happened to walk the extracted literals.
//
// realdata.Contains ranges over a map, and Go randomises that per run. Without
// a canonical order the same fixture produced the same findings in a different
// sequence every time, so a reviewer diffing two runs could not tell a new leak
// from a reshuffle — and a leak gate whose output moves on its own is one
// nobody reads carefully.
func TestLeakReportIsOrderedByLocationNotByMapWalk(t *testing.T) {
	source := t.TempDir()
	writeFile(t, source, "configuration.yaml", `homeassistant:
  name: Someones House
  latitude: 48.137154
sensor:
  password: hunter2-not-a-real-one
  host: ha.someones-domain.example
`)
	literals, err := realdata.SensitiveLiterals(source)
	if err != nil {
		t.Fatalf("extracting: %v", err)
	}

	leaky := t.TempDir()
	writeFile(t, leaky, "a.yaml", "name: Someones House\nhost: ha.someones-domain.example\n")
	writeFile(t, leaky, "b.yaml", "state: '48.137154'\npassword: hunter2-not-a-real-one\n")

	first, err := realdata.Contains(leaky, literals)
	if err != nil {
		t.Fatalf("scanning: %v", err)
	}
	if len(first) < 4 {
		t.Fatalf("want at least four leaks to have an order worth pinning, got %d: %v", len(first), first)
	}
	for range 8 {
		again, scanErr := realdata.Contains(leaky, literals)
		if scanErr != nil {
			t.Fatalf("scanning: %v", scanErr)
		}
		if len(again) != len(first) {
			t.Fatalf("the gate reported %d leaks and then %d", len(first), len(again))
		}
		for i := range first {
			if again[i] != first[i] {
				t.Fatalf("leak %d moved between runs: %v then %v", i, first[i], again[i])
			}
		}
	}
}

// H-16 for the shape report: ShapeDrift walks three maps, and a drift list
// whose order moves between runs is a diff a reviewer cannot read.
//
// The block-key comparison is the one that matters — it iterates the union of
// two `map[string]int`, which Go randomises — so the assertion uses several
// keys that all drift at once. With one key the test would pass on an
// unsorted implementation.
func TestShapeDriftIsOrderedNotMapWalked(t *testing.T) {
	before := realdata.MeasureConfig(
		"- sensor:\n  - name: a\n- binary_sensor:\n  - name: b\n- switch:\n  - name: c\n- trigger:\n  - x: 1\n")
	after := realdata.MeasureConfig("- sensor:\n  - name: a\n")

	first := realdata.ShapeDrift(before, after)
	if len(first) < 4 {
		t.Fatalf("want several drifting keys to have an order worth pinning, got %d: %v", len(first), first)
	}
	for range 12 {
		again := realdata.ShapeDrift(before, after)
		if len(again) != len(first) {
			t.Fatalf("ShapeDrift reported %d entries then %d", len(first), len(again))
		}
		for i := range first {
			if again[i] != first[i] {
				t.Fatalf("entry %d moved between runs: %q then %q", i, first[i], again[i])
			}
		}
	}

	// And the report is empty when nothing moved, or every capture would look
	// like a regression.
	if drift := realdata.ShapeDrift(before, before); len(drift) != 0 {
		t.Errorf("ShapeDrift reported %v for an unchanged shape", drift)
	}
}
