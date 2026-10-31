package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/policy"
)

// The policy family (manual "Policy (permission guardrail)", D-51..D-62).
//
// Phase 1 is the model and three read-only commands. Nothing else consults
// the policy yet, which every output says (`not enforced yet`). The profile is
// chosen by the file alone (D-62): there is deliberately no --profile flag
// and no environment variable; `--as` is a what-if that never changes the
// effective profile.

var (
	flagPolicyAs           string
	flagPolicyAllowPartial bool
)

var policyCmd = family(&cobra.Command{
	Use:   "policy",
	Short: "Read the instance's permission policy (a guardrail against mistakes, not a security boundary)",
	Long: "Read <instance>/policy.yaml: which profile is in effect, what it allows, and whether its rules " +
		"mean what their author wrote. " + policy.Disclaimer + ". hactl never writes the file. " +
		"Not enforced yet: no other command consults the policy in this version.",
})

var policyShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the effective profile, its rules, the level and the file it was loaded from",
	Long: "Print the policy file (or `no policy file`), the level-A disclaimer, the effective profile and how " +
		"it was selected, its default, its rules (deny first, then allow, each with its line) and the declared " +
		"profiles. A broken file is reported problem by problem (exit 1). --as <profile> is a read-only what-if.",
	Args: takesNone(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runPolicyShow(cmd.OutOrStdout(), flagPolicyAs, cmd.Flags().Changed("as"))
	},
}

var policyCheckCmd = &cobra.Command{
	Use:   "check <kind>:<id>[/<sub>] <verb>",
	Short: "Answer one question: is <verb> on <resource> allowed? Exit 0 allowed, 1 denied",
	Long: "Evaluate one resource and one verb (read, control, edit, create, delete) under the effective " +
		"profile and name the rule that decided. The exit code is the verdict: 0 allowed, 1 denied (with the " +
		"verdict on stdout). An invalid resource or verb, an unknown --as profile or a broken file is refused: " +
		"exit 1, nothing on stdout. A partial verdict (a deny covers a part of the resource) denies a write verb " +
		"and allows read, listing the denied parts. Contacts Home Assistant only when a where: selector can " +
		"still change the verdict.",
	Args: takes(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runPolicyCheck(cmd.Context(), cmd.OutOrStdout(), args[0], args[1], flagPolicyAs, cmd.Flags().Changed("as"))
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
		return runPolicyLint(cmd.Context(), cmd.OutOrStdout(), flagPolicyAllowPartial)
	},
}

func init() {
	const asHelp = "what-if: evaluate this declared profile instead of the effective one (read-only; the file's active: still decides)"
	policyShowCmd.Flags().StringVar(&flagPolicyAs, "as", "", asHelp)
	policyCheckCmd.Flags().StringVar(&flagPolicyAs, "as", "", asHelp)
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

// loadPolicyView loads the policy and applies a what-if. A broken file comes
// back as the *policy.LoadError.
func loadPolicyView(dir, as string, asSet bool) (*policy.Policy, error) {
	pol, err := policy.Load(dir)
	if err != nil {
		return nil, err
	}
	if !asSet {
		return pol, nil
	}
	return pol.As(as)
}

// policyBrokenError is the exit-1 error of a show/lint that reported a broken
// file: the report is on stdout, the summary on stderr.
type policyBrokenError struct{ n int }

func (e *policyBrokenError) Error() string {
	return fmt.Sprintf("policy file is broken: %d problem(s) — hactl refuses to read it as a policy (D-60)", e.n)
}

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
	File       string           `json:"file"`
	Exists     bool             `json:"exists"`
	Valid      bool             `json:"valid"`
	Problems   []policyProblem  `json:"problems"`
	Level      string           `json:"level"`
	Disclaimer string           `json:"disclaimer"`
	Enforced   bool             `json:"enforced"`
	Profile    string           `json:"profile"`
	SelectedBy string           `json:"selected_by"`
	WhatIf     bool             `json:"what_if"`
	AllowAll   bool             `json:"allow_all"`
	Default    string           `json:"default"`
	Profiles   []string         `json:"profiles"`
	Rules      []policyShowRule `json:"rules"`
}

func runPolicyShow(w io.Writer, as string, asSet bool) error {
	cfg, err := policyInstanceDir()
	if err != nil {
		return err
	}
	pol, err := loadPolicyView(cfg.Dir, as, asSet)
	var le *policy.LoadError
	switch {
	case errors.As(err, &le):
		return renderPolicyBroken(w, le)
	case err != nil:
		return err
	}

	doc := policyShowDoc{
		File: pol.Source(), Exists: pol.Exists(), Valid: true, Problems: []policyProblem{},
		Level: policy.Level, Disclaimer: policy.Disclaimer, Enforced: false,
		Profile: pol.Active(), SelectedBy: string(pol.Selection()), WhatIf: pol.WhatIf(),
		AllowAll: pol.AllowAll(), Default: string(pol.Default()),
		Profiles: pol.Profiles(), Rules: []policyShowRule{},
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

	if flagJSON {
		markStructuredOutput()
		return writeJSON(w, doc)
	}

	if !pol.Exists() {
		_, _ = fmt.Fprintf(w, "no policy file (looked for %s): everything is allowed, exactly as without a policy\n", pol.Source())
	} else {
		_, _ = fmt.Fprintf(w, "file: %s\n", pol.Source())
	}
	_, _ = fmt.Fprintf(w, "%s\n", policy.Disclaimer)
	_, _ = fmt.Fprintf(w, "level: %s — not enforced yet: no other command consults the policy in this version\n", policy.Level)
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
			_, _ = fmt.Fprintf(w, "  line %-4d %s\n", r.Line, r.Text())
		}
	}
	_, _ = fmt.Fprintf(w, "profiles: %s\n", strings.Join(pol.Profiles(), ", "))
	return nil
}

func renderPolicyBroken(w io.Writer, le *policy.LoadError) error {
	problems := make([]policyProblem, len(le.Problems))
	for i, p := range le.Problems {
		problems[i] = policyProblem{Line: p.Line, Message: p.Message}
	}
	if flagJSON {
		markStructuredOutput()
		if err := writeJSON(w, policyShowDoc{
			File: le.File, Exists: true, Valid: false, Problems: problems,
			Level: policy.Level, Disclaimer: policy.Disclaimer, Enforced: false,
			Profiles: []string{}, Rules: []policyShowRule{},
		}); err != nil {
			return err
		}
		return &policyBrokenError{n: len(problems)}
	}
	_, _ = fmt.Fprintf(w, "file: %s\n", le.File)
	_, _ = fmt.Fprintf(w, "%s\n", policy.Disclaimer)
	_, _ = fmt.Fprintln(w, "BROKEN: hactl refuses to read this file as a policy (D-60); fix every problem below:")
	_, _ = fmt.Fprintln(w, le.Error())
	return &policyBrokenError{n: len(problems)}
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
	// Partial and PartialDenies are D-66's partial verdict: the resource is
	// allowed but a deny covers a strict descendant of it.
	Partial       bool                `json:"partial"`
	PartialDenies []policyPartialDeny `json:"partial_denies"`
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

func runPolicyCheck(ctx context.Context, w io.Writer, resource, verb, as string, asSet bool) error {
	// Refusals first, and before anything is written: a refusal is exit 1
	// with nothing on stdout, a denial is exit 1 with a verdict.
	res, err := policy.ParseResource(resource)
	if err != nil {
		return err
	}
	v, err := policy.ParseVerb(verb)
	if err != nil {
		return err
	}
	cfg, err := policyInstanceDir()
	if err != nil {
		return err
	}
	pol, err := loadPolicyView(cfg.Dir, as, asSet)
	if err != nil {
		var le *policy.LoadError
		if errors.As(err, &le) {
			return fmt.Errorf("policy file is broken — hactl refuses to answer from it (D-60):\n%w", err)
		}
		return err
	}

	live := &registryLive{ctx: ctx, cfg: cfg}
	defer live.close()
	verdict := pol.Decide(res, v, live)

	doc := policyCheckDoc{
		Resource: res.String(), Verb: v.String(), Allowed: verdict.Allowed, Rule: verdict.Rule,
		Line: verdict.Line, Profile: verdict.Profile, SelectedBy: string(pol.Selection()),
		WhatIf: verdict.WhatIf, Level: verdict.Level, Enforced: false, Notes: verdict.Notes,
		Partial: verdict.Partial, PartialDenies: []policyPartialDeny{},
	}
	for _, d := range verdict.PartialDenies {
		doc.PartialDenies = append(doc.PartialDenies, policyPartialDeny{Rule: d.Rule, Line: d.Line})
	}
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
		for _, d := range doc.PartialDenies {
			_, _ = fmt.Fprintf(w, "partial: %s (line %d)\n", d.Rule, d.Line)
		}
		profile := doc.Profile
		if profile == "" {
			profile = "none"
		}
		_, _ = fmt.Fprintf(w, "profile: %s (selected by %s)\n", profile, doc.SelectedBy)
		for _, n := range doc.Notes {
			_, _ = fmt.Fprintf(w, "note: %s\n", n)
		}
		_, _ = fmt.Fprintln(w, "not enforced yet: this is what the policy says, not what a command will do")
	}
	if !doc.Allowed {
		return &policyDeniedError{what: doc.Resource + " " + doc.Verb}
	}
	return nil
}

// registryLive answers where: selectors from Home Assistant's registries,
// fetched once, on first use — `policy check` contacts HA only when a
// selector has to be evaluated. Any failure fails every question (the
// evaluator then refuses to grant on the selector and says so).
type registryLive struct {
	ctx  context.Context
	cfg  *config.Config
	once sync.Once
	ws   *haapi.WSClient
	err  error

	entities map[string]haapi.EntityRegistryEntry
	devices  map[string]haapi.DeviceRegistryEntry
	areas    map[string]haapi.AreaEntry
	labels   map[string]haapi.LabelEntry
}

// EntityArea is the entity's own area, else its device's (H-8).
func (l *registryLive) EntityArea(entityID string) (policy.Ref, bool, error) {
	if err := l.load(); err != nil {
		return policy.Ref{}, false, err
	}
	e, ok := l.entities[entityID]
	if !ok {
		return policy.Ref{}, false, nil
	}
	if e.AreaID != "" {
		ref, has := l.areaRef(e.AreaID)
		return ref, has, nil
	}
	ref, has := l.areaRef(l.devices[e.DeviceID].AreaID)
	return ref, has, nil
}

// EntityLabels are the labels on the entity's own registry entry; HA does not
// inherit labels from the device.
func (l *registryLive) EntityLabels(entityID string) ([]policy.Ref, error) {
	if err := l.load(); err != nil {
		return nil, err
	}
	return l.labelRefs(l.entities[entityID].Labels), nil
}

func (l *registryLive) DeviceArea(deviceID string) (policy.Ref, bool, error) {
	if err := l.load(); err != nil {
		return policy.Ref{}, false, err
	}
	ref, has := l.areaRef(l.devices[deviceID].AreaID)
	return ref, has, nil
}

func (l *registryLive) DeviceLabels(deviceID string) ([]policy.Ref, error) {
	if err := l.load(); err != nil {
		return nil, err
	}
	return l.labelRefs(l.devices[deviceID].Labels), nil
}

func (l *registryLive) load() error {
	l.once.Do(func() {
		ws := haapi.NewWSClient(l.cfg.URL, l.cfg.Token)
		if err := ws.Connect(l.ctx); err != nil {
			l.err = fmt.Errorf("connecting to HA: %w", err)
			return
		}
		l.ws = ws
		ents, err := ws.EntityRegistryList(l.ctx)
		if err != nil {
			l.err = fmt.Errorf("fetching entity registry: %w", err)
			return
		}
		devs, err := ws.DeviceRegistryList(l.ctx)
		if err != nil {
			l.err = fmt.Errorf("fetching device registry: %w", err)
			return
		}
		areas, err := ws.AreaRegistryList(l.ctx)
		if err != nil {
			l.err = fmt.Errorf("fetching area registry: %w", err)
			return
		}
		labels, err := ws.LabelRegistryList(l.ctx)
		if err != nil {
			l.err = fmt.Errorf("fetching label registry: %w", err)
			return
		}
		l.entities = make(map[string]haapi.EntityRegistryEntry, len(ents))
		for _, e := range ents {
			l.entities[e.EntityID] = e
		}
		l.devices = make(map[string]haapi.DeviceRegistryEntry, len(devs))
		for _, d := range devs {
			l.devices[d.ID] = d
		}
		l.areas = make(map[string]haapi.AreaEntry, len(areas))
		for _, a := range areas {
			l.areas[a.AreaID] = a
		}
		l.labels = make(map[string]haapi.LabelEntry, len(labels))
		for _, lb := range labels {
			l.labels[lb.LabelID] = lb
		}
	})
	return l.err
}

func (l *registryLive) close() {
	if l.ws != nil {
		_ = l.ws.Close()
	}
}

func (l *registryLive) areaRef(id string) (policy.Ref, bool) {
	if id == "" {
		return policy.Ref{}, false
	}
	return policy.Ref{ID: id, Name: l.areas[id].Name}, true
}

func (l *registryLive) labelRefs(ids []string) []policy.Ref {
	out := make([]policy.Ref, 0, len(ids))
	for _, id := range ids {
		out = append(out, policy.Ref{ID: id, Name: l.labels[id].Name})
	}
	return out
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
}

// policyLintError is lint's exit 1: findings, or unchecked kinds without
// --allow-partial. The report is already on stdout.
type policyLintError struct{ findings, unchecked int }

func (e *policyLintError) Error() string {
	return fmt.Sprintf("policy lint: %d finding(s), %d unchecked", e.findings, e.unchecked)
}

func (e *policyLintError) ExitCode() int { return 1 }

func runPolicyLint(ctx context.Context, w io.Writer, allowPartial bool) error {
	cfg, err := policyInstanceDir()
	if err != nil {
		return err
	}
	doc := policyLintDoc{Level: policy.Level, Findings: []policyLintFinding{}, Unchecked: []policyLintUnchecked{}}
	pol, err := policy.Load(cfg.Dir)
	var le *policy.LoadError
	switch {
	case errors.As(err, &le):
		doc.File, doc.Exists, doc.Valid = le.File, true, false
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
		rep := policy.Lint(pol, inv)
		for _, f := range rep.Findings {
			doc.Findings = append(doc.Findings, policyLintFinding{Code: f.Code, Profile: f.Profile, Rule: f.Rule, Line: f.Line, Message: f.Message})
		}
		for _, u := range rep.Unchecked {
			doc.Unchecked = append(doc.Unchecked, policyLintUnchecked{
				Kind: string(u.Kind), Reason: u.Reason, Profile: u.Profile, Rule: u.Rule, Line: u.Line,
			})
		}
	}

	if flagJSON {
		markStructuredOutput()
		if err := writeJSON(w, doc); err != nil {
			return err
		}
	} else {
		renderPolicyLint(w, doc, allowPartial)
	}
	if len(doc.Findings) > 0 || (len(doc.Unchecked) > 0 && !allowPartial) {
		return &policyLintError{findings: len(doc.Findings), unchecked: len(doc.Unchecked)}
	}
	return nil
}

func renderPolicyLint(w io.Writer, doc policyLintDoc, allowPartial bool) {
	if !doc.Exists {
		_, _ = fmt.Fprintf(w, "no policy file (looked for %s): nothing to lint\n", doc.File)
		return
	}
	_, _ = fmt.Fprintf(w, "file: %s\n", doc.File)
	if !doc.Valid {
		// A broken file's findings are its problems, each named as
		// `<file>:<line>: <message>` exactly as policy show and the refusal
		// name them (D-60).
		_, _ = fmt.Fprintln(w, "BROKEN: hactl refuses to read this file as a policy (D-60); no live checks ran. Fix every problem below:")
		for _, f := range doc.Findings {
			loc := doc.File
			if f.Line > 0 {
				loc += ":" + strconv.Itoa(f.Line)
			}
			_, _ = fmt.Fprintf(w, "  %s  %s: %s\n", f.Code, loc, f.Message)
		}
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
// (entity ids, automation config ids, script ids and attributes all come from
// it), the WebSocket connection once. After the first transport failure —
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
