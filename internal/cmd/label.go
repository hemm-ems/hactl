package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var flagLabelColor string
var flagLabelIcon string
var flagLabelDesc string
var flagLabelConfirm bool

var labelCmd = &cobra.Command{
	Use:   "label",
	Short: "Discover and manage labels",
	Long:  "List, create, and inspect Home Assistant labels.",
}

var labelLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List all labels",
	Long:  "Show all labels registered in Home Assistant.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLabelLs(cmd.Context(), cmd.OutOrStdout())
	},
}

var labelCreateCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new label",
	Long:  "Create a label in the Home Assistant label registry.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLabelCreate(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var labelDeleteCmd = &cobra.Command{
	Use:   "delete <label_id>",
	Short: "Delete a label (dry-run by default)",
	Long:  "Delete a label from the Home Assistant label registry. Use --confirm to apply.",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLabelDelete(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	labelCreateCmd.Flags().StringVar(&flagLabelColor, "color", "", "label color (e.g. red, blue, #ff0000)")
	labelCreateCmd.Flags().StringVar(&flagLabelIcon, "icon", "", "label icon (e.g. mdi:flash)")
	labelCreateCmd.Flags().StringVar(&flagLabelDesc, "description", "", "label description")
	labelCreateCmd.Flags().BoolVar(&flagLabelConfirm, "confirm", false, "actually create (default is dry-run)")
	labelDeleteCmd.Flags().BoolVar(&flagLabelConfirm, "confirm", false, "actually delete (default is dry-run)")
	labelCmd.AddCommand(labelLsCmd, labelCreateCmd, labelDeleteCmd)
	rootCmd.AddCommand(labelCmd)
}

func runLabelLs(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return fmt.Errorf("connecting to HA: %w", connErr)
	}
	defer func() { _ = ws.Close() }()

	labels, err := ws.LabelRegistryList(ctx)
	if err != nil {
		return fmt.Errorf("fetching labels: %w", err)
	}

	if len(labels) == 0 {
		return emitEmptyList(w, "no labels")
	}

	tbl := &format.Table{
		Headers: []string{"label_id", "name", "color", "description"},
		Rows:    make([][]string, len(labels)),
	}
	for i, l := range labels {
		tbl.Rows[i] = []string{
			l.LabelID,
			l.Name,
			l.Color,
			truncateStr(l.Description, 40),
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

// dryRunLabelSummary returns the dry-run summary string for label create.
func dryRunLabelSummary(name, icon, color, description string) string {
	s := "dry-run: would create label\n"
	s += fmt.Sprintf("  name:        %s\n", name)
	if icon != "" {
		s += fmt.Sprintf("  icon:        %s\n", icon)
	}
	if color != "" {
		s += fmt.Sprintf("  color:       %s\n", color)
	}
	if description != "" {
		s += fmt.Sprintf("  description: %s\n", description)
	}
	s += "use --confirm to apply"
	return s
}

func runLabelCreate(ctx context.Context, w io.Writer, name string) error {
	if !flagLabelConfirm {
		_, _ = fmt.Fprintln(w, dryRunLabelSummary(name, flagLabelIcon, flagLabelColor, flagLabelDesc))
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

	entry, err := ws.LabelRegistryCreate(ctx, name, flagLabelColor, flagLabelIcon, flagLabelDesc)
	if err != nil {
		return fmt.Errorf("creating label: %w", err)
	}

	_, _ = fmt.Fprintf(w, "created label %q (id=%s)\n", entry.Name, entry.LabelID)
	return nil
}

func runLabelDelete(ctx context.Context, w io.Writer, labelID string) error {
	if !flagLabelConfirm {
		_, _ = fmt.Fprintln(w, "dry-run: would delete label")
		_, _ = fmt.Fprintf(w, "  label_id: %s\n", labelID)
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

	if err := ws.LabelRegistryDelete(ctx, labelID); err != nil {
		return fmt.Errorf("deleting label: %w", err)
	}

	_, _ = fmt.Fprintf(w, "deleted label %q\n", labelID)
	return nil
}

func truncateStr(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-1]) + "…"
}

// fetchRegistryContext fetches entity registry, areas, labels, floors, and
// devices in sequence. Returns lookup maps for quick resolution.
//
// H-8: the device registry is fetched (and kept, in deviceByID) so an
// entity's effective area can fall back to its device's area — see
// registryContext.effectiveAreaID.
func fetchRegistryContext(ctx context.Context, ws *haapi.WSClient) (*registryContext, error) {
	entities, err := ws.EntityRegistryList(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching entity registry: %w", err)
	}

	areas, err := ws.AreaRegistryList(ctx)
	if err != nil {
		slog.Warn("could not fetch areas", "error", err)
		areas = nil
	}

	labels, err := ws.LabelRegistryList(ctx)
	if err != nil {
		slog.Warn("could not fetch labels", "error", err)
		labels = nil
	}

	floors, err := ws.FloorRegistryList(ctx)
	if err != nil {
		slog.Warn("could not fetch floors", "error", err)
		floors = nil
	}

	devices, err := ws.DeviceRegistryList(ctx)
	if err != nil {
		slog.Warn("could not fetch devices", "error", err)
		devices = nil
	}

	rc := &registryContext{
		entityByID: make(map[string]haapi.EntityRegistryEntry, len(entities)),
		areaByID:   make(map[string]haapi.AreaEntry, len(areas)),
		labelByID:  make(map[string]haapi.LabelEntry, len(labels)),
		floorByID:  make(map[string]haapi.FloorEntry, len(floors)),
		deviceByID: make(map[string]haapi.DeviceRegistryEntry, len(devices)),
	}
	for _, e := range entities {
		rc.entityByID[e.EntityID] = e
	}
	for _, a := range areas {
		rc.areaByID[a.AreaID] = a
	}
	for _, l := range labels {
		rc.labelByID[l.LabelID] = l
	}
	for _, f := range floors {
		rc.floorByID[f.FloorID] = f
	}
	for _, d := range devices {
		rc.deviceByID[d.ID] = d
	}
	return rc, nil
}

type registryContext struct {
	entityByID map[string]haapi.EntityRegistryEntry
	areaByID   map[string]haapi.AreaEntry
	labelByID  map[string]haapi.LabelEntry
	floorByID  map[string]haapi.FloorEntry
	deviceByID map[string]haapi.DeviceRegistryEntry
}

// effectiveAreaID returns the area an entity actually sits in, replicating
// HA's own fallback for area_name()/area_entities()
// (homeassistant/helpers/template/extensions/areas.py, AreaExtension): the
// entity's own area_id wins when set; otherwise it inherits its device's
// area_id. Placing the DEVICE in a room is the normal HA pattern — assigning
// an area directly to an entity is the exception — so most real entities only
// resolve correctly through this fallback (H-8). Returns "" when neither the
// entity nor its device (if any) has an area.
func (rc *registryContext) effectiveAreaID(entityID string) string {
	ent, ok := rc.entityByID[entityID]
	if !ok {
		return ""
	}
	if ent.AreaID != "" {
		return ent.AreaID
	}
	if ent.DeviceID == "" {
		return ""
	}
	return rc.deviceByID[ent.DeviceID].AreaID
}

func (rc *registryContext) areaName(entityID string) string {
	areaID := rc.effectiveAreaID(entityID)
	if areaID == "" {
		return ""
	}
	area, ok := rc.areaByID[areaID]
	if !ok {
		return areaID
	}
	return area.Name
}

// labelNames returns the entity's OWN labels only — deliberately no device
// fallback. Unlike area, HA's labels do not inherit from the device:
// label_entities() (homeassistant/helpers/template/extensions/labels.py)
// resolves via entity_registry.async_entries_for_label with no device or area
// expansion, confirmed against running HA 2026.7.2 source: label_devices()
// finds a device carrying a label, but label_entities() for that same label
// returns none of the device's entities. Do not "fix" this to mirror the area
// fallback — that would make hactl disagree with HA itself (see H-8 test
// TestEntLsLabelMatchesOracleInheritance, which asserts equality with HA's
// own label_entities()).
func (rc *registryContext) labelNames(entityID string) string {
	ent, ok := rc.entityByID[entityID]
	if !ok || len(ent.Labels) == 0 {
		return ""
	}
	names := make([]string, 0, len(ent.Labels))
	for _, lid := range ent.Labels {
		lbl, ok := rc.labelByID[lid]
		if ok {
			names = append(names, lbl.Name)
		} else {
			names = append(names, lid)
		}
	}
	return strings.Join(names, ", ")
}

// matchingLabelIDs resolves a --label filter value to the set of registry
// label_ids whose id or name contains it, case-insensitively.
//
// Substring is the semantics docs/manual.md documents for --label everywhere
// it appears (ent ls, device ls, auto ls, script ls all say "filter by label
// name (substring)"), and it's what auto.go/script.go already implement
// (filterAutosByTag, and script.go's equivalent) — narrowing ent/device to an
// exact match would make --label behave differently depending on which
// command you typed. So the semantics stay substring; what this function
// fixes is two bugs in how that substring was applied:
//
//  1. labelExistsInRegistry used to require an EXACT id/name match while
//     filterEntitiesByLabel matched by substring — a query matching several
//     labels by substring but none exactly was wrongly reported "not found"
//     even though the filter below would have matched something.
//  2. The old filter substring-matched the entity's *joined* "name1, name2"
//     display string, so a query straddling the ", " separator (or matching
//     a totally different label already present in the same join) could
//     match — a false positive no per-label check would produce. Resolving
//     to a label_id set first and checking membership avoids that.
//
// ent.go (filterEntitiesByLabel/labelExistsInRegistry) and device.go
// (deviceHasLabel) both call this, so `ent ls --label` and `device ls
// --label` agree with each other now too.
func matchingLabelIDs(labelByID map[string]haapi.LabelEntry, query string) map[string]bool {
	lower := strings.ToLower(query)
	out := make(map[string]bool)
	for id, l := range labelByID {
		if strings.Contains(strings.ToLower(id), lower) || strings.Contains(strings.ToLower(l.Name), lower) {
			out[id] = true
		}
	}
	return out
}
