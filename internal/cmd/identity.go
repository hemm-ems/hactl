package cmd

import "github.com/hemm-ems/hactl/internal/degeneracy"

// Identity declarations for the HA REST payloads decoded in this package (H-14).
//
// haapi returns the REST bodies as raw bytes, so these structs — not the client
// — are where /api/states, /api/history, /api/logbook, /api/config and
// /api/config/config_entries land. They are exposed to exactly the failure that
// made every automation run render as PASS (D1): a renamed field decodes to a
// zero value and the table still renders, one plausible blank row per record.
//
// The attribute sub-structs (automationAttributes, scriptAttributes) are
// deliberately absent: a `restored` ghost entity legitimately arrives with an
// empty attribute set, and `auto ls` must keep listing it — poisoning those
// would cry wolf on the exact case hactl exists to surface.

// Identity reports the entity key and its state. HA rejects an empty state
// string (an entity that has none reports "unknown"/"unavailable"), so a blank
// one means the payload, not the entity, is empty.
func (e *entityState) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "entity_id", Value: &e.EntityID},
		{Name: "state", Value: &e.State},
	}
}

// Identity reports the automation's entity key and state. The `id` attribute is
// not part of it: a restored ghost has no config id, which `auto ls` reports.
func (a *automationEntity) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "entity_id", Value: &a.EntityID},
		{Name: "state", Value: &a.State},
	}
}

// Identity reports the script's entity key and state.
func (s *scriptEntity) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "entity_id", Value: &s.EntityID},
		{Name: "state", Value: &s.State},
	}
}

// Identity reports when the logbook row happened. Only `when` is universal:
// state-change rows carry entity_id, event rows carry domain+name+message, and
// every context_* field is legitimately empty when HA itself made the change —
// which is precisely what `ent who` reports.
func (l *logbookEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "when", Value: &l.When}}
}

// Identity reports the sample's state and timestamp — the two values every
// history consumer here reads. entity_id is not part of it: HA omits it from
// all but the first sample of a series under minimal_response, so requiring it
// would break the moment that flag is used.
func (h *historyEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "state", Value: &h.State},
		{Name: "last_changed", Value: &h.LastChanged},
	}
}

// Identity reports the same pair for the attribute-carrying history shape.
func (h *historyEntryFull) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "state", Value: &h.State},
		{Name: "last_changed", Value: &h.LastChanged},
	}
}

// Identity reports the config entry's id and the integration it belongs to.
// Everything else (title, reason, disabled_by) is legitimately empty.
func (c *configEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "entry_id", Value: &c.EntryID},
		{Name: "domain", Value: &c.Domain},
	}
}

// Identity reports HA's version. `hactl health` leads with it and the whole
// command exists to answer "what is this instance"; a versionless /api/config
// did not decode.
func (h *haConfig) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "version", Value: &h.Version}}
}

// Identity reports the repair issue's key and owning integration — how `hactl
// issues` addresses one, and what the repairs registry keys on.
func (i *haIssue) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "domain", Value: &i.Domain},
		{Name: "issue_id", Value: &i.IssueID},
	}
}
