package enforce

import (
	"context"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// RequestChecks is what one write request will be checked as when it is sent,
// computed now — by the same classifier, with the same facts this invocation
// holds or asks for (the registries a label, area, floor or config entry
// delete reads, D-76, D-88) — so a command can put the request into its plan
// before it is sent and the plan holds the request layer's verdicts by
// construction (R-12). The facts it fetches are this session's, so the request
// sent afterwards is mapped from the same answer unless a write came between.
//
// A request that is not a write cannot be planned: the answer is a fail-closed
// denial for the plan to carry instead of checks — the request's own
// fail-closed rule when it has one (an unmappable id, D-69), else
// `fail-closed: unmapped request <what>`.
func (s *Session) RequestChecks(ctx context.Context, req reqmap.Request) ([]reqmap.Check, *Denial) {
	res := s.classify(ctx, req)
	if res.Disposition == reqmap.Write {
		return res.Checks, nil
	}
	rule := res.Rule
	if rule == "" {
		rule = "fail-closed: unmapped request " + describe(req)
	}
	return nil, &Denial{Resource: describe(req), Rule: rule}
}
