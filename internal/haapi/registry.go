package haapi

// Registry types for Home Assistant entity, area, label, and floor registries.
//
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/entity_registry.py
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/area_registry.py
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/label_registry.py
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/config/floor_registry.py

// EntityRegistryEntry is an entry from the HA entity registry.
// WS command: config/entity_registry/list
// Source: homeassistant/components/config/entity_registry.py → websocket_list_entities
type EntityRegistryEntry struct {
	Categories map[string]string `json:"categories"`
	EntityID   string            `json:"entity_id"`
	Name       string            `json:"name"`
	Icon       string            `json:"icon"`
	Platform   string            `json:"platform"`
	DeviceID   string            `json:"device_id"`
	AreaID     string            `json:"area_id"`
	DisabledBy string            `json:"disabled_by"`
	HiddenBy   string            `json:"hidden_by"`
	OrigName   string            `json:"original_name"`
	UniqueID   string            `json:"unique_id"`
	Labels     []string          `json:"labels"`
	Aliases    []string          `json:"aliases"`
}

// AreaEntry is an entry from the HA area (room) registry.
// WS command: config/area_registry/list
// Source: homeassistant/components/config/area_registry.py → websocket_list_areas
type AreaEntry struct {
	AreaID  string   `json:"area_id"`
	Name    string   `json:"name"`
	FloorID string   `json:"floor_id"`
	Picture string   `json:"picture"`
	Icon    string   `json:"icon"`
	Labels  []string `json:"labels"`
	Aliases []string `json:"aliases"`
}

// LabelEntry is an entry from the HA label registry.
// WS command: config/label_registry/list
// Source: homeassistant/components/config/label_registry.py → websocket_list_labels
type LabelEntry struct {
	LabelID     string  `json:"label_id"`
	Name        string  `json:"name"`
	Color       string  `json:"color"`
	Description string  `json:"description"`
	Icon        string  `json:"icon"`
	CreatedAt   float64 `json:"created_at"`
	ModifiedAt  float64 `json:"modified_at"`
}

// FloorEntry is an entry from the HA floor registry.
// WS command: config/floor_registry/list
// Source: homeassistant/components/config/floor_registry.py → websocket_list_floors
type FloorEntry struct {
	Level      *int     `json:"level"`
	FloorID    string   `json:"floor_id"`
	Name       string   `json:"name"`
	Icon       string   `json:"icon"`
	Aliases    []string `json:"aliases"`
	CreatedAt  float64  `json:"created_at"`
	ModifiedAt float64  `json:"modified_at"`
}

// DeviceRegistryEntry is an entry from the HA device registry.
// WS command: config/device_registry/list
// Source: homeassistant/components/config/device_registry.py → websocket_list_devices
type DeviceRegistryEntry struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Manufacturer string   `json:"manufacturer"`
	Model        string   `json:"model"`
	AreaID       string   `json:"area_id"`
	SWVersion    string   `json:"sw_version"`
	Labels       []string `json:"labels"`
}
