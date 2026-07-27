package haapi

import "github.com/hemm-ems/hactl/internal/degeneracy"

// Identity declarations for the HA wire records this package decodes (H-14).
//
// HA's WebSocket and REST APIs have no schema hactl can check against, so a
// renamed or removed field is invisible: encoding/json writes a zero value and
// returns no error, and a renderer prints that zero value as a plausible
// answer. `trace/get` did exactly that for months (D1). Each record below
// declares the field(s) without which it cannot be a real answer; the decode
// sites call degeneracy.Check, which poisons an absent identity with
// degeneracy.Marker and fails the call.
//
// A record whose zero value is a legitimate answer deliberately has no method
// here — LovelaceConfig (a dashboard with no views is a real, empty dashboard),
// LovelaceViewSummary (views are legitimately untitled and pathless), Context
// (HA leaves user_id/parent_id empty for its own state changes) and the
// transport envelopes. internal/degeneracy/sweep_test.go pins that
// classification — TestSweep_EveryWireStructIsClassified derives the json-tagged
// structs in this package from the source and fails on any that neither declares
// an Identity here nor carries a written reason in unidentifiedWireStructs.

// Identity reports the entity registry key. An entry with no entity_id is not
// an entity.
func (e *EntityRegistryEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "entity_id", Value: &e.EntityID}}
}

// Identity reports the area registry key.
func (a *AreaEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "area_id", Value: &a.AreaID}}
}

// Identity reports the label registry key.
func (l *LabelEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "label_id", Value: &l.LabelID}}
}

// Identity reports the floor registry key.
func (f *FloorEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "floor_id", Value: &f.FloorID}}
}

// Identity reports the user account id. Name and username can legitimately be
// blank on system-generated users; the id cannot.
func (u *UserEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "id", Value: &u.ID}}
}

// Identity reports the device registry key. Every other device field —
// including both name fields — is legitimately empty.
func (d *DeviceRegistryEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "id", Value: &d.ID}}
}

// Identity reports the path a dashboard is served under — the identifier every
// hactl dash command addresses it by. The `id` is identity only for
// storage-mode entries: a YAML-mode dashboard (a `lovelace: dashboards:` entry
// in configuration.yaml, or the YAML-mode default itself, which HA lists under
// url_path "lovelace") carries NO id on the wire at all — captured from HA
// 2026.7.4 (internal/integration/lovelace_oracle_test.go). Requiring it
// unconditionally made `dash ls` report UNPARSED on every instance with a
// YAML dashboard.
func (d *LovelaceDashboard) Identity() []degeneracy.Field {
	fields := []degeneracy.Field{{Name: "url_path", Value: &d.URLPath}}
	if d.Mode == "storage" {
		fields = append(fields, degeneracy.Field{Name: "id", Value: &d.ID})
	}
	return fields
}

// Identity reports the resource key and the URL it points at — a resource whose
// URL did not decode is not a resource.
func (r *LovelaceResource) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "id", Value: &r.ID},
		{Name: "url", Value: &r.URL},
	}
}

// Identity reports the integration domain. `cc ls` and `cc show` address an
// integration by it, and manifest/list never omits it (D68's neighbourhood).
func (m *IntegrationManifest) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "domain", Value: &m.Domain}}
}

// Identity reports the logger name and severity of a system log record. The
// message list and the exception are legitimately empty; a record with neither
// name nor level did not decode.
func (e *SystemLogEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "name", Value: &e.Name},
		{Name: "level", Value: &e.Level},
	}
}

// Identity reports the three fields that address a trace run. This is the
// list-side sibling of D1: TraceList keys its result map by domain+"."+item_id,
// so an all-zero decode produced a map keyed "." holding runs nothing could
// address — the same shape that made FormatCondensed print a bare ".".
func (t *TraceSummary) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "run_id", Value: &t.RunID},
		{Name: "domain", Value: &t.Domain},
		{Name: "item_id", Value: &t.ItemID},
	}
}

// Identity requires a reason whenever validate_config reports a section
// invalid. `valid` is not a string, so the zero value cannot be distinguished
// from "HA said false" — but HA never reports invalid without an error, so an
// empty error beside valid=false means the payload did not decode. A valid
// section returns nil: there is nothing that could have gone missing.
func (v *ValidateResult) Identity() []degeneracy.Field {
	if v.Valid {
		return nil
	}
	return []degeneracy.Field{{Name: "error", Value: &v.Error}}
}

// Identity reports the flow step discriminator. Every config-flow response HA
// returns — form, menu, create_entry, abort, external — carries a type, and
// hactl switches on it; an empty type is an unparsed step, not a new kind.
func (f *FlowResult) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "type", Value: &f.Type}}
}

// Identity reports a schema field's key. A field with no name cannot be filled
// in, so `config flow-step key=value` would silently drop the value.
func (s *SchemaField) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "name", Value: &s.Name}}
}

// Identity reports the service registry key. A zero decode here is not a
// legitimate answer: `svc call` refuses a service it cannot find in this list,
// so a renamed `domain` field would turn every service call into "not
// registered in Home Assistant".
func (s *ServiceDomain) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "domain", Value: &s.Domain}}
}
