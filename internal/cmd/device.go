package cmd

import (
	"context"
	"encoding/json"
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

var deviceCmd = &cobra.Command{
	Use:   "device",
	Short: "Browse and inspect devices",
	Long:  "List and inspect Home Assistant devices and their entity registry entries.",
}

var deviceLsCmd = &cobra.Command{
	Use:   "ls",
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
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runDeviceShow(cmd.Context(), cmd.OutOrStdout(), args[0])
	},
}

func init() {
	deviceLsCmd.Flags().StringVar(&flagDevicePattern, "pattern", "", "filter by device ID/name (substring or glob)")
	deviceLsCmd.Flags().StringVar(&flagDeviceName, "name", "", "filter by device name substring")
	deviceLsCmd.Flags().StringVar(&flagDeviceArea, "area", "", "filter by area/room name or ID substring")
	deviceLsCmd.Flags().StringVar(&flagDeviceLabel, "label", "", "filter by label name or ID substring")
	deviceCmd.AddCommand(deviceLsCmd, deviceShowCmd)
	rootCmd.AddCommand(deviceCmd)
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

// deviceMatchesPattern matches case-sensitively, like ent ls --pattern and
// docs/manual.md ("substring or glob"); device ls used to be the sole
// case-insensitive outlier among the --pattern-supporting commands.
func deviceMatchesPattern(d haapi.DeviceRegistryEntry, pattern string) bool {
	return matchPattern(d.ID, pattern) || matchPattern(d.Name, pattern)
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

func resolveDevice(devices []haapi.DeviceRegistryEntry, ref string) (haapi.DeviceRegistryEntry, error) {
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
