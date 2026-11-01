package reqmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/policy"
)

// templateEntityDomains are the entity domains a template block declares — the
// companion's _ENTITY_DOMAINS (templates.py). A mapping holding any of them is
// a full block; otherwise it is a bare entity item.
var templateEntityDomains = []string{
	"sensor", "binary_sensor", "number", "select", "button", "image", "weather",
	"light", "switch", "lock", "cover", "fan", "device_tracker", "event",
	"alarm_control_panel", "update", "vacuum",
}

// yamlMapping decodes a YAML (or JSON) body that must be a mapping.
func yamlMapping(body []byte) (map[string]any, error) {
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, errors.New("body is empty")
	}
	var v any
	if err := yaml.Unmarshal(body, &v); err != nil {
		return nil, fmt.Errorf("body is not YAML: %w", err)
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("body is a %T, not a mapping", v)
	}
	return m, nil
}

// singleKey returns the one top-level key of a mapping body (a script or a
// helper is created as `<id>: {…}`).
func singleKey(m map[string]any) (string, error) {
	if len(m) != 1 {
		return "", fmt.Errorf("body has %d top-level keys, want exactly one", len(m))
	}
	for k := range m {
		return k, nil
	}
	return "", nil
}

// queryID maps a typed row addressed by ?id=.
func queryID(v policy.Verb, kind policy.Kind, sub string) mapper {
	return func(c *call) (mapping, error) {
		id, err := c.requiredQuery("id")
		if err != nil {
			return mapping{}, err
		}
		ch, err := check(v, kind, id, sub)
		return mapping{checks: []Check{ch}}, err
	}
}

func systemWrite(v policy.Verb, id string) mapper {
	return func(*call) (mapping, error) {
		ch, err := check(v, policy.KindSystem, id, "")
		return mapping{checks: []Check{ch}}, err
	}
}

// mapTemplateCreate: create template:<uid> for every unique_id the body
// declares, as a bare item or a full block. An entity without a unique_id has
// no address: refused while a profile is in effect (D-78).
func mapTemplateCreate(c *call) (mapping, error) {
	m, err := yamlMapping(c.req.Body)
	if err != nil {
		return mapping{}, err
	}
	var items []any
	block := false
	for _, d := range templateEntityDomains {
		v, ok := m[d]
		if !ok {
			continue
		}
		block = true
		list, isList := v.([]any)
		if !isList {
			return mapping{}, fmt.Errorf("block key %q is a %T, not a list", d, v)
		}
		items = append(items, list...)
	}
	if !block {
		items = []any{m}
	}
	var out []Check
	for _, it := range items {
		entity, ok := it.(map[string]any)
		if !ok {
			return mapping{}, refuse("template entity without unique_id", "a template entity item is a %T, not a mapping", it)
		}
		raw, has := entity["unique_id"]
		uid, isStr := raw.(string)
		if !has || !isStr || uid == "" {
			return mapping{}, refuse("template entity without unique_id", "a template entity has no string unique_id, so no template:<uid> rule can name it")
		}
		ch, cerr := check(policy.Create, policy.KindTemplate, uid, "")
		if cerr != nil {
			return mapping{}, cerr
		}
		out = append(out, ch)
	}
	return mapping{checks: out}, nil
}

func mapScriptCreate(c *call) (mapping, error) {
	m, err := yamlMapping(c.req.Body)
	if err != nil {
		return mapping{}, err
	}
	key, err := singleKey(m)
	if err != nil {
		return mapping{}, err
	}
	ch, err := check(policy.Create, policy.KindScript, key, "")
	return mapping{checks: []Check{ch}}, err
}

// automationBodyID is the `id:` of an automation body, which must be a
// non-empty string: an id-less body would leave an automation no deny on
// automation:<id> matches (D-78, R-6).
func automationBodyID(body []byte) (string, error) {
	m, err := yamlMapping(body)
	if err != nil {
		return "", err
	}
	id, ok := m["id"].(string)
	if !ok || id == "" {
		return "", refuse("automation without a config id", "the automation body has no string id:, so no automation:<id> rule can name it")
	}
	return id, nil
}

// mapAutomationPut: edit automation:<id>/config, plus create
// automation:<body id> when the body's id differs — the companion replaces the
// entry verbatim and never compares, so a differing id is a hidden rename
// (E-5, D-78).
func mapAutomationPut(c *call) (mapping, error) {
	id, err := c.requiredQuery("id")
	if err != nil {
		return mapping{}, err
	}
	edit, err := check(policy.Edit, policy.KindAutomation, id, "config")
	if err != nil {
		return mapping{}, err
	}
	bodyID, err := automationBodyID(c.req.Body)
	if err != nil {
		return mapping{}, err
	}
	out := []Check{edit}
	if bodyID != id {
		create, cerr := check(policy.Create, policy.KindAutomation, bodyID, "")
		if cerr != nil {
			return mapping{}, cerr
		}
		out = append(out, create)
	}
	return mapping{checks: out}, nil
}

func mapAutomationCreate(c *call) (mapping, error) {
	id, err := automationBodyID(c.req.Body)
	if err != nil {
		return mapping{}, err
	}
	ch, err := check(policy.Create, policy.KindAutomation, id, "")
	return mapping{checks: []Check{ch}}, err
}

// mapAutomationDelete maps the delete only for a config id a GET verified in
// this invocation (D-72): the companion falls back to the alias and then a
// live entity_id, so an unverified ?id= may name another automation (D-78).
func mapAutomationDelete(c *call) (mapping, error) {
	id, err := c.requiredQuery("id")
	if err != nil {
		return mapping{}, err
	}
	if !c.fact().VerifiedAutomationIDs[id] {
		return mapping{}, refuse("automation config id not verified",
			"no GET /v1/config/automation in this invocation verified %q as a config id; the companion would fall back to an alias or an entity id", id)
	}
	ch, err := check(policy.Delete, policy.KindAutomation, id, "")
	return mapping{checks: []Check{ch}}, err
}

func mapHelperCreate(c *call) (mapping, error) {
	domain, err := c.requiredQuery("domain")
	if err != nil {
		return mapping{}, err
	}
	m, err := yamlMapping(c.req.Body)
	if err != nil {
		return mapping{}, err
	}
	key, err := singleKey(m)
	if err != nil {
		return mapping{}, err
	}
	ch, err := check(policy.Create, policy.KindHelper, domain+"/"+key, "")
	return mapping{checks: []Check{ch}}, err
}

// mapHelperDelete needs the domain the command resolved: a domain-less
// helper delete is unmappable, hence refused.
func mapHelperDelete(c *call) (mapping, error) {
	id, err := c.requiredQuery("id")
	if err != nil {
		return mapping{}, err
	}
	domain, err := c.requiredQuery("domain")
	if err != nil {
		return mapping{}, err
	}
	ch, err := check(policy.Delete, policy.KindHelper, domain+"/"+id, "")
	return mapping{checks: []Check{ch}}, err
}

// mapRefReplace: the body is decoded, and dry_run defaults to true when absent
// (the companion's own default). A dry run writes nothing: its plan is its
// response, which [RefReplaceChecks] maps before the confirmed write relies on
// it (preview parity by construction, D-74). A confirmed run is mapped only
// through a dry run of the same {old, new} checked in this invocation (D-77).
func mapRefReplace(c *call) (mapping, error) {
	var body struct {
		Old    *string `json:"old"`
		New    *string `json:"new"`
		DryRun *bool   `json:"dry_run"`
	}
	if err := json.Unmarshal(c.req.Body, &body); err != nil {
		return mapping{}, fmt.Errorf("body: %w", err)
	}
	if body.Old == nil || body.New == nil {
		return mapping{}, errors.New("body needs both old and new")
	}
	plan := findPlan(c.fact().RefReplacePlans, *body.Old, *body.New)
	if body.DryRun == nil || *body.DryRun {
		m := mapping{fromResponse: true}
		if plan != nil {
			checks, err := RefReplaceChecks(plan.Changes)
			if err != nil {
				return mapping{}, err
			}
			m.checks = checks
		}
		return m, nil
	}
	if plan == nil {
		return mapping{}, refuse("ref/replace without a checked dry run",
			"no dry run of {old %q, new %q} was checked in this invocation", *body.Old, *body.New)
	}
	checks, err := RefReplaceChecks(plan.Changes)
	return mapping{checks: checks}, err
}

func findPlan(plans []RefReplacePlan, oldVal, newVal string) *RefReplacePlan {
	for i := len(plans) - 1; i >= 0; i-- {
		if plans[i].Old == oldVal && plans[i].New == newVal {
			return &plans[i]
		}
	}
	return nil
}

// RefReplaceChecks maps a ref/replace plan: edit file:<location> for every
// distinct location (E-7), plus, for each change whose owner the caller
// resolved, edit <kind>:<id>/config — and when the change rewrites the
// owner's own id, a rename (E-5): edit the old address and create the new
// (D-77, R-3).
func RefReplaceChecks(changes []PlanChange) ([]Check, error) {
	set := newCheckSet()
	for _, ch := range changes {
		f, err := check(policy.Edit, policy.KindFile, ch.Location, "")
		if err != nil {
			return nil, err
		}
		set.add(f)
		if ch.Owner == nil {
			continue
		}
		switch ch.Owner.Kind {
		case policy.KindAutomation, policy.KindScript, policy.KindTemplate:
		default:
			return nil, fmt.Errorf("a change's typed owner is a %s, not an automation, script or template", ch.Owner.Kind)
		}
		edit, err := check(policy.Edit, ch.Owner.Kind, ch.Owner.ID, "config")
		if err != nil {
			return nil, err
		}
		set.add(edit)
		if ch.Owner.NewID != "" && ch.Owner.NewID != ch.Owner.ID {
			create, cerr := check(policy.Create, ch.Owner.Kind, ch.Owner.NewID, "")
			if cerr != nil {
				return nil, cerr
			}
			set.add(create)
		}
	}
	return set.list(), nil
}

// LocationsOutsidePlan returns the locations a confirmed ref/replace wrote that
// its checked dry run did not list, sorted and de-duplicated. A non-empty
// answer is reported after the fact (D-77): exit 1, rule `fail-closed: written
// outside the checked plan`.
func LocationsOutsidePlan(checked, written []PlanChange) []string {
	in := map[string]bool{}
	for _, c := range checked {
		in[c.Location] = true
	}
	seen := map[string]bool{}
	var out []string
	for _, w := range written {
		if !in[w.Location] && !seen[w.Location] {
			seen[w.Location] = true
			out = append(out, w.Location)
		}
	}
	sort.Strings(out)
	return out
}

// --- reads -------------------------------------------------------------------

func readQueryID(kind policy.Kind, key, sub string) mapper {
	return func(c *call) (mapping, error) {
		id, err := c.requiredQuery(key)
		if err != nil {
			return mapping{}, err
		}
		ch, err := check(policy.Read, kind, id, sub)
		return mapping{checks: []Check{ch}}, err
	}
}

func readConfigBlock(c *call) (mapping, error) {
	path, err := c.requiredQuery("path")
	if err != nil {
		return mapping{}, err
	}
	id, err := c.requiredQuery("id")
	if err != nil {
		return mapping{}, err
	}
	ch, err := check(policy.Read, policy.KindFile, path, "block/"+id)
	return mapping{checks: []Check{ch}}, err
}
