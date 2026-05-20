package analyze

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCondense_PassingTrace(t *testing.T) {
	raw := loadTestTrace(t, "climate_schedule_pass.json")
	ct := Condense(raw)

	if ct.Result != StepPass {
		t.Errorf("result = %q, want %q", ct.Result, StepPass)
	}
	if ct.AutoID != "automation.climate_schedule" {
		t.Errorf("auto_id = %q, want %q", ct.AutoID, "automation.climate_schedule")
	}
	if ct.Trigger != "time_pattern" {
		t.Errorf("trigger = %q, want %q", ct.Trigger, "time_pattern")
	}
	if len(ct.Steps) != 4 {
		t.Fatalf("steps = %d, want 4", len(ct.Steps))
	}

	// Verify step types: trigger, 2x condition, action
	expectedTypes := []StepType{StepTrigger, StepCondition, StepCondition, StepAction}
	for i, expected := range expectedTypes {
		if ct.Steps[i].Type != expected {
			t.Errorf("step[%d].type = %q, want %q", i, ct.Steps[i].Type, expected)
		}
	}

	// All steps should pass
	for i, s := range ct.Steps {
		if s.Result != StepPass {
			t.Errorf("step[%d].result = %q, want %q", i, s.Result, StepPass)
		}
	}
}

func TestCondense_FailingTrace(t *testing.T) {
	raw := loadTestTrace(t, "climate_schedule_fail.json")
	ct := Condense(raw)

	if ct.Result != StepFail {
		t.Errorf("result = %q, want %q", ct.Result, StepFail)
	}
	if ct.RunID != "run-fail-002" {
		t.Errorf("run_id = %q, want %q", ct.RunID, "run-fail-002")
	}

	// Should have steps; condition/1 should be the failing one
	var hasFail bool
	for _, s := range ct.Steps {
		if s.Result == StepFail {
			hasFail = true
			break
		}
	}
	if !hasFail {
		t.Error("expected at least one FAIL step in failing trace")
	}
}

func TestCondense_SimpleTrace(t *testing.T) {
	raw := loadTestTrace(t, "alarm_morning_pass.json")
	ct := Condense(raw)

	if ct.Result != StepPass {
		t.Errorf("result = %q, want %q", ct.Result, StepPass)
	}
	if ct.AutoID != "automation.alarm_morning" {
		t.Errorf("auto_id = %q, want %q", ct.AutoID, "automation.alarm_morning")
	}
	if len(ct.Steps) != 2 {
		t.Fatalf("steps = %d, want 2 (trigger + action)", len(ct.Steps))
	}
	if ct.Steps[0].Type != StepTrigger {
		t.Errorf("step[0].type = %q, want trigger", ct.Steps[0].Type)
	}
	if ct.Steps[1].Type != StepAction {
		t.Errorf("step[1].type = %q, want action", ct.Steps[1].Type)
	}
}

func TestFormatCondensed(t *testing.T) {
	raw := loadTestTrace(t, "climate_schedule_fail.json")
	ct := Condense(raw)
	out := FormatCondensed(ct)

	if out == "" {
		t.Fatal("FormatCondensed returned empty string")
	}
	if !contains(out, "FAIL") {
		t.Error("output should contain FAIL")
	}
	if !contains(out, "automation.climate_schedule") {
		t.Error("output should contain automation ID")
	}
}

func TestShortenError(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"TemplateError: UndefinedError: 'unknown' is undefined", "'unknown' is undefined"},
		{"short", "short"},
		{"", ""},
	}

	for _, tt := range tests {
		got := shortenError(tt.input)
		if got != tt.want {
			t.Errorf("shortenError(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestShortTimestamp(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"2026-04-16T09:42:00.000000+00:00", "09:42:00"},
		{"2026-04-16T09:42:00+00:00", "09:42:00"},
		{"plain", "plain"},
	}

	for _, tt := range tests {
		got := shortTimestamp(tt.input)
		if got != tt.want {
			t.Errorf("shortTimestamp(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestClassifyStep(t *testing.T) {
	tests := []struct {
		path string
		want StepType
	}{
		{"trigger/0", StepTrigger},
		{"condition/0", StepCondition},
		{"condition/1", StepCondition},
		{"action/0", StepAction},
		{"action/1/something", StepAction},
	}

	for _, tt := range tests {
		got := classifyStep(tt.path)
		if got != tt.want {
			t.Errorf("classifyStep(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestParseTrigger_String(t *testing.T) {
	raw := json.RawMessage(`"time_pattern"`)
	got := parseTrigger(raw)
	if got != "time_pattern" {
		t.Errorf("parseTrigger(string) = %q, want 'time_pattern'", got)
	}
}

func TestParseTrigger_Array(t *testing.T) {
	raw := json.RawMessage(`["state","time"]`)
	got := parseTrigger(raw)
	if got != "state, time" {
		t.Errorf("parseTrigger(array) = %q, want 'state, time'", got)
	}
}

func TestParseTrigger_RawJSON(t *testing.T) {
	raw := json.RawMessage(`{"platform":"state"}`)
	got := parseTrigger(raw)
	if got != `{"platform":"state"}` {
		t.Errorf("parseTrigger(raw JSON) = %q, want raw string", got)
	}
}

func TestParseTrigger_Empty(t *testing.T) {
	got := parseTrigger(nil)
	if got != "" {
		t.Errorf("parseTrigger(nil) = %q, want empty", got)
	}
}

func TestExtractTriggerDetail_WithPlatform(t *testing.T) {
	run := RawTraceRun{
		ChangedVariables: json.RawMessage(`{"trigger":{"platform":"time_pattern"}}`),
	}
	got := extractTriggerDetail(run)
	if got != "time_pattern" {
		t.Errorf("extractTriggerDetail = %q, want 'time_pattern'", got)
	}
}

func TestExtractTriggerDetail_NoPlatform(t *testing.T) {
	run := RawTraceRun{
		ChangedVariables: json.RawMessage(`{"trigger":{"other":"value"}}`),
	}
	got := extractTriggerDetail(run)
	if got != "" {
		t.Errorf("extractTriggerDetail without platform = %q, want empty", got)
	}
}

func TestExtractTriggerDetail_NoTriggerKey(t *testing.T) {
	run := RawTraceRun{
		ChangedVariables: json.RawMessage(`{"other_key":"value"}`),
	}
	got := extractTriggerDetail(run)
	if got != "" {
		t.Errorf("extractTriggerDetail no trigger key = %q, want empty", got)
	}
}

func TestExtractTriggerDetail_Empty(t *testing.T) {
	run := RawTraceRun{}
	got := extractTriggerDetail(run)
	if got != "" {
		t.Errorf("extractTriggerDetail empty = %q, want empty", got)
	}
}

func TestExtractConditionDetail_Path(t *testing.T) {
	run := RawTraceRun{Path: "condition/1"}
	got := extractConditionDetail(run)
	if got != "condition" {
		t.Errorf("extractConditionDetail = %q, want 'condition'", got)
	}
}

func TestExtractConditionDetail_Empty(t *testing.T) {
	run := RawTraceRun{Path: ""}
	got := extractConditionDetail(run)
	if got != "" {
		t.Errorf("extractConditionDetail empty path = %q, want empty", got)
	}
}

func TestExtractActionDetail_WithEntityID(t *testing.T) {
	run := RawTraceRun{
		Result: json.RawMessage(`{"params":{"entity_id":"light.kitchen"}}`),
	}
	got := extractActionDetail(run)
	if got != "light.kitchen" {
		t.Errorf("extractActionDetail with entity_id = %q, want 'light.kitchen'", got)
	}
}

func TestExtractActionDetail_ServiceCall(t *testing.T) {
	run := RawTraceRun{
		Result: json.RawMessage(`{"params":{"domain":"light"}}`),
	}
	got := extractActionDetail(run)
	if got != "service_call" {
		t.Errorf("extractActionDetail without entity_id = %q, want 'service_call'", got)
	}
}

func TestExtractActionDetail_Empty(t *testing.T) {
	run := RawTraceRun{}
	got := extractActionDetail(run)
	if got != "" {
		t.Errorf("extractActionDetail empty = %q, want empty", got)
	}
}

func TestStepOutcome_Error(t *testing.T) {
	run := RawTraceRun{Error: "TemplateError: undefined"}
	result, reason := stepOutcome(run)
	if result != StepFail {
		t.Errorf("stepOutcome with error: result = %q, want StepFail", result)
	}
	if reason == "" {
		t.Error("stepOutcome with error: reason should not be empty")
	}
}

func TestStepOutcome_ConditionFalse(t *testing.T) {
	run := RawTraceRun{
		Result: json.RawMessage(`{"result":false}`),
	}
	result, reason := stepOutcome(run)
	if result != StepFail {
		t.Errorf("stepOutcome condition false: result = %q, want StepFail", result)
	}
	if reason != "condition_false" {
		t.Errorf("stepOutcome condition false: reason = %q, want 'condition_false'", reason)
	}
}

func TestStepOutcome_Pass(t *testing.T) {
	run := RawTraceRun{}
	result, _ := stepOutcome(run)
	if result != StepPass {
		t.Errorf("stepOutcome pass: result = %q, want StepPass", result)
	}
}

func TestStepHasError_WithError(t *testing.T) {
	runs := []RawTraceRun{
		{Error: "something failed"},
		{},
	}
	if !stepHasError(runs) {
		t.Error("stepHasError: expected true with error, got false")
	}
}

func TestStepHasError_NoError(t *testing.T) {
	runs := []RawTraceRun{{}, {}}
	if stepHasError(runs) {
		t.Error("stepHasError: expected false with no errors, got true")
	}
}

func TestShortenError_Long(t *testing.T) {
	long := "TemplateError: some long complex: inner message that is definitely over forty characters long"
	got := shortenError(long)
	if len(got) > 40 {
		t.Errorf("shortenError: result = %q (len %d), want <= 40 chars", got, len(got))
	}
}

func loadTestTrace(t *testing.T, name string) *RawTrace {
	t.Helper()
	path := filepath.Join("..", "..", "testdata", "traces", name)
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("reading test trace %s: %v", name, err)
	}
	var raw RawTrace
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("parsing test trace %s: %v", name, err)
	}
	return &raw
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := range len(s) - len(substr) + 1 {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
