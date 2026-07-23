package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
)

func TestMatchPattern_ExactMatch(t *testing.T) {
	if !matchPattern("sensor.temperature", "sensor.temperature") {
		t.Error("exact match should return true")
	}
}

func TestMatchPattern_WildcardSuffix(t *testing.T) {
	if !matchPattern("sensor.wp_vorlauf", "sensor.wp_*") {
		t.Error("sensor.wp_vorlauf should match sensor.wp_*")
	}
}

func TestMatchPattern_WildcardPrefix(t *testing.T) {
	if !matchPattern("sensor.wp_vorlauf", "*vorlauf") {
		t.Error("sensor.wp_vorlauf should match *vorlauf")
	}
}

func TestMatchPattern_NoMatch(t *testing.T) {
	if matchPattern("binary_sensor.door", "sensor.*") {
		t.Error("binary_sensor.door should not match sensor.*")
	}
}

func TestMatchPattern_AllStar(t *testing.T) {
	if !matchPattern("anything.at_all", "*") {
		t.Error("* should match everything")
	}
}

func TestMatchPattern_QuestionMark(t *testing.T) {
	if !matchPattern("sensor.a1", "sensor.?1") {
		t.Error("sensor.a1 should match sensor.?1")
	}
}

func TestMatchPattern_EmptyPattern(t *testing.T) {
	if matchPattern("sensor.x", "") {
		t.Error("empty pattern should not match non-empty string")
	}
	if !matchPattern("", "") {
		t.Error("empty pattern should match empty string")
	}
}

func TestMatchPattern_Substring(t *testing.T) {
	if !matchPattern("ess_balkon_sende_bms_daten", "ess") {
		t.Error("bare substring 'ess' should match")
	}
}

func TestMatchPattern_SubstringMiddle(t *testing.T) {
	if !matchPattern("victron_ess_keep_alive", "ess") {
		t.Error("bare substring 'ess' should match in the middle")
	}
}

func TestMatchPattern_SubstringNoMatch(t *testing.T) {
	if matchPattern("automation.climate_schedule", "victron") {
		t.Error("'victron' should not match 'climate_schedule'")
	}
}

func TestMatchPattern_GlobStillWorks(t *testing.T) {
	if !matchPattern("sensor.wp_vorlauf", "*wp_*") {
		t.Error("glob *wp_* should still work")
	}
}

func TestTruncateState_Short(t *testing.T) {
	if got := truncateState("on"); got != "on" {
		t.Errorf("truncateState('on') = %q, want 'on'", got)
	}
}

func TestTruncateState_Long(t *testing.T) {
	long := "this is a very long state value that exceeds twenty characters"
	got := truncateState(long)
	if len(got) > 20 {
		t.Errorf("truncateState result length = %d, want <= 20", len(got))
	}
}

func TestParseEntityDomain(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"sensor.temperature", "sensor"},
		{"binary_sensor.door", "binary_sensor"},
		{"nodomain", "nodomain"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := parseEntityDomain(tt.input); got != tt.want {
			t.Errorf("parseEntityDomain(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestParseHistoryResponse_Valid(t *testing.T) {
	data := []byte(`[[
		{"entity_id":"sensor.temp","state":"21.5","last_changed":"2026-01-01T10:00:00+00:00"},
		{"entity_id":"sensor.temp","state":"22.0","last_changed":"2026-01-01T11:00:00+00:00"},
		{"entity_id":"sensor.temp","state":"unavailable","last_changed":"2026-01-01T12:00:00+00:00"},
		{"entity_id":"sensor.temp","state":"21.8","last_changed":"2026-01-01T13:00:00+00:00"}
	]]`)

	points, err := parseHistoryResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// "unavailable" should be skipped
	if len(points) != 3 {
		t.Fatalf("expected 3 numeric points, got %d", len(points))
	}
	if points[0].Value != 21.5 {
		t.Errorf("first value = %.1f, want 21.5", points[0].Value)
	}
}

func TestParseHistoryResponse_Empty(t *testing.T) {
	data := []byte(`[]`)
	points, err := parseHistoryResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("expected 0 points, got %d", len(points))
	}
}

func TestParseHistoryResponse_NonNumeric(t *testing.T) {
	data := []byte(`[[
		{"entity_id":"binary_sensor.door","state":"on","last_changed":"2026-01-01T10:00:00+00:00"},
		{"entity_id":"binary_sensor.door","state":"off","last_changed":"2026-01-01T11:00:00+00:00"}
	]]`)

	points, err := parseHistoryResponse(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("expected 0 numeric points, got %d", len(points))
	}
}

func TestFilterEntitiesByPattern(t *testing.T) {
	states := []entityState{
		{EntityID: "sensor.wp_vorlauf"},
		{EntityID: "sensor.wp_ruecklauf"},
		{EntityID: "sensor.temperature"},
		{EntityID: "binary_sensor.door"},
	}

	result := filterEntitiesByPattern(states, "sensor.wp_*")
	if len(result) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(result))
	}
}

func TestFilterEntitiesByDomain(t *testing.T) {
	states := []entityState{
		{EntityID: "sensor.wp_vorlauf"},
		{EntityID: "sensor.wp_ruecklauf"},
		{EntityID: "sensor.temperature"},
		{EntityID: "binary_sensor.door"},
		{EntityID: "light.kitchen"},
	}

	result := filterEntitiesByDomain(states, "sensor")
	if len(result) != 3 {
		t.Fatalf("expected 3 sensor entities, got %d", len(result))
	}
	for _, s := range result {
		if parseEntityDomain(s.EntityID) != "sensor" {
			t.Errorf("non-sensor entity in result: %s", s.EntityID)
		}
	}
}

func TestFilterEntitiesByDomain_BinarySensor(t *testing.T) {
	states := []entityState{
		{EntityID: "sensor.temp"},
		{EntityID: "binary_sensor.door"},
		{EntityID: "binary_sensor.window"},
	}

	result := filterEntitiesByDomain(states, "binary_sensor")
	if len(result) != 2 {
		t.Fatalf("expected 2 binary_sensor entities, got %d", len(result))
	}
}

func TestFilterEntitiesByDomain_NoMatch(t *testing.T) {
	states := []entityState{
		{EntityID: "sensor.temp"},
	}

	result := filterEntitiesByDomain(states, "light")
	if len(result) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(result))
	}
}

func TestParseStateTimeline_BinarySensor(t *testing.T) {
	data := []byte(`[[
		{"entity_id":"binary_sensor.door","state":"off","last_changed":"2026-01-01T10:00:00+00:00"},
		{"entity_id":"binary_sensor.door","state":"on","last_changed":"2026-01-01T10:05:00+00:00"},
		{"entity_id":"binary_sensor.door","state":"off","last_changed":"2026-01-01T10:10:00+00:00"}
	]]`)

	now := time.Date(2026, 1, 1, 10, 20, 0, 0, time.UTC)
	changes, err := parseStateTimeline(data, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 3 {
		t.Fatalf("expected 3 state changes, got %d", len(changes))
	}
	if changes[0].State != "off" {
		t.Errorf("first state = %q, want %q", changes[0].State, "off")
	}
	if changes[1].State != "on" {
		t.Errorf("second state = %q, want %q", changes[1].State, "on")
	}
	// First duration: 5 minutes (until next change)
	if changes[0].Duration != 5*time.Minute {
		t.Errorf("first duration = %v, want 5m0s", changes[0].Duration)
	}
	// Last duration: 10 minutes (until "now")
	if changes[2].Duration != 10*time.Minute {
		t.Errorf("last duration = %v, want 10m0s", changes[2].Duration)
	}
}

func TestParseStateTimeline_FiltersUnavailable(t *testing.T) {
	data := []byte(`[[
		{"entity_id":"binary_sensor.door","state":"unavailable","last_changed":"2026-01-01T10:05:00+00:00"},
		{"entity_id":"binary_sensor.door","state":"off","last_changed":"2026-01-01T10:00:00+00:00"},
		{"entity_id":"binary_sensor.door","state":"on","last_changed":"2026-01-01T10:10:00+00:00"}
	]]`)

	now := time.Date(2026, 1, 1, 10, 20, 0, 0, time.UTC)
	changes, err := parseStateTimeline(data, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("expected 2 state changes (unavailable filtered), got %d", len(changes))
	}
}

func TestParseAttrHistoryResponse_Brightness(t *testing.T) {
	data := []byte(`[[
		{"entity_id":"light.kitchen","state":"on","last_changed":"2026-01-01T10:00:00+00:00","attributes":{"brightness":128}},
		{"entity_id":"light.kitchen","state":"on","last_changed":"2026-01-01T11:00:00+00:00","attributes":{"brightness":255}},
		{"entity_id":"light.kitchen","state":"off","last_changed":"2026-01-01T12:00:00+00:00","attributes":{}}
	]]`)

	points, err := parseAttrHistoryResponse(data, "brightness")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 2 {
		t.Fatalf("expected 2 brightness points, got %d", len(points))
	}
	if points[0].Value != 128 {
		t.Errorf("first brightness = %.0f, want 128", points[0].Value)
	}
	if points[1].Value != 255 {
		t.Errorf("second brightness = %.0f, want 255", points[1].Value)
	}
}

func TestParseAttrHistoryResponse_MissingAttr(t *testing.T) {
	data := []byte(`[[
		{"entity_id":"light.kitchen","state":"on","last_changed":"2026-01-01T10:00:00+00:00","attributes":{"color_temp":300}},
		{"entity_id":"light.kitchen","state":"off","last_changed":"2026-01-01T11:00:00+00:00","attributes":{}}
	]]`)

	points, err := parseAttrHistoryResponse(data, "brightness")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("expected 0 brightness points, got %d", len(points))
	}
}

func TestParseAttrHistoryResponse_Empty(t *testing.T) {
	data := []byte(`[]`)
	points, err := parseAttrHistoryResponse(data, "brightness")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 0 {
		t.Fatalf("expected 0 points, got %d", len(points))
	}
}

func TestParseAttrHistoryResponse_StringNumber(t *testing.T) {
	data := []byte(`[[
		{"entity_id":"sensor.x","state":"on","last_changed":"2026-01-01T10:00:00+00:00","attributes":{"power":"42.5"}}
	]]`)

	points, err := parseAttrHistoryResponse(data, "power")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(points) != 1 {
		t.Fatalf("expected 1 point, got %d", len(points))
	}
	if points[0].Value != 42.5 {
		t.Errorf("power = %.1f, want 42.5", points[0].Value)
	}
}

func TestToFloat64(t *testing.T) {
	tests := []struct {
		input any
		want  float64
		err   bool
	}{
		{42.5, 42.5, false},
		{"123.4", 123.4, false},
		{"not_a_number", 0, true},
		{true, 0, true},
	}
	for _, tt := range tests {
		got, err := toFloat64(tt.input)
		if (err != nil) != tt.err {
			t.Errorf("toFloat64(%v) error = %v, wantErr = %v", tt.input, err, tt.err)
			continue
		}
		if !tt.err && got != tt.want {
			t.Errorf("toFloat64(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestFilterEntitiesByArea(t *testing.T) {
	states := []entityState{
		{EntityID: "light.kitchen"},
		{EntityID: "sensor.temp"},
		{EntityID: "light.bedroom"},
	}
	rc := &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			"light.kitchen": {EntityID: "light.kitchen", AreaID: "kitchen"},
			"sensor.temp":   {EntityID: "sensor.temp", AreaID: "kitchen"},
			"light.bedroom": {EntityID: "light.bedroom", AreaID: "bedroom"},
		},
		areaByID: map[string]haapi.AreaEntry{
			"kitchen": {AreaID: "kitchen", Name: "Kitchen"},
			"bedroom": {AreaID: "bedroom", Name: "Bedroom"},
		},
		labelByID: map[string]haapi.LabelEntry{},
		floorByID: map[string]haapi.FloorEntry{},
	}

	result := filterEntitiesByArea(states, rc, "kitchen")
	if len(result) != 2 {
		t.Fatalf("expected 2 kitchen entities, got %d", len(result))
	}
}

func TestFilterEntitiesByLabel(t *testing.T) {
	states := []entityState{
		{EntityID: "light.kitchen"},
		{EntityID: "sensor.power"},
	}
	rc := &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			"light.kitchen": {EntityID: "light.kitchen", Labels: []string{"lighting"}},
			"sensor.power":  {EntityID: "sensor.power", Labels: []string{"energy"}},
		},
		areaByID: map[string]haapi.AreaEntry{},
		labelByID: map[string]haapi.LabelEntry{
			"lighting": {LabelID: "lighting", Name: "Lighting"},
			"energy":   {LabelID: "energy", Name: "Energy"},
		},
		floorByID: map[string]haapi.FloorEntry{},
	}

	result := filterEntitiesByLabel(states, rc, "energy")
	if len(result) != 1 {
		t.Fatalf("expected 1 energy-labeled entity, got %d", len(result))
	}
	if result[0].EntityID != "sensor.power" {
		t.Errorf("expected sensor.power, got %s", result[0].EntityID)
	}
}

func TestRegistryContext_AreaName(t *testing.T) {
	rc := &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			"light.x": {EntityID: "light.x", AreaID: "kitchen"},
			"light.y": {EntityID: "light.y"},
		},
		areaByID: map[string]haapi.AreaEntry{
			"kitchen": {AreaID: "kitchen", Name: "Kitchen"},
		},
		labelByID: map[string]haapi.LabelEntry{},
		floorByID: map[string]haapi.FloorEntry{},
	}

	if got := rc.areaName("light.x"); got != "Kitchen" {
		t.Errorf("areaName(light.x) = %q, want Kitchen", got)
	}
	if got := rc.areaName("light.y"); got != "" {
		t.Errorf("areaName(light.y) = %q, want empty", got)
	}
	if got := rc.areaName("light.z"); got != "" {
		t.Errorf("areaName(light.z) = %q, want empty", got)
	}
}

func TestRegistryContext_LabelNames(t *testing.T) {
	rc := &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			"light.x": {EntityID: "light.x", Labels: []string{"energy", "lighting"}},
			"light.y": {EntityID: "light.y"},
		},
		areaByID: map[string]haapi.AreaEntry{},
		labelByID: map[string]haapi.LabelEntry{
			"energy":   {LabelID: "energy", Name: "Energy"},
			"lighting": {LabelID: "lighting", Name: "Lighting"},
		},
		floorByID: map[string]haapi.FloorEntry{},
	}

	got := rc.labelNames("light.x")
	if got != "Energy, Lighting" {
		t.Errorf("labelNames(light.x) = %q, want 'Energy, Lighting'", got)
	}
	if rc.labelNames("light.y") != "" {
		t.Errorf("labelNames(light.y) should be empty")
	}
}

// ---------------------------------------------------------------------------
// H-8: an entity's effective area falls back to its device's area.
// ---------------------------------------------------------------------------

func registryContextForH8() *registryContext {
	return &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			// No AreaID of its own — must inherit from dev1.
			"sensor.inherited": {EntityID: "sensor.inherited", DeviceID: "dev1"},
			// Own AreaID set — must win over dev1's area.
			"sensor.override": {EntityID: "sensor.override", DeviceID: "dev1", AreaID: "bedroom"},
			// No device at all — stays arealess.
			"sensor.orphan": {EntityID: "sensor.orphan"},
			// Device exists but the device itself has no area.
			"sensor.unplaced_device": {EntityID: "sensor.unplaced_device", DeviceID: "dev2"},
		},
		areaByID: map[string]haapi.AreaEntry{
			"kitchen": {AreaID: "kitchen", Name: "Kitchen"},
			"bedroom": {AreaID: "bedroom", Name: "Bedroom"},
		},
		labelByID: map[string]haapi.LabelEntry{},
		floorByID: map[string]haapi.FloorEntry{},
		deviceByID: map[string]haapi.DeviceRegistryEntry{
			"dev1": {ID: "dev1", Name: "Dev One", AreaID: "kitchen"},
			"dev2": {ID: "dev2", Name: "Dev Two"},
		},
	}
}

func TestRegistryContext_AreaName_DeviceFallback(t *testing.T) {
	rc := registryContextForH8()

	if got := rc.areaName("sensor.inherited"); got != "Kitchen" {
		t.Errorf("areaName(sensor.inherited) = %q, want Kitchen (inherited from dev1)", got)
	}
}

// TestRegistryContext_AreaName_OwnAreaWinsOverDevice guards the precedence
// direction: adding the fallback must not make the device always win.
func TestRegistryContext_AreaName_OwnAreaWinsOverDevice(t *testing.T) {
	rc := registryContextForH8()

	if got := rc.areaName("sensor.override"); got != "Bedroom" {
		t.Errorf("areaName(sensor.override) = %q, want Bedroom (entity's own area beats its device's)", got)
	}
}

func TestRegistryContext_AreaName_NoDeviceStaysEmpty(t *testing.T) {
	rc := registryContextForH8()

	if got := rc.areaName("sensor.orphan"); got != "" {
		t.Errorf("areaName(sensor.orphan) = %q, want empty (no area, no device)", got)
	}
	if got := rc.areaName("sensor.unplaced_device"); got != "" {
		t.Errorf("areaName(sensor.unplaced_device) = %q, want empty (device itself has no area)", got)
	}
}

// TestRegistryContext_LabelNames_NoDeviceFallback pins H-8's other half:
// unlike area, labels must NOT inherit from the device — confirmed against
// real HA (see label.go's labelNames doc comment). A device-only label must
// not appear on its entities.
func TestRegistryContext_LabelNames_NoDeviceFallback(t *testing.T) {
	rc := &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			"sensor.inherited": {EntityID: "sensor.inherited", DeviceID: "dev1"},
		},
		areaByID: map[string]haapi.AreaEntry{},
		labelByID: map[string]haapi.LabelEntry{
			"device_only": {LabelID: "device_only", Name: "Device Only"},
		},
		floorByID: map[string]haapi.FloorEntry{},
		deviceByID: map[string]haapi.DeviceRegistryEntry{
			"dev1": {ID: "dev1", Labels: []string{"device_only"}},
		},
	}

	if got := rc.labelNames("sensor.inherited"); got != "" {
		t.Errorf("labelNames(sensor.inherited) = %q, want empty (labels do not inherit from device)", got)
	}
}

func TestFilterEntitiesByArea_DeviceFallback(t *testing.T) {
	states := []entityState{
		{EntityID: "sensor.inherited"},
		{EntityID: "sensor.orphan"},
	}
	rc := registryContextForH8()

	result := filterEntitiesByArea(states, rc, "kitchen")
	if len(result) != 1 || result[0].EntityID != "sensor.inherited" {
		t.Fatalf("filterEntitiesByArea(kitchen) = %+v, want just sensor.inherited", result)
	}
}

// TestFindAreaNeighbors_UsesDeviceFallback is the site-3 unit regression:
// findAreaNeighbors used to read ent.AreaID directly, so an entity whose area
// only exists via its device could never be found as anyone's source, and
// never listed as anyone's neighbor either.
func TestFindAreaNeighbors_UsesDeviceFallback(t *testing.T) {
	rc := &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			"sensor.a": {EntityID: "sensor.a", DeviceID: "dev1"},
			"sensor.b": {EntityID: "sensor.b", DeviceID: "dev1"},
			// Different domain, same area (via the same device): an area
			// neighbour like any other — see TestFindAreaNeighbors_Found.
			"light.c": {EntityID: "light.c", DeviceID: "dev1"},
			// Different device, different area: must NOT show up.
			"sensor.d": {EntityID: "sensor.d", AreaID: "bedroom"},
		},
		areaByID: map[string]haapi.AreaEntry{
			"kitchen": {AreaID: "kitchen", Name: "Kitchen"},
			"bedroom": {AreaID: "bedroom", Name: "Bedroom"},
		},
		labelByID: map[string]haapi.LabelEntry{},
		floorByID: map[string]haapi.FloorEntry{},
		deviceByID: map[string]haapi.DeviceRegistryEntry{
			"dev1": {ID: "dev1", AreaID: "kitchen"},
		},
	}

	got := findAreaNeighbors(rc, "sensor.a")
	seen := make(map[string]string, len(got))
	for _, r := range got {
		seen[r.entityID] = r.relationship
	}
	if len(got) != 2 || seen["sensor.b"] != "area-neighbor" || seen["light.c"] != "area-neighbor" {
		t.Fatalf("findAreaNeighbors(sensor.a) = %+v, want area-neighbor rows for sensor.b and light.c "+
			"(both inherit dev1's area); sensor.d is in another area and must not appear", got)
	}
}

// TestFilterEntitiesByLabel_MatchesPerLabelNotJoinedString guards the
// cross-label false-positive matchingLabelIDs replaced: the old filter
// substring-matched an entity's *joined* "name1, name2" display string, so a
// query straddling the ", " separator between two unrelated labels could
// match. Here "y, z" is not a substring of any single label's id or name, so
// it must match nothing even though it IS a substring of the joined display
// "Energy, Zzz".
func TestFilterEntitiesByLabel_MatchesPerLabelNotJoinedString(t *testing.T) {
	states := []entityState{{EntityID: "sensor.multi"}}
	rc := &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			"sensor.multi": {EntityID: "sensor.multi", Labels: []string{"energy", "zzz"}},
		},
		areaByID: map[string]haapi.AreaEntry{},
		labelByID: map[string]haapi.LabelEntry{
			"energy": {LabelID: "energy", Name: "Energy"},
			"zzz":    {LabelID: "zzz", Name: "Zzz"},
		},
		floorByID: map[string]haapi.FloorEntry{},
	}

	if got := filterEntitiesByLabel(states, rc, "y, z"); len(got) != 0 {
		t.Errorf("filterEntitiesByLabel(%q) = %+v, want none (matches only the joined display string, not any real label)", "y, z", got)
	}
	// Sanity: a real substring of one label's name still matches.
	if got := filterEntitiesByLabel(states, rc, "energ"); len(got) != 1 {
		t.Errorf("filterEntitiesByLabel(energ) = %+v, want sensor.multi", got)
	}
}

// TestLabelExistsInRegistry_AgreesWithFilter guards the other bug
// matchingLabelIDs fixed: labelExistsInRegistry (the ent ls --label
// existence pre-check) and filterEntitiesByLabel (the actual filter) used to
// apply different match rules, so a query could pass the pre-check and then
// match nothing (or fail the pre-check despite the filter being willing to
// match). Both must now agree for the same query.
func TestLabelExistsInRegistry_AgreesWithFilter(t *testing.T) {
	states := []entityState{{EntityID: "sensor.power"}}
	rc := &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			"sensor.power": {EntityID: "sensor.power", Labels: []string{"energy_monitoring"}},
		},
		areaByID: map[string]haapi.AreaEntry{},
		labelByID: map[string]haapi.LabelEntry{
			"energy_monitoring": {LabelID: "energy_monitoring", Name: "Energy Monitoring"},
		},
		floorByID: map[string]haapi.FloorEntry{},
	}

	exists := labelExistsInRegistry(rc, "energy")
	matched := filterEntitiesByLabel(states, rc, "energy")
	if exists != (len(matched) > 0) {
		t.Errorf("labelExistsInRegistry(energy) = %v but filterEntitiesByLabel(energy) matched %d entities; they must agree",
			exists, len(matched))
	}
}

func TestParseStateTimeline_Empty(t *testing.T) {
	data := []byte(`[]`)
	now := time.Now()
	changes, err := parseStateTimeline(data, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 0 {
		t.Fatalf("expected 0 changes, got %d", len(changes))
	}
}

func TestFormatDuration(t *testing.T) {
	tests := []struct {
		want string
		d    time.Duration
	}{
		{"30s", 30 * time.Second},
		{"5m0s", 5 * time.Minute},
		{"1h30m", 90 * time.Minute},
		{"2h00m", 2 * time.Hour},
	}
	for _, tt := range tests {
		got := formatDuration(tt.d)
		if got != tt.want {
			t.Errorf("formatDuration(%v) = %q, want %q", tt.d, got, tt.want)
		}
	}
}

func TestFormatAttrList(t *testing.T) {
	got := formatAttrList([]any{})
	if got != "[]" {
		t.Errorf("formatAttrList(empty) = %q, want %q", got, "[]")
	}

	got = formatAttrList([]any{"foo"})
	if got != "[foo]" {
		t.Errorf("formatAttrList(single) = %q, want %q", got, "[foo]")
	}

	got = formatAttrList([]any{"a", "b", "c"})
	if got != "[a, b, c]" {
		t.Errorf("formatAttrList(multiple) = %q, want %q", got, "[a, b, c]")
	}

	got = formatAttrList([]any{"text", 42, true})
	if got != "[text, 42, true]" {
		t.Errorf("formatAttrList(mixed) = %q, want %q", got, "[text, 42, true]")
	}
}

func TestToFloat64_JSONNumber(t *testing.T) {
	jn := any(json.Number("99.9"))
	got, err := toFloat64(jn)
	if err != nil {
		t.Fatalf("toFloat64(json.Number) error = %v", err)
	}
	if got != 99.9 {
		t.Errorf("toFloat64(json.Number(99.9)) = %v, want 99.9", got)
	}
}

func TestParseHistoryResponse_InvalidJSON(t *testing.T) {
	_, err := parseHistoryResponse([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestParseStateTimeline_InvalidJSON(t *testing.T) {
	_, err := parseStateTimeline([]byte(`not json`), time.Now())
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestParseStateTimeline_EmptyInnerArray(t *testing.T) {
	// Outer array present but inner is empty → nil, nil
	changes, err := parseStateTimeline([]byte(`[[]]`), time.Now())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if changes != nil {
		t.Errorf("expected nil changes for empty inner array, got %v", changes)
	}
}

func TestParseAttrHistoryResponse_InvalidJSON(t *testing.T) {
	_, err := parseAttrHistoryResponse([]byte(`not json`), "brightness")
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestEntityState_DecodesContext(t *testing.T) {
	// Captured /api/states/<id> shape: HA core State._as_dict in 2026.4.4
	// (homeassistant/core.py). The public REST docs omit "context" but the
	// source returns it on every state.
	data := []byte(`{
		"entity_id": "light.kitchen",
		"state": "on",
		"attributes": {"friendly_name": "Kitchen Light"},
		"last_changed": "2026-05-21T10:00:00+00:00",
		"last_updated": "2026-05-21T10:00:00+00:00",
		"context": {
			"id": "01HXYZABCDEF",
			"parent_id": null,
			"user_id": "ae7c1d92b8f4429fae3e08d8a9b1c2d4"
		}
	}`)

	var ent entityState
	if err := json.Unmarshal(data, &ent); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}

	if ent.Context.ID != "01HXYZABCDEF" {
		t.Errorf("Context.ID = %q, want 01HXYZABCDEF", ent.Context.ID)
	}
	if ent.Context.ParentID != "" {
		t.Errorf("Context.ParentID = %q, want empty (null in JSON)", ent.Context.ParentID)
	}
	if ent.Context.UserID != "ae7c1d92b8f4429fae3e08d8a9b1c2d4" {
		t.Errorf("Context.UserID = %q, want ae7c1d92b8f4429fae3e08d8a9b1c2d4", ent.Context.UserID)
	}
}

func TestEntityState_ContextOptional(t *testing.T) {
	// Some older HA versions or oddball entities may omit "context".
	// Decode must not fail; Context should be the zero value.
	data := []byte(`{
		"entity_id": "light.x",
		"state": "off",
		"attributes": {},
		"last_changed": "2026-05-21T10:00:00+00:00",
		"last_updated": "2026-05-21T10:00:00+00:00"
	}`)

	var ent entityState
	if err := json.Unmarshal(data, &ent); err != nil {
		t.Fatalf("unmarshal failed: %v", err)
	}
	if ent.Context.ID != "" || ent.Context.UserID != "" || ent.Context.ParentID != "" {
		t.Errorf("expected zero Context, got %+v", ent.Context)
	}
}

const janUUID = "ae7c1d92b8f4429fae3e08d8a9b1c2d4"

// stateJSON returns the JSON body /api/states/light.kitchen would emit for a
// light in state "on" with the given user UUID as ContextUserID (empty for
// a system-driven change).
func stateJSON(userID string) string {
	ctx := `{"id":"01HXYZ","parent_id":null,"user_id":` + jsonString(userID) + `}`
	return `{"entity_id":"light.kitchen","state":"on",` +
		`"attributes":{"friendly_name":"Kitchen Light"},` +
		`"last_changed":"2026-05-21T10:00:00+00:00","last_updated":"2026-05-21T10:00:00+00:00",` +
		`"context":` + ctx + `}`
}

func jsonString(s string) string {
	if s == "" {
		return "null"
	}
	return `"` + s + `"`
}

// entShowFixture wires up a cmdTestServer that serves the minimal set of
// endpoints runEntShow touches for light.kitchen: REST /api/states/light.kitchen
// + WS registries + user list.
func entShowFixture(t *testing.T, body string, users []map[string]any) *cmdTestServer {
	t.Helper()
	ts := startCmdServer(t, map[string]any{
		"config/entity_registry/list": []any{},
		"config/area_registry/list":   []any{},
		"config/label_registry/list":  []any{},
		"config/floor_registry/list":  []any{},
		"config/auth/list":            users,
	}, map[string]http.HandlerFunc{
		"/api/states/light.kitchen": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(body))
		},
	})
	return ts
}

func TestRunEntShow_ChangedBy_KnownUser(t *testing.T) {
	ts := entShowFixture(t,
		stateJSON(janUUID),
		[]map[string]any{{"id": janUUID, "name": "Jan", "is_owner": true}},
	)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runEntShow(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntShow: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "changed_by:") {
		t.Fatalf("output missing 'changed_by:' line:\n%s", out)
	}
	if !strings.Contains(out, "User Jan") {
		t.Errorf("output missing 'User Jan' attribution:\n%s", out)
	}
}

func TestRunEntShow_ChangedBy_UnknownUser_UUIDFallback(t *testing.T) {
	ts := entShowFixture(t,
		stateJSON(janUUID),
		[]map[string]any{},
	)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runEntShow(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntShow: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "User ae7c1d92…") {
		t.Errorf("output missing UUID-truncated fallback:\n%s", out)
	}
}

func TestRunEntShow_ChangedBy_NoUserID(t *testing.T) {
	// State has no user_id (automation- or integration-driven). Since ent show
	// does not query the logbook for full attribution (that's `ent who`),
	// the line should still appear with the "Home Assistant" fallback.
	ts := entShowFixture(t,
		stateJSON(""),
		[]map[string]any{},
	)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runEntShow(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntShow: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "changed_by:") || !strings.Contains(out, "Home Assistant") {
		t.Errorf("expected 'changed_by: Home Assistant' line:\n%s", out)
	}
}

func TestRunEntShow_JSON_PreservesContext(t *testing.T) {
	ts := entShowFixture(t,
		stateJSON(janUUID),
		[]map[string]any{{"id": janUUID, "name": "Jan"}},
	)
	withFlagDir(t, ts.dir)

	oldJSON := flagJSON
	flagJSON = true
	defer func() { flagJSON = oldJSON }()

	var buf bytes.Buffer
	if err := runEntShow(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntShow JSON: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, buf.String())
	}
	ctx, ok := got["context"].(map[string]any)
	if !ok {
		t.Fatalf("JSON output missing 'context' object: %v", got)
	}
	if ctx["user_id"] != janUUID {
		t.Errorf("context.user_id = %v, want %s", ctx["user_id"], janUUID)
	}
}

func TestDomainNotFoundHint(t *testing.T) {
	for _, d := range []string{"helper", "helpers"} {
		if got := domainNotFoundHint(d); !strings.Contains(got, "hactl helper ls") {
			t.Errorf("domainNotFoundHint(%q) must redirect to helper ls, got %q", d, got)
		}
	}
	got := domainNotFoundHint("sonsor")
	if !strings.Contains(got, `"sonsor"`) || !strings.Contains(got, "verify the domain") {
		t.Errorf("generic hint must name the domain and ask for verification, got %q", got)
	}
}

// --- #54: restored / "ghost" entity surfacing ---

func TestIsRestoredAttr(t *testing.T) {
	cases := []struct {
		name  string
		attrs map[string]any
		want  bool
	}{
		{"restored true", map[string]any{"restored": true}, true},
		{"restored false", map[string]any{"restored": false}, false},
		{"absent", map[string]any{"friendly_name": "x"}, false},
		{"wrong type", map[string]any{"restored": "true"}, false},
		{"nil map", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRestoredAttr(tc.attrs); got != tc.want {
				t.Errorf("isRestoredAttr(%v) = %v, want %v", tc.attrs, got, tc.want)
			}
		})
	}
}

func TestFilterEntitiesByRestored(t *testing.T) {
	states := []entityState{
		{EntityID: "automation.live", Attributes: map[string]any{}},
		{EntityID: "automation.ghost", Attributes: map[string]any{"restored": true}},
		{EntityID: "sensor.also_ghost", Attributes: map[string]any{"restored": true}},
		{EntityID: "light.on", Attributes: map[string]any{"restored": false}},
	}
	result := filterEntitiesByRestored(states)
	if len(result) != 2 {
		t.Fatalf("expected 2 restored, got %d", len(result))
	}
	for _, s := range result {
		if !isRestoredAttr(s.Attributes) {
			t.Errorf("non-restored entity %q leaked through filter", s.EntityID)
		}
	}
}

func TestBoolCell(t *testing.T) {
	if got := boolCell(true); got != "yes" {
		t.Errorf("boolCell(true) = %q, want %q", got, "yes")
	}
	if got := boolCell(false); got != "" {
		t.Errorf("boolCell(false) = %q, want empty", got)
	}
}

func TestRunEntShow_Restored_SurfacesGhost(t *testing.T) {
	body := `{"entity_id":"light.kitchen","state":"unavailable",` +
		`"attributes":{"friendly_name":"Kitchen Light","restored":true},` +
		`"last_changed":"2026-05-21T10:00:00+00:00","last_updated":"2026-05-21T10:00:00+00:00",` +
		`"context":{"id":"01HXYZ","parent_id":null,"user_id":null}}`
	ts := entShowFixture(t, body, []map[string]any{})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runEntShow(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntShow: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "restored:     true") {
		t.Errorf("expected a prominent 'restored:     true' line for a ghost entity:\n%s", out)
	}
	if !strings.Contains(out, "ghost") {
		t.Errorf("restored line should explain the ghost/nothing-to-repair meaning:\n%s", out)
	}
}

func TestRunEntShow_NotRestored_NoGhostLine(t *testing.T) {
	ts := entShowFixture(t, stateJSON(""), []map[string]any{})
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runEntShow(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntShow: %v", err)
	}
	if strings.Contains(buf.String(), "restored:") {
		t.Errorf("live entity must not print a 'restored:' line:\n%s", buf.String())
	}
}

// TestRunEntShow_JSON_IncludesTableFields covers the --json completeness gap:
// the human path (above) computes name/unit/area/labels/changed_by, but --json
// used to encode only the raw state struct and omit all of them.
func TestRunEntShow_JSON_IncludesTableFields(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "light.kitchen", "area_id": "kitchen", "labels": []string{"lighting"}},
		},
		"config/area_registry/list": []map[string]any{
			{"area_id": "kitchen", "name": "Kitchen"},
		},
		"config/label_registry/list": []map[string]any{
			{"label_id": "lighting", "name": "Lighting"},
		},
		"config/floor_registry/list": []any{},
		"config/auth/list":           []map[string]any{{"id": janUUID, "name": "Jan"}},
	}, map[string]http.HandlerFunc{
		"/api/states/light.kitchen": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(stateJSON(janUUID)))
		},
	})
	withFlagDir(t, ts.dir)

	oldJSON := flagJSON
	flagJSON = true
	defer func() { flagJSON = oldJSON }()

	var buf bytes.Buffer
	if err := runEntShow(context.Background(), &buf, "light.kitchen"); err != nil {
		t.Fatalf("runEntShow --json: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output not valid JSON: %v\n%s", err, buf.String())
	}
	for _, field := range []string{"entity_id", "state", "name", "area", "labels", "changed_by"} {
		if _, ok := got[field]; !ok {
			t.Errorf("ent show --json missing field %q; got %v", field, got)
		}
	}
	if got["area"] != "Kitchen" {
		t.Errorf("area = %v, want Kitchen", got["area"])
	}
	if got["labels"] != "Lighting" {
		t.Errorf("labels = %v, want Lighting", got["labels"])
	}
	if got["name"] != "Kitchen Light" {
		t.Errorf("name = %v, want 'Kitchen Light'", got["name"])
	}
	if got["changed_by"] != "User Jan" {
		t.Errorf("changed_by = %v, want 'User Jan'", got["changed_by"])
	}
}

// ---------------------------------------------------------------------------
// --json must never carry a human header line before the JSON body.
// ---------------------------------------------------------------------------

func TestRunEntHist_JSON_NoHeaderLine(t *testing.T) {
	histData := `[[
		{"entity_id":"sensor.temp","state":"21.5","last_changed":"2026-01-01T10:00:00+00:00"},
		{"entity_id":"sensor.temp","state":"22.5","last_changed":"2026-01-01T11:00:00+00:00"}
	]]`
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/history/period/": func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, histData)
		},
	})
	withFlagDir(t, ts.dir)

	oldSince, oldJSON := flagSince, flagJSON
	flagSince = "24h"
	flagJSON = true
	defer func() { flagSince, flagJSON = oldSince, oldJSON }()

	var buf bytes.Buffer
	if err := runEntHist(context.Background(), &buf, "sensor.temp"); err != nil {
		t.Fatalf("runEntHist --json: %v", err)
	}
	out := buf.String()
	if strings.HasPrefix(strings.TrimSpace(out), "sensor.temp:") {
		t.Errorf("ent hist --json printed a human header before the JSON body:\n%s", out)
	}
	var rows []map[string]string
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("ent hist --json output not valid JSON: %v\n%s", err, out)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one history row")
	}
}

// TestRunEntAnomalies_JSON_NoHeaderLine forces the state-duration ("stuck")
// path (a non-numeric entity, changed years ago) so len(anomalies) > 0 and the
// header-printing branch actually runs.
func TestRunEntAnomalies_JSON_NoHeaderLine(t *testing.T) {
	histData := `[[
		{"entity_id":"binary_sensor.stuck","state":"on","last_changed":"2020-01-01T00:00:00+00:00"}
	]]`
	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/history/period/": func(w http.ResponseWriter, r *http.Request) {
			_, _ = fmt.Fprint(w, histData)
		},
	})
	withFlagDir(t, ts.dir)

	oldSince, oldJSON := flagSince, flagJSON
	flagSince = "3000d" // comfortably covers since 2020
	flagJSON = true
	defer func() { flagSince, flagJSON = oldSince, oldJSON }()

	var buf bytes.Buffer
	if err := runEntAnomalies(context.Background(), &buf, "binary_sensor.stuck"); err != nil {
		t.Fatalf("runEntAnomalies --json: %v", err)
	}
	out := buf.String()
	if strings.HasPrefix(strings.TrimSpace(out), "binary_sensor.stuck:") {
		t.Errorf("ent anomalies --json printed a human header before the JSON body:\n%s", out)
	}
	var rows []map[string]string
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("ent anomalies --json output not valid JSON: %v\n%s", err, out)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one anomaly row (entity stuck since 2020)")
	}
}

// TestRunEntRelated_JSON_NoHeaderLine forces a non-empty result via device
// siblings so the header-printing branch (known && len(related) > 0) runs.
func TestRunEntRelated_JSON_NoHeaderLine(t *testing.T) {
	states := []map[string]any{
		{"entity_id": "sensor.a", "state": "1", "last_changed": "2026-01-01T10:00:00Z"},
		{"entity_id": "sensor.b", "state": "2", "last_changed": "2026-01-01T10:00:00Z"},
	}
	statesJSON, _ := json.Marshal(states)
	ts := startCmdServer(t, map[string]any{
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "sensor.a", "device_id": "dev1"},
			{"entity_id": "sensor.b", "device_id": "dev1"},
		},
		"config/area_registry/list":  []any{},
		"config/label_registry/list": []any{},
		"config/floor_registry/list": []any{},
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(statesJSON)
		},
	})
	withFlagDir(t, ts.dir)

	oldJSON := flagJSON
	flagJSON = true
	defer func() { flagJSON = oldJSON }()

	var buf bytes.Buffer
	if err := runEntRelated(context.Background(), &buf, "sensor.a"); err != nil {
		t.Fatalf("runEntRelated --json: %v", err)
	}
	out := buf.String()
	if strings.HasPrefix(strings.TrimSpace(out), "sensor.a:") {
		t.Errorf("ent related --json printed a human header before the JSON body:\n%s", out)
	}
	var rows []map[string]string
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("ent related --json output not valid JSON: %v\n%s", err, out)
	}
	if len(rows) == 0 {
		t.Fatal("expected at least one related row (sensor.b is a device sibling)")
	}
}

// R4: HA's history returns one record per state *update*, and an attribute-only
// update repeats the same state. The timeline used to emit a row per record, so
// a sensor that held one value for 40 minutes rendered as eight identical rows
// of 5m — 287 rows where the entity had 37 actual state runs. A "timeline" that
// lists the same state eight times in a row is not a timeline; the reader has to
// re-derive the runs the command was asked to produce.
func TestParseStateTimeline_CollapsesRepeatedStates(t *testing.T) {
	data := []byte(`[[
		{"entity_id":"sensor.mode","state":"heating","last_changed":"2026-01-01T10:00:00+00:00"},
		{"entity_id":"sensor.mode","state":"heating","last_changed":"2026-01-01T10:05:00+00:00"},
		{"entity_id":"sensor.mode","state":"heating","last_changed":"2026-01-01T10:10:00+00:00"},
		{"entity_id":"sensor.mode","state":"idle","last_changed":"2026-01-01T10:40:00+00:00"},
		{"entity_id":"sensor.mode","state":"idle","last_changed":"2026-01-01T10:45:00+00:00"},
		{"entity_id":"sensor.mode","state":"heating","last_changed":"2026-01-01T10:50:00+00:00"}
	]]`)

	now := time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC)
	changes, err := parseStateTimeline(data, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 3 {
		t.Fatalf("got %d rows, want 3 runs (heating, idle, heating): %+v", len(changes), changes)
	}
	want := []struct {
		state string
		dur   time.Duration
	}{
		{"heating", 40 * time.Minute}, // 10:00 → 10:40, not 3 × 5m
		{"idle", 10 * time.Minute},    // 10:40 → 10:50
		{"heating", 10 * time.Minute}, // 10:50 → now
	}
	for i, w := range want {
		if changes[i].State != w.state || changes[i].Duration != w.dur {
			t.Errorf("run %d = %s for %v, want %s for %v",
				i, changes[i].State, changes[i].Duration, w.state, w.dur)
		}
	}
	// A run starts when the state was first entered, not when it was last re-reported.
	if got := changes[0].Time; !got.Equal(time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("first run starts at %v, want 10:00 — the moment the state was entered", got)
	}
}

// An outage between two identical states is a discontinuity, not one long run.
// unavailable/unknown records are dropped from the timeline, but the break they
// mark is kept — otherwise collapsing repeats would silently claim the entity
// held that state right through the outage.
func TestParseStateTimeline_UnavailableBreaksARun(t *testing.T) {
	data := []byte(`[[
		{"entity_id":"binary_sensor.door","state":"on","last_changed":"2026-01-01T10:00:00+00:00"},
		{"entity_id":"binary_sensor.door","state":"unavailable","last_changed":"2026-01-01T10:10:00+00:00"},
		{"entity_id":"binary_sensor.door","state":"on","last_changed":"2026-01-01T10:20:00+00:00"}
	]]`)

	now := time.Date(2026, 1, 1, 10, 30, 0, 0, time.UTC)
	changes, err := parseStateTimeline(data, now)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(changes) != 2 {
		t.Fatalf("got %d rows, want 2 runs of 'on' either side of the outage: %+v", len(changes), changes)
	}
	if changes[0].Duration != 20*time.Minute || changes[1].Duration != 10*time.Minute {
		t.Errorf("durations = %v, %v; want 20m (10:00→10:20) and 10m (10:20→now)",
			changes[0].Duration, changes[1].Duration)
	}
}
