package cmd

import (
	"context"
	"fmt"
	"io"
	"math"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/companion"
)

var (
	flagLogsComponent string
	flagLogsLevel     string
)

var companionLogsCmd = &cobra.Command{
	Use:   "logs",
	Short: "Show recent companion add-on logs (not in `hactl log`)",
	Long: "Fetch the companion add-on's own recent log records over the Ingress lifeline.\n\n" +
		"Add-on logs never reach Home Assistant's core logger, so `hactl log` cannot\n" +
		"show them. Use --component wireguard to focus on the WireGuard tunnel and its\n" +
		"dyndns re-resolution monitor. --since and --top apply as the time window and\n" +
		"max line count.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCompanionLogs(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	companionLogsCmd.Flags().StringVar(&flagLogsComponent, "component", "", "filter by component (e.g. wireguard) or logger-name substring")
	companionLogsCmd.Flags().StringVar(&flagLogsLevel, "level", "", "minimum level (debug, info, warning, error)")
	companionCmd.AddCommand(companionLogsCmd)
}

func runCompanionLogs(ctx context.Context, w io.Writer) error {
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	res, err := cc.Logs(ctx, companion.LogsParams{
		Component: flagLogsComponent,
		Level:     flagLogsLevel,
		Since:     flagSince,
		Limit:     flagTop,
	})
	if err != nil {
		return err
	}
	if flagJSON {
		return writeJSON(w, res)
	}
	writeCompanionLogs(w, res, flagLogsComponent != "")
	return nil
}

// writeCompanionLogs prints one line per entry: "HH:MM:SS LEVEL [name] message".
// The component name is omitted when the caller already filtered by component.
func writeCompanionLogs(w io.Writer, res *companion.LogsResponse, componentFiltered bool) {
	if len(res.Entries) == 0 {
		_, _ = fmt.Fprintln(w, "(no log entries)")
		return
	}
	for _, e := range res.Entries {
		ts := time.Unix(int64(e.Ts), int64(math.Mod(e.Ts, 1)*1e9)).Format("15:04:05")
		name := strings.TrimPrefix(e.Name, "companion.")
		if componentFiltered {
			_, _ = fmt.Fprintf(w, "%s %-5s %s\n", ts, e.Level, e.Message)
		} else {
			_, _ = fmt.Fprintf(w, "%s %-5s %s: %s\n", ts, e.Level, name, e.Message)
		}
	}
}
