package enforce

import (
	"fmt"
	"slices"
	"strconv"
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
	// ClassMissing is an instance that requires a policy and has none in
	// effect (D-136).
	ClassMissing = "policy_missing"
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
	// Read marks the refusal of a read (D-138): one thing the caller may not
	// read, or a read no row maps (D-145).
	Read bool
}

// Error is the one stderr line, a count that repeats no denial — the report
// on stdout names them (D-119): `policy_denied: <m> of <n> resources denied
// (profile <p>)`; a request-layer refusal adds what was already written.
func (r *DeniedError) Error() string {
	if r.Read {
		// A refused read is reported like a denied write (D-155): the
		// count on stderr, the denials in the report on stdout.
		checked := max(r.Checked, len(r.Denied))
		return fmt.Sprintf("%s: %d of %d resources denied (profile %s) — reading it is refused", ClassDenied, len(r.Denied), checked, r.Profile)
	}
	// A request refused whole — unmapped, unmappable, a redirect hop that
	// maps differently — checked no (resource, verb) pair; it counts as the
	// one thing checked, so the line never reads "1 of 0".
	checked := max(r.Checked, len(r.Denied))
	s := fmt.Sprintf("%s: %d of %d resources denied (profile %s)", ClassDenied, len(r.Denied), checked, r.Profile)
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

// FirstDenial is `policy_denied: <verb> <resource> — <rule> (line <n>)` for
// the first denial: the note a read command that skips a denied probe quotes
// (D-94), where no report names the denial.
func (r *DeniedError) FirstDenial() string {
	if len(r.Denied) == 0 {
		return ClassDenied + ": refused"
	}
	return ClassDenied + ": " + r.Denied[0].Text()
}

// ExitCode is 1 (D-70: no new exit code).
func (r *DeniedError) ExitCode() int { return 1 }

// PolicyClass is the error class, ClassDenied.
func (r *DeniedError) PolicyClass() string { return ClassDenied }

// previewPassedHint leads the hint of a refused --confirm whose preview had
// passed (D-80).
const previewPassedHint = "the preview had passed, and something it depends on changed since " +
	"(the policy, an area or label, a service call's reach)"

// Hint is the refusal's closing line, built from the denials' causes (D-118):
// whether the preview had passed, the lines of the deny rules that decided,
// what nothing allows, what hactl could not check, and how much of the plan
// was allowed — joined by "; ". It never suggests --confirm: the policy is the
// owner's, and a denial is to be reported (D-70).
func (r *DeniedError) Hint() string {
	if r.Layer == LayerRequest && r.PlanAllowed {
		return "hactl's own plan missed this request — a hactl bug to report; do not edit policy.yaml"
	}
	var parts []string
	if r.PreviewPassed {
		parts = append(parts, previewPassedHint)
	}
	var lines []int
	var defaults []Denial
	var failClosed *Denial
	for i, d := range r.Denied {
		switch {
		case d.Rule == policy.RuleDefaultDeny || d.Rule == RuleTemplateNeedsAllow:
			// Nothing allows it: under default: deny, or system:template
			// while the profile denies a read (D-143).
			defaults = append(defaults, d)
		case strings.HasPrefix(d.Rule, failClosedPrefix):
			if failClosed == nil {
				failClosed = &r.Denied[i]
			}
		case d.Line > 0 && !slices.Contains(lines, d.Line):
			lines = append(lines, d.Line)
		}
	}
	if len(lines) > 0 {
		slices.Sort(lines)
		nums := make([]string, len(lines))
		for i, n := range lines {
			nums[i] = strconv.Itoa(n)
		}
		parts = append(parts, "the owner would change the rule on line "+strings.Join(nums, ", "))
	}
	if len(defaults) > 0 {
		s := "nothing allows " + defaults[0].Resource
		if more := len(defaults) - 1; more > 0 {
			s += fmt.Sprintf(" (and %d more)", more)
		}
		parts = append(parts, s+"; the owner would add an allow")
	}
	if failClosed != nil {
		reason := strings.TrimPrefix(failClosed.Rule, failClosedPrefix)
		parts = append(parts, fmt.Sprintf("hactl could not check %s (%s), so it refused", failClosed.Resource, reason))
	}
	if denied := len(r.Denied); denied > 0 && denied < r.Checked {
		parts = append(parts, fmt.Sprintf("allowed: %d of %d checked resources, but the command is refused as a whole",
			r.Checked-denied, r.Checked))
	}
	if len(parts) == 0 {
		// Every refusal carries a denial; this only keeps the line whole.
		parts = append(parts, "refused by the owner's policy.yaml (profile "+r.Profile+")")
	}
	return strings.Join(parts, "; ") + " — report it; do not edit policy.yaml"
}

// failClosedPrefix starts the rule of every denial hactl makes without a rule.
const failClosedPrefix = "fail-closed: "

// BrokenHint is the sentence every broken-file output closes with (D-117):
// the file is the owner's, so the caller reports it and does not fix it.
const BrokenHint = "policy.yaml belongs to the owner; hactl never writes it — report this, do not edit it"

// BrokenReport is a broken file in the one wording every output uses (D-117):
// `policy_broken: <file>:<line>: <message>` per problem, then the closing
// `policy_broken: <BrokenHint>` line, newline-joined.
func BrokenReport(le *policy.LoadError) string {
	lines := strings.Split(le.Error(), "\n")
	for i, l := range lines {
		lines[i] = ClassBroken + ": " + l
	}
	return strings.Join(append(lines, ClassBroken+": "+BrokenHint), "\n")
}

// BrokenError is the D-60 refusal: policy.yaml cannot be read as a policy, so
// every command but the exempt ones refuses with the BrokenReport on stderr.
type BrokenError struct {
	Err *policy.LoadError
}

func (e *BrokenError) Error() string { return BrokenReport(e.Err) }

// Unwrap exposes the *policy.LoadError.
func (e *BrokenError) Unwrap() error { return e.Err }

// ExitCode is 1 (D-70).
func (e *BrokenError) ExitCode() int { return 1 }

// PolicyClass is the error class, ClassBroken.
func (e *BrokenError) PolicyClass() string { return ClassBroken }

// MissingError refuses a command on an instance that requires a policy
// (`HACTL_POLICY=required`, D-136) while none is in effect: no policy.yaml, a
// file that selects no profile, or an unreadable requirement.
type MissingError struct {
	// File is the policy.yaml looked for; Why says what is wrong with it.
	File, Why string
}

func (e *MissingError) Error() string {
	return ClassMissing + ": " + e.File + " — this instance requires a policy; the owner restores it\n" +
		"  why: " + e.Why + " (HACTL_POLICY=required in .env)\n" +
		"  hint: report this; do not edit policy.yaml or .env"
}

// ExitCode is 1.
func (e *MissingError) ExitCode() int { return 1 }

// PolicyClass is the error class, ClassMissing.
func (e *MissingError) PolicyClass() string { return ClassMissing }
