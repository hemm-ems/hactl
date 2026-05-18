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
