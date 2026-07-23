package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/analyze"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/pkg/ids"
)

var (
	flagLogErrors    bool
	flagLogWarnings  bool
	flagLogUnique    bool
	flagLogComponent string
)

var logCmd = &cobra.Command{
	Use:   "log",
	Short: "View Home Assistant logs",
	Long:  "Display HA error log with deduplication and filtering.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLog(cmd.Context(), cmd.OutOrStdout(), cmd.Flags().Changed("since"))
	},
}

var logShowCmd = &cobra.Command{
	Use:   "show <log-id>",
	Short: "Show log entry details",
	Long:  "Display full details for a specific log entry by stable ID.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLogShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	logCmd.Flags().BoolVar(&flagLogErrors, "errors", false, "show only ERROR-level entries")
	logCmd.Flags().BoolVar(&flagLogWarnings, "warnings", false, "show only WARNING-level entries (combine with --errors for both)")
	logCmd.Flags().BoolVar(&flagLogUnique, "unique", false, "deduplicate identical messages")
	logCmd.Flags().StringVar(&flagLogComponent, "component", "", "filter by component name")
	logCmd.AddCommand(logShowCmd)
	rootCmd.AddCommand(logCmd)
}

func runLog(ctx context.Context, w io.Writer, sinceSet bool) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	entries, err := fetchLogEntries(ctx, cfg)
	if err != nil {
		return err
	}

	if entries, err = applyLogSince(entries, sinceSet); err != nil {
		return err
	}

	// --errors and --warnings are additive: either alone narrows to that
	// level; together they surface both (the "what went wrong" signal, since
	// operational warnings like "skipping solve" never reach ERROR).
	var levels []string
	if flagLogErrors {
		levels = append(levels, "ERROR")
	}
	if flagLogWarnings {
		levels = append(levels, "WARNING")
	}
	entries = analyze.FilterByLevels(entries, levels...)
	if flagLogComponent != "" {
		entries = analyze.FilterByComponent(entries, flagLogComponent)
	}

	if flagLogUnique {
		return renderDedupedLogs(w, entries)
	}

	return renderLogEntries(w, cfg, entries)
}

func renderDedupedLogs(w io.Writer, entries []analyze.LogEntry) error {
	deduped := analyze.DeduplicateLogs(entries)

	tbl := &format.Table{
		Headers: []string{"count", "level", "component", "first_seen", "last_seen", "message"},
		Rows:    make([][]string, len(deduped)),
	}
	for i, d := range deduped {
		msg := d.Message
		if len(msg) > 60 {
			msg = msg[:57] + "..."
		}
		tbl.Rows[i] = []string{
			strconv.Itoa(d.Count),
			d.Level,
			shortComponent(d.Component),
			analyze.FormatShortTimestamp(d.FirstSeen),
			analyze.FormatShortTimestamp(d.LastSeen),
			msg,
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func renderLogEntries(w io.Writer, cfg *config.Config, entries []analyze.LogEntry) error {
	idsPath := filepath.Join(cfg.Dir, "cache", "ids.json")
	reg := ids.NewRegistry(idsPath)
	if loadErr := reg.Load(); loadErr != nil {
		slog.Warn("could not load ids registry", "error", loadErr)
	}

	tbl := &format.Table{
		Headers: []string{"id", "time", "level", "component", "message"},
		Rows:    make([][]string, len(entries)),
	}
	for i, e := range entries {
		logKey := e.Timestamp + "|" + e.Component + "|" + e.Message
		shortID := reg.GetOrCreate("log", logKey)

		msg := e.Message
		if len(msg) > 60 {
			msg = msg[:57] + "..."
		}
		tbl.Rows[i] = []string{
			shortID,
			analyze.FormatShortTimestamp(e.Timestamp),
			e.Level,
			shortComponent(e.Component),
			msg,
		}
	}

	if saveErr := reg.Save(); saveErr != nil {
		slog.Warn("could not save ids registry", "error", saveErr)
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func runLogShow(_ context.Context, w io.Writer, logID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	// pkg/ids.Registry stores every prefix's short IDs in one flat reverse
	// map (Resolve doesn't check which prefix minted an entry), so without
	// this check a "trc:" or "anom:" ID resolves cleanly here too. Those
	// namespaces' keys happen to also be pipe-delimited 3-part strings (e.g.
	// anom: is "entity_id|type|start_time"), so the branch below would print
	// them as if they were a log entry's timestamp/component/message —
	// fabricated fields lifted from an unrelated record. Mirrors the same
	// prefix check trace.go's resolveTraceID does for "trc:".
	if !strings.HasPrefix(logID, "log:") {
		return fmt.Errorf("invalid log ID: %s (expected log:<hash>)", logID)
	}

	idsPath := filepath.Join(cfg.Dir, "cache", "ids.json")
	reg := ids.NewRegistry(idsPath)
	if loadErr := reg.Load(); loadErr != nil {
		return fmt.Errorf("loading ids registry: %w", loadErr)
	}

	key, ok := reg.Resolve(logID)
	if !ok {
		return fmt.Errorf("unknown log ID: %s", logID)
	}

	// key format: "timestamp|component|message"
	parts := strings.SplitN(key, "|", 3)

	if flagJSON {
		out := map[string]any{"id": logID}
		if len(parts) == 3 {
			out["timestamp"] = parts[0]
			out["component"] = parts[1]
			out["message"] = parts[2]
		} else {
			out["entry"] = key
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	_, _ = fmt.Fprintf(w, "id:        %s\n", logID)
	if len(parts) == 3 {
		_, _ = fmt.Fprintf(w, "timestamp: %s\n", parts[0])
		_, _ = fmt.Fprintf(w, "component: %s\n", parts[1])
		_, _ = fmt.Fprintf(w, "message:   %s\n", parts[2])
	} else {
		_, _ = fmt.Fprintf(w, "entry:     %s\n", key)
	}
	return nil
}

// fetchLogEntries tries WS system_log/list first, then falls back to REST /api/error_log.
func fetchLogEntries(ctx context.Context, cfg *config.Config) ([]analyze.LogEntry, error) {
	// Try WS system_log/list (available when system_log integration is loaded)
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if wsErr := ws.Connect(ctx); wsErr == nil {
		entries, err := ws.SystemLogList(ctx)
		_ = ws.Close()
		if err == nil {
			slog.Debug("fetched logs via system_log/list", "count", len(entries))
			return systemLogToEntries(entries), nil
		}
		slog.Debug("system_log/list unavailable, trying REST", "error", err)
	}

	// Fall back to REST /api/error_log
	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.GetErrorLog(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching error log: %w", err)
	}
	return analyze.ParseLogLines(string(data)), nil
}

// systemLogToEntries converts WS system_log entries to analyze.LogEntry format.
//
// Component keeps the FULL logger name (e.g.
// "homeassistant.components.automation.oracle_missing_service"), not just its
// last dot-segment. analyze.FilterByComponent (--component, `cc logs <name>`)
// substring-matches this same field, so truncating it here made every filter
// value other than the logger's own last segment match nothing — e.g.
// `--component automation` returned zero rows even when automation errors
// existed, because "homeassistant.components.automation.x" had already been
// cut down to "x" before the filter ever ran. shortComponent() shortens it
// again, but only at render time, for the printed table column.
func systemLogToEntries(entries []haapi.SystemLogEntry) []analyze.LogEntry {
	result := make([]analyze.LogEntry, 0, len(entries))
	for _, e := range entries {
		sec := int64(e.Timestamp)
		nsec := int64((e.Timestamp - float64(sec)) * 1e9)
		ts := time.Unix(sec, nsec)
		msg := strings.Join(e.Message, "\n")
		if e.Exception != "" {
			msg += "\n" + e.Exception
		}

		// HA's system_log/list pre-aggregates identical messages into one
		// record with a count; carry it through rather than treating every
		// record as a single occurrence (defect #2). Default to 1 if HA ever
		// omits/zeroes it.
		count := e.Count
		if count <= 0 {
			count = 1
		}

		result = append(result, analyze.LogEntry{
			Timestamp: ts.Format("2006-01-02 15:04:05.000"),
			Level:     strings.ToUpper(e.Level),
			Component: e.Name,
			Message:   msg,
			Count:     count,
		})
	}
	return result
}

// shortComponent extracts the trailing dot-segment of a full logger name for
// display (e.g. "homeassistant.components.zha" -> "zha"). This is display-only
// — matching (--component, `cc logs <name>`) always operates on the full
// logger name held in analyze.LogEntry.Component/DedupedLog.Component.
func shortComponent(full string) string {
	if idx := strings.LastIndex(full, "."); idx >= 0 {
		return full[idx+1:]
	}
	return full
}

// formatLogAsText formats log entries as HA error_log compatible text for caching.
func formatLogAsText(entries []analyze.LogEntry) string {
	var sb strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&sb, "%s %s (MainThread) [%s] %s\n", e.Timestamp, e.Level, e.Component, e.Message)
	}
	return sb.String()
}

// applyLogSince narrows log entries to the --since window, but only when the
// caller actually passed the flag.
//
// HA's system log is a fixed-size in-memory buffer: there is no server-side
// time window to ask for, which is why --since was accepted and then ignored
// entirely — a flag that silently does nothing is indistinguishable, to the
// caller, from one that found nothing. Every entry does carry a timestamp, so
// the window is answerable here.
//
// Honouring the 24h default would be the wrong fix: the buffer routinely holds
// entries older than that, and hiding them by default would make `log --errors`
// go quiet on exactly the long-running instance whose errors matter most. So
// the default stays "the whole buffer", and an explicit --since means what it
// says.
func applyLogSince(entries []analyze.LogEntry, sinceSet bool) ([]analyze.LogEntry, error) {
	if !sinceSet {
		return entries, nil
	}
	d, err := parseSince(flagSince)
	if err != nil {
		return nil, err
	}
	return analyze.FilterSince(entries, time.Now().Add(-d)), nil
}
