package realdata

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
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
//   - the value of a `name:` / `friendly_name:` / `alias:` / `title:` key;
//   - the value of a `unique_id:` key;
//   - the words and numbers of a free-text value — a `description:` above all,
//     which is where the reference instance's automations name the people who
//     live there. See prose.go.
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
	entityRef = regexp.MustCompile(`\b(` + strings.Join(entityDomains, "|") + `)\.([a-z0-9_]+)\b`)
	// One `key: value` line, split into the parts that have to be put back
	// exactly: the indentation and any `- `, the key, the space after the colon,
	// the value, and the trailing whitespace. The last of those is not
	// cosmetic — 117 of the reference instance's 169 files carry trailing
	// whitespace and ShapeDrift counts it.
	valueLine = regexp.MustCompile(`^([ \t]*(?:-[ \t]+)?)([A-Za-z_]+):([ \t]*)(.*?)([ \t]*)$`)
	// A whole-line comment only. A `#` after a value is far more often inside a
	// template or a quoted string than it is a comment, and rewriting one of
	// those would change what the document MEANS rather than what it says.
	wholeLineComment = regexp.MustCompile(`(?m)^([ \t]*)#([ \t]*)([^\n]*)$`)
)

// valueKind is how a key's value has to be replaced.
type valueKind int

const (
	kindName   valueKind = iota // a display name: replaced wholesale, rune length kept
	kindOpaque                  // a token HA stores and never parses: a unique_id
	kindProse                   // free text: replaced word by word (see prose.go)
)

// sensitiveValueKeys are the keys whose value identifies somebody, and which
// replacement each one needs.
//
// This is a list of POSITIONS rather than of values, the same rule the leak
// gate is built on: what sits at one of these keys is replaced whether or not
// it looks like it matters.
var sensitiveValueKeys = map[string]valueKind{
	"name":          kindName,
	"friendly_name": kindName,
	"alias":         kindName,
	"title":         kindName,
	"unique_id":     kindOpaque,
	"description":   kindProse,
	"message":       kindProse,
	"option":        kindProse,
	"effect":        kindProse,
}

// SanitizeConfigText rewrites one config file's identifying values, leaving
// every byte that is not one of them exactly where it was.
func SanitizeConfigText(src string, s *Sanitizer) string {
	out := entityRef.ReplaceAllStringFunc(src, func(match string) string {
		parts := entityRef.FindStringSubmatch(match)
		return parts[1] + "." + s.Identifier(parts[2])
	})

	out = sanitizeValues(out, s)

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

// sanitizeValues walks the document a line at a time, because a YAML scalar is
// not a line.
//
// A regular expression over `key: value` was what this used to be, and it wrote
// a file Home Assistant refused to parse. The reference instance has aliases
// long enough that the emitter wrapped them, so `alias: 'Übergabe ...` continued
// on the next line — the pattern replaced the first line's value with a shorter
// synthetic name and left `  auf '` behind as a dangling scalar. Nothing caught
// it: the count-based shape checks all passed, and the file was 9,600 lines
// long. StructureDrift exists because of that morning.
func sanitizeValues(src string, s *Sanitizer) string {
	lines := strings.Split(src, "\n")
	out := make([]string, 0, len(lines))
	for i := 0; i < len(lines); {
		m := valueLine.FindStringSubmatch(lines[i])
		kind, sensitive := valueKind(-1), false
		if m != nil {
			kind, sensitive = sensitiveValueKeys[m[2]]
		}
		if !sensitive || m[4] == "" {
			out = append(out, lines[i])
			i++
			continue
		}
		head, value, trailing := m[1]+m[2]+":"+m[3], m[4], m[5]

		// A scalar that wraps. Word-by-word substitution is the only rewrite
		// that can touch it without moving a line boundary, so a wrapped name
		// is treated as prose regardless of its key.
		if end := scalarEnd(lines, i+1, len(m[1]), value); end > i+1 {
			body := strings.Join(append([]string{value + trailing}, lines[i+1:end]...), "\n")
			out = append(out, strings.Split(head+s.proseText(body), "\n")...)
			i = end
			continue
		}
		out = append(out, head+s.replaceValue(kind, value)+trailing)
		i++
	}
	return strings.Join(out, "\n")
}

// replaceValue rewrites one single-line value according to its key's kind.
func (s *Sanitizer) replaceValue(kind valueKind, value string) string {
	bare := unquote(value)
	switch {
	case kind == kindOpaque:
		// Opaque, not Identifier: Home Assistant never parses a unique_id, and
		// eight of the reference instance's carry an umlaut that Identifier's
		// slug grammar would delete.
		return quoteLike(value, s.Opaque(bare))
	case kind == kindProse:
		return s.proseText(value)
	case strings.HasPrefix(bare, ">") || strings.HasPrefix(bare, "|"):
		// A block scalar indicator is structure, not a name. Its content is on
		// the following lines and the wrapped branch above has it.
		return value
	case strings.Contains(bare, "{{") || strings.Contains(bare, "{%"):
		// A name that is a template: the expression is what the entity pass
		// already rewrote and has to keep, the words around it are not.
		return s.proseText(value)
	}
	return quoteLike(value, s.Name(bare))
}

// scalarEnd returns the index one past the last continuation line of the scalar
// that starts with value on the key's line.
//
// Indentation alone is not the rule, and reading it as the rule cost a
// regeneration: a `unique_id:` in template.yaml is followed by a blank line and
// two more-indented COMMENT lines, so an indentation-only scan swallowed the
// comments into the value and replaced them. What a following line means
// depends on the scalar's style, so that is what this branches on.
func scalarEnd(lines []string, from, column int, value string) int {
	switch {
	case strings.HasPrefix(value, "|"), strings.HasPrefix(value, ">"):
		// A block scalar owns everything more indented than its key, `#` lines
		// included: inside one, a `#` is content.
		end := from
		for j := from; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) != "" && indentOf(lines[j]) <= column {
				break
			}
			end = j + 1
		}
		return trimBlank(lines, from, end)
	case value[0] == '\'', value[0] == '"':
		// A quoted scalar ends where its quote closes, wherever that is. Nothing
		// in between is a comment, and a blank line in the middle is a
		// paragraph break the fixture is carrying on purpose.
		if quoteCloses(value) {
			return from
		}
		for j := from; j < len(lines); j++ {
			if strings.TrimSpace(lines[j]) != "" && indentOf(lines[j]) <= column {
				break // unterminated; leave it alone and let StructureDrift judge
			}
			if quoteCloses(value[:1] + strings.TrimSpace(lines[j])) {
				return j + 1
			}
		}
		return from
	}
	// A plain scalar. A comment or a blank line ends it as far as this is
	// concerned — under-reaching here means a tail goes unsanitized, which the
	// leak gate reports, and over-reaching means rewriting something that was
	// never part of the value, which nothing would report.
	end := from
	for j := from; j < len(lines); j++ {
		text := strings.TrimSpace(lines[j])
		if text == "" || strings.HasPrefix(text, "#") || indentOf(lines[j]) <= column {
			break
		}
		end = j + 1
	}
	return end
}

// trimBlank drops trailing blank lines from a block scalar's span: they are as
// much the next node's separator as this one's content, and leaving them out
// keeps the substitution off a line it has nothing to do with.
func trimBlank(lines []string, from, end int) int {
	for end > from && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return end
}

// quoteCloses reports whether a quoted scalar beginning at s[0] is terminated
// within s, honouring YAML's two escapes: `''` inside single quotes and a
// backslash inside double quotes.
func quoteCloses(s string) bool {
	quote := s[0]
	for i := 1; i < len(s); i++ {
		switch {
		case quote == '"' && s[i] == '\\':
			i++
		case s[i] != quote:
		case quote == '\'' && i+1 < len(s) && s[i+1] == '\'':
			i++
		default:
			return true
		}
	}
	return false
}

// indentOf is the column the first non-blank byte of a line sits at.
func indentOf(line string) int {
	for i, r := range line {
		if r != ' ' && r != '\t' {
			return i
		}
	}
	return len(line)
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

// StructureDrift reports how the derivative's document differs from the
// source's, as Home Assistant's own parser would see them.
//
// ShapeDrift counts things in the TEXT, and every one of its counts agreed on
// the run that produced a file HA refused to load: the line count matched, the
// list-item count matched, the trailing-whitespace count matched, and a
// replacement had still left a wrapped scalar's second line dangling. Nothing
// short of parsing can tell you that, so this parses.
//
// Values are expected to differ — replacing them is the point. Keys, nesting
// and the number of children are not.
func StructureDrift(before, after string) []string {
	var source, derivative yaml.Node
	if err := yaml.Unmarshal([]byte(before), &source); err != nil {
		return []string{"the SOURCE is not valid YAML: " + err.Error()}
	}
	if err := yaml.Unmarshal([]byte(after), &derivative); err != nil {
		return []string{"the derivative is not valid YAML: " + err.Error()}
	}
	var drift []string
	compareNodes(&source, &derivative, "$", &drift)
	return drift
}

// maxStructureDrift bounds the report. A derivative that has drifted at three
// places has drifted; printing the other nine hundred helps nobody.
const maxStructureDrift = 3

func compareNodes(before, after *yaml.Node, path string, drift *[]string) {
	if len(*drift) >= maxStructureDrift {
		return
	}
	switch {
	case before.Kind != after.Kind:
		*drift = append(*drift, fmt.Sprintf("%s: %s became %s", path, nodeKind(before), nodeKind(after)))
		return
	case len(before.Content) != len(after.Content):
		*drift = append(*drift, fmt.Sprintf("%s: %d children became %d", path, len(before.Content), len(after.Content)))
		return
	}
	for i := range before.Content {
		child := fmt.Sprintf("%s[%d]", path, i)
		if before.Kind == yaml.MappingNode && i%2 == 0 {
			if before.Content[i].Value != after.Content[i].Value {
				*drift = append(*drift, fmt.Sprintf("%s: the key %q became %q",
					path, before.Content[i].Value, after.Content[i].Value))
				continue
			}
			child = path + "." + before.Content[i].Value
		}
		compareNodes(before.Content[i], after.Content[i], child, drift)
	}
}

func nodeKind(n *yaml.Node) string {
	switch n.Kind {
	case yaml.DocumentNode:
		return "a document"
	case yaml.SequenceNode:
		return "a list"
	case yaml.MappingNode:
		return "a mapping"
	case yaml.ScalarNode:
		return "a scalar"
	case yaml.AliasNode:
		return "an alias"
	}
	return "nothing"
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
