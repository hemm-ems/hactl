package policy

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hemm-ems/hactl/internal/glob"
)

// FileName is the policy file inside an instance directory (D-57). hactl
// writes it in exactly two places, both the owner's and both behind a
// terminal: `policy activate` and `agent add`'s read-only starter (D-148).
const FileName = "policy.yaml"

// Level is the enforcement level every verdict carries (D-51): "A", a
// client-side guardrail. Levels B and C are not planned.
const Level = "A"

// Disclaimer is the level-A sentence `policy show` prints verbatim, in prose
// and as the `disclaimer` field of --json. Tests compare against this
// constant, so the wording lives in exactly one place.
const Disclaimer = "a guardrail against mistakes, not a security boundary: " +
	"the token in .env can do everything"

// Effect is what a rule or a profile default does.
type Effect string

// The two effects. A profile that does not declare `default:` has
// DefaultAllow (D-52: default deny applies only "within a profile that
// declares it").
const (
	Allow Effect = "allow"
	Deny  Effect = "deny"
)

// Selection says how the effective profile was chosen (D-61, D-62). It is the
// `selected_by` field of `policy show --json` and `policy check --json`.
type Selection string

// The ways a profile comes to be effective.
const (
	// SelectedByNoFile: there is no policy.yaml; everything is allowed (D-58).
	SelectedByNoFile Selection = "no-file"
	// SelectedByActive: the file's top-level `active:` names the profile.
	SelectedByActive Selection = "active"
	// SelectedByDefault: no `active:`, and the file declares a profile named
	// `default` (D-61).
	SelectedByDefault Selection = "default-profile"
	// SelectedByImplicit: no `active:` and no `default` profile; the implicit
	// default is allow-all (D-61) and `policy show` says so loudly.
	SelectedByImplicit Selection = "implicit-allow-all"
	// SelectedByWhatIf: `--as <profile>` — a read-only what-if (D-62). The
	// effective profile is unchanged; only this view evaluates another one.
	SelectedByWhatIf Selection = "what-if"
)

// Policy is a loaded, valid policy.yaml (or the allow-all stand-in for a
// missing one), viewed through one profile. A broken file never yields a
// Policy: Load returns a *LoadError instead (D-60).
//
// A Policy is immutable after Load; As returns a new view and never changes
// the receiver. Its fields are the evaluator's to define.
type Policy struct {
	source string
	exists bool
	// hash is the hex SHA-256 of the file's bytes as loaded, "" without a
	// file. Everything enforcement verifies and then relies on is tagged with
	// it, so nothing checked under one policy authorises a request under
	// another (D-72, R-7).
	hash string

	// profiles are the declared profiles, sorted by name; nil for no file.
	profiles []*profile
	// view is the profile this view evaluates, nil for an AllowAll view.
	view      *profile
	selection Selection
	// base* record the effective profile when this view is a what-if, so
	// `policy show --as` can say what it is NOT showing.
	baseActive    string
	baseSelection Selection
	// draft marks a file read by LoadDraft: it is never in effect (D-161).
	draft bool
}

// Draft reports whether this policy was read as a draft (LoadDraft), not as
// the instance's policy.yaml: nothing of it is enforced (D-161).
func (p *Policy) Draft() bool { return p.draft }

// profile is one declared profile.
type profile struct {
	name  string
	line  int
	deflt Effect
	// rules: deny rules first, then allow rules, each in file order (Line,
	// then Column).
	rules []Rule
	// indirectUnchecked is `indirect: unchecked` (D-135): what a written
	// automation, script or template would do is not checked.
	indirectUnchecked bool
	// indirectLine is the line of that key, 0 when absent.
	indirectLine int
}

// Load reads `<instanceDir>/policy.yaml`.
//
//   - No file (and no `policy.yml`): an allow-all Policy, Exists() false,
//     Selection() SelectedByNoFile — byte-identical behaviour to having no
//     policy at all (D-58).
//   - A `policy.yml` (wrong extension) beside or instead of policy.yaml: a
//     *LoadError — a policy written under the wrong name must not silently be
//     no policy.
//   - A policy.yaml or policy.yml that exists but is not a readable regular
//     file — a dangling symlink, a directory, no permission: a *LoadError
//     naming the path, never "no policy file" (D-60).
//   - A file that is not a valid policy (manual "Policy file format"): a
//     *LoadError listing every problem found, each with its 1-based line
//     (D-60). A YAML syntax error is reported as the one problem it is.
//   - Otherwise: the Policy, viewed through the profile D-61/D-62 select.
//
// Load never consults the environment or a flag for the profile (D-62).
func Load(instanceDir string) (*Policy, error) {
	return load(instanceDir)
}

// Problem is one reason a policy file is broken (D-60).
type Problem struct {
	// Line is the 1-based line of the offending YAML node; 0 only when the
	// problem has no line (e.g. both policy.yaml and policy.yml exist).
	Line int
	// Message says what is wrong in the manual's vocabulary, quoting the
	// offending value: `unknown verb "wirte" (want read, control, edit,
	// create, delete or write)`.
	Message string
}

// LoadError is a policy file hactl refuses to read as a policy (D-60). Problems
// are ordered by Line, then Message — never by map order (H-16).
type LoadError struct {
	// File is the path of the policy file as Load built it from instanceDir.
	File     string
	Problems []Problem
}

// Error renders one line per problem, `<file>:<line>: <message>` (just
// `<file>: <message>` when Line is 0), joined by newlines.
func (e *LoadError) Error() string {
	lines := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		if p.Line > 0 {
			lines = append(lines, e.File+":"+strconv.Itoa(p.Line)+": "+p.Message)
		} else {
			lines = append(lines, e.File+": "+p.Message)
		}
	}
	if len(lines) == 0 {
		return e.File + ": invalid policy"
	}
	return strings.Join(lines, "\n")
}

// Source is the path Load looked at: the policy file, present or not.
func (p *Policy) Source() string { return p.source }

// Exists reports whether a policy file was found.
func (p *Policy) Exists() bool { return p.exists }

// Hash is the content hash of the loaded file — the hex SHA-256 of its bytes —
// or "" when there is no file. A what-if view carries the hash of the file it
// came from.
func (p *Policy) Hash() string { return p.hash }

// InEffect reports whether a profile is in effect for this view: a file
// selected one (D-61, D-62) or a what-if names one. Enforcement acts only then
// (D-74); with no file or the implicit allow-all nothing is checked (D-58).
func (p *Policy) InEffect() bool { return !p.AllowAll() }

// Active returns the name of the profile this view evaluates, or "" when no
// profile is in effect (SelectedByNoFile, SelectedByImplicit). For a what-if
// view it is the `--as` profile.
func (p *Policy) Active() string {
	if p.view == nil {
		return ""
	}
	return p.view.name
}

// Selection says how Active was chosen.
func (p *Policy) Selection() Selection { return p.selection }

// IndirectChecked reports whether the profile in effect (or the what-if
// profile) checks what a written automation, script or template would do
// (D-56, D-133). Only `indirect: unchecked` on that profile switches it off
// (D-135); with no profile in effect nothing is checked anyway (D-58).
func (p *Policy) IndirectChecked() bool {
	return p.view == nil || !p.view.indirectUnchecked
}

// IndirectLine is the line of the active profile's `indirect: unchecked`, or
// 0 when the profile checks what written candidates would do.
func (p *Policy) IndirectLine() int {
	if p.view == nil || !p.view.indirectUnchecked {
		return 0
	}
	return p.view.indirectLine
}

// Effective returns the effective profile and how it was selected, ignoring
// any what-if: for a view from As it is the receiver's, never the `--as`
// profile.
func (p *Policy) Effective() (string, Selection) {
	if p.selection == SelectedByWhatIf {
		return p.baseActive, p.baseSelection
	}
	return p.Active(), p.selection
}

// WhatIf reports whether this view came from As — a `--as` what-if.
func (p *Policy) WhatIf() bool { return p.Selection() == SelectedByWhatIf }

// AllowAll reports whether this view allows everything without consulting any
// rule: no file (D-58) or the implicit default (D-61). A declared profile is
// never AllowAll, even if its rules happen to allow everything.
func (p *Policy) AllowAll() bool {
	s := p.Selection()
	return s == SelectedByNoFile || s == SelectedByImplicit
}

// Profiles returns every profile the file declares, sorted by name (byte
// order). Empty for no file.
func (p *Policy) Profiles() []string {
	out := make([]string, len(p.profiles))
	for i, pr := range p.profiles {
		out[i] = pr.name
	}
	return out
}

// Default returns the default effect of the profile this view evaluates:
// Deny when it declares `default: deny`, Allow otherwise (including an
// AllowAll view).
func (p *Policy) Default() Effect {
	if p.view == nil {
		return Allow
	}
	return p.view.deflt
}

// Rules returns the rules of the profile this view evaluates, deny rules
// first, then allow rules, each group in file order (Line, then Column).
// Empty for an AllowAll view.
func (p *Policy) Rules() []Rule {
	if p.view == nil {
		return []Rule{}
	}
	return append([]Rule(nil), p.view.rules...)
}

// As returns a what-if view that evaluates the declared profile name instead
// of the effective one (D-62). The receiver is unchanged, the result's
// Selection is SelectedByWhatIf, and every Verdict it produces has WhatIf set.
// An undeclared name — including any name when there is no file — is an
// error naming the declared profiles.
func (p *Policy) As(name string) (*Policy, error) {
	if !p.exists {
		return nil, fmt.Errorf("--as %q: there is no policy file, so there is no profile to evaluate", name)
	}
	pr := p.profile(name)
	if pr == nil {
		declared := p.Profiles()
		if len(declared) == 0 {
			return nil, fmt.Errorf("--as %q: the policy file declares no profiles", name)
		}
		return nil, fmt.Errorf("--as %q: no such profile (declared: %s)", name, strings.Join(declared, ", "))
	}
	base, baseSel := p.Effective()
	return &Policy{
		source:        p.source,
		exists:        p.exists,
		hash:          p.hash,
		profiles:      p.profiles,
		view:          pr,
		selection:     SelectedByWhatIf,
		baseActive:    base,
		baseSelection: baseSel,
		draft:         p.draft,
	}, nil
}

// Rule is one entry of a profile's `allow:` or `deny:` mapping.
type Rule struct {
	Effect Effect
	// Pattern is the mapping key exactly as written (`entity:lock.*`).
	Pattern string
	// Written is the verb list exactly as written, in the file's order, with
	// `write` unexpanded — what the rule text shows.
	Written []string
	// Verbs is Written parsed by ParseVerbs — de-duplicated, canonical order
	// — with `write` expanded to the write verbs that apply at the pattern
	// (D-110; for a deny also those that change it from above, D-125) and
	// every verb written by name kept: what evaluation uses.
	Verbs []Verb
	// Where is the selector narrowing this allow, nil when none. Always nil on
	// a deny rule (`where:` narrows allows only).
	Where *Where
	// Line is the 1-based line of the pattern key in the file.
	Line int
	// Column is the 1-based column of the pattern key. Two rules share a line
	// only in a flow mapping (`deny: {"entity:lock.*": [write], "entity:*":
	// [control]}`); the leftmost is first in the file (D-52).
	Column int

	addr address
	// applicable are the write verbs that apply at the pattern — at its
	// address or below it (D-110), and for a deny also every verb that
	// changes it from above (D-125).
	applicable []Verb
}

// Text renders the rule as a Verdict names it and `policy show` lists it:
//
//	deny "entity:lock.*" [read write]
//	allow "entity:light.*" [control] where area=wohnzimmer
//
// Effect, the pattern in double quotes (Go %q quoting), the written verbs
// space-separated in brackets, then — for a narrowed allow — ` where ` and the
// selector (Where.Text).
func (r Rule) Text() string {
	s := string(r.Effect) + " " + strconv.Quote(r.Pattern) + " [" + strings.Join(r.Written, " ") + "]"
	if r.Where != nil {
		s += " where " + r.Where.Text()
	}
	return s
}

// Kind is the kind the rule's pattern names, "" for the bare pattern `*`,
// which names every kind.
func (r Rule) Kind() Kind {
	if r.addr.any {
		return ""
	}
	return r.addr.kind
}

// MatchesID reports whether the rule's pattern reaches kind k's id: its kind
// and id match (`*` reaches everything), whatever its sub-resource path —
// `entity:*/registry/entity_id` reaches every entity, through a part of it.
// It is what `policy review` counts a rule's reach with.
func (r Rule) MatchesID(k Kind, id string) bool {
	if r.addr.any {
		return true
	}
	return r.addr.kind == k && r.addr.matchID(id)
}

// OnPart reports whether the rule's pattern names a part of a resource (a
// sub-resource path), not the resource as a whole.
func (r Rule) OnPart() bool { return !r.addr.any && len(r.addr.sub) > 0 }

// before is file order: line, then column — a total order over one file's
// rules, so no answer depends on map order (H-16).
func (r Rule) before(o Rule) bool {
	if r.Line != o.Line {
		return r.Line < o.Line
	}
	return r.Column < o.Column
}

// covers reports whether the rule applies to verb v on resource r, before
// any `where:` selector.
func (r Rule) covers(res Resource, v Verb) bool {
	return slices.Contains(r.Verbs, v) && r.addr.matches(res)
}

// headText is Text without the selector: what a Verdict note names.
func (r Rule) headText() string {
	return string(r.Effect) + " " + strconv.Quote(r.Pattern) + " [" + strings.Join(r.Written, " ") + "]"
}

// Where is a `where:` selector (D-55): live narrowing of an allow on an entity
// or device pattern. Within a key the values are alternatives (any-of); across
// keys all must hold. An empty Where never occurs — a selector with no keys is
// a D-60 problem.
type Where struct {
	// Area holds the area values as written; each is matched with glob.Match
	// against both the area_id and the area name of the resource's effective
	// area (H-8: an entity's own area, else its device's).
	Area []string
	// Label holds the label values as written; each is matched with
	// glob.Match against the label_id and the label name of every label on
	// the resource itself (an entity's own labels — HA does not inherit
	// labels from the device, H-8).
	Label []string
	// Line is the 1-based line of the selector's key under `where:` — where
	// lint reports a value that matches nothing (D-113).
	Line int
}

// Text renders the selector: `area=<v>` then `label=<v>`, each present only
// when set, separated by one space; several values of one key joined by `|`
// in the order written. E.g. `area=wohnzimmer|kueche label=agent_ok`.
func (w Where) Text() string {
	var parts []string
	if len(w.Area) > 0 {
		parts = append(parts, "area="+strings.Join(w.Area, "|"))
	}
	if len(w.Label) > 0 {
		parts = append(parts, "label="+strings.Join(w.Label, "|"))
	}
	return strings.Join(parts, " ")
}

// Rule texts a Verdict carries when no rule decided. Tests compare against
// these constants.
const (
	// RuleNoFile decides every question when there is no policy file (D-58).
	RuleNoFile = "no policy file"
	// RuleImplicit decides every question when the file has no `active:` and
	// no `default` profile (D-61).
	RuleImplicit = "implicit allow-all: no active: and no default profile"
	// RuleDefaultDeny decides when no rule matched in a `default: deny`
	// profile.
	RuleDefaultDeny = "default: deny"
	// RuleDefaultAllow decides when no rule matched in a profile without
	// `default: deny`.
	RuleDefaultAllow = "default: allow"
	// RuleNoDeny decides a deny-only check no deny matched (D-107): the
	// profile default and the allows do not decide it.
	RuleNoDeny = "no deny"
)

// Verdict is the answer to one (resource, verb) question.
type Verdict struct {
	Allowed bool
	// Rule is the deciding rule's Text(), or one of RuleNoFile, RuleImplicit,
	// RuleDefaultDeny, RuleDefaultAllow.
	Rule string
	// Line is the deciding rule's line, 0 when no rule decided.
	Line int
	// Profile is the profile that decided ("" for RuleNoFile/RuleImplicit).
	Profile string
	// Level is always the package constant Level ("A").
	Level string
	// WhatIf is set when the verdict came from an As view.
	WhatIf bool
	// Notes record every narrowed allow whose `where:` was tried and could
	// not be evaluated — no Live, or a Live error — and therefore did NOT
	// grant (fail closed):
	// `where not evaluated for allow "entity:light.*" [control]: <reason>`.
	// In the order tried, which is file order (Line, then Column). A selector
	// that was never tried — a deny decided, or an earlier allow already
	// granted — is not evaluated and not noted. Empty, never nil.
	Notes []string
	// Partial is set when the resource itself is allowed but a deny of the
	// same profile, for the same verb, covers a strict descendant of it
	// (D-66). For a write verb the verdict is then denied and Rule/Line name
	// the first descendant deny; for Read it stays allowed, Rule/Line name
	// what allowed it, and PartialDenies are the parts a read must mask.
	Partial bool
	// PartialDenies are the descendant denies of a Partial verdict, in file
	// order (Line, then Column). Empty, never nil.
	PartialDenies []RuleRef
	// UnmetWhere records every narrowed allow that matched the resource and
	// verb, was tried, and whose `where:` evaluated to false (D-112), in the
	// order tried. Non-empty only when no allow granted — the profile default
	// decided. A selector that could not be evaluated is a Notes entry
	// instead; one never tried is in neither. Empty, never nil.
	UnmetWhere []UnmetWhere
}

// UnmetWhere is one narrowed allow whose selector did not hold (D-112): the
// allow's Text() with its selector, its line, and the first key that failed,
// area before label — `light.kitchen has no label pg_core_label`,
// `light.kitchen is in area kitchen_id, not wohnzimmer or buero`,
// `device_tracker.jan has no area`.
type UnmetWhere struct {
	Rule   string `json:"rule"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// RuleRef names one rule: its Text() and its line.
type RuleRef struct {
	Rule string
	Line int
}

// Ref is a registry object a `where:` selector compares against: its id and
// its display name.
type Ref struct {
	ID   string
	Name string
}

// Live answers the questions a `where:` selector asks, at the moment of the
// check (D-55: evaluated live on every check, so a relabel takes effect at
// once). The CLI implements it over the entity, device, area and label
// registries; a test implements it over a fixture.
//
// A nil Live, or a method that returns an error, makes every where-narrowed
// allow NOT match (fail closed); the Verdict says so in Notes.
type Live interface {
	// EntityArea returns the entity's effective area — its own area_id when
	// set, else its device's (H-8) — and ok=false when it has none (including
	// an entity that is not in the entity registry).
	EntityArea(entityID string) (area Ref, ok bool, err error)
	// EntityLabels returns the labels on the entity's own registry entry
	// (none for an entity not in the registry).
	EntityLabels(entityID string) ([]Ref, error)
	// DeviceArea returns the device's area, ok=false when it has none.
	DeviceArea(deviceID string) (area Ref, ok bool, err error)
	// DeviceLabels returns the labels on the device's registry entry.
	DeviceLabels(deviceID string) ([]Ref, error)
}

// Decide answers whether verb v on resource r is allowed under this view's
// profile, by D-52 and D-66:
//
//  1. AllowAll view: allowed, Rule RuleNoFile or RuleImplicit, never partial.
//  2. A deny rule matching r for v: denied, that rule decides.
//  3. Else the allow rules matching r for v, tried in file order: the first
//     without a `where:`, or the first whose `where:` holds, grants and
//     decides. A selector is evaluated only while it can still change the
//     verdict or the rule named — none after an allow that already grants.
//  4. Else the profile default: RuleDefaultDeny or RuleDefaultAllow.
//  5. If 3 or 4 allowed r but a deny for v covers a strict descendant of r
//     (same kind and id, one or more further sub-resource segments) and v
//     applies where that deny sits (D-110), the verdict is Partial: a write
//     verb is denied, naming the first such deny; Read stays allowed and
//     lists them.
//
// Rule order in the file carries no meaning for the verdict. When several
// rules of the deciding effect match, the one named is the first in the file
// (Line, then Column) — a total order, so the answer never depends on map
// order (H-16). Matching is the manual's "Matching" section: kind equal (or
// the pattern `*`), the full id by the shared glob (D-63), a pattern's sub
// path a segment-wise prefix of r's (a parent covers its children; an allow
// on a child grants only the child).
//
// Decide never errors and never panics: r is assumed valid (ParseResource's
// output), and a live failure is a non-match recorded in Notes.
func (p *Policy) Decide(r Resource, v Verb, live Live) Verdict {
	out := newVerdict(p.WhatIf())
	switch p.selection {
	case SelectedByNoFile:
		out.Allowed, out.Rule = true, RuleNoFile
		return out
	case SelectedByImplicit:
		out.Allowed, out.Rule = true, RuleImplicit
		return out
	}
	pr := p.view
	out.Profile = pr.name

	// Rules are held deny-first, each group in file order: the first match of
	// the deciding effect is the one named (H-16).
	for i := range pr.rules {
		rule := &pr.rules[i]
		if rule.Effect == Deny && rule.covers(r, v) {
			out.Rule, out.Line = rule.Text(), rule.Line
			return out
		}
	}
	var granted *Rule
	for i := range pr.rules {
		rule := &pr.rules[i]
		if rule.Effect != Allow || !rule.covers(r, v) {
			continue
		}
		if rule.Where != nil {
			unmet, reason := rule.Where.holds(rule.addr.kind, r.ID, live)
			if reason != "" {
				out.Notes = append(out.Notes, "where not evaluated for "+rule.headText()+": "+reason)
				continue
			}
			if unmet != "" {
				out.UnmetWhere = append(out.UnmetWhere, UnmetWhere{Rule: rule.Text(), Line: rule.Line, Reason: unmet})
				continue
			}
		}
		granted = rule
		break
	}
	switch {
	case granted != nil:
		out.UnmetWhere = []UnmetWhere{}
		out.Allowed, out.Rule, out.Line = true, granted.Text(), granted.Line
	case pr.deflt == Deny:
		out.Rule = RuleDefaultDeny
		return out
	default:
		out.Allowed, out.Rule = true, RuleDefaultAllow
	}

	for i := range pr.rules {
		rule := &pr.rules[i]
		if rule.Effect == Deny && slices.Contains(rule.Verbs, v) && rule.appliesHere(v) && rule.addr.coversDescendant(r) {
			out.PartialDenies = append(out.PartialDenies, RuleRef{Rule: rule.Text(), Line: rule.Line})
		}
	}
	if len(out.PartialDenies) > 0 {
		out.Partial = true
		if v != Read {
			first := out.PartialDenies[0]
			out.Allowed, out.Rule, out.Line = false, first.Rule, first.Line
		}
	}
	return out
}

// DecideDenyOnly answers verb v on r for its denies only (D-107): denied when a
// deny of the effective profile for v covers r, or — for a write verb — a
// strict descendant of r where v applies (D-66's partial, D-110); otherwise
// allowed with RuleNoDeny.
// The profile default and the allows never decide: a deny-only check is how a
// service call consults a typed address it reaches (an automation, a script,
// a helper, a template entity) whose grant is decided on the entity. The
// AllowAll views answer as Decide.
func (p *Policy) DecideDenyOnly(r Resource, v Verb) Verdict {
	out := newVerdict(p.WhatIf())
	switch p.selection {
	case SelectedByNoFile:
		out.Allowed, out.Rule = true, RuleNoFile
		return out
	case SelectedByImplicit:
		out.Allowed, out.Rule = true, RuleImplicit
		return out
	}
	pr := p.view
	out.Profile = pr.name
	for i := range pr.rules {
		rule := &pr.rules[i]
		if rule.Effect == Deny && rule.covers(r, v) {
			out.Rule, out.Line = rule.Text(), rule.Line
			return out
		}
	}
	out.Allowed, out.Rule = true, RuleNoDeny
	for i := range pr.rules {
		rule := &pr.rules[i]
		if rule.Effect == Deny && slices.Contains(rule.Verbs, v) && rule.appliesHere(v) && rule.addr.coversDescendant(r) {
			out.PartialDenies = append(out.PartialDenies, RuleRef{Rule: rule.Text(), Line: rule.Line})
		}
	}
	if len(out.PartialDenies) > 0 {
		out.Partial = true
		if v != Read {
			first := out.PartialDenies[0]
			out.Allowed, out.Rule, out.Line = false, first.Rule, first.Line
		}
	}
	return out
}

// DecideKindWideDenyOnly is DecideKindWide for its denies only (D-107): denied
// when a deny for v names kind k (whatever its id) and covers sub, or — for a
// write verb — a strict descendant of sub; otherwise allowed with RuleNoDeny.
// It is the check on a typed address a service call reaches but whose id this
// invocation could not read.
func (p *Policy) DecideKindWideDenyOnly(k Kind, sub string, v Verb) Verdict {
	out := newVerdict(p.WhatIf())
	switch p.selection {
	case SelectedByNoFile:
		out.Allowed, out.Rule = true, RuleNoFile
		return out
	case SelectedByImplicit:
		out.Allowed, out.Rule = true, RuleImplicit
		return out
	}
	pr := p.view
	out.Profile = pr.name
	subSegs := splitPath(sub)
	for i := range pr.rules {
		rule := &pr.rules[i]
		if rule.Effect != Deny || !slices.Contains(rule.Verbs, v) {
			continue
		}
		if rule.addr.any || (rule.addr.kind == k && matchSubPrefix(rule.addr.sub, subSegs)) {
			out.Rule, out.Line = rule.Text(), rule.Line
			return out
		}
	}
	out.Allowed, out.Rule = true, RuleNoDeny
	for i := range pr.rules {
		rule := &pr.rules[i]
		if rule.Effect == Deny && slices.Contains(rule.Verbs, v) && rule.appliesHere(v) && !rule.addr.any && rule.addr.kind == k &&
			len(rule.addr.sub) > len(subSegs) && matchSubPrefix(rule.addr.sub[:len(subSegs)], subSegs) {
			out.PartialDenies = append(out.PartialDenies, RuleRef{Rule: rule.Text(), Line: rule.Line})
		}
	}
	if len(out.PartialDenies) > 0 {
		out.Partial = true
		if v != Read {
			first := out.PartialDenies[0]
			out.Allowed, out.Rule, out.Line = false, first.Rule, first.Line
		}
	}
	return out
}

// DecideKindWide answers verb v on EVERY id of kind k — `<kind>:*[/<sub>]` — by
// D-75: the check a command makes when Home Assistant mints the id (area,
// floor and label create, a new config entry), so there is no id to check yet.
//
//  1. AllowAll view: allowed, as Decide.
//  2. A deny for v whose pattern names kind k (or is `*`), whatever its id, and
//     whose sub path covers sub: denied, the first such deny (line, then column)
//     decides — a deny on one id refuses a check on all of them.
//  3. Else an allow for v without `where:` whose id matches every id of the
//     kind — each id segment only `*` (or a `**` in a file id), as many as the
//     kind's id spans — and whose sub path covers sub grants, the first in file
//     order. A narrower allow covers some ids, not all, and grants nothing here.
//  4. Else the profile default.
//  5. If 3 or 4 allowed, a deny for v on a strict descendant of sub on kind k
//     (any id), where v applies (D-110), makes the verdict partial: a write
//     verb is denied naming the first such deny, read lists them (D-66).
//
// sub is the `/`-joined sub path below each id ("" = the resource itself). The
// verdict never consults a where: selector, so it needs no Live.
func (p *Policy) DecideKindWide(k Kind, sub string, v Verb) Verdict {
	out := newVerdict(p.WhatIf())
	switch p.selection {
	case SelectedByNoFile:
		out.Allowed, out.Rule = true, RuleNoFile
		return out
	case SelectedByImplicit:
		out.Allowed, out.Rule = true, RuleImplicit
		return out
	}
	pr := p.view
	out.Profile = pr.name
	subSegs := splitPath(sub)

	for i := range pr.rules {
		rule := &pr.rules[i]
		if rule.Effect != Deny || !slices.Contains(rule.Verbs, v) {
			continue
		}
		if rule.addr.any || (rule.addr.kind == k && matchSubPrefix(rule.addr.sub, subSegs)) {
			out.Rule, out.Line = rule.Text(), rule.Line
			return out
		}
	}
	var granted *Rule
	for i := range pr.rules {
		rule := &pr.rules[i]
		if rule.Effect != Allow || rule.Where != nil || !slices.Contains(rule.Verbs, v) {
			continue
		}
		if rule.addr.any || (rule.addr.kind == k && rule.addr.matchesEveryID() && matchSubPrefix(rule.addr.sub, subSegs)) {
			granted = rule
			break
		}
	}
	switch {
	case granted != nil:
		out.Allowed, out.Rule, out.Line = true, granted.Text(), granted.Line
	case pr.deflt == Deny:
		out.Rule = RuleDefaultDeny
		return out
	default:
		out.Allowed, out.Rule = true, RuleDefaultAllow
	}
	for i := range pr.rules {
		rule := &pr.rules[i]
		if rule.Effect == Deny && slices.Contains(rule.Verbs, v) && rule.appliesHere(v) && !rule.addr.any && rule.addr.kind == k &&
			len(rule.addr.sub) > len(subSegs) && matchSubPrefix(rule.addr.sub[:len(subSegs)], subSegs) {
			out.PartialDenies = append(out.PartialDenies, RuleRef{Rule: rule.Text(), Line: rule.Line})
		}
	}
	if len(out.PartialDenies) > 0 {
		out.Partial = true
		if v != Read {
			first := out.PartialDenies[0]
			out.Allowed, out.Rule, out.Line = false, first.Rule, first.Line
		}
	}
	return out
}

// newVerdict is a verdict before any rule decided: level A, the what-if mark,
// and every list empty, never nil.
func newVerdict(whatIf bool) Verdict {
	return Verdict{Level: Level, WhatIf: whatIf, Notes: []string{}, PartialDenies: []RuleRef{}, UnmetWhere: []UnmetWhere{}}
}

// profile returns the declared profile name, or nil.
func (p *Policy) profile(name string) *profile {
	for _, pr := range p.profiles {
		if pr.name == name {
			return pr
		}
	}
	return nil
}

// holds evaluates the selector for the resource id of kind k. It returns why
// the selector does not hold ("" when it holds) — the first key that failed,
// area before label (D-112) — and, apart from that, a non-empty reason when it
// could not be evaluated, which never grants either (fail closed).
func (w Where) holds(k Kind, id string, live Live) (unmet, reason string) {
	if live == nil {
		return "", "no live instance to evaluate it against"
	}
	if len(w.Area) > 0 {
		var area Ref
		var ok bool
		var err error
		if k == KindDevice {
			area, ok, err = live.DeviceArea(id)
		} else {
			area, ok, err = live.EntityArea(id)
		}
		switch {
		case err != nil:
			return "", err.Error()
		case !ok:
			return id + " has no area", ""
		case !anyRefMatches([]Ref{area}, w.Area):
			return id + " is in area " + area.ID + ", not " + strings.Join(w.Area, " or "), ""
		}
	}
	if len(w.Label) > 0 {
		var labels []Ref
		var err error
		if k == KindDevice {
			labels, err = live.DeviceLabels(id)
		} else {
			labels, err = live.EntityLabels(id)
		}
		if err != nil {
			return "", err.Error()
		}
		if !anyRefMatches(labels, w.Label) {
			return id + " has no label " + strings.Join(w.Label, " or "), ""
		}
	}
	return "", ""
}

// anyRefMatches: one of refs matches one of values, by id or by name.
func anyRefMatches(refs []Ref, values []string) bool {
	for _, ref := range refs {
		for _, v := range values {
			if glob.Match(ref.ID, v) || (ref.Name != "" && glob.Match(ref.Name, v)) {
				return true
			}
		}
	}
	return false
}

