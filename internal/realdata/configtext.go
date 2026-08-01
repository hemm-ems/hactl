package realdata

import (
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// The YAML config tree
// ---------------------------------------------------------------------------
//
// The helper collections are JSON documents the generator builds from scratch.
// The config tree is not: it is somebody's hand-written YAML, and §11's whole
// point is that its MESS is the shape — 91 top-level blocks in template.yaml,
// commented-out keys with live content beneath them, ragged indentation,
// trailing whitespace in 117 of 169 files, block scalars, non-ASCII in names,
// comment positions.
//
// So this sanitizes the TEXT, not a parsed document. A yaml.Node round-trip
// would be the obvious approach and it destroys exactly what is being carried:
// comments do not survive intact, block scalar style is normalised, quoting is
// rewritten, and blank lines between blocks are lost. Every one of those is a
// property some command has to cope with — `tpl create` appending to the right
// block, `config file` resolving a document without reformatting it — and a
// fixture that arrives pre-normalised cannot test any of them.
//
// The replacements are therefore surgical and positional:
//
//   - an entity reference `<domain>.<object_id>` anywhere, including inside a
//     Jinja expression, so `states('sensor.x')` stays valid and every reference
//     to one entity keeps agreeing with every other;
//   - the value of a `name:` / `friendly_name:` / `alias:` key;
//   - the value of a `unique_id:` key.
//
// Everything else is passed through untouched, and the leak gate is what says
// whether that was enough. This file may not decide it is safe; it can only
// fail to be caught, which is why realdata.ShapeLeaks reads the RESULT rather
// than trusting the transformation.

// entityDomains is the set of Home Assistant entity domains an identifier can
// be qualified by.
//
// It is a list, and that is legitimate here in a way it would not be in a gate:
// this is a TRANSFORMATION input, not an enforcement mechanism. A domain missing
// from it means an identifier is not rewritten — which the leak gate then
// catches, because the gate reads the output and does not consult this list.
// The project's rule is that a hand-list may not be what proves a property; it
// says nothing against one being what performs a substitution.
//
// The set is Home Assistant's own and public. It identifies nobody.
var entityDomains = []string{
	"air_quality", "alarm_control_panel", "automation", "binary_sensor", "button",
	"calendar", "camera", "climate", "conversation", "counter", "cover", "date",
	"datetime", "device_tracker", "event", "fan", "group", "humidifier", "image",
	"image_processing", "input_boolean", "input_button", "input_datetime",
	"input_number", "input_select", "input_text", "lawn_mower", "light", "lock",
	"media_player", "notify", "number", "person", "remote", "scene", "schedule",
	"script", "select", "sensor", "siren", "stt", "sun", "switch", "text", "time",
	"timer", "todo", "tts", "update", "vacuum", "valve", "wake_word", "water_heater",
	"weather", "zone",
}

var (
	entityRef  = regexp.MustCompile(`\b(` + strings.Join(entityDomains, "|") + `)\.([a-z0-9_]+)\b`)
	namedValue = regexp.MustCompile(`(?m)^(\s*(?:-\s+)?(?:name|friendly_name|alias|title):\s*)(.+?)(\s*)$`)
	uniqueID   = regexp.MustCompile(`(?m)^(\s*(?:-\s+)?unique_id:\s*)(.+?)(\s*)$`)
	// A whole-line comment only. A `#` after a value is far more often inside a
	// template or a quoted string than it is a comment, and rewriting one of
	// those would change what the document MEANS rather than what it says.
	wholeLineComment = regexp.MustCompile(`(?m)^([ \t]*)#([ \t]*)([^\n]*)$`)
)

// SanitizeConfigText rewrites one config file's identifying values, leaving
// every byte that is not one of them exactly where it was.
func SanitizeConfigText(src string, s *Sanitizer) string {
	out := entityRef.ReplaceAllStringFunc(src, func(match string) string {
		parts := entityRef.FindStringSubmatch(match)
		return parts[1] + "." + s.Identifier(parts[2])
	})

	out = uniqueID.ReplaceAllStringFunc(out, func(match string) string {
		p := uniqueID.FindStringSubmatch(match)
		// Opaque, not Identifier: Home Assistant never parses a unique_id, and
		// eight of the reference instance's carry an umlaut that Identifier's
		// slug grammar would delete.
		return p[1] + quoteLike(p[2], s.Opaque(unquote(p[2]))) + p[3]
	})

	out = namedValue.ReplaceAllStringFunc(out, func(match string) string {
		p := namedValue.FindStringSubmatch(match)
		value := unquote(p[2])
		// A block scalar or a template is a VALUE with structure, not a name;
		// rewriting `name: >` would destroy the document. Names that are
		// themselves templates keep whatever the entity rewrite above already
		// did to them.
		if value == "" || strings.HasPrefix(value, ">") || strings.HasPrefix(value, "|") ||
			strings.Contains(value, "{{") || strings.Contains(value, "{%") {
			return match
		}
		return p[1] + quoteLike(p[2], s.Name(value)) + p[3]
	})

	// Comments last, and they have to be done at all: §11 lists comment
	// POSITIONS as a shape to carry, and it is easy to read that as licence to
	// carry the comments themselves. It is not. `# Küche dunstregelung` sat
	// three lines above a sanitized sensor and named the room it belongs to —
	// the leak gate caught it, on the first pass over a file whose every value
	// had already been replaced.
	//
	// So the position, the indentation and the length survive; the words do
	// not. A comment is a place in the document, and that is the whole of what
	// this fixture needs from it.
	out = wholeLineComment.ReplaceAllStringFunc(out, func(match string) string {
		p := wholeLineComment.FindStringSubmatch(match)
		if strings.TrimSpace(p[3]) == "" {
			return match
		}
		return p[1] + "#" + p[2] + s.Name(p[3])
	})
	return out
}

// unquote strips one layer of YAML quoting for the value being replaced.
func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}

// quoteLike puts the replacement back in the same quoting the original used.
//
// Preserving the quoting is not cosmetic. `name: "Energie Zählerstand"` and
// `name: Energie Zählerstand` are the same value to a parser and different
// bytes to a document-preserving writer — and whether hactl's writes preserve
// them is precisely what findings #20 and #89 are about.
func quoteLike(original, replacement string) string {
	trimmed := strings.TrimSpace(original)
	switch {
	case strings.HasPrefix(trimmed, `"`):
		return `"` + strings.ReplaceAll(replacement, `"`, `'`) + `"`
	case strings.HasPrefix(trimmed, `'`):
		return `'` + strings.ReplaceAll(replacement, `'`, "") + `'`
	default:
		// An unquoted scalar that would now need quoting is quoted rather than
		// emitted broken: a colon or a leading indicator changes what YAML
		// reads, and the fixture has to parse.
		if strings.ContainsAny(replacement, ":#{}[]&*!|>'\"%@`") || replacement == "" {
			return `"` + strings.ReplaceAll(replacement, `"`, `'`) + `"`
		}
		return replacement
	}
}

// ConfigShape is what a config file carries, measured so the generator can
// refuse a derivative that lost a shape rather than shipping a smaller one.
//
// S4: "the generator refuses rather than silently degrading if a shape it
// promised is missing". Counting before and after is how that promise is kept
// honest — it already caught the schedule blocks vanishing.
type ConfigShape struct {
	Lines        int
	TopLevelDash int            // `- ` at column zero: template.yaml's blocks
	BlocksByKey  map[string]int // `- sensor:` vs `- trigger:` vs `- binary_sensor:`
	UniqueIDs    int
	NonASCII     int // lines carrying a byte above ASCII
	LongestLine  int
	Trailing     int // lines with trailing whitespace
	Comments     int
}

var (
	topLevelBlock = regexp.MustCompile(`(?m)^- (\w+):`)
	topLevelDash  = regexp.MustCompile(`(?m)^- `)
)

// MeasureConfig reports the shape of one config file.
func MeasureConfig(src string) ConfigShape {
	shape := ConfigShape{BlocksByKey: map[string]int{}}
	shape.TopLevelDash = len(topLevelDash.FindAllString(src, -1))
	for _, m := range topLevelBlock.FindAllStringSubmatch(src, -1) {
		shape.BlocksByKey[m[1]]++
	}
	shape.UniqueIDs = strings.Count(src, "unique_id:")
	for line := range strings.SplitSeq(src, "\n") {
		shape.Lines++
		if runeLen(line) > shape.LongestLine {
			shape.LongestLine = runeLen(line)
		}
		if len(line) != runeLen(line) {
			shape.NonASCII++
		}
		if strings.TrimRight(line, " \t") != line {
			shape.Trailing++
		}
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			shape.Comments++
		}
	}
	return shape
}

// ShapeDrift names every property that changed between two measurements.
//
// Empty means the derivative carries the source's shape. A non-empty result is
// the generator's refusal: it means the sanitizer did something structural, and
// a structural change is a shape the fixture no longer holds.
func ShapeDrift(before, after ConfigShape) []string {
	var drift []string
	add := func(what string, b, a int) {
		if b != a {
			drift = append(drift, what+": "+itoa(b)+" -> "+itoa(a))
		}
	}
	add("lines", before.Lines, after.Lines)
	add("top-level list items", before.TopLevelDash, after.TopLevelDash)
	add("unique_id count", before.UniqueIDs, after.UniqueIDs)
	add("comment lines", before.Comments, after.Comments)
	add("lines with trailing whitespace", before.Trailing, after.Trailing)

	keys := map[string]bool{}
	for k := range before.BlocksByKey {
		keys[k] = true
	}
	for k := range after.BlocksByKey {
		keys[k] = true
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	for _, k := range ordered {
		add("`- "+k+":` blocks", before.BlocksByKey[k], after.BlocksByKey[k])
	}

	// Non-ASCII may only ever GROW: the sanitizer's replacement names carry
	// non-ASCII back when the source had it, and it can land on a line that had
	// none. Losing it is the failure that matters — finding #14 is a
	// byte-versus-rune cut, and a derivative of pure ASCII cannot express it.
	if after.NonASCII < before.NonASCII {
		drift = append(drift, "lines carrying non-ASCII: "+itoa(before.NonASCII)+" -> "+itoa(after.NonASCII))
	}
	return drift
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
