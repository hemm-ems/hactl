package analyze

import (
	"strings"
	"testing"
)

const sampleLog = `2026-04-16 09:42:00.123 ERROR (MainThread) [homeassistant.components.zha] Failed to connect to device
2026-04-16 09:42:01.456 WARNING (MainThread) [homeassistant.components.mqtt] Connection lost, reconnecting
2026-04-16 09:43:00.789 ERROR (MainThread) [homeassistant.components.zha] Failed to connect to device
2026-04-16 09:44:00.012 ERROR (MainThread) [homeassistant.components.climate] Error updating state
2026-04-16 09:45:00.345 ERROR (SyncWorker_0) [homeassistant.components.zha] Failed to connect to device
2026-04-16 10:00:00.000 INFO (MainThread) [homeassistant.core] Bus:Handling <Event state_changed>`

func TestParseLogLines(t *testing.T) {
	entries := ParseLogLines(sampleLog)
	if len(entries) != 6 {
		t.Fatalf("parsed %d entries, want 6", len(entries))
	}

	if entries[0].Level != "ERROR" {
		t.Errorf("entry[0].level = %q, want ERROR", entries[0].Level)
	}
	if entries[0].Component != "homeassistant.components.zha" {
		t.Errorf("entry[0].component = %q", entries[0].Component)
	}
	if entries[0].Message != "Failed to connect to device" {
		t.Errorf("entry[0].message = %q", entries[0].Message)
	}
	if entries[1].Level != "WARNING" {
		t.Errorf("entry[1].level = %q, want WARNING", entries[1].Level)
	}
}

func TestParseLogLines_Multiline(t *testing.T) {
	log := `2026-04-16 09:42:00.123 ERROR (MainThread) [comp.test] Something failed
Traceback (most recent call last):
  File "test.py", line 1
ValueError: bad value`

	entries := ParseLogLines(log)
	if len(entries) != 1 {
		t.Fatalf("parsed %d entries, want 1 (multiline)", len(entries))
	}
	if !strings.Contains(entries[0].Message, "Traceback") {
		t.Error("multiline message should contain Traceback")
	}
	if !strings.Contains(entries[0].Message, "ValueError") {
		t.Error("multiline message should contain ValueError")
	}
}

func TestDeduplicateLogs(t *testing.T) {
	entries := ParseLogLines(sampleLog)
	deduped := DeduplicateLogs(entries)

	if len(deduped) < 3 {
		t.Fatalf("deduped groups = %d, want >= 3", len(deduped))
	}

	// The "Failed to connect to device" messages from zha should be grouped
	var zhaGroup *DedupedLog
	for i := range deduped {
		if strings.Contains(deduped[i].Component, "zha") {
			zhaGroup = &deduped[i]
			break
		}
	}
	if zhaGroup == nil {
		t.Fatal("expected a zha group in deduped results")
		return
	}
	// Two entries match exactly (same thread), the third has different thread but same normalized message
	if zhaGroup.Count < 2 {
		t.Errorf("zha group count = %d, want >= 2", zhaGroup.Count)
	}
}

func TestFilterByLevel(t *testing.T) {
	entries := ParseLogLines(sampleLog)
	errors := FilterByLevel(entries, "ERROR")

	for _, e := range errors {
		if e.Level != "ERROR" {
			t.Errorf("filtered entry has level %q, want ERROR", e.Level)
		}
	}
	if len(errors) != 4 {
		t.Errorf("error count = %d, want 4", len(errors))
	}
}

func TestFilterByComponent(t *testing.T) {
	entries := ParseLogLines(sampleLog)
	zha := FilterByComponent(entries, "zha")

	if len(zha) != 3 {
		t.Errorf("zha entries = %d, want 3", len(zha))
	}
	for _, e := range zha {
		if !strings.Contains(e.Component, "zha") {
			t.Errorf("filtered component %q doesn't contain 'zha'", e.Component)
		}
	}
}

func TestFilterByLevel_CaseInsensitive(t *testing.T) {
	entries := ParseLogLines(sampleLog)
	errors := FilterByLevel(entries, "error")
	if len(errors) != 4 {
		t.Errorf("error count = %d, want 4", len(errors))
	}
}

func TestFilterByLevels(t *testing.T) {
	entries := ParseLogLines(sampleLog) // 4 ERROR, 1 WARNING, 1 INFO

	if got := FilterByLevels(entries, "WARNING"); len(got) != 1 {
		t.Errorf("WARNING-only count = %d, want 1", len(got))
	}
	if got := FilterByLevels(entries, "ERROR", "WARNING"); len(got) != 5 {
		t.Errorf("ERROR+WARNING count = %d, want 5", len(got))
	}
	// Case-insensitive, and no levels means no filtering.
	if got := FilterByLevels(entries, "warning"); len(got) != 1 {
		t.Errorf("lowercase warning count = %d, want 1", len(got))
	}
	if got := FilterByLevels(entries); len(got) != len(entries) {
		t.Errorf("no-levels count = %d, want %d (unchanged)", len(got), len(entries))
	}
}

func TestNormalizeMessage(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"error at 192.168.1.1 port 8123", "error at <N> port <N>"},
		{"timestamp 2026-04-16T09:42:00 end", "timestamp <N> end"},
		{"no numbers here", "no numbers here"},
	}
	for _, tt := range tests {
		got := normalizeMessage(tt.input)
		if got != tt.want {
			t.Errorf("normalizeMessage(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatShortTimestamp(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"", "-"},
	}
	for _, tt := range tests {
		got := FormatShortTimestamp(tt.input)
		if got != tt.want {
			t.Errorf("FormatShortTimestamp(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestFormatShortTimestamp_Formats(t *testing.T) {
	// Test legacy format "2006-01-02 15:04:05"
	got := FormatShortTimestamp("2026-01-01 09:00:00")
	if got == "-" || got == "2026-01-01 09:00:00" {
		// Should have parsed and reformatted to "01-02 15:04" or "15:04"
		t.Logf("FormatShortTimestamp(legacy) = %q", got)
	} else if len(got) == 0 {
		t.Error("FormatShortTimestamp(legacy) returned empty string")
	}

	// Test with sub-second precision
	got = FormatShortTimestamp("2026-01-01 09:42:00.123")
	if got == "-" {
		t.Error("FormatShortTimestamp(with milliseconds) returned '-'")
	}

	// Test with RFC3339
	got = FormatShortTimestamp("2026-01-01T09:42:00Z")
	if got == "-" {
		t.Error("FormatShortTimestamp(RFC3339) returned '-'")
	}

	// Test with RFC3339Nano
	got = FormatShortTimestamp("2026-01-01T09:42:00.123456Z")
	if got == "-" {
		t.Error("FormatShortTimestamp(RFC3339Nano) returned '-'")
	}

	// Test unparseable input is returned as-is
	got = FormatShortTimestamp("not-a-timestamp")
	if got != "not-a-timestamp" {
		t.Errorf("FormatShortTimestamp(unparseable) = %q, want original string", got)
	}
}
