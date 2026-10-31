package policy

import (
	"errors"
	"fmt"
	"slices"
	"strings"

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
		n := len(segs)
		switch {
		case n >= 2 && segs[n-1] == "block":
			a.id, a.sub = segs[:n-1], segs[n-1:]
		case n >= 2 && segs[n-2] == "block":
			a.id, a.sub = segs[:n-2], segs[n-2:]
		default:
			a.id = segs
		}
		if len(a.id) == 0 || (len(a.id) == 1 && a.id[0] == "") {
			return errors.New("empty file path (a trailing /block/<id> is the block sub-resource; the path comes before it)")
		}
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

// checkID refuses an empty id or id segment, and a system id outside the
// kind's closed set (as a glob: one that matches none of it).
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
	case a.spec.Qualified:
		return glob.MatchQualified(id, a.id[0])
	default:
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
