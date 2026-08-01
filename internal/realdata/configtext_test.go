package realdata_test

import (
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/realdata"
	"gopkg.in/yaml.v3"
)

// The config-tree half of the generator, tested on the shapes automations.yaml
// turned out to hold — every one of which was found by a real file rather than
// imagined, and three of which the first implementation got wrong.

// TestSanitizeConfigTextRewritesAScalarThatWraps is the defect that produced a
// fixture Home Assistant refused to load.
//
// A `key: value` regular expression sees one line. YAML does not: the reference
// instance has aliases long enough that its emitter wrapped them, so the value
// continued on the next line. Replacing only the first line's text left the
// continuation behind as a dangling scalar, and the file — 9,600 lines of it —
// failed to parse at boot with `expected <block end>`. Every count-based check
// passed on that run.
func TestSanitizeConfigTextRewritesAScalarThatWraps(t *testing.T) {
	var s realdata.Sanitizer
	src := "- id: '1'\n" +
		"  alias: 'Übergabe Anwesenheit Küche Bad Schlafzimmer Wohnzimmer Flur\n" +
		"    auf '\n" +
		"  mode: single\n"

	out := realdata.SanitizeConfigText(src, &s)
	if drift := realdata.StructureDrift(src, out); len(drift) > 0 {
		t.Fatalf("the derivative is not the same document as its source: %v\n%s", drift, out)
	}
	for _, word := range []string{"Anwesenheit", "Küche", "Schlafzimmer", "auf"} {
		if strings.Contains(out, word) {
			t.Errorf("the wrapped alias still carries %q — only its first line was replaced:\n%s", word, out)
		}
	}
	if lines := strings.Count(out, "\n"); lines != strings.Count(src, "\n") {
		t.Errorf("the derivative has %d lines and the source has %d — the wrap is a shape",
			lines, strings.Count(src, "\n"))
	}
}

// TestSanitizeConfigTextLeavesACommentBelowAValueAlone is the other half of the
// same question, and the answer that indentation alone cannot give.
//
// template.yaml has a `unique_id:` followed by a blank line and two comment
// lines indented deeper than the key. Reading "more indented" as "part of the
// value" swallowed the comments into the scalar and replaced them there, which
// changed a regeneration of a file that had been correct for a week.
func TestSanitizeConfigTextLeavesACommentBelowAValueAlone(t *testing.T) {
	var s realdata.Sanitizer
	src := "- sensor:\n" +
		"  - state: '{{ 1 }}'\n" +
		"    unique_id: uuidTemplateKaltok\n" +
		"\n" +
		"      #\n" +
		"      #    float(states('input_number.kwl_anforderung'),0),\n" +
		"- sensor:\n" +
		"  - state: '{{ 2 }}'\n"

	out := realdata.SanitizeConfigText(src, &s)
	if drift := realdata.StructureDrift(src, out); len(drift) > 0 {
		t.Fatalf("structure drift: %v\n%s", drift, out)
	}
	// The comment is still a comment, at its column, with its `#` alone on the
	// line above it. Its WORDS are replaced — that is the comment pass's job and
	// it has its own reason (a comment named a room once) — but its position is
	// the shape.
	if !strings.Contains(out, "\n      #\n") {
		t.Errorf("the bare `#` line moved or was rewritten:\n%s", out)
	}
	if strings.Contains(out, "uuidTemplateKaltok") {
		t.Errorf("the unique_id survived:\n%s", out)
	}

	// And it is the unique_id path that replaced the unique_id. Swallowing the
	// comments into the value makes the whole span prose, and prose is
	// substituted word for word — so the id comes out as one unbroken run of
	// letters where Opaque would have produced the underscore-separated slug a
	// unique_id actually looks like. That is the difference the boot never
	// notices and a reader of `ref scan` would.
	var id string
	for line := range strings.SplitSeq(out, "\n") {
		if key, value, found := strings.Cut(strings.TrimSpace(line), "unique_id:"); found && key == "" {
			id = strings.TrimSpace(value)
		}
	}
	if !strings.Contains(id, "_") {
		t.Errorf("the unique_id became %q — a run with no separator is what the prose "+
			"substitution produces, so the comments below it were taken for part of its value", id)
	}
}

// TestSanitizeConfigTextReplacesProseButNotWhatRendersIt — a description is the
// one place free text reaches a config file, and on the reference instance four
// of the household's first names are in one.
func TestSanitizeConfigTextReplacesProseButNotWhatRendersIt(t *testing.T) {
	var s realdata.Sanitizer
	src := "- id: '1'\n" +
		"  description: 'Neuberechnung von Gritta LIVE seit 2026-07-13: schreibt auf\n" +
		"    input_number.posclock_jan. Alte Bridge \"Jans Zeiger\" ist weg.\n" +
		"\n" +
		"    '\n" +
		"  actions:\n" +
		"  - data:\n" +
		"      message: \"Preis: {{ states('sensor.strompreis') }} €/kWh\"\n"

	out := realdata.SanitizeConfigText(src, &s)
	if drift := realdata.StructureDrift(src, out); len(drift) > 0 {
		t.Fatalf("structure drift: %v\n%s", drift, out)
	}
	for _, personal := range []string{"Gritta", "Jans", "Neuberechnung", "Bridge", "Preis"} {
		if strings.Contains(out, personal) {
			t.Errorf("the prose still carries %q:\n%s", personal, out)
		}
	}
	// The template renders against Home Assistant, so its expression has to
	// survive intact — and the entity inside it has to keep the name the entity
	// pass gave it everywhere else.
	if !strings.Contains(out, "{{ states('sensor.") || !strings.Contains(out, "') }}") {
		t.Errorf("the Jinja expression did not survive the prose pass:\n%s", out)
	}
	if strings.Contains(out, "sensor.strompreis") || strings.Contains(out, "input_number.posclock_jan") {
		t.Errorf("an entity reference inside prose kept its real object id:\n%s", out)
	}
	if strings.Contains(out, "2026-07-13") {
		t.Errorf("a number inside free text survived — a house number and a phone number "+
			"reach a config the same way:\n%s", out)
	}
	// The rune length of each line is what makes the wrap a wrap.
	source := strings.Split(src, "\n")
	for i, line := range strings.Split(out, "\n") {
		want := source[i]
		if len([]rune(line)) != len([]rune(want)) {
			t.Errorf("line %d is %d runes and was %d — the prose substitution has to be length-for-length\n%q\n%q",
				i+1, len([]rune(line)), len([]rune(want)), line, want)
		}
	}
}

// TestProseSubstitutionIsLengthForLengthInEveryCase — the property the whole
// prose pass rests on, asserted against the cases that break it.
//
// A line in a wrapped scalar is a line because of where its last word ends. A
// replacement one rune longer or shorter moves that boundary, and YAML re-folds
// around the new one — so the fixture stops carrying the wrap it was captured
// for, and does so silently, because the document still parses.
// The replacement is drawn by hashing the source word, so one example proves
// nothing about the next one: `GRÖSSE` is only a counter-example when the hash
// happens to put a `ß` in the replacement. So the case quantifies — enough
// distinct all-capitals words that every character of the non-ASCII vocabulary
// is reached, and a failure is then a property of the code rather than of which
// word somebody picked.
func TestProseSubstitutionIsLengthForLengthInEveryCase(t *testing.T) {
	var s realdata.Sanitizer
	const generated = 200
	words := make([]string, 0, generated+6)
	words = append(words, "Straße", "A", "x", "Küche", "GRÖSSE", "WÄRMERÜCKGEWINNUNG")
	for i := range generated {
		words = append(words, strings.ToUpper("MÜLL"+strconv.Itoa(i)+"Ö"))
	}
	for _, word := range words {
		src := "- description: 'a " + word + " b'\n"
		out := realdata.SanitizeConfigText(src, &s)
		if len([]rune(out)) != len([]rune(src)) {
			t.Fatalf("%q: the line went from %d runes to %d\n%q", word,
				len([]rune(src)), len([]rune(out)), out)
		}
		if strings.Contains(out, word) {
			t.Errorf("%q survived the prose pass: %q", word, out)
		}
	}
}

// TestStructureDriftCatchesADanglingScalar: the gate that would have caught the
// wrapped-alias defect in the second it happened, rather than at a container's
// boot log twenty minutes later.
func TestStructureDriftCatchesADanglingScalar(t *testing.T) {
	src := "- alias: 'a long name\n    that wraps'\n  mode: single\n"
	broken := "- alias: 'short'\n    that wraps'\n  mode: single\n"

	if drift := realdata.StructureDrift(src, broken); len(drift) == 0 {
		t.Error("StructureDrift accepted a derivative that is not valid YAML at all")
	}
	// A value that changed is the POINT, so it must not be reported.
	if drift := realdata.StructureDrift(src, "- alias: 'Hallway Latch'\n  mode: single\n"); len(drift) > 0 {
		t.Errorf("StructureDrift reported a replaced value as drift: %v", drift)
	}
	// A key that changed is not.
	if drift := realdata.StructureDrift(src, "- name: 'Hallway Latch'\n  mode: single\n"); len(drift) == 0 {
		t.Error("StructureDrift accepted a derivative whose key had been renamed")
	}
	// So is an item that vanished.
	if drift := realdata.StructureDrift(src+"- alias: b\n", src); len(drift) == 0 {
		t.Error("StructureDrift accepted a derivative that lost a top-level item")
	}
	// And so is nothing at all. An empty derivative decodes without error — a
	// zero Node, not a failure — so H-7's question has to be asked here
	// explicitly or a capture that wrote an empty file would report no drift.
	if drift := realdata.StructureDrift(src, ""); len(drift) == 0 {
		t.Error("StructureDrift accepted an empty derivative as the same document")
	}
	if drift := realdata.StructureDrift(src, "\n# only a comment\n"); len(drift) == 0 {
		t.Error("StructureDrift accepted a derivative holding no document at all")
	}
}

// TestExclusionReportIsOrderedNotMapWalked — H-16. The counts live in a map
// keyed by the reason, and Go randomises that walk per run, so a capture's own
// log would otherwise print its three lines in a different order every time and
// a reader diffing two regenerations could not tell a change from a reshuffle.
func TestExclusionReportIsOrderedNotMapWalked(t *testing.T) {
	removed := map[string]int{
		"triggered by MQTT":       3,
		"a community blueprint":   10,
		"a device only one house": 1,
	}
	first := realdata.ExclusionReport(removed)
	for range 20 {
		if got := realdata.ExclusionReport(removed); !slices.Equal(got, first) {
			t.Fatalf("ExclusionReport is not stable across runs:\n%v\n%v", first, got)
		}
	}
	want := []string{
		"10 dropped: a community blueprint",
		"1 dropped: a device only one house",
		"3 dropped: triggered by MQTT",
	}
	if !slices.Equal(first, want) {
		t.Errorf("ExclusionReport = %v, want it ordered by reason: %v", first, want)
	}
}

// TestDropTopLevelItemsReportsWhatItRemoved — SPEC §5's bootability rule is that
// what cannot be carried is named. A count of zero is the interesting one: it
// means the marker no longer matches anything and the reason has stopped
// describing the source.
func TestDropTopLevelItemsReportsWhatItRemoved(t *testing.T) {
	src := "# a preamble comment\n" +
		"- id: keep_me\n  mode: single\n" +
		"- id: blueprinted\n  use_blueprint:\n    path: someone/thing.yaml\n" +
		"- id: keep_me_too\n  mode: restart\n"

	out, removed := realdata.DropTopLevelItems(src, []realdata.Exclusion{
		{Marker: "use_blueprint:", Why: "a community blueprint the fixture does not carry"},
		{Marker: "platform: mqtt", Why: "an MQTT trigger and the rig has no broker"},
	})
	if got := removed["a community blueprint the fixture does not carry"]; got != 1 {
		t.Errorf("the blueprint exclusion removed %d items, want 1", got)
	}
	if _, reported := removed["an MQTT trigger and the rig has no broker"]; !reported {
		t.Error("an exclusion that matched nothing is missing from the report, so a marker that " +
			"has stopped matching cannot be noticed")
	}
	if strings.Contains(out, "blueprinted") {
		t.Errorf("the excluded item survived:\n%s", out)
	}
	for _, keep := range []string{"# a preamble comment", "keep_me", "keep_me_too"} {
		if !strings.Contains(out, keep) {
			t.Errorf("%q was removed with its neighbour:\n%s", keep, out)
		}
	}
	var doc []map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("the result is not a YAML list: %v\n%s", err, out)
	}
	if len(doc) != 2 {
		t.Errorf("%d items survived, want 2", len(doc))
	}
}

// TestInertStopsAnItemWithoutChangingWhatItSays: the automations are carried for
// their shape, not their behaviour (see realdata.Inert).
func TestInertStopsAnItemWithoutChangingWhatItSays(t *testing.T) {
	src := "- id: fires\n  triggers:\n  - trigger: time_pattern\n    hours: '/1'\n\n" +
		"- id: already_said_so\n  initial_state: true\n  mode: single\n"

	out, changed := realdata.Inert(src)
	if changed != 1 {
		t.Errorf("Inert changed %d items, want 1 — the one that already stated an initial_state "+
			"must keep the source's answer", changed)
	}
	var doc []map[string]any
	if err := yaml.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("the result is not a YAML list: %v\n%s", err, out)
	}
	if len(doc) != 2 {
		t.Fatalf("%d items survived, want 2:\n%s", len(doc), out)
	}
	if doc[0]["initial_state"] != false {
		t.Errorf("the first item is still able to run: %v", doc[0])
	}
	if doc[1]["initial_state"] != true {
		t.Errorf("Inert overwrote an initial_state the source stated: %v", doc[1])
	}
	// The trigger is what the entry is carried FOR, so it has to be untouched.
	if !strings.Contains(out, "trigger: time_pattern") {
		t.Errorf("the trigger form did not survive:\n%s", out)
	}
	// And the blank line between the two items is a separator, not part of the
	// first item, so the new key goes above it.
	if !strings.Contains(out, "hours: '/1'\n  initial_state: false\n\n- id:") {
		t.Errorf("initial_state landed after the item's trailing blank line:\n%q", out)
	}
}
