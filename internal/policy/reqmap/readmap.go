package reqmap

import (
	"fmt"
	"strings"

	"github.com/hemm-ems/hactl/internal/policy"
)

// Reads (W-2, D-138..D-147). A read row answers two things: what the request
// names itself (Checks — one thing read whole, refused before it is sent when
// denied) and how its answer is filtered before any command decodes it (a
// ReadFilter — rows dropped, parts masked, a single read refused on an address
// only the answer carries). The filter itself is applied by the session
// (internal/policy/enforce): the classifier stays pure and only names it.

// FilterKind names the shape of a read's answer, and so which filter applies.
type FilterKind string

// The answer shapes hactl reads. Each names its rows' address.
const (
	// FilterNone passes the answer as it is: the request's Checks decided it whole.
	FilterNone FilterKind = ""
	// FilterStates — GET /api/states: a state object per entity, dropped by
	// `read entity:<e>`, its `state` and attributes masked by `…/state` and
	// `…/attr/<name>`.
	FilterStates FilterKind = "states"
	// FilterState — GET /api/states/<e>: one state object, masked as above.
	FilterState FilterKind = "state"
	// FilterEntityRegistry — config/entity_registry/list: rows by `read
	// entity:<entity_id>`, fields masked by `…/registry/<field>`.
	FilterEntityRegistry FilterKind = "entity_registry"
	// FilterDeviceRegistry — config/device_registry/list: rows by `read
	// device:<id>`, fields masked by `…/registry/<field>`.
	FilterDeviceRegistry FilterKind = "device_registry"
	// FilterAreas, FilterLabels, FilterFloors — the registry lists, by
	// `read area:<area_id>`, `label:<label_id>`, `floor:<floor_id>`.
	FilterAreas  FilterKind = "areas"
	FilterLabels FilterKind = "labels"
	FilterFloors FilterKind = "floors"
	// FilterDashboards — lovelace/dashboards/list, by `read dashboard:<url_path>`.
	FilterDashboards FilterKind = "dashboards"
	// FilterDashboardConfig — lovelace/config: the views a deny covers are
	// dropped from the config (`read dashboard:<u>/views/<path>`, D-140).
	FilterDashboardConfig FilterKind = "dashboard_config"
	// FilterTraces — trace/list, by `read <domain>:<item_id>/trace`.
	FilterTraces FilterKind = "traces"
	// FilterStatisticIDs — recorder/list_statistic_ids and
	// recorder/get_statistics_metadata rows, by `read entity:<statistic_id>/stats`;
	// an external statistic (`domain:name`) has no address and is hidden.
	FilterStatisticIDs FilterKind = "statistic_ids"
	// FilterStatistics — recorder/statistics_during_period: a map keyed by
	// statistic id, filtered as FilterStatisticIDs.
	FilterStatistics FilterKind = "statistics"
	// FilterEnergyPrefs — energy/get_prefs: each source (and each legacy grid
	// flow, each tracked device) by `read entity:<statistic>` for every
	// statistic it names (D-142).
	FilterEnergyPrefs FilterKind = "energy_prefs"
	// FilterConfigEntries — GET …/config_entries/entry, by `read config_entry:<entry_id>`.
	FilterConfigEntries FilterKind = "config_entries"
	// FilterHistory — GET /api/history/period: one list per entity, by `read
	// entity:<e>/history`, each row masked like a state.
	FilterHistory FilterKind = "history"
	// FilterLogbook — GET /api/logbook: each row by `read entity:<e>` (D-142);
	// a row without an entity is hidden (D-141).
	FilterLogbook FilterKind = "logbook"
	// FilterServices — GET /api/services: each service by `read service:<d>.<s>`.
	FilterServices FilterKind = "services"
	// FilterCoreConfig — GET /api/config: the home's location, which is
	// zone.home's, masked by `read entity:zone.home/attr/<name>`.
	FilterCoreConfig FilterKind = "core_config"
	// FilterFiles — companion GET /v1/config/files, by `read file:<path>` (D-144).
	FilterFiles FilterKind = "files"
	// FilterConfigBlock — companion GET /v1/config/block: the block's
	// canonical id is in the answer only; `read file:<path>/block/<id>`
	// refuses it.
	FilterConfigBlock FilterKind = "config_block"
	// FilterRelated — companion GET /v1/related/entity: each related entity
	// by `read entity:<e>`, each stale reference as a hit (FilterRefHits).
	FilterRelated FilterKind = "related"
	// FilterRefHits — companion ref/scan, ref/grep, ref/entities: each hit
	// by `read file:<location>` plus the typed owner it lies in (D-142).
	FilterRefHits FilterKind = "ref_hits"
	// FilterTemplates — companion GET /v1/config/templates, by `read
	// template:<unique_id>`; an entity without one is hidden (D-141).
	FilterTemplates FilterKind = "templates"
	// FilterHelpers — companion GET /v1/config/helpers, by `read helper:<domain>/<id>`.
	FilterHelpers FilterKind = "helpers"
	// FilterHelper — companion GET /v1/config/helper: the address is in the
	// answer; `read helper:<domain>/<id>/config` refuses it.
	FilterHelper FilterKind = "helper"
)

// ReadFilter is a read's answer handling: its shape and the request facts the
// filter needs to address the answer.
type ReadFilter struct {
	Kind FilterKind
	// Entity is the entity a single state answer is of (FilterState).
	Entity string
	// URLPath is the canonical dashboard a config answer is of
	// (FilterDashboardConfig): "" is the default dashboard.
	URLPath string
	// Path is the file a block answer lies in (FilterConfigBlock).
	Path string
	// Rows is the key holding the hits of a wide read (FilterRefHits):
	// `hits` (ref/scan, ref/grep) or `entities` (ref/entities).
	Rows string
}

// filtered is a mapping that filters the answer and names nothing itself.
func filtered(kind FilterKind) mapper {
	return func(*call) (mapping, error) {
		return mapping{filter: ReadFilter{Kind: kind}}, nil
	}
}

// readSystem reads one system id whole: logs, check_config, wireguard,
// template.
func readSystem(id string) mapper {
	return func(*call) (mapping, error) {
		ch, err := check(policy.Read, policy.KindSystem, id, "")
		return mapping{checks: []Check{ch}}, err
	}
}

// readTraceList: the domain is automation, script or both; each row is decided
// by the filter.
func readTraceList(c *call) (mapping, error) {
	domain, _, err := c.param("domain")
	if err != nil {
		return mapping{}, err
	}
	switch domain {
	case "automation", "script", "":
		return mapping{filter: ReadFilter{Kind: FilterTraces}}, nil
	}
	return mapping{}, fmt.Errorf("trace domain %q is neither automation nor script", domain)
}

func readTraceGet(c *call) (mapping, error) {
	domain, err := c.requiredParam("domain")
	if err != nil {
		return mapping{}, err
	}
	item, err := c.requiredParam("item_id")
	if err != nil {
		return mapping{}, err
	}
	if domain != "automation" && domain != "script" {
		return mapping{}, fmt.Errorf("trace domain %q is neither automation nor script", domain)
	}
	ch, err := check(policy.Read, policy.Kind(domain), item, "trace")
	return mapping{checks: []Check{ch}}, err
}

// readDashboardConfig reads dashboard:<u> whole and drops the views a deny
// covers from its answer (D-140).
func readDashboardConfig(c *call) (mapping, error) {
	u, _, err := c.param("url_path")
	if err != nil {
		return mapping{}, err
	}
	u = canonicalDashboard(u)
	ch, err := check(policy.Read, policy.KindDashboard, u, "")
	return mapping{checks: []Check{ch}, filter: ReadFilter{Kind: FilterDashboardConfig, URLPath: u}}, err
}

// readStatistics reads entity:<id>/stats for every statistic the request names
// — an external statistic (`domain:name`) has no address and names nothing —
// and filters the answer by the same rule: a statistic hactl cannot address is
// hidden (D-141).
func readStatistics(kind FilterKind) mapper {
	return func(c *call) (mapping, error) {
		m := mapping{filter: ReadFilter{Kind: kind}}
		raw, ok := c.req.Params["statistic_ids"]
		if !ok || raw == nil {
			return m, nil
		}
		list, isList := raw.([]any)
		if !isList {
			if ss, isStrs := raw.([]string); isStrs {
				for _, s := range ss {
					list = append(list, s)
				}
			} else {
				return mapping{}, fmt.Errorf("statistic_ids is a %T, not a list", raw)
			}
		}
		for _, v := range list {
			s, isStr := v.(string)
			if !isStr {
				return mapping{}, fmt.Errorf("a statistic id is a %T, not a string", v)
			}
			if strings.Contains(s, ":") {
				continue
			}
			ch, err := check(policy.Read, policy.KindEntity, s, "stats")
			if err != nil {
				return mapping{}, err
			}
			m.checks = append(m.checks, ch)
		}
		return m, nil
	}
}

// readDiagnostics reads config_entry:<id> whole: its answer holds data and
// options in one body, so a partial verdict refuses it rather than masks.
func readDiagnostics(c *call) (mapping, error) {
	ch, err := check(policy.Read, policy.KindConfigEntry, c.vars["entry_id"], "")
	return mapping{checks: []Check{ch}, whole: true}, err
}

// readEntityState reads entity:<e> and masks its state and attributes.
func readEntityState(c *call) (mapping, error) {
	id := c.vars["entity_id"]
	ch, err := check(policy.Read, policy.KindEntity, id, "")
	return mapping{checks: []Check{ch}, filter: ReadFilter{Kind: FilterState, Entity: id}}, err
}

// queryEntities reads the entity ids a query parameter names (one or more,
// comma-separated, the parameter given once or more).
func queryEntities(c *call, key, sub string) ([]Check, error) {
	var out []Check
	for _, v := range c.query[key] {
		for id := range strings.SplitSeq(v, ",") {
			ch, err := check(policy.Read, policy.KindEntity, strings.TrimSpace(id), sub)
			if err != nil {
				return nil, err
			}
			out = append(out, ch)
		}
	}
	return out, nil
}

// readHistory reads entity:<e>/history for each entity filter_entity_id
// names, and filters the answer per entity — every entity's history without
// one (E-6).
func readHistory(c *call) (mapping, error) {
	cs, err := queryEntities(c, "filter_entity_id", "history")
	return mapping{checks: cs, filter: ReadFilter{Kind: FilterHistory}}, err
}

// readLogbook reads entity:<e> for the entity `entity=` names, and filters each
// row by its entity (D-142).
func readLogbook(c *call) (mapping, error) {
	cs, err := queryEntities(c, "entity", "")
	return mapping{checks: cs, filter: ReadFilter{Kind: FilterLogbook}}, err
}

// readQueryID reads <kind>:<?key>[/<sub>] whole.
func readQueryID(kind policy.Kind, key, sub string) mapper {
	return func(c *call) (mapping, error) {
		id, err := c.requiredQuery(key)
		if err != nil {
			return mapping{}, err
		}
		ch, err := check(policy.Read, kind, id, sub)
		return mapping{checks: []Check{ch}}, err
	}
}

// readConfigFile reads file:<path> whole (D-144: contents are not redacted).
// A resolved read inlines every file the path !includes, which only the
// companion knows: it needs read on every file (kind-wide, a superset).
func readConfigFile(c *call) (mapping, error) {
	path, err := c.requiredQuery("path")
	if err != nil {
		return mapping{}, err
	}
	ch, err := check(policy.Read, policy.KindFile, path, "")
	if err != nil {
		return mapping{}, err
	}
	m := mapping{checks: []Check{ch}}
	if vs := c.query["resolve"]; len(vs) > 0 && strings.EqualFold(vs[len(vs)-1], "true") {
		m.checks = append(m.checks, kindWide(policy.Read, policy.KindFile, ""))
	}
	return m, nil
}

// readConfigBlock reads file:<path>/block/<id> as the request names it, and the
// canonical id the answer carries (an alias or `[n]` addresses the same block).
func readConfigBlock(c *call) (mapping, error) {
	path, err := c.requiredQuery("path")
	if err != nil {
		return mapping{}, err
	}
	id, err := c.requiredQuery("id")
	if err != nil {
		return mapping{}, err
	}
	m := mapping{filter: ReadFilter{Kind: FilterConfigBlock, Path: path}}
	ch, err := check(policy.Read, policy.KindFile, path, "block/"+id)
	if err != nil {
		// An alias or a `[n]` index need not be a grammar segment; the
		// answer's canonical id decides then. The file itself must be one.
		fc, ferr := check(policy.Read, policy.KindFile, path, "")
		if ferr != nil {
			return mapping{}, ferr
		}
		m.checks = []Check{fc}
		return m, nil
	}
	m.checks = []Check{ch}
	return m, nil
}

// readRelated reads entity:<e> and filters what the companion relates to it.
func readRelated(c *call) (mapping, error) {
	id, err := c.requiredQuery("entity_id")
	if err != nil {
		return mapping{}, err
	}
	ch, err := check(policy.Read, policy.KindEntity, id, "")
	return mapping{checks: []Check{ch}, filter: ReadFilter{Kind: FilterRelated}}, err
}

// readRefHits is a wide read (E-6): each hit by its file and owner.
func readRefHits(rows string) mapper {
	return func(*call) (mapping, error) {
		return mapping{filter: ReadFilter{Kind: FilterRefHits, Rows: rows}}, nil
	}
}
