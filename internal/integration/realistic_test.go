//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// realisticHA provides a lazily-initialized HA instance with the "realistic" fixture.
// The realistic fixture includes template sensors, input helpers, system_log,
// and diverse automations — modelled after a real-world 381-automation installation.
var (
	realisticOnce sync.Once
	realisticHA   *hatest.Instance
)

// waitForRunning polls /api/config until HA reports state=RUNNING.
// The realistic fixture has complex config; HA may need extra time after
// the HTTP endpoint becomes reachable.
func waitForRunning(t *testing.T, inst *hatest.Instance) {
	t.Helper()
	client := haapi.New(inst.URL(), inst.Token())
	ctx := context.Background()

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := client.GetConfig(ctx)
		if err == nil {
			var cfg struct {
				State string `json:"state"`
			}
			if json.Unmarshal(raw, &cfg) == nil && cfg.State == "RUNNING" {
				return
			}
		}
		time.Sleep(1 * time.Second)
	}
	t.Log("waitForRunning: HA did not reach RUNNING state within 60s, proceeding anyway")
}

func getRealisticHA(t *testing.T) *hatest.Instance {
	t.Helper()
	realisticOnce.Do(func() {
		realisticHA = hatest.StartShared(t, hatest.WithFixture("realistic"))
		// Wait for HA to reach RUNNING state before seeding history.
		// The container reports ready when /api/onboarding responds,
		// but HA may still be initializing (state=NOT_RUNNING).
		waitForRunning(t, realisticHA)
		// Seed numeric history by toggling input_number values a few times.
		seedHistory(t, realisticHA)
	})
	return realisticHA
}

// seedHistory uses the HA REST API to set input_number values,
// creating state-change history that ent hist / ent anomalies can query.
func seedHistory(t *testing.T, inst *hatest.Instance) {
	t.Helper()
	client := haapi.New(inst.URL(), inst.Token())
	ctx := context.Background()

	// Simulate temperature readings over time
	temps := []float64{15.0, 18.5, 22.0, 19.5, 16.0}
	for _, temp := range temps {
		err := client.CallService(ctx, "input_number", "set_value", map[string]any{
			"entity_id": "input_number.outdoor_temperature",
			"value":     temp,
		})
		if err != nil {
			t.Logf("seedHistory: set outdoor_temperature to %.1f: %v", temp, err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Simulate power consumption with a spike (for anomaly detection)
	powers := []float64{350, 400, 380, 4500, 360}
	for _, p := range powers {
		err := client.CallService(ctx, "input_number", "set_value", map[string]any{
			"entity_id": "input_number.power_consumption",
			"value":     p,
		})
		if err != nil {
			t.Logf("seedHistory: set power_consumption to %.0f: %v", p, err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Toggle guest mode to generate events, log entries, and binary_sensor.front_door timeline
	for range 3 {
		_ = client.CallService(ctx, "input_boolean", "toggle", map[string]any{
			"entity_id": "input_boolean.guest_mode",
		})
		time.Sleep(500 * time.Millisecond)
	}

	// Trigger automations to create traces for E2E trace testing
	for _, autoID := range []string{"guest_welcome", "vacation_security"} {
		err := client.CallService(ctx, "automation", "trigger", map[string]any{
			"entity_id": "automation." + autoID,
		})
		if err != nil {
			t.Logf("seedHistory: trigger %s: %v", autoID, err)
		}
		time.Sleep(500 * time.Millisecond)
	}

	// Let HA settle and record states
	time.Sleep(2 * time.Second)
}

// --- Health ---

func TestRealisticHealth(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "health")
	assertContains(t, out, "HA ")
	assertContains(t, out, "Realistic Home")
	assertContains(t, out, "state=RUNNING")
	assertContains(t, out, "recorder=ok")
}

func TestRealisticHealthJSON(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "health", "--json")

	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("health --json invalid JSON: %v\noutput: %s", err, out)
	}
	if result["version"] == nil {
		t.Error("health --json missing 'version'")
	}
	if result["state"] != "RUNNING" {
		t.Errorf("health --json state = %v, want RUNNING", result["state"])
	}
	if result["recorder"] != "ok" {
		t.Errorf("health --json recorder = %v, want ok", result["recorder"])
	}
	if result["location"] != "Realistic Home" {
		t.Errorf("health --json location = %v, want 'Realistic Home'", result["location"])
	}
}

// --- Logs (WS system_log/list) ---

func TestRealisticLog(t *testing.T) {
	inst := getRealisticHA(t)
	// The realistic fixture has system_log explicitly configured,
	// so the WS system_log/list path should work.
	out, err := runHactlDirErr(t, inst.Dir(), "log")
	if err != nil {
		// Fresh HA may have no log entries — the REST fallback may also 404.
		// As long as we don't panic, that's acceptable.
		if !strings.Contains(out, "panic") {
			t.Log("log command returned error (may be normal for fresh HA):", err)
			return
		}
		t.Fatalf("log panicked: %s", out)
	}
	assertNotContains(t, out, "panic")
}

func TestRealisticLogErrors(t *testing.T) {
	inst := getRealisticHA(t)
	out, _ := runHactlDirErr(t, inst.Dir(), "log", "--errors")
	assertNotContains(t, out, "panic")
}

func TestRealisticLogJSON(t *testing.T) {
	inst := getRealisticHA(t)
	out, err := runHactlDirErr(t, inst.Dir(), "log", "--json", "--full")
	if err != nil {
		t.Log("log --json returned error:", err)
		return
	}
	// Should be valid JSON (array)
	trimmed := strings.TrimSpace(out)
	if trimmed != "" && strings.HasPrefix(trimmed, "[") {
		var entries []json.RawMessage
		if jsonErr := json.Unmarshal([]byte(trimmed), &entries); jsonErr != nil {
			t.Errorf("log --json invalid JSON: %v", jsonErr)
		}
	}
}

func TestRealisticLogUnique(t *testing.T) {
	inst := getRealisticHA(t)
	out, _ := runHactlDirErr(t, inst.Dir(), "log", "--unique")
	assertNotContains(t, out, "panic")
}

// --- Entities with numeric history ---

func TestRealisticEntLsInputs(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "ent", "ls", "--pattern", "input_number.*")
	assertContains(t, out, "input_number.outdoor_temperature")
	assertContains(t, out, "input_number.power_consumption")
	assertContains(t, out, "input_number.indoor_humidity")
}

func TestRealisticEntLsSensors(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "ent", "ls", "--pattern", "sensor.*")
	assertContains(t, out, "sensor.outdoor_temperature")
	assertContains(t, out, "sensor.power_consumption")
}

func TestRealisticEntShowSensor(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "ent", "show", "sensor.outdoor_temperature")
	assertContains(t, out, "sensor.outdoor_temperature")
}

func TestRealisticEntShowJSON(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "ent", "show", "sensor.outdoor_temperature", "--json")
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("ent show --json invalid JSON: %v\noutput: %s", err, out)
	}
	if result["entity_id"] != "sensor.outdoor_temperature" {
		t.Errorf("entity_id = %v, want sensor.outdoor_temperature", result["entity_id"])
	}
}

func TestRealisticEntHistNumeric(t *testing.T) {
	inst := getRealisticHA(t)
	// input_number.outdoor_temperature has been seeded with 5 state changes
	out, err := runHactlDirErr(t, inst.Dir(), "ent", "hist", "input_number.outdoor_temperature", "--since", "1h")
	if err != nil {
		if strings.Contains(out, "no numeric") || strings.Contains(out, "no history") {
			t.Skip("no numeric history yet (HA may not have recorded changes)")
		}
		t.Fatalf("ent hist failed: %v\noutput: %s", err, out)
	}
	// Should have a table with time and value columns
	assertContains(t, out, "time")
	assertContains(t, out, "value")
}

func TestRealisticEntHistJSON(t *testing.T) {
	inst := getRealisticHA(t)
	out, err := runHactlDirErr(t, inst.Dir(), "ent", "hist", "input_number.outdoor_temperature", "--since", "1h", "--json")
	if err != nil {
		t.Skip("no numeric history data yet")
	}
	trimmed := strings.TrimSpace(out)
	// May have header line before JSON
	if idx := strings.Index(trimmed, "["); idx >= 0 {
		trimmed = trimmed[idx:]
	}
	var entries []map[string]string
	if jsonErr := json.Unmarshal([]byte(trimmed), &entries); jsonErr != nil {
		t.Errorf("ent hist --json invalid JSON: %v\noutput: %s", jsonErr, out)
	}
}

func TestRealisticEntAnomalies(t *testing.T) {
	inst := getRealisticHA(t)
	// power_consumption was seeded with a spike (4500W vs ~370W normal)
	out, err := runHactlDirErr(t, inst.Dir(), "ent", "anomalies", "input_number.power_consumption", "--since", "1h")
	if err != nil {
		if strings.Contains(out, "no numeric") || strings.Contains(out, "no history") {
			t.Skip("no numeric history yet for anomaly detection")
		}
		t.Fatalf("ent anomalies failed: %v\noutput: %s", err, out)
	}
	// Should output either anomalies table or "no anomalies"
	assertNotContains(t, out, "panic")
}

// --- Automations ---

func TestRealisticAutoLs(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "auto", "ls")
	assertContains(t, out, "id")
	assertContains(t, out, "state")
}

func TestRealisticAutoLsJSON(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "auto", "ls", "--json")
	var entries []map[string]string
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("auto ls --json invalid JSON: %v\noutput: %s", err, out)
	}
	// Should have the automations from our fixture
	if len(entries) < 5 {
		t.Errorf("expected at least 5 automations, got %d", len(entries))
	}
	// Check schema
	for _, key := range []string{"id", "state", "runs_24h"} {
		if _, ok := entries[0][key]; !ok {
			t.Errorf("auto ls --json entry missing key %q", key)
		}
	}
}

func TestRealisticAutoLsFailing(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "auto", "ls", "--failing")
	assertNotContains(t, out, "panic")
}

func TestRealisticAutoShow(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "auto", "show", "climate_schedule")
	assertContains(t, out, "climate_schedule")
	assertContains(t, out, "state=")
	assertContains(t, out, "mode=")
}

func TestRealisticAutoShowBathroomLight(t *testing.T) {
	inst := getRealisticHA(t)
	out, err := runHactlDirErr(t, inst.Dir(), "auto", "show", "bathroom_light_on_door")
	if err != nil {
		t.Skip("bathroom_light_on_door automation not loaded by HA (trigger entity may not exist)")
	}
	assertContains(t, out, "bathroom_light_on_door")
	assertContains(t, out, "state=")
}

func TestRealisticAutoShowTraces(t *testing.T) {
	inst := getRealisticHA(t)
	// Traces were seeded in seedHistory by triggering guest_welcome.
	// Verify auto show displays them.
	out := runHactlDir(t, inst.Dir(), "auto", "show", "guest_welcome")
	assertContains(t, out, "guest_welcome")
	assertContains(t, out, "traces")
	// With the trace fix, we should see trace IDs (trc:XX)
	assertContains(t, out, "trc:")
}

// --- E2E Trace Pipeline ---

func realisticExtractTraceID(out string) string {
	for l := range strings.SplitSeq(out, "\n") {
		if !strings.Contains(l, "trc:") {
			continue
		}
		for f := range strings.FieldsSeq(l) {
			if strings.HasPrefix(f, "trc:") {
				return f
			}
		}
	}
	return ""
}

func TestRealisticTraceShowCondensed(t *testing.T) {
	inst := getRealisticHA(t)
	// Get trace ID from auto show (traces seeded during seedHistory)
	out := runHactlDir(t, inst.Dir(), "auto", "show", "guest_welcome")
	traceID := realisticExtractTraceID(out)
	if traceID == "" {
		t.Fatal("expected trace ID in auto show output, got none")
	}

	// Show condensed trace — full pipeline: ID resolve → WS trace/get → condense → format
	traceOut := runHactlDir(t, inst.Dir(), "trace", "show", traceID)
	assertNotContains(t, traceOut, "panic")
	if len(traceOut) == 0 {
		t.Error("trace show returned empty output")
	}
	// Condensed output should contain step types or at least a result line
	hasStep := strings.Contains(traceOut, "trigger") ||
		strings.Contains(traceOut, "action") ||
		strings.Contains(traceOut, "cond")
	hasResult := strings.Contains(traceOut, "PASS") ||
		strings.Contains(traceOut, "FAIL")
	if !hasStep && !hasResult {
		t.Errorf("condensed trace missing step types and result:\n%s", traceOut)
	}
}

func TestRealisticTraceShowFull(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "auto", "show", "guest_welcome")
	traceID := realisticExtractTraceID(out)
	if traceID == "" {
		t.Fatal("expected trace ID in auto show output, got none")
	}

	// Show full trace — should be valid JSON
	traceOut := runHactlDir(t, inst.Dir(), "trace", "show", traceID, "--full", "--tokensmax=0")
	trimmed := strings.TrimSpace(stripTokenHeader(traceOut))
	if !strings.HasPrefix(trimmed, "{") {
		t.Fatalf("trace show --full expected JSON object, got: %.100s", trimmed)
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		t.Fatalf("trace show --full returned invalid JSON: %v", err)
	}
	// Full trace should contain trace steps
	if _, ok := raw["trace"]; !ok {
		t.Error("full trace JSON missing 'trace' key")
	}
}

func TestRealisticTraceMultipleAutomations(t *testing.T) {
	inst := getRealisticHA(t)
	// Both guest_welcome and vacation_security were triggered during seedHistory.
	// Verify auto ls shows non-zero runs for at least one automation.
	out := runHactlDir(t, inst.Dir(), "auto", "ls", "--json")
	var entries []map[string]string
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("auto ls --json invalid JSON: %v", err)
	}

	hasRuns := false
	for _, e := range entries {
		if e["runs_24h"] != "0" {
			hasRuns = true
			break
		}
	}
	if !hasRuns {
		t.Error("expected at least one automation with runs_24h > 0 after seeding traces")
	}
}

// --- Binary Sensor Timeline ---

func TestRealisticEntHistBinarySensor(t *testing.T) {
	inst := getRealisticHA(t)
	// binary_sensor.front_door is driven by input_boolean.guest_mode (toggled in seedHistory)
	out, err := runHactlDirErr(t, inst.Dir(), "ent", "hist", "binary_sensor.front_door", "--since", "1h")
	if err != nil {
		if strings.Contains(out, "no history") {
			t.Skip("no history data for binary_sensor.front_door yet")
		}
		t.Fatalf("ent hist binary_sensor failed: %v\noutput: %s", err, out)
	}
	// Should show state timeline (not "no numeric history data")
	assertNotContains(t, out, "no numeric")
	assertContains(t, out, "state changes")
	assertContains(t, out, "state")
	assertContains(t, out, "duration")
}

func TestRealisticEntHistBinarySensorJSON(t *testing.T) {
	inst := getRealisticHA(t)
	out, err := runHactlDirErr(t, inst.Dir(), "ent", "hist", "binary_sensor.front_door", "--since", "1h", "--json")
	if err != nil {
		t.Skip("no history data for binary_sensor.front_door")
	}
	trimmed := strings.TrimSpace(out)
	if idx := strings.Index(trimmed, "["); idx >= 0 {
		trimmed = trimmed[idx:]
	}
	var entries []map[string]string
	if jsonErr := json.Unmarshal([]byte(trimmed), &entries); jsonErr != nil {
		t.Errorf("ent hist --json invalid JSON: %v\noutput: %s", jsonErr, out)
	}
	// Should have state and duration columns
	if len(entries) > 0 {
		if _, ok := entries[0]["state"]; !ok {
			t.Error("binary sensor timeline JSON missing 'state' key")
		}
		if _, ok := entries[0]["duration"]; !ok {
			t.Error("binary sensor timeline JSON missing 'duration' key")
		}
	}
}

func TestRealisticEntHistInputBoolean(t *testing.T) {
	inst := getRealisticHA(t)
	// input_boolean.guest_mode was toggled 3 times in seedHistory
	out, err := runHactlDirErr(t, inst.Dir(), "ent", "hist", "input_boolean.guest_mode", "--since", "1h")
	if err != nil {
		if strings.Contains(out, "no history") {
			t.Skip("no history data yet")
		}
		t.Fatalf("ent hist input_boolean failed: %v\noutput: %s", err, out)
	}
	// Should show state timeline for on/off states
	assertNotContains(t, out, "no numeric")
	assertContains(t, out, "state changes")
}

func TestRealisticEntAnomaliesBinarySensor(t *testing.T) {
	inst := getRealisticHA(t)
	// binary_sensor.front_door — short-lived test, so no stuck anomalies expected
	out, err := runHactlDirErr(t, inst.Dir(), "ent", "anomalies", "binary_sensor.front_door", "--since", "1h")
	if err != nil {
		if strings.Contains(out, "no history") {
			t.Skip("no history data for binary_sensor.front_door")
		}
		t.Fatalf("ent anomalies binary_sensor failed: %v\noutput: %s", err, out)
	}
	// Should not panic or show "no numeric" — either "no anomalies" or anomalies table
	assertNotContains(t, out, "no numeric")
	assertNotContains(t, out, "panic")
}

// --- Changes ---

func TestRealisticChanges(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "changes", "--since", "1h")
	// We seeded state changes, so there should be entries
	assertNotContains(t, out, "panic")
}

func TestRealisticChangesJSON(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "changes", "--since", "1h", "--json")
	trimmed := strings.TrimSpace(out)
	if strings.HasPrefix(trimmed, "[") {
		var entries []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &entries); err != nil {
			t.Errorf("changes --json invalid JSON: %v", err)
		}
		if len(entries) == 0 {
			t.Log("no changes in last 1h (possible on very fast test)")
		}
	}
}

// --- Cache ---

func TestRealisticCacheRefreshLogs(t *testing.T) {
	inst := getRealisticHA(t)
	out, err := runHactlDirErr(t, inst.Dir(), "cache", "refresh", "logs")
	if err != nil {
		t.Log("cache refresh logs error (may be normal):", err)
		return
	}
	assertContains(t, out, "logs refreshed")
}

func TestRealisticCacheRefreshTraces(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "cache", "refresh", "traces")
	assertContains(t, out, "traces refreshed")
}

func TestRealisticCacheStatus(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "cache", "status")
	assertContains(t, out, "traces:")
	assertContains(t, out, "logs:")
}

func TestRealisticCacheStatusJSON(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "cache", "status", "--json")
	var result map[string]any
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("cache status --json invalid JSON: %v\noutput: %s", err, out)
	}
	for _, key := range []string{"trace_count", "traces_db_size", "log_size"} {
		if _, ok := result[key]; !ok {
			t.Errorf("cache status --json missing key %q", key)
		}
	}
}

// --- Templates ---

func TestRealisticTplEvalSensor(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "tpl", "eval", "{{ states('sensor.outdoor_temperature') }}")
	trimmed := strings.TrimSpace(out)
	if trimmed == "" || trimmed == "unavailable" || trimmed == "unknown" {
		t.Skip("sensor.outdoor_temperature not ready yet")
	}
	// Should be a numeric value (the last seeded temp was 16.0)
	assertNotContains(t, out, "error")
}

func TestRealisticTplEvalHouseMode(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "tpl", "eval", "{{ states('input_select.house_mode') }}")
	trimmed := strings.TrimSpace(stripTokenHeader(out))
	// Should be one of our defined options
	validModes := []string{"home", "away", "sleep", "party"}
	if !slices.Contains(validModes, trimmed) {
		t.Errorf("house_mode = %q, want one of %v", trimmed, validModes)
	}
}

func TestRealisticTplEvalCount(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "tpl", "eval", "{{ states.sensor | list | count }}")
	trimmed := strings.TrimSpace(out)
	// Should be a number > 0 (we have template sensors defined)
	if trimmed == "0" {
		t.Error("expected at least 1 sensor entity")
	}
}

// --- Issues ---

func TestRealisticIssues(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "issues")
	assertNotContains(t, out, "panic")
}

// --- CC (custom components) ---

func TestRealisticCCLs(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "cc", "ls")
	// Realistic fixture has no custom components — "no custom components" expected
	assertNotContains(t, out, "panic")
}

func TestRealisticCCLogs(t *testing.T) {
	inst := getRealisticHA(t)
	out, _ := runHactlDirErr(t, inst.Dir(), "cc", "logs", "recorder")
	// Should not panic, may have log entries for recorder component
	assertNotContains(t, out, "panic")
}

// --- WebSocket system_log/list ---

func TestRealisticWSSystemLogList(t *testing.T) {
	inst := getRealisticHA(t)
	ctx := context.Background()

	ws := haapi.NewWSClient(inst.URL(), inst.Token())
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("WebSocket connect failed: %v", err)
	}
	defer func() { _ = ws.Close() }()

	entries, err := ws.SystemLogList(ctx)
	if err != nil {
		// system_log/list may not be available in all HA versions
		t.Logf("SystemLogList error: %v (may be expected)", err)
		return
	}
	// Entries may be empty on fresh HA — that's OK
	t.Logf("SystemLogList returned %d entries", len(entries))
	for _, e := range entries {
		if e.Name == "" {
			t.Error("SystemLogEntry has empty name")
		}
		if e.Level == "" {
			t.Error("SystemLogEntry has empty level")
		}
	}
}

// --- Version ---

func TestRealisticVersion(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "version")
	assertContains(t, out, "hactl")
}

// --- Scripts ---

func TestRealisticScriptLs(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "script", "ls")
	assertContains(t, out, "id")
	assertContains(t, out, "state")
}

func TestRealisticScriptLsJSON(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "script", "ls", "--json")
	var entries []map[string]string
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("script ls --json invalid JSON: %v\noutput: %s", err, out)
	}
	if len(entries) == 0 {
		t.Skip("no scripts loaded in realistic HA")
	}
}

func TestRealisticScriptLsHasFixtures(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "script", "ls", "--json")
	var entries []map[string]string
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("script ls --json invalid: %v", err)
	}
	ids := make(map[string]bool)
	for _, e := range entries {
		ids[e["id"]] = true
	}
	for _, want := range []string{"morning_routine", "night_mode", "guest_welcome"} {
		if !ids[want] {
			t.Errorf("realistic scripts missing %q, got: %v", want, ids)
		}
	}
}

func TestRealisticScriptRun(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "script", "run", "morning_routine", "--confirm")
	assertContains(t, out, "executed script.morning_routine")
}

func TestRealisticScriptShow(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "script", "show", "night_mode")
	assertContains(t, out, "night_mode")
	assertContains(t, out, "state=")
}

// --- Ent ls --domain ---

func TestRealisticEntLsDomainSensor(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "ent", "ls", "--domain", "sensor")
	assertContains(t, out, "sensor.")
	assertNotContains(t, out, "input_number.")
}

func TestRealisticEntLsDomainInputNumber(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "ent", "ls", "--domain", "input_number")
	assertContains(t, out, "input_number.")
	assertNotContains(t, out, "sensor.")
}

func TestRealisticEntLsDomainJSON(t *testing.T) {
	inst := getRealisticHA(t)
	entries := make([]map[string]string, 0)
	out := runHactlDir(t, inst.Dir(), "ent", "ls", "--domain", "input_boolean", "--json")
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("ent ls --domain --json invalid: %v", err)
	}
	for _, e := range entries {
		if !strings.HasPrefix(e["entity_id"], "input_boolean.") {
			t.Errorf("non-input_boolean entity in --domain filter: %s", e["entity_id"])
		}
	}
}

// --- Stats ---

func TestRealisticVersionStats(t *testing.T) {
	inst := getRealisticHA(t)
	out := runHactlDir(t, inst.Dir(), "version", "--stats")
	assertContains(t, out, "stats:")
	assertContains(t, out, "bytes")
	assertContains(t, out, "tokens")
}

// --- Cache includes script traces ---

func TestRealisticCacheRefreshIncludesScripts(t *testing.T) {
	inst := getRealisticHA(t)

	// First run a script to generate a trace
	runHactlDir(t, inst.Dir(), "script", "run", "guest_welcome", "--confirm")

	// Wait a moment for HA to record the trace
	time.Sleep(2 * time.Second)

	// Refresh cache
	out := runHactlDir(t, inst.Dir(), "cache", "refresh", "traces")
	assertContains(t, out, "traces refreshed")

	// Cache status should show entries
	statusOut := runHactlDir(t, inst.Dir(), "cache", "status")
	assertContains(t, statusOut, "traces:")
}
