package haapi

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/hemm-ems/hactl/internal/degeneracy"
)

// EnergyPreferences is the stored configuration of HA's Energy dashboard —
// which statistics feed which card.
// WS command: energy/get_prefs (request/response, so it fits sendCommand's
// one-write/one-read contract; the streaming sibling energy/subscribe is
// deliberately out of scope, see SPEC-energy.md).
// Source: homeassistant/components/energy/websocket_api.py → ws_get_prefs;
// shape: homeassistant/components/energy/data.py (EnergyPreferences).
// Only the rendered fields are typed — tolerant by construction (H-21's
// spirit): a per-source key beyond these changes nothing hactl shows.
type EnergyPreferences struct {
	EnergySources     []EnergySource      `json:"energy_sources"`
	DeviceConsumption []DeviceConsumption `json:"device_consumption"`
}

// EnergySource is one source on the energy dashboard.
//
// All five types HA defines — "grid", "solar", "battery", "gas", "water" —
// carry their statistics on stat_energy_from/stat_energy_to. flow_from and
// flow_to are the LEGACY grid form: HA's own store rewrites it into the flat
// fields while loading (data.py, _EnergyPreferencesStore, minor_version 3), so
// a current instance never answers with it and the arms exist only for an
// instance that has not migrated yet.
//
// The comment here used to say the opposite — that grid carries its statistics
// in flow_from/flow_to and every other type on the flat fields — and the unit
// fixture was written to match, which is how finding #26 survived: the only
// grid shape under test was the one no live instance produces.
type EnergySource struct {
	Type           string       `json:"type"`
	StatEnergyFrom string       `json:"stat_energy_from"`
	StatEnergyTo   string       `json:"stat_energy_to"`
	FlowFrom       []EnergyFlow `json:"flow_from"`
	FlowTo         []EnergyFlow `json:"flow_to"`
}

// EnergyFlow is one grid flow entry: consumption rows carry stat_energy_from,
// return-to-grid rows stat_energy_to.
type EnergyFlow struct {
	StatEnergyFrom string `json:"stat_energy_from"`
	StatEnergyTo   string `json:"stat_energy_to"`
}

// DeviceConsumption is one individually tracked device.
type DeviceConsumption struct {
	StatConsumption string `json:"stat_consumption"`
	Name            string `json:"name"`
}

// Identity reports the source type — a source that decoded without one cannot
// be routed to its stat fields.
func (s *EnergySource) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "type", Value: &s.Type}}
}

// Identity is conditional: a consumption row carries stat_energy_from, a
// return-to-grid row stat_energy_to. Requiring one unconditionally would
// poison the other direction's legitimate emptiness — but a flow with NEITHER
// did not decode, so the from-field is required exactly when to is empty.
func (f *EnergyFlow) Identity() []degeneracy.Field {
	if f.StatEnergyTo != "" {
		return nil
	}
	return []degeneracy.Field{{Name: "stat_energy_from", Value: &f.StatEnergyFrom}}
}

// Identity reports the statistic the device row tracks.
func (d *DeviceConsumption) Identity() []degeneracy.Field {
	return []degeneracy.Field{{Name: "stat_consumption", Value: &d.StatConsumption}}
}

// EnergyGetPrefs reads the Energy dashboard configuration.
// Behaviour on an instance without one (oracle-probed, see
// TestOracleEnergyGetPrefsUnconfigured): HA answers a WS error, never empty
// prefs — the caller maps that to an honest "no energy configuration".
func (ws *WSClient) EnergyGetPrefs(ctx context.Context) (*EnergyPreferences, error) {
	result, err := ws.sendCommand(ctx, "energy/get_prefs", nil)
	if err != nil {
		return nil, err
	}
	var prefs EnergyPreferences
	if err := json.Unmarshal(result, &prefs); err != nil {
		return nil, fmt.Errorf("parsing energy preferences: %w", err)
	}
	if err := degeneracy.Check("energy/get_prefs", &prefs); err != nil {
		return nil, err
	}
	return &prefs, nil
}
