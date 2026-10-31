package policy

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/hemm-ems/hactl/internal/glob"
)

// address is a parsed `<kind>:<id>[/<sub>…]` — a rule pattern or, parsed with
// concrete=true, a resource. It is the one grammar both share (manual,
// "Resources").
type address struct {
	// any is the bare pattern `*`: every resource of every kind.
	any  bool
	kind Kind
	spec KindSpec
	// id holds the id's segments: one for most kinds, two for helper, one or
	// more for file. The default dashboard's id is the single segment "".
	id []string
	// sub holds the sub-resource segments below the id, possibly none.
	sub []string
}

// kindSpec looks a kind up in KindSpecs by its exact spelling.
func kindSpec(k string) (KindSpec, bool) {
	for _, s := range KindSpecs() {
		if string(s.Kind) == k {
			return s, true
		}
	}
	return KindSpec{}, false
}

func kindNames() string {
	ks := Kinds()
	names := make([]string, len(ks))
	for i, k := range ks {
		names[i] = string(k)
	}
	return strings.Join(names, ", ")
}

// parseAddress parses s by the manual's grammar. concrete is the resource
// form (`policy check`): no wildcard anywhere, a system id must be one of the
// set. The caller has already refused a wildcard in a concrete address; the
// checks here that mention wildcards are the pattern form's.
func parseAddress(s string, concrete bool) (address, error) {
	if !concrete && s == "*" {
		return address{any: true}, nil
	}
	kindStr, rest, ok := strings.Cut(s, ":")
	if !ok {
		return address{}, errors.New("missing \":\" (want <kind>:<id>, or the pattern \"*\")")
	}
	spec, ok := kindSpec(kindStr)
	if !ok {
		return address{}, fmt.Errorf("unknown kind %q (want one of %s)", kindStr, kindNames())
	}
	a := address{kind: spec.Kind, spec: spec}
	if err := a.splitIDAndSub(strings.Split(rest, "/"), rest); err != nil {
		return address{}, err
	}
	if err := a.checkID(concrete); err != nil {
		return address{}, err
	}
	if err := a.checkSub(concrete); err != nil {
		return address{}, err
	}
	return a, nil
}

// splitIDAndSub divides the segments after the kind into the id and the
// sub-resource path, by the kind's id arity.
func (a *address) splitIDAndSub(segs []string, rest string) error {
	switch {
	case a.spec.Kind == KindFile:
		return a.splitFile(segs)
	case a.spec.IDSegments == 2:
		if len(segs) < 2 {
			return fmt.Errorf("%s id is <domain>/<id>, two segments (got %q)", a.spec.Kind, rest)
		}
		a.id, a.sub = segs[:2], segs[2:]
	default:
		a.id, a.sub = segs[:1], segs[1:]
	}
	return nil
}

// fileBlockWord is the one sub-resource word of a file.
const fileBlockWord = "block"

// splitFile reads a file address (D-67). A trailing `/block/<id>` with at
// least one path segment before it is the block sub-resource; a segment named
// `block` (in any case) anywhere else is refused as ambiguous, and so is a
// `.` or `..` path segment — a path is never canonicalised, because
// `file:./secrets.yaml` would otherwise pass a deny on `file:secrets.yaml`.
func (a *address) splitFile(segs []string) error {
	n := len(segs)
	if n >= 3 && segs[n-2] == fileBlockWord {
		a.id, a.sub = segs[:n-2], segs[n-2:]
	} else {
		a.id = segs
	}
	if len(a.id) == 1 && a.id[0] == "" {
		return errors.New("empty file path")
	}
	path := strings.Join(a.id, "/")
	for i, seg := range segs {
		isBlockSub := a.sub != nil && i == n-2
		if !isBlockSub && strings.EqualFold(seg, fileBlockWord) {
			return fmt.Errorf("segment %q in file address %q is ambiguous with the block sub-resource: "+
				"only a trailing /block/<id> after the path is a block, so no path segment (and no block id) may be named block (D-67)",
				seg, strings.Join(segs, "/"))
		}
	}
	for _, seg := range a.id {
		if seg == "." || seg == ".." {
			return fmt.Errorf("file path %q holds a %q segment: a file path is written exactly as hactl prints it, "+
				"never relative to itself, and is not canonicalised (D-67)", path, seg)
		}
	}
	return nil
}

// checkID refuses an empty id or id segment, a character outside the kind's
// charset (D-68), a system id outside the kind's closed set (as a glob: one
// that matches none of it), and — in a pattern — an entity or service id
// that can never match because it has neither a `.` nor a wildcard (D-63).
func (a *address) checkID(concrete bool) error {
	spec := a.spec
	for _, seg := range a.id {
		if seg != "" || (spec.EmptyID && len(a.id) == 1) {
			continue
		}
		if len(a.id) == 1 {
			return fmt.Errorf("empty %s id", spec.Kind)
		}
		return fmt.Errorf("empty segment in %s id %q", spec.Kind, strings.Join(a.id, "/"))
	}
	if err := a.checkCharset(concrete); err != nil {
		return err
	}
	if spec.Qualified && !concrete {
		id := a.id[0]
		if !strings.Contains(id, ".") && !glob.HasWildcard(id) {
			return fmt.Errorf("%s id %q has neither a \".\" nor a wildcard, so it can never match: a policy pattern matches the full "+
				"<domain>.<name> id, never the part after the domain (D-63) — write %s:*.%s to mean every domain",
				spec.Kind, id, spec.Kind, id)
		}
	}
	if spec.IDs == nil {
		return nil
	}
	id := a.id[0]
	for _, v := range spec.IDs {
		if (concrete && v == id) || (!concrete && glob.Match(v, id)) {
			return nil
		}
	}
	return fmt.Errorf("%s id %q names none of %s", spec.Kind, id, strings.Join(spec.IDs, ", "))
}

// checkCharset applies D-68 to every id segment. The kinds whose ids Home
// Assistant restricts take only its charset (letters in any case, D-2), plus
// `*` and `?` in a pattern; every other kind refuses white space and control
// characters only. Sub-resource segments are not checked here.
func (a *address) checkCharset(concrete bool) error {
	allowed, desc := idCharset(a.spec.Kind)
	for _, seg := range a.id {
		for _, r := range seg {
			switch {
			case !concrete && (r == '*' || r == '?'):
				continue
			case unicode.IsSpace(r) || unicode.IsControl(r):
				return fmt.Errorf("%s id %q holds %s: no id may hold white space or a control character (D-68)",
					a.spec.Kind, strings.Join(a.id, "/"), describeRune(r))
			case allowed != nil && !allowed(r):
				return fmt.Errorf("%s id %q holds %s: a %s id is %s (D-68)",
					a.spec.Kind, strings.Join(a.id, "/"), describeRune(r), a.spec.Kind, desc)
			}
		}
	}
	return nil
}

// idCharset returns the rune predicate of a kind whose ids Home Assistant
// restricts, and how the manual describes it; nil for every other kind.
func idCharset(k Kind) (func(rune) bool, string) {
	word := func(r rune) bool {
		return r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
	}
	switch k {
	case KindEntity, KindService:
		return func(r rune) bool { return word(r) || r == '.' }, "ASCII letters, digits, _ and ."
	case KindHelper:
		return word, "<domain>/<id>, each ASCII letters, digits and _"
	case KindArea, KindFloor, KindLabel:
		return word, "ASCII letters, digits and _"
	}
	return nil, ""
}

// describeRune names a character in an error so an invisible one is visible.
func describeRune(r rune) string {
	if unicode.IsPrint(r) && !unicode.IsSpace(r) {
		return fmt.Sprintf("%q (U+%04X)", r, r)
	}
	return fmt.Sprintf("the character U+%04X", r)
}

// checkSub validates the sub-resource segments against the kind's grammar.
func (a *address) checkSub(concrete bool) error {
	if len(a.sub) == 0 {
		return nil
	}
	if slices.Contains(a.sub, "") {
		return fmt.Errorf("empty segment in sub-resource %q", strings.Join(a.sub, "/"))
	}
	spec := a.spec
	if len(spec.Subs) == 0 {
		return fmt.Errorf("%s has no sub-resources (got %q)", spec.Kind, strings.Join(a.sub, "/"))
	}

	// system: the only sub is the domain below `reload`, with no fixed word.
	if spec.Kind == KindSystem {
		id := a.id[0]
		if id != "reload" && (concrete || !glob.Match("reload", id)) {
			return fmt.Errorf("system:%s has no sub-resources (only system:reload/<domain> does)", id)
		}
		if len(a.sub) > 1 {
			return fmt.Errorf("system:reload takes one segment below it, the domain (got %q)", strings.Join(a.sub, "/"))
		}
		return nil
	}

	word := a.sub[0]
	var sub *SubSpec
	for i := range spec.Subs {
		if spec.Subs[i].Word == word {
			sub = &spec.Subs[i]
			break
		}
	}
	if sub == nil {
		if glob.HasWildcard(word) {
			return fmt.Errorf("sub-resource word %q holds a wildcard: sub-resource words are literal (want one of %s)", word, subWords(spec))
		}
		return fmt.Errorf("unknown sub-resource %q for %s (want one of %s)", word, spec.Kind, subWords(spec))
	}
	if len(a.sub) == 1 {
		return nil
	}
	if !sub.Param {
		return fmt.Errorf("%s/%s takes nothing below it (got %q)", spec.Kind, word, strings.Join(a.sub, "/"))
	}
	if len(a.sub) > 2 {
		return fmt.Errorf("%s/%s takes one segment below it (got %q)", spec.Kind, word, strings.Join(a.sub, "/"))
	}
	if sub.ParamValues != nil {
		v := a.sub[1]
		if slices.Contains(sub.ParamValues, v) {
			return nil
		}
		if glob.HasWildcard(v) {
			return fmt.Errorf("%s field %q holds a wildcard: the field names are literal (want one of %s; %s/%s alone covers every field)",
				word, v, strings.Join(sub.ParamValues, ", "), spec.Kind, word)
		}
		return fmt.Errorf("unknown %s field %q for %s (want one of %s)", word, v, spec.Kind, strings.Join(sub.ParamValues, ", "))
	}
	return nil
}

func subWords(spec KindSpec) string {
	words := make([]string, len(spec.Subs))
	for i, s := range spec.Subs {
		words[i] = s.Word
	}
	return strings.Join(words, ", ")
}

// matches reports whether the pattern a covers resource r (manual,
// "Matching"): kind equal, id matched segment by segment with the shared glob,
// the pattern's sub path a segment-wise prefix of r's.
func (a *address) matches(r Resource) bool {
	if a.any {
		return true
	}
	if a.kind != r.Kind {
		return false
	}
	if !a.matchID(r.ID) {
		return false
	}
	return matchSubPrefix(a.sub, splitPath(r.Sub))
}

// coversDescendant reports whether the pattern a covers a strict descendant of
// resource r without covering r itself or an ancestor of it (D-66): kind and
// id match, and a's sub path is longer than r's with r's sub path a
// segment-wise prefix of it. A file path is never a descendant of another
// path — only sub-resources are descendants — which the full-id match gives.
func (a *address) coversDescendant(r Resource) bool {
	if a.any || a.kind != r.Kind || !a.matchID(r.ID) {
		return false
	}
	sub := splitPath(r.Sub)
	if len(a.sub) <= len(sub) {
		return false
	}
	for i := range sub {
		if !glob.Match(sub[i], a.sub[i]) {
			return false
		}
	}
	return true
}

// coversSubSet reports whether the pattern a, for the live id of kind k,
// covers every resource an allow with sub path sub can address — the allow's
// sub path read as a set, not a literal (lint's CodeUnreachable): a's sub path
// is no longer than sub, each literal segment of sub matches a's segment, and
// each wildcard segment of sub faces a segment of a that is only `*`.
func (a *address) coversSubSet(k Kind, id string, sub []string) bool {
	if a.any {
		return true
	}
	if a.kind != k || !a.matchID(id) || len(a.sub) > len(sub) {
		return false
	}
	for i, ds := range a.sub {
		as := sub[i]
		if glob.HasWildcard(as) {
			if strings.Trim(ds, "*") != "" {
				return false
			}
			continue
		}
		if !glob.Match(as, ds) {
			return false
		}
	}
	return true
}

// attrName returns the free segment of an `entity:<id>/attr/<name>` pattern,
// the one sub-resource segment lint checks against the instance (D-65).
func (a *address) attrName() (string, bool) {
	if a.kind == KindEntity && len(a.sub) == 2 && a.sub[0] == "attr" {
		return a.sub[1], true
	}
	return "", false
}

// hasUncheckableSub reports whether the pattern holds a free sub-resource
// segment lint cannot check — `views/<path>`, `block/<id>`, `reload/<domain>`:
// every free (Param without ParamValues) segment except an entity's attribute
// name (D-65).
func (a *address) hasUncheckableSub() bool {
	if a.any || len(a.sub) == 0 {
		return false
	}
	if a.kind == KindSystem {
		return true // reload/<domain>: the only sub system has is free
	}
	if len(a.sub) < 2 {
		return false
	}
	if _, ok := a.attrName(); ok {
		return false
	}
	for _, s := range a.spec.Subs {
		if s.Word == a.sub[0] {
			return s.Param && s.ParamValues == nil
		}
	}
	return false
}

// matchID matches the pattern's id part against a resource id.
func (a *address) matchID(id string) bool {
	switch {
	case a.spec.Kind == KindFile:
		return matchSegments(a.id, strings.Split(id, "/"))
	case len(a.id) > 1:
		segs := strings.Split(id, "/")
		if len(segs) != len(a.id) {
			return false
		}
		for i := range segs {
			if !glob.Match(segs[i], a.id[i]) {
				return false
			}
		}
		return true
	default:
		// The full id only, for every kind (D-63): no second anchor.
		return glob.Match(id, a.id[0])
	}
}

// matchSegments matches path segments against pattern segments, where a
// segment that is exactly `**` matches zero or more whole segments.
func matchSegments(pat, segs []string) bool {
	if len(pat) == 0 {
		return len(segs) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(segs); i++ {
			if matchSegments(pat[1:], segs[i:]) {
				return true
			}
		}
		return false
	}
	if len(segs) == 0 {
		return false
	}
	return glob.Match(segs[0], pat[0]) && matchSegments(pat[1:], segs[1:])
}

// matchSubPrefix: a pattern without a sub covers the resource and everything
// below it; a pattern with one covers that sub-resource and everything below
// it, and not the resource itself.
func matchSubPrefix(pat, sub []string) bool {
	if len(pat) > len(sub) {
		return false
	}
	for i := range pat {
		if !glob.Match(sub[i], pat[i]) {
			return false
		}
	}
	return true
}

func splitPath(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "/")
}
