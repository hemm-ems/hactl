package cmd

import (
	"bytes"
	"context"
	"encoding/json"
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
// light turned on by the given user UUID (or empty for a system-driven change).
func stateJSON(state, userID string) string {
	ctx := `{"id":"01HXYZ","parent_id":null,"user_id":` + jsonString(userID) + `}`
	return `{"entity_id":"light.kitchen","state":"` + state +
		`","attributes":{"friendly_name":"Kitchen Light"},` +
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
		stateJSON("on", janUUID),
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
		stateJSON("on", janUUID),
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
		stateJSON("on", ""),
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
		stateJSON("on", janUUID),
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
