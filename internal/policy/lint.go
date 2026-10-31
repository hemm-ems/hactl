package policy

import (
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/hemm-ems/hactl/internal/glob"
)

// Finding codes `policy lint` reports (manual, "policy lint"). Each is a way a
// rule can fail to mean what its author wrote. Tests compare against these.
const (
	// CodeBroken: a D-60 problem — the file is not a valid policy. Produced by
	// the command from a *LoadError, one finding per Problem; Lint itself
	// never sees a broken file.
	CodeBroken = "broken"
	// CodeImplicitAllowAll: the file has no `active:` and no `default`
	// profile, so nothing it declares is in effect (D-61).
	CodeImplicitAllowAll = "implicit-allow-all"
	// CodeRedundantAllow: an allow rule in a profile without `default: deny`.
	// Such a profile allows whatever no deny matches, so the allow — and any
	// `where:` narrowing it — decides nothing.
	CodeRedundantAllow = "redundant-allow"
	// CodeNoMatch: a rule whose pattern matches no live resource of its kind.
	// A typo in a deny is a silent hole otherwise.
	CodeNoMatch = "no-match"
	// CodeUnreachable: an allow rule (for one of its verbs) whose every live
	// match is denied for that verb by a deny in the same profile — it can
	// never decide.
	CodeUnreachable = "unreachable"
)

// Finding is one lint result.
type Finding struct {
	Code string
	// Profile is the profile the rule belongs to ("" for file-level findings:
	// CodeBroken, CodeImplicitAllowAll).
	Profile string
	// Rule is the rule's Text() ("" for file-level findings).
	Rule string
	// Line is the rule's (or problem's) 1-based line, 0 when it has none.
	Line int
	// Message is one human sentence: what is wrong and what it means.
	Message string
}

// Reasons an Unchecked entry carries when the cause is not an inventory
// error (whose text is the reason then). Tests compare against these.
const (
	// ReasonNotEnumerated: the inventory does not list the kind
	// (ErrNotEnumerated) — template, helper, config_entry and file in this
	// version.
	ReasonNotEnumerated = "not enumerated"
	// ReasonNoLive: Lint ran without an inventory.
	ReasonNoLive = "no live instance"
	// ReasonSubNotChecked: the rule holds a free sub-resource segment lint
	// cannot check against the instance — `views/<path>`, `block/<id>`,
	// `reload/<domain>` holding a literal character; one of only `*` names
	// nothing that could be misspelt and counts as checked (D-65).
	ReasonSubNotChecked = "sub-resource not checked"
	// ReasonAttrsNotEnumerated: the rule names `attr/<name>` and the inventory
	// cannot list attributes (it is not an AttributeInventory).
	ReasonAttrsNotEnumerated = "attributes not enumerated"
)

// Unchecked records something lint could not check against the live
// instance, and why. An unchecked entry is never reported as clean (H-10: a
// sweep that could only read part of the instance is truncated).
//
// An entry about a whole kind (the inventory could not list it) has Profile
// and Rule "" and Line 0. An entry about one rule — a free sub-resource
// segment (ReasonSubNotChecked) or an `attr/<name>` whose attributes could
// not be read (D-65) — names the rule's profile, Text() and line.
type Unchecked struct {
	Kind    Kind
	Reason  string
	Profile string
	Rule    string
	Line    int
}

// LintReport is what Lint returns. Findings are ordered by (Line, Code,
// Profile, Rule); Unchecked by Kinds() order, and within a kind the kind's own
// entry first, then one entry per rule in file order (line, then column).
// Both are empty slices, not nil, when there is nothing to report — they
// render as [] in --json.
type LintReport struct {
	Findings  []Finding
	Unchecked []Unchecked
}

// ErrNotEnumerated is what an Inventory returns for a kind it does not list.
// Lint turns it into an Unchecked entry with the reason ReasonNotEnumerated.
var ErrNotEnumerated = errors.New("policy: kind not enumerated")

// Inventory lists the live ids of a kind, for CodeNoMatch and
// CodeUnreachable. The CLI implements it over Home Assistant (manual: which
// kinds phase 1 enumerates and from where); a test implements it over a
// fixture. Any other error makes the kind Unchecked with err's text as the
// reason.
type Inventory interface {
	// IDs returns every live id of kind, in any order (Lint sorts).
	IDs(kind Kind) ([]string, error)
}

// AttributeInventory is an Inventory that can also list state attributes, so
// Lint can check an `entity:<id>/attr/<name>` rule's attribute name (D-65).
// With a plain Inventory, every such rule is Unchecked with
// ReasonAttrsNotEnumerated.
type AttributeInventory interface {
	Inventory
	// Attributes returns, keyed by entity_id, the attribute names in the
	// live state of every entity that has a state. It is asked at most once
	// per Lint, and only when a rule names an attribute. An error makes every
	// such rule Unchecked with err's text as the reason.
	Attributes() (map[string][]string, error)
}

// Lint checks every profile the policy declares — not only the effective one:
// a typo in an inactive profile is a hole the day the owner switches to it.
//
//   - CodeImplicitAllowAll when p.Selection() is SelectedByImplicit and the
//     file declares at least one profile.
//   - CodeRedundantAllow for every allow rule in a profile without
//     `default: deny`.
//   - CodeNoMatch for every rule (allow or deny) whose kind inv enumerates
//     and whose pattern's id part matches none of the live ids — or, for an
//     `entity:<id>/attr/<name>` rule whose id matches, whose name matches no
//     attribute in the live state of any entity the id matches (D-65). A
//     name of only `*` is not looked up: it names every attribute. The bare
//     `*` pattern is never CodeNoMatch.
//   - CodeUnreachable for every (allow rule, verb) whose kind inv enumerates,
//     whose id part matches at least one live id, and where for every matched
//     id one deny of the same profile for that verb covers the allow's whole
//     sub path. The allow's sub path is a set, not a literal: the deny's sub
//     path is no longer than the allow's, each literal allow segment matches
//     the deny's segment, and each wildcard allow segment faces a deny
//     segment of only `*`. `where:` is ignored here: a deny wins regardless.
//   - Unchecked for every kind that some rule names and inv cannot list
//     (inv nil: every such kind, ReasonNoLive), and for every rule holding a
//     free sub-resource segment other than `attr/<name>` that holds a literal
//     character (ReasonSubNotChecked), whether or not its kind is enumerated;
//     a segment of only `*` (`*`, `**`) counts as checked (D-65).
//
// A Policy with no file reports nothing. Lint never errors: an inventory
// failure is an Unchecked entry, not a failed lint.
func Lint(p *Policy, inv Inventory) LintReport {
	rep := LintReport{Findings: []Finding{}, Unchecked: []Unchecked{}}
	if p == nil || !p.exists {
		return rep
	}
	if base, sel := p.Effective(); sel == SelectedByImplicit && base == "" && len(p.profiles) > 0 {
		rep.Findings = append(rep.Findings, Finding{
			Code: CodeImplicitAllowAll,
			Message: "profiles are declared (" + strings.Join(p.Profiles(), ", ") + "), but there is no active: " +
				"and no default profile, so none of them is in effect: everything is allowed",
		})
	}
	l := &linter{inv: inv}
	l.enumerate(p)
	for _, pr := range p.profiles {
		l.profile(pr)
	}
	rep.Findings = append(rep.Findings, l.findings...)
	sortFindings(rep.Findings)
	rep.Unchecked = append(rep.Unchecked, l.sortedUnchecked()...)
	return rep
}

// linter carries one Lint run's live data and results.
type linter struct {
	inv  Inventory
	live map[Kind][]string

	attrsAsked bool
	attrs      map[string][]string
	attrsErr   string

	findings []Finding
	kinds    []Unchecked
	rules    []ruleUnchecked
}

// ruleUnchecked is a per-rule Unchecked entry with the rule's file position,
// for ordering.
type ruleUnchecked struct {
	u    Unchecked
	rule Rule
}

// enumerate asks inv once for every kind some rule names, in Kinds() order
// (never map order, H-16). A kind it cannot list is Unchecked.
func (l *linter) enumerate(p *Policy) {
	named := map[Kind]bool{}
	for _, pr := range p.profiles {
		for _, r := range pr.rules {
			if !r.addr.any {
				named[r.addr.kind] = true
			}
		}
	}
	l.live = map[Kind][]string{}
	for _, k := range Kinds() {
		if !named[k] {
			continue
		}
		if l.inv == nil {
			l.kinds = append(l.kinds, Unchecked{Kind: k, Reason: ReasonNoLive})
			continue
		}
		ids, err := l.inv.IDs(k)
		switch {
		case errors.Is(err, ErrNotEnumerated):
			l.kinds = append(l.kinds, Unchecked{Kind: k, Reason: ReasonNotEnumerated})
		case err != nil:
			l.kinds = append(l.kinds, Unchecked{Kind: k, Reason: err.Error()})
		default:
			sorted := append([]string(nil), ids...)
			sort.Strings(sorted)
			l.live[k] = sorted
		}
	}
}

// profile reports one profile's rules.
func (l *linter) profile(pr *profile) {
	for i := range pr.rules {
		r := pr.rules[i]
		finding := func(code, msg string) {
			l.findings = append(l.findings, Finding{Code: code, Profile: pr.name, Rule: r.Text(), Line: r.Line, Message: msg})
		}
		unchecked := func(reason string) {
			l.rules = append(l.rules, ruleUnchecked{rule: r, u: Unchecked{
				Kind: r.addr.kind, Reason: reason, Profile: pr.name, Rule: r.Text(), Line: r.Line,
			}})
		}
		if r.Effect == Allow && pr.deflt != Deny {
			finding(CodeRedundantAllow, "profile "+strconv.Quote(pr.name)+" has no default: deny, so it already allows "+
				"whatever no deny matches: this allow (and any where: on it) decides nothing")
		}
		if r.addr.any {
			continue
		}
		if r.addr.hasUncheckableSub() {
			unchecked(ReasonSubNotChecked)
		}
		ids, enumerated := l.live[r.addr.kind]
		if !enumerated {
			continue
		}
		var matched []string
		for _, id := range ids {
			if r.addr.matchID(id) {
				matched = append(matched, id)
			}
		}
		if len(matched) == 0 {
			finding(CodeNoMatch, "matches no "+string(r.addr.kind)+" on this instance")
			continue
		}
		if name, ok := r.addr.attrName(); ok && !onlyWildcards(name) {
			has, reason := l.anyAttr(matched, name)
			switch {
			case reason != "":
				unchecked(reason)
			case !has:
				finding(CodeNoMatch, "its attribute "+strconv.Quote(name)+" matches no attribute of any entity it matches on this instance")
				continue
			}
		}
		if r.Effect != Allow {
			continue
		}
		for _, v := range r.Verbs {
			if allDenied(pr, r, matched, v) {
				finding(CodeUnreachable, "never decides "+v.String()+": every "+string(r.addr.kind)+
					" it matches is denied "+v.String()+" by a deny of this profile")
			}
		}
	}
}

// anyAttr reports whether one of the entities has an attribute matching the
// glob name. A non-empty reason means the attributes could not be read.
func (l *linter) anyAttr(entities []string, name string) (bool, string) {
	if !l.attrsAsked {
		l.attrsAsked = true
		ai, ok := l.inv.(AttributeInventory)
		if !ok {
			l.attrsErr = ReasonAttrsNotEnumerated
		} else if attrs, err := ai.Attributes(); err != nil {
			l.attrsErr = err.Error()
		} else {
			l.attrs = attrs
		}
	}
	if l.attrsErr != "" {
		return false, l.attrsErr
	}
	for _, id := range entities {
		for _, a := range l.attrs[id] {
			if glob.Match(a, name) {
				return true, ""
			}
		}
	}
	return false, ""
}

// sortedUnchecked orders the entries: by Kinds() order, the kind's own entry
// first, then the rule entries in file order (line, then column).
func (l *linter) sortedUnchecked() []Unchecked {
	rank := map[Kind]int{}
	for i, k := range Kinds() {
		rank[k] = i
	}
	sort.SliceStable(l.rules, func(i, j int) bool {
		a, b := l.rules[i], l.rules[j]
		if a.u.Kind != b.u.Kind {
			return rank[a.u.Kind] < rank[b.u.Kind]
		}
		if a.rule.Line != b.rule.Line || a.rule.Column != b.rule.Column {
			return a.rule.before(b.rule)
		}
		return a.u.Reason < b.u.Reason
	})
	out := []Unchecked{}
	ri := 0
	for _, k := range Kinds() {
		for _, u := range l.kinds {
			if u.Kind == k {
				out = append(out, u)
			}
		}
		for ri < len(l.rules) && l.rules[ri].u.Kind == k {
			out = append(out, l.rules[ri].u)
			ri++
		}
	}
	return out
}

// allDenied reports whether, for every matched id, one deny of the profile
// for v covers the allow's whole sub path (see Lint, CodeUnreachable).
func allDenied(pr *profile, allow Rule, matched []string, v Verb) bool {
	for _, id := range matched {
		covered := false
		for _, d := range pr.rules {
			if d.Effect == Deny && slices.Contains(d.Verbs, v) && d.addr.coversSubSet(allow.addr.kind, id, allow.addr.sub) {
				covered = true
				break
			}
		}
		if !covered {
			return false
		}
	}
	return true
}

// sortFindings orders by (Line, Code, Profile, Rule, Message): a total order.
func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		a, b := fs[i], fs[j]
		switch {
		case a.Line != b.Line:
			return a.Line < b.Line
		case a.Code != b.Code:
			return a.Code < b.Code
		case a.Profile != b.Profile:
			return a.Profile < b.Profile
		case a.Rule != b.Rule:
			return a.Rule < b.Rule
		default:
			return a.Message < b.Message
		}
	})
}
