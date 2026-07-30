package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// ---------------------------------------------------------------------------
// The shared shape of a write that HAS happened.
//
// dryrun.go gave every preview one machine-readable shape, and the `preview`
// surface (dev/surfaces/preview.manifest) closed the set: no --confirm-gated
// command may assemble its plan any other way. Nothing did the same for the
// other branch. Every write command printed its confirmed outcome with a bare
// Fprintf, so `--json` was honoured on the plan and silently dropped the
// moment `--confirm` was added — invalid JSON on the SUCCESS path of a write,
// which is the one moment a machine caller most needs a parseable answer
// (H-10). A caller that scripted `hactl svc call … --confirm --json` got a
// JSON parse error immediately after a real, successful mutation, with exit 0.
//
// writeResult is dryRunPlan's mirror. The text form is whatever the command
// always printed — passed in verbatim, so no human output changes — and the
// JSON form is the same content as a machine object, telling a plan from a
// result by `dry_run: false` and `ok: true` rather than by remembering which
// flags were passed.
// ---------------------------------------------------------------------------

// writeResult is the outcome of a confirmed write.
type writeResult struct {
	details  map[string]any
	action   string
	keys     []string // insertion order, for stable reading
	lines    []string // the human text, exactly as it was printed before
	warnings []string
	preview  bool // set by asPreview: the run did not pass --confirm
}

// done starts a result. action names what happened in the same vocabulary the
// preview used, so `dry-run: would create area` and `{"action":"create area"}`
// describe one operation.
func done(action string) *writeResult {
	return &writeResult{action: action, details: map[string]any{}}
}

// with adds one machine detail. Values keep their Go type, so numbers and
// booleans stay numbers and booleans in the JSON form.
func (r *writeResult) with(key string, value any) *writeResult {
	if _, seen := r.details[key]; !seen {
		r.keys = append(r.keys, key)
	}
	r.details[key] = value
	return r
}

// withIf adds a detail only when cond holds — for fields that are absent
// rather than empty (no backup was written, HA returned no entity_id).
func (r *writeResult) withIf(cond bool, key string, value any) *writeResult {
	if cond {
		return r.with(key, value)
	}
	return r
}

// text appends one line of human output, verbatim. It is the command's own
// sentence: this type deliberately does not invent a generic rendering,
// because every existing message is asserted somewhere and reads better than
// anything a shared renderer would produce.
func (r *writeResult) text(format string, a ...any) *writeResult {
	r.lines = append(r.lines, strings.TrimSuffix(fmt.Sprintf(format, a...), "\n"))
	return r
}

// warn records a caveat that survives into the machine form.
//
// Warnings used to exist only as prose ("written but HA did not confirm
// reload"), so a --json caller could not see the one thing about a successful
// write it most needed to act on. The text line is printed exactly as before;
// the machine form carries the same sentence in `warnings`.
func (r *writeResult) warn(format string, a ...any) *writeResult {
	msg := strings.TrimSuffix(fmt.Sprintf(format, a...), "\n")
	r.warnings = append(r.warnings, msg)
	r.lines = append(r.lines, "warning: "+msg)
	return r
}

// asPreview marks a result as belonging to a run that did NOT pass --confirm.
//
// It exists for one shape: the no-op. `auto apply`, `script apply` and
// `dash replace` all decide "there is nothing to do" BEFORE they consult
// --confirm, and the answer is the same either way — nothing was written and
// nothing would be. That answer is a result rather than a plan, so it renders
// here rather than through dryRun(); but `dry_run` has to keep meaning "this
// run was a preview", because that is the one thing a caller can check without
// re-reading its own argv. Without this, a preview that found nothing to do
// reported `dry_run: false` and read as a completed write.
func (r *writeResult) asPreview(cond bool) *writeResult {
	r.preview = cond
	return r
}

// writeResultJSON is the machine form.
//
// `dry_run` is present and normally false on purpose: it is the field that
// distinguishes this document from dryRunJSON, and a consumer must be able to
// branch on one key rather than on the absence of another. asPreview is the one
// thing that sets it true here.
type writeResultJSON struct {
	Details  map[string]any `json:"details"`
	Action   string         `json:"action"`
	Warnings []string       `json:"warnings,omitempty"`
	OK       bool           `json:"ok"`
	DryRun   bool           `json:"dry_run"`
}

// render writes the result as the command's own text, or as JSON under --json.
func (r *writeResult) render(w io.Writer) error {
	if flagJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(writeResultJSON{
			OK:       true,
			DryRun:   r.preview,
			Action:   r.action,
			Details:  r.details,
			Warnings: r.warnings,
		})
	}
	var b strings.Builder
	for _, line := range r.lines {
		b.WriteString(line)
		b.WriteString("\n")
	}
	_, err := io.WriteString(w, b.String())
	return err
}
