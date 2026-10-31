// Package glob is hactl's one glob engine: the matcher behind every
// `--pattern` filter and behind every policy rule (D-2, D-28; internal/policy
// doc). One matcher, never two — a filter and an allow that disagree about what
// `light.*` means is a policy that lies about what it grants.
//
// The engine is deliberately small: `*` is any run of characters (including
// none), `?` exactly one byte, every other byte is literal. There is no
// character class, no escape and no brace expansion. Case is ignored (D-2).
//
// Two entry points share the engine:
//
//   - Match anchors the pattern at both ends of s.
//   - MatchQualified also tries the part of s after its first dot — D-28's
//     second anchor, for qualified `<domain>.<name>` identifiers.
//
// A pattern without a wildcard is an exact, case-insensitive comparison under
// both. The substring convenience `--pattern` offers for a wildcard-free
// pattern is the filter's own (internal/cmd matchPattern) and is not here: a
// policy allow must never err towards more rights.
package glob

import "strings"

// Match reports whether s matches pattern, case-insensitively, anchored at both
// ends.
func Match(s, pattern string) bool {
	return match(strings.ToLower(s), strings.ToLower(pattern))
}

// MatchQualified reports whether s matches pattern at s itself or at the part
// of s after its first dot (D-28). A pattern carrying a dot keeps its domain
// selecting power: the tail of `binary_sensor.door` is `door`, which
// `sensor.*` does not match.
func MatchQualified(s, pattern string) bool {
	s, pattern = strings.ToLower(s), strings.ToLower(pattern)
	if match(s, pattern) {
		return true
	}
	if _, tail, found := strings.Cut(s, "."); found {
		return match(tail, pattern)
	}
	return false
}

// HasWildcard reports whether pattern contains a glob metacharacter.
func HasWildcard(pattern string) bool { return strings.ContainsAny(pattern, "*?") }

// match is the engine. Both arguments are already case-folded.
func match(s, pattern string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			// Skip consecutive stars
			for len(pattern) > 0 && pattern[0] == '*' {
				pattern = pattern[1:]
			}
			if len(pattern) == 0 {
				return true
			}
			for i := range len(s) + 1 {
				if match(s[i:], pattern) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			s = s[1:]
			pattern = pattern[1:]
		default:
			if len(s) == 0 || s[0] != pattern[0] {
				return false
			}
			s = s[1:]
			pattern = pattern[1:]
		}
	}
	return len(s) == 0
}
