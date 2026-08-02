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
	if !strings.Contains(err.Error(), "hactl setup") {
		t.Errorf("error = %q, want it to contain 'hactl setup'", err.Error())
	}
}

func TestCalverMonth(t *testing.T) {
	tests := []struct {
		version string
		want    int
		wantOK  bool
	}{
		{"2026.8.0", 2026*12 + 8, true},
		{"v2026.8.0", 2026*12 + 8, true},
		{"2026.12", 2026*12 + 12, true},
		{"2026.07.11", 2026*12 + 7, true}, // zero-padded month
		{"dev", 0, false},
		{"", 0, false},
		{"2026", 0, false},    // no month
		{"1.2.3", 0, false},   // semver, not CalVer
		{"5.0.0", 0, false},   // semver whose leading field is a plausible year
		{"2026.13", 0, false}, // month out of range
		{"2026.0.1", 0, false},
		{"2026.x.1", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got, ok := calverMonth(tt.version)
			if ok != tt.wantOK {
				t.Fatalf("calverMonth(%q) ok = %v, want %v", tt.version, ok, tt.wantOK)
			}
			if ok && got != tt.want {
				t.Errorf("calverMonth(%q) = %d, want %d", tt.version, got, tt.want)
			}
		})
	}
}

func TestCheckVersionCompat(t *testing.T) {
	tests := []struct {
		name      string
		hactl     string
		companion string
		wantEmpty bool
	}{
		// The versions this project actually ships. Before CalVer awareness
		// every one of these compared equal on the leading field — the year —
		// so the check was silent no matter how far the two had drifted.
		{"same release", "2026.8.0", "2026.8.0", true},
		{"patch skew within a month", "2026.7.15", "2026.7.11", true},
		{"companion one month behind", "2026.8.0", "2026.7.11", true},
		{"companion two months behind", "2026.8.0", "2026.6.4", true},
		{"companion three months behind - warn", "2026.8.0", "2026.5.4", false},
		{"companion seven months behind - warn", "2026.8.0", "2026.1.0", false},
		{"hactl three months behind - warn", "2026.5.0", "2026.8.1", false},
		{"across a year boundary", "2027.1.0", "2026.12.4", true},
		{"across a year boundary - warn", "2027.1.0", "2026.9.4", false},
		{"a year apart - warn", "2027.8.0", "2026.8.0", false},
		// Not CalVer on one side: no shared scale, so no verdict.
		{"dev build", "dev", "2026.8.0", true},
		{"companion reports nothing useful", "2026.8.0", "unknown", true},
		{"companion reports semver", "2026.8.0", "1.0.0", true},
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
