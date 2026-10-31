package policy

import "errors"

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
//
// Implementation: evaluator stage.
func Lint(p *Policy, inv Inventory) LintReport {
	_, _ = p, inv
	return LintReport{Findings: []Finding{}, Unchecked: []Unchecked{}}
}
