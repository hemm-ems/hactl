package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/format"
	"github.com/hemm-ems/hactl/internal/haapi"
)

var (
	flagDevicePattern string
	flagDeviceName    string
	flagDeviceArea    string
	flagDeviceLabel   string
)

var flagDeviceConfirm bool

var deviceCmd = family(&cobra.Command{
	Use:   "device",
	Short: "Browse, inspect, and place devices",
	Long:  "List and inspect Home Assistant devices, and assign their area or labels.",
})

var deviceSetAreaCmd = &cobra.Command{
	Use:   "set-area <device> <area>",
	Short: "Place a device in an area (dry-run by default)",
	Long: "Assign a device to an area; the device may be given by ID or name, the area by name or ID. " +
		"Every entity of the device without its own area_id inherits the device's area (H-8), so this " +
		"is the one-command way to move a whole device. Use --confirm to apply.",
	Args: takes(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDeviceSetArea(cmd.Context(), cmd.OutOrStdout(), args[0], args[1])
	},
}

var deviceSetLabelCmd = &cobra.Command{
	Use:   "set-label <device> <label>...",
	Short: "Add label(s) to a device (dry-run by default)",
	Long: "Merge one or more labels (by name or ID) into a device's labels; the device may be given " +
		"by ID or name. Use --confirm to apply.",
	Args: takesAtLeast(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDeviceSetLabel(cmd.Context(), cmd.OutOrStdout(), args[0], args[1:])
	},
}

var deviceLsCmd = &cobra.Command{
	Use:   "ls",
	Args:  takesNone(),
	Short: "List devices",
	Long:  "Show devices from the Home Assistant device registry, with entity counts.",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDeviceLs(cmd.Context(), cmd.OutOrStdout())
	},
}

var deviceShowCmd = &cobra.Command{
	Use:   "show <device>",
	Short: "Show device profile",
	Long:  "Display one device with its area, labels, and registered entities. The device argument may be an ID or name.",
	Args:  takes(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDeviceShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	deviceLsCmd.Flags().StringVar(&flagDevicePattern, "pattern", "", "filter by device ID/name (substring or glob)")
	deviceLsCmd.Flags().StringVar(&flagDeviceName, "name", "", "filter by device name substring")
	deviceLsCmd.Flags().StringVar(&flagDeviceArea, "area", "", "filter by area/room name or ID substring")
	deviceLsCmd.Flags().StringVar(&flagDeviceLabel, "label", "", "filter by label name or ID substring")
	deviceSetAreaCmd.Flags().BoolVar(&flagDeviceConfirm, "confirm", false, "actually set the area (default is dry-run)")
	deviceSetLabelCmd.Flags().BoolVar(&flagDeviceConfirm, "confirm", false, "actually set the labels (default is dry-run)")
	deviceCmd.AddCommand(deviceLsCmd, deviceShowCmd, deviceSetAreaCmd, deviceSetLabelCmd)
	rootCmd.AddCommand(deviceCmd)
}

// runDeviceSetArea mirrors runEntSetArea one registry over: resolve the area
// and the device before planning anything, so the dry run fails exactly where
// --confirm would (H-2). The write is DeviceRegistryUpdate — per its doc
// comment the only way to place an existing device into an area.
func runDeviceSetArea(ctx context.Context, w io.Writer, deviceRef, area string) error {
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
	areaEntry, ok := resolveAreaEntry(areas, area)
	if !ok {
		return fmt.Errorf("area %q not found (use 'area ls' to see available areas)", area)
	}

	devices, err := ws.DeviceRegistryList(ctx)
	if err != nil {
		return fmt.Errorf("fetching device registry: %w", err)
	}
	// H-17: device ls/show print the ID and the name — both resolve here.
	device, err := resolveDevice(devices, deviceRef)
	if err != nil {
		return err
	}

	if !flagDeviceConfirm {
		return dryRunDeviceSetAreaSummary(device, areaEntry, areas).render(w)
	}

	if err := ws.DeviceRegistryUpdate(ctx, device.ID, map[string]any{"area_id": areaEntry.AreaID}); err != nil {
		return fmt.Errorf("updating device area: %w", err)
	}

	_, _ = fmt.Fprintf(w, "%s: area set to %s\n", device.ID, areaEntry.AreaID)
	return nil
}

func dryRunDeviceSetAreaSummary(device haapi.DeviceRegistryEntry, area haapi.AreaEntry, areas []haapi.AreaEntry) *dryRunPlan {
	currentArea := device.AreaID
	for _, a := range areas {
		if a.AreaID == device.AreaID {
			currentArea = fmt.Sprintf("%s (%s)", a.Name, a.AreaID)
			break
		}
	}
	return dryRun("set device area").
		with("device_id", device.ID).
		withIf(deviceUserFacingName(device) != "", "device_name", deviceUserFacingName(device)).
		withIf(currentArea != "", "current_area", currentArea).
		with("new_area", fmt.Sprintf("%s (%s)", area.Name, area.AreaID)).
		withHint("use --confirm to apply (entities without an own area_id inherit the device's area)")
}

// runDeviceSetLabel mirrors runEntSetLabel: labels resolve by name or ID and
// merge into the device's existing set, deduplicated.
func runDeviceSetLabel(ctx context.Context, w io.Writer, deviceRef string, labels []string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return fmt.Errorf("connecting to HA: %w", connErr)
	}
	defer func() { _ = ws.Close() }()

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

	devices, err := ws.DeviceRegistryList(ctx)
	if err != nil {
		return fmt.Errorf("fetching device registry: %w", err)
	}
	device, err := resolveDevice(devices, deviceRef)
	if err != nil {
		return err
	}

	seen := make(map[string]bool, len(device.Labels)+len(resolved))
	merged := make([]string, 0, len(device.Labels)+len(resolved))
	for _, l := range device.Labels {
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

	if !flagDeviceConfirm {
		// Slices, not their %v rendering, so --json carries real arrays.
		return dryRun("set device labels").
			with("device_id", device.ID).
			withIf(deviceUserFacingName(device) != "", "device_name", deviceUserFacingName(device)).
			with("current_labels", nonNil(device.Labels)).
			with("new_labels", nonNil(merged)).
			render(w)
	}

	if err := ws.DeviceRegistryUpdate(ctx, device.ID, map[string]any{"labels": merged}); err != nil {
		return fmt.Errorf("updating device labels: %w", err)
	}

	_, _ = fmt.Fprintf(w, "%s: labels set to %v\n", device.ID, merged)
	return nil
}

type deviceRegistryContext struct {
	devices    []haapi.DeviceRegistryEntry
	areaByID   map[string]haapi.AreaEntry
	labelByID  map[string]haapi.LabelEntry
	entityByID map[string][]haapi.EntityRegistryEntry
	deviceByID map[string]haapi.DeviceRegistryEntry
}

func runDeviceLs(ctx context.Context, w io.Writer) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return fmt.Errorf("connecting to HA: %w", connErr)
	}
	defer func() { _ = ws.Close() }()

	rc, err := fetchDeviceRegistryContext(ctx, ws)
	if err != nil {
		return err
	}

	devices := filterDevices(rc.devices, rc)
	if len(devices) == 0 {
		return emitEmptyList(w, "no devices")
	}

	sort.Slice(devices, func(i, j int) bool {
		return strings.ToLower(deviceDisplayName(devices[i])) < strings.ToLower(deviceDisplayName(devices[j]))
	})

	tbl := &format.Table{
		Headers: []string{"device_id", "name", "area", "labels", "entities", "manufacturer", "model"},
		Rows:    make([][]string, len(devices)),
	}
	for i, d := range devices {
		tbl.Rows[i] = []string{
			d.ID,
			d.Name,
			deviceAreaName(d, rc),
			deviceLabelNames(d, rc),
			strconv.Itoa(len(rc.entityByID[d.ID])),
			d.Manufacturer,
			d.Model,
		}
	}

	return tbl.Render(w, format.RenderOpts{
		Top:      flagTop,
		Full:     flagFull,
		JSON:     flagJSON,
		Compact:  true,
		MoreHint: "use --full, --top N, --pattern, --area, or --label to see more",
	})
}

func runDeviceShow(ctx context.Context, w io.Writer, deviceRef string) error {
	cfg, err := config.Load(flagDir)
	if err != nil {
		return err
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if connErr := ws.Connect(ctx); connErr != nil {
		return fmt.Errorf("connecting to HA: %w", connErr)
	}
	defer func() { _ = ws.Close() }()

	rc, err := fetchDeviceRegistryContext(ctx, ws)
	if err != nil {
		return err
	}

	device, err := resolveDevice(rc.devices, deviceRef)
	if err != nil {
		return err
	}

	entities := append([]haapi.EntityRegistryEntry(nil), rc.entityByID[device.ID]...)
	sort.Slice(entities, func(i, j int) bool {
		return entities[i].EntityID < entities[j].EntityID
	})

	if flagJSON {
		result := map[string]any{
			"device_id":    device.ID,
			"name":         device.Name,
			"area":         deviceAreaName(device, rc),
			"area_id":      device.AreaID,
			"labels":       deviceLabelNameList(device, rc),
			"entity_count": len(entities),
			"manufacturer": device.Manufacturer,
			"model":        device.Model,
			"sw_version":   device.SWVersion,
			"entities":     deviceEntityRows(entities, rc),
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(result)
	}

	_, _ = fmt.Fprintf(w, "device_id: %s\n", device.ID)
	_, _ = fmt.Fprintf(w, "name: %s\n", device.Name)
	_, _ = fmt.Fprintf(w, "area: %s\n", deviceAreaName(device, rc))
	_, _ = fmt.Fprintf(w, "labels: %s\n", deviceLabelNames(device, rc))
	if device.Manufacturer != "" {
		_, _ = fmt.Fprintf(w, "manufacturer: %s\n", device.Manufacturer)
	}
	if device.Model != "" {
		_, _ = fmt.Fprintf(w, "model: %s\n", device.Model)
	}
	if device.SWVersion != "" {
		_, _ = fmt.Fprintf(w, "sw_version: %s\n", device.SWVersion)
	}
	_, _ = fmt.Fprintf(w, "entities: %d\n", len(entities))

	if len(entities) == 0 {
		return nil
	}
	_, _ = fmt.Fprintln(w)

	tbl := &format.Table{
		Headers: []string{"entity_id", "name", "area", "labels", "platform"},
		Rows:    deviceEntityRows(entities, rc),
	}
	return tbl.Render(w, format.RenderOpts{
		Top:      flagTop,
		Full:     flagFull,
		JSON:     false,
		Compact:  true,
		MoreHint: "use --full or --top N to see more entities",
	})
}

func fetchDeviceRegistryContext(ctx context.Context, ws *haapi.WSClient) (*deviceRegistryContext, error) {
	devices, err := ws.DeviceRegistryList(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching device registry: %w", err)
	}
	entities, err := ws.EntityRegistryList(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching entity registry: %w", err)
	}
	areas, err := ws.AreaRegistryList(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching areas: %w", err)
	}
	labels, err := ws.LabelRegistryList(ctx)
	if err != nil {
		return nil, fmt.Errorf("fetching labels: %w", err)
	}

	rc := &deviceRegistryContext{
		devices:    devices,
		areaByID:   make(map[string]haapi.AreaEntry, len(areas)),
		labelByID:  make(map[string]haapi.LabelEntry, len(labels)),
		entityByID: make(map[string][]haapi.EntityRegistryEntry),
		deviceByID: make(map[string]haapi.DeviceRegistryEntry, len(devices)),
	}
	for _, area := range areas {
		rc.areaByID[area.AreaID] = area
	}
	for _, label := range labels {
		rc.labelByID[label.LabelID] = label
	}
	for _, entity := range entities {
		if entity.DeviceID == "" {
			continue
		}
		rc.entityByID[entity.DeviceID] = append(rc.entityByID[entity.DeviceID], entity)
	}
	for _, d := range devices {
		rc.deviceByID[d.ID] = d
	}
	return rc, nil
}

func filterDevices(devices []haapi.DeviceRegistryEntry, rc *deviceRegistryContext) []haapi.DeviceRegistryEntry {
	result := make([]haapi.DeviceRegistryEntry, 0, len(devices))
	for _, d := range devices {
		if flagDevicePattern != "" && !deviceMatchesPattern(d, flagDevicePattern) {
			continue
		}
		if flagDeviceName != "" && !containsFold(deviceUserFacingName(d), flagDeviceName) {
			continue
		}
		if flagDeviceArea != "" && !containsFold(d.AreaID, flagDeviceArea) && !containsFold(deviceAreaName(d, rc), flagDeviceArea) {
			continue
		}
		if flagDeviceLabel != "" && !deviceHasLabel(d, rc, flagDeviceLabel) {
			continue
		}
		result = append(result, d)
	}
	return result
}

// deviceMatchesPattern matches the device id or its user-facing name,
// case-insensitively like every other filter on this command (matchPattern
// handles the folding).
//
// The name checked is deviceUserFacingName, not the raw Name: `--name` has
// honoured name_by_user since issue #72, and a --pattern that did not would be
// the same defect one flag over.
func deviceMatchesPattern(d haapi.DeviceRegistryEntry, pattern string) bool {
	return matchPattern(d.ID, pattern) || matchPattern(deviceUserFacingName(d), pattern)
}

// deviceHasLabel matches via the same matchingLabelIDs substring rule ent.go's
// filterEntitiesByLabel uses (see its doc comment in label.go), so `device ls
// --label` and `ent ls --label` agree with each other.
func deviceHasLabel(d haapi.DeviceRegistryEntry, rc *deviceRegistryContext, label string) bool {
	matchIDs := matchingLabelIDs(rc.labelByID, label)
	if len(matchIDs) == 0 {
		return false
	}
	for _, id := range d.Labels {
		if matchIDs[id] {
			return true
		}
	}
	return false
}

// errNoDeviceGiven is what resolveDevice answers a blank reference with.
var errNoDeviceGiven = errors.New("no device given (use 'device ls' to see available devices)")

func resolveDevice(devices []haapi.DeviceRegistryEntry, ref string) (haapi.DeviceRegistryEntry, error) {
	// A blank reference matches nothing. HA leaves a device's registry `name`
	// empty in ordinary cases (a user-renamed device carries the override in
	// name_by_user), so the exact-match pass below matched `""` and answered
	// `device show ''` with an arbitrary real device. The H-22 contract refuses
	// the empty string at the CLI boundary; this refuses it where the wrong
	// match was made. `resolveRegistryTarget` has had the same guard since it
	// was written — this resolver is the one that never got it.
	if strings.TrimSpace(ref) == "" {
		return haapi.DeviceRegistryEntry{}, errNoDeviceGiven
	}
	refLower := strings.ToLower(ref)
	for _, d := range devices {
		if strings.ToLower(d.ID) == refLower || strings.ToLower(d.Name) == refLower {
			return d, nil
		}
	}

	var matches []haapi.DeviceRegistryEntry
	for _, d := range devices {
		if containsFold(d.ID, ref) || containsFold(d.Name, ref) {
			matches = append(matches, d)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	if len(matches) > 1 {
		names := make([]string, len(matches))
		for i, d := range matches {
			names[i] = fmt.Sprintf("%s (%s)", deviceDisplayName(d), d.ID)
		}
		sort.Strings(names)
		return haapi.DeviceRegistryEntry{}, fmt.Errorf("device %q is ambiguous: %s", ref, strings.Join(names, ", "))
	}
	return haapi.DeviceRegistryEntry{}, fmt.Errorf("device %q not found (use 'device ls' to see available devices)", ref)
}

func deviceAreaName(d haapi.DeviceRegistryEntry, rc *deviceRegistryContext) string {
	if d.AreaID == "" {
		return ""
	}
	if area, ok := rc.areaByID[d.AreaID]; ok {
		return area.Name
	}
	return d.AreaID
}

func deviceLabelNames(d haapi.DeviceRegistryEntry, rc *deviceRegistryContext) string {
	return strings.Join(deviceLabelNameList(d, rc), ", ")
}

func deviceLabelNameList(d haapi.DeviceRegistryEntry, rc *deviceRegistryContext) []string {
	names := make([]string, 0, len(d.Labels))
	for _, labelID := range d.Labels {
		if label, ok := rc.labelByID[labelID]; ok {
			names = append(names, label.Name)
		} else {
			names = append(names, labelID)
		}
	}
	sort.Strings(names)
	return names
}

func deviceEntityRows(entities []haapi.EntityRegistryEntry, rc *deviceRegistryContext) [][]string {
	rows := make([][]string, len(entities))
	for i, e := range entities {
		rows[i] = []string{
			e.EntityID,
			firstNonEmpty(e.Name, e.OrigName),
			registryEntityAreaName(e, rc),
			registryEntityLabelNames(e, rc),
			e.Platform,
		}
	}
	return rows
}

// registryEntityAreaName is the `device show` entity-row equivalent of
// registryContext.areaName in label.go: the entity's own area wins, else it
// falls back to its (containing) device's area (H-8). Every entity passed in
// here came from rc.entityByID[device.ID], so e.DeviceID is always that
// device — but look it up via rc.deviceByID rather than assume, in case a
// future caller reuses this on an entity from elsewhere.
func registryEntityAreaName(e haapi.EntityRegistryEntry, rc *deviceRegistryContext) string {
	areaID := e.AreaID
	if areaID == "" && e.DeviceID != "" {
		areaID = rc.deviceByID[e.DeviceID].AreaID
	}
	if areaID == "" {
		return ""
	}
	if area, ok := rc.areaByID[areaID]; ok {
		return area.Name
	}
	return areaID
}

func registryEntityLabelNames(e haapi.EntityRegistryEntry, rc *deviceRegistryContext) string {
	names := make([]string, 0, len(e.Labels))
	for _, labelID := range e.Labels {
		if label, ok := rc.labelByID[labelID]; ok {
			names = append(names, label.Name)
		} else {
			names = append(names, labelID)
		}
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func deviceDisplayName(d haapi.DeviceRegistryEntry) string {
	return firstNonEmpty(d.Name, d.ID)
}

// deviceUserFacingName returns the name a user searches for and sees in the
// HA UI: the custom name_by_user when set, falling back to the registry name.
func deviceUserFacingName(d haapi.DeviceRegistryEntry) string {
	return firstNonEmpty(d.NameByUser, d.Name)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
