package haapi

import "encoding/json"

// Lovelace types for Home Assistant dashboard management.
//
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/lovelace/
// Source: https://github.com/home-assistant/frontend/blob/dev/src/data/lovelace.ts

// LovelaceDashboard is a dashboard entry from lovelace/dashboards/list.
type LovelaceDashboard struct {
	ID            string `json:"id"`
	URLPath       string `json:"url_path"`
	Mode          string `json:"mode"`
	Title         string `json:"title"`
	Icon          string `json:"icon"`
	RequireAdmin  bool   `json:"require_admin"`
	ShowInSidebar bool   `json:"show_in_sidebar"`
}

// DashboardCreateParams holds parameters for creating a new storage-mode dashboard.
type DashboardCreateParams struct {
	URLPath       string `json:"url_path"`
	Title         string `json:"title"`
	Icon          string `json:"icon,omitempty"`
	RequireAdmin  bool   `json:"require_admin"`
	ShowInSidebar bool   `json:"show_in_sidebar"`
}

// LovelaceConfig is the top-level config for a Lovelace dashboard.
// Views are preserved as raw JSON to support arbitrary card types without data loss.
type LovelaceConfig struct {
	Views []json.RawMessage `json:"views"`
}

// LovelaceViewSummary holds the key fields of a view for display purposes.
// Extracted from the raw view JSON.
type LovelaceViewSummary struct {
	Title    string `json:"title"`
	Path     string `json:"path"`
	Icon     string `json:"icon"`
	Type     string `json:"type"`
	Cards    int    `json:"cards"`
	Sections int    `json:"sections"`
	Badges   int    `json:"badges"`
}

// ParseViewSummary extracts display-relevant fields from a raw view JSON.
func ParseViewSummary(raw json.RawMessage) LovelaceViewSummary {
	var v struct {
		Title    string            `json:"title"`
		Path     string            `json:"path"`
		Icon     string            `json:"icon"`
		Type     string            `json:"type"`
		Cards    []json.RawMessage `json:"cards"`
		Sections []json.RawMessage `json:"sections"`
		Badges   []json.RawMessage `json:"badges"`
	}
	_ = json.Unmarshal(raw, &v)
	return LovelaceViewSummary{
		Title:    v.Title,
		Path:     v.Path,
		Icon:     v.Icon,
		Type:     v.Type,
		Cards:    len(v.Cards),
		Sections: len(v.Sections),
		Badges:   len(v.Badges),
	}
}

// LovelaceResource is a registered frontend resource (JS module, CSS, etc.).
// WS command: lovelace/resources
type LovelaceResource struct {
	ID   string `json:"id"`
	Type string `json:"type"`
	URL  string `json:"url"`
}

// LovelaceInfo holds lovelace system information.
// WS command: lovelace/info
type LovelaceInfo struct {
	Mode         string `json:"mode"`
	ResourceMode string `json:"resource_mode"`
}
