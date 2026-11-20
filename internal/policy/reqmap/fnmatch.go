package reqmap

import (
	"slices"
	"strings"
)

// Home Assistant's entity globs (D-192): recorder.purge_entities'
// entity_globs, like every entity filter, are compiled by
// homeassistant.helpers.entityfilter._convert_globs_to_pattern —
// `re.compile(fnmatch.translate(glob))` with no flags, matched from the
// start, so a glob matches an entity id exactly as Python's
// fnmatch.fnmatchcase does: case-sensitive, `*` any run, `?` one character,
// `[seq]` / `[!seq]` a set with Python's own reading of `]`, `-` and an
// unclosed `[`. fnmatchCase is that function, ported from CPython's
// fnmatch._translate; testdata/policy/services/entity_globs.json holds the
// vectors recorded from Home Assistant's own filter.

type globItem struct {
	kind byte // 'l' literal, '?' any, '*' star, '[' set, '!' never
	lit  rune
	set  []globRange
	neg  bool
}

type globRange struct{ lo, hi rune }

// fnmatchCase reports whether name matches pattern as Python's
// fnmatch.fnmatchcase does. ok is false when Python's translate or re would
// raise on the pattern: Home Assistant then fails the call, and the caller
// decides what to assume.
func fnmatchCase(pattern, name string) (match, ok bool) {
	items, ok := translateGlob([]rune(pattern))
	if !ok {
		return false, false
	}
	return globMatch(items, []rune(name)), true
}

// translateGlob is CPython 3.13's fnmatch._translate, producing items rather
// than a regular expression.
func translateGlob(pat []rune) (items []globItem, ok bool) {
	defer func() {
		if recover() != nil {
			items, ok = nil, false
		}
	}()
	n := len(pat)
	i := 0
	for i < n {
		c := pat[i]
		i++
		switch c {
		case '*':
			if len(items) == 0 || items[len(items)-1].kind != '*' {
				items = append(items, globItem{kind: '*'})
			}
			for i < n && pat[i] == '*' {
				i++
			}
		case '?':
			items = append(items, globItem{kind: '?'})
		case '[':
			item, next, ok := translateClass(pat, i)
			if !ok {
				return nil, false
			}
			items = append(items, item)
			i = next
		default:
			items = append(items, globItem{kind: 'l', lit: c})
		}
	}
	return items, true
}

// translateClass reads the `[…]` whose `[` is just before pat[i], as
// _translate does: an unclosed `[` is the literal `[`. It answers the item
// and the index after it.
func translateClass(pat []rune, i int) (globItem, int, bool) {
	n := len(pat)
	j := i
	if j < n && pat[j] == '!' {
		j++
	}
	if j < n && pat[j] == ']' {
		j++
	}
	for j < n && pat[j] != ']' {
		j++
	}
	if j >= n {
		return globItem{kind: 'l', lit: '['}, i, true
	}
	var stuff string
	if !slices.Contains(pat[i:j], '-') {
		stuff = strings.ReplaceAll(string(pat[i:j]), `\`, `\\`)
	} else {
		stuff = classWithRanges(pat, i, j)
	}
	// Escape set operations (&&, ~~ and ||).
	var b strings.Builder
	for _, r := range stuff {
		if r == '&' || r == '~' || r == '|' {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	stuff = b.String()
	switch stuff {
	case "":
		return globItem{kind: '!'}, j + 1, true
	case "!":
		return globItem{kind: '?'}, j + 1, true
	}
	neg := false
	switch stuff[0] {
	case '!':
		neg, stuff = true, stuff[1:]
	case '^', '[':
		stuff = `\` + stuff
	}
	set, ok := parseClass([]rune(stuff))
	if !ok {
		return globItem{}, 0, false
	}
	return globItem{kind: '[', set: set, neg: neg}, j + 1, true
}

// classWithRanges is _translate's reading of a set holding `-`: split into
// chunks at the hyphens that can form a range, drop empty ranges, escape the
// rest.
func classWithRanges(pat []rune, i, j int) string {
	var chunks []string
	k := i + 1
	if pat[i] == '!' {
		k = i + 2
	}
	for {
		k = indexRune(pat, '-', k, j)
		if k < 0 {
			break
		}
		chunks = append(chunks, string(pat[i:k]))
		i = k + 1
		k += 3
	}
	if chunk := string(pat[i:j]); chunk != "" {
		chunks = append(chunks, chunk)
	} else {
		chunks[len(chunks)-1] += "-"
	}
	// Remove empty ranges — invalid in a regular expression.
	for k := len(chunks) - 1; k > 0; k-- {
		prev, cur := []rune(chunks[k-1]), []rune(chunks[k])
		if prev[len(prev)-1] > cur[0] {
			chunks[k-1] = string(prev[:len(prev)-1]) + string(cur[1:])
			chunks = append(chunks[:k], chunks[k+1:]...)
		}
	}
	parts := make([]string, len(chunks))
	for k, c := range chunks {
		parts[k] = strings.ReplaceAll(strings.ReplaceAll(c, `\`, `\\`), "-", `\-`)
	}
	return strings.Join(parts, "-")
}

// parseClass reads the inside of a Python regular-expression character class
// as translate built it: `\x` is the literal x, `a-b` a range, a `-` that
// cannot start a range a literal. ok is false where Python's re would raise
// (a reversed range).
func parseClass(s []rune) ([]globRange, bool) {
	var out []globRange
	item := func(i int) (rune, int) {
		if s[i] == '\\' && i+1 < len(s) {
			return s[i+1], i + 2
		}
		return s[i], i + 1
	}
	i := 0
	for i < len(s) {
		lo, next := item(i)
		if next < len(s)-1 && s[next] == '-' {
			hi, after := item(next + 1)
			if hi < lo {
				return nil, false
			}
			out = append(out, globRange{lo, hi})
			i = after
			continue
		}
		out = append(out, globRange{lo, lo})
		i = next
	}
	return out, true
}

func (g globItem) matches(r rune) bool {
	switch g.kind {
	case 'l':
		return r == g.lit
	case '?':
		return true
	case '[':
		in := false
		for _, rg := range g.set {
			if r >= rg.lo && r <= rg.hi {
				in = true
				break
			}
		}
		return in != g.neg
	}
	return false
}

// globMatch is a full match of items against name.
func globMatch(items []globItem, name []rune) bool {
	// dp[j]: items[:i] match name[:j].
	dp := make([]bool, len(name)+1)
	dp[0] = true
	for _, it := range items {
		next := make([]bool, len(name)+1)
		if it.kind == '*' {
			seen := false
			for j := 0; j <= len(name); j++ {
				seen = seen || dp[j]
				next[j] = seen
			}
		} else {
			for j := 1; j <= len(name); j++ {
				next[j] = dp[j-1] && it.matches(name[j-1])
			}
		}
		dp = next
	}
	return dp[len(name)]
}

// indexRune is Python's str.find(r, from, to) on a rune slice.
func indexRune(s []rune, r rune, from, to int) int {
	for k := from; k < to && k < len(s); k++ {
		if k >= 0 && s[k] == r {
			return k
		}
	}
	return -1
}
