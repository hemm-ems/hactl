package enforce

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// RuleRegistriesUnread refuses a registry edit whose reach cannot be worked
// out because the registries could not be read (D-186).
const RuleRegistriesUnread = "fail-closed: registries unread"

// ErrNoCompanion is a TargetFetcher's answer when no companion is configured
// for this instance: the config files cannot be searched, and the search is
// left out (D-186 names the limit) rather than counted as targeting.
var ErrNoCompanion = errors.New("no companion is configured for this instance")

// ConfigGrepHit is one place a config file names a container id.
type ConfigGrepHit struct {
	Location string
	// Key is the nearest enclosing mapping key of the match.
	Key string
	// MatchedValue is the whole string that matched.
	MatchedValue string
}

// ConfigGrepResult is a config-file search's answer. Truncated: more hits
// exist than were returned.
type ConfigGrepResult struct {
	Hits      []ConfigGrepHit
	Truncated bool
}

// TargetFetcher is a Fetcher that can also say what targets a container
// (D-186): Home Assistant's search/related, and a whole-word search of the
// config files through the companion.
type TargetFetcher interface {
	Related(ctx context.Context, itemType, itemID string) (map[string][]string, error)
	ConfigGrep(ctx context.Context, id string) (ConfigGrepResult, error)
}

// RegistryChange is one registry edit, as a command or a
// config/<entity|device>_registry/update or config/<area|floor|label>_registry/delete
// message states it. Exactly one of EntityID, DeviceID, DeletedArea,
// DeletedFloor and DeletedLabel is set.
type RegistryChange struct {
	EntityID string
	DeviceID string
	// DeletedArea, DeletedFloor, DeletedLabel: the edit deletes that area,
	// floor or label (D-195).
	DeletedArea  string
	DeletedFloor string
	DeletedLabel string
	// Labels is the full label list the edit sets; nil leaves labels alone.
	Labels *[]string
	// AreaSet: the edit sets the area to AreaID ("" clears it).
	AreaSet bool
	AreaID  string
	// NewEntityID is an entity rename's new id; "" for none.
	NewEntityID string
}

// targetKeys are the keys under which a config file names a container as a
// target (D-186).
var targetKeys = map[string]bool{"label_id": true, "area_id": true, "floor_id": true, "device_id": true, "entity_id": true}

// RegistryReachChecks is what a registry edit makes reachable (D-186, D-195):
// an entity it puts under a label, an area (and so that area's floor and
// labels — also by deleting the entity's own area, so that it falls back to
// its device's), or a new entity id, that an automation, a script or a template
// entity already targets, needs `control` — the targeting automation would
// switch it the next time it runs, and nothing would check that then.
//
// Containers of an entity: each of its labels; its area (its own, else its
// device's), that area's floor and labels; its device's labels; its own
// entity id. Only a container the edit adds counts; a container is targeted
// when search/related names an automation or a script for it, or the config
// files name it under a target key or inside a template. An answer hactl
// cannot get counts as targeting. With no profile in effect, `indirect:
// unchecked`, or a profile that denies nothing, it adds nothing.
func (s *Session) RegistryReachChecks(ctx context.Context, ch RegistryChange) ([]reqmap.Check, []Denial) {
	if !s.pol.IndirectChecked() || !DeniesAnything(s.pol) {
		return nil, nil
	}
	s.gather(ctx, reqmap.Request{}, needs{registries: true})
	s.mu.Lock()
	var reg *reqmap.Registries
	if s.cache.regRead {
		reg = s.cache.registries
	}
	s.mu.Unlock()
	subject := "entity:" + ch.EntityID
	switch {
	case ch.DeviceID != "":
		subject = "device:" + ch.DeviceID
	case ch.DeletedArea != "":
		subject = "area:" + ch.DeletedArea
	case ch.DeletedFloor != "":
		subject = "floor:" + ch.DeletedFloor
	case ch.DeletedLabel != "":
		subject = "label:" + ch.DeletedLabel
	}
	if reg == nil {
		return nil, []Denial{{Resource: subject, Verb: policy.Control.String(), Rule: RuleRegistriesUnread}}
	}
	gained := gainedContainers(reg, ch)
	if len(gained) == 0 {
		return nil, nil
	}
	var checks []reqmap.Check
	var denials []Denial
	for _, e := range sortedKeysOf(gained) {
		targeted := false
		for _, c := range gained[e] {
			if s.containerTargeted(ctx, c) {
				targeted = true
				break
			}
		}
		if !targeted {
			continue
		}
		if c, ok := entityCheck(policy.Control, e); ok {
			checks = append(checks, c)
		} else {
			denials = append(denials, Denial{Resource: "entity:" + e, Verb: policy.Control.String(), Rule: "fail-closed: unmappable resource entity:" + e})
		}
	}
	return dedupeChecks(checks), denials
}

// container is a label, area or floor id, or an entity id, by its
// search/related item type.
type container struct{ kind, id string }

func (c container) String() string { return c.kind + ":" + c.id }

// gainedContainers maps each entity the edit affects to the containers it
// is reachable through after the edit and was not before: the registries as
// they are, and as Home Assistant leaves them after the edit (D-186, D-195),
// compared entity by entity.
func gainedContainers(reg *reqmap.Registries, ch RegistryChange) map[string][]container {
	next, ok := applyChange(reg, ch)
	if !ok {
		return nil // not in the registry: Home Assistant refuses the edit
	}
	before, after := newRegIndex(reg), newRegIndex(next)
	out := map[string][]container{}
	for i, e := range reg.Entities {
		n := next.Entities[i]
		// A rename is checked on the entity the owner's rules name: its
		// current id (the new id is checked as created, E-5).
		id := e.EntityID
		if ch.EntityID == e.EntityID && ch.NewEntityID != "" {
			id = ch.NewEntityID
		}
		had := before.containersOf(e, before.devices[e.DeviceID], e.EntityID)
		var gained []container
		for k, c := range after.containersOf(n, after.devices[n.DeviceID], id) {
			if _, was := had[k]; !was {
				gained = append(gained, c)
			}
		}
		if len(gained) == 0 {
			continue
		}
		sort.Slice(gained, func(i, j int) bool { return gained[i].String() < gained[j].String() })
		out[e.EntityID] = gained
	}
	return out
}

// applyChange is the registries as Home Assistant leaves them after ch, in
// the same order; ok is false when ch names a row that does not exist.
//
// Deleting an area clears it from every entity and device whose own area it
// is; an entity whose own area is cleared is then in its device's area (Home
// Assistant's area fallback) — the area delete that moves an entity into an
// area a script targets (re-hunt 2026-11-20, B2). Deleting a floor clears it
// from its areas; deleting a label removes it from every entity, device and
// area holding it.
func applyChange(reg *reqmap.Registries, ch RegistryChange) (*reqmap.Registries, bool) {
	next := &reqmap.Registries{
		Entities: slices.Clone(reg.Entities),
		Devices:  slices.Clone(reg.Devices),
		Areas:    slices.Clone(reg.Areas),
	}
	switch {
	case ch.EntityID != "":
		i := slices.IndexFunc(next.Entities, func(e reqmap.RegistryEntity) bool { return e.EntityID == ch.EntityID })
		if i < 0 {
			return nil, false
		}
		applyEdit(&next.Entities[i].Labels, &next.Entities[i].AreaID, ch)
	case ch.DeviceID != "":
		i := slices.IndexFunc(next.Devices, func(d reqmap.RegistryDevice) bool { return d.ID == ch.DeviceID })
		if i < 0 {
			return nil, false
		}
		applyEdit(&next.Devices[i].Labels, &next.Devices[i].AreaID, ch)
	case ch.DeletedArea != "":
		deleteArea(next, ch.DeletedArea)
	case ch.DeletedFloor != "":
		for i := range next.Areas {
			if next.Areas[i].FloorID == ch.DeletedFloor {
				next.Areas[i].FloorID = ""
			}
		}
	case ch.DeletedLabel != "":
		deleteLabel(next, ch.DeletedLabel)
	}
	return next, true
}

// applyEdit applies an entity's or device's label and area edit.
func applyEdit(labels *[]string, area *string, ch RegistryChange) {
	if ch.Labels != nil {
		*labels = *ch.Labels
	}
	if ch.AreaSet {
		*area = ch.AreaID
	}
}

// deleteArea is Home Assistant's area delete: every entity and device in it
// loses its area — an entity then falls back to its device's.
func deleteArea(next *reqmap.Registries, area string) {
	for i := range next.Entities {
		if next.Entities[i].AreaID == area {
			next.Entities[i].AreaID = ""
		}
	}
	for i := range next.Devices {
		if next.Devices[i].AreaID == area {
			next.Devices[i].AreaID = ""
		}
	}
	next.Areas = slices.DeleteFunc(next.Areas, func(a reqmap.RegistryArea) bool { return a.ID == area })
}

// deleteLabel is Home Assistant's label delete: every entity, device and area
// loses the label.
func deleteLabel(next *reqmap.Registries, label string) {
	without := func(ls []string) []string {
		return slices.DeleteFunc(slices.Clone(ls), func(x string) bool { return x == label })
	}
	for i := range next.Entities {
		next.Entities[i].Labels = without(next.Entities[i].Labels)
	}
	for i := range next.Devices {
		next.Devices[i].Labels = without(next.Devices[i].Labels)
	}
	for i := range next.Areas {
		next.Areas[i].Labels = without(next.Areas[i].Labels)
	}
}

// regIndex is the registries by id.
type regIndex struct {
	areas   map[string]reqmap.RegistryArea
	devices map[string]reqmap.RegistryDevice
}

func newRegIndex(reg *reqmap.Registries) regIndex {
	idx := regIndex{areas: map[string]reqmap.RegistryArea{}, devices: map[string]reqmap.RegistryDevice{}}
	for _, a := range reg.Areas {
		idx.areas[a.ID] = a
	}
	for _, d := range reg.Devices {
		idx.devices[d.ID] = d
	}
	return idx
}

// containersOf is every container entity e (of device d) is reachable
// through as entityID: the id itself, its and its device's labels, its area
// (its own, else its device's), that area's floor and labels.
func (idx regIndex) containersOf(e reqmap.RegistryEntity, d reqmap.RegistryDevice, entityID string) map[string]container {
	out := map[string]container{}
	add := func(kind, id string) {
		if id != "" {
			c := container{kind, id}
			out[c.String()] = c
		}
	}
	add("entity", entityID)
	for _, l := range slices.Concat(e.Labels, d.Labels) {
		add("label", l)
	}
	area := e.AreaID
	if area == "" {
		area = d.AreaID
	}
	if area != "" {
		a := idx.areas[area]
		add("area", area)
		add("floor", a.FloorID)
		for _, l := range a.Labels {
			add("label", l)
		}
	}
	return out
}

// containerTargeted asks, once per invocation, whether anything targets c.
func (s *Session) containerTargeted(ctx context.Context, c container) bool {
	s.mu.Lock()
	if s.targeted == nil {
		s.targeted = map[string]bool{}
	}
	t, have := s.targeted[c.String()]
	s.mu.Unlock()
	if have {
		return t
	}
	t = s.askTargeted(ctx, c)
	s.mu.Lock()
	s.targeted[c.String()] = t
	s.mu.Unlock()
	return t
}

func (s *Session) askTargeted(ctx context.Context, c container) bool {
	tf, ok := s.fetcher.(TargetFetcher)
	if !ok || s.fetcher == nil {
		return true // cannot ask: counts as targeting
	}
	rel, err := tf.Related(own(ctx), c.kind, c.id)
	if err != nil || len(rel["automation"])+len(rel["script"]) > 0 {
		return true
	}
	res, err := tf.ConfigGrep(own(ctx), c.id)
	switch {
	case errors.Is(err, ErrNoCompanion):
		return false
	case err != nil:
		return true
	}
	for _, h := range res.Hits {
		if targetKeys[h.Key] || strings.Contains(h.MatchedValue, "{{") || strings.Contains(h.MatchedValue, "{%") {
			return true
		}
	}
	return res.Truncated
}

// registryChangeOf reads a registry update message as a RegistryChange; ok
// is false for a message that is not one.
func registryChangeOf(row string, msg map[string]any) (RegistryChange, bool) {
	var ch RegistryChange
	switch row {
	case "ws config/entity_registry/update":
		ch.EntityID, _ = msg["entity_id"].(string)
		ch.NewEntityID, _ = msg["new_entity_id"].(string)
	case "ws config/device_registry/update":
		ch.DeviceID, _ = msg["device_id"].(string)
	case "ws config/area_registry/delete":
		ch.DeletedArea, _ = msg["area_id"].(string)
		return ch, ch.DeletedArea != ""
	case "ws config/floor_registry/delete":
		ch.DeletedFloor, _ = msg["floor_id"].(string)
		return ch, ch.DeletedFloor != ""
	case "ws config/label_registry/delete":
		ch.DeletedLabel, _ = msg["label_id"].(string)
		return ch, ch.DeletedLabel != ""
	default:
		return ch, false
	}
	if ch.EntityID == "" && ch.DeviceID == "" {
		return ch, false
	}
	if v, ok := msg["labels"]; ok {
		labels := []string{}
		if list, isList := v.([]any); isList {
			for _, l := range list {
				labels = append(labels, fmt.Sprint(l))
			}
		}
		ch.Labels = &labels
	}
	if v, ok := msg["area_id"]; ok {
		ch.AreaSet = true
		if v != nil {
			ch.AreaID = fmt.Sprint(v)
		}
	}
	return ch, true
}

func sortedKeysOf[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
