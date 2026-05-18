package cmd

import (
	"context"
	"fmt"
	"io"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var floorCmd = &cobra.Command{
	Use:   "floor",
	Short: "Manage floors",
	Long:  "List, create, and delete Home Assistant floors.",
}

var floorLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List all floors",
	Long:  "Show all floors registered in Home Assistant.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFloorLs(cmd.Context(), cmd.OutOrStdout())
	},
}

var flagFloorIcon string
var flagFloorLevel int
var flagFloorConfirm bool

var floorCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new floor",
	Long:  "Create a floor in the Home Assistant floor registry.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFloorCreate(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var floorDeleteCmd = &cobra.Command{
	Use:   "delete <floor_id>",
	Short: "Delete a floor (dry-run by default)",
	Long:  "Delete a floor from the Home Assistant floor registry. Use --confirm to apply.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runFloorDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	floorCreateCmd.Flags().StringVar(&flagFloorIcon, "icon", "", "floor icon (e.g. mdi:home-floor-1)")
	floorCreateCmd.Flags().IntVar(&flagFloorLevel, "level", 0, "floor level number")
	floorDeleteCmd.Flags().BoolVar(&flagFloorConfirm, "confirm", false, "actually delete (default is dry-run)")
	floorCmd.AddCommand(floorLsCmd, floorCreateCmd, floorDeleteCmd)
	rootCmd.AddCommand(floorCmd)
}

func runFloorLs(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return fmt.Errorf("connecting to HA: %w", connErr)
	}
	defer func() { _ = ws.Close() }()

	floors, err := ws.FloorRegistryList(ctx)
	if err != nil {
		return fmt.Errorf("fetching floors: %w", err)
	}

	if len(floors) == 0 {
		_, _ = fmt.Fprintln(w, "no floors")
		return nil
	}

	tbl := &format.Table{
		Headers: []string{"floor_id", "name", "level", "icon"},
		Rows:    make([][]string, len(floors)),
	}
	for i, f := range floors {
		levelStr := ""
		if f.Level != nil {
			levelStr = strconv.Itoa(*f.Level)
		}
		tbl.Rows[i] = []string{
			f.FloorID,
			f.Name,
			levelStr,
			f.Icon,
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func runFloorCreate(ctx context.Context, w io.Writer, name string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return fmt.Errorf("connecting to HA: %w", connErr)
	}
	defer func() { _ = ws.Close() }()

	var level *int
	if flagFloorLevel != 0 {
		level = &flagFloorLevel
	}

	entry, err := ws.FloorRegistryCreate(ctx, name, flagFloorIcon, level)
	if err != nil {
		return fmt.Errorf("creating floor: %w", err)
	}

	_, _ = fmt.Fprintf(w, "created floor %q (id=%s)\n", entry.Name, entry.FloorID)
	return nil
}

func runFloorDelete(ctx context.Context, w io.Writer, floorID string) error {
	if !flagFloorConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would delete floor")
		_, _ = fmt.Fprintf(w, "  floor_id: %s\n", floorID)
		_, _ = fmt.Fprintln(w, "use --confirm to apply")
		return nil
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

	if err := ws.FloorRegistryDelete(ctx, floorID); err != nil {
		return fmt.Errorf("deleting floor: %w", err)
	}

	_, _ = fmt.Fprintf(w, "deleted floor %q\n", floorID)
	return nil
}
