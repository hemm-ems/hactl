package enforce

import (
	"slices"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// RuleOpaqueServiceNeedsAllow decides `control service:<x>` for an opaque
// service (D-184) that only a wildcard allow or the profile default granted.
const RuleOpaqueServiceNeedsAllow = "fail-closed: this service reaches what its call does not name — it needs an allow by its exact name while the profile denies anything"

// OpaqueService applies D-184 to a control verdict on an opaque service —
// one whose reach no field of the call states (`conversation.process` acts on
// what a sentence means, `shell_command.*` on whatever the command does,
// reqmap.IsOpaqueService): while the profile denies anything — a deny rule or
// `default: deny` — it is allowed only by an allow whose pattern is exactly
// `service:<x>` and lists control (a narrowed one only when it is the rule
// that decided), never by a wildcard or the default alone. A denial stays a
// denial; every other verdict is returned as it is. `policy check` and the
// request layer answer it alike, as for system:template (D-143).
func OpaqueService(pol *policy.Policy, r policy.Resource, verb policy.Verb, v policy.Verdict) policy.Verdict {
	if !v.Allowed || verb != policy.Control || r.Kind != policy.KindService || r.Sub != "" ||
		!reqmap.IsOpaqueService(r.ID) || !DeniesAnything(pol) {
		return v
	}
	exact := string(policy.KindService) + ":" + r.ID
	for _, rule := range pol.Rules() {
		if rule.Effect != policy.Allow || rule.Pattern != exact || !slices.Contains(rule.Verbs, policy.Control) {
			continue
		}
		if rule.Where == nil || rule.Text() == v.Rule {
			return v
		}
	}
	v.Allowed, v.Rule, v.Line, v.Partial = false, RuleOpaqueServiceNeedsAllow, 0, false
	return v
}

// DeniesAnything reports whether the profile in effect denies anything: it
// declares `default: deny` or holds a deny rule.
func DeniesAnything(pol *policy.Policy) bool {
	if pol == nil || !pol.InEffect() {
		return false
	}
	if pol.Default() == policy.Deny {
		return true
	}
	for _, rule := range pol.Rules() {
		if rule.Effect == policy.Deny {
			return true
		}
	}
	return false
}
