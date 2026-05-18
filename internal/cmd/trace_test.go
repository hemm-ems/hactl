package cmd

import (
	"testing"
)

func TestParseTraceKey_Valid(t *testing.T) {
	domain, itemID, runID, err := parseTraceKey("automation.climate_schedule/run-001")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if domain != "automation" {
		t.Errorf("domain = %q, want 'automation'", domain)
	}
	if itemID != "climate_schedule" {
		t.Errorf("item_id = %q, want 'climate_schedule'", itemID)
	}
	if runID != "run-001" {
		t.Errorf("run_id = %q, want 'run-001'", runID)
	}
}

func TestParseTraceKey_NoSlash(t *testing.T) {
	_, _, _, parseErr := parseTraceKey("automation.climate_schedule") //nolint:dogsled // testing error return
	if parseErr == nil {
		t.Fatal("expected error for key without slash")
	}
}

func TestParseTraceKey_NoDot(t *testing.T) {
	_, _, _, parseErr := parseTraceKey("invalid/run-001") //nolint:dogsled // testing error return
	if parseErr == nil {
		t.Fatal("expected error for key without dot in entity ID")
	}
}

func TestFormatByteSize(t *testing.T) {
	tests := []struct {
		want  string
		input int64
	}{
		{"0 B", 0},
		{"512 B", 512},
		{"1.0 KB", 1024},
		{"1.5 KB", 1536},
		{"1.0 MB", 1048576},
		{"1.5 MB", 1572864},
	}
	for _, tt := range tests {
		got := formatByteSize(tt.input)
		if got != tt.want {
			t.Errorf("formatByteSize(%d) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatSyncAge_Never(t *testing.T) {
	got := formatSyncAge("")
	if got != "never" {
		t.Errorf("formatSyncAge('') = %q, want 'never'", got)
	}
}

func TestFormatSyncAge_Invalid(t *testing.T) {
	got := formatSyncAge("not-a-date")
	if got != "not-a-date" {
		t.Errorf("formatSyncAge('not-a-date') = %q, want 'not-a-date'", got)
	}
}
