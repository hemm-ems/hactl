package cmd

import (
	"context"

	"github.com/hemm-ems/hactl/internal/policy"
)

// The service-call family's plans (manual "Enforcement (writes)", "Service
// calls: reach" and "Service calls: the service itself"; D-59, D-73, D-84).
// Every service call hactl sends — the one `svc call` is given and the ones
// hactl issues on its own (`script run`'s script.turn_on, `script apply`'s
// script.reload) — enters its command's plan through addServiceCall, the
// request layer's own mapping of that very request, so the plan holds every
// verdict the request layer reaches when the call is sent (R-12).

// The service `script apply` sends after its write, named once so the plan
// and the call cannot drift apart.
const (
	scriptReloadDomain  = "script"
	scriptReloadService = "reload"
)

// scriptRunData is the service data `script run` sends with script.turn_on —
// one value for the plan and the call.
func scriptRunData(entityID string) map[string]any {
	return map[string]any{"entity_id": entityID}
}

// scriptApplyPlan is `script apply`'s plan: edit script:<id>/config (the
// companion write) + script.reload, the service hactl sends itself after the
// write (D-73: a profile using `script apply` under `default: deny` must allow
// service:script.reload).
func scriptApplyPlan(ctx context.Context, scriptID string) *writePlan {
	return newWritePlan("apply script").
		add(policy.Edit, policy.KindScript, scriptID, "config").
		addServiceCall(ctx, scriptReloadDomain, scriptReloadService, nil)
}

// addScriptRun declares `script run`'s call: script.turn_on on entityID, as
// the request layer maps it (control service:script.turn_on, the entity in the
// call's reach and data, and script:<registry unique_id>/run, D-84) — plus,
// as a superset, what the manual promises for every run whatever Home
// Assistant answers to the reach question: control on the script entity
// itself, and a run check (on every script's run when the reach named no
// script entity whose config key could be read). With no profile in effect it
// adds nothing (D-58).
func (p *writePlan) addScriptRun(ctx context.Context, entityID string) *writePlan {
	if policySession == nil {
		return p
	}
	p.addServiceCall(ctx, "script", "turn_on", scriptRunData(entityID))
	var haveEntity, haveRun bool
	for _, c := range p.checks {
		if c.Verb != policy.Control {
			continue
		}
		switch {
		case !c.KindWide && c.Resource.Kind == policy.KindEntity && c.Resource.ID == entityID && c.Resource.Sub == "":
			haveEntity = true
		case c.Resource.Kind == policy.KindScript && c.Resource.Sub == "run":
			haveRun = true
		}
	}
	if !haveEntity {
		p.add(policy.Control, policy.KindEntity, entityID, "")
	}
	if !haveRun {
		p.addKindWide(policy.Control, policy.KindScript, "run")
	}
	return p
}
