package enforce

import "github.com/hemm-ems/hactl/internal/policy"

// RuleWrittenOutsidePlan reports a location a confirmed ref/replace wrote that
// its checked dry run did not list (D-77, A-7): the window between the checked
// dry run and the confirmed write is documented, not closed (R-4), so what fell
// into it is reported after the fact.
const RuleWrittenOutsidePlan = "fail-closed: written outside the checked plan"

// WrittenOutsidePlan reports, after the fact, the files a confirmed write
// changed although its checked plan did not list them (D-77, A-7): each is a
// denied `edit file:<location>` with RuleWrittenOutsidePlan, audited like any
// denial, and listed in Written — the run ends refused (exit 1,
// policy_denied) with them already written. locations nil or empty returns
// nil.
func (s *Session) WrittenOutsidePlan(action string, locations []string) *DeniedError {
	if len(locations) == 0 {
		return nil
	}
	ref := s.newDenial(LayerCommand, false, action)
	lines := make([]AuditLine, 0, len(locations))
	for _, loc := range locations {
		res := string(policy.KindFile) + ":" + loc
		ref.Denied = append(ref.Denied, Denial{Resource: res, Verb: policy.Edit.String(), Rule: RuleWrittenOutsidePlan})
		ref.Written = append(ref.Written, policy.Edit.String()+" "+res)
		lines = append(lines, s.line(LayerCommand, false, res, policy.Edit.String(), false, RuleWrittenOutsidePlan, 0))
	}
	ref.Checked = len(locations)
	// The write already happened: an audit log that cannot take the lines
	// changes nothing about the refusal, which is reported either way.
	_ = s.audit.Append(lines)
	return ref
}
