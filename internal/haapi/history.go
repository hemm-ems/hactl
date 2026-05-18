package haapi

import (
	"context"
	"net/url"
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
