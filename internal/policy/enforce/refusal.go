package enforce

import (
	"fmt"
	"strings"

	"github.com/hemm-ems/hactl/internal/policy"
)

// The two error classes of a policy refusal (D-70). Both exit 1.
const (
	// ClassDenied: a rule, or a fail-closed reason, denied the plan or a
	// request.
	ClassDenied = "policy_denied"
	// ClassBroken: policy.yaml cannot be read as a policy (D-60).
	ClassBroken = "policy_broken"
)

// The two layers a check is made at (D-71's `layer`).
const (
	// LayerCommand is a write command's plan check, before its first write.
	LayerCommand = "command"
	// LayerRequest is the check of one request before it leaves the process.
	LayerRequest = "request"
)

// Fail-closed reasons hactl refuses for without a rule (`rule` is
// `fail-closed: <reason>`, `line` 0).
const (
	// RuleAuditUnwritable refuses a write whose audit line cannot be written
	// (D-71) — a preview too, since a preview authorises --confirm.
	RuleAuditUnwritable = "fail-closed: audit log not writable"
	// RuleHopDiffers refuses a redirect hop that maps to other resources
	// than the request it follows (D-74).
	RuleHopDiffers = "fail-closed: redirect maps differently"
)

// Denial is one denied (resource, verb) of a refusal, in plan order.
type Denial struct {
	// Resource is the checked address, `<kind>:*[/<sub>]` for a kind-wide
	// check (D-75), or the request's description for a refused request no row
	// maps.
	Resource string `json:"resource"`
	Verb     string `json:"verb"`
	// Rule is the deciding rule's text, or `fail-closed: <reason>`.
	Rule string `json:"rule"`
	// Line is the deciding rule's line, 0 for a fail-closed reason.
	Line int `json:"line"`
	// Partial marks a denial by a deny on a part of the resource (D-66).
	Partial bool `json:"partial"`
	// KindWide marks a check on every id of a kind (D-75).
	KindWide bool `json:"kind_wide,omitempty"`
	// UnmetWhere are the narrowed allows tried whose selector did not hold
	// (D-112), present only when there are any.
	UnmetWhere []policy.UnmetWhere `json:"unmet_where,omitempty"`
}

// Text renders `<verb> <resource> — <rule> (line <n>)`, the form of the
// stderr summary and of each `denied:` line; a fail-closed reason has no line.
func (d Denial) Text() string {
	s := strings.TrimSpace(d.Verb + " " + d.Resource)
	s += " — " + d.Rule
	if d.Line > 0 {
		s += fmt.Sprintf(" (line %d)", d.Line)
	}
	return s
}

// DeniedError is a policy_denied refusal: of a write command's plan (the command
// layer, before anything was written), or of one request (the request layer —
// the command's plan was one resource short, a hactl bug to report, and
// Written names what the run had already written).
type DeniedError struct {
	// Layer is LayerCommand or LayerRequest.
	Layer string
	// DryRun is true for a refused preview, false for a refused --confirm or
	// a command without one.
	DryRun bool
	// Action completes "would …" (the plan's action), or names the request
	// refused at the request layer.
	Action string
	// Denied lists every denied (resource, verb), in plan order.
	Denied []Denial
	// Checked counts the (resource, verb) pairs checked.
	Checked int
	// Profile and SelectedBy are the profile in effect and how it was chosen.
	Profile    string
	SelectedBy policy.Selection
	// Written lists what had already been written when the refusal came:
	// always empty for a plan refusal.
	Written []string
	// PreviewPassed is set on a refused --confirm whose preview had passed:
	// something it depends on changed since (D-80).
	PreviewPassed bool
	// PlanAllowed is set on a request-layer refusal when the command had
	// checked and allowed its plan: the plan was one resource short.
	PlanAllowed bool
}

// Error is the one stderr line (D-70): `policy_denied: <verb> <resource> —
// <rule> (line <n>)` for the first denial; a request-layer refusal adds what
// was already written.
func (r *DeniedError) Error() string {
	first := "refused"
	if len(r.Denied) > 0 {
		first = r.Denied[0].Text()
	}
	s := ClassDenied + ": " + first
	if r.Layer == LayerRequest {
		written := "nothing"
		if len(r.Written) > 0 {
			written = strings.Join(r.Written, ", ")
		}
		if r.PlanAllowed {
			s += " — refused at the request layer although the command's plan allowed it (a hactl bug to report)"
		} else {
			s += " — refused at the request layer"
		}
		s += "; already written: " + written
	}
	return s
}

// ExitCode is 1 (D-70: no new exit code).
func (r *DeniedError) ExitCode() int { return 1 }

// PolicyClass is the error class, ClassDenied.
func (r *DeniedError) PolicyClass() string { return ClassDenied }

// Hint is the refusal's closing line: never "use --confirm" — the policy is
// the owner's, and a denial is to be reported (D-70).
func (r *DeniedError) Hint() string {
	s := "refused by the owner's policy.yaml (profile " + r.Profile + ") — report it; do not edit the policy"
	if r.PreviewPassed {
		s = "the preview had passed, and something it depends on changed since (the policy, an area or label, a service call's reach); " + s
	}
	return s
}

// BrokenError is the D-60 refusal: policy.yaml cannot be read as a policy, so
// every command but the exempt ones refuses, naming each problem as
// `policy_broken: <file>:<line>: <message>`.
type BrokenError struct {
	Err *policy.LoadError
}

func (e *BrokenError) Error() string {
	lines := strings.Split(e.Err.Error(), "\n")
	for i, l := range lines {
		lines[i] = ClassBroken + ": " + l
	}
	return strings.Join(lines, "\n")
}

// Unwrap exposes the *policy.LoadError.
func (e *BrokenError) Unwrap() error { return e.Err }

// ExitCode is 1 (D-70).
func (e *BrokenError) ExitCode() int { return 1 }

// PolicyClass is the error class, ClassBroken.
func (e *BrokenError) PolicyClass() string { return ClassBroken }
