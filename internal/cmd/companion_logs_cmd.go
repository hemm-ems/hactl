package cmd

import (
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
	Args:  takesNone(),
	Short: "Show recent companion add-on logs (not in `hactl log`)",
	Long: "Fetch the companion add-on's own recent log records over the Ingress lifeline.\n\n" +
		"Add-on logs never reach Home Assistant's core logger, so `hactl log` cannot\n" +
		"show them. Use --component wireguard to focus on the WireGuard tunnel and its\n" +
		"dyndns re-resolution monitor. --since applies as the time window; --top caps\n" +
		"the printed line count, and never the --json payload.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCompanionLogs(cmd, cmd.OutOrStdout())
	},
}

func init() {
	companionLogsCmd.Flags().StringVar(&flagLogsComponent, "component", "", "filter by component (e.g. wireguard) or logger-name substring")
	companionLogsCmd.Flags().StringVar(&flagLogsLevel, "level", "",
		"show only records at or above this level (debug, info, warning, error)")
	companionCmd.AddCommand(companionLogsCmd)
}

func runCompanionLogs(cmd *cobra.Command, w io.Writer) error {
	ctx := cmd.Context()
	cc, err := connectCompanion(ctx)
	if err != nil {
		return err
	}
	// --top caps rows in TEXT output only (H-10). This is the one read command
	// whose --top reaches its *source*: the companion applies `limit` server
	// side, so forwarding it under --json returned a silently short answer that
	// parsed perfectly — `--json --top 1` reported one of the buffer's records
	// as if it were all of them. `hactl log` and `cc logs`, which cap only
	// their tables, never had it; this is that fix's missing third site.
	// Asserted by TestJSONContract (clause 2 counts elements at every depth,
	// so the {"entries": [...]} wrapper no longer hides the truncation).
	limit := flagTop
	if flagJSON {
		limit = 0 // no source-side cap: --json is the complete window
	}
	res, err := cc.Logs(ctx, companion.LogsParams{
		Component: flagLogsComponent,
		Level:     flagLogsLevel,
		Since:     sinceWindow(),
		Limit:     limit,
	})
	if err != nil {
		return err
	}
	if flagJSON {
		return writeJSON(w, res)
	}
	if len(res.Entries) == 0 {
		// unknownTotal: this listing is narrowed by the companion, server side,
		// so the buffer's unfiltered size never crosses the wire. Naming the
		// narrowings is the half of the answer hactl does hold, and it is the
		// half "(no log entries)" was silently standing in for.
		return emptyListing(cmd, w, "log records", unknownTotal)
	}
	writeCompanionLogs(w, res, flagLogsComponent != "")
	return nil
}

// writeCompanionLogs prints one line per entry: "HH:MM:SS LEVEL [name] message".
// The component name is omitted when the caller already filtered by component.
func writeCompanionLogs(w io.Writer, res *companion.LogsResponse, componentFiltered bool) {
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
