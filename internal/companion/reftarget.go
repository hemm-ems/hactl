package companion

import (
	"fmt"
	"unicode"
	"unicode/utf8"
)

// InvalidRefTargetError is a target the ref routes cannot match as a whole
// token. It is its own type so a caller can tell it apart from a transport
// failure: an unreachable companion makes an answer PARTIAL, while a target
// like "." makes the question meaningless, and absorbing the second into the
// first would report "config files could not be scanned" for a typo.
type InvalidRefTargetError struct{ Target string }

func (e *InvalidRefTargetError) Error() string {
	return fmt.Sprintf("%q cannot be matched as a whole token: the config scan matches the target with a word "+
		"boundary at each end, so a target that does not START and END on a letter, digit or underscore is "+
		"matched only where those boundaries bind to the surrounding text instead — \".\" matches the dot "+
		"inside every entity_id and every service name (2747 hits on a real instance), which is not a "+
		"reference to anything. Scan an entity_id, or for term discovery: hactl ent ls --pattern '*<term>*'",
		e.Target)
}

// ValidRefTarget reports whether target can be matched as a whole token by the
// companion's ref routes.
//
// The companion builds `\b` + re.escape(target) + `\b` and its own docstring
// states the premise that makes those boundaries mean "whole token": *every
// entity_id-shaped target starts and ends on a word character*. When it does
// not, each `\b` binds to the neighbouring character instead, so the pattern
// asks the opposite question — `\b\.\b` matches a dot that HAS word characters
// on both sides, i.e. the interior of `light.turn_on` and of every id in the
// tree.
//
// So the rule is exactly that premise, and it is about the FIRST and LAST rune
// only: "Wozi TV" is a legitimate target (dashboards match whole string values,
// and a display name is a real thing to search for), "sensor.a-b" is a
// legitimate id-shaped one, and both `\b`s land where the caller means. A
// non-ASCII letter is a word character to Python's `\b` too, so "Küche" is
// accepted rather than refused for being unfamiliar.
func ValidRefTarget(target string) bool {
	if target == "" {
		return false
	}
	first, _ := utf8.DecodeRuneInString(target)
	last, _ := utf8.DecodeLastRuneInString(target)
	return isWordRune(first) && isWordRune(last)
}

// isWordRune mirrors what `\b` treats as a word character on a Python str
// pattern: letters, digits and the underscore, unicode included.
func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// RefTargetError is ValidRefTarget as a gate. It is called by the client — so
// no caller of the routes can bypass it — and by the commands, so the refusal
// arrives before hactl connects to anything. One rule at two sites; never two
// rules that can drift apart.
func RefTargetError(target string) error {
	if ValidRefTarget(target) {
		return nil
	}
	return &InvalidRefTargetError{Target: target}
}
