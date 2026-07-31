package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/analyze"
	"github.com/hemm-ems/hactl/internal/cache"
	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
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
	flagEntRestored bool
)

var entCmd = family(&cobra.Command{
	Use:        "ent",
	SuggestFor: []string{"entity", "entities", "states", "sensor", "sensors"},
	Short:      "Browse and inspect entities",
	Long:       "List, inspect, and analyze Home Assistant entities and their history.",
})

var entLsCmd = &cobra.Command{
	Use:   "ls",
	Args:  takesNone(),
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
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var entHistCmd = &cobra.Command{
	Use:   "hist <entity_id>",
	Short: "Show entity history",
	Long:  "Display entity time series, auto-resampled to ~50 points by default.",
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntHist(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var entAnomaliesCmd = &cobra.Command{
	Use:   "anomalies <entity_id>",
	Short: "Detect entity anomalies",
	Long:  "Find gaps, stuck values, and spikes in entity history.",
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntAnomalies(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var entRelatedCmd = &cobra.Command{
	Use:   "related <entity_id>",
	Short: "Show entities related to the given entity",
	Long:  "Spider automations, device siblings, and area neighbors to find related entities.",
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntRelated(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var entSetLabelCmd = &cobra.Command{
	Use:   "set-label <entity_id> <label>...",
	Short: "Assign labels to an entity (dry-run by default)",
	Long:  "Set one or more labels on an entity via the HA entity registry. Dry-run by default: previews the merged label set; use --confirm to apply.",
	Args:  takesAtLeast(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntSetLabel(cmd.Context(), cmd.OutOrStdout(), args[0], args[1:])
	},
}

var entSetAreaCmd = &cobra.Command{
	Use:   "set-area <entity_id> <area>",
	Short: "Assign an area to an entity (dry-run by default)",
	Long:  "Set the area (room) for an entity via the HA entity registry. Use --confirm to apply.",
	Args:  takes(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntSetArea(cmd.Context(), cmd.OutOrStdout(), args[0], args[1])
	},
}

func init() {
	entLsCmd.Flags().StringVar(&flagEntPattern, "pattern", "", "filter by entity_id — for automations also config id or alias (substring or glob, e.g. sensor.wp_*)")
	entLsCmd.Flags().StringVar(&flagEntDomain, "domain", "", "filter entities by domain (e.g. sensor, binary_sensor)")
	entLsCmd.Flags().StringVar(&flagEntArea, "area", "", "filter entities by area/room name (substring)")
	entLsCmd.Flags().StringVar(&flagEntLabel, "label", "", "filter entities by label name (substring)")
	entLsCmd.Flags().BoolVar(&flagEntRestored, "restored", false, "show only restored 'ghost' entities (state resurrected from the registry with no live entity — deleted or re-authored)")
	entHistCmd.Flags().StringVar(&flagEntResample, "resample", "", "resample bucket duration (e.g. 5m, 1h)")
	entHistCmd.Flags().StringVar(&flagEntAttr, "attr", "", "track a specific attribute instead of state (e.g. brightness)")
	entSetLabelCmd.Flags().BoolVar(&flagEntConfirm, "confirm", false, "actually set labels (default is dry-run)")
	entSetAreaCmd.Flags().BoolVar(&flagEntConfirm, "confirm", false, "actually set area (default is dry-run)")
	entRenameCmd.Flags().BoolVar(&flagEntConfirm, "confirm", false, "actually rename and rewrite references (default is dry-run)")
	entRenameCmd.Flags().BoolVar(&flagEntRenameAllowPartial, "allow-partial", false,
		"rename even when some dashboards cannot be scanned or written "+
			"(references in those dashboards are left unchanged; same posture as ref replace)")
	entRelatedCmd.Flags().BoolVar(&flagEntStale, "stale", false, "if the entity is gone, list where it is still referenced in config")
	entCmd.AddCommand(entLsCmd, entShowCmd, entHistCmd, entAnomaliesCmd, entRelatedCmd, entSetLabelCmd, entSetAreaCmd, entRenameCmd)
	rootCmd.AddCommand(entCmd)
}

// wireAttributes is an entity's attribute map decoded WITHOUT imposing a Go
// numeric type on it.
//
// H-21 established that a domain-specific schema may only be applied to the
// entities a command renders, and #105 fixed the decode half. This is the
// encode half, and it was left standing: `encoding/json` decodes every JSON
// number into `float64` for a `map[string]any`, and marshals `float64(5000)`
// back as `5000`. So `ent show --json` re-emitted HA's `"max": 5000.0` — a
// float by construction on every `number.*` entity, and on climate's
// temperature/min_temp/max_temp — as a bare JSON integer. Python's
// `json.loads` types that as `int`, and any consumer checking against HA's own
// attribute contracts silently disagrees with HA about the entity it is
// looking at. Non-integral floats were unaffected, which is why it survived:
// `12.7` round-trips, `45.0` does not.
//
// `json.Number` keeps the literal HA sent, so the value re-encodes byte for
// byte. The property belongs to the TYPE rather than to `ent show`'s renderer
// on purpose: every decode of an entity state gets it, including the ones
// inside a slice and the ones written next year, and there is no second place
// that has to remember. `toFloat64` already accepted `json.Number` before this
// existed, and nothing in the product asserts `.(float64)` on an attribute.
type wireAttributes map[string]any

// UnmarshalJSON decodes the attribute map with numbers left as their wire
// literal.
func (a *wireAttributes) UnmarshalJSON(data []byte) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var m map[string]any
	if err := dec.Decode(&m); err != nil {
		return err
	}
	*a = m
	return nil
}

// entityState holds a generic entity from /api/states.
// Context carries HA's trigger metadata; see haapi.Context.
type entityState struct {
	Attributes  wireAttributes `json:"attributes"`
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
	if err := degeneracy.Check("/api/states", &states); err != nil {
		return err
	}

	if flagEntDomain != "" {
		filtered := filterEntitiesByDomain(states, flagEntDomain)
		if len(filtered) == 0 {
			return emitEmptyList(w, domainNotFoundHint(flagEntDomain))
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
			return emitEmptyList(w, labelNotFoundHint(flagEntLabel))
		}
		states = filterEntitiesByLabel(states, rc, flagEntLabel)
	}

	if flagEntRestored {
		states = filterEntitiesByRestored(states)
	}

	// #54: HA marks a state `restored: true` when it was resurrected from the
	// registry/recorder with no live platform entity behind its unique_id — a
	// "ghost" left by a deleted or re-authored automation/helper/script. Surface
	// it as a column, but only when at least one entity is actually restored, so
	// the common all-live listing keeps its narrower shape.
	anyRestored := false
	for i := range states {
		if isRestoredAttr(states[i].Attributes) {
			anyRestored = true
			break
		}
	}

	headers := []string{"entity_id", "state", "area", "labels", "last_changed"}
	if anyRestored {
		headers = append(headers, "restored")
	}
	tbl := &format.Table{
		Headers: headers,
		Rows:    make([][]string, len(states)),
	}
	// A state wider than the column is shortened for the reader only. It used
	// to be shortened here, so `ent ls --json` answered
	// `"state": "2026-07-31T03:13:..."` for 76 of the reference instance's 4486
	// entities while `ent show --json` answered `"2026-08-01T03:33:44+00:00"`
	// for the same field of the same entity (H-10, the class behind finding
	// #14).
	tbl.SetWidth("state", entStateWidth)
	for i, s := range states {
		var areaName, lblNames string
		if rc != nil {
			areaName = rc.areaName(s.EntityID)
			lblNames = rc.labelNames(s.EntityID)
		}
		row := []string{
			s.EntityID,
			s.State,
			areaName,
			lblNames,
			formatShortTime(s.LastChanged),
		}
		if anyRestored {
			row = append(row, boolCell(isRestoredAttr(s.Attributes)))
		}
		tbl.Rows[i] = row
		// The cell above is the reader's short clock; a machine gets the
		// instant HA sent, with its offset (H-10).
		tbl.SetMachine(i, "last_changed", formatMachineTime(s.LastChanged))
	}

	return tbl.Render(w, format.RenderOpts{
		Top:      flagTop,
		Full:     flagFull,
		JSON:     flagJSON,
		Compact:  true,
		MoreHint: "try --pattern '<glob>', --domain <d>, --area <a>, --label <l>, --restored, or --top N",
	})
}

// writeEntShowJSON emits `ent show --json`.
//
// INVARIANT H-10: the JSON carries the same information the human table
// computes — name, unit, area, labels, changed_by — not merely the raw state
// struct. A --json consumer that had to re-derive area or attribution from
// `attributes` would be reimplementing device-area inheritance (H-8) itself.
// D-4: changed_by comes from the shared actor resolution, and the two fields
// beside it name its source — `changed_by_source` ("logbook" | "state
// context") and `logbook_excluded` (true when HA's logbook excludes this
// entity, which is why the state-context fallback answered).
func writeEntShowJSON(
	w io.Writer,
	entityID string,
	ent entityState,
	rc *registryContext,
	ans actorAnswer,
) error {
	result := map[string]any{
		"entity_id":         ent.EntityID,
		"state":             ent.State,
		"attributes":        ent.Attributes,
		"last_changed":      ent.LastChanged,
		"last_updated":      ent.LastUpdated,
		"context":           ent.Context,
		"changed_by":        ans.ChangedBy,
		"changed_by_source": ans.Source,
		"logbook_excluded":  ans.LogbookExcluded,
	}
	if friendly, ok := ent.Attributes["friendly_name"]; ok {
		result["name"] = friendly
	}
	if unit, ok := ent.Attributes["unit_of_measurement"]; ok {
		result["unit"] = unit
	}
	if rc != nil {
		if areaName := rc.areaName(entityID); areaName != "" {
			result["area"] = areaName
		}
		if labelNames := rc.labelNames(entityID); labelNames != "" {
			result["labels"] = labelNames
		}
		// Ownership: which integration/config entry this entity belongs to —
		// the join a caller otherwise has to leave hactl for (issue #110).
		// Omit-when-empty like area/labels: a YAML-configured platform has no
		// config entry, a state-only entity has no registry entry at all.
		if entry, ok := rc.entityByID[entityID]; ok {
			if entry.Platform != "" {
				result["platform"] = entry.Platform
			}
			if entry.UniqueID != "" {
				result["unique_id"] = entry.UniqueID
			}
			if entry.ConfigEntryID != "" {
				result["config_entry_id"] = entry.ConfigEntryID
			}
		}
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// fetchEntityState reads one entity from /api/states and refuses a payload that
// decoded without an entity_id or a state (H-14) — HA answers "unknown" or
// "unavailable" for an entity that has no value, never an empty string, so a
// blank one means the payload did not decode rather than that the entity is
// empty.
func fetchEntityState(ctx context.Context, client *haapi.Client, entityID string) (entityState, error) {
	var ent entityState
	data, err := client.GetState(ctx, entityID)
	if err != nil {
		return ent, fmt.Errorf("fetching entity state: %w", err)
	}
	if err := json.Unmarshal(data, &ent); err != nil {
		return ent, fmt.Errorf("parsing entity state: %w", err)
	}
	if err := degeneracy.Check("/api/states/"+entityID, &ent); err != nil {
		return ent, err
	}
	return ent, nil
}

// changedByLogbookEntries fetches the logbook window `ent show`'s changed_by
// resolution asks about: anchored just before the state's last_changed, so it
// always contains the most recent change's entry whenever the logbook records
// the entity at all. Every failure degrades to nil — the state-context
// fallback — rather than failing the command: `ent show` must keep working on
// an instance whose recorder or logbook is disabled, and its answer then
// honestly names "state context" as its source.
func changedByLogbookEntries(ctx context.Context, client *haapi.Client, ent entityState) []logbookEntry {
	lastChanged, err := time.Parse(time.RFC3339, ent.LastChanged)
	if err != nil {
		return nil
	}
	entries, err := fetchLogbookEntries(ctx, client,
		lastChanged.Add(-time.Minute), time.Now(), ent.EntityID)
	if err != nil {
		slog.Debug("logbook unavailable for changed_by; falling back to state context", "error", err)
		return nil
	}
	return entries
}

// writeEntShowRegistryLines prints the registry-derived lines of the text
// view — area, labels, and ownership (platform / unique_id / config_entry_id).
// H-10 both directions: the same information writeEntShowJSON carries. Every
// line is omit-when-empty; a state-only entity has no registry entry at all.
func writeEntShowRegistryLines(w io.Writer, rc *registryContext, entityID string) {
	if rc == nil {
		return
	}
	if areaName := rc.areaName(entityID); areaName != "" {
		_, _ = fmt.Fprintf(w, "area:         %s\n", areaName)
	}
	if labelNames := rc.labelNames(entityID); labelNames != "" {
		_, _ = fmt.Fprintf(w, "labels:       %s\n", labelNames)
	}
	entry, ok := rc.entityByID[entityID]
	if !ok {
		return
	}
	if entry.Platform != "" {
		_, _ = fmt.Fprintf(w, "platform:     %s\n", entry.Platform)
	}
	if entry.UniqueID != "" {
		_, _ = fmt.Fprintf(w, "unique_id:    %s\n", entry.UniqueID)
	}
	if entry.ConfigEntryID != "" {
		_, _ = fmt.Fprintf(w, "config_entry_id: %s\n", entry.ConfigEntryID)
	}
}

func runEntShow(ctx context.Context, w io.Writer, entityID string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	client := haapi.New(cfg.URL, cfg.Token)
	ent, err := fetchEntityState(ctx, client, entityID)
	if err != nil {
		return err
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

	// D-4: changed_by comes from the shared actor resolution — the logbook's
	// answer about the change this line describes when the logbook has one,
	// the state's own context otherwise, either way naming its source.
	ans := resolveActor(changedByLogbookEntries(ctx, client, ent), ent, users)

	if flagJSON {
		return writeEntShowJSON(w, entityID, ent, rc, ans)
	}

	_, _ = fmt.Fprintf(w, "entity:       %s\n", ent.EntityID)
	_, _ = fmt.Fprintf(w, "state:        %s\n", ent.State)
	if isRestoredAttr(ent.Attributes) {
		// #54: HA restored this from the registry with no live entity behind it —
		// a ghost from a deleted/re-authored config, not a repairable reference.
		_, _ = fmt.Fprintf(w, "restored:     true (ghost: restored from registry; no live entity — deleted or re-authored, nothing to repair)\n")
	}
	_, _ = fmt.Fprintf(w, "last_changed: %s\n", formatShortTime(ent.LastChanged))
	_, _ = fmt.Fprintf(w, "last_updated: %s\n", formatShortTime(ent.LastUpdated))
	_, _ = fmt.Fprintf(w, "changed_by:   %s\n", ans.Label())

	if friendly, ok := ent.Attributes["friendly_name"]; ok {
		_, _ = fmt.Fprintf(w, "name:         %v\n", friendly)
	}
	if unit, ok := ent.Attributes["unit_of_measurement"]; ok {
		_, _ = fmt.Fprintf(w, "unit:         %v\n", unit)
	}
	if dc, ok := ent.Attributes["device_class"]; ok {
		_, _ = fmt.Fprintf(w, "device_class: %v\n", dc)
	}
	writeEntShowRegistryLines(w, rc, entityID)

	if flagFull {
		// Show all remaining attributes. "restored" is already surfaced above.
		shown := map[string]bool{
			"friendly_name":       true,
			"unit_of_measurement": true,
			"device_class":        true,
			"restored":            true,
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
		for _, k := range []string{"friendly_name", "unit_of_measurement", "device_class", "restored"} {
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

// errUnknownEntity distinguishes a typo from a genuinely quiet entity.
//
// `ent hist`, `ent who` and `ent anomalies` all reach an empty result the same
// way whether the entity does not exist or simply did nothing, and all three
// used to report that as an empty answer at exit 0 — while `ent show`, in the
// same family, correctly 404s. Under the manual's "stop at the first miss"
// rule an agent reads the empty answer as a verified negative, so a mistyped
// entity_id becomes a confident "nothing happened".
//
// Call this only once the result is already empty. An entity that has been
// deleted can still have recorder history, and reporting that history is the
// whole point of asking — so history rows, when there are any, settle the
// question before we ever get here. What remains is: no live state and nothing
// recorded in the window, which is indistinguishable from a typo and is
// reported as one.
func errUnknownEntity(ctx context.Context, client *haapi.Client, entityID string) error {
	if _, err := client.GetState(ctx, entityID); err == nil {
		return nil // the entity is real; an empty answer about it is a fact
	}
	return fmt.Errorf("entity %q: no live state, and no recorded history in the last %s "+
		"(check the entity_id with `ent ls --pattern '*%s*'`, or widen --since)",
		entityID, flagSince, lastIDSegment(entityID))
}

// parseResampleDuration parses --resample and rejects values the resampler
// cannot honour. analyze.ResampleDuration returns its input untouched for a
// zero or negative bucket, so `--resample 0m` and `--resample -5m` used to
// produce ordinary default-resampled output with no indication the flag had
// been ignored — the caller cannot tell an honoured value from a discarded one.
func parseResampleDuration(s string) (time.Duration, error) {
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("invalid resample duration %q: %w", s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("invalid resample duration %q: a bucket must be positive (e.g. 5m, 1h)", s)
	}
	return d, nil
}

// lastIDSegment returns the object_id half of an entity_id, for use in a
// suggested search pattern.
func lastIDSegment(entityID string) string {
	if i := strings.Index(entityID, "."); i >= 0 && i+1 < len(entityID) {
		return entityID[i+1:]
	}
	return entityID
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

	// Validate --resample before fetching anything. Doing it at the point of
	// use meant only the numeric branch ever checked: a non-numeric entity took
	// the state-timeline path and never saw the flag at all, so `--resample 0m`
	// was rejected for one entity and silently ignored for the next.
	if flagEntResample != "" {
		if _, resErr := parseResampleDuration(flagEntResample); resErr != nil {
			return resErr
		}
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
			if err := errUnknownEntity(ctx, client, entityID); err != nil {
				return err
			}
			return emitEmptyList(w, "no history data")
		}
		return renderStateTimeline(w, entityID, changes)
	}

	// Cache the fetched points
	cachePoints(ctx, cfg.Dir, entityID, points)

	// Resample
	if flagEntResample != "" {
		d, parseErr := parseResampleDuration(flagEntResample)
		if parseErr != nil {
			return parseErr
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
		// An unknown entity has no attributes either; say which of the two it is.
		if err := errUnknownEntity(ctx, client, entityID); err != nil {
			return err
		}
		return emitEmptyList(w, fmt.Sprintf("no attribute data for %q", flagEntAttr))
	}

	// Resample
	if flagEntResample != "" {
		d, parseErr := parseResampleDuration(flagEntResample)
		if parseErr != nil {
			return parseErr
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
			if err := errUnknownEntity(ctx, client, entityID); err != nil {
				return err
			}
			return emitEmptyList(w, "no history data")
		}
		return renderStateAnomalies(w, entityID, changes)
	}

	anomalies := analyze.DetectAll(points,
		defaultGapThreshold,
		defaultStuckThreshold,
		defaultSpikeZ,
	)

	if len(anomalies) == 0 {
		return emitEmptyList(w, entityID+": no anomalies detected")
	}

	if !flagJSON {
		_, _ = fmt.Fprintf(w, "%s: %d anomalies\n", entityID, len(anomalies))
	}

	tbl := &format.Table{
		Headers: []string{"type", "time", "detail"},
		Rows:    make([][]string, len(anomalies)),
	}
	for i, a := range anomalies {
		tbl.Rows[i] = []string{
			string(a.Type),
			formatShortTime(a.Start.Format(time.RFC3339)),
			a.Detail,
		}
		tbl.SetMachine(i, "time", formatMachineTime(a.Start.Format(time.RFC3339)))
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
	if err := degeneracy.Check("/api/history/period", &outer); err != nil {
		return nil, err
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
	Attributes  wireAttributes `json:"attributes"`
	EntityID    string         `json:"entity_id"`
	State       string         `json:"state"`
	LastChanged string         `json:"last_changed"`
}

func parseAttrHistoryResponse(data []byte, attr string) ([]analyze.DataPoint, error) {
	var outer [][]historyEntryFull
	if err := json.Unmarshal(data, &outer); err != nil {
		return nil, fmt.Errorf("parsing history response: %w", err)
	}
	if err := degeneracy.Check("/api/history/period", &outer); err != nil {
		return nil, err
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
	if err := degeneracy.Check("/api/history/period", &outer); err != nil {
		return nil, err
	}

	if len(outer) == 0 || len(outer[0]) == 0 {
		return nil, nil
	}

	// Records for states worth showing; unavailable/unknown are dropped, but
	// remembered as a discontinuity (see below).
	type record struct {
		entry historyEntry
		// afterGap marks a record that follows a dropped unavailable/unknown
		// one, so two identical states either side of an outage stay two runs.
		afterGap bool
	}
	var entries []record
	gap := false
	for _, e := range outer[0] {
		if e.State == "unavailable" || e.State == "unknown" {
			gap = true
			continue
		}
		entries = append(entries, record{entry: e, afterGap: gap})
		gap = false
	}
	if len(entries) == 0 {
		return nil, nil
	}

	// Collapse consecutive records that report the SAME state into one run.
	//
	// HA's history returns a record per state *update*, and an attribute-only
	// update repeats the state unchanged — so a sensor that held one value for
	// 40 minutes arrives as eight records five minutes apart. Rendering one row
	// each turned 37 real state runs into 287 rows of `5m0s`, and left the
	// reader to re-derive the runs this command exists to produce. A run keeps
	// the time the state was ENTERED, and lasts until the next different state
	// (or until now).
	type run struct {
		at    time.Time
		state string
	}
	runs := make([]run, 0, len(entries))
	for _, rec := range entries {
		t, timeErr := time.Parse(time.RFC3339Nano, rec.entry.LastChanged)
		if timeErr != nil {
			t, timeErr = time.Parse(time.RFC3339, rec.entry.LastChanged)
			if timeErr != nil {
				continue
			}
		}
		if n := len(runs); n > 0 && runs[n-1].state == rec.entry.State && !rec.afterGap {
			continue // same state, still the same run
		}
		runs = append(runs, run{at: t, state: rec.entry.State})
	}

	changes := make([]analyze.StateChange, len(runs))
	for i, r := range runs {
		end := now
		if i+1 < len(runs) {
			end = runs[i+1].at
		}
		changes[i] = analyze.StateChange{
			Time:     r.at,
			State:    r.state,
			Duration: end.Sub(r.at),
		}
	}

	return changes, nil
}

func renderStateTimeline(w io.Writer, entityID string, changes []analyze.StateChange) error {
	if !flagJSON {
		_, _ = fmt.Fprintf(w, "%s: %d state changes\n", entityID, len(changes))
	}

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
		tbl.SetMachine(i, "time", formatMachineTime(c.Time.Format(time.RFC3339)))
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

func renderStateAnomalies(w io.Writer, entityID string, changes []analyze.StateChange) error {
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
		return emitEmptyList(w, entityID+": no anomalies detected")
	}

	if !flagJSON {
		_, _ = fmt.Fprintf(w, "%s: %d anomalies\n", entityID, len(anomalies))
	}

	tbl := &format.Table{
		Headers: []string{"type", "time", "detail"},
		Rows:    make([][]string, len(anomalies)),
	}
	for i, a := range anomalies {
		tbl.Rows[i] = []string{
			string(a.Type),
			formatShortTime(a.Start.Format(time.RFC3339)),
			a.Detail,
		}
		tbl.SetMachine(i, "time", formatMachineTime(a.Start.Format(time.RFC3339)))
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
	if !flagJSON {
		_, _ = fmt.Fprintf(w, "%s: %d points\n", entityID, len(points))
	}

	tbl := &format.Table{
		Headers: []string{"time", "value"},
		Rows:    make([][]string, len(points)),
	}
	for i, p := range points {
		tbl.Rows[i] = []string{
			formatShortTime(p.Time.Format(time.RFC3339)),
			strconv.FormatFloat(p.Value, 'f', 2, 64),
		}
		tbl.SetMachine(i, "time", formatMachineTime(p.Time.Format(time.RFC3339)))
	}

	return tbl.Render(w, format.RenderOpts{
		Top:     flagTop,
		Full:    flagFull,
		JSON:    flagJSON,
		Compact: true,
	})
}

// filterEntitiesByPattern keeps entities matching pattern on the entity_id or
// on any other identifier hactl prints for them (D-1, invariant H-17).
//
// Today that second set is exactly the automation's other two names: the config
// `id:` and the alias. HA carries them as attributes.id and
// attributes.friendly_name; `auto show --json` prints the former as config_id,
// `auto cat`/`diff`/`apply` require it, and every `auto` target command
// resolves the latter — so a caller can be holding either when they reach for
// the discovery fallback the manual points them at, `ent ls --pattern`.
// Matching only entity_id answered "nothing", which under the
// stop-at-the-first-miss rule reads as "no such entity" (D6/R2).
func filterEntitiesByPattern(states []entityState, pattern string) []entityState {
	result := make([]entityState, 0, len(states))
	for _, s := range states {
		if matchPattern(s.EntityID, pattern) {
			result = append(result, s)
			continue
		}
		if cfgID := entityConfigID(s); cfgID != "" && matchPattern(cfgID, pattern) {
			result = append(result, s)
			continue
		}
		if alias := entityAlias(s); alias != "" && matchPattern(alias, pattern) {
			result = append(result, s)
		}
	}
	return result
}

// entityConfigID returns the config `id:` HA carries for an automation entity,
// or "" for anything else. Only the automation domain is claimed here: it is the
// domain where HA's config id and entity_id are independent strings by design,
// and the only one hactl prints the config id for.
func entityConfigID(s entityState) string {
	if haapi.EntityIDDomain(s.EntityID) != "automation" {
		return ""
	}
	id, _ := s.Attributes["id"].(string)
	return id
}

// entityAlias returns the alias HA carries for an automation entity (verbatim,
// as friendly_name), or "" for anything else. The scope bound is the same as
// entityConfigID's, and for the same reason: an automation's alias is an
// identifier — every `auto` target command resolves it (D-1) — while another
// domain's friendly_name is a display name no hactl command resolves anything
// by, so matching it would widen the filter past what hactl accepts.
func entityAlias(s entityState) string {
	if haapi.EntityIDDomain(s.EntityID) != "automation" {
		return ""
	}
	alias, _ := s.Attributes["friendly_name"].(string)
	return alias
}

func filterEntitiesByRestored(states []entityState) []entityState {
	result := make([]entityState, 0, len(states))
	for _, s := range states {
		if isRestoredAttr(s.Attributes) {
			result = append(result, s)
		}
	}
	return result
}

// isRestoredAttr reports whether HA flagged this state as restored — i.e. it was
// resurrected from the entity registry/recorder because no active platform
// entity backs its unique_id anymore. It is HA's own marker for an orphaned
// "ghost" entry: the automation/script/helper was deleted or re-authored under a
// new id, so there is no config left to repair (nothing for ref scan/replace to
// find). See issue #54.
func isRestoredAttr(attrs map[string]any) bool {
	b, _ := attrs["restored"].(bool)
	return b
}

// boolCell renders a boolean for table output: "yes" when true, empty otherwise
// (so the compact renderer keeps the majority of rows quiet).
func boolCell(b bool) string {
	if b {
		return "yes"
	}
	return ""
}

func filterEntitiesByDomain(states []entityState, domain string) []entityState {
	result := make([]entityState, 0, len(states))
	for _, s := range states {
		if haapi.EntityIDDomain(s.EntityID) == domain {
			result = append(result, s)
		}
	}
	return result
}

// matchPattern matches a record's name against a glob or substring pattern,
// ignoring case. If the pattern contains no glob characters (* or ?), it is
// treated as a substring match. Otherwise it is matched as a glob.
//
// Case is ignored because every sibling filter ignores it — `--name`, `--area`
// and `--label` all do — and because the values this matches are not always
// machine-cased. The doc comment used to say "matches entity IDs", which are
// always lowercase, so case genuinely could not bite; then `device ls
// --pattern` reused the function against `name`, which Home Assistant carries
// exactly as a human typed it ("WoziSued", "Wozi Tv"). `--pattern wozi`
// answered "no devices" for eight matching devices, and an empty listing reads
// as "no such thing" rather than "not spelled the way I store it".
//
// `device ls --pattern` was in fact case-insensitive once. Commit 17039dd
// deleted the strings.ToLower to make it consistent with `ent ls --pattern` —
// harmonising toward the sibling with no stake in the answer. Consistency was
// the right instinct and the wrong direction.
func matchPattern(s, pattern string) bool {
	if pattern == "" {
		return s == ""
	}
	s, pattern = strings.ToLower(s), strings.ToLower(pattern)
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

// entStateWidth is how wide the state column renders for a reader; the machine
// contract carries what Home Assistant sent. See the SetWidth call in
// writeEntListing.
const entStateWidth = 20

// The domain half of an entity_id is haapi.EntityIDDomain. This file used to
// carry its own `parseEntityDomain`, which differed in one case — a dotless id
// came back whole rather than empty — and the rename check then had to agree
// with it about what "the domain" is. Two functions answering one question is
// how #30's display and --name halves came to disagree; there is one now.
// filterEntitiesByArea matches an area by the id `area ls` prints in its first
// column, or by its name — the same pair `device ls --area` has always matched.
//
// It matched the name only. `area ls` prints `area_id` first, and
// docs/manual.md routes a caller who cannot find something to "the matching
// registry `ls`" — which handed them the id that returned zero rows, exit 0,
// while `device ls --area <same id>` returned the row. H-17: an identifier
// hactl prints is an identifier hactl accepts.
func filterEntitiesByArea(states []entityState, rc *registryContext, area string) []entityState {
	result := make([]entityState, 0, len(states))
	for _, s := range states {
		areaID := rc.effectiveAreaID(s.EntityID)
		if areaID == "" {
			continue
		}
		if containsFold(areaID, area) || containsFold(rc.areaName(s.EntityID), area) {
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

// labelExistsInRegistry returns true if the given label value matches (by
// substring on id or name) at least one registered label.
func labelExistsInRegistry(rc *registryContext, label string) bool {
	return len(matchingLabelIDs(rc.labelByID, label)) > 0
}

// filterEntitiesByLabel keeps entities carrying any label matched by
// matchingLabelIDs — see its doc comment for the substring rule and the two
// bugs (pre-check/filter disagreement, cross-label joined-string false
// positives) this replaced.
func filterEntitiesByLabel(states []entityState, rc *registryContext, label string) []entityState {
	matchIDs := matchingLabelIDs(rc.labelByID, label)
	if len(matchIDs) == 0 {
		return nil
	}
	result := make([]entityState, 0, len(states))
	for _, s := range states {
		ent, ok := rc.entityByID[s.EntityID]
		if !ok {
			continue
		}
		for _, lid := range ent.Labels {
			if matchIDs[lid] {
				result = append(result, s)
				break
			}
		}
	}
	return result
}

var flagEntRenameAllowPartial bool

var entRenameCmd = &cobra.Command{
	Use:   "rename <old_entity_id> <new_entity_id>",
	Short: "Rename an entity and rewrite every reference (dry-run by default)",
	Long: "Rename a registry entity (config/entity_registry/update with new_entity_id), then rewrite " +
		"every literal reference across YAML config files and dashboards — the ref replace pass — in " +
		"one command (D-11: registry first, it is the authoritative object). Dry-run by default: the " +
		"preview resolves the old id against the registry, pre-checks the new id for collisions, and " +
		"counts the references a confirmed run would rewrite. Requires hactl-companion (it rewrites " +
		"the config half). A dashboard that cannot be scanned refuses the run unless --allow-partial.",
	Args: takes(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runEntRename(cmd.Context(), cmd.OutOrStdout(), args[0], args[1])
	},
}

// runEntRename composes the two halves of a rename per D-11: registry rename
// first (a failure there leaves every reference untouched), reference rewrite
// second (a failure there names the completed half and the idempotent
// remediation, never a silent rollback).
func runEntRename(ctx context.Context, w io.Writer, oldID, newID string) error {
	// Shape refusals before any connection (H-2: the preview fails exactly
	// where --confirm would, and HA refuses a malformed id at confirm time).
	//
	// This used to check only that the id had a dot with something on both
	// sides, so `input_boolean.pg w5 bad`, `input_boolean.PG_w5_Bad!`,
	// `input_boolean.pg_w5_🔥bad`, `input_boolean.pg.w5.bad` and the
	// cross-domain `switch.pg_w5_renamed` all printed a confident "would
	// rename … references: 2" at exit 0 — and `config/entity_registry/update`
	// answers "Invalid entity ID" or "New entity ID should be same domain" to
	// every one of them (measured on a live instance, 2026-07-31). The comment
	// above was already the promise; the code kept half of it, which is the
	// shape of every defect in dev/surfaces/README.md.
	if !haapi.ValidEntityID(newID) {
		return fmt.Errorf("new entity_id %q is not one Home Assistant accepts: <domain>.<object_id>, both "+
			"lowercase letters, digits and underscores, no leading, trailing or doubled underscore "+
			"(HA answers \"Invalid entity ID\")", newID)
	}
	if from, to := haapi.EntityIDDomain(oldID), haapi.EntityIDDomain(newID); from != to {
		return fmt.Errorf("renaming %s to %s would move it from the %s domain to %s — Home Assistant refuses "+
			"that (\"New entity ID should be same domain\"); an entity's domain follows its platform, "+
			"not its id", oldID, newID, from, to)
	}
	if newID == oldID {
		return errors.New("old and new entity_id are identical — nothing to rename")
	}

	// The companion is the transport for the reference half; without it this
	// command does not run at all (same posture as ref replace).
	src, err := connectRefSources(ctx)
	if err != nil {
		return err
	}
	defer src.close()

	entries, err := src.ws.EntityRegistryList(ctx)
	if err != nil {
		return fmt.Errorf("fetching entity registry: %w", err)
	}
	entry, ok := findEntityRegistryEntry(entries, oldID)
	if !ok {
		return fmt.Errorf("entity %q not found in the registry — only registry entities can be renamed; "+
			"a state-only entity has no registry entry (use 'ent ls' to see entities, 'ent show' for detail)", oldID)
	}
	// Collision pre-check; HA also refuses on a race ("Entity with this ID is
	// already registered", oracle-verified), so this is the friendlier first
	// line of the same defense, not the only one.
	if _, exists := findEntityRegistryEntry(entries, newID); exists {
		return fmt.Errorf("entity %q already exists — renaming onto it would collide (HA refuses this too)", newID)
	}

	// Both paths scan first: the preview reports the reference count, and the
	// confirmed run must refuse a partial dashboard scan BEFORE the registry
	// write, or the refusal would arrive with the rename already done.
	refs, scope, err := countRenameReferences(ctx, src, oldID)
	if err != nil {
		return err
	}
	if scope.partial() && !flagEntRenameAllowPartial {
		return fmt.Errorf("%d of %d dashboard(s) could not be scanned (%s), so this rename cannot claim to cover "+
			"every reference; nothing was renamed. Re-run with --allow-partial to proceed over what could be read",
			len(scope.unscanned), scope.total(), strings.Join(scope.unscanned, "; "))
	}

	if !flagEntConfirm {
		return dryRun("rename entity").
			with("entity_id", oldID).
			with("new_entity_id", newID).
			withIf(entry.Platform != "", "platform", entry.Platform).
			with("references", refs).
			withHint("use --confirm to rename the registry entry and rewrite every reference "+
				"(reference detail: hactl ref scan "+oldID+")").
			render(w)
	}

	if err := src.ws.EntityRegistryUpdate(ctx, oldID, map[string]any{"new_entity_id": newID}); err != nil {
		return fmt.Errorf("renaming registry entry: %w", err)
	}
	if !flagJSON {
		_, _ = fmt.Fprintf(w, "renamed registry entry %s -> %s\n", oldID, newID)
	}

	if err := refReplaceWithOptions(ctx, w, oldID, newID, true, flagEntRenameAllowPartial); err != nil {
		return fmt.Errorf("registry entry renamed to %s, but the reference rewrite failed: %w — "+
			"references may still point at %s; re-run 'hactl ref replace %s %s --confirm' (idempotent)",
			newID, err, oldID, oldID, newID)
	}
	return nil
}

// countRenameReferences runs the read-only halves of the ref scan so the
// rename preview can report how many references a confirmed run would move.
func countRenameReferences(ctx context.Context, src *refSources, target string) (int, dashboardScanScope, error) {
	cfgResp, err := src.cc.RefScan(ctx, target)
	if err != nil {
		return 0, dashboardScanScope{}, fmt.Errorf("companion ref scan: %w", err)
	}
	dashboards, err := src.ws.DashboardList(ctx)
	if err != nil {
		return 0, dashboardScanScope{}, fmt.Errorf("listing dashboards: %w", err)
	}
	hits, scope := scanDashboards(ctx, src.ws, dashboardScanTargets(dashboards), target)
	return len(cfgResp.Hits) + len(hits), scope, nil
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

	// Resolve the entity before planning anything. Labels live in the entity
	// registry, so an entity that is not in it cannot carry one — HA rejects
	// the update. Resolving here makes the dry run fail exactly where the
	// confirmed run would, and matches what `ent set-area` has always done:
	// the two commands used to disagree on the same unregistered entity, one
	// erroring and one printing a confident plan at exit 0.
	entries, err := ws.EntityRegistryList(ctx)
	if err != nil {
		return fmt.Errorf("fetching entity registry: %w", err)
	}
	entry, ok := findEntityRegistryEntry(entries, entityID)
	if !ok {
		return fmt.Errorf("entity %q not found in registry (use 'ent ls' to see available entities)", entityID)
	}
	currentLabels := entry.Labels

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

	if !flagEntConfirm {
		return dryRunEntSetLabelSummary(entityID, currentLabels, merged).render(w)
	}

	if err := ws.EntityRegistryUpdate(ctx, entityID, map[string]any{"labels": merged}); err != nil {
		return fmt.Errorf("updating entity labels: %w", err)
	}

	// Slices, not their %v rendering: under --json a caller gets real arrays.
	return done("set entity labels").
		with("entity_id", entityID).
		with("labels", nonNil(merged)).
		text("%s: labels set to %v", entityID, merged).
		render(w)
}

func dryRunEntSetLabelSummary(entityID string, current, merged []string) *dryRunPlan {
	// Slices, not their %v rendering: under --json a caller gets real arrays.
	return dryRun("set entity labels").
		with("entity_id", entityID).
		with("current_labels", nonNil(current)).
		with("new_labels", nonNil(merged))
}

// nonNil turns a nil slice into an empty one, so --json emits [] rather than
// null for "this entity has no labels".
func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
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
		return dryRunEntSetAreaSummary(entityEntry, areaEntry, areas).render(w)
	}

	if err := ws.EntityRegistryUpdate(ctx, entityID, map[string]any{"area_id": areaEntry.AreaID}); err != nil {
		return fmt.Errorf("updating entity area: %w", err)
	}

	return done("set entity area").
		with("entity_id", entityID).
		with("area_id", areaEntry.AreaID).
		with("area_name", areaEntry.Name).
		text("%s: area set to %s", entityID, areaEntry.AreaID).
		render(w)
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

func dryRunEntSetAreaSummary(entity haapi.EntityRegistryEntry, area haapi.AreaEntry, areas []haapi.AreaEntry) *dryRunPlan {
	currentArea := entity.AreaID
	for _, a := range areas {
		if a.AreaID == entity.AreaID {
			currentArea = fmt.Sprintf("%s (%s)", a.Name, a.AreaID)
			break
		}
	}

	return dryRun("set entity area").
		with("entity_id", entity.EntityID).
		withIf(currentArea != "", "current_area", currentArea).
		with("new_area", fmt.Sprintf("%s (%s)", area.Name, area.AreaID))
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
	if degErr := degeneracy.Check("/api/states", &states); degErr != nil {
		return degErr
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

	// An entity that is in neither the registry nor the state machine AND has
	// no references anywhere is a miss, and a miss is an error — the rule
	// `ent hist`, `ent who`, `ent anomalies` and `ent show` already follow.
	//
	// Only the empty case. An unknown entity WITH references is a real and
	// useful answer: a config-only entity that HA never instantiated still has
	// automations pointing at it, and reporting those dangling references is
	// most of why this command exists.
	//
	// The narrow case was broken in one output mode only. Text said "not in the
	// registry (stale/renamed or deleted); 0 relations found"; --json returned
	// before `known` was ever consulted and emitted `[]` — the identical
	// document a live entity with no relations produces. The manual tells a
	// caller "an empty answer means the entity was quiet, never that it was
	// mistyped", and TestEmptyResultJSON_EntRelated pinned that [] as correct.
	//
	// `--stale` is the documented way to ask about an entity that is gone and
	// has already returned above, so this cannot break it.
	if len(related) == 0 {
		if !known {
			return fmt.Errorf("entity %q is not in the registry or the state machine and nothing references it "+
				"(stale, renamed, deleted, or mistyped) — use 'ent related %s --stale' to search the config files",
				entityID, entityID)
		}
		if flagJSON {
			return writeEmptyJSONArray(w)
		}
		_, _ = fmt.Fprintf(w, "%s: no related entities found\n", entityID)
		return nil
	}

	if !flagJSON {
		if known {
			_, _ = fmt.Fprintf(w, "%s: %d related entities\n", entityID, len(related))
		} else {
			_, _ = fmt.Fprintf(w, "%s: not in the registry (stale/renamed or deleted); %d dangling reference(s) still point here\n", entityID, len(related))
		}
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

// findCompanionRelations asks the companion for the config/YAML half of the
// relation graph. Failures are warned about rather than swallowed: `ent related`
// guides delete decisions, so answering "no related entities found" when the
// config scan never ran is a harmful wrong answer, and slog.Debug is invisible
// at the default log level.
func findCompanionRelations(ctx context.Context, cfg *config.Config, ws *haapi.WSClient, entityID string, stale bool) ([]relatedEntry, []companion.StaleRef) {
	companionURL, err := companion.Discover(ctx, cfg, ws)
	if err != nil {
		var de *companion.DiscoveryError
		if errors.As(err, &de) {
			slog.Warn("companion unavailable; config files were not scanned for references", "reason", string(de.Reason))
		} else {
			slog.Warn("companion unavailable; config files were not scanned for references", "error", err)
		}
		return nil, nil
	}
	cc := companion.New(companionURL, cfg.CompanionToken).WithIngressAuth(ws)
	res, err := cc.RelatedEntity(ctx, entityID, stale)
	if err != nil {
		slog.Warn("companion related-graph call failed; config files were not scanned for references", "error", err)
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
		return emitEmptyList(w, entityID+": no stale references found (entity fully cleaned up or config unavailable)")
	}
	if !flagJSON {
		_, _ = fmt.Fprintf(w, "%s: stale (renamed/deleted); %d config reference(s):\n", entityID, len(refs))
	}
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

// findAreaNeighbors lists other same-domain entities in entityID's EFFECTIVE
// area (rc.effectiveAreaID — entity's own area, else its device's, per H-8).
// Reading ent.AreaID/e.AreaID directly here would miss every entity whose
// area only exists via its device, which is the common case.
func findAreaNeighbors(rc *registryContext, entityID string) []relatedEntry {
	if _, ok := rc.entityByID[entityID]; !ok {
		return nil
	}
	areaID := rc.effectiveAreaID(entityID)
	if areaID == "" {
		return nil
	}
	// Every entity in the area, whatever its domain. This used to also require
	// haapi.EntityIDDomain(e) == haapi.EntityIDDomain(entityID), which made "area
	// neighbors" mean "same area AND same domain" — narrower than `ent ls
	// --area` and than HA's own area_entities(), which has no notion of a
	// domain. The restriction was invisible in the output (there is no domain
	// column, and the manual qualifies nothing), so the light in the same room
	// as the sensor you are about to delete simply never appeared.
	areaName := rc.areaByID[areaID].Name
	var result []relatedEntry
	for _, e := range rc.entityByID {
		if e.EntityID == entityID {
			continue
		}
		if rc.effectiveAreaID(e.EntityID) == areaID {
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
		if haapi.EntityIDDomain(s.EntityID) != "group" {
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
