package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/cache"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Manage local cache",
	Long:  "View status, refresh, or clear the local trace, log, and entity-history cache.",
}

var cacheStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show cache status",
	Long:  "Display cache age, size, and item counts.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCacheStatus(cmd.Context(), cmd.OutOrStdout())
	},
}

var cacheRefreshCmd = &cobra.Command{
	Use:   "refresh [traces|logs]",
	Short: "Refresh cache data",
	Long:  "Fetch fresh data from HA and update the cache. Optionally specify which category.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		category := ""
		if len(args) > 0 {
			category = args[0]
		}
		return runCacheRefresh(cmd.Context(), cmd.OutOrStdout(), category)
	},
}

var cacheClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear all cached data",
	Long:  "Remove all cached traces, logs, and entity history samples.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runCacheClear(cmd.Context(), cmd.OutOrStdout())
	},
}

func init() {
	cacheCmd.AddCommand(cacheStatusCmd, cacheRefreshCmd, cacheClearCmd)
	rootCmd.AddCommand(cacheCmd)
}

func runCacheStatus(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	store, err := cache.Open(ctx, cfg.Dir)
	if err != nil {
		return fmt.Errorf("opening cache: %w", err)
	}
	defer func() { _ = store.Close() }()

	status, err := store.GetStatus(ctx)
	if err != nil {
		return fmt.Errorf("getting cache status: %w", err)
	}

	// timeseries.db is a separate file (written by `ent hist`) and the only
	// cache that grows during normal use — report it alongside traces/logs.
	if tsErr := withTimeseriesStore(ctx, cfg.Dir, func(ts *cache.TSStore) error {
		tsStatus, statusErr := ts.GetStatus(ctx)
		if statusErr != nil {
			return statusErr
		}
		status.TimeseriesSampleCount = tsStatus.SampleCount
		status.TimeseriesDBSize = tsStatus.DBSize
		return nil
	}); tsErr != nil {
		return fmt.Errorf("getting timeseries cache status: %w", tsErr)
	}

	if flagJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(status)
	}

	_, _ = fmt.Fprintf(w, "traces:      %d entries  %s\n", status.TraceCount, formatByteSize(status.TracesDBSize))
	_, _ = fmt.Fprintf(w, "logs:        %s\n", formatByteSize(status.LogSize))
	_, _ = fmt.Fprintf(w, "timeseries:  %d samples  %s\n", status.TimeseriesSampleCount, formatByteSize(status.TimeseriesDBSize))
	_, _ = fmt.Fprintf(w, "synced:      traces=%s  logs=%s\n",
		formatSyncAge(status.TracesSync),
		formatSyncAge(status.LogsSync))
	_, _ = fmt.Fprintf(w, "dir:         %s\n", store.Dir())
	return nil
}

// withTimeseriesStore opens the timeseries cache that sits next to traces.db in
// the same cache directory, runs fn against it, and closes it again. It has its
// own database handle, so every whole-cache operation has to reach it this way.
func withTimeseriesStore(ctx context.Context, instanceDir string, fn func(*cache.TSStore) error) error {
	ts, err := cache.OpenTS(ctx, instanceDir)
	if err != nil {
		return fmt.Errorf("opening timeseries cache: %w", err)
	}
	defer func() { _ = ts.Close() }()
	return fn(ts)
}

func runCacheRefresh(ctx context.Context, w io.Writer, category string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	store, err := cache.Open(ctx, cfg.Dir)
	if err != nil {
		return fmt.Errorf("opening cache: %w", err)
	}
	defer func() { _ = store.Close() }()

	refreshTraces := category == "" || category == "traces"
	refreshLogs := category == "" || category == "logs"

	if refreshTraces {
		if err := refreshTracesFromHA(ctx, cfg, store); err != nil {
			return fmt.Errorf("refreshing traces: %w", err)
		}
		_, _ = fmt.Fprintln(w, "traces refreshed")
	}

	if refreshLogs {
		if err := refreshLogsFromHA(ctx, cfg, store); err != nil {
			return fmt.Errorf("refreshing logs: %w", err)
		}
		_, _ = fmt.Fprintln(w, "logs refreshed")
	}

	return nil
}

func refreshTracesFromHA(ctx context.Context, cfg *config.Config, store *cache.Store) error {
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		return fmt.Errorf("websocket connect: %w", err)
	}
	defer func() { _ = ws.Close() }()

	var records []cache.TraceRecord

	// Fetch automation traces
	autoTraces, err := ws.TraceList(ctx, "automation")
	if err != nil {
		return fmt.Errorf("fetching automation trace list: %w", err)
	}

	for entityID, traces := range autoTraces {
		for _, tr := range traces {
			rawJSON, getErr := ws.TraceGet(ctx, tr.Domain, tr.ItemID, tr.RunID)
			if getErr != nil {
				slog.Warn("could not fetch trace detail", "entity", entityID, "run_id", tr.RunID, "error", getErr)
				continue
			}
			records = append(records, cache.TraceRecord{
				RunID:     tr.RunID,
				Domain:    tr.Domain,
				ItemID:    tr.ItemID,
				StartTime: tr.Timestamp.Start,
				Execution: tr.Execution,
				ErrorMsg:  tr.Error,
				LastStep:  tr.LastStep,
				Trigger:   tr.Trigger,
				RawJSON:   string(rawJSON),
			})
		}
	}

	// Fetch script traces
	scriptTraces, scriptErr := ws.TraceList(ctx, "script")
	if scriptErr != nil {
		slog.Warn("could not fetch script traces", "error", scriptErr)
	} else {
		for entityID, traces := range scriptTraces {
			for _, tr := range traces {
				rawJSON, getErr := ws.TraceGet(ctx, tr.Domain, tr.ItemID, tr.RunID)
				if getErr != nil {
					slog.Warn("could not fetch script trace detail", "entity", entityID, "run_id", tr.RunID, "error", getErr)
					continue
				}
				records = append(records, cache.TraceRecord{
					RunID:     tr.RunID,
					Domain:    tr.Domain,
					ItemID:    tr.ItemID,
					StartTime: tr.Timestamp.Start,
					Execution: tr.Execution,
					ErrorMsg:  tr.Error,
					LastStep:  tr.LastStep,
					Trigger:   tr.Trigger,
					RawJSON:   string(rawJSON),
				})
			}
		}
	}

	return store.RefreshTraces(ctx, records)
}

func refreshLogsFromHA(ctx context.Context, cfg *config.Config, store *cache.Store) error {
	entries, err := fetchLogEntries(ctx, cfg)
	if err != nil {
		return err
	}
	return store.RefreshLogs(ctx, formatLogAsText(entries))
}

func runCacheClear(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	store, err := cache.Open(ctx, cfg.Dir)
	if err != nil {
		return fmt.Errorf("opening cache: %w", err)
	}
	defer func() { _ = store.Close() }()

	if err := store.Clear(ctx); err != nil {
		return fmt.Errorf("clearing cache: %w", err)
	}

	// Must run before the success line: leaving timeseries.db populated while
	// printing "cache cleared" is how half the cache survived unnoticed.
	if err := withTimeseriesStore(ctx, cfg.Dir, func(ts *cache.TSStore) error {
		return ts.Clear(ctx)
	}); err != nil {
		return fmt.Errorf("clearing timeseries cache: %w", err)
	}

	_, _ = fmt.Fprintln(w, "cache cleared")
	return nil
}

func formatByteSize(b int64) string {
	switch {
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func formatSyncAge(syncTime string) string {
	if syncTime == "" {
		return "never"
	}
	t, err := time.Parse(time.RFC3339, syncTime)
	if err != nil {
		return syncTime
	}
	age := time.Since(t)
	switch {
	case age < time.Minute:
		return "just now"
	case age < time.Hour:
		return fmt.Sprintf("%dm ago", int(age.Minutes()))
	case age < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(age.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(age.Hours()/24))
	}
}
