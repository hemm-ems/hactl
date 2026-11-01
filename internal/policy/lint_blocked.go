package policy

import (
	"slices"
	"strconv"
	"strings"

	"github.com/hemm-ems/hactl/internal/glob"
)

// blockedByPart reports CodeBlockedByPart for profile pr (D-127): one finding
// per (allow, deny) on the allow's line, for the allow's write verbs that the
// deny holds, that apply at the deny's pattern (deniedUnder) and that some
// command asks for at an address at or below the allow and above the deny —
// wherever the two patterns overlap. It needs no live instance: the overlap is
// read from the patterns themselves. Profiles without `default: deny` are
// skipped; their allows are CodeRedundantAllow already.
func blockedByPart(pr *profile) []Finding {
	if pr.deflt != Deny {
		return nil
	}
	var out []Finding
	for i := range pr.rules {
		a := &pr.rules[i]
		if a.Effect != Allow {
			continue
		}
		for j := range pr.rules {
			d := &pr.rules[j]
			if d.Effect != Deny || !partOf(a.addr, d.addr) {
				continue
			}
			var blocked []Verb
			for _, v := range a.Verbs {
				if v != Read && a.appliesHere(v) && slices.Contains(d.Verbs, v) && d.appliesHere(v) && askedBetween(a.addr, d.addr, v) {
					blocked = append(blocked, v)
				}
			}
			if len(blocked) == 0 {
				continue
			}
			names := make([]string, len(blocked))
			for k, v := range blocked {
				names[k] = v.String()
			}
			msg := "never grants " + strings.Join(names, " or ") + " where " + d.headText() + " (line " + strconv.Itoa(d.Line) +
				") covers a part: a part of it is denied, so all of it is"
			if narrower := narrowerDeny(d, blocked); len(narrower) > 0 {
				msg += " — if the deny should only stop changes to the part itself, narrow it to [" + strings.Join(narrower, " ") + "]"
			}
			out = append(out, Finding{Code: CodeBlockedByPart, Profile: pr.name, Rule: a.Text(), Line: a.Line, Message: msg})
		}
	}
	return out
}

// partOf reports whether the deny pattern d sits on a strict descendant of
// what the allow pattern a can address: same kind (or a is `*`), d's sub path
// longer than a's, and every id segment and every one of a's sub segments
// compatible by glob.
func partOf(a, d address) bool {
	if d.any {
		return false
	}
	if a.any {
		return len(d.sub) > 0
	}
	if a.kind != d.kind || len(d.sub) <= len(a.sub) || !idsOverlap(a, d) {
		return false
	}
	for i := range a.sub {
		if !globsOverlap(a.sub[i], d.sub[i]) {
			return false
		}
	}
	return true
}

// idsOverlap reports whether some id could match both patterns' id parts. A
// `**` file segment overlaps anything; otherwise the segments pair up one to
// one.
func idsOverlap(a, d address) bool {
	if slices.Contains(a.id, "**") || slices.Contains(d.id, "**") {
		return true
	}
	if len(a.id) != len(d.id) {
		return false
	}
	for i := range a.id {
		if !globsOverlap(a.id[i], d.id[i]) {
			return false
		}
	}
	return true
}

// globsOverlap reports whether some text could match both globs, case-
// insensitively: a literal must match the other glob; two globs overlap unless
// their literal prefixes (before the first wildcard) or literal suffixes
// (after the last) disagree. It may say yes where no text matches both, never
// no where one does.
func globsOverlap(x, y string) bool {
	switch {
	case !glob.HasWildcard(x):
		return glob.Match(x, y)
	case !glob.HasWildcard(y):
		return glob.Match(y, x)
	}
	x, y = strings.ToLower(x), strings.ToLower(y)
	px, py := x[:strings.IndexAny(x, "*?")], y[:strings.IndexAny(y, "*?")]
	sx, sy := x[strings.LastIndexAny(x, "*?")+1:], y[strings.LastIndexAny(y, "*?")+1:]
	return (strings.HasPrefix(px, py) || strings.HasPrefix(py, px)) &&
		(strings.HasSuffix(sx, sy) || strings.HasSuffix(sy, sx))
}

// askedBetween reports whether some command asks verb v at an address at or
// below the allow pattern a and strictly above the deny pattern d: a row of
// the applicability table for v whose sub shape is no shorter than a's sub
// path, shorter than d's, and compatible with both.
func askedBetween(a, d address, v Verb) bool {
	for _, row := range applicability {
		if row.kind != d.kind || !slices.Contains(row.verbs, v) {
			continue
		}
		if row.id != "" && (len(d.id) == 0 || !glob.Match(row.id, d.id[0])) {
			continue
		}
		if len(row.sub) < len(a.sub) || len(row.sub) >= len(d.sub) {
			continue
		}
		if subShapeCovers(row.sub, d.sub[:len(row.sub)]) && subShapeCovers(row.sub[:len(a.sub)], a.sub) {
			return true
		}
	}
	return false
}

// narrowerDeny is the verb list a deny could be narrowed to so that it stops
// only changes to its part itself (D-127): the verbs it holds that apply at or
// below its pattern, without create and delete (they reach the owner too) and
// without the verbs it blocks. None for a deny on a part no command changes
// directly — `state`, `attr/…`, a file `block/…`.
func narrowerDeny(d *Rule, blocked []Verb) []string {
	var out []string
	for _, v := range applicableUnder(d.addr) {
		if v == Create || v == Delete || slices.Contains(blocked, v) || !slices.Contains(d.Verbs, v) {
			continue
		}
		out = append(out, v.String())
	}
	return out
}
