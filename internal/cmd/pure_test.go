package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/analyze"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// --- systemLogToEntries ---

func TestSystemLogToEntries_Basic(t *testing.T) {
	// NOTE: this test previously asserted that Component was truncated to the
	// logger name's last dot-segment ("recorder", "base"). That was defect #1:
	// --component and `cc logs <name>` filter on this same field
	// (analyze.FilterByComponent), so truncating it here silently broke every
	// filter value that isn't the logger's last segment — e.g.
	// `--component automation` matched nothing because
	// "homeassistant.components.automation.x" had already been cut down to
	// "x". Component must carry the FULL logger name; only the rendered
	// table column shortens it for display (see shortComponent).
	entries := []haapi.SystemLogEntry{
		{
			Name:      "homeassistant.components.recorder",
			Message:   []string{"Unable to find entity"},
			Level:     "ERROR",
			Timestamp: 1745308920.5,
			Count:     1,
		},
		{
			Name:      "custom_components.hacs.base",
			Message:   []string{"Rate limited", "Try again later"},
			Level:     "warning",
			Exception: "Traceback:\n  ...",
			Timestamp: 1745308950.0,
			Count:     3,
		},
	}

	result := systemLogToEntries(entries)
	if len(result) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(result))
	}

	// First entry
	if result[0].Level != "ERROR" {
		t.Errorf("level = %q, want ERROR", result[0].Level)
	}
	if result[0].Component != "homeassistant.components.recorder" {
		t.Errorf("component = %q, want full logger name 'homeassistant.components.recorder'", result[0].Component)
	}
	if result[0].Message != "Unable to find entity" {
		t.Errorf("message = %q, want 'Unable to find entity'", result[0].Message)
	}
	if result[0].Count != 1 {
		t.Errorf("count = %d, want 1", result[0].Count)
	}

	// Second entry: WARNING → uppercased, multi-line message, exception appended
	if result[1].Level != "WARNING" {
		t.Errorf("level = %q, want WARNING", result[1].Level)
	}
	if result[1].Component != "custom_components.hacs.base" {
		t.Errorf("component = %q, want full logger name 'custom_components.hacs.base'", result[1].Component)
	}
	if !strings.Contains(result[1].Message, "Rate limited") {
		t.Errorf("message missing first line: %q", result[1].Message)
	}
	if !strings.Contains(result[1].Message, "Traceback") {
		t.Errorf("message missing exception: %q", result[1].Message)
	}
	// Defect #2: HA's own pre-aggregated count must survive, not be dropped.
	if result[1].Count != 3 {
		t.Errorf("count = %d, want 3 (HA's own pre-aggregated count)", result[1].Count)
	}
}

func TestSystemLogToEntries_CountDefaultsToOneWhenHAOmitsIt(t *testing.T) {
	entry := haapi.SystemLogEntry{
		Name:      "core",
		Message:   []string{"startup"},
		Level:     "INFO",
		Timestamp: 1745308920.0,
		// Count deliberately left zero.
	}
	result := systemLogToEntries([]haapi.SystemLogEntry{entry})
	if len(result) != 1 {
		t.Fatalf("expected 1, got %d", len(result))
	}
	if result[0].Count != 1 {
		t.Errorf("count = %d, want 1 (default when HA's count field is absent/zero)", result[0].Count)
	}
}

func TestShortComponent(t *testing.T) {
	tests := []struct{ in, want string }{
		{"homeassistant.components.zha", "zha"},
		{"homeassistant.components.automation.oracle_missing_service", "oracle_missing_service"},
		{"core", "core"},
		{"", ""},
	}
	for _, tt := range tests {
		if got := shortComponent(tt.in); got != tt.want {
			t.Errorf("shortComponent(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestSystemLogToEntries_Empty(t *testing.T) {
	result := systemLogToEntries(nil)
	if len(result) != 0 {
		t.Errorf("expected 0 entries, got %d", len(result))
	}
}

func TestSystemLogToEntries_ShortName(t *testing.T) {
	// Name without dots should be used as-is for component
	entry := haapi.SystemLogEntry{
		Name:      "core",
		Message:   []string{"startup"},
		Level:     "INFO",
		Timestamp: 1745308920.0,
	}
	result := systemLogToEntries([]haapi.SystemLogEntry{entry})
	if len(result) != 1 {
		t.Fatalf("expected 1, got %d", len(result))
	}
	if result[0].Component != "core" {
		t.Errorf("component = %q, want 'core'", result[0].Component)
	}
}

// --- formatLogAsText ---

func TestFormatLogAsText_Basic(t *testing.T) {
	entries := []analyze.LogEntry{
		{
			Timestamp: "2026-01-01 10:00:00.000",
			Level:     "ERROR",
			Component: "recorder",
			Message:   "Cannot connect to database",
		},
		{
			Timestamp: "2026-01-01 10:01:00.000",
			Level:     "WARNING",
			Component: "zha",
			Message:   "Device not responding",
		},
	}

	result := formatLogAsText(entries)
	if !strings.Contains(result, "ERROR") {
		t.Errorf("formatLogAsText missing ERROR level: %q", result)
	}
	if !strings.Contains(result, "recorder") {
		t.Errorf("formatLogAsText missing component: %q", result)
	}
	if !strings.Contains(result, "Cannot connect") {
		t.Errorf("formatLogAsText missing message: %q", result)
	}
	if !strings.Contains(result, "2026-01-01 10:01") {
		t.Errorf("formatLogAsText missing second entry timestamp: %q", result)
	}
}

func TestFormatLogAsText_Empty(t *testing.T) {
	result := formatLogAsText(nil)
	if result != "" {
		t.Errorf("formatLogAsText(nil) = %q, want empty", result)
	}
}

// --- renderLogEntriesSimple ---

func TestRenderLogEntriesSimple_Basic(t *testing.T) {
	entries := []analyze.LogEntry{
		{Timestamp: "2026-01-01 10:00:00.000", Level: "ERROR", Component: "recorder", Message: "Short message"},
		{Timestamp: "2026-01-01 10:01:00.000", Level: "WARNING", Component: "mqtt", Message: "This is a message that is definitely longer than sixty characters so it should be truncated"},
	}

	var buf bytes.Buffer
	if err := renderLogEntriesSimple(&buf, entries); err != nil {
		t.Fatalf("renderLogEntriesSimple failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "recorder") {
		t.Errorf("output missing component 'recorder': %q", out)
	}
	if !strings.Contains(out, "ERROR") {
		t.Errorf("output missing level 'ERROR': %q", out)
	}
}

func TestRenderLogEntriesSimple_Empty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderLogEntriesSimple(&buf, nil); err != nil {
		t.Fatalf("renderLogEntriesSimple(nil) failed: %v", err)
	}
}

// --- renderDedupedLogs ---

func TestRenderDedupedLogs_Basic(t *testing.T) {
	entries := []analyze.LogEntry{
		{Timestamp: "2026-01-01 10:00:00.000", Level: "ERROR", Component: "zha", Message: "Device not found"},
		{Timestamp: "2026-01-01 10:01:00.000", Level: "ERROR", Component: "zha", Message: "Device not found"},
		{Timestamp: "2026-01-01 10:02:00.000", Level: "WARNING", Component: "mqtt", Message: "Connection lost"},
	}

	var buf bytes.Buffer
	if err := renderDedupedLogs(&buf, entries); err != nil {
		t.Fatalf("renderDedupedLogs failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "zha") {
		t.Errorf("output missing component 'zha': %q", out)
	}
}

func TestRenderDedupedLogs_Empty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderDedupedLogs(&buf, nil); err != nil {
		t.Fatalf("renderDedupedLogs(nil) failed: %v", err)
	}
}

// --- buildScriptRows ---

func TestBuildScriptRows_Basic(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)
	scripts := []scriptEntity{
		{EntityID: "script.welcome_home", State: "on"},
		{EntityID: "script.good_night", State: "off"},
	}
	traces := haapi.TraceListResult{
		"script.welcome_home": {
			{RunID: "r1", Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-1 * time.Hour).Format(time.RFC3339Nano)}, Execution: "finished"},
			{RunID: "r2", Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)}, Execution: "error", Error: "template error"},
		},
	}

	rows := buildScriptRows(scripts, traces, cutoff)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	// Find welcome_home row
	var welcomeRow *scriptRow
	for i := range rows {
		if rows[i].id == "welcome_home" {
			welcomeRow = &rows[i]
		}
	}
	if welcomeRow == nil {
		t.Fatal("welcome_home row not found")
	}
	if welcomeRow.runs != 2 {
		t.Errorf("welcome_home runs = %d, want 2", welcomeRow.runs)
	}
	if welcomeRow.errors != 1 {
		t.Errorf("welcome_home errors = %d, want 1", welcomeRow.errors)
	}
}

func TestBuildScriptRows_NoTraces(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)
	scripts := []scriptEntity{{EntityID: "script.empty", State: "on"}}

	rows := buildScriptRows(scripts, nil, cutoff)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].runs != 0 {
		t.Errorf("runs = %d, want 0", rows[0].runs)
	}
}

func TestBuildScriptRows_OutsideWindow(t *testing.T) {
	cutoff := time.Now().Add(-1 * time.Hour)
	scripts := []scriptEntity{{EntityID: "script.old", State: "on"}}
	traces := haapi.TraceListResult{
		"script.old": {
			// 48 hours ago — outside the 1h window
			{RunID: "r1", Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-48 * time.Hour).Format(time.RFC3339Nano)}, Execution: "finished"},
		},
	}

	rows := buildScriptRows(scripts, traces, cutoff)
	if rows[0].runs != 0 {
		t.Errorf("runs outside window = %d, want 0", rows[0].runs)
	}
}

// --- filterScriptsByPattern ---

func TestFilterScriptsByPattern(t *testing.T) {
	rows := []scriptRow{
		{id: "welcome_home"},
		{id: "good_night"},
		{id: "welcome_back"},
	}

	result := filterScriptsByPattern(rows, "welcome_*")
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}

	// With domain prefix
	result = filterScriptsByPattern(rows, "script.good_night")
	if len(result) != 1 {
		t.Fatalf("expected 1, got %d", len(result))
	}
}

// --- filterScriptsByLabel ---

func TestFilterScriptsByLabel(t *testing.T) {
	rows := []scriptRow{
		{id: "morning", labels: "routine, energy"},
		{id: "night", labels: "routine"},
		{id: "hvac", labels: "energy, climate"},
	}

	result := filterScriptsByLabel(rows, "energy")
	if len(result) != 2 {
		t.Fatalf("expected 2 matches for 'energy', got %d", len(result))
	}

	result = filterScriptsByLabel(rows, "ROUTINE")
	if len(result) != 2 {
		t.Fatalf("expected 2 matches for case-insensitive 'ROUTINE', got %d", len(result))
	}
}

// --- filterScriptsFailing ---

func TestFilterScriptsFailing(t *testing.T) {
	rows := []scriptRow{
		{id: "a", errors: 0},
		{id: "b", errors: 2},
		{id: "c", errors: 1},
	}

	result := filterScriptsFailing(rows)
	if len(result) != 2 {
		t.Fatalf("expected 2, got %d", len(result))
	}
	if result[0].id != "b" {
		t.Errorf("first failing = %q, want 'b'", result[0].id)
	}
}

// --- maskToken ---

func TestMaskToken_Long(t *testing.T) {
	token := "eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9.test" //nolint:gosec
	got := maskToken(token)
	if !strings.Contains(got, "***") {
		t.Errorf("maskToken = %q, want '***' in result", got)
	}
	if !strings.HasPrefix(got, "eyJh") {
		t.Errorf("maskToken = %q, want to start with first 4 chars", got)
	}
}

func TestMaskToken_Short(t *testing.T) {
	got := maskToken("short")
	if got != "***" {
		t.Errorf("maskToken(short) = %q, want '***'", got)
	}
}

func TestMaskToken_Empty(t *testing.T) {
	got := maskToken("")
	if got != "***" {
		t.Errorf("maskToken(empty) = %q, want '***'", got)
	}
}

// --- renderStateTimeline ---

func TestRenderStateTimeline_Basic(t *testing.T) {
	now := time.Now()
	changes := []analyze.StateChange{
		{Time: now.Add(-10 * time.Minute), State: "on", Duration: 5 * time.Minute},
		{Time: now.Add(-5 * time.Minute), State: "off", Duration: 5 * time.Minute},
	}

	var buf bytes.Buffer
	if err := renderStateTimeline(&buf, "binary_sensor.door", changes); err != nil {
		t.Fatalf("renderStateTimeline failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "binary_sensor.door") {
		t.Errorf("output missing entity ID: %q", out)
	}
	if !strings.Contains(out, "2 state changes") {
		t.Errorf("output missing change count: %q", out)
	}
}

func TestRenderStateTimeline_Empty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderStateTimeline(&buf, "sensor.x", nil); err != nil {
		t.Fatalf("renderStateTimeline(empty) failed: %v", err)
	}
	if !strings.Contains(buf.String(), "0 state changes") {
		t.Errorf("output = %q, want '0 state changes'", buf.String())
	}
}

// --- renderStateAnomalies ---

func TestRenderStateAnomalies_NoAnomalies(t *testing.T) {
	// Short durations below defaultStateStuckDuration → no anomalies
	changes := []analyze.StateChange{
		{Time: time.Now().Add(-1 * time.Minute), State: "on", Duration: 30 * time.Second},
	}

	var buf bytes.Buffer
	dir := t.TempDir()
	if err := renderStateAnomalies(&buf, "binary_sensor.x", dir, changes); err != nil {
		t.Fatalf("renderStateAnomalies failed: %v", err)
	}
	if !strings.Contains(buf.String(), "no anomalies") {
		t.Errorf("output = %q, want 'no anomalies'", buf.String())
	}
}

func TestRenderStateAnomalies_WithStuck(t *testing.T) {
	// Duration >= defaultStateStuckDuration → anomaly
	// defaultStateStuckDuration is likely 4+ hours
	changes := []analyze.StateChange{
		{Time: time.Now().Add(-10 * time.Hour), State: "on", Duration: 8 * time.Hour},
	}

	var buf bytes.Buffer
	dir := t.TempDir()
	if err := renderStateAnomalies(&buf, "binary_sensor.door", dir, changes); err != nil {
		t.Fatalf("renderStateAnomalies with stuck failed: %v", err)
	}
	// If there's a stuck anomaly it should show it, otherwise 'no anomalies'
	// Either way, should not error
}

// --- renderHistoryPoints ---

func TestRenderHistoryPoints_Basic(t *testing.T) {
	points := []analyze.DataPoint{
		{Time: time.Date(2026, 1, 1, 10, 0, 0, 0, time.UTC), Value: 21.5},
		{Time: time.Date(2026, 1, 1, 11, 0, 0, 0, time.UTC), Value: 22.3},
		{Time: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), Value: 20.8},
	}

	var buf bytes.Buffer
	if err := renderHistoryPoints(&buf, "sensor.temperature", points); err != nil {
		t.Fatalf("renderHistoryPoints failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "sensor.temperature") {
		t.Errorf("output missing entity ID: %q", out)
	}
	if !strings.Contains(out, "3 points") {
		t.Errorf("output missing point count: %q", out)
	}
	if !strings.Contains(out, "21.50") {
		t.Errorf("output missing value: %q", out)
	}
}

func TestRenderHistoryPoints_Empty(t *testing.T) {
	var buf bytes.Buffer
	if err := renderHistoryPoints(&buf, "sensor.x", nil); err != nil {
		t.Fatalf("renderHistoryPoints(empty) failed: %v", err)
	}
	if !strings.Contains(buf.String(), "0 points") {
		t.Errorf("output = %q, want '0 points'", buf.String())
	}
}

// --- findDeviceSiblings ---

func TestFindDeviceSiblings_Found(t *testing.T) {
	rc := &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			"sensor.temp":     {EntityID: "sensor.temp", DeviceID: "device1"},
			"sensor.humidity": {EntityID: "sensor.humidity", DeviceID: "device1"},
			"light.kitchen":   {EntityID: "light.kitchen", DeviceID: "device2"},
		},
		areaByID:  map[string]haapi.AreaEntry{},
		labelByID: map[string]haapi.LabelEntry{},
		floorByID: map[string]haapi.FloorEntry{},
	}

	result := findDeviceSiblings(rc, "sensor.temp")
	if len(result) != 1 {
		t.Fatalf("expected 1 sibling, got %d", len(result))
	}
	if result[0].entityID != "sensor.humidity" {
		t.Errorf("sibling = %q, want 'sensor.humidity'", result[0].entityID)
	}
	if result[0].relationship != "device-sibling" {
		t.Errorf("relationship = %q, want 'device-sibling'", result[0].relationship)
	}
}

func TestFindDeviceSiblings_NoDevice(t *testing.T) {
	rc := &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			"sensor.x": {EntityID: "sensor.x"},
		},
		areaByID:  map[string]haapi.AreaEntry{},
		labelByID: map[string]haapi.LabelEntry{},
		floorByID: map[string]haapi.FloorEntry{},
	}
	result := findDeviceSiblings(rc, "sensor.x")
	if len(result) != 0 {
		t.Errorf("expected 0 siblings for entity without device, got %d", len(result))
	}
}

// --- findAreaNeighbors ---

func TestFindAreaNeighbors_Found(t *testing.T) {
	rc := &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			"light.kitchen1": {EntityID: "light.kitchen1", AreaID: "kitchen"},
			"light.kitchen2": {EntityID: "light.kitchen2", AreaID: "kitchen"},
			"sensor.temp":    {EntityID: "sensor.temp", AreaID: "kitchen"},
		},
		areaByID: map[string]haapi.AreaEntry{
			"kitchen": {AreaID: "kitchen", Name: "Kitchen"},
		},
		labelByID: map[string]haapi.LabelEntry{},
		floorByID: map[string]haapi.FloorEntry{},
	}

	// Inverted 2026-07-23 (R1): this asserted "1 area neighbor (same domain,
	// same area)" and so pinned the domain filter as correct. An area neighbour
	// is any entity in the same area — that is what HA's area_entities() means,
	// what `ent ls --area` returns, and what the manual promises. sensor.temp
	// belongs in the answer.
	result := findAreaNeighbors(rc, "light.kitchen1")
	got := make(map[string]bool, len(result))
	for _, r := range result {
		got[r.entityID] = true
	}
	if len(result) != 2 || !got["light.kitchen2"] || !got["sensor.temp"] {
		t.Fatalf("findAreaNeighbors(light.kitchen1) = %+v, want both light.kitchen2 and sensor.temp", result)
	}
}

func TestFindAreaNeighbors_NoArea(t *testing.T) {
	rc := &registryContext{
		entityByID: map[string]haapi.EntityRegistryEntry{
			"sensor.x": {EntityID: "sensor.x"},
		},
		areaByID:  map[string]haapi.AreaEntry{},
		labelByID: map[string]haapi.LabelEntry{},
		floorByID: map[string]haapi.FloorEntry{},
	}
	result := findAreaNeighbors(rc, "sensor.x")
	if len(result) != 0 {
		t.Errorf("expected 0 neighbors for entity without area, got %d", len(result))
	}
}

// --- findGroupMemberships ---

func TestFindGroupMemberships_Found(t *testing.T) {
	states := []entityState{
		{EntityID: "group.lights", Attributes: map[string]any{
			"entity_id": []any{"light.kitchen", "light.bedroom"},
		}},
		{EntityID: "group.sensors", Attributes: map[string]any{
			"entity_id": []any{"sensor.temp"},
		}},
	}

	result := findGroupMemberships(states, "light.kitchen")
	if len(result) != 1 {
		t.Fatalf("expected 1 group membership, got %d", len(result))
	}
	if result[0].entityID != "group.lights" {
		t.Errorf("group = %q, want 'group.lights'", result[0].entityID)
	}
	if result[0].relationship != "group-member" {
		t.Errorf("relationship = %q, want 'group-member'", result[0].relationship)
	}
}

func TestFindGroupMemberships_NotMember(t *testing.T) {
	states := []entityState{
		{EntityID: "group.lights", Attributes: map[string]any{
			"entity_id": []any{"light.bedroom"},
		}},
	}
	result := findGroupMemberships(states, "light.kitchen")
	if len(result) != 0 {
		t.Errorf("expected 0 memberships, got %d", len(result))
	}
}

// --- showSingleView ---

func TestShowSingleView_Found(t *testing.T) {
	import_ := `{"title":"Energy","path":"energy","type":"sections"}`
	cfg := &haapi.LovelaceConfig{
		Views: []json.RawMessage{
			json.RawMessage(`{"title":"Main","path":"main","type":"masonry"}`),
			json.RawMessage(import_),
		},
	}

	old := flagDashView
	flagDashView = "energy"
	defer func() { flagDashView = old }()

	var buf bytes.Buffer
	if err := showSingleView(&buf, cfg); err != nil {
		t.Fatalf("showSingleView failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "energy") {
		t.Errorf("output missing 'energy': %q", out)
	}
}

func TestShowSingleView_ByTitle(t *testing.T) {
	cfg := &haapi.LovelaceConfig{
		Views: []json.RawMessage{
			json.RawMessage(`{"title":"Main","path":"main"}`),
		},
	}

	old := flagDashView
	flagDashView = "Main"
	defer func() { flagDashView = old }()

	var buf bytes.Buffer
	if err := showSingleView(&buf, cfg); err != nil {
		t.Fatalf("showSingleView by title failed: %v", err)
	}
	if !strings.Contains(buf.String(), "Main") {
		t.Errorf("output missing 'Main': %q", buf.String())
	}
}

func TestShowSingleView_NotFound(t *testing.T) {
	cfg := &haapi.LovelaceConfig{
		Views: []json.RawMessage{
			json.RawMessage(`{"title":"Main","path":"main"}`),
		},
	}

	old := flagDashView
	flagDashView = "nonexistent"
	defer func() { flagDashView = old }()

	var buf bytes.Buffer
	if err := showSingleView(&buf, cfg); err == nil {
		t.Fatal("expected error for nonexistent view, got nil")
	}
}

func TestRenderStateAnomalies_AnomalyDetected(t *testing.T) {
	// Duration >= defaultStateStuckDuration (24h) triggers anomaly table rendering
	changes := []analyze.StateChange{
		{Time: time.Now().Add(-30 * time.Hour), State: "on", Duration: 25 * time.Hour},
	}

	var buf bytes.Buffer
	dir := t.TempDir()
	if err := renderStateAnomalies(&buf, "binary_sensor.door", dir, changes); err != nil {
		t.Fatalf("renderStateAnomalies with anomaly failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "anomal") {
		t.Errorf("output = %q, expected 'anomal...' in output", out)
	}
}

func TestTruncationHint_LogCmd(t *testing.T) {
	got := truncationHint("hactl log")
	if !strings.Contains(got, "--errors") {
		t.Errorf("truncationHint(log) = %q, want hint with --errors", got)
	}
}

func TestTruncationHint_EntLs(t *testing.T) {
	got := truncationHint("hactl ent ls")
	if !strings.Contains(got, "--domain") {
		t.Errorf("truncationHint(ent ls) = %q, want hint with --domain", got)
	}
}

func TestTruncationHint_AutoLs(t *testing.T) {
	got := truncationHint("hactl auto ls")
	if !strings.Contains(got, "--pattern") {
		t.Errorf("truncationHint(auto ls) = %q, want hint with --pattern", got)
	}
}

func TestTruncationHint_ScriptLs(t *testing.T) {
	got := truncationHint("hactl script ls")
	if !strings.Contains(got, "--pattern") {
		t.Errorf("truncationHint(script ls) = %q, want hint with --pattern", got)
	}
}

func TestTruncationHint_EntShow(t *testing.T) {
	old := flagFull
	flagFull = false
	defer func() { flagFull = old }()

	got := truncationHint("hactl ent show sensor.temp")
	if !strings.Contains(got, "--tokensmax") {
		t.Errorf("truncationHint(ent show) = %q, want hint with --tokensmax", got)
	}
}

func TestTruncationHint_EntShowFull(t *testing.T) {
	old := flagFull
	flagFull = true
	defer func() { flagFull = old }()

	got := truncationHint("hactl ent show sensor.temp")
	if !strings.Contains(got, "--full") {
		t.Errorf("truncationHint(ent show --full) = %q, want hint about --full", got)
	}
}

func TestTruncationHint_Default(t *testing.T) {
	got := truncationHint("hactl some other cmd")
	if !strings.Contains(got, "--tokensmax") {
		t.Errorf("truncationHint(default) = %q, want default hint with --tokensmax", got)
	}
}

// A resample bucket of zero or a negative duration is not a request the command
// can honour: analyze.ResampleDuration returns the input untouched for both, so
// `--resample 0m` silently produced default output and `--resample -5m` did the
// same. Silently ignoring a flag value the caller chose is the same class of
// defect as a --json that does nothing: the caller cannot tell it was ignored.
func TestParseResampleDuration_RejectsNonPositive(t *testing.T) {
	for _, tc := range []struct {
		in      string
		wantErr bool
	}{
		{"5m", false},
		{"1h", false},
		{"0m", true},
		{"0s", true},
		{"-5m", true},
		{"banana", true},
		{"", true},
	} {
		_, err := parseResampleDuration(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("parseResampleDuration(%q) error = %v, want error: %v", tc.in, err, tc.wantErr)
		}
	}
}
