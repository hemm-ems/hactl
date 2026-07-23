package cmd

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var areaCmd = &cobra.Command{
	Use:   "area",
	Short: "Manage areas (rooms)",
	Long:  "List, create, and delete Home Assistant areas (rooms).",
}

var areaLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List all areas",
	Long:  "Show all areas (rooms) registered in Home Assistant.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAreaLs(cmd.Context(), cmd.OutOrStdout())
	},
}

var flagAreaIcon string
var flagAreaFloor string
var flagAreaConfirm bool

var areaCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new area",
	Long:  "Create an area (room) in the Home Assistant area registry.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAreaCreate(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var areaDeleteCmd = &cobra.Command{
	Use:   "delete <area_id>",
	Short: "Delete an area (dry-run by default)",
	Long:  "Delete an area from the Home Assistant area registry. Use --confirm to apply.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAreaDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	areaCreateCmd.Flags().StringVar(&flagAreaIcon, "icon", "", "area icon (e.g. mdi:sofa)")
	areaCreateCmd.Flags().StringVar(&flagAreaFloor, "floor", "", "floor ID to assign")
	areaCreateCmd.Flags().BoolVar(&flagAreaConfirm, "confirm", false, "actually create (default is dry-run)")
	areaDeleteCmd.Flags().BoolVar(&flagAreaConfirm, "confirm", false, "actually delete (default is dry-run)")
	areaCmd.AddCommand(areaLsCmd, areaCreateCmd, areaDeleteCmd)
	rootCmd.AddCommand(areaCmd)
}

func runAreaLs(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return fmt.Errorf("connecting to HA: %w", connErr)
	}
	defer func() { _ = ws.Close() }()

	areas, err := ws.AreaRegistryList(ctx)
	if err != nil {
		return fmt.Errorf("fetching areas: %w", err)
	}

	if len(areas) == 0 {
		return emitEmptyList(w, "no areas")
	}

	// Resolve floor names
	floors, floorErr := ws.FloorRegistryList(ctx)
	floorMap := make(map[string]string, len(floors))
	if floorErr == nil {
		for _, f := range floors {
			floorMap[f.FloorID] = f.Name
		}
	}

	// Resolve label names
	labels, labelErr := ws.LabelRegistryList(ctx)
	labelMap := make(map[string]string, len(labels))
	if labelErr == nil {
		for _, l := range labels {
			labelMap[l.LabelID] = l.Name
		}
	}

	tbl := &format.Table{
		Headers: []string{"area_id", "name", "floor", "labels"},
		Rows:    make([][]string, len(areas)),
	}
	for i, a := range areas {
		floorName := floorMap[a.FloorID]
		var lblNames []string
		for _, lid := range a.Labels {
			if name, ok := labelMap[lid]; ok {
				lblNames = append(lblNames, name)
			} else {
				lblNames = append(lblNames, lid)
			}
		}
		tbl.Rows[i] = []string{
			a.AreaID,
			a.Name,
			floorName,
			joinStrings(lblNames),
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:      flagTop,
		Full:     flagFull,
		JSON:     flagJSON,
		Compact:  true,
		MoreHint: "use --full or --top N to see more",
	})
}

func joinStrings(s []string) string {
	return strings.Join(s, ", ")
}

func runAreaCreate(ctx context.Context, w io.Writer, name string) error {
	if !flagAreaConfirm {
		return dryRun("create area").
			with("name", name).
			withIf(flagAreaIcon != "", "icon", flagAreaIcon).
			withIf(flagAreaFloor != "", "floor", flagAreaFloor).
			render(w)
	}

	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return fmt.Errorf("connecting to HA: %w", connErr)
	}
	defer func() { _ = ws.Close() }()

	entry, err := ws.AreaRegistryCreate(ctx, name, flagAreaIcon, flagAreaFloor)
	if err != nil {
		return fmt.Errorf("creating area: %w", err)
	}

	_, _ = fmt.Fprintf(w, "created area %q (id=%s)\n", entry.Name, entry.AreaID)
	return nil
}

func runAreaDelete(ctx context.Context, w io.Writer, areaID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return fmt.Errorf("connecting to HA: %w", connErr)
	}
	defer func() { _ = ws.Close() }()

	// Resolve before planning. A preview that accepts an id HA has never heard
	// of describes a delete that cannot happen, and under the manual's
	// stop-at-the-first-miss rule a typo then reads as a verified plan.
	areas, err := ws.AreaRegistryList(ctx)
	if err != nil {
		return fmt.Errorf("fetching areas: %w", err)
	}
	entry, ok := resolveRegistryTarget(areaID, areas, func(a haapi.AreaEntry) (string, string) {
		return a.AreaID, a.Name
	})
	if !ok {
		return fmt.Errorf("area %q not found (use 'area ls' to see available areas)", areaID)
	}

	if !flagAreaConfirm {
		return dryRun("delete area").
			with("area_id", entry.AreaID).
			with("name", entry.Name).
			render(w)
	}

	if err := ws.AreaRegistryDelete(ctx, entry.AreaID); err != nil {
		return fmt.Errorf("deleting area: %w", err)
	}

	_, _ = fmt.Fprintf(w, "deleted area %q\n", entry.AreaID)
	return nil
}
