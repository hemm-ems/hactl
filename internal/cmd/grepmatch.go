package cmd

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// grepMinLiteralRunes is the shortest literal term `grep` searches for, counted
// in runes (D-49, oracle resolution 3). On the reference house a 1-character
// term matched 62 % (`a`) and 75 % (`e`) of every string leaf; from 2 up length
// does not predict a flood (`ab` 277 leaves, `licht` 627, `on` 1,769), so there
// is no further rule and the per-half cap handles the rest. The companion
// enforces the same number (MIN_LITERAL_LEN); a regex is not length-gated.
const grepMinLiteralRunes = 2

// grepTermError is a term or pattern `grep` will not search for. It is the
// caller's input, refused before anything is contacted — never a source that
// went unread.
type grepTermError struct{ msg string }

func (e *grepTermError) Error() string { return e.msg }

// errGrepTermRefused lets a caller tell a term refusal from a transport
// failure without matching on wording: errors.Is(err, errGrepTermRefused).
var errGrepTermRefused = errors.New("grep term refused")

// Is reports every grepTermError as errGrepTermRefused.
func (e *grepTermError) Is(target error) bool { return target == errGrepTermRefused }

func grepRefusal(format string, args ...any) error {
	return &grepTermError{msg: fmt.Sprintf(format, args...)}
}

// grepMatch is a compiled `grep` predicate: the one question both halves of the
// search answer, asked of one string by the dashboard half in Go. The config
// half asks the companion the same question in Python (D-49).
type grepMatch func(value string) bool

// validateGrepTerm refuses a term `grep` will not search for and returns the
// dashboard half's predicate for the one it will. It contacts nothing, so every
// refusal it makes costs no connection (AC-5, AC-6).
func validateGrepTerm(term string, regex, caseSensitive bool) (grepMatch, error) {
	if strings.TrimSpace(term) == "" {
		return nil, grepRefusal("grep needs a term: an empty or whitespace-only term matches everything and answers nothing")
	}
	if !regex {
		if n := utf8.RuneCountInString(term); n < grepMinLiteralRunes {
			return nil, grepRefusal("term %q is %d character long; grep needs at least %d — a 1-character term "+
				"matches most of any real config (use a longer term, or --regex for a deliberate short pattern)",
				term, n, grepMinLiteralRunes)
		}
		return literalGrepMatch(term, caseSensitive), nil
	}
	re, err := validateGrepRegex(term, caseSensitive)
	if err != nil {
		return nil, err
	}
	return regexGrepMatch(re), nil
}

// grepRegex is a compiled --regex pattern and how the dashboard half has to
// present a value to it so Go answers the question Python answers.
type grepRegex struct {
	re *regexp.Regexp
	// pythonFold: the pattern runs case-insensitively, so values (and the
	// pattern, already) are mapped through grepPythonFold before matching.
	pythonFold bool
}

// grepPythonFold closes the one gap between Go's (?i) and Python's
// re.IGNORECASE: Python treats four groups of characters as one case class
// where Go sees several. Go's (?i) folds a character to the members of its
// unicode.SimpleFold orbit; Python's IGNORECASE matches when the simple
// lowercase of the two agree, or through its own extra-case table
// (re._casefix). Measured over every code point (Python 3.12 `re`, the
// companion's base image, and Go 1.26, both Unicode 15.0): every Go orbit lies
// inside one Python class, Python's relation is an equivalence, and exactly
// these four classes are wider than a Go orbit —
//
//	{I, i, İ (U+0130), ı (U+0131)}     `istanbul` finds `İstanbul` in Python only
//	{ΐ (U+0390), ΐ (U+1FD3)}
//	{ΰ (U+03B0), ΰ (U+1FE3)}
//	{ﬅ (U+FB05), ﬆ (U+FB06)}
//
// Mapping each character outside its class's Go orbit onto a member of that
// orbit — in the value and in the pattern alike — makes Go's (?i) answer
// Python's question for every literal and every class member. A class range
// keeps its endpoints and gains the mapped member instead; see
// grepPythonFoldPattern.
func grepPythonFold(r rune) rune {
	switch r {
	case 0x130, 0x131: // İ, ı → i
		return 'i'
	case 0x1fd3: // ΐ → ΐ
		return 0x390
	case 0x1fe3: // ΰ → ΰ
		return 0x3b0
	case 0xfb06: // ﬆ → ﬅ
		return 0xfb05
	}
	return r
}

// grepPythonFoldRemapped is every character grepPythonFold moves.
var grepPythonFoldRemapped = []rune{0x130, 0x131, 0x1fd3, 0x1fe3, 0xfb06}

// literalGrepMatch is D-49's substring rule. strings.ToLower maps one rune to
// one rune by unicode.ToLower — the simple Unicode lowercase mapping the
// companion's simple_lower applies on the config half, so `Küche` finds `KÜCHE`,
// `istanbul` finds `İstanbul`, and `straße` does not find `STRASSE` (no case
// folding, which has no Go twin for `ß`→`ss`).
func literalGrepMatch(term string, caseSensitive bool) grepMatch {
	if caseSensitive {
		return func(v string) bool { return strings.Contains(v, term) }
	}
	needle := strings.ToLower(term)
	return func(v string) bool { return strings.Contains(strings.ToLower(v), needle) }
}

// regexGrepMatch reads `$` the way the companion's Python does.
//
// Without (?m), Python's `$` matches at the end of the string OR just before a
// final newline; Go's matches only at the end. A block scalar keeps its trailing
// newline (78 of 7,406 string leaves on the reference house), so `}}$` found a
// template body in config and missed the same text in a dashboard. Trying the
// value once more without that one newline gives Go's `$` the position Python's
// also accepts; within the subset validateGrepRegex admits, every other
// construct answers the same on both forms of the value (oracle resolution 4).
//
// Under a case-insensitive pattern the value is first mapped through
// grepPythonFold, so `istanbul` finds `İstanbul` here as it does in Python.
func regexGrepMatch(g *grepRegex) grepMatch {
	re := g.re
	return func(v string) bool {
		if g.pythonFold {
			v = strings.Map(grepPythonFold, v)
		}
		if re.MatchString(v) {
			return true
		}
		trimmed, hadNewline := strings.CutSuffix(v, "\n")
		return hadNewline && re.MatchString(trimmed)
	}
}

// validateGrepRegex compiles a --regex pattern for the dashboard half and
// refuses anything outside the common subset of Python 3.12 `re` (the config
// half, on the companion) and Go `regexp` (the dashboard half, here).
//
// The subset is measured, not assumed: 56 constructs probed on both engines
// (oracle resolution 4, SPEC-global-grep.md). Three kinds of pattern are
// refused, all before any wire call:
//
//   - what RE2 cannot compile. That covers the Python-only set — lookaround,
//     backreferences, possessive and atomic groups, `\Z`, `\u`, repeat counts
//     above 1000 — and is the parser's own message.
//   - what only Go accepts, so the dashboard half would answer a question the
//     config half refuses: `\z`, `\p{..}`, `\Q…\E`, `\x{..}`, `(?<name>…)`,
//     `(?U)`, a global flag group that is not at the start or negates.
//   - what both accept and read differently: `\w \W \d \D \s \S \b \B` (ASCII in
//     Go, Unicode in Python), a digit escape (`\12` is octal in Go and a
//     backreference in Python), an unescaped `{,` (`a{,3}` is 0..3 in Python and
//     literal in Go), and `[`, `--`, `&&`, `~~`, `||` inside a class (`[[:alpha:]]`
//     is a POSIX class in Go and a reserved nested set in Python).
//
// Case-insensitivity is where the two engines' readings part for a handful of
// characters, which the dashboard half closes by mapping value and pattern
// through grepPythonFold. That is only right for a pattern in one case mode,
// so a pattern that switches case-sensitivity for part of itself is refused
// too (`(?-i:…)` under the default, `(?i:…)` with --case-sensitive).
//
// A pattern that matches the empty string is refused too: it matches every
// value in both halves, which is a flood rather than an answer.
func validateGrepRegex(pattern string, caseSensitive bool) (*grepRegex, error) {
	if _, err := regexp.Compile(pattern); err != nil {
		return nil, grepRefusal("invalid --regex: %v", err)
	}
	insensitive, err := grepRegexSubsetError(pattern, caseSensitive)
	if err != nil {
		return nil, grepRefusal("--regex %q is outside what both halves of grep read the same way: %v "+
			"(see `hactl rtfm`, \"Term search\")", pattern, err)
	}
	src := pattern
	if insensitive {
		src = grepPythonFoldPattern(src)
	}
	if !caseSensitive {
		src = "(?i)" + src
	}
	re, err := regexp.Compile(src)
	if err != nil {
		return nil, grepRefusal("invalid --regex: %v", err)
	}
	if re.MatchString("") {
		return nil, grepRefusal("--regex %q matches the empty string, so it matches every value; "+
			"anchor it to some text", pattern)
	}
	return &grepRegex{re: re, pythonFold: insensitive}, nil
}

// grepRegexSubsetError scans a pattern RE2 has already compiled for the
// constructs validateGrepRegex refuses, and names the first one found. It also
// reports whether the pattern runs case-insensitively as a whole: by default,
// or under --case-sensitive when the pattern opens with a global `(?i)`.
func grepRegexSubsetError(p string, caseSensitive bool) (insensitive bool, err error) {
	s := grepRegexScan{p: p, leadingFlags: true, insensitive: !caseSensitive}
	for s.i = 0; s.i < len(p); s.i++ {
		if err := s.step(); err != nil {
			return false, err
		}
	}
	return s.insensitive, nil
}

// grepPythonFoldPattern gives a case-insensitive pattern the value's mapping
// (grepPythonFold), so Go's (?i) reads it as Python's re.IGNORECASE does.
//
// A character outside a class, or a class member on its own, is mapped like
// the value: `ıstanbul` becomes `istanbul`, `[ı]` becomes `[i]`. None of them
// can be part of an escape, since every escape letter is ASCII. A class RANGE
// keeps its endpoints — mapping one would change what the range holds — and,
// for each remapped character it spans, gains that character's mapping as an
// extra member: Python reads `[ĥ-ķ]` as also holding i and I (through İ and ı),
// so `[ĥ-ķ]` becomes `[ĥ-ķii]`. That stays right under negation: `[^ĥ-ķ]`
// becomes `[^ĥ-ķii]`, which leaves out i and I as Python's does.
//
// Every remapped character is above U+00FF, so an escape (at most U+00FF:
// `\xHH`, with `\x{…}` and octal escapes refused) is never one and, as a range
// endpoint, never reaches one.
func grepPythonFoldPattern(p string) string {
	var b strings.Builder
	b.Grow(len(p))
	for i := 0; i < len(p); {
		switch p[i] {
		case '\\':
			text, _, _, next := grepClassAtom(p, i)
			b.WriteString(text)
			i = next
		case '[':
			i = grepPythonFoldClass(&b, p, i)
		default:
			r, n := utf8.DecodeRuneInString(p[i:])
			b.WriteRune(grepPythonFold(r))
			i += n
		}
	}
	return b.String()
}

// grepPythonFoldClass writes the class opening at p[i] (`[`) to b with
// grepPythonFoldPattern's mapping, and returns the index just past it.
func grepPythonFoldClass(b *strings.Builder, p string, i int) int {
	b.WriteByte('[')
	i++
	if i < len(p) && p[i] == '^' {
		b.WriteByte('^')
		i++
	}
	for first := true; i < len(p) && (first || p[i] != ']'); first = false {
		text, lo, _, next := grepClassAtom(p, i)
		if next+1 < len(p) && p[next] == '-' && p[next+1] != ']' {
			hiText, _, hi, after := grepClassAtom(p, next+1)
			b.WriteString(text + "-" + hiText)
			for _, x := range grepPythonFoldRemapped {
				if lo <= x && x <= hi {
					b.WriteRune(grepPythonFold(x))
				}
			}
			i = after
			continue
		}
		if text[0] == '\\' {
			b.WriteString(text)
		} else {
			b.WriteRune(grepPythonFold(lo))
		}
		i = next
	}
	if i < len(p) {
		b.WriteByte(']')
		i++
	}
	return i
}

// grepClassAtom reads one pattern atom at p[i:] — an escape or one rune — and
// returns its source text, the lowest and highest rune it can stand for (an
// escape is read as U+0000..U+00FF), and the index just past it.
func grepClassAtom(p string, i int) (text string, lo, hi rune, next int) {
	if p[i] == '\\' {
		j := i + 1
		if j < len(p) {
			if p[j] == 'x' && j+2 < len(p) {
				j += 2 // \xHH
			}
			_, n := utf8.DecodeRuneInString(p[j:])
			j += n
		}
		return p[i:j], 0, 0xff, j
	}
	r, n := utf8.DecodeRuneInString(p[i:])
	return p[i : i+n], r, r, i + n
}

// grepRegexScan is grepRegexSubsetError's cursor. It reads bytes, not runes:
// every construct it judges is ASCII, and a UTF-8 continuation byte never
// equals an ASCII one.
type grepRegexScan struct {
	p            string
	i            int
	inClass      bool
	classStart   int  // index just past the class's opening `[` (and any `^`)
	leadingFlags bool // still inside the run of flag groups a pattern may open with
	insensitive  bool // the pattern runs case-insensitively as a whole
}

func (s *grepRegexScan) step() error {
	c := s.p[s.i]
	switch {
	case c == '\\':
		return s.escape()
	case s.inClass:
		return s.classByte(c)
	default:
		return s.outsideByte(c)
	}
}

func (s *grepRegexScan) escape() error {
	s.leadingFlags = false
	if s.i+1 >= len(s.p) {
		return nil // RE2 already refused a trailing backslash
	}
	s.i++
	return grepEscapeError(s.p, s.i)
}

func (s *grepRegexScan) classByte(c byte) error {
	switch {
	case c == ']' && s.i > s.classStart:
		s.inClass = false
	case c == '[':
		return errors.New("a `[` inside a class (`[[:alpha:]]` is a POSIX class in Go and a nested set in Python) — escape it as `\\[`")
	case s.i+1 < len(s.p) && s.p[s.i+1] == c && strings.IndexByte("-&~|", c) >= 0:
		return fmt.Errorf("`%c%c` inside a class is reserved for set operations in Python — escape one of them", c, c)
	}
	return nil
}

func (s *grepRegexScan) outsideByte(c byte) error {
	leading := s.leadingFlags
	s.leadingFlags = false
	switch c {
	case '[':
		s.inClass = true
		s.classStart = s.i + 1
		if s.classStart < len(s.p) && s.p[s.classStart] == '^' {
			s.classStart++
		}
	case '{':
		if s.i+1 < len(s.p) && s.p[s.i+1] == ',' {
			return errors.New("`{,n}` is a 0..n repeat in Python and literal text in Go — write `{0,n}`")
		}
	case '(':
		if s.i+1 >= len(s.p) || s.p[s.i+1] != '?' {
			return nil
		}
		global, flags, err := grepGroupError(s.p, s.i+2)
		if err != nil {
			return err
		}
		if !global {
			return s.scopedCaseError(flags)
		}
		if !leading {
			return errors.New("a flag group like `(?i)` must stand at the start of the pattern — Python refuses " +
				"global flags anywhere else; use a scoped `(?i:…)`")
		}
		if strings.Contains(flags, "i") {
			s.insensitive = true // a leading (?i) makes the whole pattern case-insensitive
		}
		// Skip to the group's `)`: the next construct may still be a leading
		// flag group, as in `(?i)(?s)`.
		s.i += strings.IndexByte(s.p[s.i:], ')')
		s.leadingFlags = true
	}
	return nil
}

// scopedCaseError refuses a scoped flag group that switches case-sensitivity
// for part of the pattern. The dashboard half reads a case-insensitive pattern
// the way Python does by mapping the value through grepPythonFold, which is
// only right when the whole pattern is case-insensitive: under `(?i)a(?-i:[^i])`
// the mapped value `ı`→`i` would no longer match the case-sensitive part that
// Python's `ı` does.
func (s *grepRegexScan) scopedCaseError(flags string) error {
	on, off, _ := strings.Cut(flags, "-")
	switch {
	case s.insensitive && strings.Contains(off, "i"):
		return errors.New("`(?-i:…)` makes part of a case-insensitive pattern case-sensitive, which the two " +
			"halves read differently for characters such as İ and ı — use --case-sensitive and spell the " +
			"case variants out, e.g. `[Kk]üche`")
	case !s.insensitive && strings.Contains(on, "i"):
		return errors.New("`(?i:…)` makes part of a --case-sensitive pattern case-insensitive, which the two " +
			"halves read differently for characters such as İ and ı — drop --case-sensitive, or spell the case " +
			"variants out, e.g. `[Kk]üche`")
	}
	return nil
}

// grepEscapeError judges the escape whose letter is at p[j].
func grepEscapeError(p string, j int) error {
	c := p[j]
	switch {
	case strings.IndexByte("wWdDsSbB", c) >= 0:
		return fmt.Errorf("`\\%c` is ASCII-only in Go and Unicode-aware in Python (`\\w` matches `Küche` whole in one, `K` in the other) — "+
			"write the class out, e.g. `[A-Za-z0-9_]`, `[0-9]`, `[ \\t\\n]`", c)
	case c >= '0' && c <= '9':
		return fmt.Errorf("the digit escape `\\%c` is an octal character in Go and a backreference in Python — write the character as `\\xHH`", c)
	case strings.IndexByte("zpPQEC", c) >= 0:
		return fmt.Errorf("`\\%c` is Go-only syntax; Python has no such escape", c)
	case c == 'x' && j+1 < len(p) && p[j+1] == '{':
		return errors.New("`\\x{…}` is Go-only syntax — write two hex digits, `\\xHH`")
	}
	return nil
}

// grepGroupError judges the group that opens with `(?` and whose next byte is
// p[j]. global reports a flag-only group — `(?i)` — as opposed to a scoped one
// `(?i:…)` or a non-flag extension; flags is the group's flag letters (empty
// for a non-flag group).
func grepGroupError(p string, j int) (global bool, flags string, err error) {
	if j >= len(p) {
		return false, "", nil
	}
	switch p[j] {
	case 'P', ':':
		return false, "", nil // (?P<name>…) and (?:…) mean the same in both
	case '<':
		return false, "", errors.New("`(?<name>…)` is Go-only syntax — write `(?P<name>…)`")
	}
	end := j
	for end < len(p) && p[end] != ')' && p[end] != ':' {
		end++
	}
	if end >= len(p) {
		return false, "", nil
	}
	flags = p[j:end]
	for _, f := range flags {
		if !strings.ContainsRune("ims-", f) {
			return false, "", fmt.Errorf("the flag `%c` in `(?%s…)` is Go-only or unknown to Python — only i, m and s are shared", f, flags)
		}
	}
	if p[end] == ':' {
		return false, flags, nil
	}
	if strings.Contains(flags, "-") {
		return true, flags, fmt.Errorf("`(?%s)` turns a flag off globally, which Python refuses — use a scoped group such as `(?-s:…)`, or --case-sensitive for `i`", flags)
	}
	return true, flags, nil
}
