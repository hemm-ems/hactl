package policy

import (
	"errors"
	"strconv"
	"strings"
)

// FileName is the policy file inside an instance directory (D-57). hactl
// reads it and never writes it; there is no command that does.
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
}

// Load reads `<instanceDir>/policy.yaml`.
//
//   - No file (and no `policy.yml`): an allow-all Policy, Exists() false,
//     Selection() SelectedByNoFile — byte-identical behaviour to having no
//     policy at all (D-58).
//   - A `policy.yml` (wrong extension) beside or instead of policy.yaml: a
//     *LoadError — a policy written under the wrong name must not silently be
//     no policy.
//   - A path that exists but is not a readable regular file: an error.
//   - A file that is not a valid policy (manual "Policy file format"): a
//     *LoadError listing every problem found, each with its 1-based line
//     (D-60). A YAML syntax error is reported as the one problem it is.
//   - Otherwise: the Policy, viewed through the profile D-61/D-62 select.
//
// Load never consults the environment or a flag for the profile (D-62).
//
// Implementation: evaluator stage.
func Load(instanceDir string) (*Policy, error) {
	return nil, notImplemented("Load(" + instanceDir + ")")
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

// Active returns the name of the profile this view evaluates, or "" when no
// profile is in effect (SelectedByNoFile, SelectedByImplicit). For a what-if
// view it is the `--as` profile.
//
// Implementation: evaluator stage.
func (p *Policy) Active() string { return "" }

// Selection says how Active was chosen.
//
// Implementation: evaluator stage.
func (p *Policy) Selection() Selection { return "" }

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
//
// Implementation: evaluator stage.
func (p *Policy) Profiles() []string { return nil }

// Default returns the default effect of the profile this view evaluates:
// Deny when it declares `default: deny`, Allow otherwise (including an
// AllowAll view).
//
// Implementation: evaluator stage.
func (p *Policy) Default() Effect { return "" }

// Rules returns the rules of the profile this view evaluates, deny rules
// first, then allow rules, each group ordered by Line, then Pattern. Empty for
// an AllowAll view.
//
// Implementation: evaluator stage.
func (p *Policy) Rules() []Rule { return nil }

// As returns a what-if view that evaluates the declared profile name instead
// of the effective one (D-62). The receiver is unchanged, the result's
// Selection is SelectedByWhatIf, and every Verdict it produces has WhatIf set.
// An undeclared name — including any name when there is no file — is an
// error naming the declared profiles.
//
// Implementation: evaluator stage.
func (p *Policy) As(name string) (*Policy, error) {
	return nil, notImplemented("As(" + name + ")")
}

// Rule is one entry of a profile's `allow:` or `deny:` mapping.
type Rule struct {
	Effect Effect
	// Pattern is the mapping key exactly as written (`entity:lock.*`).
	Pattern string
	// Written is the verb list exactly as written, in the file's order, with
	// `write` unexpanded — what the rule text shows.
	Written []string
	// Verbs is Written parsed by ParseVerbs: expanded, de-duplicated,
	// canonical order — what evaluation uses.
	Verbs []Verb
	// Where is the selector narrowing this allow, nil when none. Always nil on
	// a deny rule (`where:` narrows allows only).
	Where *Where
	// Line is the 1-based line of the pattern key in the file.
	Line int
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
	// Notes record, in a fixed order, every narrowed allow that matched the
	// resource and verb but whose `where:` could not be evaluated — no Live,
	// or a Live error — and therefore did NOT grant (fail closed):
	// `where not evaluated for allow "entity:light.*" [control]: <reason>`.
	// Ordered by the rule's Line, then Pattern. Empty, never nil-vs-empty
	// significant.
	Notes []string
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
// profile, by D-52:
//
//  1. AllowAll view: allowed, Rule RuleNoFile or RuleImplicit.
//  2. A deny rule matching r for v: denied, that rule decides.
//  3. Else an allow rule matching r for v whose `where:` (if any) holds:
//     allowed, that rule decides.
//  4. Else the profile default: RuleDefaultDeny or RuleDefaultAllow.
//
// Rule order in the file carries no meaning. When several rules of the
// deciding effect match, the one named is the first by (Line, Pattern) — a
// total order, so the answer never depends on map order (H-16). Matching is
// the manual's "Matching" section: kind equal (or the pattern `*`), id by the
// shared glob, a pattern's sub path a segment-wise prefix of r's (a parent
// covers its children; an allow on a child grants only the child).
//
// Decide never errors and never panics: r is assumed valid (ParseResource's
// output), and a live failure is a non-match recorded in Notes.
//
// Implementation: evaluator stage. The placeholder denies everything.
func (p *Policy) Decide(r Resource, v Verb, live Live) Verdict {
	_ = live
	return Verdict{
		Allowed: false,
		Rule:    "policy: not implemented (" + r.String() + " " + v.String() + ")",
		Level:   Level,
	}
}

// errNotImplemented marks a placeholder the evaluator stage replaces.
var errNotImplemented = errors.New("policy: not implemented")

func notImplemented(what string) error {
	return errors.Join(errNotImplemented, errors.New(what))
}
