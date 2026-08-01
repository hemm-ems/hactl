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

var labelCmd = family(&cobra.Command{
	Use:   "label",
	Short: "Discover and manage labels",
	Long:  "List, create, and inspect Home Assistant labels.",
})

var labelLsCmd = &cobra.Command{
	Use:   "ls",
	Args:  takesNone(),
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
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runLabelCreate(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

var labelDeleteCmd = &cobra.Command{
	Use:   "delete <label_id>",
	Short: "Delete a label (dry-run by default)",
	Long:  "Delete a label from the Home Assistant label registry. Use --confirm to apply.",
	Args:  takes(1),
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
	tbl.SetWidth("description", 40)
	for i, l := range labels {
		tbl.Rows[i] = []string{
			l.LabelID,
			l.Name,
			l.Color,
			l.Description,
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

// dryRunLabelSummary builds the preview for label create.
func dryRunLabelSummary(name, icon, color, description string) *dryRunPlan {
	return dryRun("create label").
		with("name", name).
		withIf(icon != "", "icon", icon).
		withIf(color != "", "color", color).
		withIf(description != "", "description", description)
}

func runLabelCreate(ctx context.Context, w io.Writer, name string) error {
	// Before the plan, so the preview fails exactly where --confirm would
	// (H-2). See requireRegistryName for why this cannot wait for HA's answer.
	if err := requireRegistryName("label", name); err != nil {
		return err
	}

	if !flagLabelConfirm {
		return dryRunLabelSummary(name, flagLabelIcon, flagLabelColor, flagLabelDesc).render(w)
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

	return done("create label").
		with("label_id", entry.LabelID).
		with("name", entry.Name).
		withIf(flagLabelIcon != "", "icon", flagLabelIcon).
		withIf(flagLabelColor != "", "color", flagLabelColor).
		withIf(flagLabelDesc != "", "description", flagLabelDesc).
		text("created label %q (id=%s)", entry.Name, entry.LabelID).
		render(w)
}

func runLabelDelete(ctx context.Context, w io.Writer, labelID string) error {
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
	entry, ok := resolveRegistryTarget(labelID, labels, func(l haapi.LabelEntry) (string, string) {
		return l.LabelID, l.Name
	})
	if !ok {
		return fmt.Errorf("label %q not found (use 'label ls' to see available labels)", labelID)
	}
	labelID = entry.LabelID

	if !flagLabelConfirm {
		return dryRun("delete label").
			with("label_id", entry.LabelID).
			with("name", entry.Name).
			render(w)
	}

	if err := ws.LabelRegistryDelete(ctx, labelID); err != nil {
		return fmt.Errorf("deleting label: %w", err)
	}

	return done("delete label").
		with("label_id", labelID).
		with("name", entry.Name).
		text("deleted label %q", labelID).
		render(w)
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

// --- shared add/remove resolution for `ent set-label` and `device set-label` ---
//
// Both commands resolve a caller-given label (by name or ID) against the
// registry, merge it into the target's existing set, and — since finding #81 —
// can also take one back off. The resolution and the merge/remove arithmetic
// used to be written out twice; here once, so the two commands cannot answer
// the same input differently the way `device ls --pattern` once diverged from
// its siblings (D-2's cautionary tale, dev/surfaces/README.md).

// resolvedLabel pairs a caller-given label name with the registry ID it
// resolved to, so a refusal can quote back what the caller typed rather than
// hactl's internal ID.
type resolvedLabel struct {
	name string
	id   string
}

// labelIndex builds the name/ID → label_id lookup runEntSetLabel and
// runDeviceSetLabel both need, exactly as each built it inline before.
func labelIndex(labels []haapi.LabelEntry) map[string]string {
	index := make(map[string]string, len(labels))
	for _, l := range labels {
		index[strings.ToLower(l.Name)] = l.LabelID
		index[l.LabelID] = l.LabelID
	}
	return index
}

// resolveLabelRefs resolves each caller-given label against the index,
// case-insensitively by name or exactly by ID, or refuses on the first one
// that matches neither.
func resolveLabelRefs(index map[string]string, names []string) ([]resolvedLabel, error) {
	out := make([]resolvedLabel, 0, len(names))
	for _, name := range names {
		id, ok := index[strings.ToLower(name)]
		if !ok {
			return nil, fmt.Errorf("label %q not found (use 'label ls' to see available labels)", name)
		}
		out = append(out, resolvedLabel{name: name, id: id})
	}
	return out, nil
}

// refuseAddRemoveOverlap applies H-25's exclusivity clause to `set-label`'s
// two ways of naming what should happen to one label: a label resolving to the
// same registry ID on both sides says two different things about itself in one
// call, so the command ends rather than picking a winner (D-44).
func refuseAddRemoveOverlap(toAdd, toRemove []resolvedLabel) error {
	removeByID := make(map[string]string, len(toRemove))
	for _, r := range toRemove {
		removeByID[r.id] = r.name
	}
	for _, a := range toAdd {
		if removeName, conflict := removeByID[a.id]; conflict {
			return &flagContractError{fmt.Sprintf(
				"label %q and --remove %q name the same label and cannot both be honoured; pass one",
				a.name, removeName)}
		}
	}
	return nil
}

// applyLabelDelta computes the label set a write should send: the current set,
// with toAdd merged in (deduplicated, original merge-only behaviour) and
// toRemove taken back out. removed lists exactly the IDs that were actually
// present and came off — so a --remove of a label the target never had is
// visible in the plan as a no-op rather than silently claimed.
func applyLabelDelta(current []string, toAdd, toRemove []resolvedLabel) (final, removed []string) {
	removeSet := make(map[string]bool, len(toRemove))
	for _, r := range toRemove {
		removeSet[r.id] = true
	}

	seen := make(map[string]bool, len(current)+len(toAdd))
	merged := make([]string, 0, len(current)+len(toAdd))
	for _, l := range current {
		if !seen[l] {
			seen[l] = true
			merged = append(merged, l)
		}
	}
	for _, r := range toAdd {
		if !seen[r.id] {
			seen[r.id] = true
			merged = append(merged, r.id)
		}
	}

	final = make([]string, 0, len(merged))
	for _, l := range merged {
		if removeSet[l] {
			removed = append(removed, l)
			continue
		}
		final = append(final, l)
	}
	return final, removed
}
