package haapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/hemm-ems/hactl/internal/degeneracy"
)

// GetHistory calls GET /api/history/period/<startTime>?filter_entity_id=<entityID>&end_time=<endTime>.
// Returns the raw JSON response body (array of state change arrays).
//
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/history/__init__.py
// Response format: [[{"entity_id":..., "state":..., "attributes":{...}, "last_changed":...}, ...]]
func (c *Client) GetHistory(ctx context.Context, entityID, startTime, endTime string) ([]byte, error) {
	path := "/api/history/period/" + url.PathEscape(startTime)
	params := url.Values{}
	params.Set("filter_entity_id", entityID)
	if endTime != "" {
		params.Set("end_time", endTime)
	}
	return c.doGet(ctx, path+"?"+params.Encode())
}

// MinimalHistoryRow is one row of a minimal_response history answer. The
// first row of each entity is full (entity_id, state, last_changed,
// last_updated, empty attributes); every later row carries state and
// last_changed only — TestOracleDeadHistoryShape is the wire proof (HA
// 2026.11).
type MinimalHistoryRow struct {
	EntityID    string `json:"entity_id"`
	State       string `json:"state"`
	LastChanged string `json:"last_changed"`
}

// GetHistoryMinimal asks the recorder for the state changes of several
// entities between start and end, as cheaply as the API allows:
// minimal_response, no_attributes, significant changes only (HA's default),
// start-time state included (HA's default). The answer is keyed by entity_id,
// read from each group's first row: HA OMITS an entity it has no rows for — a
// recorder-excluded entity and an unknown one alike — rather than answering an
// empty group, so a position in the outer array names nothing.
func (c *Client) GetHistoryMinimal(ctx context.Context, start, end string, entityIDs []string) (map[string][]MinimalHistoryRow, error) {
	path := "/api/history/period/" + url.PathEscape(start)
	params := url.Values{}
	params.Set("filter_entity_id", strings.Join(entityIDs, ","))
	params.Set("end_time", end)
	params.Set("minimal_response", "")
	params.Set("no_attributes", "")
	body, err := c.doGet(ctx, path+"?"+params.Encode())
	if err != nil {
		return nil, err
	}
	var groups [][]MinimalHistoryRow
	if err := json.Unmarshal(body, &groups); err != nil {
		return nil, fmt.Errorf("parsing history: %w", err)
	}
	if err := degeneracy.Check("/api/history/period", &groups); err != nil {
		return nil, err
	}
	out := make(map[string][]MinimalHistoryRow, len(groups))
	for _, g := range groups {
		if len(g) == 0 || g[0].EntityID == "" {
			return nil, errors.New("parsing history: a group without an entity_id on its first row")
		}
		out[g[0].EntityID] = g
	}
	return out, nil
}
