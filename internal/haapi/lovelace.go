package haapi

import (
	"encoding/json"
	"fmt"
)

// Lovelace types for Home Assistant dashboard management.
//
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/lovelace/
// Source: https://github.com/home-assistant/frontend/blob/dev/src/data/lovelace.ts

// WSErrCodeLovelaceConfigNotFound is the error code `lovelace/config` answers
// with when the requested dashboard has no stored config — for the default
// dashboard (no url_path) that is exactly HA's auto-generated state, and for a
// named one the state between its creation and its first save.
//
// Captured from HA 2026.7.4 (internal/integration/lovelace_oracle_test.go):
// {"code":"config_not_found","message":"No config found."}
//
// The same code also answers "there is no such dashboard"
// ({"code":"config_not_found","message":"Unknown config specified: x"},
// measured on the reference instance 2026-07-31): HA's handler sends it from
// two places, and the difference is an English message rather than a code. A
// caller that classifies on this constant must therefore have resolved the
// url_path first — see internal/cmd's classifyDashboardConfig.
const WSErrCodeLovelaceConfigNotFound = "config_not_found"

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

// ParseLovelaceConfig parses a raw `lovelace/config` document. A dashboard
// with no views is a real, empty dashboard, so no degeneracy identity applies.
func ParseLovelaceConfig(raw json.RawMessage) (*LovelaceConfig, error) {
	var cfg LovelaceConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parsing dashboard config: %w", err)
	}
	return &cfg, nil
}

// LovelaceStrategyType returns the type of the dashboard-level strategy in a
// raw `lovelace/config` document, or "" when it carries none.
//
// A strategy dashboard has no `views` key at all: the frontend builds its views
// from the strategy at view time. Home Assistant creates one itself during
// onboarding — the `map` dashboard, whose entire stored config is
// {"strategy":{"type":"map"}} (lovelace/__init__.py _create_map_dashboard,
// HA 2026.7.4) — so this is stock shape on every instance, and it is why
// "no views" alone was an answer that read as "this dashboard is empty".
func LovelaceStrategyType(raw json.RawMessage) string {
	var doc struct {
		Strategy struct {
			Type string `json:"type"`
		} `json:"strategy"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return ""
	}
	return doc.Strategy.Type
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

// There is deliberately no `lovelace/info` client here. That route answers
// only {"resource_mode": "storage"|"yaml"} — it does NOT emit the `mode` field
// hactl once decoded, and resource_mode describes where frontend resources are
// configured, not which state the default dashboard is in (captured from HA
// 2026.7.4 in both states: internal/integration/lovelace_oracle_test.go).
// A dashboard is classified by attempting `lovelace/config` instead
// (internal/cmd's classifyDashboardConfig, D-6).
