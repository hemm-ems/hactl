// Package realdata turns a capture of a real Home Assistant instance into a
// fixture that can be committed to a public repository.
//
// # Why it exists
//
// FIXPLAN-livefire.md §1: every fixture in this repository was three flat YAML
// files, so the conditions the 2026-07-30 defects lived in could not occur on
// the rig. The suite was green by construction — `helper show` 404ing on
// storage-backed helpers was untestable while no fixture had a `.storage`, and
// the whole helper family was therefore fixed and verified against a real house
// only. The instance has 220 storage-backed helpers; the rig had none.
//
// A copy of that instance cannot be published, and would not boot if it were.
// So the fixture is a DERIVATIVE: the shapes are carried over at their real
// magnitude and every value that could identify anybody is replaced.
//
// # The two properties, and which one is load-bearing
//
// Shape-preserving and leak-proof pull in opposite directions, and the
// resolution is that they operate on different things. Structure — counts,
// domain distribution, identifier lengths, the presence of non-ASCII, which
// keys a helper carries — is preserved exactly, because that is the entire
// reason the fixture exists. Values in a sensitive position are replaced
// wholesale, because a value that survives is a leak no matter how harmless it
// looked to whoever read it.
//
// Nothing here decides what is "harmless". The replacement is unconditional for
// a position, so a sensitive value that happens to look like a boring one is
// still gone. That is the opposite of a denylist, and deliberately: a denylist
// is a hand-list, and this project's rule is that a hand-list is never the
// enforcement mechanism (see internal/surfaceaudit).
//
// # Determinism, stated precisely
//
// The sanitizer is deterministic on one frozen capture: same input, byte-
// identical output, asserted by running it twice. The CAPTURE is not
// reproducible, because the instance moves — its restored ghosts went from 37
// to 660 in a single day. Conflating the two would make every future
// regeneration look like a defect, so the distinction is stated here rather
// than discovered later.
//
// Determinism comes from hashing each source value, never from a counter over
// a map or a slice: a counter makes the output depend on iteration order, and
// Go randomises that per run.
package realdata

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode"
)

// salt fixes the mapping from a source value to its replacement.
//
// It is a constant rather than a parameter so that regenerating the fixture
// from an unchanged capture produces an unchanged tree, and so that a reviewer
// diffing two regenerations sees only what actually moved on the instance.
const salt = "hactl-realdata-fixture-v1"

// Sanitizer maps values from a captured instance to synthetic replacements.
//
// The zero value is ready to use and holds no state that affects the output —
// the cache exists so that a repeated value costs one hash, not so that the
// answer depends on what was seen first.
type Sanitizer struct {
	cache map[string]string
}

// Name returns the replacement for a human-authored display name.
//
// The shape is carried, not the content. A name that contained non-ASCII gets
// non-ASCII back, because finding #14 was a byte-versus-rune cut and a fixture
// of pure ASCII cannot express it; a long name stays long, because every
// truncation finding (#9 #14 #51 #87) needs something worth truncating.
func (s *Sanitizer) Name(source string) string {
	if strings.TrimSpace(source) == "" {
		return source
	}
	return s.memo("name:"+source, func() string {
		word := pick(nameWords, "name:"+source)
		out := word + " " + pick(nameQualifiers, "qual:"+source)
		if hasNonASCII(source) {
			out = pick(nameNonASCII, "uml:"+source) + " " + out
		}
		return padTo(out, runeLen(source), "name:"+source)
	})
}

// Identifier returns the replacement for a slug: an entity object id, a YAML
// top-level key, a unique_id.
//
// Length is preserved to the rune, because the identifier lengths ARE a shape
// (§11: longest identifier 67 characters against the rig's ~20) and a command
// that formats a table has to meet one. The result is always a legal HA slug.
func (s *Sanitizer) Identifier(source string) string {
	if source == "" {
		return source
	}
	return s.memo("id:"+source, func() string {
		base := pick(idWords, "id:"+source) + "_" + pick(idWords, "id2:"+source)
		return slugPadTo(base, runeLen(source), "id:"+source)
	})
}

// Opaque returns the replacement for a token Home Assistant stores but never
// parses — a `unique_id` above all.
//
// It exists because Identifier does not fit and using it there LOST a shape.
// An entity's object id must be a legal slug, so Identifier flattens to
// [a-z0-9_]; a unique_id has no such rule, and the reference instance has eight
// of them carrying an umlaut (`u2123erppübyxcrdt`). Running those through
// Identifier produced tidy ASCII slugs and quietly deleted the one property
// that made them interesting — an identifier a byte-oriented matcher can cut in
// half, which is what `ref scan`/`ref replace` walk.
//
// So this preserves the source's character classes rather than imposing a
// grammar: length to the rune, and non-ASCII where the source had non-ASCII.
// ShapeDrift is what caught the loss, which is the argument for measuring a
// derivative instead of trusting the transformation that made it.
func (s *Sanitizer) Opaque(source string) string {
	if source == "" {
		return source
	}
	return s.memo("opaque:"+source, func() string {
		base := pick(idWords, "op:"+source) + "_" + pick(idWords, "op2:"+source)
		if hasNonASCII(source) {
			base = strings.ToLower(pick(nameNonASCII, "opuml:"+source)) + "_" + base
		}
		return slugPadTo(base, runeLen(source), "op:"+source)
	})
}

// Icon passes an mdi: icon through unchanged.
//
// An icon name is drawn from Material Design Icons, a fixed public vocabulary,
// so it identifies nobody — and it is worth carrying verbatim because the
// instance's icons include a real typo (`mdi:pressence`), which is exactly the
// kind of value a fixture invented by hand would never contain.
func (s *Sanitizer) Icon(source string) string { return source }

// memo caches by key so a repeated source value maps to one replacement. The
// answer does not depend on the cache: compute is a pure function of the key.
func (s *Sanitizer) memo(key string, compute func() string) string {
	if s.cache == nil {
		s.cache = map[string]string{}
	}
	if got, ok := s.cache[key]; ok {
		return got
	}
	out := compute()
	s.cache[key] = out
	return out
}

// pick selects deterministically from a vocabulary by hashing the key.
//
// The hash is what makes this order-independent. Handing out words by a counter
// would make every value depend on which value was sanitized first, which in Go
// means on map iteration order — so the same capture would produce a different
// fixture on every run and A5 could not hold.
func pick(vocab []string, key string) string {
	return vocab[hashIndex(key, len(vocab))]
}

func hashIndex(key string, n int) int {
	sum := sha256.Sum256([]byte(salt + "\x00" + key))
	return int(binary.BigEndian.Uint64(sum[:8]) % uint64(n)) //nolint:gosec // G115: n is a vocabulary length, and the modulus bounds the result
}

// padTo grows or trims a replacement to match the source's rune length.
//
// Trimming is on a rune boundary, never a byte one: a fixture that carried a
// broken UTF-8 sequence would be testing hactl against a document Home
// Assistant would not have produced.
func padTo(s string, want int, key string) string {
	for runeLen(s) < want {
		s += " " + pick(nameWords, fmt.Sprintf("%s:pad:%d", key, runeLen(s)))
	}
	return trimRunes(s, want)
}

// slugPadTo is padTo for identifiers, keeping the result a legal slug.
func slugPadTo(s string, want int, key string) string {
	for runeLen(s) < want {
		s += "_" + pick(idWords, fmt.Sprintf("%s:pad:%d", key, runeLen(s)))
	}
	s = trimRunes(s, want)
	return strings.Trim(s, "_")
}

func trimRunes(s string, want int) string {
	if want <= 0 || runeLen(s) <= want {
		return s
	}
	runes := []rune(s)
	return strings.TrimRight(string(runes[:want]), " _")
}

func runeLen(s string) int { return len([]rune(s)) }

func hasNonASCII(s string) bool {
	for _, r := range s {
		if r > unicode.MaxASCII {
			return true
		}
	}
	return false
}

// The vocabularies. Every word is generic household or industrial vocabulary
// that belongs to nobody — deliberately not names, not places, not anything a
// person could be recognised by.
var (
	nameWords = []string{
		"Hallway", "Utility", "Workshop", "Pantry", "Landing", "Porch",
		"Cellar", "Loft", "Annex", "Scullery", "Boiler", "Meter",
		"Cistern", "Airlock", "Gantry", "Vestibule",
	}
	nameQualifiers = []string{
		"Sensor", "Setpoint", "Threshold", "Window", "Mode", "Schedule",
		"Limit", "Counter", "Latch", "Interval", "Offset", "Target",
	}
	// Carried so a name that had non-ASCII gets non-ASCII back: umlauts, a
	// degree sign and a euro sign are what the instance's 60 non-ASCII files
	// actually contain (§11).
	nameNonASCII = []string{"Übergabe", "Größe", "Wärme", "Höhe", "Öffnung", "Zähler", "Maß", "Süd"}
	idWords      = []string{
		"hallway", "utility", "workshop", "pantry", "landing", "porch",
		"cellar", "loft", "annex", "scullery", "boiler", "meter",
		"cistern", "airlock", "gantry", "vestibule", "setpoint", "threshold",
		"window", "mode", "schedule", "limit", "counter", "latch",
	}
)
