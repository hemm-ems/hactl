package policy

import (
	"slices"
	"strconv"
	"strings"

	"github.com/hemm-ems/hactl/internal/glob"
)

// The applicability table (D-110): which write verbs some command asks for at
// which address. A verb applies at an address only if some check names it
// there; `read` applies everywhere and is not part of the table.
//
// The table is the Go form of the manual's table under "Verbs". It is not
// hand-kept on its own: the gates in internal/policy/applicabilitygate hold it
// in lockstep with the (kind, sub shape, verb) triples the request layer
// (testdata/policy/requests, dev/surfaces/policy.manifest) and the command
// plans emit — a triple emitted but missing here fails, and so does a triple
// here that nothing emits (an over-wide table would quietly re-widen what a
// deny refuses) — and with the manual's table.

// applicableRow is one address shape and the write verbs that apply exactly
// there.
type applicableRow struct {
	kind Kind
	// id is the fixed id of a system row; "" for every other kind, whose rows
	// hold for any id.
	id string
	// sub is the sub shape below the id: closed words literal (`config`,
	// `run`, `options`), and `*` for the one segment after a word, free or
	// closed (`views/*`, `registry/*` — every registry field).
	sub []string
	// verbs are the write verbs that apply there, in canonical order.
	verbs []Verb
	// show is the address as the manual's table writes it, for lint messages.
	show string
}

var applicability = []applicableRow{
	{kind: KindEntity, verbs: []Verb{Control, Create, Delete}, show: "entity:<id>"},
	{kind: KindEntity, sub: []string{"registry", "*"}, verbs: []Verb{Edit}, show: "entity:<id>/registry/<field>"},
	{kind: KindDevice, verbs: []Verb{Delete}, show: "device:<id>"},
	{kind: KindDevice, sub: []string{"registry", "*"}, verbs: []Verb{Edit}, show: "device:<id>/registry/<field>"},
	{kind: KindArea, verbs: []Verb{Edit, Create, Delete}, show: "area:<id>"},
	{kind: KindFloor, verbs: []Verb{Create, Delete}, show: "floor:<id>"},
	{kind: KindLabel, verbs: []Verb{Create, Delete}, show: "label:<id>"},
	// control on a typed address is the deny-only check a service call makes
	// (D-107).
	{kind: KindAutomation, verbs: []Verb{Control, Create, Delete}, show: "automation:<id>"},
	{kind: KindAutomation, sub: []string{"config"}, verbs: []Verb{Edit}, show: "automation:<id>/config"},
	{kind: KindScript, verbs: []Verb{Control, Create, Delete}, show: "script:<id>"},
	{kind: KindScript, sub: []string{"config"}, verbs: []Verb{Edit}, show: "script:<id>/config"},
	{kind: KindScript, sub: []string{"run"}, verbs: []Verb{Control}, show: "script:<id>/run"},
	{kind: KindTemplate, verbs: []Verb{Control, Create, Delete}, show: "template:<id>"},
	{kind: KindTemplate, sub: []string{"config"}, verbs: []Verb{Edit}, show: "template:<id>/config"},
	{kind: KindHelper, verbs: []Verb{Control, Create, Delete}, show: "helper:<domain>/<id>"},
	{kind: KindDashboard, verbs: []Verb{Edit, Create, Delete}, show: "dashboard:<url_path>"},
	{kind: KindDashboard, sub: []string{"views", "*"}, verbs: []Verb{Edit, Create, Delete}, show: "dashboard:<url_path>/views/<path>"},
	{kind: KindConfigEntry, verbs: []Verb{Edit, Create, Delete}, show: "config_entry:<id>"},
	{kind: KindConfigEntry, sub: []string{"options"}, verbs: []Verb{Edit}, show: "config_entry:<id>/options"},
	{kind: KindFile, verbs: []Verb{Edit}, show: "file:<path>"},
	{kind: KindService, verbs: []Verb{Control}, show: "service:<domain>.<service>"},
	{kind: KindSystem, id: "wireguard", verbs: []Verb{Control, Edit}, show: "system:wireguard"},
	{kind: KindSystem, id: "cache", verbs: []Verb{Edit, Delete}, show: "system:cache"},
}

// changesBelow lists what a verb asked at an address changes below it, besides
// create and delete, which create or delete every part of what they act on
// (D-125): a service call changes an entity's state and attributes, and an
// edit of a file rewrites its blocks. word is the first sub word of the part.
// A deny on such a part holds the verb too: a part of it is denied, so all of
// it is (D-66).
var changesBelow = []struct {
	kind Kind
	verb Verb
	word string
}{
	{KindEntity, Control, "state"},
	{KindEntity, Control, "attr"},
	{KindFile, Edit, "block"},
}

// ApplicableVerbs returns the write verbs that apply exactly at r (D-110), in
// the order control, edit, create, delete; none for an address no command
// changes (`entity:<id>/state`, `file:<path>/block/<id>`, `system:logs`, …).
// It is the table itself: a parent's verbs do not include its children's.
func ApplicableVerbs(r Resource) []Verb {
	sub := splitPath(r.Sub)
	var set verbSet
	for _, row := range applicability {
		if row.kind != r.Kind || (row.id != "" && row.id != r.ID) || len(row.sub) != len(sub) {
			continue
		}
		if subShapeCovers(row.sub, sub) {
			set.add(row.verbs...)
		}
	}
	return set.list()
}

// ApplicableAtOrBelow returns the write verbs that apply at r or at any
// address below it, in canonical order — what `write` means for r (D-110,
// D-120): `entity:light.x` gets all four (edit from its registry fields),
// `entity:light.x/attr/brightness` none.
func ApplicableAtOrBelow(r Resource) []Verb {
	return applicableUnder(address{kind: r.Kind, id: []string{r.ID}, sub: splitPath(r.Sub)})
}

// applicableUnder returns the write verbs the table lists at any address the
// pattern a covers — its own address or one below it — in canonical order.
// The pattern `*` covers every kind.
func applicableUnder(a address) []Verb {
	if a.any {
		return []Verb{Control, Edit, Create, Delete}
	}
	var set verbSet
	for _, row := range applicability {
		if row.kind != a.kind {
			continue
		}
		if row.id != "" && (len(a.id) == 0 || !glob.Match(row.id, a.id[0])) {
			continue
		}
		if len(a.sub) > len(row.sub) || !subShapeCovers(row.sub[:len(a.sub)], a.sub) {
			continue
		}
		set.add(row.verbs...)
	}
	return set.list()
}

// deniedUnder returns the write verbs that apply at a deny's pattern a
// (D-125): the table's verbs at or below it, and every verb that, asked at an
// address above a, changes what a covers — create and delete of every owner
// the table lists them for, and changesBelow. An allow's are applicableUnder
// alone: an allow on a part grants only the part.
func deniedUnder(a address) []Verb {
	var set verbSet
	set.add(applicableUnder(a)...)
	if a.any || len(a.sub) == 0 {
		return set.list()
	}
	for _, row := range applicability {
		if row.kind != a.kind || len(row.sub) >= len(a.sub) {
			continue
		}
		if row.id != "" && (len(a.id) == 0 || !glob.Match(row.id, a.id[0])) {
			continue
		}
		if !subShapeCovers(row.sub, a.sub[:len(row.sub)]) {
			continue
		}
		for _, v := range row.verbs {
			if v == Create || v == Delete {
				set.add(v)
			}
		}
	}
	for _, c := range changesBelow {
		if c.kind == a.kind && c.word == a.sub[0] {
			set.add(c.verb)
		}
	}
	return set.list()
}

// subShapeCovers reports whether the sub segments segs (a resource's, or a
// pattern's) fall within the row's sub shape of the same length: a `*` in the
// shape takes any segment, a word must match.
func subShapeCovers(shape, segs []string) bool {
	for i, s := range shape {
		if s != "*" && !glob.Match(s, segs[i]) {
			return false
		}
	}
	return true
}

// appliesHere reports whether verb v applies where the rule's pattern sits —
// at its address or below it, and for a deny also from above it (D-125). read
// applies everywhere. Partial (D-66 step 4) counts a descendant deny for v
// only when this holds (D-110).
func (r Rule) appliesHere(v Verb) bool {
	return v == Read || slices.Contains(r.applicable, v)
}

// narrowWrite is D-110's per-address `write`: in a rule whose verb list holds
// `write`, keep the write verbs that apply at the pattern, plus every verb
// written by name. verbs is ParseVerbs(written), so the order stays canonical.
func narrowWrite(written []string, verbs, applicable []Verb) []Verb {
	if !slices.Contains(written, WriteAlias) {
		return verbs
	}
	out := make([]Verb, 0, len(verbs))
	for _, v := range verbs {
		if v == Read || slices.Contains(applicable, v) || slices.Contains(written, v.String()) {
			out = append(out, v)
		}
	}
	return out
}

// verbSet collects verbs and lists them in canonical order.
type verbSet [Delete + 1]bool

func (s *verbSet) add(vs ...Verb) {
	for _, v := range vs {
		s[v] = true
	}
}

func (s *verbSet) list() []Verb {
	out := []Verb{}
	for _, v := range AllVerbs() {
		if s[v] {
			out = append(out, v)
		}
	}
	return out
}

// CodeVerbNotApplicable is the lint finding for a write verb written by name
// that applies nowhere the rule's pattern reaches, or a `write` that applies
// to nothing there (D-110). The rule is kept as written — it is not a broken
// file — but it decides nothing a command asks.
const CodeVerbNotApplicable = "verb-not-applicable"

// verbNotApplicable reports CodeVerbNotApplicable for every rule of every
// profile: one finding per rule and inapplicable verb, and one for a `write`
// that expands to nothing. It needs no live instance.
func verbNotApplicable(p *Policy) []Finding {
	var out []Finding
	for _, pr := range p.profiles {
		for i := range pr.rules {
			r := &pr.rules[i]
			finding := func(msg string) {
				out = append(out, Finding{Code: CodeVerbNotApplicable, Profile: pr.name, Rule: r.Text(), Line: r.Line, Message: msg})
			}
			if slices.Contains(r.Written, WriteAlias) && len(r.applicable) == 0 {
				finding(strconv.Quote(WriteAlias) + " applies to nothing at " + r.Pattern +
					" — no command asks for a write there, so the rule decides nothing for writes")
			}
			for _, v := range []Verb{Control, Edit, Create, Delete} {
				if !slices.Contains(r.Written, v.String()) || r.appliesHere(v) {
					continue
				}
				finding(strconv.Quote(v.String()) + " does not apply at " + r.Pattern +
					" — no command asks for it there, so the rule decides nothing for it (" + checkedAt(r.addr.kind, v) + ")")
			}
		}
	}
	return out
}

// checkedAt names where verb v is checked on kind k, as the manual's table
// writes the addresses: `control on entity is checked at: entity:<id>`.
func checkedAt(k Kind, v Verb) string {
	var at []string
	for _, row := range applicability {
		if row.kind == k && slices.Contains(row.verbs, v) {
			at = append(at, row.show)
		}
	}
	if len(at) == 0 {
		return "no command asks for " + v.String() + " on any " + string(k)
	}
	return v.String() + " on " + string(k) + " is checked at: " + strings.Join(at, ", ")
}
