package cmd

import (
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/analyze"
)

func TestCountErrorEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []analyze.LogEntry
		want    int
	}{
		{
			name:    "no errors",
			entries: []analyze.LogEntry{{Level: "INFO"}, {Level: "WARNING"}},
			want:    0,
		},
		{
			name:    "two errors",
			entries: []analyze.LogEntry{{Level: "ERROR"}, {Level: "INFO"}, {Level: "ERROR"}},
			want:    2,
		},
		{
			name:    "empty",
			entries: nil,
			want:    0,
		},
		{
			name:    "case-sensitive: only exact ERROR counts",
			entries: []analyze.LogEntry{{Level: "ERROR"}, {Level: "error"}, {Level: "Error"}},
			want:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := countErrorEntries(tt.entries)
			if got != tt.want {
				t.Errorf("countErrorEntries() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestHealthCommand_NoEnv(t *testing.T) {
	// Call health without a valid instance directory → should fail with useful error.
	dir := t.TempDir() // no .env file

	rootCmd.SetArgs([]string{"health", "--dir", dir})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cannot open .env") {
		t.Errorf("error = %q, want it to contain %q", err.Error(), "cannot open .env")
	}
}

func TestParseMajor(t *testing.T) {
	tests := []struct {
		version string
		want    int
	}{
		{"1.2.3", 1},
		{"v2.0.0", 2},
		{"0.5.1", 0},
		{"dev", -1},
		{"", -1},
		{"3", 3},
		{"v10.1", 10},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			if got := parseMajor(tt.version); got != tt.want {
				t.Errorf("parseMajor(%q) = %d, want %d", tt.version, got, tt.want)
			}
		})
	}
}

func TestCheckVersionCompat(t *testing.T) {
	tests := []struct {
		name       string
		hactl      string
		companion  string
		wantEmpty  bool
	}{
		{"same version", "1.0.0", "1.0.0", true},
		{"minor diff", "1.0.0", "1.5.0", true},
		{"major diff 1", "2.0.0", "1.0.0", true},
		{"major diff 2", "3.0.0", "1.0.0", true},
		{"major diff 3 - warn", "4.0.0", "1.0.0", false},
		{"major diff 5 - warn", "5.0.0", "0.1.0", false},
		{"unparseable hactl", "dev", "1.0.0", true},
		{"unparseable companion", "1.0.0", "unknown", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := checkVersionCompat(tt.hactl, tt.companion)
			if tt.wantEmpty && got != "" {
				t.Errorf("checkVersionCompat(%q, %q) = %q, want empty", tt.hactl, tt.companion, got)
			}
			if !tt.wantEmpty && got == "" {
				t.Errorf("checkVersionCompat(%q, %q) = empty, want warning", tt.hactl, tt.companion)
			}
		})
	}
}
