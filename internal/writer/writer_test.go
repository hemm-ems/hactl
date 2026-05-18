package writer

import (
	"testing"
)

func TestDiffLines_NoChanges(t *testing.T) {
	lines := diffLines("foo\nbar\nbaz\n", "foo\nbar\nbaz\n")
	for _, l := range lines {
		if len(l) > 0 && l[0] != ' ' {
			t.Errorf("expected no changes, got line: %q", l)
		}
	}
}

func TestDiffLines_WithChanges(t *testing.T) {
	lines := diffLines("foo\nbar\nbaz\n", "foo\nqux\nbaz\n")
	hasPlus := false
	hasMinus := false
	for _, l := range lines {
		if len(l) > 0 && l[0] == '+' {
			hasPlus = true
		}
		if len(l) > 0 && l[0] == '-' {
			hasMinus = true
		}
	}
	if !hasPlus || !hasMinus {
		t.Errorf("expected +/- lines in diff, got: %v", lines)
	}
}

func TestDiffLines_Addition(t *testing.T) {
	lines := diffLines("foo\n", "foo\nbar\n")
	hasPlus := false
	for _, l := range lines {
		if len(l) > 0 && l[0] == '+' {
			hasPlus = true
		}
	}
	if !hasPlus {
		t.Error("expected + line for addition")
	}
}

func TestDiffLines_Deletion(t *testing.T) {
	lines := diffLines("foo\nbar\n", "foo\n")
	hasMinus := false
	for _, l := range lines {
		if len(l) > 0 && l[0] == '-' {
			hasMinus = true
		}
	}
	if !hasMinus {
		t.Error("expected - line for deletion")
	}
}

func TestSplitLines(t *testing.T) {
	tests := []struct {
		input string
		want  int
	}{
		{"", 0},
		{"foo", 1},
		{"foo\n", 1},
		{"foo\nbar", 2},
		{"foo\nbar\n", 2},
		{"foo\nbar\nbaz", 3},
	}
	for _, tt := range tests {
		got := splitLines(tt.input)
		if len(got) != tt.want {
			t.Errorf("splitLines(%q) = %d lines, want %d", tt.input, len(got), tt.want)
		}
	}
}

func TestIsYAMLFile(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{"foo.yaml", true},
		{"foo.yml", true},
		{"backup_climate.yaml", true},
		{"foo.json", false},
		{"a.yaml", true},
		{".yaml", false},
		{"test", false},
	}
	for _, tt := range tests {
		got := isYAMLFile(tt.name)
		if got != tt.want {
			t.Errorf("isYAMLFile(%q) = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestContainsAutoID(t *testing.T) {
	tests := []struct {
		filename string
		autoID   string
		want     bool
	}{
		{"2026-04-17T09-42-05_climate_schedule.yaml", "climate_schedule", true},
		{"2026-04-17T09-42-05_alarm_morning.yaml", "alarm_morning", true},
		{"2026-04-17T09-42-05_alarm_morning.yaml", "climate_schedule", false},
	}
	for _, tt := range tests {
		got := containsAutoID(tt.filename, tt.autoID)
		if got != tt.want {
			t.Errorf("containsAutoID(%q, %q) = %v, want %v", tt.filename, tt.autoID, got, tt.want)
		}
	}
}

func TestExtractAutoIDFromBackup(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/backups/2026-04-17T09-42-05_climate_schedule.yaml", "climate_schedule"},
		{"/backups/2026-04-17T09-42-05_alarm_morning.yaml", "alarm_morning"},
	}
	for _, tt := range tests {
		got := extractAutoIDFromBackup(tt.path)
		if got != tt.want {
			t.Errorf("extractAutoIDFromBackup(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}
