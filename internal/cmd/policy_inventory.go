package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/policy"
)

// The kinds `policy lint` reads beyond Home Assistant's own listings (D-116),
// and the registries its cross-rule checks resolve against (D-113..D-115).

var _ policy.RegistryInventory = (*haInventory)(nil)

// helperIDs are `<domain>/<id>` of every helper `helper ls` lists: the
// companion's YAML helpers and the helper-domain entities in the states (the
// storage half; a YAML helper's entity is there too, under the same id).
// Without the companion the storage half comes back with a
// *policy.PartialError carrying the companion's error.
func (h *haInventory) helperIDs() ([]string, error) {
	states, err := h.allStates()
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, s := range states {
		domain, object, _ := strings.Cut(s.EntityID, ".")
		if slices.Contains(helperDomains, domain) {
			ids = append(ids, domain+"/"+object)
		}
	}
	cc, err := h.companionClient()
	if err == nil {
		var resp *companion.HelpersResponse
		if resp, err = cc.ListHelpers(h.ctx, ""); err == nil {
			for _, hd := range resp.Helpers {
				ids = append(ids, hd.Domain+"/"+hd.ID)
			}
			return ids, nil
		}
		err = fmt.Errorf("listing helpers: %w", err)
	}
	return ids, &policy.PartialError{Err: err}
}

// templateIDs are the unique_ids of the companion's template list. An entity
// without one cannot be addressed and adds nothing.
func (h *haInventory) templateIDs() ([]string, error) {
	cc, err := h.companionClient()
	if err != nil {
		return nil, err
	}
	resp, err := cc.ListTemplates(h.ctx)
	if err != nil {
		return nil, fmt.Errorf("listing templates: %w", err)
	}
	var ids []string
	for _, t := range resp.Templates {
		if t.UniqueID != "" {
			ids = append(ids, t.UniqueID)
		}
	}
	return ids, nil
}

// configEntryIDs are the entry ids `config entries` lists.
func (h *haInventory) configEntryIDs() ([]string, error) {
	if h.down != nil {
		return nil, h.down
	}
	data, err := h.client().GetConfigEntries(h.ctx)
	if err != nil {
		return nil, h.note(fmt.Errorf("fetching config entries: %w", err))
	}
	var entries []configEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parsing config entries: %w", err)
	}
	if err := degeneracy.Check("/api/config/config_entries/entry", &entries); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		ids = append(ids, e.EntryID)
	}
	return ids, nil
}

// companionClient connects to the companion once per lint. Like the REST
// client it is built, not stored (see client): only the failure is kept. Its
// error is one line — a discovery failure's remediation text belongs to the
// commands that need the companion, not to an unchecked reason.
func (h *haInventory) companionClient() (*companion.Client, error) {
	if h.down != nil {
		return nil, h.down
	}
	if h.companionErr != nil {
		return nil, h.companionErr
	}
	// Discovery without COMPANION_URL asks the Supervisor over the websocket;
	// a failed connection is handed over too — it carries the reason.
	ws, wsErr := h.wsClient()
	if wsErr != nil {
		ws = haapi.NewWSClient(h.cfg.URL, h.cfg.Token)
	}
	url, err := companion.Discover(h.ctx, h.cfg, ws)
	if err != nil {
		first, _, _ := strings.Cut(err.Error(), "\n")
		h.companionErr = errors.New("companion: " + first)
		return nil, h.companionErr
	}
	cc := companion.New(url, h.cfg.CompanionToken)
	if ws.Connected() {
		cc = cc.WithIngressAuth(ws)
	}
	return cc, nil
}

// Refs implements policy.RegistryInventory: the areas or labels with their
// names, from the registry lists IDs reads.
func (h *haInventory) Refs(kind policy.Kind) ([]policy.Ref, error) {
	ws, err := h.wsClient()
	if err != nil {
		return nil, err
	}
	var refs []policy.Ref
	switch kind {
	case policy.KindArea:
		areas, err := ws.AreaRegistryList(h.ctx)
		if err != nil {
			return nil, h.note(fmt.Errorf("fetching area registry: %w", err))
		}
		for _, a := range areas {
			refs = append(refs, policy.Ref{ID: a.AreaID, Name: a.Name})
		}
	case policy.KindLabel:
		labels, err := ws.LabelRegistryList(h.ctx)
		if err != nil {
			return nil, h.note(fmt.Errorf("fetching label registry: %w", err))
		}
		for _, l := range labels {
			refs = append(refs, policy.Ref{ID: l.LabelID, Name: l.Name})
		}
	default:
		return nil, policy.ErrNotEnumerated
	}
	return refs, nil
}

// Registries implements policy.RegistryInventory: the entity and device
// registry rows, and each automation's config id from the states — the reads
// the service-call check resolves typed addresses with (D-107).
func (h *haInventory) Registries() (*policy.Registries, error) {
	states, err := h.allStates()
	if err != nil {
		return nil, err
	}
	ws, err := h.wsClient()
	if err != nil {
		return nil, err
	}
	ents, err := ws.EntityRegistryList(h.ctx)
	if err != nil {
		return nil, h.note(fmt.Errorf("fetching entity registry: %w", err))
	}
	devs, err := ws.DeviceRegistryList(h.ctx)
	if err != nil {
		return nil, h.note(fmt.Errorf("fetching device registry: %w", err))
	}
	reg := &policy.Registries{AutomationIDs: map[string]string{}}
	for _, e := range ents {
		reg.Entities = append(reg.Entities, policy.RegistryEntity{
			EntityID: e.EntityID, Platform: e.Platform, UniqueID: e.UniqueID,
			DeviceID: e.DeviceID, AreaID: e.AreaID, Labels: e.Labels,
		})
	}
	for _, d := range devs {
		reg.Devices = append(reg.Devices, policy.RegistryDevice{ID: d.ID, AreaID: d.AreaID, Labels: d.Labels})
	}
	for _, s := range states {
		if !strings.HasPrefix(s.EntityID, "automation.") {
			continue
		}
		var attrs struct {
			ID string `json:"id"`
		}
		if decErr := decodeStateAttributes(s, &attrs); decErr != nil {
			return nil, fmt.Errorf("parsing states: %w", decErr)
		}
		if attrs.ID != "" {
			reg.AutomationIDs[s.EntityID] = attrs.ID
		}
	}
	return reg, nil
}
