package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/analyze"
	"github.com/hemm-ems/hactl/internal/cache"
	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/pkg/ids"
)

var (
	flagEntPattern  string
	flagEntDomain   string
	flagEntResample string
	flagEntAttr     string
	flagEntArea     string
	flagEntLabel    string
	flagEntConfirm  bool
	flagEntStale    bool
)

var entCmd = &cobra.Command{
	Use:   "ent",
	Short: "Browse and inspect entities",
	Long:  "List, inspect, and analyze Home Assistant entities and their history.",
}

var entLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List entities",
	Long:  "Show entities table, optionally filtered by glob pattern.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntLs(cmd.Context(), cmd.OutOrStdout())
	},
}

var entShowCmd = &cobra.Command{
	Use:   "show <entity_id>",
	Short: "Show entity profile",
	Long:  "Display entity current state, attributes, and last change.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var entHistCmd = &cobra.Command{
	Use:   "hist <entity_id>",
	Short: "Show entity history",
	Long:  "Display entity time series, auto-resampled to ~50 points by default.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntHist(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var entAnomaliesCmd = &cobra.Command{
	Use:   "anomalies <entity_id>",
	Short: "Detect entity anomalies",
	Long:  "Find gaps, stuck values, and spikes in entity history.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntAnomalies(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var entRelatedCmd = &cobra.Command{
	Use:   "related <entity_id>",
	Short: "Show entities related to the given entity",
	Long:  "Spider automations, device siblings, and area neighbors to find related entities.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntRelated(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var entSetLabelCmd = &cobra.Command{
	Use:   "set-label <entity_id> <label>...",
	Short: "Assign labels to an entity",
	Long:  "Set one or more labels on an entity via the HA entity registry.",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntSetLabel(cmd.Context(), cmd.OutOrStdout(), args[0], args[1:])
	},
}

var entSetAreaCmd = &cobra.Command{
	Use:   "set-area <entity_id> <area>",
	Short: "Assign an area to an entity (dry-run by default)",
	Long:  "Set the area (room) for an entity via the HA entity registry. Use --confirm to apply.",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntSetArea(cmd.Context(), cmd.OutOrStdout(), args[0], args[1])
	},
}

func init() {
	entLsCmd.Flags().StringVar(&flagEntPattern, "pattern", "", "filter by name (substring or glob, e.g. sensor.wp_*)")
	entLsCmd.Flags().StringVar(&flagEntDomain, "domain", "", "filter entities by domain (e.g. sensor, binary_sensor)")
	entLsCmd.Flags().StringVar(&flagEntArea, "area", "", "filter entities by area/room name (substring)")
	entLsCmd.Flags().StringVar(&flagEntLabel, "label", "", "filter entities by label name (substring)")
	entHistCmd.Flags().StringVar(&flagEntResample, "resample", "", "resample bucket duration (e.g. 5m, 1h)")
	entHistCmd.Flags().StringVar(&flagEntAttr, "attr", "", "track a specific attribute instead of state (e.g. brightness)")
	entSetAreaCmd.Flags().BoolVar(&flagEntConfirm, "confirm", false, "actually set area (default is dry-run)")
	entRelatedCmd.Flags().BoolVar(&flagEntStale, "stale", false, "if the entity is gone, list where it is still referenced in config")
	entCmd.AddCommand(entLsCmd, entShowCmd, entHistCmd, entAnomaliesCmd, entRelatedCmd, entSetLabelCmd, entSetAreaCmd)
	rootCmd.AddCommand(entCmd)
}

// entityState holds a generic entity from /api/states.
// Context carries HA's trigger metadata; see haapi.Context.
type entityState struct {
	Attributes  map[string]any `json:"attributes"`
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	LastChanged string         `json:"last_changed"`
	LastUpdated string         `json:"last_updated"`
	Context     haapi.Context  `json:"context"`
}

func runEntLs(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.GetStates(ctx)
	if err != nil {
		return fmt.Errorf("fetching states: %w", err)
	}

	var states []entityState
	if err := json.Unmarshal(data, &states); err != nil {
		return fmt.Errorf("parsing states: %w", err)
	}

	if flagEntDomain != "" {
		filtered := filterEntitiesByDomain(states, flagEntDomain)
		if len(filtered) == 0 {
			_, _ = fmt.Fprintln(w, domainNotFoundHint(flagEntDomain))
			return nil
		}
		states = filtered
	}

	if flagEntPattern != "" {
		states = filterEntitiesByPattern(states, flagEntPattern)
	}

	// Fetch registry context for area/label enrichment
	var rc *registryContext
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if wsErr := ws.Connect(ctx); wsErr == nil {
		rc, _ = fetchRegistryContext(ctx, ws)
		_ = ws.Close()
	} else {
		slog.Warn("could not connect to WS for registry data", "error", wsErr)
	}

	// Apply area/label filters
	if rc != nil && flagEntArea != "" {
		states = filterEntitiesByArea(states, rc, flagEntArea)
	}
	if rc != nil && flagEntLabel != "" {
		if !labelExistsInRegistry(rc, flagEntLabel) {
			_, _ = fmt.Fprintln(w, labelNotFoundHint(flagEntLabel))
			return nil
		}
		states = filterEntitiesByLabel(states, rc, flagEntLabel)
	}

	tbl := &format.Table{
		Headers: []string{"entity_id", "state", "area", "labels", "last_changed"},
		Rows:    make([][]string, len(states)),
	}
	for i, s := range states {
		var areaName, lblNames string
		if rc != nil {
			areaName = rc.areaName(s.EntityID)
			lblNames = rc.labelNames(s.EntityID)
		}
		tbl.Rows[i] = []string{
			s.EntityID,
			truncateState(s.State),
			areaName,
			lblNames,
			formatShortTime(s.LastChanged),
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:      flagTop,
		Full:     flagFull,
		JSON:     flagJSON,
		Compact:  true,
		MoreHint: "try --pattern '<glob>', --domain <d>, --area <a>, --label <l>, or --top N",
	})
}

func runEntShow(ctx context.Context, w io.Writer, entityID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	data, err := client.GetState(ctx, entityID)
	if err != nil {
		return fmt.Errorf("fetching entity state: %w", err)
	}

	var ent entityState
	if err := json.Unmarshal(data, &ent); err != nil {
		return fmt.Errorf("parsing entity state: %w", err)
	}

	// Fetch registry for area/labels, and users for changed_by attribution.
	var rc *registryContext
	var users map[string]haapi.UserEntry
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if wsErr := ws.Connect(ctx); wsErr == nil {
		rc, _ = fetchRegistryContext(ctx, ws)
		users = loadUsers(ctx, ws)
		_ = ws.Close()
	}

	if flagJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(ent)
	}

	_, _ = fmt.Fprintf(w, "entity:       %s\n", ent.EntityID)
	_, _ = fmt.Fprintf(w, "state:        %s\n", ent.State)
	_, _ = fmt.Fprintf(w, "last_changed: %s\n", formatShortTime(ent.LastChanged))
	_, _ = fmt.Fprintf(w, "last_updated: %s\n", formatShortTime(ent.LastUpdated))
	_, _ = fmt.Fprintf(w, "changed_by:   %s\n",
		triggerLabel(logbookEntry{ContextUserID: ent.Context.UserID}, users))

	if friendly, ok := ent.Attributes["friendly_name"]; ok {
		_, _ = fmt.Fprintf(w, "name:         %v\n", friendly)
	}
	if unit, ok := ent.Attributes["unit_of_measurement"]; ok {
		_, _ = fmt.Fprintf(w, "unit:         %v\n", unit)
	}
	if dc, ok := ent.Attributes["device_class"]; ok {
		_, _ = fmt.Fprintf(w, "device_class: %v\n", dc)
	}
	if rc != nil {
		if areaName := rc.areaName(entityID); areaName != "" {
			_, _ = fmt.Fprintf(w, "area:         %s\n", areaName)
		}
		if labelNames := rc.labelNames(entityID); labelNames != "" {
			_, _ = fmt.Fprintf(w, "labels:       %s\n", labelNames)
		}
	}

	if flagFull {
		// Show all remaining attributes
		shown := map[string]bool{
			"friendly_name":       true,
			"unit_of_measurement": true,
			"device_class":        true,
		}
		keys := make([]string, 0, len(ent.Attributes))
		for k := range ent.Attributes {
			if !shown[k] {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := ent.Attributes[k]
			switch val := v.(type) {
			case []any:
				_, _ = fmt.Fprintf(w, "%-13s %s\n", k+":", formatAttrList(val))
			default:
				_, _ = fmt.Fprintf(w, "%-13s %v\n", k+":", v)
			}
		}
	} else {
		// Show hint if there are hidden attributes
		numShown := 0
		for _, k := range []string{"friendly_name", "unit_of_measurement", "device_class"} {
			if _, ok := ent.Attributes[k]; ok {
				numShown++
			}
		}
		total := len(ent.Attributes)
		if total > numShown {
			_, _ = fmt.Fprintf(w, "attributes:   %d total; use --full to see all\n", total)
		}
	}

	return nil
}

func formatAttrList(items []any) string {
	strs := make([]string, len(items))
	for i, item := range items {
		strs[i] = fmt.Sprintf("%v", item)
	}
	return "[" + strings.Join(strs, ", ") + "]"
}

func runEntHist(ctx context.Context, w io.Writer, entityID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	sinceDur, err := parseSince(flagSince)
	if err != nil {
		return err
	}

	now := time.Now()
	startTime := now.Add(-sinceDur)

	client := haapi.New(cfg.URL, cfg.Token)

	// If --attr is set, fetch attribute history instead of state history
	if flagEntAttr != "" {
		return runEntHistAttr(ctx, w, client, entityID, startTime, now)
	}

	points, err := fetchHistoryPoints(ctx, client, entityID, startTime, now)
	if err != nil {
		return err
	}

	if len(points) == 0 {
		// Try state timeline for non-numeric entities (binary sensors, input_booleans, etc.)
		data, histErr := client.GetHistory(ctx, entityID,
			startTime.Format(time.RFC3339),
			now.Format(time.RFC3339))
		if histErr != nil {
			return fmt.Errorf("fetching history: %w", histErr)
		}
		changes, parseErr := parseStateTimeline(data, now)
		if parseErr != nil {
			return parseErr
		}
		if len(changes) == 0 {
			_, _ = fmt.Fprintln(w, "no history data")
			return nil
		}
		return renderStateTimeline(w, entityID, changes)
	}

	// Cache the fetched points
	cachePoints(ctx, cfg.Dir, entityID, points)

	// Resample
	if flagEntResample != "" {
		d, parseErr := time.ParseDuration(flagEntResample)
		if parseErr != nil {
			return fmt.Errorf("invalid resample duration: %w", parseErr)
		}
		points = analyze.ResampleDuration(points, d)
	} else {
		points = analyze.Resample(points, defaultResampleTarget)
	}

	return renderHistoryPoints(w, entityID, points)
}

// runEntHistAttr fetches history and extracts a specific attribute as numeric timeline.
func runEntHistAttr(ctx context.Context, w io.Writer, client *haapi.Client, entityID string, startTime, endTime time.Time) error {
	data, err := client.GetHistory(ctx, entityID,
		startTime.Format(time.RFC3339),
		endTime.Format(time.RFC3339))
	if err != nil {
		return fmt.Errorf("fetching history: %w", err)
	}

	points, err := parseAttrHistoryResponse(data, flagEntAttr)
	if err != nil {
		return err
	}

	if len(points) == 0 {
		_, _ = fmt.Fprintf(w, "no attribute data for %q\n", flagEntAttr)
		return nil
	}

	// Resample
	if flagEntResample != "" {
		d, parseErr := time.ParseDuration(flagEntResample)
		if parseErr != nil {
			return fmt.Errorf("invalid resample duration: %w", parseErr)
		}
		points = analyze.ResampleDuration(points, d)
	} else {
		points = analyze.Resample(points, defaultResampleTarget)
	}

	return renderHistoryPoints(w, entityID+" ["+flagEntAttr+"]", points)
}

func runEntAnomalies(ctx context.Context, w io.Writer, entityID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	sinceDur, err := parseSince(flagSince)
	if err != nil {
		return err
	}

	now := time.Now()
	startTime := now.Add(-sinceDur)

	client := haapi.New(cfg.URL, cfg.Token)
	points, err := fetchHistoryPoints(ctx, client, entityID, startTime, now)
	if err != nil {
		return err
	}

	if len(points) == 0 {
		// Try state-duration anomaly for non-numeric entities
		data, histErr := client.GetHistory(ctx, entityID,
			startTime.Format(time.RFC3339),
			now.Format(time.RFC3339))
		if histErr != nil {
			return fmt.Errorf("fetching history: %w", histErr)
		}
		changes, parseErr := parseStateTimeline(data, now)
		if parseErr != nil {
			return parseErr
		}
		if len(changes) == 0 {
			_, _ = fmt.Fprintln(w, "no history data")
			return nil
		}
		return renderStateAnomalies(w, entityID, cfg.Dir, changes)
	}

	// Setup ID registry
	idsPath := filepath.Join(cfg.Dir, "cache", "ids.json")
	reg := ids.NewRegistry(idsPath)
	if loadErr := reg.Load(); loadErr != nil {
		slog.Warn("could not load ids registry", "error", loadErr)
	}

	anomalies := analyze.DetectAll(points,
		defaultGapThreshold,
		defaultStuckThreshold,
		defaultSpikeZ,
	)

	if len(anomalies) == 0 {
		_, _ = fmt.Fprintf(w, "%s: no anomalies detected\n", entityID)
		return nil
	}

	_, _ = fmt.Fprintf(w, "%s: %d anomalies\n", entityID, len(anomalies))

	tbl := &format.Table{
		Headers: []string{"id", "type", "time", "detail"},
		Rows:    make([][]string, len(anomalies)),
	}
	for i, a := range anomalies {
		anomalyKey := entityID + "|" + string(a.Type) + "|" + a.Start.Format(time.RFC3339)
		shortID := reg.GetOrCreate("anom", anomalyKey)

		tbl.Rows[i] = []string{
			shortID,
			string(a.Type),
			formatShortTime(a.Start.Format(time.RFC3339)),
			a.Detail,
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

// Default constants for entity analysis.
const (
	defaultResampleTarget     = 50
	defaultGapThreshold       = 1 * time.Hour
	defaultStuckThreshold     = 2 * time.Hour
	defaultSpikeZ             = 3.0
	defaultStateStuckDuration = 24 * time.Hour
)

// historyEntry is one state record from the HA history API.
type historyEntry struct {
	EntityID    string `json:"entity_id"`
	State       string `json:"state"`
	LastChanged string `json:"last_changed"`
}

func fetchHistoryPoints(ctx context.Context, client *haapi.Client, entityID string, startTime, endTime time.Time) ([]analyze.DataPoint, error) {
	data, err := client.GetHistory(ctx, entityID,
		startTime.Format(time.RFC3339),
		endTime.Format(time.RFC3339))
	if err != nil {
		return nil, fmt.Errorf("fetching history: %w", err)
	}

	return parseHistoryResponse(data)
}

func parseHistoryResponse(data []byte) ([]analyze.DataPoint, error) {
	var outer [][]historyEntry
	if err := json.Unmarshal(data, &outer); err != nil {
		return nil, fmt.Errorf("parsing history response: %w", err)
	}

	if len(outer) == 0 || len(outer[0]) == 0 {
		return nil, nil
	}

	var points []analyze.DataPoint
	for _, entry := range outer[0] {
		val, parseErr := strconv.ParseFloat(entry.State, 64)
		if parseErr != nil {
			continue // skip non-numeric states
		}
		t, timeErr := time.Parse(time.RFC3339Nano, entry.LastChanged)
		if timeErr != nil {
			t, timeErr = time.Parse(time.RFC3339, entry.LastChanged)
			if timeErr != nil {
				continue
			}
		}
		points = append(points, analyze.DataPoint{
			Time:  t,
			Value: val,
		})
	}

	return points, nil
}

// historyEntryFull is a history entry with full attributes (for --attr parsing).
// Source: HA /api/history/period/ returns attributes in each state object.
type historyEntryFull struct {
	Attributes  map[string]any `json:"attributes"`
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	LastChanged string         `json:"last_changed"`
}

func parseAttrHistoryResponse(data []byte, attr string) ([]analyze.DataPoint, error) {
	var outer [][]historyEntryFull
	if err := json.Unmarshal(data, &outer); err != nil {
		return nil, fmt.Errorf("parsing history response: %w", err)
	}

	if len(outer) == 0 || len(outer[0]) == 0 {
		return nil, nil
	}

	var points []analyze.DataPoint
	for _, entry := range outer[0] {
		raw, ok := entry.Attributes[attr]
		if !ok {
			continue
		}
		val, parseErr := toFloat64(raw)
		if parseErr != nil {
			continue
		}
		t, timeErr := time.Parse(time.RFC3339Nano, entry.LastChanged)
		if timeErr != nil {
			t, timeErr = time.Parse(time.RFC3339, entry.LastChanged)
			if timeErr != nil {
				continue
			}
		}
		points = append(points, analyze.DataPoint{
			Time:  t,
			Value: val,
		})
	}

	return points, nil
}

// toFloat64 converts a JSON value to float64.
func toFloat64(v any) (float64, error) {
	switch val := v.(type) {
	case float64:
		return val, nil
	case json.Number:
		return val.Float64()
	case string:
		return strconv.ParseFloat(val, 64)
	default:
		return 0, fmt.Errorf("cannot convert %T to float64", v)
	}
}

func parseStateTimeline(data []byte, now time.Time) ([]analyze.StateChange, error) {
	var outer [][]historyEntry
	if err := json.Unmarshal(data, &outer); err != nil {
		return nil, fmt.Errorf("parsing history response: %w", err)
	}

	if len(outer) == 0 || len(outer[0]) == 0 {
		return nil, nil
	}

	// Filter out unavailable/unknown states
	var entries []historyEntry
	for _, e := range outer[0] {
		if e.State != "unavailable" && e.State != "unknown" {
			entries = append(entries, e)
		}
	}
	if len(entries) == 0 {
		return nil, nil
	}

	changes := make([]analyze.StateChange, len(entries))
	for i, entry := range entries {
		t, timeErr := time.Parse(time.RFC3339Nano, entry.LastChanged)
		if timeErr != nil {
			t, timeErr = time.Parse(time.RFC3339, entry.LastChanged)
			if timeErr != nil {
				continue
			}
		}

		// Duration = time until next state change (or until now for the last entry)
		var dur time.Duration
		if i+1 < len(entries) {
			next, nextErr := time.Parse(time.RFC3339Nano, entries[i+1].LastChanged)
			if nextErr != nil {
				next, _ = time.Parse(time.RFC3339, entries[i+1].LastChanged)
			}
			dur = next.Sub(t)
		} else {
			dur = now.Sub(t)
		}

		changes[i] = analyze.StateChange{
			Time:     t,
			State:    entry.State,
			Duration: dur,
		}
	}

	return changes, nil
}

func renderStateTimeline(w io.Writer, entityID string, changes []analyze.StateChange) error {
	_, _ = fmt.Fprintf(w, "%s: %d state changes\n", entityID, len(changes))

	tbl := &format.Table{
		Headers: []string{"time", "state", "duration"},
		Rows:    make([][]string, len(changes)),
	}
	for i, c := range changes {
		tbl.Rows[i] = []string{
			formatShortTime(c.Time.Format(time.RFC3339)),
			c.State,
			formatDuration(c.Duration),
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func renderStateAnomalies(w io.Writer, entityID, instanceDir string, changes []analyze.StateChange) error {
	idsPath := filepath.Join(instanceDir, "cache", "ids.json")
	reg := ids.NewRegistry(idsPath)
	if loadErr := reg.Load(); loadErr != nil {
		slog.Warn("could not load ids registry", "error", loadErr)
	}

	var anomalies []analyze.Anomaly
	for _, c := range changes {
		if c.Duration >= defaultStateStuckDuration {
			anomalies = append(anomalies, analyze.Anomaly{
				Type:     analyze.AnomalyStuck,
				Start:    c.Time,
				End:      c.Time.Add(c.Duration),
				Duration: c.Duration,
				Detail:   fmt.Sprintf("stuck %q for %s", c.State, formatDuration(c.Duration)),
			})
		}
	}

	if len(anomalies) == 0 {
		_, _ = fmt.Fprintf(w, "%s: no anomalies detected\n", entityID)
		return nil
	}

	_, _ = fmt.Fprintf(w, "%s: %d anomalies\n", entityID, len(anomalies))

	tbl := &format.Table{
		Headers: []string{"id", "type", "time", "detail"},
		Rows:    make([][]string, len(anomalies)),
	}
	for i, a := range anomalies {
		anomalyKey := entityID + "|" + string(a.Type) + "|" + a.Start.Format(time.RFC3339)
		shortID := reg.GetOrCreate("anom", anomalyKey)
		tbl.Rows[i] = []string{
			shortID,
			string(a.Type),
			formatShortTime(a.Start.Format(time.RFC3339)),
			a.Detail,
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

func formatDuration(d time.Duration) string {
	if d < time.Minute {
		return d.Truncate(time.Second).String()
	}
	if d < time.Hour {
		return d.Truncate(time.Second).String()
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	return fmt.Sprintf("%dh%02dm", h, m)
}

func cachePoints(ctx context.Context, instanceDir, entityID string, points []analyze.DataPoint) {
	ts, err := cache.OpenTS(ctx, instanceDir)
	if err != nil {
		slog.Debug("could not open timeseries cache", "error", err)
		return
	}
	defer func() { _ = ts.Close() }()

	times := make([]time.Time, len(points))
	values := make([]float64, len(points))
	for i, p := range points {
		times[i] = p.Time
		values[i] = p.Value
	}

	if storeErr := ts.StoreSamples(ctx, entityID, times, values); storeErr != nil {
		slog.Debug("could not cache timeseries", "error", storeErr)
	}
}

func renderHistoryPoints(w io.Writer, entityID string, points []analyze.DataPoint) error {
	_, _ = fmt.Fprintf(w, "%s: %d points\n", entityID, len(points))

	tbl := &format.Table{
		Headers: []string{"time", "value"},
		Rows:    make([][]string, len(points)),
	}
	for i, p := range points {
		tbl.Rows[i] = []string{
			formatShortTime(p.Time.Format(time.RFC3339)),
			strconv.FormatFloat(p.Value, 'f', 2, 64),
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func filterEntitiesByPattern(states []entityState, pattern string) []entityState {
	result := make([]entityState, 0, len(states))
	for _, s := range states {
		if matchPattern(s.EntityID, pattern) {
			result = append(result, s)
		}
	}
	return result
}

func filterEntitiesByDomain(states []entityState, domain string) []entityState {
	result := make([]entityState, 0, len(states))
	for _, s := range states {
		if parseEntityDomain(s.EntityID) == domain {
			result = append(result, s)
		}
	}
	return result
}

// matchPattern matches entity IDs against a glob or substring pattern.
// If the pattern contains no glob characters (* or ?), it is treated as a
// substring match. Otherwise it is matched as a glob.
func matchPattern(s, pattern string) bool {
	if pattern == "" {
		return s == ""
	}
	if !strings.ContainsAny(pattern, "*?") {
		return strings.Contains(s, pattern)
	}
	return matchGlob(s, pattern)
}

func matchGlob(s, pattern string) bool {
	for len(pattern) > 0 {
		switch pattern[0] {
		case '*':
			// Skip consecutive stars
			for len(pattern) > 0 && pattern[0] == '*' {
				pattern = pattern[1:]
			}
			if len(pattern) == 0 {
				return true
			}
			for i := range len(s) + 1 {
				if matchGlob(s[i:], pattern) {
					return true
				}
			}
			return false
		case '?':
			if len(s) == 0 {
				return false
			}
			s = s[1:]
			pattern = pattern[1:]
		default:
			if len(s) == 0 || s[0] != pattern[0] {
				return false
			}
			s = s[1:]
			pattern = pattern[1:]
		}
	}
	return len(s) == 0
}

func truncateState(state string) string {
	if len(state) > 20 {
		return state[:17] + "..."
	}
	return state
}

// parseEntityDomain extracts the domain from an entity ID (e.g. "sensor" from "sensor.temperature").
func parseEntityDomain(entityID string) string {
	if domain, _, ok := strings.Cut(entityID, "."); ok {
		return domain
	}
	return entityID
}

func filterEntitiesByArea(states []entityState, rc *registryContext, area string) []entityState {
	areaLower := strings.ToLower(area)
	result := make([]entityState, 0, len(states))
	for _, s := range states {
		name := strings.ToLower(rc.areaName(s.EntityID))
		if name != "" && strings.Contains(name, areaLower) {
			result = append(result, s)
		}
	}
	return result
}

// labelNotFoundHint returns the user-facing message when a --label value isn't in the registry.
func labelNotFoundHint(label string) string {
	return fmt.Sprintf("label %q not found in registry (try: hactl label ls)", label)
}

// domainNotFoundHint returns the user-facing message when --domain matches no
// entities. "helper" is the common trap: helpers live in input_* / counter /
// timer / schedule domains and have their own listing command.
func domainNotFoundHint(domain string) string {
	if domain == "helper" || domain == "helpers" {
		return fmt.Sprintf("%q is not an entity domain — list helpers with: hactl helper ls", domain)
	}
	return fmt.Sprintf("no entities in domain %q — verify the domain exists (e.g. sensor, light, input_boolean) before reporting a negative result", domain)
}

// labelExistsInRegistry returns true if the given label name or ID exists in the registry.
func labelExistsInRegistry(rc *registryContext, label string) bool {
	lower := strings.ToLower(label)
	for _, l := range rc.labelByID {
		if strings.ToLower(l.LabelID) == lower || strings.ToLower(l.Name) == lower {
			return true
		}
	}
	return false
}

func filterEntitiesByLabel(states []entityState, rc *registryContext, label string) []entityState {
	labelLower := strings.ToLower(label)
	result := make([]entityState, 0, len(states))
	for _, s := range states {
		names := strings.ToLower(rc.labelNames(s.EntityID))
		if names != "" && strings.Contains(names, labelLower) {
			result = append(result, s)
		}
	}
	return result
}

func runEntSetLabel(ctx context.Context, w io.Writer, entityID string, labels []string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return fmt.Errorf("connecting to HA: %w", connErr)
	}
	defer func() { _ = ws.Close() }()

	// Validate labels exist
	existingLabels, err := ws.LabelRegistryList(ctx)
	if err != nil {
		return fmt.Errorf("fetching labels: %w", err)
	}
	labelIDs := make(map[string]string, len(existingLabels))
	for _, l := range existingLabels {
		labelIDs[strings.ToLower(l.Name)] = l.LabelID
		labelIDs[l.LabelID] = l.LabelID
	}

	resolved := make([]string, 0, len(labels))
	for _, lbl := range labels {
		id, ok := labelIDs[strings.ToLower(lbl)]
		if !ok {
			return fmt.Errorf("label %q not found (use 'label ls' to see available labels)", lbl)
		}
		resolved = append(resolved, id)
	}

	// Get current entity labels and merge
	entries, err := ws.EntityRegistryList(ctx)
	if err != nil {
		return fmt.Errorf("fetching entity registry: %w", err)
	}
	var currentLabels []string
	for _, e := range entries {
		if e.EntityID == entityID {
			currentLabels = e.Labels
			break
		}
	}

	// Merge: add new labels to existing ones (deduplicate)
	seen := make(map[string]bool, len(currentLabels)+len(resolved))
	merged := make([]string, 0, len(currentLabels)+len(resolved))
	for _, l := range currentLabels {
		if !seen[l] {
			seen[l] = true
			merged = append(merged, l)
		}
	}
	for _, l := range resolved {
		if !seen[l] {
			seen[l] = true
			merged = append(merged, l)
		}
	}

	if err := ws.EntityRegistryUpdate(ctx, entityID, map[string]any{"labels": merged}); err != nil {
		return fmt.Errorf("updating entity labels: %w", err)
	}

	_, _ = fmt.Fprintf(w, "%s: labels set to %v\n", entityID, merged)
	return nil
}

func runEntSetArea(ctx context.Context, w io.Writer, entityID, area string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return fmt.Errorf("connecting to HA: %w", connErr)
	}
	defer func() { _ = ws.Close() }()

	// Resolve area name to area ID
	areas, err := ws.AreaRegistryList(ctx)
	if err != nil {
		return fmt.Errorf("fetching areas: %w", err)
	}
	areaEntry, ok := resolveAreaEntry(areas, area)
	if !ok {
		return fmt.Errorf("area %q not found (use 'area ls' to see available areas)", area)
	}

	entries, err := ws.EntityRegistryList(ctx)
	if err != nil {
		return fmt.Errorf("fetching entity registry: %w", err)
	}
	entityEntry, ok := findEntityRegistryEntry(entries, entityID)
	if !ok {
		return fmt.Errorf("entity %q not found in registry (use 'ent ls' to see available entities)", entityID)
	}

	if !flagEntConfirm {
		_, _ = fmt.Fprintln(w, dryRunEntSetAreaSummary(entityEntry, areaEntry, areas))
		return nil
	}

	if err := ws.EntityRegistryUpdate(ctx, entityID, map[string]any{"area_id": areaEntry.AreaID}); err != nil {
		return fmt.Errorf("updating entity area: %w", err)
	}

	_, _ = fmt.Fprintf(w, "%s: area set to %s\n", entityID, areaEntry.AreaID)
	return nil
}

func resolveAreaEntry(areas []haapi.AreaEntry, area string) (haapi.AreaEntry, bool) {
	areaLower := strings.ToLower(area)
	for _, a := range areas {
		if strings.ToLower(a.Name) == areaLower || strings.ToLower(a.AreaID) == areaLower {
			return a, true
		}
	}
	return haapi.AreaEntry{}, false
}

func findEntityRegistryEntry(entries []haapi.EntityRegistryEntry, entityID string) (haapi.EntityRegistryEntry, bool) {
	for _, e := range entries {
		if e.EntityID == entityID {
			return e, true
		}
	}
	return haapi.EntityRegistryEntry{}, false
}

func dryRunEntSetAreaSummary(entity haapi.EntityRegistryEntry, area haapi.AreaEntry, areas []haapi.AreaEntry) string {
	currentArea := entity.AreaID
	for _, a := range areas {
		if a.AreaID == entity.AreaID {
			currentArea = fmt.Sprintf("%s (%s)", a.Name, a.AreaID)
			break
		}
	}

	s := "dry-run: would set entity area\n"
	s += fmt.Sprintf("  entity_id:    %s\n", entity.EntityID)
	if currentArea != "" {
		s += fmt.Sprintf("  current_area: %s\n", currentArea)
	}
	s += fmt.Sprintf("  new_area:     %s (%s)\n", area.Name, area.AreaID)
	s += "use --confirm to apply"
	return s
}

// relatedEntry holds one edge in the entity relationship graph.
type relatedEntry struct {
	entityID     string
	relationship string
	detail       string
}

func runEntRelated(ctx context.Context, w io.Writer, entityID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)

	// Fetch all states for automation scanning
	statesData, err := client.GetStates(ctx)
	if err != nil {
		return fmt.Errorf("fetching states: %w", err)
	}
	var states []entityState
	if unmarshalErr := json.Unmarshal(statesData, &states); unmarshalErr != nil {
		return fmt.Errorf("parsing states: %w", unmarshalErr)
	}

	// Fetch entity registry for device/area relationships
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if wsErr := ws.Connect(ctx); wsErr != nil {
		return fmt.Errorf("connecting to HA: %w", wsErr)
	}
	defer func() { _ = ws.Close() }()

	rc, err := fetchRegistryContext(ctx, ws)
	if err != nil {
		return fmt.Errorf("fetching registry: %w", err)
	}

	related := make([]relatedEntry, 0, len(states))

	// 1. Ask the companion's generic storage/YAML graph first. This avoids the
	// old per-automation config spider, which is slow on large HA instances.
	// With --stale, the companion also returns where a gone entity is still
	// referenced in config (staleRefs).
	companionRelated, staleRefs := findCompanionRelations(ctx, cfg, ws, entityID, flagEntStale)
	related = append(related, companionRelated...)

	// 2. Find device siblings (same device_id)
	related = append(related, findDeviceSiblings(rc, entityID)...)

	// 3. Find area neighbors (same area, same domain)
	related = append(related, findAreaNeighbors(rc, entityID)...)

	// 4. Find groups that contain this entity
	related = append(related, findGroupMemberships(states, entityID)...)
	related = dedupeAndSortRelated(related)

	// A stale/renamed/deleted entity is absent from both the live registry and
	// the state machine. Without this check an empty result is reported as
	// "no related entities found" — indistinguishable from a live entity that
	// genuinely has no relations — silently hiding that the entity is gone.
	known := entityKnown(rc, states, entityID)

	// --stale: for a gone entity, surface where it is still referenced in config
	// (the companion's literal scan) so the reference can be found and repaired.
	if flagEntStale && !known {
		return renderStaleRefs(w, entityID, staleRefs)
	}

	if len(related) == 0 {
		if known {
			_, _ = fmt.Fprintf(w, "%s: no related entities found\n", entityID)
		} else {
			_, _ = fmt.Fprintf(w, "%s: not in the registry (stale/renamed or deleted); 0 relations found\n", entityID)
		}
		return nil
	}

	if known {
		_, _ = fmt.Fprintf(w, "%s: %d related entities\n", entityID, len(related))
	} else {
		_, _ = fmt.Fprintf(w, "%s: not in the registry (stale/renamed or deleted); %d dangling reference(s) still point here\n", entityID, len(related))
	}

	tbl := &format.Table{
		Headers: []string{"entity_id", "relationship", "detail"},
		Rows:    make([][]string, len(related)),
	}
	for i, r := range related {
		tbl.Rows[i] = []string{
			r.entityID,
			r.relationship,
			r.detail,
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

// entityKnown reports whether entityID currently exists in HA — either in the
// live entity registry or the current state machine. A stale/renamed/deleted
// entity is in neither; checking both (rather than the companion's on-disk
// registry snapshot) avoids false "stale" positives for YAML-only entities that
// have a state but no registry entry. rc may be nil if the registry fetch failed.
func entityKnown(rc *registryContext, states []entityState, entityID string) bool {
	if rc != nil {
		if _, ok := rc.entityByID[entityID]; ok {
			return true
		}
	}
	for i := range states {
		if states[i].EntityID == entityID {
			return true
		}
	}
	return false
}

func findCompanionRelations(ctx context.Context, cfg *config.Config, ws *haapi.WSClient, entityID string, stale bool) ([]relatedEntry, []companion.StaleRef) {
	companionURL, err := companion.Discover(ctx, cfg, ws)
	if err != nil {
		slog.Debug("companion related graph unavailable", "error", err)
		return nil, nil
	}
	cc := companion.New(companionURL, cfg.CompanionToken).WithIngressAuth(ws)
	res, err := cc.RelatedEntity(ctx, entityID, stale)
	if err != nil {
		slog.Debug("companion related graph failed", "error", err)
		return nil, nil
	}
	related := make([]relatedEntry, 0, len(res.Related))
	for _, item := range res.Related {
		if item.EntityID == "" || item.EntityID == entityID {
			continue
		}
		related = append(related, relatedEntry{
			entityID:     item.EntityID,
			relationship: item.Relationship,
			detail:       item.Detail,
		})
	}
	return related, res.StaleRefs
}

// renderStaleRefs reports where a gone entity is still referenced in config.
func renderStaleRefs(w io.Writer, entityID string, refs []companion.StaleRef) error {
	if len(refs) == 0 {
		_, _ = fmt.Fprintf(w, "%s: no stale references found (entity fully cleaned up or config unavailable)\n", entityID)
		return nil
	}
	_, _ = fmt.Fprintf(w, "%s: stale (renamed/deleted); %d config reference(s):\n", entityID, len(refs))
	tbl := &format.Table{
		Headers: []string{"location", "path", "matched_value"},
		Rows:    make([][]string, len(refs)),
	}
	for i, r := range refs {
		tbl.Rows[i] = []string{r.Location, r.Path, r.MatchedValue}
	}
	return tbl.Render(w, format.RenderOpts{
		Top:  flagTop,
		Full: true,
		JSON: flagJSON,
	})
}

func dedupeAndSortRelated(entries []relatedEntry) []relatedEntry {
	if len(entries) < 2 {
		return entries
	}
	seen := make(map[relatedEntry]bool, len(entries))
	out := make([]relatedEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.entityID == "" || seen[entry] {
			continue
		}
		seen[entry] = true
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].entityID != out[j].entityID {
			return out[i].entityID < out[j].entityID
		}
		if out[i].relationship != out[j].relationship {
			return out[i].relationship < out[j].relationship
		}
		return out[i].detail < out[j].detail
	})
	return out
}

// findAutomationRelations scans automation configs for references to the target entity.
func findAutomationRelations(ctx context.Context, client *haapi.Client, states []entityState, targetEntityID string) []relatedEntry {
	var result []relatedEntry
	for _, s := range states {
		if parseEntityDomain(s.EntityID) != "automation" {
			continue
		}
		autoID, ok := s.Attributes["id"]
		if !ok {
			continue
		}
		autoIDStr, ok := autoID.(string)
		if !ok {
			continue
		}

		cfgData, err := client.GetAutomationConfig(ctx, autoIDStr)
		if err != nil {
			continue
		}

		// Simple string search for entity references in config JSON
		cfgStr := string(cfgData)
		if strings.Contains(cfgStr, targetEntityID) {
			rel := "referenced-by"
			if strings.Contains(cfgStr, `"trigger"`) && strings.Contains(cfgStr, targetEntityID) {
				rel = "triggers"
			}
			if strings.Contains(cfgStr, `"action"`) && strings.Contains(cfgStr, targetEntityID) {
				rel = "controls"
			}
			result = append(result, relatedEntry{
				entityID:     s.EntityID,
				relationship: rel,
				detail:       "auto=" + autoIDStr,
			})
		}
	}
	return result
}

func findDeviceSiblings(rc *registryContext, entityID string) []relatedEntry {
	ent, ok := rc.entityByID[entityID]
	if !ok || ent.DeviceID == "" {
		return nil
	}
	var result []relatedEntry
	for _, e := range rc.entityByID {
		if e.EntityID != entityID && e.DeviceID == ent.DeviceID {
			result = append(result, relatedEntry{
				entityID:     e.EntityID,
				relationship: "device-sibling",
				detail:       "device=" + ent.DeviceID,
			})
		}
	}
	return result
}

func findAreaNeighbors(rc *registryContext, entityID string) []relatedEntry {
	ent, ok := rc.entityByID[entityID]
	if !ok || ent.AreaID == "" {
		return nil
	}
	targetDomain := parseEntityDomain(entityID)
	areaName := rc.areaByID[ent.AreaID].Name
	var result []relatedEntry
	for _, e := range rc.entityByID {
		if e.EntityID != entityID && e.AreaID == ent.AreaID && parseEntityDomain(e.EntityID) == targetDomain {
			result = append(result, relatedEntry{
				entityID:     e.EntityID,
				relationship: "area-neighbor",
				detail:       "area=" + areaName,
			})
		}
	}
	return result
}

func findGroupMemberships(states []entityState, entityID string) []relatedEntry {
	var result []relatedEntry
	for _, s := range states {
		if parseEntityDomain(s.EntityID) != "group" {
			continue
		}
		members, ok := s.Attributes["entity_id"].([]any)
		if !ok {
			continue
		}
		for _, m := range members {
			if mStr, ok := m.(string); ok && mStr == entityID {
				result = append(result, relatedEntry{
					entityID:     s.EntityID,
					relationship: "group-member",
					detail:       "group contains this entity",
				})
			}
		}
	}
	return result
}
