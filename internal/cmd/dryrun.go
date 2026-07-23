package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ---------------------------------------------------------------------------
// The shared shape of a dry-run preview.
//
// Every write command in hactl is dry-run by default (H-2), and each one used
// to hand-roll its own preview: its own field order, its own column padding,
// and — because the text was assembled with Fprintf — no answer at all under
// --json, which was a silent no-op on nearly every preview. An agent that
// asks for JSON and gets prose has no way to tell a plan from an error.
//
// dryRunPlan gives all of them one shape. The text form is what it always
// was; the JSON form is the same content as a machine object, so a preview
// now honours --json like every other command (H-10).
// ---------------------------------------------------------------------------

// dryRunPlan is a preview of a write that has not happened.
type dryRunPlan struct {
	details map[string]any
	action  string
	hint    string
	keys    []string // insertion order, for the text form
}

// dryRun starts a preview. action completes the sentence "would …", e.g.
// dryRun("delete script") renders as "dry-run: would delete script".
func dryRun(action string) *dryRunPlan {
	return &dryRunPlan{
		action:  action,
		hint:    "use --confirm to apply",
		details: map[string]any{},
	}
}

// with adds one detail line. Values keep their Go type, so numbers and
// booleans stay numbers and booleans in the JSON form.
func (p *dryRunPlan) with(key string, value any) *dryRunPlan {
	if _, seen := p.details[key]; !seen {
		p.keys = append(p.keys, key)
	}
	p.details[key] = value
	return p
}

// withIf adds a detail line only when cond holds — for optional flags that
// should not show up as empty strings.
func (p *dryRunPlan) withIf(cond bool, key string, value any) *dryRunPlan {
	if cond {
		return p.with(key, value)
	}
	return p
}

// withHint replaces the closing line for commands where --confirm does
// something other than "apply" (e.g. "use --confirm to start").
func (p *dryRunPlan) withHint(hint string) *dryRunPlan {
	p.hint = hint
	return p
}

// dryRunJSON is the machine form. `dry_run` is always true and stated
// explicitly: a caller must be able to tell a plan from a result by looking
// at the object, not by remembering which flags it passed.
type dryRunJSON struct {
	Details map[string]any `json:"details"`
	Action  string         `json:"action"`
	Hint    string         `json:"hint"`
	DryRun  bool           `json:"dry_run"`
}

// render writes the preview as text, or as JSON under --json.
func (p *dryRunPlan) render(w io.Writer) error {
	if flagJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(dryRunJSON{
			DryRun:  true,
			Action:  p.action,
			Details: p.details,
			Hint:    p.hint,
		})
	}

	var b strings.Builder
	b.WriteString("dry-run: would " + p.action + "\n")
	width := 0
	for _, k := range p.keys {
		if len(k) > width {
			width = len(k)
		}
	}
	for _, k := range p.keys {
		_, _ = fmt.Fprintf(&b, "  %-*s %v\n", width+1, k+":", p.details[k])
	}
	b.WriteString(p.hint + "\n")
	_, err := io.WriteString(w, b.String())
	return err
}
