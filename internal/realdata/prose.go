package realdata

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// ---------------------------------------------------------------------------
// Free text
// ---------------------------------------------------------------------------
//
// A `name:` is one value and can be replaced wholesale. A `description:` is a
// paragraph somebody wrote, and on the reference instance those paragraphs name
// the people in the house: four first names appear inside automation
// descriptions, one of them in a sentence about which automation replaced which.
// The leak gate found all four on the first pass over a file whose every name,
// alias and entity reference had already been sanitized.
//
// Replacing the paragraph would be the obvious move and it would delete a shape
// the fixture exists to carry. A description wraps across lines; YAML folds it;
// `auto cat` has to render it and `auto apply` has to write it back. So the
// words are replaced ONE AT A TIME, each by a synthetic word of the same rune
// length, case and non-ASCII — the line count, the line lengths, the wrapping,
// the indentation, the quoting and the punctuation all survive exactly, and the
// content does not.
//
// Three kinds of span are carried through untouched, because substituting
// inside them would change what the document DOES rather than what it says:
//
//   - a Jinja expression, which may itself wrap across lines;
//   - an entity reference, which the entity pass has already rewritten and
//     which every other reference to the same entity has to keep agreeing with;
//   - a backslash escape, because `\t` is one token and rewriting the `t` makes
//     it an invalid escape in a double-quoted scalar.
//
// Digits are replaced too. A free-text field is where a house number or a phone
// number ends up, and no positional rule can find one there.

// preservedSpan matches the parts of a free-text value that are not prose,
// followed by the two things that are: a run of letters and a run of digits.
//
// Go's alternation is leftmost-first, so the preserved forms have to come
// first: at the `s` of `sensor.x` both the entity-reference branch and the
// letter branch match, and only the earlier one keeps the reference whole.
var preservedSpan = regexp.MustCompile(`(?s)\{\{.*?\}\}|\{%.*?%\}|\{#.*?#\}|\\.|` +
	`\b(?:` + strings.Join(entityDomains, "|") + `)\.[a-z0-9_]+\b|` +
	`\p{L}+|\p{Nd}+`)

// proseText substitutes every word and every number outside a preserved span.
func (s *Sanitizer) proseText(text string) string {
	return preservedSpan.ReplaceAllStringFunc(text, func(match string) string {
		switch {
		case match[0] == '{' || match[0] == '\\', strings.Contains(match, "."):
			return match
		case match[0] >= '0' && match[0] <= '9':
			return s.proseDigits(match)
		}
		return s.proseWord(match)
	})
}

// proseWord returns a synthetic word carrying the source word's rune length,
// case shape and non-ASCII positions.
func (s *Sanitizer) proseWord(source string) string {
	return s.memo("prose:"+source, func() string {
		runes := []rune(source)
		base := pick(proseWords, "prose:"+source)
		for runeLen(base) < len(runes) {
			base += pick(proseWords, fmt.Sprintf("prose:%s:%d", source, runeLen(base)))
		}
		out := []rune(base)[:len(runes)]
		// Non-ASCII goes back where the source had it, rune index for rune
		// index: the fixture's reason to carry German text at all is that
		// finding #14 was a byte-versus-rune cut, and a cut lands where the
		// multi-byte character is.
		nonASCII := []rune(proseNonASCII)
		allUpper := true
		for i, r := range runes {
			if unicode.IsLower(r) {
				allUpper = false
			}
			if r > unicode.MaxASCII {
				out[i] = nonASCII[hashIndex(fmt.Sprintf("uml:%s:%d", source, i), len(nonASCII))]
			}
		}
		// Case shape, and it has to keep the rune length: a replacement one rune
		// longer moves every line boundary after it inside a wrapped scalar, and
		// the document still parses, so nothing would say so. Go's ToUpper maps
		// rune to rune (`ß` is left alone rather than expanded to `SS`, which is
		// Unicode's full case mapping and not what strings.ToUpper does), and
		// TestProseSubstitutionIsLengthForLengthInEveryCase is what keeps that
		// true rather than this comment.
		switch {
		case allUpper && len(runes) > 1:
			return strings.ToUpper(string(out))
		case unicode.IsUpper(runes[0]):
			out[0] = unicode.ToUpper(out[0])
		}
		return string(out)
	})
}

// proseDigits replaces a run of digits with another of the same length.
func (s *Sanitizer) proseDigits(source string) string {
	return s.memo("digits:"+source, func() string {
		var out strings.Builder
		for i := range len(source) {
			out.WriteString(strconv.Itoa(hashIndex(fmt.Sprintf("digit:%s:%d", source, i), 10)))
		}
		return out.String()
	})
}

// The prose vocabulary is separate from the name vocabulary and lower case:
// these words are substituted mid-sentence, where a capitalised noun would read
// as a proper name — which is the one thing this file exists to remove.
var (
	proseWords = []string{
		"lamp", "vent", "duct", "pipe", "valve", "relay", "cable", "panel",
		"hatch", "gauge", "motor", "pump", "fuse", "hinge", "brack", "spool",
		"clamp", "shelf", "grate", "joint", "plate", "screw", "washer", "bolt",
	}
	proseNonASCII = "äöüßéÄÖÜ"
)
