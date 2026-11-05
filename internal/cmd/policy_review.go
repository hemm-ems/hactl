package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// `policy review` (manual "`policy review [--file <draft>]`", D-162): what a
// profile lets the agent do on THIS instance, for the owner, in counts and
// names. It reads Home Assistant through the same inventory `policy lint`
// uses, decides a fixed list of grants for every live id, and changes nothing.
// `policy activate` shows the same review before it asks (D-163).

var policyReviewCmd = &cobra.Command{
	Use:   "review",
	Short: "Say what the policy (or a draft, --file) lets the agent do on this instance, in counts and names",
	Long: "For the owner: which entities the profile in effect lets the agent see, switch and edit, which " +
		"automations, scripts and dashboards it may edit, create or delete, which services it may call, what " +
		"every deny keeps it from, whether what written automations do is checked, and every lint finding. " +
		"Reads Home Assistant, changes nothing. Exit 0 whatever it finds; 1 for a broken file, a missing draft " +
		"or an unreachable Home Assistant.",
	Args: takesNone(),
	RunE: func(cmd *cobra.Command, _ []string) error {
		return runPolicyReview(cmd.Context(), cmd.OutOrStdout(), flagPolicyFile)
	},
}

func init() {
	policyReviewCmd.Flags().StringVar(&flagPolicyFile, "file", "",
		"review this draft instead of policy.yaml — the answer is the draft's, and nothing of it is in effect")
	policyCmd.AddCommand(policyReviewCmd)
}

// reviewNamesShown is how many ids a text line lists before `… and <k> more`.
const reviewNamesShown = 8

type reviewGrant struct {
	Kind      string   `json:"kind"`
	Verb      string   `json:"verb"`
	Sub       string   `json:"sub"`
	Label     string   `json:"label"`
	Allowed   int      `json:"allowed"`
	Total     int      `json:"total"`
	IDs       []string `json:"ids"`
	Unchecked string   `json:"unchecked"`
	// byDomain groups entity and service counts for the text answer.
	byDomain bool
	domains  map[string][2]int
}

type reviewCreate struct {
	Kind  string              `json:"kind"`
	Any   bool                `json:"any"`
	Rules []policyPartialDeny `json:"rules"`
}

type reviewNever struct {
	Rule      string   `json:"rule"`
	Line      int      `json:"line"`
	Kind      string   `json:"kind"`
	Matched   int      `json:"matched"`
	IDs       []string `json:"ids"`
	Unchecked string   `json:"unchecked"`
	onPart    bool
}

type reviewLint struct {
	Findings  []policyLintFinding   `json:"findings"`
	Unchecked []policyLintUnchecked `json:"unchecked"`
}

type reviewDoc struct {
	File          string         `json:"file"`
	Draft         bool           `json:"draft"`
	Profile       string         `json:"profile"`
	SelectedBy    string         `json:"selected_by"`
	AllowAll      bool           `json:"allow_all"`
	Default       string         `json:"default"`
	Indirect      string         `json:"indirect"`
	EnforcedVerbs []string       `json:"enforced_verbs"`
	Grants        []reviewGrant  `json:"grants"`
	Create        []reviewCreate `json:"create"`
	Never         []reviewNever  `json:"never"`
	Lint          reviewLint     `json:"lint"`

	exists       bool
	indirectLine int
}

func runPolicyReview(ctx context.Context, w io.Writer, file string) error {
	cfg, err := policyInstanceDir()
	if err != nil {
		return err
	}
	pol, err := loadPolicyFile(cfg.Dir, file)
	var le *policy.LoadError
	switch {
	case errors.As(err, &le):
		return renderPolicyBroken(w, le, file != "")
	case err != nil:
		return err
	}
	doc, err := buildReview(ctx, cfg, pol)
	if err != nil {
		return err
	}
	markStructuredOutput()
	if flagJSON {
		return writeJSON(w, doc)
	}
	_, err = io.WriteString(w, renderReview(doc))
	return err
}

// buildReview decides every grant of the review for pol's effective profile
// over the live instance. An unreachable Home Assistant is an error: a review
// that cannot count says nothing the owner can rely on.
func buildReview(ctx context.Context, cfg *config.Config, pol *policy.Policy) (reviewDoc, error) {
	_, enforcedVerbs := policyEnforcement(pol)
	if pol.Draft() {
		// What it would enforce once activated: the verbs this version enforces.
		enforcedVerbs = verbNames([]policy.Verb{policy.Control, policy.Edit, policy.Create, policy.Delete})
	}
	doc := reviewDoc{
		File: pol.Source(), Draft: pol.Draft(), Profile: pol.Active(), SelectedBy: string(pol.Selection()),
		AllowAll: pol.AllowAll(), Default: string(pol.Default()), EnforcedVerbs: enforcedVerbs,
		Grants: []reviewGrant{}, Create: []reviewCreate{}, Never: []reviewNever{},
		Lint:   reviewLint{Findings: []policyLintFinding{}, Unchecked: []policyLintUnchecked{}},
		exists: pol.Exists(),
	}
	if !pol.InEffect() {
		doc.EnforcedVerbs = []string{}
		return doc, nil
	}
	doc.Indirect = "checked"
	if !pol.IndirectChecked() {
		doc.Indirect, doc.indirectLine = "unchecked", pol.IndirectLine()
	}

	inv := &haInventory{ctx: ctx, cfg: cfg}
	defer inv.close()
	ids := map[policy.Kind][]string{}
	errs := map[policy.Kind]error{}
	list := func(k policy.Kind) ([]string, error) {
		if got, ok := ids[k]; ok || errs[k] != nil {
			return got, errs[k]
		}
		got, err := inv.IDs(k)
		if err != nil {
			errs[k] = err
			return nil, err
		}
		got = sortedUnique(got)
		ids[k] = got
		return got, nil
	}
	if _, err := list(policy.KindEntity); err != nil && inv.down != nil {
		return doc, fmt.Errorf("cannot review: Home Assistant is not reachable at %s: %w", cfg.URL, inv.down)
	}

	fetcher := enforce.NewHAFetcher(ctx, cfg.URL, cfg.Token, nil)
	defer fetcher.Close()
	live := fetcher.Live()

	type grantSpec struct {
		kind     policy.Kind
		verb     policy.Verb
		sub      string // the address the verb is decided at, below each id
		jsonSub  string // the sub the JSON names
		label    string
		byDomain bool
	}
	for _, g := range []grantSpec{
		{policy.KindEntity, policy.Read, "", "", "see", true},
		{policy.KindEntity, policy.Control, "", "", "switch", true},
		{policy.KindEntity, policy.Edit, "registry", "registry", "edit", true},
		{policy.KindAutomation, policy.Edit, "config", "config", "edit", false},
		{policy.KindAutomation, policy.Delete, "", "", "delete", false},
		{policy.KindScript, policy.Edit, "config", "config", "edit", false},
		{policy.KindScript, policy.Delete, "", "", "delete", false},
		{policy.KindScript, policy.Control, "run", "run", "run", false},
		{policy.KindDashboard, policy.Edit, "", "", "edit", false},
		{policy.KindDashboard, policy.Delete, "", "", "delete", false},
		{policy.KindService, policy.Control, "", "", "call", true},
	} {
		row := reviewGrant{
			Kind: string(g.kind), Verb: g.verb.String(), Sub: g.jsonSub, Label: g.label,
			IDs: []string{}, byDomain: g.byDomain, domains: map[string][2]int{},
		}
		all, err := list(g.kind)
		if err != nil {
			row.Unchecked = err.Error()
			doc.Grants = append(doc.Grants, row)
			continue
		}
		row.Total = len(all)
		for _, id := range all {
			v := pol.Decide(policy.Resource{Kind: g.kind, ID: id, Sub: g.sub}, g.verb, live)
			d := domainOf(id)
			c := row.domains[d]
			c[1]++
			if v.Allowed {
				row.Allowed++
				row.IDs = append(row.IDs, id)
				c[0]++
			}
			row.domains[d] = c
		}
		doc.Grants = append(doc.Grants, row)
	}

	for _, k := range []policy.Kind{policy.KindAutomation, policy.KindScript, policy.KindDashboard} {
		row := reviewCreate{Kind: string(k), Rules: []policyPartialDeny{}}
		if pol.DecideKindWide(k, "", policy.Create).Allowed {
			row.Any = true
		} else {
			for _, r := range pol.Rules() {
				if r.Effect == policy.Allow && (r.Kind() == k || r.Kind() == "") && !r.OnPart() && slices.Contains(r.Verbs, policy.Create) {
					row.Rules = append(row.Rules, policyPartialDeny{Rule: r.Text(), Line: r.Line})
				}
			}
		}
		doc.Create = append(doc.Create, row)
	}

	for _, r := range pol.Rules() {
		if r.Effect != policy.Deny {
			continue
		}
		row := reviewNever{Rule: r.Text(), Line: r.Line, Kind: string(r.Kind()), IDs: []string{}, onPart: r.OnPart()}
		if r.Kind() == "" {
			row.Kind = "*"
			doc.Never = append(doc.Never, row)
			continue
		}
		all, err := list(r.Kind())
		switch {
		case errors.Is(err, policy.ErrNotEnumerated):
			row.Unchecked = "hactl cannot list this kind"
		case err != nil:
			row.Unchecked = err.Error()
		default:
			for _, id := range all {
				if r.MatchesID(r.Kind(), id) {
					row.Matched++
					row.IDs = append(row.IDs, id)
				}
			}
		}
		doc.Never = append(doc.Never, row)
	}

	doc.Lint.Findings, doc.Lint.Unchecked = lintReportRows(policy.Lint(pol, inv))
	return doc, nil
}

// sortedUnique sorts ids and drops repeats (entity ids come from the states
// and the registry both).
func sortedUnique(ids []string) []string {
	out := append([]string{}, ids...)
	sort.Strings(out)
	return slices.Compact(out)
}

// domainOf is the part of an entity or service id before its first dot.
func domainOf(id string) string {
	d, _, _ := strings.Cut(id, ".")
	return d
}

// kindPlural names a kind's section.
func kindPlural(k string) string {
	switch k {
	case string(policy.KindEntity):
		return "entities"
	case string(policy.KindAutomation):
		return "automations"
	case string(policy.KindScript):
		return "scripts"
	case string(policy.KindDashboard):
		return "dashboards"
	case string(policy.KindService):
		return "services"
	}
	return k
}

// reviewIDName shows an id the way the owner reads it: the default dashboard's
// empty url_path as "(default)".
func reviewIDName(kind, id string) string {
	if kind == string(policy.KindDashboard) && id == "" {
		return "(default)"
	}
	return id
}

// namesText lists at most reviewNamesShown ids, then `… and <k> more`.
func namesText(kind string, ids []string) string {
	shown := ids
	if len(shown) > reviewNamesShown {
		shown = shown[:reviewNamesShown]
	}
	names := make([]string, len(shown))
	for i, id := range shown {
		names[i] = reviewIDName(kind, id)
	}
	s := strings.Join(names, ", ")
	if rest := len(ids) - len(shown); rest > 0 {
		s += fmt.Sprintf(" … and %d more", rest)
	}
	return s
}

// renderReview is the text answer (manual "`policy review`").
func renderReview(doc reviewDoc) string {
	var b strings.Builder
	file := doc.File
	if doc.Draft {
		file += " (draft, not in effect)"
	}
	if !doc.exists {
		fmt.Fprintf(&b, "review: no policy file (looked for %s)\n", file)
	} else {
		fmt.Fprintf(&b, "review: %s\n", file)
	}
	if doc.AllowAll {
		b.WriteString("ALLOW-ALL: no profile is in effect, so everything is allowed — there is nothing to count\n")
		return b.String()
	}
	fmt.Fprintf(&b, "profile: %s (selected by %s), default: %s\n", doc.Profile, doc.SelectedBy, doc.Default)
	if doc.Indirect == "unchecked" {
		fmt.Fprintf(&b, "indirect: unchecked (line %d) — what written automations and scripts would switch is NOT checked\n", doc.indirectLine)
	} else {
		b.WriteString("indirect: checked — what written automations and scripts would switch is checked as if switched directly\n")
	}
	if !slices.Contains(doc.EnforcedVerbs, policy.Read.String()) {
		b.WriteString("reads: not enforced yet — see is what the profile would allow\n")
	}

	section := ""
	for _, g := range doc.Grants {
		if g.Kind != section {
			section = g.Kind
			total := g.Total
			if g.Unchecked != "" {
				fmt.Fprintf(&b, "%s:\n", kindPlural(g.Kind))
			} else {
				fmt.Fprintf(&b, "%s (%d):\n", kindPlural(g.Kind), total)
			}
		}
		label := g.Label
		if g.Kind == string(policy.KindEntity) && g.Verb == policy.Edit.String() {
			label = "edit (every registry field: rename, area, labels)"
		}
		if g.Unchecked != "" {
			fmt.Fprintf(&b, "  %s: not checked: %s\n", label, g.Unchecked)
			continue
		}
		fmt.Fprintf(&b, "  %s: %d of %d", label, g.Allowed, g.Total)
		switch {
		case g.Allowed == 0:
		case g.byDomain:
			b.WriteString(" — " + domainsText(g.domains))
		default:
			b.WriteString(" — " + namesText(g.Kind, g.IDs))
		}
		b.WriteString("\n")
		if c, ok := createFor(doc, g); ok {
			fmt.Fprintf(&b, "  create: %s\n", c)
		}
	}

	if len(doc.Never) == 0 {
		b.WriteString("never: no deny rules\n")
	} else {
		b.WriteString("never:\n")
		for _, n := range doc.Never {
			fmt.Fprintf(&b, "  %s (line %d): ", n.Rule, n.Line)
			switch {
			case n.Kind == "*":
				b.WriteString("everything\n")
			case n.Unchecked != "":
				fmt.Fprintf(&b, "not checked: %s\n", n.Unchecked)
			default:
				fmt.Fprintf(&b, "%d %s", n.Matched, kindPlural(n.Kind))
				if n.onPart {
					b.WriteString(" (a part of each)")
				}
				if n.Matched > 0 {
					b.WriteString(" — " + namesText(n.Kind, n.IDs))
				}
				b.WriteString("\n")
			}
		}
	}

	if len(doc.Lint.Findings) == 0 && len(doc.Lint.Unchecked) == 0 {
		b.WriteString("lint: clean\n")
	} else {
		fmt.Fprintf(&b, "lint: %d finding(s), %d unchecked\n", len(doc.Lint.Findings), len(doc.Lint.Unchecked))
		for _, f := range doc.Lint.Findings {
			fmt.Fprintf(&b, "  %s  line %d", f.Code, f.Line)
			if f.Profile != "" {
				b.WriteString(" profile " + f.Profile)
			}
			if f.Rule != "" {
				b.WriteString(" " + f.Rule)
			}
			fmt.Fprintf(&b, ": %s\n", f.Message)
		}
		for _, u := range doc.Lint.Unchecked {
			fmt.Fprintf(&b, "  unchecked %s: %s\n", u.Kind, u.Reason)
		}
	}
	return b.String()
}

// createFor is the `create:` line of a kind, printed after the kind's last
// grant row.
func createFor(doc reviewDoc, g reviewGrant) (string, bool) {
	last := -1
	for i, o := range doc.Grants {
		if o.Kind == g.Kind {
			last = i
		}
	}
	if last < 0 || doc.Grants[last].Verb != g.Verb || doc.Grants[last].Sub != g.Sub {
		return "", false
	}
	for _, c := range doc.Create {
		if c.Kind != g.Kind {
			continue
		}
		switch {
		case c.Any:
			return "any id", true
		case len(c.Rules) == 0:
			return "no", true
		}
		parts := make([]string, len(c.Rules))
		for i, r := range c.Rules {
			parts[i] = fmt.Sprintf("%s (line %d)", r.Rule, r.Line)
		}
		return "only what " + strings.Join(parts, ", ") + " allows", true
	}
	return "", false
}

// domainsText lists the domains with at least one allowed id, by name:
// `light 20 of 20, switch 14 of 30`.
func domainsText(domains map[string][2]int) string {
	var names []string
	for d, c := range domains {
		if c[0] > 0 {
			names = append(names, d)
		}
	}
	sort.Strings(names)
	parts := make([]string, len(names))
	for i, d := range names {
		parts[i] = fmt.Sprintf("%s %d of %d", d, domains[d][0], domains[d][1])
	}
	return strings.Join(parts, ", ")
}
