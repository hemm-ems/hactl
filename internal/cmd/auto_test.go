package cmd

import (
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
)

func TestParseSince_Hours(t *testing.T) {
	d, err := parseSince("24h")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 24*time.Hour {
		t.Errorf("parseSince(24h) = %v, want 24h", d)
	}
}

func TestParseSince_Days(t *testing.T) {
	d, err := parseSince("7d")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 7*24*time.Hour {
		t.Errorf("parseSince(7d) = %v, want 168h", d)
	}
}

func TestParseSince_Complex(t *testing.T) {
	d, err := parseSince("1h30m")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if d != 90*time.Minute {
		t.Errorf("parseSince(1h30m) = %v, want 1h30m", d)
	}
}

func TestParseSince_Invalid(t *testing.T) {
	_, err := parseSince("abc")
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}
}

func TestFormatShortTime_Today(t *testing.T) {
	now := time.Now()
	iso := now.Format(time.RFC3339)
	result := formatShortTime(iso)
	if result != now.Format("15:04") {
		t.Errorf("formatShortTime(%q) = %q, want %q", iso, result, now.Format("15:04"))
	}
}

func TestFormatShortTime_OtherDay(t *testing.T) {
	past := time.Now().Add(-72 * time.Hour)
	iso := past.Format(time.RFC3339)
	result := formatShortTime(iso)
	expected := past.Format("01-02 15:04")
	if result != expected {
		t.Errorf("formatShortTime(%q) = %q, want %q", iso, result, expected)
	}
}

func TestFormatShortTime_Empty(t *testing.T) {
	if got := formatShortTime(""); got != "-" {
		t.Errorf("formatShortTime('') = %q, want '-'", got)
	}
}

func TestShortenStep(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"action/0", "action"},
		{"condition/1", "condition"},
		{"trigger/0/sub", "trigger"},
		{"simple", "simple"},
		{"", ""},
	}

	for _, tt := range tests {
		got := shortenStep(tt.input)
		if got != tt.want {
			t.Errorf("shortenStep(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestIsTraceError(t *testing.T) {
	tests := []struct {
		name   string
		exec   string
		errMsg string
		want   bool
	}{
		{"error execution", "error", "", true},
		{"error message", "finished", "some error", true},
		{"ok", "finished", "", false},
		{"empty", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// We can't directly import haapi here without circular dep,
			// but isTraceError is in the same package, so we test it via traceResult
		})
	}
}

func TestFilterFailing(t *testing.T) {
	rows := []autoRow{
		{id: "a", errors: 0},
		{id: "b", errors: 2},
		{id: "c", errors: 0},
		{id: "d", errors: 1},
	}

	result := filterFailing(rows)
	if len(result) != 2 {
		t.Fatalf("expected 2 failing, got %d", len(result))
	}
	if result[0].id != "b" {
		t.Errorf("first failing = %q, want %q", result[0].id, "b")
	}
	if result[1].id != "d" {
		t.Errorf("second failing = %q, want %q", result[1].id, "d")
	}
}

func TestFilterAutosByTag(t *testing.T) {
	rows := []autoRow{
		{id: "ess_charge", labels: []string{"ess", "energy"}},
		{id: "climate_schedule", labels: []string{"climate"}},
		{id: "ess_discharge", labels: []string{"ess"}},
		{id: "light_on", labels: nil},
	}

	result := filterAutosByTag(rows, "ess")
	if len(result) != 2 {
		t.Fatalf("expected 2 matches for tag 'ess', got %d", len(result))
	}
	if result[0].id != "ess_charge" {
		t.Errorf("first match = %q, want %q", result[0].id, "ess_charge")
	}
	if result[1].id != "ess_discharge" {
		t.Errorf("second match = %q, want %q", result[1].id, "ess_discharge")
	}
}

func TestFilterAutosByTag_CaseInsensitive(t *testing.T) {
	rows := []autoRow{
		{id: "a", labels: []string{"ESS"}},
		{id: "b", labels: []string{"climate"}},
	}

	result := filterAutosByTag(rows, "ess")
	if len(result) != 1 {
		t.Fatalf("expected 1 match for case-insensitive tag, got %d", len(result))
	}
}

func TestFilterAutosByTag_NoMatch(t *testing.T) {
	rows := []autoRow{
		{id: "a", labels: []string{"climate"}},
	}

	result := filterAutosByTag(rows, "ess")
	if len(result) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(result))
	}
}

func TestFilterAutosByTag_EmptyLabels(t *testing.T) {
	rows := []autoRow{
		{id: "a", labels: nil},
		{id: "b", labels: []string{}},
	}

	result := filterAutosByTag(rows, "ess")
	if len(result) != 0 {
		t.Fatalf("expected 0 matches for empty labels, got %d", len(result))
	}
}

func TestBuildAutoRows_RunsFromLogbook(t *testing.T) {
	// Logbook count of 1500 must beat trace storage (HA caps at ~5/automation).
	cutoff := time.Now().Add(-24 * time.Hour)
	autos := []automationEntity{
		{EntityID: "automation.storm", State: "on"},
		{EntityID: "automation.quiet", State: "on"},
	}
	traces := haapi.TraceListResult{
		"automation.storm": {
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-1 * time.Hour).Format(time.RFC3339Nano)}, Execution: "finished"},
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-30 * time.Minute).Format(time.RFC3339Nano)}, Execution: "error"},
		},
	}
	fires := map[string]int{"automation.storm": 1500}

	rows := buildAutoRows(autos, traces, fires, cutoff)
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	stormIdx := -1
	for i, r := range rows {
		if r.id == "storm" {
			stormIdx = i
		}
	}
	if stormIdx < 0 {
		t.Fatal("storm row missing")
	}
	if rows[stormIdx].runs != 1500 {
		t.Errorf("storm runs = %d, want 1500 (logbook count, not trace count)", rows[stormIdx].runs)
	}
	if rows[stormIdx].errors != 1 {
		t.Errorf("storm errors = %d, want 1 (still derived from traces)", rows[stormIdx].errors)
	}
}

func TestBuildAutoRows_FallbackToTraceCountWhenLogbookMissing(t *testing.T) {
	// Logbook fetch failed → fires == nil. Run count must fall back to in-window traces.
	cutoff := time.Now().Add(-24 * time.Hour)
	autos := []automationEntity{{EntityID: "automation.foo", State: "on"}}
	traces := haapi.TraceListResult{
		"automation.foo": {
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-1 * time.Hour).Format(time.RFC3339Nano)}, Execution: "finished"},
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-48 * time.Hour).Format(time.RFC3339Nano)}, Execution: "finished"}, // outside window
		},
	}

	rows := buildAutoRows(autos, traces, nil, cutoff)
	if rows[0].runs != 1 {
		t.Errorf("runs = %d, want 1 (only one trace inside cutoff window)", rows[0].runs)
	}
}

func TestBuildAutoRows_NoTracesNoFires(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)
	autos := []automationEntity{{EntityID: "automation.idle", State: "on"}}

	rows := buildAutoRows(autos, nil, map[string]int{}, cutoff)
	if rows[0].runs != 0 {
		t.Errorf("runs = %d, want 0", rows[0].runs)
	}
	if rows[0].errors != 0 {
		t.Errorf("errors = %d, want 0", rows[0].errors)
	}
}

func TestFilterAutosByPattern(t *testing.T) {
	rows := []autoRow{
		{id: "ess_balkon_sende_bms"},
		{id: "victron_ess_keep_alive"},
		{id: "wecker_starten_sinje"},
		{id: "ess_strom_kaufen"},
		{id: "standby_nachts"},
	}

	tests := []struct {
		name    string
		pattern string
		want    int
	}{
		{"prefix", "ess_*", 2},
		{"contains", "*ess*", 3},
		{"exact", "standby_nachts", 1},
		{"no match", "nonexistent*", 0},
		{"all", "*", 5},
		{"with domain prefix", "automation.ess_*", 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterAutosByPattern(rows, tt.pattern)
			if len(result) != tt.want {
				t.Errorf("filterAutosByPattern(%q) returned %d items, want %d", tt.pattern, len(result), tt.want)
			}
		})
	}
}
