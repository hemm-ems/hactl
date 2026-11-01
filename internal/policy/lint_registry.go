package policy

import (
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// The checks in this file look below a rule's id: what its `where:` values
// name (D-113), whether another allow lets the agent satisfy a `where:` itself
// (D-114), and whether a deny protects only one of an automation's two
// addresses (D-115). They need the registries, which a RegistryInventory
// reads; with a plain Inventory each lists what it could not check.

// RegistryInventory is an Inventory that can also read the registries lint
// resolves selectors and owners against. The CLI implements it over Home
// Assistant; a test implements it over a fixture.
type RegistryInventory interface {
	Inventory
	// Refs returns every area (KindArea) or every label (KindLabel), id and
	// name — what a `where:` value is matched against.
	Refs(kind Kind) ([]Ref, error)
	// Registries returns the entity and device registry rows and the
	// automations' config ids. It is asked at most once per Lint, and only
	// when a check needs it.
	Registries() (*Registries, error)
}

// Registries is the registry snapshot CodeSelfGrant and CodeHalfProtected
// read.
type Registries struct {
	Entities []RegistryEntity
	Devices  []RegistryDevice
	// AutomationIDs maps an automation entity to its config id, its state's
	// `id` attribute (D-107). Absent: the automation has none.
	AutomationIDs map[string]string
}

// RegistryEntity is one entity registry row.
type RegistryEntity struct {
	EntityID string
	// Platform is the integration providing the entity: it tells a helper
	// (platform = its helper domain) or a template entity (platform
	// `template`) apart from other entities of its domain (D-107).
	Platform string
	UniqueID string
	DeviceID string
	// AreaID is the entity's own area, "" when it takes its device's.
	AreaID string
	Labels []string
}

// RegistryDevice is one device registry row.
type RegistryDevice struct {
	ID     string
	AreaID string
	Labels []string
}

// HelperDomains are the domains a helper entity lives in: an entity of one of
// them whose registry platform is the domain itself is a helper (D-107). The
// request layer's typed-control mapping and `helper ls` hold the same set;
// tests keep the copies in lockstep.
func HelperDomains() []string {
	return []string{
		"input_boolean", "input_number", "input_select", "input_text",
		"input_datetime", "input_button", "counter", "timer", "schedule",
	}
}

// entityRegistryFields are the `entity:<id>/registry/<field>` words in the
// kinds table's order: CodeHalfProtected names the first one still editable.
func entityRegistryFields() []string {
	for _, s := range KindSpecs() {
		if s.Kind != KindEntity {
			continue
		}
		for _, sub := range s.Subs {
			if sub.Word == "registry" {
				return sub.ParamValues
			}
		}
	}
	return nil
}

// registryCache holds one Lint run's registry reads, each asked at most once.
// A non-empty *Err means the read failed (or could not be asked: the reason
// is then ReasonNoLive or ReasonNotEnumerated).
type registryCache struct {
	refsAsked map[Kind]bool
	refs      map[Kind][]Ref
	refsErr   map[Kind]string

	regAsked bool
	reg      *registryIndex
	regErr   string
}

// registryIndex is Registries keyed for lookup, with the area and label
// names a `where:` matches by.
type registryIndex struct {
	// entityIDs are the registry's entities and the automations with a
	// config id, sorted and once each: what can own or be owned.
	entityIDs []string
	// ownedBy maps a typed address (`<kind>:<id>`, every form of the id) to
	// the entities it owns, in entityIDs order.
	ownedBy       map[string][]string
	entities      map[string]RegistryEntity
	devices       map[string]RegistryDevice
	automationIDs map[string]string
	areaNames     map[string]string
	labelNames    map[string]string
}

// refsOf returns the areas or labels, or the reason they could not be read.
func (l *linter) refsOf(k Kind) ([]Ref, string) {
	c := &l.regs
	if c.refsAsked == nil {
		c.refsAsked, c.refs, c.refsErr = map[Kind]bool{}, map[Kind][]Ref{}, map[Kind]string{}
	}
	if !c.refsAsked[k] {
		c.refsAsked[k] = true
		ri, ok := l.inv.(RegistryInventory)
		switch {
		case l.inv == nil:
			c.refsErr[k] = ReasonNoLive
		case !ok:
			c.refsErr[k] = ReasonNotEnumerated
		default:
			refs, err := ri.Refs(k)
			switch {
			case errors.Is(err, ErrNotEnumerated):
				c.refsErr[k] = ReasonNotEnumerated
			case err != nil:
				c.refsErr[k] = err.Error()
			default:
				c.refs[k] = refs
			}
		}
	}
	return c.refs[k], c.refsErr[k]
}

// registries returns the indexed registries, or the reason they could not be
// read.
func (l *linter) registries() (*registryIndex, string) {
	c := &l.regs
	if !c.regAsked {
		c.regAsked = true
		ri, ok := l.inv.(RegistryInventory)
		switch {
		case l.inv == nil:
			c.regErr = ReasonNoLive
		case !ok:
			c.regErr = ReasonNotEnumerated
		default:
			reg, err := ri.Registries()
			switch {
			case errors.Is(err, ErrNotEnumerated):
				c.regErr = ReasonNotEnumerated
			case err != nil:
				c.regErr = err.Error()
			default:
				c.reg = l.index(reg)
			}
		}
	}
	return c.reg, c.regErr
}

func (l *linter) index(reg *Registries) *registryIndex {
	idx := &registryIndex{
		entities: map[string]RegistryEntity{}, devices: map[string]RegistryDevice{},
		automationIDs: reg.AutomationIDs, areaNames: map[string]string{}, labelNames: map[string]string{},
	}
	for _, e := range reg.Entities {
		idx.entities[e.EntityID] = e
		idx.entityIDs = append(idx.entityIDs, e.EntityID)
	}
	for _, e := range sortedKeys(reg.AutomationIDs) {
		if _, dup := idx.entities[e]; !dup {
			idx.entityIDs = append(idx.entityIDs, e)
		}
	}
	sort.Strings(idx.entityIDs)
	idx.ownedBy = map[string][]string{}
	for _, e := range idx.entityIDs {
		kind, forms := idx.owner(e)
		for _, id := range forms {
			key := string(kind) + ":" + id
			idx.ownedBy[key] = append(idx.ownedBy[key], e)
		}
	}
	for _, d := range reg.Devices {
		idx.devices[d.ID] = d
	}
	// Names only widen what a granting allow's own where: can match; an
	// unreadable registry leaves the ids to match by.
	areas, _ := l.refsOf(KindArea)
	for _, a := range areas {
		idx.areaNames[a.ID] = a.Name
	}
	labels, _ := l.refsOf(KindLabel)
	for _, lb := range labels {
		idx.labelNames[lb.ID] = lb.Name
	}
	return idx
}

// kindUnchecked lists kind k unchecked for reason, unless k is listed
// already: one entry per kind.
func (l *linter) kindUnchecked(k Kind, reason string) {
	for _, u := range l.kinds {
		if u.Kind == k {
			return
		}
	}
	l.kinds = append(l.kinds, Unchecked{Kind: k, Reason: reason})
}

// ruleUnchecked lists rule r of profile pr unchecked for reason.
func (l *linter) ruleUnchecked(pr *profile, r Rule, reason string) {
	l.rules = append(l.rules, ruleUnchecked{rule: r, u: Unchecked{
		Kind: r.addr.kind, Reason: reason, Profile: pr.name, Rule: r.Text(), Line: r.Line,
	}})
}

// whereValues reports every `where:` value of the narrowed allow r that
// matches no area or label on the instance (D-113).
func (l *linter) whereValues(pr *profile, r Rule) {
	for _, key := range []struct {
		name   string
		kind   Kind
		values []string
	}{
		{"area", KindArea, r.Where.Area},
		{"label", KindLabel, r.Where.Label},
	} {
		var refs []Ref
		asked := false
		for _, v := range key.values {
			if onlyWildcards(v) {
				continue
			}
			if !asked {
				asked = true
				var reason string
				if refs, reason = l.refsOf(key.kind); reason != "" {
					l.kindUnchecked(key.kind, reason)
					break
				}
			}
			if !anyRefMatches(refs, []string{v}) {
				l.findings = append(l.findings, Finding{
					Code: CodeNoMatch, Profile: pr.name, Rule: r.Text(), Line: r.Where.Line,
					Message: "where: " + key.name + " " + strconv.Quote(v) + " matches no " + key.name +
						" on this instance (by id or name)",
				})
			}
		}
	}
}

// view is a Policy evaluating profile pr, for the checks that ask what the
// profile decides.
func (l *linter) view(pr *profile) *Policy {
	return &Policy{
		source: l.pol.source, exists: true, hash: l.pol.hash, profiles: l.pol.profiles,
		view: pr, selection: SelectedByActive,
	}
}

// matchedIDs are the live ids of a's kind its id matches, sorted; enumerated
// is false when lint has no complete list of the kind.
func (l *linter) matchedIDs(a address) (matched []string, enumerated bool) {
	ids, ok := l.live[a.kind]
	if !ok || l.partial[a.kind] {
		return nil, false
	}
	for _, id := range ids {
		if a.matchID(id) {
			matched = append(matched, id)
		}
	}
	return matched, true
}

// grantingAllow is the allow rule of pr a verdict names, nil when the
// verdict did not allow or no allow rule decided it (the default).
func grantingAllow(pr *profile, v Verdict) *Rule {
	if !v.Allowed || v.Line == 0 {
		return nil
	}
	for i := range pr.rules {
		if r := &pr.rules[i]; r.Effect == Allow && r.Line == v.Line && r.Text() == v.Rule {
			return r
		}
	}
	return nil
}

// selfGrant reports CodeSelfGrant for profile pr (D-114). Only a `default:
// deny` profile is checked: elsewhere a narrowed allow is redundant-allow
// already. For every narrowed allow n and every live entity (or device) n
// matches, it asks the profile whether an edit of what n's selector reads is
// allowed, and by which allow (never n itself, never the default):
//
//   - label on an entity: edit entity:<e>/registry/labels;
//   - area on an entity: edit entity:<e>/registry/area_id, and — for an
//     entity without an area of its own, which takes its device's — edit
//     device:<d>/registry/area_id;
//   - label or area on a device: edit device:<d>/registry/labels or
//     …/registry/area_id.
//
// One finding per (n, granting allow, field), on n's line. An entity outside
// the entity registry has no labels or area to change and is not counted.
// Nothing is refused at runtime; the finding is the whole feature.
func (l *linter) selfGrant(pr *profile) {
	if pr.deflt != Deny {
		return
	}
	var view *Policy
	var live Live
	for i := range pr.rules {
		n := pr.rules[i]
		if n.Effect != Allow || n.Where == nil || n.addr.any {
			continue
		}
		matched, enumerated := l.matchedIDs(n.addr)
		if !enumerated || len(matched) == 0 {
			continue
		}
		idx, reason := l.registries()
		if reason != "" {
			l.ruleUnchecked(pr, n, ReasonSelfGrantNotResolved)
			continue
		}
		if view == nil {
			view, live = l.view(pr), registryLive{idx}
		}
		sg := selfGrants{pr: pr, narrowed: &pr.rules[i], view: view, live: live}
		for _, id := range matched {
			sg.tryMatch(idx, id)
		}
		l.findings = append(l.findings, sg.findings(pr.name)...)
	}
}

// tryMatch asks, for one live match of the narrowed allow, every edit that
// would change what its selector reads (see selfGrant).
func (s *selfGrants) tryMatch(idx *registryIndex, id string) {
	where := s.narrowed.Where
	if s.narrowed.addr.kind == KindDevice {
		if len(where.Label) > 0 {
			s.try(Resource{Kind: KindDevice, ID: id, Sub: "registry/labels"}, id, false)
		}
		if len(where.Area) > 0 {
			s.try(Resource{Kind: KindDevice, ID: id, Sub: "registry/area_id"}, id, false)
		}
		return
	}
	e, inRegistry := idx.entities[id]
	if !inRegistry {
		return
	}
	if len(where.Label) > 0 {
		s.try(Resource{Kind: KindEntity, ID: id, Sub: "registry/labels"}, id, false)
	}
	if len(where.Area) > 0 {
		s.try(Resource{Kind: KindEntity, ID: id, Sub: "registry/area_id"}, id, false)
		if e.AreaID == "" && e.DeviceID != "" {
			s.try(Resource{Kind: KindDevice, ID: e.DeviceID, Sub: "registry/area_id"}, id, true)
		}
	}
}

// selfGrants collects one narrowed allow's self-grants.
type selfGrants struct {
	pr       *profile
	narrowed *Rule
	view     *Policy
	live     Live
	order    []selfGrantKey
	byKey    map[selfGrantKey]*selfGrant
}

type selfGrantKey struct {
	granting *Rule
	field    string
	// onDevice: the granting allow changes the device's field, which the
	// matched entity inherits.
	onDevice bool
}

type selfGrant struct {
	matched []string
	devices []string
}

// try asks whether res may be edited and by which allow; matched is the id
// of the narrowed allow's match it counts for.
func (s *selfGrants) try(res Resource, matched string, onDevice bool) {
	g := grantingAllow(s.pr, s.view.Decide(res, Edit, s.live))
	if g == nil || g == s.narrowed {
		return
	}
	field := res.Sub[strings.LastIndexByte(res.Sub, '/')+1:]
	key := selfGrantKey{granting: g, field: field, onDevice: onDevice}
	if s.byKey == nil {
		s.byKey = map[selfGrantKey]*selfGrant{}
	}
	sg, ok := s.byKey[key]
	if !ok {
		sg = &selfGrant{}
		s.byKey[key] = sg
		s.order = append(s.order, key)
	}
	if !slices.Contains(sg.matched, matched) {
		sg.matched = append(sg.matched, matched)
	}
	if onDevice && !slices.Contains(sg.devices, res.ID) {
		sg.devices = append(sg.devices, res.ID)
	}
}

// findings renders the collected self-grants in the order they were found;
// Lint orders every finding (sortFindings).
func (s *selfGrants) findings(profile string) []Finding {
	n := s.narrowed
	noun := "entities"
	if n.addr.kind == KindDevice {
		noun = "devices"
	}
	out := make([]Finding, 0, len(s.order))
	for _, key := range s.order {
		sg := s.byKey[key]
		closeAt := idPattern(n.addr)
		if key.onDevice {
			if len(sg.devices) == 1 {
				closeAt = string(KindDevice) + ":" + sg.devices[0]
			} else {
				closeAt = idPattern(key.granting.addr)
			}
		}
		out = append(out, Finding{
			Code: CodeSelfGrant, Profile: profile, Rule: n.Text(), Line: n.Line,
			Message: "the agent can satisfy this where: itself — " + key.granting.Text() +
				" (line " + strconv.Itoa(key.granting.Line) + ") lets it change " + key.field + " on " +
				strconv.Itoa(len(sg.matched)) + " of the " + noun + " this allow matches (e.g. " + sg.matched[0] +
				"); a guardrail does not stop that — to close it, deny the field edit: deny " +
				strconv.Quote(closeAt+"/registry/"+key.field) + ": [edit]",
		})
	}
	return out
}

// idPattern is a pattern's kind and id, without its sub path: `entity:light.*`.
// The bare `*` stays `*` but for a device grant, whose only kind is device.
func idPattern(a address) string {
	if a.any {
		return string(KindDevice) + ":*"
	}
	return string(a.kind) + ":" + strings.Join(a.id, "/")
}

// registryLive answers a `where:` from lint's registry snapshot, so a
// granting allow narrowed by its own selector is judged as enforcement would.
type registryLive struct{ idx *registryIndex }

func (rl registryLive) EntityArea(entityID string) (Ref, bool, error) {
	e, ok := rl.idx.entities[entityID]
	if !ok {
		return Ref{}, false, nil
	}
	area := e.AreaID
	if area == "" {
		area = rl.idx.devices[e.DeviceID].AreaID
	}
	if area == "" {
		return Ref{}, false, nil
	}
	return Ref{ID: area, Name: rl.idx.areaNames[area]}, true, nil
}

func (rl registryLive) EntityLabels(entityID string) ([]Ref, error) {
	return rl.labelRefs(rl.idx.entities[entityID].Labels), nil
}

func (rl registryLive) DeviceArea(deviceID string) (Ref, bool, error) {
	area := rl.idx.devices[deviceID].AreaID
	if area == "" {
		return Ref{}, false, nil
	}
	return Ref{ID: area, Name: rl.idx.areaNames[area]}, true, nil
}

func (rl registryLive) DeviceLabels(deviceID string) ([]Ref, error) {
	return rl.labelRefs(rl.idx.devices[deviceID].Labels), nil
}

func (rl registryLive) labelRefs(ids []string) []Ref {
	out := make([]Ref, 0, len(ids))
	for _, id := range ids {
		out = append(out, Ref{ID: id, Name: rl.idx.labelNames[id]})
	}
	return out
}

// halfProtected reports CodeHalfProtected for profile pr (D-115): a deny on a
// typed entry (automation, script, helper, template — its address or its
// `config`) holding edit or delete whose owned entity still allows an edit of
// a registry field, and a deny on `entity:<e>` holding edit whose owner still
// allows `edit <kind>:<id>/config` (automation, script, template) or `delete
// <kind>:<id>`. Owner and entity are resolved as the service-call check
// resolves them (D-107). One finding per rule, naming the first counterpart
// and the others after it; a rule whose counterparts cannot be resolved is listed
// unchecked.
func (l *linter) halfProtected(pr *profile) {
	var view *Policy
	for i := range pr.rules {
		r := pr.rules[i]
		if r.Effect != Deny || r.addr.any {
			continue
		}
		typed := ownerKind(r.addr.kind) && (len(r.addr.sub) == 0 || (len(r.addr.sub) == 1 && r.addr.sub[0] == "config")) &&
			(slices.Contains(r.Verbs, Edit) || slices.Contains(r.Verbs, Delete))
		entity := r.addr.kind == KindEntity && len(r.addr.sub) == 0 && slices.Contains(r.Verbs, Edit)
		if !typed && !entity {
			continue
		}
		// A partial list is fine here: a match in it owns what it owns.
		ids, ok := l.live[r.addr.kind]
		if !ok {
			continue
		}
		var matched []string
		for _, id := range ids {
			if r.addr.matchID(id) {
				matched = append(matched, id)
			}
		}
		if len(matched) == 0 {
			continue
		}
		idx, reason := l.registries()
		if reason != "" {
			l.ruleUnchecked(pr, r, ReasonCounterpartNotResolved)
			continue
		}
		if view == nil {
			view = l.view(pr)
		}
		live := registryLive{idx}
		var msg string
		if typed {
			msg = configHalf(view, live, idx, r, matched)
		} else {
			msg = entityHalf(view, live, idx, r, matched)
		}
		if msg != "" {
			l.findings = append(l.findings, Finding{Code: CodeHalfProtected, Profile: pr.name, Rule: r.Text(), Line: r.Line, Message: msg})
		}
	}
}

// ownerKind reports whether k is a typed entry that owns an entity.
func ownerKind(k Kind) bool {
	return k == KindAutomation || k == KindScript || k == KindHelper || k == KindTemplate
}

// configHalf is the message for a deny r on typed entries whose owned
// entities are not protected, "" when every one is.
func configHalf(view *Policy, live Live, idx *registryIndex, r Rule, matched []string) string {
	type counterpart struct{ entity, field string }
	var found []counterpart
	seen := map[string]bool{}
	for _, id := range matched {
		for _, e := range idx.owned(r.addr.kind, id) {
			if seen[e] {
				continue
			}
			seen[e] = true
			for _, f := range entityRegistryFields() {
				if view.Decide(Resource{Kind: KindEntity, ID: e, Sub: "registry/" + f}, Edit, live).Allowed {
					found = append(found, counterpart{e, f})
					break
				}
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	c := found[0]
	var others []string
	for _, o := range found[1:] {
		others = append(others, o.entity)
	}
	return r.headText() + " protects the " + string(r.addr.kind) + "'s config, but its entity " + c.entity +
		" can still be changed (edit entity:" + c.entity + "/registry/" + c.field + " is allowed) — to protect both, also deny " +
		strconv.Quote("entity:"+c.entity) + ": [write]" + more(others)
}

// entityHalf is the message for a deny r on entities whose owning typed
// entries are not protected, "" when every one is (or none is owned).
func entityHalf(view *Policy, live Live, idx *registryIndex, r Rule, matched []string) string {
	type counterpart struct{ owner, change string }
	var found []counterpart
	for _, e := range matched {
		kind, forms := idx.owner(e)
		if len(forms) == 0 {
			continue
		}
		checks := []struct {
			verb Verb
			sub  string
		}{{Edit, "config"}, {Delete, ""}}
		if kind == KindHelper {
			checks = checks[1:] // a helper's config has no edit of its own
		}
		for _, ch := range checks {
			// Every form of the owner's id must still allow it: a deny on
			// any one of them is what a command checks (D-107).
			allowed := true
			for _, id := range forms {
				if !view.Decide(Resource{Kind: kind, ID: id, Sub: ch.sub}, ch.verb, live).Allowed {
					allowed = false
					break
				}
			}
			if allowed {
				res := Resource{Kind: kind, ID: forms[0], Sub: ch.sub}
				found = append(found, counterpart{string(kind) + ":" + forms[0], ch.verb.String() + " " + res.String()})
				break
			}
		}
	}
	if len(found) == 0 {
		return ""
	}
	c := found[0]
	var others []string
	for _, o := range found[1:] {
		others = append(others, o.owner)
	}
	return r.headText() + " protects the entity, but its config " + c.owner + " can still be changed (" + c.change +
		" is allowed) — to protect both, also deny " + strconv.Quote(c.owner) + ": [write]" + more(others)
}

// more names the counterparts after the first, so a finding never ends in an
// unexplained count (D-128): at most three by name, then how many more.
func more(others []string) string {
	if len(others) == 0 {
		return ""
	}
	const named = 3
	if len(others) <= named {
		return "; the same holds for " + strings.Join(others, ", ")
	}
	return "; the same holds for " + strings.Join(others[:named], ", ") + " and " + strconv.Itoa(len(others)-named) + " more"
}

// owned are the registry entities the typed entry <kind>:<id> owns, sorted:
// an automation's by its config id, a script's by its registry unique_id or
// object id, a helper's (`<domain>/<id>`) by its domain and unique_id or
// object id, a template's by its unique_id or a suffix of it after a `-`.
func (idx *registryIndex) owned(kind Kind, id string) []string {
	return idx.ownedBy[string(kind)+":"+id]
}

// owner is the typed entry owning entity e and every form of its id (D-107):
// automation:<config id>; script:<unique_id> (its object id without a
// registry unique_id); helper:<domain>/<unique_id>, plus
// helper:<domain>/<object id> when the two differ, for an entity whose
// platform is its helper domain; template:<unique_id> plus every suffix after
// a `-` for platform template. No forms: e is owned by nothing lint knows.
func (idx *registryIndex) owner(e string) (Kind, []string) {
	domain, object, _ := strings.Cut(e, ".")
	if domain == "automation" {
		if cid := idx.automationIDs[e]; cid != "" {
			return KindAutomation, []string{cid}
		}
		return "", nil
	}
	row, ok := idx.entities[e]
	if !ok {
		return "", nil
	}
	switch {
	case domain == "script":
		if row.UniqueID != "" {
			return KindScript, []string{row.UniqueID}
		}
		return KindScript, []string{object}
	case slices.Contains(HelperDomains(), domain) && row.Platform == domain:
		uid := row.UniqueID
		if uid == "" {
			uid = object
		}
		forms := []string{domain + "/" + uid}
		if object != uid {
			forms = append(forms, domain+"/"+object)
		}
		return KindHelper, forms
	case row.Platform == "template" && row.UniqueID != "":
		uid := row.UniqueID
		forms := []string{uid}
		for i := strings.IndexByte(uid, '-'); i >= 0; i = strings.IndexByte(uid, '-') {
			if uid = uid[i+1:]; uid != "" {
				forms = append(forms, uid)
			}
		}
		return KindTemplate, forms
	}
	return "", nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
