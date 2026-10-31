package policy

import (
	"errors"
	"sort"
	"strconv"
	"strings"
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

// Unchecked records a kind whose rules lint could not check against the live
// instance, and why. An unchecked kind is never reported as clean (H-10: a
// sweep that could only read part of the instance is truncated).
type Unchecked struct {
	Kind   Kind
	Reason string
}

// LintReport is what Lint returns. Findings are ordered by (Line, Code,
// Profile, Rule); Unchecked by Kinds() order. Both are empty slices, not nil,
// when there is nothing to report — they render as [] in --json.
type LintReport struct {
	Findings  []Finding
	Unchecked []Unchecked
}

// ErrNotEnumerated is what an Inventory returns for a kind it does not list.
// Lint turns it into an Unchecked entry with the reason "not enumerated".
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

// Lint checks every profile the policy declares — not only the effective one:
// a typo in an inactive profile is a hole the day the owner switches to it.
//
//   - CodeImplicitAllowAll when p.Selection() is SelectedByImplicit and the
//     file declares at least one profile.
//   - CodeRedundantAllow for every allow rule in a profile without
//     `default: deny`.
//   - CodeNoMatch for every rule (allow or deny) whose kind inv enumerates
//     and whose pattern's id part matches none of the live ids. The bare `*`
//     pattern is never CodeNoMatch. Sub-resource segments are not checked live.
//   - CodeUnreachable for every (allow rule, verb) whose kind inv enumerates,
//     whose id part matches at least one live id, and where every matched id —
//     with the allow's own sub path appended literally — is denied for that
//     verb by some deny of the same profile. `where:` is ignored here: a deny
//     wins regardless.
//   - Unchecked for every kind that some rule names and inv cannot list
//     (inv nil: every such kind, reason "no live instance").
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
	live, unchecked := enumerate(p, inv)
	rep.Unchecked = append(rep.Unchecked, unchecked...)
	for _, pr := range p.profiles {
		rep.Findings = append(rep.Findings, lintProfile(pr, live)...)
	}
	sortFindings(rep.Findings)
	return rep
}

// enumerate asks inv once for every kind some rule names, in Kinds() order
// (never map order, H-16). A kind it cannot list is Unchecked.
func enumerate(p *Policy, inv Inventory) (map[Kind][]string, []Unchecked) {
	named := map[Kind]bool{}
	for _, pr := range p.profiles {
		for _, r := range pr.rules {
			if !r.addr.any {
				named[r.addr.kind] = true
			}
		}
	}
	live := map[Kind][]string{}
	var unchecked []Unchecked
	for _, k := range Kinds() {
		if !named[k] {
			continue
		}
		if inv == nil {
			unchecked = append(unchecked, Unchecked{Kind: k, Reason: "no live instance"})
			continue
		}
		ids, err := inv.IDs(k)
		switch {
		case errors.Is(err, ErrNotEnumerated):
			unchecked = append(unchecked, Unchecked{Kind: k, Reason: "not enumerated"})
		case err != nil:
			unchecked = append(unchecked, Unchecked{Kind: k, Reason: err.Error()})
		default:
			sorted := append([]string(nil), ids...)
			sort.Strings(sorted)
			live[k] = sorted
		}
	}
	return live, unchecked
}

// lintProfile reports one profile's rules.
func lintProfile(pr *profile, live map[Kind][]string) []Finding {
	var out []Finding
	for i := range pr.rules {
		r := pr.rules[i]
		finding := func(code, msg string) {
			out = append(out, Finding{Code: code, Profile: pr.name, Rule: r.Text(), Line: r.Line, Message: msg})
		}
		if r.Effect == Allow && pr.deflt != Deny {
			finding(CodeRedundantAllow, "profile "+strconv.Quote(pr.name)+" has no default: deny, so it already allows "+
				"whatever no deny matches: this allow (and any where: on it) decides nothing")
		}
		if r.addr.any {
			continue
		}
		ids, enumerated := live[r.addr.kind]
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
	return out
}

// allDenied reports whether every matched id — with the allow's own sub path
// appended literally — is denied v by some deny of the profile.
func allDenied(pr *profile, allow Rule, matched []string, v Verb) bool {
	sub := strings.Join(allow.addr.sub, "/")
	for _, id := range matched {
		res := Resource{Kind: allow.addr.kind, ID: id, Sub: sub}
		denied := false
		for _, d := range pr.rules {
			if d.Effect == Deny && d.covers(res, v) {
				denied = true
				break
			}
		}
		if !denied {
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
