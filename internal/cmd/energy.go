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

var energyCmd = &cobra.Command{
	Use:   "energy",
	Short: "Inspect the Energy dashboard configuration",
	Long: "Read what HA's Energy dashboard measures — which statistics feed which card. " +
		"Curated read of the energy/get_prefs WebSocket API (D-12: no generic WS passthrough).",
}

var energyShowCmd = &cobra.Command{
	Use:   "show",
	Short: "Show the Energy dashboard's sources and tracked devices",
	Long: "List every energy source (grid/solar/battery/gas/water) with the statistic ids it reads " +
		"from, and the individually tracked devices — so a caller can see which entity feeds which " +
		"card before touching anything. An instance whose Energy dashboard was never set up says " +
		"exactly that (HA answers an error, not empty preferences).",
	Args: cobra.NoArgs,
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
	Direction string `json:"direction"` // "consumption" | "production" | "return"
	Statistic string `json:"statistic"`
}

// energySourceRows flattens prefs into one row per statistic. Grid sources
// nest their statistics in flow_from/flow_to; every other type carries them
// on stat_energy_from (consumption/production) and stat_energy_to (battery
// charge / return).
func energySourceRows(prefs *haapi.EnergyPreferences) []energySourceRow {
	rows := make([]energySourceRow, 0, len(prefs.EnergySources))
	for _, s := range prefs.EnergySources {
		for _, f := range s.FlowFrom {
			rows = append(rows, energySourceRow{Type: s.Type, Direction: "consumption", Statistic: f.StatEnergyFrom})
		}
		for _, f := range s.FlowTo {
			rows = append(rows, energySourceRow{Type: s.Type, Direction: "return", Statistic: f.StatEnergyTo})
		}
		if s.StatEnergyFrom != "" {
			rows = append(rows, energySourceRow{Type: s.Type, Direction: "production", Statistic: s.StatEnergyFrom})
		}
		if s.StatEnergyTo != "" {
			rows = append(rows, energySourceRow{Type: s.Type, Direction: "return", Statistic: s.StatEnergyTo})
		}
	}
	return rows
}
