package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var energyCmd = family(&cobra.Command{
	Use:   "energy",
	Short: "Inspect the Energy dashboard configuration",
	Long: "Read what HA's Energy dashboard measures — which statistics feed which card. " +
		"Curated read of the energy/get_prefs WebSocket API (D-12: no generic WS passthrough).",
})

var energyShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the Energy dashboard's sources and tracked devices",
	Long: "List every energy source (grid/solar/battery/gas/water) with the statistic ids it reads " +
		"from, and the individually tracked devices — so a caller can see which entity feeds which " +
		"card before touching anything. An instance whose Energy dashboard was never set up says " +
		"exactly that (HA answers an error, not empty preferences).",
	Args: takesNone(),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEnergyShow(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	energyCmd.AddCommand(energyShowCmd)
	rootCmd.AddCommand(energyCmd)
}

// isEnergyUnconfigured recognizes HA's answer for an Energy dashboard that
// was never set up. Oracle-probed (TestOracleEnergyGetPrefsUnconfigured, HA
// stable 2026-07-30): the WS command fails with "No prefs" — an error, never
// empty preferences, so an empty document and a missing one cannot be
// conflated (H-7).
func isEnergyUnconfigured(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "no prefs")
}

func runEnergyShow(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return fmt.Errorf("connecting to HA: %w", connErr)
	}
	defer func() { _ = ws.Close() }()

	prefs, err := ws.EnergyGetPrefs(ctx)
	if isEnergyUnconfigured(err) {
		// An honest negative, not an error: the instance has no energy
		// dashboard, which is a real answer to "what does it measure".
		if flagJSON {
			enc := json.NewEncoder(w)
			enc.SetIndent("", "  ")
			return enc.Encode(map[string]any{"configured": false})
		}
		_, _ = fmt.Fprintln(w, "no energy configuration on this instance (set up via Settings → Dashboards → Energy)")
		return nil
	}
	if err != nil {
		return fmt.Errorf("fetching energy preferences: %w", err)
	}

	if flagJSON {
		// H-10: the same joined view the tables show — one row per statistic
		// with its source type and direction, plus the tracked devices.
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{
			"configured":         true,
			"sources":            energySourceRows(prefs),
			"device_consumption": prefs.DeviceConsumption,
		})
	}

	rows := energySourceRows(prefs)
	if len(rows) == 0 && len(prefs.DeviceConsumption) == 0 {
		return emitEmptyList(w, "energy dashboard is configured but tracks nothing yet")
	}

	if len(rows) > 0 {
		tbl := &format.Table{
			Headers: []string{"type", "direction", "statistic"},
			Rows:    make([][]string, len(rows)),
		}
		for i, r := range rows {
			tbl.Rows[i] = []string{r.Type, r.Direction, r.Statistic}
		}
		if err := tbl.Render(w, format.RenderOpts{Top: flagTop, Full: flagFull, JSON: false, Compact: true}); err != nil {
			return err
		}
	}
	if len(prefs.DeviceConsumption) > 0 {
		_, _ = fmt.Fprintf(w, "\ntracked devices:\n")
		for _, d := range prefs.DeviceConsumption {
			if d.Name != "" {
				_, _ = fmt.Fprintf(w, "  %s (%s)\n", d.StatConsumption, d.Name)
			} else {
				_, _ = fmt.Fprintf(w, "  %s\n", d.StatConsumption)
			}
		}
	}
	return nil
}

// energySourceRow is one statistic feeding the energy dashboard, flattened
// from HA's per-type nesting so grid flows and simple sources read the same.
type energySourceRow struct {
	Type      string `json:"type"`
	Direction string `json:"direction"` // see energyDirectionByType
	Statistic string `json:"statistic"`
}

// energyDirections is what the two statistic fields of one source type mean.
// An empty string is "this type has no such field", which is not the same as
// "this type is unknown" — both render as energyDirectionUnknown, but only the
// second says nothing can be concluded.
type energyDirections struct{ From, To string }

// energyDirectionByType is the whole rule, and the reason it is a table.
//
// A statistic's direction is a property of the SOURCE TYPE and the field it
// sits on, never of the field alone — and deriving it from the field alone is
// finding #26: `stat_energy_from` was labelled "production" for every type, so
// a real instance's grid import and gas consumption both read as production
// while the same file called the grid's flow_from "consumption". The two
// halves of one rule sat eight lines apart in one function and disagreed.
//
// The values come from HA's own descriptions of each TypedDict in
// homeassistant/components/energy/data.py, not from reasoning about what a
// name ought to mean:
//
//	GridSourceType.stat_energy_from   "Import meter - kWh consumed from grid"
//	GridSourceType.stat_energy_to     "Export meter … kWh returned to grid"
//	SolarSourceType                   "the source of energy production"
//	BatterySourceType.stat_rate       "positive when discharging, negative when charging"
//	GasSourceType                     "the source of gas consumption"
//	WaterSourceType                   "the source of water consumption"
//
// Battery is the one type the three-word vocabulary could not describe
// honestly, so it names what HA names (D-16): energy leaving the battery is a
// discharge, energy entering it is a charge. Calling the first "production"
// would say a battery generates energy, which is the same class of confident
// wrongness this fix removes.
//
// The legacy grid form (flow_from/flow_to, migrated away by HA on load) reads
// the same two entries, so the modern and legacy paths cannot drift apart
// again: there is one place left to be wrong.
var energyDirectionByType = map[string]energyDirections{
	"grid":    {From: "consumption", To: "return"},
	"solar":   {From: "production"},
	"battery": {From: "discharge", To: "charge"},
	"gas":     {From: "consumption"},
	"water":   {From: "consumption"},
}

// energyDirectionUnknown is what a source type this build has never heard of
// gets. HA's SourceType union is closed and TestOracleEnergySourceTypes fails
// the build when it gains a member, so reaching this in practice means running
// against an HA newer than the oracle — at which point the statistic is still
// worth showing and the claim about it is not. H-14's spirit: a value that did
// not resolve is spelled out, never rendered as an answer.
const energyDirectionUnknown = "unknown"

func energyDirection(sourceType string, to bool) string {
	d, known := energyDirectionByType[sourceType]
	if !known {
		return energyDirectionUnknown
	}
	answer := d.From
	if to {
		answer = d.To
	}
	if answer == "" {
		return energyDirectionUnknown
	}
	return answer
}

// energySourceRows flattens prefs into one row per statistic.
//
// Every current instance answers with the flat stat_energy_* fields: HA's
// store migrates the flow_from/flow_to grid form away while loading it
// (_EnergyPreferencesStore._async_migrate_func, minor_version 3). The flow
// arms are kept for an instance old enough not to have migrated yet, and route
// through the same table as everything else.
func energySourceRows(prefs *haapi.EnergyPreferences) []energySourceRow {
	rows := make([]energySourceRow, 0, len(prefs.EnergySources))
	for _, s := range prefs.EnergySources {
		for _, f := range s.FlowFrom {
			rows = append(rows, energySourceRow{Type: s.Type, Direction: energyDirection(s.Type, false), Statistic: f.StatEnergyFrom})
		}
		for _, f := range s.FlowTo {
			rows = append(rows, energySourceRow{Type: s.Type, Direction: energyDirection(s.Type, true), Statistic: f.StatEnergyTo})
		}
		if s.StatEnergyFrom != "" {
			rows = append(rows, energySourceRow{Type: s.Type, Direction: energyDirection(s.Type, false), Statistic: s.StatEnergyFrom})
		}
		if s.StatEnergyTo != "" {
			rows = append(rows, energySourceRow{Type: s.Type, Direction: energyDirection(s.Type, true), Statistic: s.StatEnergyTo})
		}
	}
	return rows
}
