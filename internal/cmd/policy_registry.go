package cmd

import (
	"context"
	"errors"
	"net/url"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// Plan helpers of the registry family (manual "What each write command
// checks"): the entity and device registry fields, area/floor/label create and
// delete, config entries and their flows, the config show probe and the local
// cache (D-75, D-76, D-79, D-83).

// addRequestMapping declares a write request the confirmed run will send, as
// the request layer will map it when it is sent (enforce.Session.RequestChecks):
// a delete's cascade (D-76) is enumerated from the registries this invocation
// reads, through the same mapping and the same answer, so the plan holds the
// request layer's verdicts by construction (R-12) and the superset rules live
// in one place — the classifier's. A request the classifier cannot plan joins
// the plan as a fail-closed denial. With no profile in effect it asks nothing
// and adds nothing (D-58).
func (p *writePlan) addRequestMapping(ctx context.Context, req reqmap.Request) *writePlan {
	sess := policySession
	if sess == nil {
		return p
	}
	checks, denial := sess.RequestChecks(ctx, req)
	if denial != nil {
		p.failClosed = append(p.failClosed, *denial)
		return p
	}
	p.checks = append(p.checks, checks...)
	return p
}

// registryDeleteRequest is the websocket message `label delete`, `area delete`
// and `floor delete` send (haapi.WSClient.LabelRegistryDelete and siblings):
// `config/<kind>_registry/delete` with `<kind>_id`.
func registryDeleteRequest(kind policy.Kind, id string) reqmap.Request {
	return reqmap.Request{
		Transport: reqmap.WS,
		Type:      "config/" + string(kind) + "_registry/delete",
		Params:    map[string]any{string(kind) + "_id": id},
	}
}

// configEntryDeleteRequest is the REST request `config delete` sends
// (haapi.Client.DeleteConfigEntry).
func configEntryDeleteRequest(entryID string) reqmap.Request {
	return reqmap.Request{
		Transport: reqmap.REST, Method: "DELETE",
		Path: "/api/config/config_entries/entry/" + url.PathEscape(entryID),
	}
}

// configFlowPlan is what driving a flow by its id needs — a step (`config
// flow-step`) or the GET that runs the current step (`config flow-inspect`, and
// `flow-step`'s own preview; D-74, R-5). An options flow edits its entry's
// options; a flow started by an earlier command names no entry hactl can see,
// so it is every entry's options (A-5, kind-wide). A config flow may create an
// entry or reconfigure one: create and edit on every config entry id (D-75).
func configFlowPlan(action string, options bool) *writePlan {
	p := newWritePlan(action)
	if options {
		return p.addKindWide(policy.Edit, policy.KindConfigEntry, "options")
	}
	return p.addKindWide(policy.Create, policy.KindConfigEntry, "").
		addKindWide(policy.Edit, policy.KindConfigEntry, "")
}

// checkProbePlan checks the plan of a best-effort probe a read command makes
// (D-79: `config show --probe-options-flow`) before the probe is sent. Unlike
// checkWritePlan it renders nothing: a denied probe is skipped and the command
// carries on with a note (D-91), so the answer is the refusal for the note to
// quote, or nil when the probe may run (or no profile is in effect, D-58). The
// decision is audited like any plan check. Any other failure to decide skips
// the probe too — it never runs unchecked.
func checkProbePlan(p *writePlan) *enforce.DeniedError {
	sess := policySession
	if sess == nil {
		return nil
	}
	_, err := sess.CheckPlan(enforce.Plan{Action: p.action, Checks: p.checks, FailClosed: p.failClosed}, false, false)
	if err == nil {
		return nil
	}
	var ref *enforce.DeniedError
	if errors.As(err, &ref) {
		return ref
	}
	return &enforce.DeniedError{
		Layer: enforce.LayerCommand, Action: p.action,
		Denied: []enforce.Denial{{Resource: p.action, Rule: "fail-closed: " + err.Error()}},
	}
}
