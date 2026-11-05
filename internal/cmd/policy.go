package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// The policy family (manual "Policy (permission guardrail)", D-51..D-62).
//
// Three read-only commands over the model. Writes are enforced by every write
// command while a profile is in effect (policy_enforce.go, H-29); reads are
// not yet, which show and check say. The profile is
// chosen by the file alone (D-62): there is deliberately no --profile flag
// and no environment variable; `--as` is a what-if that never changes the
// effective profile.

var (
	flagPolicyAs           string
	flagPolicyAllowPartial bool
	// flagPolicyFile is --file <draft>: read this file instead of policy.yaml
	// (D-161). The answer is the draft's and says so; nothing of it is in effect.
	flagPolicyFile string
)

var policyCmd = family(&cobra.Command{
	Use:   "policy",
	Short: "Read the instance's permission policy (a guardrail against mistakes, not a security boundary)",
	Long: "Read <instance>/policy.yaml: which profile is in effect, what it allows, and whether its rules " +
		"mean what their author wrote. " + policy.Disclaimer + ". Only `policy activate`, in the owner's terminal, writes the file. " +
		"Writes are enforced while a profile is in effect; reads are not yet.",
})

var policyShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the effective profile, its rules, the level and the file it was loaded from",
	Long: "Print the policy file (or `no policy file`), the level-A disclaimer, the effective profile and how " +
		"it was selected, its default, its rules (deny first, then allow, each with its line) and the declared " +
		"profiles. A broken file is reported problem by problem (exit 1). --as <profile> is a read-only what-if.",
	Args: takesNone(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runPolicyShow(cmd.OutOrStdout(), flagPolicyAs, cmd.Flags().Changed("as"), flagPolicyFile)
	},
}

var policyCheckCmd = &cobra.Command{
	Use:   "check <kind>:<id>[/<sub>] <verb>",
	Short: "Answer one question: is <verb> on <resource> allowed? Exit 0 allowed, 1 denied",
	Long: "Evaluate one resource and one verb (read, control, edit, create, delete) under the effective " +
		"profile and name the rule that decided; write answers every write verb that applies at the resource, " +
		"one verdict each, allowed only when all are. The exit code is the verdict: 0 allowed, 1 denied (with the " +
		"verdict on stdout). An invalid resource or verb, an unknown --as profile or a broken file is refused: " +
		"exit 1, nothing on stdout. A partial verdict (a deny covers a part of the resource) denies a write verb " +
		"and allows read, listing the denied parts. Contacts Home Assistant only when a where: selector can " +
		"still change the verdict.",
	Args: takes(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPolicyCheck(cmd.Context(), cmd.OutOrStdout(), args[0], args[1], flagPolicyAs, cmd.Flags().Changed("as"), flagPolicyFile)
	},
}

var policyLintCmd = &cobra.Command{
	Use:   "lint",
	Short: "Validate the policy file, then check every rule of every profile against the live instance",
	Long: "Report rules that cannot mean what their author wrote: broken-file problems, an implicit " +
		"allow-all, redundant allows, patterns that match nothing on the instance, and allows a deny makes " +
		"unreachable. A kind lint could not enumerate is listed as unchecked and is never reported clean; " +
		"--allow-partial accepts that (it never hides a finding). Exit 0 clean, 1 otherwise.",
	Args: takesNone(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runPolicyLint(cmd.Context(), cmd.OutOrStdout(), flagPolicyAllowPartial, flagPolicyFile)
	},
}

func init() {
	const asHelp = "what-if: evaluate this declared profile instead of the effective one (read-only; the file's active: still decides)"
	policyShowCmd.Flags().StringVar(&flagPolicyAs, "as", "", asHelp)
	policyCheckCmd.Flags().StringVar(&flagPolicyAs, "as", "", asHelp)
	const fileHelp = "read this draft instead of policy.yaml — the answer is the draft's, and nothing of it is in effect"
	policyShowCmd.Flags().StringVar(&flagPolicyFile, "file", "", fileHelp)
	policyCheckCmd.Flags().StringVar(&flagPolicyFile, "file", "", fileHelp)
	policyLintCmd.Flags().StringVar(&flagPolicyFile, "file", "", fileHelp)
	policyLintCmd.Flags().BoolVar(&flagPolicyAllowPartial, "allow-partial", false,
		"accept a lint that could not enumerate every kind its rules name (unchecked kinds are still listed)")
	policyCmd.AddCommand(policyShowCmd, policyCheckCmd, policyLintCmd)
	rootCmd.AddCommand(policyCmd)
}

// policyInstanceDir resolves the instance the same way every other command
// does — the policy belongs to the instance whose .env hactl would use.
func policyInstanceDir() (*config.Config, error) {
	return config.Load(flagDir)
}

// loadPolicyView loads the policy — the draft at file when one is named
// (D-161), else the instance's policy.yaml — and applies a what-if. A broken
// file comes back as the *policy.LoadError, a missing draft as the
// *policy.DraftMissingError.
func loadPolicyView(dir, as string, asSet bool, file string) (*policy.Policy, error) {
	pol, err := loadPolicyFile(dir, file)
	if err != nil {
		return nil, err
	}
	if !asSet {
		return pol, nil
	}
	return pol.As(as)
}

// loadPolicyFile reads the draft at file, or the instance's policy.yaml when
// file is "".
func loadPolicyFile(dir, file string) (*policy.Policy, error) {
	if file == "" {
		return policy.Load(dir)
	}
	pol, _, err := policy.LoadDraft(draftPath(dir, file))
	return pol, err
}

// draftPath resolves a draft path (D-161): a relative one against the
// instance directory, beside .env — never the working directory, so the
// skill's `--file policy.draft.yaml` names the instance's draft from anywhere.
func draftPath(dir, file string) string {
	if filepath.IsAbs(file) {
		return filepath.Clean(file)
	}
	return filepath.Join(dir, file)
}

// fileText is the `file:` line's value: the path, and for a draft that it is
// not in effect (D-161).
func fileText(pol *policy.Policy) string {
	if pol.Draft() {
		return pol.Source() + " (draft, not in effect)"
	}
	return pol.Source()
}

// policyBrokenError is the exit-1 error of a show/lint that reported a broken
// file: the report is on stdout, the one-line summary on stderr (D-117).
type policyBrokenError struct {
	n    int
	file string
}

func (e *policyBrokenError) Error() string {
	return fmt.Sprintf("%s: %d problem(s) in %s", enforce.ClassBroken, e.n, e.file)
}

// ExitCode is 1. It has no PolicyClass: show and lint report a broken file,
// they are not refused by it (D-60).
func (e *policyBrokenError) ExitCode() int { return 1 }

// --- policy show -------------------------------------------------------------

type policyShowRule struct {
	Effect   string           `json:"effect"`
	Pattern  string           `json:"pattern"`
	Verbs    []string         `json:"verbs"`
	Expanded []string         `json:"expanded"`
	Where    *policyShowWhere `json:"where"`
	Line     int              `json:"line"`
}

type policyShowWhere struct {
	Area  []string `json:"area"`
	Label []string `json:"label"`
}

type policyProblem struct {
	Line    int    `json:"line"`
	Message string `json:"message"`
}

type policyShowDoc struct {
	File       string          `json:"file"`
	Exists     bool            `json:"exists"`
	Valid      bool            `json:"valid"`
	Problems   []policyProblem `json:"problems"`
	Level      string          `json:"level"`
	Disclaimer string          `json:"disclaimer"`
	Enforced   bool            `json:"enforced"`
	// EnforcedVerbs are the verbs commands enforce in this version, in the
	// order read, control, edit, create, delete — the four write verbs while
	// a profile is in effect, [] otherwise (D-70).
	EnforcedVerbs []string         `json:"enforced_verbs"`
	Profile       string           `json:"profile"`
	SelectedBy    string           `json:"selected_by"`
	WhatIf        bool             `json:"what_if"`
	AllowAll      bool             `json:"allow_all"`
	Default       string           `json:"default"`
	Profiles      []string         `json:"profiles"`
	Rules         []policyShowRule `json:"rules"`
	// Hint is the owner sentence, only on a broken file (D-117).
	Hint string `json:"hint,omitempty"`
	// Indirect is "unchecked" when the profile switched off the check of
	// what written automations, scripts and templates would do (D-135),
	// "checked" while a profile is in effect, "" otherwise.
	Indirect string `json:"indirect,omitempty"`
	// Draft is true when --file named a draft (D-161).
	Draft bool `json:"draft"`
}

func runPolicyShow(w io.Writer, as string, asSet bool, file string) error {
	cfg, err := policyInstanceDir()
	if err != nil {
		return err
	}
	pol, err := loadPolicyView(cfg.Dir, as, asSet, file)
	var le *policy.LoadError
	switch {
	case errors.As(err, &le):
		return renderPolicyBroken(w, le, file != "")
	case err != nil:
		return err
	}

	enforced, verbs := policyEnforcement(pol)
	doc := policyShowDoc{
		File: pol.Source(), Exists: pol.Exists(), Valid: true, Problems: []policyProblem{},
		Level: policy.Level, Disclaimer: policy.Disclaimer, Enforced: enforced, EnforcedVerbs: verbs,
		Profile: pol.Active(), SelectedBy: string(pol.Selection()), WhatIf: pol.WhatIf(),
		AllowAll: pol.AllowAll(), Default: string(pol.Default()),
		Profiles: pol.Profiles(), Rules: []policyShowRule{}, Draft: pol.Draft(),
	}
	if enforced {
		doc.Indirect = "checked"
		if !pol.IndirectChecked() {
			doc.Indirect = "unchecked"
		}
	}
	for _, r := range pol.Rules() {
		row := policyShowRule{
			Effect: string(r.Effect), Pattern: r.Pattern, Verbs: append([]string{}, r.Written...),
			Expanded: verbNames(r.Verbs), Line: r.Line,
		}
		if r.Where != nil {
			row.Where = &policyShowWhere{Area: nonNil(r.Where.Area), Label: nonNil(r.Where.Label)}
		}
		doc.Rules = append(doc.Rules, row)
	}

	// The report is a document, never capped: a cut would hide rules (manual
	// "Output conventions").
	markStructuredOutput()
	if flagJSON {
		return writeJSON(w, doc)
	}

	if !pol.Exists() {
		_, _ = fmt.Fprintf(w, "no policy file (looked for %s): everything is allowed, exactly as without a policy\n", pol.Source())
	} else {
		_, _ = fmt.Fprintf(w, "file: %s\n", fileText(pol))
	}
	_, _ = fmt.Fprintf(w, "%s\n", policy.Disclaimer)
	_, _ = fmt.Fprintf(w, "level: %s\n", policy.Level)
	switch {
	case pol.Draft():
		_, _ = fmt.Fprintln(w, "enforced: nothing — a draft is not in effect; hactl policy activate puts it in effect")
	case enforced:
		_, _ = fmt.Fprintln(w, "enforced: writes — reads not yet")
		if !pol.IndirectChecked() {
			_, _ = fmt.Fprintf(w, "indirect: unchecked (line %d) — what written automations, scripts and templates would do is not checked\n", pol.IndirectLine())
		}
	default:
		_, _ = fmt.Fprintln(w, "enforced: nothing — no profile is in effect, so every command runs as without a policy")
	}
	if !pol.Exists() {
		return nil
	}
	if pol.WhatIf() {
		base, baseSel := pol.Effective()
		effective := "none (allow-all)"
		if base != "" {
			effective = base
		}
		_, _ = fmt.Fprintf(w, "what-if: --as %s — a read-only view; the effective profile is still %s (selected by %s)\n",
			pol.Active(), effective, baseSel)
	}
	if pol.Selection() == policy.SelectedByImplicit {
		declared := "none"
		if ps := pol.Profiles(); len(ps) > 0 {
			declared = strings.Join(ps, ", ")
		}
		_, _ = fmt.Fprintf(w, "ALLOW-ALL: the file has no active: and no profile named default, so no profile is in effect "+
			"and everything is allowed (declared profiles: %s)\n", declared)
		return nil
	}
	_, _ = fmt.Fprintf(w, "profile: %s (selected by %s)\n", pol.Active(), pol.Selection())
	_, _ = fmt.Fprintf(w, "default: %s\n", pol.Default())
	if len(doc.Rules) == 0 {
		_, _ = fmt.Fprintln(w, "rules: none")
	} else {
		_, _ = fmt.Fprintln(w, "rules:")
		for _, r := range pol.Rules() {
			_, _ = fmt.Fprintf(w, "  line %-4d %s%s\n", r.Line, r.Text(), expansionText(r))
		}
	}
	_, _ = fmt.Fprintf(w, "profiles: %s\n", strings.Join(pol.Profiles(), ", "))
	return nil
}

// expansionText is what a rule holding `write` expands to at its address,
// ` → [<verbs>]`, "" for a list without `write`, which means what it says
// (D-128).
func expansionText(r policy.Rule) string {
	if !slices.Contains(r.Written, policy.WriteAlias) {
		return ""
	}
	return " → [" + strings.Join(verbNames(r.Verbs), " ") + "]"
}

func renderPolicyBroken(w io.Writer, le *policy.LoadError, draft bool) error {
	problems := make([]policyProblem, len(le.Problems))
	for i, p := range le.Problems {
		problems[i] = policyProblem{Line: p.Line, Message: p.Message}
	}
	markStructuredOutput()
	brokenErr := &policyBrokenError{n: len(problems), file: le.File}
	if flagJSON {
		if err := writeJSON(w, policyShowDoc{
			File: le.File, Exists: true, Valid: false, Problems: problems,
			Level: policy.Level, Disclaimer: policy.Disclaimer, Enforced: false, EnforcedVerbs: []string{},
			Profiles: []string{}, Rules: []policyShowRule{}, Hint: enforce.BrokenHint, Draft: draft,
		}); err != nil {
			return err
		}
		return brokenErr
	}
	if draft {
		_, _ = fmt.Fprintf(w, "file: %s (draft, not in effect)\n", le.File)
	} else {
		_, _ = fmt.Fprintf(w, "file: %s\n", le.File)
	}
	_, _ = fmt.Fprintf(w, "%s\n", policy.Disclaimer)
	_, _ = fmt.Fprintln(w, enforce.BrokenReport(le))
	return brokenErr
}

// policyEnforcement is what commands enforce under pol's EFFECTIVE profile —
// a what-if (`--as`) changes the view, not what is enforced: the write verbs
// while a profile is in effect, nothing otherwise (D-70, D-74).
func policyEnforcement(pol *policy.Policy) (bool, []string) {
	if pol.Draft() {
		return false, []string{} // a draft is never in effect (D-161)
	}
	_, sel := pol.Effective()
	if sel == policy.SelectedByNoFile || sel == policy.SelectedByImplicit {
		return false, []string{}
	}
	return true, verbNames([]policy.Verb{policy.Control, policy.Edit, policy.Create, policy.Delete})
}

func verbNames(vs []policy.Verb) []string {
	out := make([]string, len(vs))
	for i, v := range vs {
		out[i] = v.String()
	}
	return out
}

// --- policy check ------------------------------------------------------------

type policyCheckDoc struct {
	Resource   string   `json:"resource"`
	Verb       string   `json:"verb"`
	Allowed    bool     `json:"allowed"`
	Rule       string   `json:"rule"`
	Line       int      `json:"line"`
	Profile    string   `json:"profile"`
	SelectedBy string   `json:"selected_by"`
	WhatIf     bool     `json:"what_if"`
	Level      string   `json:"level"`
	Enforced   bool     `json:"enforced"`
	Notes      []string `json:"notes"`
	// Draft is true when --file named a draft (D-161).
	Draft bool `json:"draft"`
	// Partial and PartialDenies are D-66's partial verdict: the resource is
	// allowed but a deny covers a strict descendant of it.
	Partial       bool                `json:"partial"`
	PartialDenies []policyPartialDeny `json:"partial_denies"`
	// UnmetWhere are the narrowed allows tried whose selector did not hold
	// (D-112); [] when none.
	UnmetWhere []policy.UnmetWhere `json:"unmet_where"`

	// denyOnlyDefault: a deny-only check (D-107) denied by the default alone,
	// which no command enforces — the text answer says so. scriptRun is the
	// allowed script:<id>/run that decides a script's run instead (D-128).
	denyOnlyDefault bool
	scriptRun       string
}

// policyCheckWriteDoc is `policy check <resource> write --json` (D-120): one
// verdict per write verb that applies at or below the resource, each exactly
// the one-verb document.
type policyCheckWriteDoc struct {
	Resource   string           `json:"resource"`
	Verb       string           `json:"verb"`
	Allowed    bool             `json:"allowed"`
	Profile    string           `json:"profile"`
	SelectedBy string           `json:"selected_by"`
	WhatIf     bool             `json:"what_if"`
	Level      string           `json:"level"`
	Enforced   bool             `json:"enforced"`
	Draft      bool             `json:"draft"`
	Verdicts   []policyCheckDoc `json:"verdicts"`
}

type policyPartialDeny struct {
	Rule string `json:"rule"`
	Line int    `json:"line"`
}

// policyDeniedError carries a denial's exit code. The verdict is already on
// stdout; this is the exit 1 that makes it machine-readable.
type policyDeniedError struct{ what string }

func (e *policyDeniedError) Error() string { return "denied by policy: " + e.what }

func (e *policyDeniedError) ExitCode() int { return 1 }

// enforcedVerdict reports whether a command would act on a verdict for verb v
// under pol: a write verb while a profile is in effect (D-70). Reads are not
// enforced in this version.
func enforcedVerdict(pol *policy.Policy, v policy.Verb) bool {
	enforced, _ := policyEnforcement(pol)
	return enforced && v != policy.Read
}

// denyOnlyControl reports whether a check of verb v on r is one only a deny
// decides at enforcement (D-107): control on an automation, a helper, a
// template entity, or a script itself (its `run` is checked in full).
func denyOnlyControl(r policy.Resource, v policy.Verb) bool {
	if v != policy.Control || r.Sub != "" {
		return false
	}
	switch r.Kind {
	case policy.KindAutomation, policy.KindHelper, policy.KindTemplate, policy.KindScript:
		return true
	}
	return false
}

func runPolicyCheck(ctx context.Context, w io.Writer, resource, verb, as string, asSet bool, file string) error {
	// Refusals first, and before anything is written: a refusal is exit 1
	// with nothing on stdout, a denial is exit 1 with a verdict.
	res, err := policy.ParseResource(resource)
	if err != nil {
		return err
	}
	// `write` asks about every write verb that applies at or below the
	// resource (D-120); one where none applies has nothing to answer.
	write := verb == policy.WriteAlias
	var verbs []policy.Verb
	if write {
		verbs = policy.ApplicableAtOrBelow(res)
		if len(verbs) == 0 {
			return fmt.Errorf("write applies to nothing at %s: no command asks for a write there", res)
		}
	} else {
		v, verr := policy.ParseVerb(verb)
		if verr != nil {
			return verr
		}
		verbs = []policy.Verb{v}
	}
	cfg, err := policyInstanceDir()
	if err != nil {
		return err
	}
	pol, err := loadPolicyView(cfg.Dir, as, asSet, file)
	if err != nil {
		var le *policy.LoadError
		if errors.As(err, &le) {
			return &enforce.BrokenError{Err: le}
		}
		return err
	}

	ha := enforce.NewHAFetcher(ctx, cfg.URL, cfg.Token, nil)
	defer ha.Close()
	docs := make([]policyCheckDoc, len(verbs))
	for i, v := range verbs {
		docs[i] = policyCheckVerdict(pol, res, v, ha.Live())
	}
	if write {
		return renderPolicyCheckWrite(w, pol, res, docs)
	}
	doc := docs[0]
	if flagJSON {
		markStructuredOutput()
		if err := writeJSON(w, doc); err != nil {
			return err
		}
	} else {
		if doc.WhatIf {
			_, _ = fmt.Fprintf(w, "what-if: --as %s (the effective profile is unchanged)\n", pol.Active())
		}
		word := "denied"
		if doc.Allowed {
			word = "allowed"
		}
		_, _ = fmt.Fprintf(w, "%s: %s %s\n", word, doc.Resource, doc.Verb)
		if doc.Line > 0 {
			_, _ = fmt.Fprintf(w, "rule: %s (line %d)\n", doc.Rule, doc.Line)
		} else {
			_, _ = fmt.Fprintf(w, "rule: %s\n", doc.Rule)
		}
		for _, u := range doc.UnmetWhere {
			_, _ = fmt.Fprintf(w, "%s\n", unmetWhereText(u))
		}
		for _, d := range doc.PartialDenies {
			_, _ = fmt.Fprintf(w, "partial: %s (line %d)\n", d.Rule, d.Line)
		}
		_, _ = fmt.Fprintf(w, "profile: %s\n", checkProfileText(doc.Profile, doc.SelectedBy))
		for _, n := range doc.Notes {
			_, _ = fmt.Fprintf(w, "note: %s\n", n)
		}
		switch {
		case doc.Draft:
			_, _ = fmt.Fprintln(w, "not enforced: a draft is not in effect")
		case doc.Verb == policy.Read.String():
			_, _ = fmt.Fprintln(w, "not enforced yet: reads are a later version")
		case doc.denyOnlyDefault:
			_, _ = fmt.Fprintln(w, denyOnlyDefaultText(res, doc.scriptRun))
		case doc.Enforced:
			_, _ = fmt.Fprintln(w, "enforced: a write command refuses what this verdict denies")
		default:
			_, _ = fmt.Fprintln(w, "not enforced: no profile is in effect, so every command runs as without a policy")
		}
	}
	if !doc.Allowed {
		return &policyDeniedError{what: doc.Resource + " " + doc.Verb}
	}
	return nil
}

// policyCheckVerdict decides verb v on res and builds its one-verb document.
func policyCheckVerdict(pol *policy.Policy, res policy.Resource, v policy.Verb, live policy.Live) policyCheckDoc {
	verdict := pol.Decide(res, v, live)

	// A typed address a service call reaches is checked for its denies only
	// (D-107): a denial by the profile default alone is never enforced there.
	denyOnlyDefault := denyOnlyControl(res, v) && !verdict.Allowed && verdict.Rule == policy.RuleDefaultDeny
	// A script is the exception when its run is denied: the default that
	// decided control on script:<id> decides script:<id>/run too, which
	// `script run` and a service call starting the script enforce in full,
	// so the denial counts (D-128).
	var runNote, runAllowed string
	if denyOnlyDefault && res.Kind == policy.KindScript {
		run := policy.Resource{Kind: policy.KindScript, ID: res.ID, Sub: "run"}
		rv := pol.Decide(run, v, live)
		if rv.Allowed {
			runAllowed = run.String()
		} else {
			denyOnlyDefault = false
			rule := rv.Rule
			if rv.Line > 0 {
				rule = fmt.Sprintf("%s (line %d)", rv.Rule, rv.Line)
			}
			runNote = fmt.Sprintf("a run is checked as control %s, denied — %s: script run and a service call starting the script refuse it", run, rule)
		}
	}
	doc := policyCheckDoc{
		Resource: res.String(), Verb: v.String(), Allowed: verdict.Allowed, Rule: verdict.Rule,
		Line: verdict.Line, Profile: verdict.Profile, SelectedBy: string(pol.Selection()),
		WhatIf: verdict.WhatIf, Level: verdict.Level, Enforced: enforcedVerdict(pol, v) && !denyOnlyDefault, Notes: verdict.Notes,
		Partial: verdict.Partial, PartialDenies: []policyPartialDeny{}, UnmetWhere: verdict.UnmetWhere,
		denyOnlyDefault: denyOnlyDefault, scriptRun: runAllowed, Draft: pol.Draft(),
	}
	if runNote != "" {
		doc.Notes = append(doc.Notes, runNote)
	}
	for _, d := range verdict.PartialDenies {
		doc.PartialDenies = append(doc.PartialDenies, policyPartialDeny{Rule: d.Rule, Line: d.Line})
	}
	// An allow reads like a grant there, but enforcement never consults it
	// (D-111): the verdict stands, the note says what it is worth. A script
	// allow is the exception: it also grants the script's run (D-128).
	if denyOnlyControl(res, v) && res.Kind != policy.KindScript && verdict.Allowed && verdict.Line > 0 && strings.HasPrefix(verdict.Rule, string(policy.Allow)+" ") {
		doc.Notes = append(doc.Notes, "control on "+doc.Resource+
			" is checked for its denies only — this allow grants nothing; a service call is decided by the entity's own verdict")
	}
	// A write verb no command asks for at or below the resource answers as
	// any other, and says so (D-110).
	if v != policy.Read && !slices.Contains(policy.ApplicableAtOrBelow(res), v) {
		doc.Notes = append(doc.Notes, fmt.Sprintf("%s does not apply at %s: no command asks for it there, so this verdict decides no command", v, res))
	}
	return doc
}

// renderPolicyCheckWrite answers `policy check <resource> write` (D-120): the
// overall verdict, one line per applicable verb with its partial, where-not-met
// and note lines indented below it, then the profile and the enforced line.
// Exit 0 only when every verb a command enforces is allowed: a deny-only
// control the default decided is shown and does not count (D-128) — a
// script's counts when the default also denies its run (D-128).
func renderPolicyCheckWrite(w io.Writer, pol *policy.Policy, res policy.Resource, docs []policyCheckDoc) error {
	enforced, _ := policyEnforcement(pol)
	doc := policyCheckWriteDoc{
		Resource: res.String(), Verb: policy.WriteAlias, Allowed: true, Profile: docs[0].Profile,
		SelectedBy: docs[0].SelectedBy, WhatIf: docs[0].WhatIf, Level: docs[0].Level, Enforced: enforced, Verdicts: docs,
		Draft: pol.Draft(),
	}
	for _, d := range docs {
		doc.Allowed = doc.Allowed && (d.Allowed || d.denyOnlyDefault)
	}
	if flagJSON {
		markStructuredOutput()
		if err := writeJSON(w, doc); err != nil {
			return err
		}
	} else {
		var b strings.Builder
		if doc.WhatIf {
			fmt.Fprintf(&b, "what-if: --as %s (the effective profile is unchanged)\n", pol.Active())
		}
		word := "denied"
		if doc.Allowed {
			word = "allowed"
		}
		fmt.Fprintf(&b, "%s: %s %s\n", word, doc.Resource, doc.Verb)
		for _, d := range docs {
			word := "denied"
			if d.Allowed {
				word = "allowed"
			}
			fmt.Fprintf(&b, "%s: %s — %s", d.Verb, word, d.Rule)
			if d.Line > 0 {
				fmt.Fprintf(&b, " (line %d)", d.Line)
			}
			b.WriteString("\n")
			for _, p := range d.PartialDenies {
				fmt.Fprintf(&b, "  partial: %s (line %d)\n", p.Rule, p.Line)
			}
			for _, u := range d.UnmetWhere {
				fmt.Fprintf(&b, "  %s\n", unmetWhereText(u))
			}
			for _, n := range d.Notes {
				fmt.Fprintf(&b, "  note: %s\n", n)
			}
			if d.denyOnlyDefault {
				fmt.Fprintf(&b, "  %s\n", denyOnlyDefaultText(res, d.scriptRun))
			}
		}
		fmt.Fprintf(&b, "profile: %s\n", checkProfileText(doc.Profile, doc.SelectedBy))
		switch {
		case doc.Draft:
			b.WriteString("not enforced: a draft is not in effect\n")
		case enforced:
			b.WriteString("enforced: a write command refuses what this verdict denies\n")
		default:
			b.WriteString("not enforced: no profile is in effect, so every command runs as without a policy\n")
		}
		if _, err := io.WriteString(w, b.String()); err != nil {
			return err
		}
	}
	if !doc.Allowed {
		return &policyDeniedError{what: doc.Resource + " " + doc.Verb}
	}
	return nil
}

// checkProfileText is the `profile:` line's value: the profile and how it was
// selected, `none` when no profile decided.
func checkProfileText(profile, selectedBy string) string {
	if profile == "" {
		profile = "none"
	}
	return profile + " (selected by " + selectedBy + ")"
}

// denyOnlyDefaultText says that a default-deny verdict on a deny-only address
// (D-107) is not enforced, and what decides instead: the entity a service
// call reaches, or for a script the allowed run (D-128).
func denyOnlyDefaultText(res policy.Resource, scriptRun string) string {
	if scriptRun != "" {
		return fmt.Sprintf("not enforced: control on %s:<id> is checked for its denies only — a run is decided by control %s, which is allowed", res.Kind, scriptRun)
	}
	return fmt.Sprintf("not enforced: control on %s:<id> is checked for its denies only — the default decides on the entity a service call reaches", res.Kind)
}

// --- policy lint -------------------------------------------------------------

type policyLintFinding struct {
	Code    string `json:"code"`
	Profile string `json:"profile"`
	Rule    string `json:"rule"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

type policyLintUnchecked struct {
	Kind    string `json:"kind"`
	Reason  string `json:"reason"`
	Profile string `json:"profile"`
	Rule    string `json:"rule"`
	Line    int    `json:"line"`
}

type policyLintDoc struct {
	File      string                `json:"file"`
	Exists    bool                  `json:"exists"`
	Valid     bool                  `json:"valid"`
	Level     string                `json:"level"`
	Findings  []policyLintFinding   `json:"findings"`
	Unchecked []policyLintUnchecked `json:"unchecked"`
	// Hint is the owner sentence, only on a broken file (D-117).
	Hint string `json:"hint,omitempty"`
	// Draft is true when --file named a draft (D-161).
	Draft bool `json:"draft"`
}

// policyLintError is lint's exit 1: findings, or unchecked kinds without
// --allow-partial. The report is already on stdout.
type policyLintError struct{ findings, unchecked int }

func (e *policyLintError) Error() string {
	return fmt.Sprintf("policy lint: %d finding(s), %d unchecked", e.findings, e.unchecked)
}

func (e *policyLintError) ExitCode() int { return 1 }

func runPolicyLint(ctx context.Context, w io.Writer, allowPartial bool, file string) error {
	cfg, err := policyInstanceDir()
	if err != nil {
		return err
	}
	doc := policyLintDoc{Level: policy.Level, Findings: []policyLintFinding{}, Unchecked: []policyLintUnchecked{}, Draft: file != ""}
	pol, err := loadPolicyFile(cfg.Dir, file)
	var le *policy.LoadError
	switch {
	case errors.As(err, &le):
		doc.File, doc.Exists, doc.Valid, doc.Hint = le.File, true, false, enforce.BrokenHint
		for _, p := range le.Problems {
			doc.Findings = append(doc.Findings, policyLintFinding{Code: policy.CodeBroken, Line: p.Line, Message: p.Message})
		}
	case err != nil:
		return err
	default:
		doc.File, doc.Exists, doc.Valid = pol.Source(), pol.Exists(), true
		var inv policy.Inventory
		if pol.Exists() {
			hi := &haInventory{ctx: ctx, cfg: cfg}
			defer hi.close()
			inv = hi
		}
		doc.Findings, doc.Unchecked = lintReportRows(policy.Lint(pol, inv))
	}

	if flagJSON {
		markStructuredOutput()
		if err := writeJSON(w, doc); err != nil {
			return err
		}
	} else {
		renderPolicyLint(w, doc, le, allowPartial)
	}
	if le != nil {
		return &policyBrokenError{n: len(le.Problems), file: le.File}
	}
	if len(doc.Findings) > 0 || (len(doc.Unchecked) > 0 && !allowPartial) {
		return &policyLintError{findings: len(doc.Findings), unchecked: len(doc.Unchecked)}
	}
	return nil
}

// lintReportRows converts a lint report to its --json rows.
func lintReportRows(rep policy.LintReport) ([]policyLintFinding, []policyLintUnchecked) {
	findings, unchecked := []policyLintFinding{}, []policyLintUnchecked{}
	for _, f := range rep.Findings {
		findings = append(findings, policyLintFinding{Code: f.Code, Profile: f.Profile, Rule: f.Rule, Line: f.Line, Message: f.Message})
	}
	for _, u := range rep.Unchecked {
		unchecked = append(unchecked, policyLintUnchecked{
			Kind: string(u.Kind), Reason: u.Reason, Profile: u.Profile, Rule: u.Rule, Line: u.Line,
		})
	}
	return findings, unchecked
}

func renderPolicyLint(w io.Writer, doc policyLintDoc, le *policy.LoadError, allowPartial bool) {
	if !doc.Exists {
		_, _ = fmt.Fprintf(w, "no policy file (looked for %s): nothing to lint\n", doc.File)
		return
	}
	if doc.Draft {
		_, _ = fmt.Fprintf(w, "file: %s (draft, not in effect)\n", doc.File)
	} else {
		_, _ = fmt.Fprintf(w, "file: %s\n", doc.File)
	}
	if le != nil {
		// A broken file's findings are its problems, in the one wording policy
		// show and every refusal use (D-117).
		_, _ = fmt.Fprintln(w, enforce.BrokenReport(le))
		return
	}
	if len(doc.Findings) == 0 {
		_, _ = fmt.Fprintln(w, "findings: none")
	} else {
		_, _ = fmt.Fprintf(w, "findings: %d\n", len(doc.Findings))
		for _, f := range doc.Findings {
			where := "line " + strconv.Itoa(f.Line)
			if f.Profile != "" {
				where += " profile " + f.Profile
			}
			if f.Rule != "" {
				where += " " + f.Rule
			}
			_, _ = fmt.Fprintf(w, "  %s  %s: %s\n", f.Code, where, f.Message)
		}
	}
	if len(doc.Unchecked) > 0 {
		suffix := " — not checked, so not clean (--allow-partial accepts this)"
		if allowPartial {
			suffix = " — accepted by --allow-partial"
		}
		_, _ = fmt.Fprintf(w, "unchecked: %d%s\n", len(doc.Unchecked), suffix)
		for _, u := range doc.Unchecked {
			if u.Rule == "" {
				_, _ = fmt.Fprintf(w, "  %s: %s\n", u.Kind, u.Reason)
				continue
			}
			_, _ = fmt.Fprintf(w, "  %s: %s — line %d profile %s %s\n", u.Kind, u.Reason, u.Line, u.Profile, u.Rule)
		}
	}
	if len(doc.Findings) == 0 && len(doc.Unchecked) == 0 {
		_, _ = fmt.Fprintln(w, "clean")
	}
}

// haInventory lists live ids for lint (manual "policy lint"). Every
// Home Assistant source is fetched at most once, on demand: the states once
// (entity ids, automation config ids, script ids, storage helpers and
// attributes all come from it), the WebSocket connection once. The companion
// adds the YAML helpers and the templates (policy_inventory.go). After the first transport failure —
// refused, timed out — it stops asking: every later Home Assistant-backed
// question answers with that same error, so a stalled host costs one
// --timeout, not one per kind. Any other failure makes only its own kind
// unchecked.
type haInventory struct {
	ctx context.Context
	cfg *config.Config

	// down is the first transport failure; once set, nothing asks HA again.
	down error

	statesDone bool
	states     []statesEnvelope
	statesErr  error

	ws     *haapi.WSClient
	wsDone bool
	wsErr  error

	// companionErr is the companion's connection failure, once there was one.
	companionErr error
}

var _ policy.AttributeInventory = (*haInventory)(nil)

// IDs implements policy.Inventory.
func (h *haInventory) IDs(kind policy.Kind) ([]string, error) {
	switch kind {
	case policy.KindSystem:
		for _, s := range policy.KindSpecs() {
			if s.Kind == policy.KindSystem {
				return s.IDs, nil
			}
		}
	case policy.KindEntity:
		return h.entityIDs()
	case policy.KindDevice, policy.KindArea, policy.KindFloor, policy.KindLabel, policy.KindDashboard:
		ws, err := h.wsClient()
		if err != nil {
			return nil, err
		}
		ids, err := registryIDs(h.ctx, ws, kind)
		return ids, h.note(err)
	case policy.KindAutomation:
		return h.automationIDs()
	case policy.KindScript:
		return h.scriptIDs()
	case policy.KindService:
		return h.serviceIDs()
	case policy.KindHelper:
		return h.helperIDs()
	case policy.KindTemplate:
		return h.templateIDs()
	case policy.KindConfigEntry:
		return h.configEntryIDs()
	}
	return nil, policy.ErrNotEnumerated
}

// Attributes implements policy.AttributeInventory from the one states fetch.
func (h *haInventory) Attributes() (map[string][]string, error) {
	states, err := h.allStates()
	if err != nil {
		return nil, err
	}
	out := make(map[string][]string, len(states))
	for _, s := range states {
		var attrs map[string]json.RawMessage
		if decErr := decodeStateAttributes(s, &attrs); decErr != nil {
			return nil, fmt.Errorf("parsing states: %w", decErr)
		}
		out[s.EntityID] = sortedKeys(attrs)
	}
	return out, nil
}

// automationIDs are the config ids: the policy's automation id is the config
// id (D-1, the kinds table), so that is what a pattern is checked against —
// not the entity object id `auto ls` prints, which differs for every
// UI-authored automation. An automation without a config id cannot be
// addressed.
func (h *haInventory) automationIDs() ([]string, error) {
	states, err := h.allStates()
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, s := range states {
		if !strings.HasPrefix(s.EntityID, "automation.") {
			continue
		}
		var attrs struct {
			ID string `json:"id"`
		}
		if decErr := decodeStateAttributes(s, &attrs); decErr != nil {
			return nil, fmt.Errorf("parsing states: %w", decErr)
		}
		if attrs.ID != "" {
			ids = append(ids, attrs.ID)
		}
	}
	return ids, nil
}

// scriptIDs are the object ids `script ls` prints.
func (h *haInventory) scriptIDs() ([]string, error) {
	states, err := h.allStates()
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, s := range states {
		if id, ok := strings.CutPrefix(s.EntityID, "script."); ok {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// serviceIDs are the `<domain>.<service>` pairs Home Assistant reports.
func (h *haInventory) serviceIDs() ([]string, error) {
	if h.down != nil {
		return nil, h.down
	}
	domains, err := h.client().ServiceList(h.ctx)
	if err != nil {
		return nil, h.note(err)
	}
	var ids []string
	for _, d := range domains {
		for _, name := range sortedKeys(d.Services) {
			ids = append(ids, d.Domain+"."+name)
		}
	}
	return ids, nil
}

// note records err as the inventory's transport failure when it is one, and
// returns it unchanged.
func (h *haInventory) note(err error) error {
	if err != nil && h.down == nil && isTransportFailure(err) {
		h.down = err
	}
	return err
}

// isTransportFailure reports whether err means Home Assistant could not be
// reached at all — refused, timed out, reset — rather than that it answered
// with an error for one request.
func isTransportFailure(err error) bool {
	var netErr net.Error
	var urlErr *url.Error
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, syscall.ECONNREFUSED) ||
		errors.As(err, &netErr) || errors.As(err, &urlErr)
}

// allStates fetches /api/states once.
func (h *haInventory) allStates() ([]statesEnvelope, error) {
	if !h.statesDone {
		if h.down != nil {
			return nil, h.down
		}
		h.statesDone = true
		h.states, h.statesErr = fetchDomainStates(h.ctx, h.client(), "")
		h.statesErr = h.note(h.statesErr)
	}
	return h.states, h.statesErr
}

// client builds a REST client per call rather than holding one: a
// *haapi.Client stored in a type that reaches an interface makes every
// exported Client method look reachable to the deadcode gate (RTA treats a
// runtime type's fields reflectively), which would silently retire four
// allowlisted gate-only methods.
func (h *haInventory) client() *haapi.Client {
	return haapi.New(h.cfg.URL, h.cfg.Token)
}

func (h *haInventory) wsClient() (*haapi.WSClient, error) {
	if !h.wsDone {
		if h.down != nil {
			return nil, h.down
		}
		h.wsDone = true
		ws := haapi.NewWSClient(h.cfg.URL, h.cfg.Token)
		if err := ws.Connect(h.ctx); err != nil {
			h.wsErr = h.note(fmt.Errorf("connecting to HA: %w", err))
		} else {
			h.ws = ws
		}
	}
	return h.ws, h.wsErr
}

func (h *haInventory) close() {
	if h.ws != nil {
		_ = h.ws.Close()
	}
}

func (h *haInventory) entityIDs() ([]string, error) {
	states, err := h.allStates()
	if err != nil {
		return nil, err
	}
	ws, err := h.wsClient()
	if err != nil {
		return nil, err
	}
	reg, err := ws.EntityRegistryList(h.ctx)
	if err != nil {
		return nil, h.note(fmt.Errorf("fetching entity registry: %w", err))
	}
	ids := make([]string, 0, len(states)+len(reg))
	for _, s := range states {
		ids = append(ids, s.EntityID)
	}
	for _, e := range reg {
		ids = append(ids, e.EntityID)
	}
	return ids, nil
}

func registryIDs(ctx context.Context, ws *haapi.WSClient, kind policy.Kind) ([]string, error) {
	var ids []string
	switch kind {
	case policy.KindDevice:
		devs, err := ws.DeviceRegistryList(ctx)
		if err != nil {
			return nil, fmt.Errorf("fetching device registry: %w", err)
		}
		for _, d := range devs {
			ids = append(ids, d.ID)
		}
	case policy.KindArea:
		areas, err := ws.AreaRegistryList(ctx)
		if err != nil {
			return nil, fmt.Errorf("fetching area registry: %w", err)
		}
		for _, a := range areas {
			ids = append(ids, a.AreaID)
		}
	case policy.KindFloor:
		floors, err := ws.FloorRegistryList(ctx)
		if err != nil {
			return nil, fmt.Errorf("fetching floor registry: %w", err)
		}
		for _, f := range floors {
			ids = append(ids, f.FloorID)
		}
	case policy.KindLabel:
		labels, err := ws.LabelRegistryList(ctx)
		if err != nil {
			return nil, fmt.Errorf("fetching label registry: %w", err)
		}
		for _, l := range labels {
			ids = append(ids, l.LabelID)
		}
	case policy.KindDashboard:
		dashes, err := ws.DashboardList(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing dashboards: %w", err)
		}
		ids = append(ids, "") // the default dashboard
		for _, d := range dashes {
			ids = append(ids, d.URLPath)
		}
	}
	return ids, nil
}
