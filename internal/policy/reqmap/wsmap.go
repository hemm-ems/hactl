package reqmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"

	"github.com/hemm-ems/hactl/internal/policy"
)

// entityRegistryFields maps an entity registry update's wire key to the
// grammar's registry field. new_entity_id is handled apart (E-5). A key not
// listed has no grammar field: the request is unmappable, hence refused.
var entityRegistryFields = map[string]string{
	"labels":      "labels",
	"area_id":     "area_id",
	"name":        "name",
	"icon":        "icon",
	"disabled_by": "disabled_by",
}

var deviceRegistryFields = map[string]string{
	"area_id":      "area_id",
	"labels":       "labels",
	"name_by_user": "name_by_user",
}

// changedKeys returns the params' keys other than the identity key, sorted.
func changedKeys(params map[string]any, identity string) []string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k != identity {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys
}

func mapEntityRegistryUpdate(c *call) (mapping, error) {
	id, err := c.requiredParam("entity_id")
	if err != nil {
		return mapping{}, err
	}
	var out []Check
	keys := changedKeys(c.req.Params, "entity_id")
	for _, k := range keys {
		if k == "new_entity_id" {
			newID, _, perr := c.param(k)
			if perr != nil {
				return mapping{}, perr
			}
			if newID == "" {
				return mapping{}, errors.New("new_entity_id is empty")
			}
			edit, cerr := check(policy.Edit, policy.KindEntity, id, "registry/entity_id")
			if cerr != nil {
				return mapping{}, cerr
			}
			create, cerr := check(policy.Create, policy.KindEntity, newID, "")
			if cerr != nil {
				return mapping{}, cerr
			}
			out = append(out, edit, create)
			continue
		}
		field, ok := entityRegistryFields[k]
		if !ok {
			return mapping{}, fmt.Errorf("entity registry key %q has no field in the policy grammar", k)
		}
		ch, cerr := check(policy.Edit, policy.KindEntity, id, "registry/"+field)
		if cerr != nil {
			return mapping{}, cerr
		}
		out = append(out, ch)
	}
	if len(keys) == 0 {
		// An update naming no field changes nothing; it is still a write to
		// the row, so it is never checked as nothing.
		ch, cerr := check(policy.Edit, policy.KindEntity, id, "registry")
		if cerr != nil {
			return mapping{}, cerr
		}
		out = append(out, ch)
	}
	return mapping{checks: out}, nil
}

func mapEntityRegistryRemove(c *call) (mapping, error) {
	id, err := c.requiredParam("entity_id")
	if err != nil {
		return mapping{}, err
	}
	ch, err := check(policy.Delete, policy.KindEntity, id, "")
	return mapping{checks: []Check{ch}}, err
}

func mapDeviceRegistryUpdate(c *call) (mapping, error) {
	id, err := c.requiredParam("device_id")
	if err != nil {
		return mapping{}, err
	}
	var out []Check
	keys := changedKeys(c.req.Params, "device_id")
	for _, k := range keys {
		field, ok := deviceRegistryFields[k]
		if !ok {
			return mapping{}, fmt.Errorf("device registry key %q has no field in the policy grammar", k)
		}
		ch, cerr := check(policy.Edit, policy.KindDevice, id, "registry/"+field)
		if cerr != nil {
			return mapping{}, cerr
		}
		out = append(out, ch)
	}
	if len(keys) == 0 {
		ch, cerr := check(policy.Edit, policy.KindDevice, id, "registry")
		if cerr != nil {
			return mapping{}, cerr
		}
		out = append(out, ch)
	}
	return mapping{checks: out}, nil
}

// kindWideCreate maps a create whose id Home Assistant mints: create on every
// id of the kind (D-75).
func kindWideCreate(kind policy.Kind) mapper {
	return func(*call) (mapping, error) {
		return mapping{checks: []Check{kindWide(policy.Create, kind, "")}}, nil
	}
}

// cascade appends one check per id, or the kind-wide check when the registries
// were not read in this invocation — a superset, never an omission (D-76).
type cascade struct {
	out []Check
	err error
}

func (cs *cascade) add(v policy.Verb, kind policy.Kind, id, sub string) {
	if cs.err != nil {
		return
	}
	ch, err := check(v, kind, id, sub)
	if err != nil {
		cs.err = err
		return
	}
	cs.out = append(cs.out, ch)
}

func mapLabelDelete(c *call) (mapping, error) {
	id, err := c.requiredParam("label_id")
	if err != nil {
		return mapping{}, err
	}
	cs := &cascade{}
	cs.add(policy.Delete, policy.KindLabel, id, "")
	reg := c.fact().Registries
	if reg == nil {
		cs.out = append(cs.out,
			kindWide(policy.Edit, policy.KindEntity, "registry/labels"),
			kindWide(policy.Edit, policy.KindDevice, "registry/labels"),
			kindWide(policy.Edit, policy.KindArea, ""))
		return mapping{checks: cs.out}, cs.err
	}
	for _, e := range reg.Entities {
		if slices.Contains(e.Labels, id) {
			cs.add(policy.Edit, policy.KindEntity, e.EntityID, "registry/labels")
		}
	}
	for _, d := range reg.Devices {
		if slices.Contains(d.Labels, id) {
			cs.add(policy.Edit, policy.KindDevice, d.ID, "registry/labels")
		}
	}
	for _, a := range reg.Areas {
		if slices.Contains(a.Labels, id) {
			cs.add(policy.Edit, policy.KindArea, a.ID, "")
		}
	}
	return mapping{checks: cs.out}, cs.err
}

func mapAreaDelete(c *call) (mapping, error) {
	id, err := c.requiredParam("area_id")
	if err != nil {
		return mapping{}, err
	}
	cs := &cascade{}
	cs.add(policy.Delete, policy.KindArea, id, "")
	reg := c.fact().Registries
	if reg == nil {
		cs.out = append(cs.out,
			kindWide(policy.Edit, policy.KindEntity, "registry/area_id"),
			kindWide(policy.Edit, policy.KindDevice, "registry/area_id"))
		return mapping{checks: cs.out}, cs.err
	}
	for _, e := range reg.Entities {
		if e.AreaID == id {
			cs.add(policy.Edit, policy.KindEntity, e.EntityID, "registry/area_id")
		}
	}
	for _, d := range reg.Devices {
		if d.AreaID == id {
			cs.add(policy.Edit, policy.KindDevice, d.ID, "registry/area_id")
		}
	}
	return mapping{checks: cs.out}, cs.err
}

func mapFloorDelete(c *call) (mapping, error) {
	id, err := c.requiredParam("floor_id")
	if err != nil {
		return mapping{}, err
	}
	cs := &cascade{}
	cs.add(policy.Delete, policy.KindFloor, id, "")
	reg := c.fact().Registries
	if reg == nil {
		cs.out = append(cs.out, kindWide(policy.Edit, policy.KindArea, ""))
		return mapping{checks: cs.out}, cs.err
	}
	for _, a := range reg.Areas {
		if a.FloorID == id {
			cs.add(policy.Edit, policy.KindArea, a.ID, "")
		}
	}
	return mapping{checks: cs.out}, cs.err
}

func mapDashboardSave(c *call) (mapping, error) {
	u, _, err := c.param("url_path")
	if err != nil {
		return mapping{}, err
	}
	u = canonicalDashboard(u)
	cfg, ok := c.req.Params["config"]
	if !ok || cfg == nil {
		return mapping{}, fmt.Errorf("parameter %q is missing", "config")
	}
	proposed, err := json.Marshal(cfg)
	if err != nil {
		return mapping{}, fmt.Errorf("config: %w", err)
	}
	stored, read := c.fact().Dashboards[u]
	if !read {
		whole, cerr := check(policy.Edit, policy.KindDashboard, u, "")
		return mapping{checks: []Check{whole}}, cerr
	}
	// Read, and Home Assistant holds no config: "nothing is stored" is an
	// empty config, not an unknown (D-126).
	prev := stored.Config
	if prev == nil {
		prev = EmptyDashboard
	}
	checks, err := DiffDashboard(u, prev, proposed)
	return mapping{checks: checks}, err
}

func mapDashboardCreate(c *call) (mapping, error) {
	u, err := c.requiredParam("url_path")
	if err != nil {
		return mapping{}, err
	}
	ch, err := check(policy.Create, policy.KindDashboard, u, "")
	return mapping{checks: []Check{ch}}, err
}

// mapDashboardDelete resolves the wire's dashboard_id — HA's storage id, not
// the url_path — through the lovelace/dashboards/list this invocation read
// (D-72). Without that read the request is unmappable.
func mapDashboardDelete(c *call) (mapping, error) {
	id, err := c.requiredParam("dashboard_id")
	if err != nil {
		return mapping{}, err
	}
	ids := c.fact().DashboardIDs
	if ids == nil {
		return mapping{}, refuse("dashboard id not resolved",
			"lovelace/dashboards/delete names dashboard_id %q, and no lovelace/dashboards/list in this invocation maps it to a url_path", id)
	}
	u, ok := ids[id]
	if !ok {
		return mapping{}, refuse("dashboard id not resolved",
			"dashboard_id %q is not in the lovelace/dashboards/list this invocation read", id)
	}
	ch, err := check(policy.Delete, policy.KindDashboard, canonicalDashboard(u), "")
	return mapping{checks: []Check{ch}}, err
}
