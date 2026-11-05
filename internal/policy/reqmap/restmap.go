package reqmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hemm-ems/hactl/internal/policy"
)

// jsonObject decodes a JSON body that must be an object. An empty body is an
// empty object.
func jsonObject(body []byte) (map[string]any, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return map[string]any{}, nil
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("body is not JSON: %w", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("body is a JSON %T, not an object", v)
	}
	return m, nil
}

// mapConfigEntryDelete: delete the entry, plus every entity and device whose
// registry row names it — the cascade as a deliberate superset (D-76).
func mapConfigEntryDelete(c *call) (mapping, error) {
	id := c.vars["entry_id"]
	cs := &cascade{}
	cs.add(policy.Delete, policy.KindConfigEntry, id, "")
	reg := c.fact().Registries
	if reg == nil {
		cs.out = append(cs.out,
			kindWide(policy.Delete, policy.KindEntity, ""),
			kindWide(policy.Delete, policy.KindDevice, ""))
		return mapping{checks: cs.out}, cs.err
	}
	for _, e := range reg.Entities {
		if e.ConfigEntryID == id {
			cs.add(policy.Delete, policy.KindEntity, e.EntityID, "")
		}
	}
	for _, d := range reg.Devices {
		if slices.Contains(d.ConfigEntries, id) {
			cs.add(policy.Delete, policy.KindDevice, d.ID, "")
		}
	}
	return mapping{checks: cs.out}, cs.err
}

// mapOptionsFlowStart: starting an options flow can already persist, so the
// start is edit config_entry:<handler>/options; the handler is the entry_id
// (D-83).
func mapOptionsFlowStart(c *call) (mapping, error) {
	body, err := jsonObject(c.req.Body)
	if err != nil {
		return mapping{}, err
	}
	h, ok := body["handler"].(string)
	if !ok || h == "" {
		return mapping{}, errors.New("body has no string handler")
	}
	ch, err := check(policy.Edit, policy.KindConfigEntry, h, "options")
	return mapping{checks: []Check{ch}}, err
}

// mapOptionsFlowStep covers the step, the abort and the GET of an options flow
// (the GET runs the current step, D-74/R-5): edit on the handler's options when
// this invocation holds the start response naming it, else on every config
// entry's options (kind-wide, an over-deny).
func mapOptionsFlowStep(c *call) (mapping, error) {
	if h, ok := c.fact().OptionsFlowHandlers[c.vars["flow_id"]]; ok && h != "" {
		ch, err := check(policy.Edit, policy.KindConfigEntry, h, "options")
		return mapping{checks: []Check{ch}}, err
	}
	return mapping{checks: []Check{kindWide(policy.Edit, policy.KindConfigEntry, "options")}}, nil
}

// mapConfigFlowStart: a new entry has no id until Home Assistant answers, so
// create on every config entry id (D-75).
func mapConfigFlowStart(*call) (mapping, error) {
	return mapping{checks: []Check{kindWide(policy.Create, policy.KindConfigEntry, "")}}, nil
}

// mapConfigFlowStep: the flow may create an entry (user, discovery) or edit
// one (reauth, reconfigure); the request carries only the flow id, so both,
// kind-wide (D-75). The GET runs the step too (R-5).
func mapConfigFlowStep(*call) (mapping, error) {
	return mapping{checks: []Check{
		kindWide(policy.Create, policy.KindConfigEntry, ""),
		kindWide(policy.Edit, policy.KindConfigEntry, ""),
	}}, nil
}
