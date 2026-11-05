package enforce

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// HAFetcher answers a session's questions from Home Assistant, through the
// same clients every command uses — so its own requests pass the session's
// gate too, as the reads (and the exempt extract_from_target) they are. It
// connects lazily, on the first question, so a command whose requests need no
// fact contacts nothing extra.
type HAFetcher struct {
	ctx        context.Context
	url, token string
	// cfgErr is why no URL and token were available (no .env): every
	// question then fails, and fails closed.
	cfgErr error

	mu   sync.Mutex
	rest *haapi.Client
	ws   *haapi.WSClient
	err  error

	live *registryLive
}

// NewHAFetcher answers from the instance at url with token. A non-nil cfgErr
// (the instance's .env could not be read) makes every question fail.
//
// ctx bounds the questions the where: selectors ask, which the evaluator
// asks without a context of its own.
func NewHAFetcher(ctx context.Context, url, token string, cfgErr error) *HAFetcher {
	f := &HAFetcher{ctx: ctx, url: url, token: token, cfgErr: cfgErr}
	f.live = &registryLive{fetch: f}
	return f
}

// LiveStates implements Fetcher: the entity ids of GET /api/states, each
// state's `entity_id` attribute — the members a group, a group platform
// entity or a scene forwards a call to (D-106) — and an automation's `id`
// attribute, its config id (D-107). A state without an entity_id makes the
// whole answer unusable: the live set is then unread, and a mapping that
// needs it fails closed (H-7).
func (f *HAFetcher) LiveStates(ctx context.Context) (*reqmap.LiveStates, error) {
	c, err := f.client()
	if err != nil {
		return nil, err
	}
	body, err := c.GetStates(ctx)
	if err != nil {
		return nil, err
	}
	var states []struct {
		EntityID   string `json:"entity_id"`
		Attributes struct {
			EntityID any `json:"entity_id"`
			ID       any `json:"id"`
		} `json:"attributes"`
	}
	if err := json.Unmarshal(body, &states); err != nil {
		return nil, fmt.Errorf("parsing /api/states: %w", err)
	}
	out := &reqmap.LiveStates{
		EntityIDs:     make([]string, 0, len(states)),
		Members:       map[string][]string{},
		AutomationIDs: map[string]string{},
	}
	for _, s := range states {
		if s.EntityID == "" {
			return nil, errors.New("/api/states holds a state without an entity_id")
		}
		out.EntityIDs = append(out.EntityIDs, s.EntityID)
		if m := memberIDs(s.Attributes.EntityID); len(m) > 0 {
			out.Members[s.EntityID] = m
		}
		if id, ok := s.Attributes.ID.(string); ok && id != "" && strings.HasPrefix(s.EntityID, "automation.") {
			out.AutomationIDs[s.EntityID] = id
		}
	}
	return out, nil
}

// memberIDs reads an `entity_id` attribute: a list of entity ids, or one id
// (a comma-separated list names each).
func memberIDs(v any) []string {
	var out []string
	add := func(s string) {
		for part := range strings.SplitSeq(s, ",") {
			if p := strings.TrimSpace(part); p != "" {
				out = append(out, p)
			}
		}
	}
	switch t := v.(type) {
	case string:
		add(t)
	case []any:
		for _, e := range t {
			if s, ok := e.(string); ok {
				add(s)
			}
		}
	}
	return out
}

// Reach implements Fetcher.
func (f *HAFetcher) Reach(ctx context.Context, target map[string]any) ([]string, error) {
	ws, err := f.socket(ctx)
	if err != nil {
		return nil, err
	}
	return ws.ExtractFromTarget(ctx, target)
}

// Registries implements Fetcher.
func (f *HAFetcher) Registries(ctx context.Context) (*reqmap.Registries, bool, error) {
	ws, err := f.socket(ctx)
	if err != nil {
		return nil, false, err
	}
	ents, err := ws.EntityRegistryList(ctx)
	if err != nil {
		return nil, false, err
	}
	devs, err := ws.DeviceRegistryList(ctx)
	if err != nil {
		return nil, false, err
	}
	devEntries, complete, err := ws.DeviceConfigEntries(ctx)
	if err != nil {
		return nil, false, err
	}
	areas, err := ws.AreaRegistryList(ctx)
	if err != nil {
		return nil, false, err
	}
	reg := &reqmap.Registries{}
	for _, e := range ents {
		reg.Entities = append(reg.Entities, reqmap.RegistryEntity{
			ID: e.ID, EntityID: e.EntityID, DeviceID: e.DeviceID, UniqueID: e.UniqueID, Platform: e.Platform, ConfigEntryID: e.ConfigEntryID,
			AreaID: e.AreaID, Labels: e.Labels,
		})
	}
	for _, d := range devs {
		reg.Devices = append(reg.Devices, reqmap.RegistryDevice{
			ID: d.ID, AreaID: d.AreaID, Labels: d.Labels, ConfigEntries: devEntries[d.ID],
		})
	}
	for _, a := range areas {
		reg.Areas = append(reg.Areas, reqmap.RegistryArea{ID: a.AreaID, FloorID: a.FloorID, Labels: a.Labels})
	}
	return reg, complete, nil
}

// Dashboard implements Fetcher. A dashboard Home Assistant holds no stored
// config for answers a nil Config.
func (f *HAFetcher) Dashboard(ctx context.Context, urlPath string) (reqmap.StoredDashboard, error) {
	ws, err := f.socket(ctx)
	if err != nil {
		return reqmap.StoredDashboard{}, err
	}
	raw, err := ws.DashboardConfigRaw(ctx, urlPath)
	if err != nil {
		var apiErr *haapi.APIError
		if errors.As(err, &apiErr) && apiErr.Code == haapi.WSErrCodeLovelaceConfigNotFound {
			return reqmap.StoredDashboard{}, nil
		}
		return reqmap.StoredDashboard{}, err
	}
	return reqmap.StoredDashboard{Config: raw}, nil
}

// DashboardIDs implements Fetcher.
func (f *HAFetcher) DashboardIDs(ctx context.Context) (map[string]string, error) {
	ws, err := f.socket(ctx)
	if err != nil {
		return nil, err
	}
	list, err := ws.DashboardList(ctx)
	if err != nil {
		return nil, err
	}
	ids := make(map[string]string, len(list))
	for _, d := range list {
		ids[d.ID] = d.URLPath
	}
	return ids, nil
}

// Live implements Fetcher: where: selectors answered from the registries,
// read once, on the first selector asked.
func (f *HAFetcher) Live() policy.Live { return f.live }

// Close implements Fetcher.
func (f *HAFetcher) Close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ws != nil {
		_ = f.ws.Close()
	}
}

func (f *HAFetcher) socket(ctx context.Context) (*haapi.WSClient, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cfgErr != nil {
		return nil, f.cfgErr
	}
	if f.ws != nil || f.err != nil {
		return f.ws, f.err
	}
	ws := haapi.NewWSClient(f.url, f.token)
	if err := ws.Connect(ctx); err != nil {
		f.err = fmt.Errorf("connecting to HA: %w", err)
		return nil, f.err
	}
	f.ws = ws
	return ws, nil
}

func (f *HAFetcher) client() (*haapi.Client, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cfgErr != nil {
		return nil, f.cfgErr
	}
	if f.rest == nil {
		f.rest = haapi.New(f.url, f.token)
	}
	return f.rest, nil
}

// registryLive answers where: selectors (D-55) from the entity, device, area
// and label registries. Any failure fails every question: the evaluator then
// refuses to grant on the selector and says so.
type registryLive struct {
	fetch *HAFetcher
	once  sync.Once
	err   error

	entities map[string]haapi.EntityRegistryEntry
	devices  map[string]haapi.DeviceRegistryEntry
	areas    map[string]haapi.AreaEntry
	labels   map[string]haapi.LabelEntry
}

// EntityArea is the entity's own area, else its device's (H-8).
func (l *registryLive) EntityArea(entityID string) (policy.Ref, bool, error) {
	if err := l.load(); err != nil {
		return policy.Ref{}, false, err
	}
	e, ok := l.entities[entityID]
	if !ok {
		return policy.Ref{}, false, nil
	}
	if e.AreaID != "" {
		ref, has := l.areaRef(e.AreaID)
		return ref, has, nil
	}
	ref, has := l.areaRef(l.devices[e.DeviceID].AreaID)
	return ref, has, nil
}

// EntityLabels are the entity's own labels (HA does not inherit the device's).
func (l *registryLive) EntityLabels(entityID string) ([]policy.Ref, error) {
	if err := l.load(); err != nil {
		return nil, err
	}
	return l.labelRefs(l.entities[entityID].Labels), nil
}

// DeviceArea is the device's area.
func (l *registryLive) DeviceArea(deviceID string) (policy.Ref, bool, error) {
	if err := l.load(); err != nil {
		return policy.Ref{}, false, err
	}
	ref, has := l.areaRef(l.devices[deviceID].AreaID)
	return ref, has, nil
}

// DeviceLabels are the device's labels.
func (l *registryLive) DeviceLabels(deviceID string) ([]policy.Ref, error) {
	if err := l.load(); err != nil {
		return nil, err
	}
	return l.labelRefs(l.devices[deviceID].Labels), nil
}

func (l *registryLive) load() error {
	l.once.Do(func() {
		ctx := l.fetch.ctx
		ws, err := l.fetch.socket(ctx)
		if err != nil {
			l.err = err
			return
		}
		ents, err := ws.EntityRegistryList(ctx)
		if err != nil {
			l.err = fmt.Errorf("fetching entity registry: %w", err)
			return
		}
		devs, err := ws.DeviceRegistryList(ctx)
		if err != nil {
			l.err = fmt.Errorf("fetching device registry: %w", err)
			return
		}
		areas, err := ws.AreaRegistryList(ctx)
		if err != nil {
			l.err = fmt.Errorf("fetching area registry: %w", err)
			return
		}
		labels, err := ws.LabelRegistryList(ctx)
		if err != nil {
			l.err = fmt.Errorf("fetching label registry: %w", err)
			return
		}
		l.entities = map[string]haapi.EntityRegistryEntry{}
		for _, e := range ents {
			l.entities[e.EntityID] = e
		}
		l.devices = map[string]haapi.DeviceRegistryEntry{}
		for _, d := range devs {
			l.devices[d.ID] = d
		}
		l.areas = map[string]haapi.AreaEntry{}
		for _, a := range areas {
			l.areas[a.AreaID] = a
		}
		l.labels = map[string]haapi.LabelEntry{}
		for _, lb := range labels {
			l.labels[lb.LabelID] = lb
		}
	})
	return l.err
}

func (l *registryLive) areaRef(id string) (policy.Ref, bool) {
	if id == "" {
		return policy.Ref{}, false
	}
	return policy.Ref{ID: id, Name: l.areas[id].Name}, true
}

func (l *registryLive) labelRefs(ids []string) []policy.Ref {
	out := make([]policy.Ref, 0, len(ids))
	for _, id := range ids {
		out = append(out, policy.Ref{ID: id, Name: l.labels[id].Name})
	}
	return out
}
