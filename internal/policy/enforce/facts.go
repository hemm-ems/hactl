package enforce

import (
	"context"
	"encoding/json"
	"maps"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// Fetcher asks Home Assistant the questions a mapping or a where: selector
// needs answered (D-55, D-59, D-76). Every method may fail; a failure never
// widens an allow — the mapping refuses (`fail-closed: reach unknown`) or
// checks a superset instead (reqmap's pole).
type Fetcher interface {
	// LiveStates is the live entity set (GET /api/states), with each
	// state's forwarding members (D-106) and an automation's config id
	// (D-107).
	LiveStates(ctx context.Context) (*reqmap.LiveStates, error)
	// Reach is Home Assistant's extract_from_target answer for a target,
	// asked with expand_group: true (D-59).
	Reach(ctx context.Context, target map[string]any) ([]string, error)
	// Registries are the entity, device and area registries. A device row
	// whose config entries Home Assistant did not report makes the answer
	// unusable for a config entry's cascade: the fetcher then returns
	// complete=false, and the cascade is checked kind-wide.
	Registries(ctx context.Context) (reg *reqmap.Registries, complete bool, err error)
	// Dashboard is the stored config of a dashboard (canonical url_path, ""
	// = default); a nil Config means Home Assistant holds none.
	Dashboard(ctx context.Context, urlPath string) (reqmap.StoredDashboard, error)
	// DashboardIDs maps each dashboard's storage id to its url_path.
	DashboardIDs(ctx context.Context) (map[string]string, error)
	// Live answers where: selectors.
	Live() policy.Live
	// Close releases what the fetcher opened.
	Close()
}

// factCache holds what this invocation established, tagged with the hash of
// the policy it was established under (D-72, R-7, A-8): a cache whose hash is
// not the session's is dropped before it is read, so nothing verified under
// one policy authorises a request under another.
//
// Two kinds of fact live here. Fetched facts (the live set, the registries, a
// dashboard's stored config, the dashboard id map) describe Home Assistant now
// and are dropped after every write this invocation sends, since the write may
// have changed them. Observed facts (a verified automation id, an options
// flow's handler, a checked ref/replace dry run, a dashboard config the
// command read) are answers this invocation received and stay for its length.
type factCache struct {
	hash string

	live         *reqmap.LiveStates
	liveRead     bool
	registries   *reqmap.Registries
	regComplete  bool
	regRead      bool
	dashboards   map[string]reqmap.StoredDashboard
	dashboardIDs map[string]string

	verifiedAutomations map[string]bool
	optionsHandlers     map[string]string
	refPlans            []reqmap.RefReplacePlan
}

func newFactCache(hash string) *factCache {
	return &factCache{
		hash:                hash,
		dashboards:          map[string]reqmap.StoredDashboard{},
		verifiedAutomations: map[string]bool{},
		optionsHandlers:     map[string]string{},
	}
}

// forHash returns the cache when it was built under hash, and a fresh one
// otherwise.
func (c *factCache) forHash(hash string) *factCache {
	if c == nil || c.hash != hash {
		return newFactCache(hash)
	}
	return c
}

// dropFetched forgets what describes Home Assistant now: a write was sent.
func (c *factCache) dropFetched() {
	c.live, c.liveRead = nil, false
	c.registries, c.regComplete, c.regRead = nil, false, false
	c.dashboards = map[string]reqmap.StoredDashboard{}
	c.dashboardIDs = nil
}

// facts renders the cache as the classifier's Facts. reach is this request's
// own extract_from_target answer, never cached.
func (c *factCache) facts(reach *reqmap.Reach, cascadeRegistries bool) *reqmap.Facts {
	f := &reqmap.Facts{
		PolicyHash:            c.hash,
		Reach:                 reach,
		Dashboards:            maps.Clone(c.dashboards),
		VerifiedAutomationIDs: maps.Clone(c.verifiedAutomations),
		OptionsFlowHandlers:   maps.Clone(c.optionsHandlers),
		RefReplacePlans:       append([]reqmap.RefReplacePlan(nil), c.refPlans...),
	}
	if c.liveRead {
		f.Live = c.live
	}
	if c.regRead && (c.regComplete || !cascadeRegistries) {
		f.Registries = c.registries
	}
	if c.dashboardIDs != nil {
		f.DashboardIDs = maps.Clone(c.dashboardIDs)
	}
	return f
}

// observe records the facts an answer establishes.
func (c *factCache) observe(req reqmap.Request, resp []byte) {
	switch {
	case req.Transport == reqmap.WS && req.Type == "lovelace/config":
		u, _ := req.Params["url_path"].(string)
		c.dashboards[canonicalDashboard(u)] = reqmap.StoredDashboard{Config: append(json.RawMessage(nil), resp...)}
	case req.Transport == reqmap.WS && req.Type == "lovelace/dashboards/list":
		var list []struct {
			ID      string `json:"id"`
			URLPath string `json:"url_path"`
		}
		if json.Unmarshal(resp, &list) == nil {
			ids := map[string]string{}
			for _, d := range list {
				if d.ID != "" {
					ids[d.ID] = d.URLPath
				}
			}
			c.dashboardIDs = ids
		}
	case req.Transport == reqmap.Companion && upperMethod(req.Method) == "GET" && routeOf(req.Path) == "/v1/config/automation":
		// Verified only when the companion answered with the very config id
		// asked for — it would otherwise fall back to an alias or an entity id
		// (D-78).
		asked := queryValue(req, "id")
		var got struct {
			ID string `json:"id"`
		}
		if asked != "" && json.Unmarshal(resp, &got) == nil && got.ID == asked {
			c.verifiedAutomations[asked] = true
		}
	case req.Transport == reqmap.REST && upperMethod(req.Method) == "POST" && routeOf(req.Path) == "/api/config/config_entries/options/flow":
		// D-83: the handler of an options flow is the config entry's entry_id.
		var got struct {
			FlowID  string `json:"flow_id"`
			Handler string `json:"handler"`
		}
		if json.Unmarshal(resp, &got) == nil && got.FlowID != "" && got.Handler != "" {
			c.optionsHandlers[got.FlowID] = got.Handler
		}
	case req.Transport == reqmap.Companion && upperMethod(req.Method) == "POST" && routeOf(req.Path) == "/v1/ref/replace":
		c.observeRefReplace(req, resp)
	}
}

// observeRefReplace keeps a dry run's answer as the plan the confirmed write
// of the same {old, new} is mapped through (D-77). The latest plan of a pair
// wins: a command that resolves the changes' typed owners records its plan
// after the answer arrives (Session.RecordRefReplacePlan), so its plan is the
// one used.
func (c *factCache) observeRefReplace(req reqmap.Request, resp []byte) {
	var body struct {
		Old    *string `json:"old"`
		New    *string `json:"new"`
		DryRun *bool   `json:"dry_run"`
	}
	if json.Unmarshal(req.Body, &body) != nil || body.Old == nil || body.New == nil {
		return
	}
	if body.DryRun != nil && !*body.DryRun {
		return // a confirmed write's answer is no plan
	}
	var got struct {
		Changes []struct {
			Location string `json:"location"`
			Path     string `json:"path"`
		} `json:"changes"`
	}
	if json.Unmarshal(resp, &got) != nil {
		return
	}
	plan := reqmap.RefReplacePlan{Old: *body.Old, New: *body.New, PolicyHash: c.hash}
	for _, ch := range got.Changes {
		plan.Changes = append(plan.Changes, reqmap.PlanChange{Location: ch.Location, Path: ch.Path})
	}
	c.refPlans = append(c.refPlans, plan)
}

// canonicalDashboard: an absent url_path, "" and "lovelace" are all the
// default dashboard (D-66 as refined).
func canonicalDashboard(u string) string {
	if u == "lovelace" {
		return ""
	}
	return u
}
