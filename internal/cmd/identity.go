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

// `state` is deliberately absent from every Identity below (finding #38).
//
// It was an identity field on five structs, justified by this comment: "HA
// rejects an empty state string (an entity that has none reports
// 'unknown'/'unavailable'), so a blank one means the payload, not the entity,
// is empty." Home Assistant does no such thing. `sensor.strompreis_kategorie`
// on the reference instance served 62 of 407 history records over 400 days
// with `"state": ""`, the key present on every one, and a second entity on the
// same instance carries the same shape. Because the guard compares the decoded
// value against Go's zero value — it cannot see whether the wire carried the
// key at all — a legitimate empty state was indistinguishable from a renamed
// field, and `ent hist`/`ent anomalies` exited 1 with empty stdout rather than
// rendering the series.
//
// That premise was authored from the code's own model of Home Assistant rather
// than probed against one. It is the same mistake the paragraph above avoids
// for restored ghosts, one screen away.
//
// Every struct keeps an identity that empty cannot legitimately be, so a
// renamed or removed field is still caught at record level — the guard is
// corrected here, not weakened.

// Identity reports the entity key. `state` is not part of it: see above.
func (e *entityState) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "entity_id", Value: &e.EntityID},
	}
}

// Identity reports the automation's entity key. The `id` attribute is not part
// of it: a restored ghost has no config id, which `auto ls` reports. Nor is
// `state` — see above.
func (a *automationEntity) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "entity_id", Value: &a.EntityID},
	}
}

// Identity reports the script's entity key. `state` is not part of it: see above.
func (s *scriptEntity) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "entity_id", Value: &s.EntityID},
	}
}

// Identity reports when the logbook row happened. Only `when` is universal:
// state-change rows carry entity_id, event rows carry domain+name+message, and
// every context_* field is legitimately empty when HA itself made the change —
// which is precisely what `ent who` reports.
func (l *logbookEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "when", Value: &l.When}}
}

// Identity reports when the sample was taken. entity_id is not part of it: HA
// omits it from all but the first sample of a series under minimal_response, so
// requiring it would break the moment that flag is used. `state` is not part of
// it either — see above; this is the struct finding #38 was reported against.
func (h *historyEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "last_changed", Value: &h.LastChanged},
	}
}

// Identity reports the same for the attribute-carrying history shape.
func (h *historyEntryFull) Identity() []degeneracy.Field {
	return []degeneracy.Field{
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
