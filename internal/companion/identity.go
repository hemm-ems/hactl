package companion

import "github.com/hemm-ems/hactl/internal/degeneracy"

// Identity declarations for the companion wire records (H-14).
//
// The companion seam already has a field-level contract (H-13: every json tag
// is documented, every documented property is decoded), but that contract is
// checked against the *vendored* spec. Between a companion release and the next
// `make sync-spec`, hactl decodes a wire it has no schema for — and a renamed
// property there degrades exactly like the HA wire does: zero values, no error,
// a rendered table of blanks. The identities below make that loud instead.
//
// List wrappers (ConfigFilesResponse, TemplatesResponse, ScriptsResponse,
// AutomationsResponse, HelpersResponse, RefEntitiesResponse, LogsResponse) have
// no method on purpose: an empty list is a legitimate answer, and poisoning it
// would turn "no helpers configured" into a failure. Their *elements* carry the
// identity instead, which is where a wire-shape change actually shows.
//
// AutomationDefinition has no method for the opposite reason: the companion
// reports an automation with no `id:` key as an empty id
// (routes/automations.py: `"id": item.get("id", "")`), and an id-less
// automation is legal HA. Poisoning it would fail the command on a config that
// is merely hand-written. The same holds for a template entity's unique_id
// (routes/templates.py: `uid = item.get("unique_id", "")`), which is why
// TemplateDefinition is identified by its domain alone.

// Identity reports the companion's health verdict and version — the two things
// GET /v1/health exists to answer.
func (h *HealthResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "status", Value: &h.Status},
		{Name: "version", Value: &h.Version},
	}
}

// Identity reports the companion version. The booleans beside it are
// legitimately false on a stock install.
func (s *StatusResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "version", Value: &s.Version}}
}

// Identity reports which file was read. Content is legitimately empty — an
// empty configuration file is a real answer.
func (r *ConfigFileResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "path", Value: &r.Path}}
}

// Identity reports the file and the block key that was read.
func (r *ConfigBlockResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "path", Value: &r.Path},
		{Name: "id", Value: &r.ID},
	}
}

// Identity reports the write acknowledgement. A write whose status did not
// decode is the D45 class: hactl would print "written" for an answer it never
// read.
func (r *ConfigWriteResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "status", Value: &r.Status}}
}

// Identity reports the shared write/delete acknowledgement status.
func (r *ConfigDeleteResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "status", Value: &r.Status}}
}

// Identity reports the entity the relation graph was asked about.
func (r *RelatedEntityResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "entity_id", Value: &r.EntityID}}
}

// Identity reports the neighbour and the edge kind. An edge with no
// relationship is not an answer — it is the row `ent related` prints blank.
func (e *RelatedEntityEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "entity_id", Value: &e.EntityID},
		{Name: "relationship", Value: &e.Relationship},
	}
}

// Identity reports where a stale reference lives and what was found there.
func (s *StaleRef) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "location", Value: &s.Location},
		{Name: "matched_value", Value: &s.MatchedValue},
	}
}

// Identity reports what the scan was for.
func (r *RefScanResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "target", Value: &r.Target}}
}

// Identity reports where a reference was found and what matched.
func (h *RefScanHit) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "location", Value: &h.Location},
		{Name: "matched_value", Value: &h.MatchedValue},
	}
}

// Identity reports where an entity-shaped value was found and what it was. Key
// is deliberately not part of it: a value at a bare list position has none.
func (e *RefEntity) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "location", Value: &e.Location},
		{Name: "matched_value", Value: &e.MatchedValue},
	}
}

// Identity reports whether the replace was a dry run or applied — the single
// field that decides whether hactl tells the user their config changed.
func (r *RefReplaceResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "status", Value: &r.Status}}
}

// Identity reports the rewritten location and both sides of the rewrite.
func (c *RefChange) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "location", Value: &c.Location},
		{Name: "before", Value: &c.Before},
		{Name: "after", Value: &c.After},
	}
}

// Identity reports the template entity's domain — the `template:` block key it
// was found under, which the companion always fills from _ENTITY_DOMAINS.
//
// unique_id is deliberately excluded even though `tpl cat/delete` addresses by
// it: HA does not require one on a template entity, and the companion reports
// such an entity with an empty unique_id rather than skipping it. An entity
// nothing can address is a real answer hactl must keep listing.
func (d *TemplateDefinition) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "domain", Value: &d.Domain}}
}

// Identity reports which template definition was returned.
func (r *TemplateResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "unique_id", Value: &r.UniqueID}}
}

// Identity reports the create acknowledgement and the id it created.
func (r *TemplateCreateResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "status", Value: &r.Status},
		{Name: "unique_id", Value: &r.UniqueID},
	}
}

// Identity reports the entity's domain, and deliberately not its unique_id.
//
// `domain` is a member of the companion's own _ENTITY_DOMAINS — a block's
// domain key, or the validated `--domain` parameter — so it is never a legal
// empty string. `unique_id` looks like the stronger identity and is not one:
// the route rejects an item with no `unique_id` KEY (`"unique_id" not in
// item`) and a block where no item has one, but neither check rejects the
// empty STRING, so `unique_id: ""` reaches this struct as a legitimate value.
// Declaring it would poison a real answer, which is finding #38's mistake
// repeated on a new struct — read out of the emitting route rather than
// assumed, per H-14.
func (r *TemplateEntityResult) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "domain", Value: &r.Domain},
	}
}

// Identity reports the script key. Alias and mode are legitimately absent.
func (d *ScriptDefinition) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "id", Value: &d.ID}}
}

// Identity reports which script definition was returned.
func (r *ScriptResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "id", Value: &r.ID}}
}

// Identity reports the create acknowledgement and the id it created.
func (r *ScriptCreateResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "status", Value: &r.Status},
		{Name: "id", Value: &r.ID},
	}
}

// AutomationDefinition has no Identity: see the package header — an automation
// with no `id:` is legal HA and the companion reports it with an empty id.

// Identity reports which automation definition was returned.
func (r *AutomationResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "id", Value: &r.ID}}
}

// Identity reports the create acknowledgement and the id it created. entity_id
// is excluded by design: the companion documents it as empty when HA never
// confirmed the entity.
func (r *AutomationCreateResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "status", Value: &r.Status},
		{Name: "id", Value: &r.ID},
	}
}

// Identity reports the config-check verdict's status. Valid is a pointer
// precisely so an omitted verdict stays distinguishable, and Errors is empty on
// a healthy config.
func (r *CheckConfigResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "status", Value: &r.Status}}
}

// Identity reports the helper key and its domain.
func (d *HelperDefinition) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "id", Value: &d.ID},
		{Name: "domain", Value: &d.Domain},
	}
}

// Identity reports which helper definition was returned. Source is deliberately
// excluded: a companion older than the release that added the field omits it,
// and an empty source is that companion's honest answer rather than a decode
// that fell through — poisoning it would break `helper show` against every
// companion still in the field. The drift this would otherwise catch is caught
// statically instead, by H-13's struct-tag-versus-spec sweep, which sees a
// renamed wire field without needing a payload.
func (r *HelperResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "id", Value: &r.ID},
		{Name: "domain", Value: &r.Domain},
	}
}

// Identity reports the domain the wiring verdict is about. Wired is a bool, so
// it cannot distinguish "false" from "never decoded" — the domain echo can, and
// a preview that acted on an undecoded verdict would silently report every
// instance unwired.
func (r *WiringResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "domain", Value: &r.Domain}}
}

// Identity reports where the walk stopped short. An entry with no location
// cannot be acted on and cannot even be reported to the operator.
func (s *SkippedFile) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "location", Value: &s.Location},
		{Name: "reason", Value: &s.Reason},
	}
}

// Identity reports the create acknowledgement and the id it created. entity_id
// is excluded: HA may not have created the entity yet.
func (r *HelperCreateResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "status", Value: &r.Status},
		{Name: "id", Value: &r.ID},
	}
}

// Identity reports the tunnel and its state. WireGuardIface/WireGuardMonitor
// deliberately have none: every one of their fields is optional, so no value
// there distinguishes "absent" from "not decoded".
func (r *WireGuardStatusResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "tunnel", Value: &r.Tunnel},
		{Name: "state", Value: &r.State},
	}
}

// Identity reports the peer's public key — a peer is nothing else.
func (p *WireGuardPeer) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "public_key", Value: &p.PublicKey}}
}

// Identity reports the action acknowledgement and the tunnel it acted on.
func (r *WireGuardActionResponse) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "status", Value: &r.Status},
		{Name: "tunnel", Value: &r.Tunnel},
	}
}

// Identity reports a log record's severity and logger. The message can be
// empty; a record with neither level nor logger did not decode.
func (e *LogEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{
		{Name: "level", Value: &e.Level},
		{Name: "name", Value: &e.Name},
	}
}

// Identity reports the Supervisor add-on slug — how discovery addresses the
// companion add-on.
func (a *addonEntry) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "slug", Value: &a.Slug}}
}

// Identity reports the slug of the add-on whose info was fetched.
func (a *addonInfo) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "slug", Value: &a.Slug}}
}
