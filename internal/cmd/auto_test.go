package cmd

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
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

// pinLocalZone points time.Local at a fixed zone for the duration of the test,
// so a renderer that formats in the reader's zone can be tested for a definite
// answer instead of one that depends on where the suite happens to run.
//
//nolint:gosmopolitan // Pinning the zone is the only way to make a local-time renderer testable in CI, which runs in UTC.
func pinLocalZone(t *testing.T, zone *time.Location) {
	t.Helper()

	restore := time.Local
	time.Local = zone

	t.Cleanup(func() { time.Local = restore })
}

// TestFormatShortTime_UTCWireRendersInLocalTime pins the shape Home Assistant
// actually sends. HA reports last_changed/last_updated in UTC ("…Z"), but the
// two tests above build their input with time.Now().Format(RFC3339), which
// carries the *local* offset — a shape the wire never produces. So neither of
// them could see that the renderer printed the UTC wall-clock as if it were
// local, and compared a UTC calendar day against a local "today".
func TestFormatShortTime_UTCWireRendersInLocalTime(t *testing.T) {
	// A zone east of UTC, fixed so this test means the same thing in CI (which
	// runs in UTC) as it does on a developer's machine.
	zone := time.FixedZone("TEST+02", 2*60*60)
	pinLocalZone(t, zone)

	// 00:30 local today. The same instant is 22:30 *yesterday* in UTC, which is
	// what the wire carries — so this is a "today" timestamp whatever the hour,
	// and the test does not depend on when it runs.
	now := time.Now()
	local := time.Date(now.Year(), now.Month(), now.Day(), 0, 30, 0, 0, zone)
	iso := local.UTC().Format(time.RFC3339)

	if got, want := formatShortTime(iso), "00:30"; got != want {
		t.Errorf("formatShortTime(%q) = %q, want %q — today's timestamp, in local time", iso, got, want)
	}
}

func TestFormatShortTime_Empty(t *testing.T) {
	if got := formatShortTime(""); got != "-" {
		t.Errorf("formatShortTime('') = %q, want '-'", got)
	}
}

func TestFormatShortTime_InvalidString(t *testing.T) {
	// Completely unparseable string → returned as-is
	got := formatShortTime("not-a-time")
	if got != "not-a-time" {
		t.Errorf("formatShortTime(invalid) = %q, want 'not-a-time'", got)
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

// TestIsTraceError pins how a run is judged failed, and what `auto ls` prints
// for it.
//
// The body of this test used to be a table, a t.Run per row, and an empty
// closure carrying a comment about a circular import that does not exist — this
// file imports haapi twenty lines further down. So the four cases were declared
// and never run: the test named after the function never called it. It now
// covers isTraceError and the traceResult rendering it feeds, including the
// row (`empty`) that the single-case tests below never reached.
func TestIsTraceError(t *testing.T) {
	tests := []struct {
		name       string
		exec       string
		errMsg     string
		state      string
		want       bool
		wantResult string
	}{
		{"error execution", "error", "", "stopped", true, "error"},
		// An execution HA called finished is still a failure when it carries an
		// error message. Losing this row is how a failing automation renders as
		// a clean run.
		{"error message", "finished", "some error", "stopped", true, "error"},
		{"ok", "finished", "", "stopped", false, "finished"},
		// No script_execution at all: the entity state is the only thing left
		// to report, and it must not be spelled "error".
		{"empty", "", "", "stopped", false, "stopped"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr := haapi.TraceSummary{Execution: tt.exec, Error: tt.errMsg, State: tt.state}
			if got := isTraceError(tr); got != tt.want {
				t.Errorf("isTraceError(%+v) = %v, want %v", tr, got, tt.want)
			}
			if got := traceResult(tr); got != tt.wantResult {
				t.Errorf("traceResult(%+v) = %q, want %q", tr, got, tt.wantResult)
			}
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
		{id: "ess_charge", labels: "ess, energy"},
		{id: "climate_schedule", labels: "climate"},
		{id: "ess_discharge", labels: "ess"},
		{id: "light_on", labels: ""},
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
		{id: "a", labels: "ESS"},
		{id: "b", labels: "climate"},
	}

	result := filterAutosByTag(rows, "ess")
	if len(result) != 1 {
		t.Fatalf("expected 1 match for case-insensitive tag, got %d", len(result))
	}
}

func TestFilterAutosByTag_NoMatch(t *testing.T) {
	rows := []autoRow{
		{id: "a", labels: "climate"},
	}

	result := filterAutosByTag(rows, "ess")
	if len(result) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(result))
	}
}

func TestFilterAutosByTag_EmptyLabels(t *testing.T) {
	rows := []autoRow{
		{id: "a", labels: ""},
		{id: "b", labels: ""},
	}

	result := filterAutosByTag(rows, "ess")
	if len(result) != 0 {
		t.Fatalf("expected 0 matches for empty labels, got %d", len(result))
	}
}

// logbookSaid and logbookUnreadable are the two states invariant H-18 keeps
// apart, named so a test can say which one it means instead of leaving it to a
// nil check the reader has to decode.
//
// logbookSaid is the logbook answering: the map is its complete answer, and an
// automation absent from it ran zero times. logbookUnreadable is the logbook not
// answering at all — the only case in which the traces stand in.
func logbookSaid(counts map[string]int) fireCounts {
	return fireCounts{byEntityID: counts, ok: true}
}

var logbookUnreadable = fireCounts{}

// TestFireCounts_AnsweredZeroIsNotUnknown pins the distinction H-18 rests on at
// its source: a logbook that answered "no entries for this automation" reports
// zero runs and reports that it answered, while a logbook that could not be read
// reports that it did not. The old code expressed both as a missing map key and
// so could not tell them apart (D65).
func TestFireCounts_AnsweredZeroIsNotUnknown(t *testing.T) {
	answered := logbookSaid(map[string]int{"automation.busy": 7})

	if n, ok := answered.runs("automation.busy"); n != 7 || !ok {
		t.Errorf("runs(known) = (%d, %v), want (7, true)", n, ok)
	}
	// The automation the logbook never mentioned: an answer, and the answer is 0.
	if n, ok := answered.runs("automation.silent"); n != 0 || !ok {
		t.Errorf("runs(absent from an answered logbook) = (%d, %v), want (0, true) — "+
			"an answered logbook that holds no entry for an automation says it did not run", n, ok)
	}
	// The logbook could not be read: not an answer, whatever the number.
	if n, ok := logbookUnreadable.runs("automation.busy"); n != 0 || ok {
		t.Errorf("runs(unreadable logbook) = (%d, %v), want (0, false)", n, ok)
	}
}

// TestFetchAutomationFireCounts_ClassifiesTheLogbooksAnswer covers the wire end
// of H-18: which of the two states a real logbook response lands in.
//
// The subtle case is the third one. Excluding the automation domain from the
// recorder or the logbook is ordinary HA tuning, and then /api/logbook answers
// 200 with entries that never mention an automation — for the whole instance,
// forever. Treating that as an answer would report runs_24h = 0 everywhere, so
// it counts as "could not be read" and the traces stand in.
func TestFetchAutomationFireCounts_ClassifiesTheLogbooksAnswer(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantOK  bool
		wantMap map[string]int
	}{
		{
			name:    "entries for automations",
			body:    `[{"when":"2026-07-26T08:00:00.000Z","domain":"automation","entity_id":"automation.a"},
				{"when":"2026-07-26T09:00:00.000Z","domain":"automation","entity_id":"automation.a"},
				{"when":"2026-07-26T10:00:00.000Z","domain":"automation","entity_id":"automation.b"}]`,
			wantOK:  true,
			wantMap: map[string]int{"automation.a": 2, "automation.b": 1},
		},
		{
			name:   "a busy logbook that never mentions an automation",
			body:   `[{"when":"2026-07-26T08:00:00.000Z","domain":"light","entity_id":"light.kitchen"},
				{"when":"2026-07-26T09:00:00.000Z","domain":"sensor","entity_id":"sensor.temp"}]`,
			wantOK: false,
		},
		{
			name:   "an empty logbook",
			body:   `[]`,
			wantOK: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if !strings.HasPrefix(r.URL.Path, "/api/logbook/") {
					http.Error(w, "not found", http.StatusNotFound)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprint(w, tt.body)
			}))
			defer srv.Close()

			got, err := fetchAutomationFireCounts(context.Background(), haapi.New(srv.URL, "tok"), 24*time.Hour)
			if err != nil {
				t.Fatalf("fetchAutomationFireCounts: %v", err)
			}
			if got.ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v — %q", got.ok, tt.wantOK, tt.name)
			}
			for entityID, want := range tt.wantMap {
				if n, answered := got.runs(entityID); n != want || !answered {
					t.Errorf("runs(%q) = (%d, %v), want (%d, true)", entityID, n, answered, want)
				}
			}
		})
	}
}

// TestBuildAutoRows_BlockedTriggersAreNotRuns is the direct D65 unit regression,
// in both of the states H-18 keeps apart. Every trigger of this automation was
// stopped by its condition: HA traced three, entered its actions none of them,
// and wrote no logbook entry. runs_24h must be 0 whether the logbook answered
// (and did not mention it) or could not be read at all — otherwise the column
// means "runs" in one state and "triggers" in the other.
func TestBuildAutoRows_BlockedTriggersAreNotRuns(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)
	autos := []automationEntity{{EntityID: "automation.blocked", State: "on"}}
	traces := haapi.TraceListResult{
		"automation.blocked": {
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-3 * time.Hour).Format(time.RFC3339Nano)}, Execution: "failed_conditions"},
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)}, Execution: "failed_conditions"},
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-1 * time.Hour).Format(time.RFC3339Nano)}, Execution: "failed_conditions"},
		},
	}

	states := map[string]fireCounts{
		// The logbook is recording automations — it just has nothing to say
		// about this one, because this one never ran.
		"logbook answered, silent about it": logbookSaid(map[string]int{"automation.other": 4}),
		"logbook could not be read":         logbookUnreadable,
	}
	for name, fires := range states {
		t.Run(name, func(t *testing.T) {
			rows := buildAutoRows(autos, traces, fires, cutoff)
			if rows[0].runs != 0 {
				t.Errorf("runs = %d, want 0 — HA traced 3 triggers and stopped every one at the "+
					"condition, so the automation never entered its actions and `auto show` lists "+
					"three rows all marked failed_conditions. Reporting %d is one automation "+
					"answering two different ways in two commands (H-18).", rows[0].runs, rows[0].runs)
			}
		})
	}
}

// TestBuildAutoRows_AnsweredSilenceIsZeroNotUnknown pins the half of H-18 that
// the pre-fix code could not express at all: a logbook that answered, and that
// demonstrably records automation runs, saying nothing about THIS automation is
// a zero — not a reason to consult the traces.
//
// If a human ever decides HA's traces should outrank the logbook's silence (see
// the recorder-exclusion note on fetchAutomationFireCounts), this is the test to
// change, and it is the only one.
func TestBuildAutoRows_AnsweredSilenceIsZeroNotUnknown(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)
	autos := []automationEntity{{EntityID: "automation.quiet", State: "on"}}
	// Traces that WOULD count as runs — the two sources genuinely disagree, so
	// the row's number says which one hactl believes.
	traces := haapi.TraceListResult{
		"automation.quiet": {
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)}, Execution: "finished"},
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-1 * time.Hour).Format(time.RFC3339Nano)}, Execution: "finished"},
		},
	}

	rows := buildAutoRows(autos, traces, logbookSaid(map[string]int{"automation.other": 9}), cutoff)
	if rows[0].runs != 0 {
		t.Errorf("runs = %d, want 0 — the logbook answered and records automation runs "+
			"(it counted 9 for automation.other) but holds none for automation.quiet, so it "+
			"says this automation did not run. An answered logbook is not a missing one (H-18).",
			rows[0].runs)
	}
}

// TestBuildAutoRows_TraceFallbackCountsRunsNotTriggers pins the other half of
// H-18: when the logbook cannot be read the traces stand in, but they are
// filtered by the SAME definition of a run. Otherwise the column would mean
// "runs" with a logbook and "triggers" without one.
func TestBuildAutoRows_TraceFallbackCountsRunsNotTriggers(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)
	autos := []automationEntity{{EntityID: "automation.mixed", State: "on"}}
	traces := haapi.TraceListResult{
		"automation.mixed": {
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-5 * time.Hour).Format(time.RFC3339Nano)}, Execution: "failed_conditions"},
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-4 * time.Hour).Format(time.RFC3339Nano)}, Execution: "failed_conditions"},
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-3 * time.Hour).Format(time.RFC3339Nano)}, Execution: "finished"},
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-2 * time.Hour).Format(time.RFC3339Nano)}, Execution: "error"},
			// Still running: no script_execution yet, but it is past its
			// conditions by definition, so it counts.
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-1 * time.Hour).Format(time.RFC3339Nano)}, State: "running"},
			// Outside the window entirely.
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-30 * time.Hour).Format(time.RFC3339Nano)}, Execution: "finished"},
		},
	}

	rows := buildAutoRows(autos, traces, logbookUnreadable, cutoff)
	if rows[0].runs != 3 {
		t.Errorf("runs = %d, want 3 — 5 in-window traces of which 2 were stopped at the "+
			"condition; the fallback must count runs by the same definition the logbook "+
			"uses, not triggers (H-18)", rows[0].runs)
	}
	// An errored run is still a run, and it is still an error.
	if rows[0].errors != 1 {
		t.Errorf("errors = %d, want 1", rows[0].errors)
	}
}

// TestTraceIsRun_MatchesTheWordAutoShowPrints keeps `auto ls`'s counting rule and
// `auto show`'s outcome column on one constant. If they ever diverge the two
// commands report two different truths about the same trace (H-18).
func TestTraceIsRun_MatchesTheWordAutoShowPrints(t *testing.T) {
	tests := []struct {
		name      string
		tr        haapi.TraceSummary
		isRun     bool
		wantShown string
	}{
		{"condition blocked it", haapi.TraceSummary{Execution: "failed_conditions"}, false, "failed_conditions"},
		{"ran to the end", haapi.TraceSummary{Execution: "finished"}, true, "finished"},
		{"ran and failed", haapi.TraceSummary{Execution: "error"}, true, "error"},
		{"ran and errored out mid-script", haapi.TraceSummary{Execution: "finished", Error: "boom"}, true, "error"},
		{"still in flight", haapi.TraceSummary{State: "running"}, true, "running"},
		{"aborted after its conditions passed", haapi.TraceSummary{Execution: "aborted"}, true, "aborted"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := traceIsRun(tt.tr); got != tt.isRun {
				t.Errorf("traceIsRun(%+v) = %v, want %v", tt.tr, got, tt.isRun)
			}
			if got := traceResult(tt.tr); got != tt.wantShown {
				t.Errorf("traceResult(%+v) = %q, want %q", tt.tr, got, tt.wantShown)
			}
			// The cross-command tie: `auto show` prints failed_conditions for
			// exactly the traces `auto ls` refuses to count, and for no others.
			if shownBlocked := traceResult(tt.tr) == traceFailedConditions; shownBlocked == traceIsRun(tt.tr) {
				t.Errorf("traceResult(%+v) = %q but traceIsRun = %v — the word `auto show` prints "+
					"and the rule `auto ls` counts by have come apart",
					tt.tr, traceResult(tt.tr), traceIsRun(tt.tr))
			}
		})
	}
}

// TestRuns24hReconcilesWithAutoShowTraceTable is the H-11-class cross-command
// check at the unit level: given ONE set of traces, the number `auto ls` puts in
// runs_24h must equal the number of rows `auto show` renders that it does not
// itself mark failed_conditions. The integration tier proves the same against a
// live HA; this proves the two derivations cannot drift apart in the first place.
func TestRuns24hReconcilesWithAutoShowTraceTable(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)
	at := func(ago time.Duration, exec string) haapi.TraceSummary {
		return haapi.TraceSummary{
			Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-ago).Format(time.RFC3339Nano)},
			Execution: exec,
		}
	}
	ts := []haapi.TraceSummary{
		at(5*time.Hour, "failed_conditions"),
		at(4*time.Hour, "failed_conditions"),
		at(3*time.Hour, "finished"),
		at(2*time.Hour, "error"),
		at(1*time.Hour, "finished"),
	}
	autos := []automationEntity{
		{EntityID: "automation.gated", State: "on", Attributes: automationAttributes{ID: "cfgid_gated"}},
	}
	rows := buildAutoRows(autos, haapi.TraceListResult{"automation.cfgid_gated": ts},
		logbookUnreadable, cutoff)

	// What `auto show` renders: one row per trace, carrying traceResult's word.
	shownRuns, shownBlocked := 0, 0
	for _, tr := range ts {
		if traceResult(tr) == traceFailedConditions {
			shownBlocked++
		} else {
			shownRuns++
		}
	}
	if shownBlocked == 0 {
		t.Fatal("fixture lapsed: no condition-blocked trace, so the two counts cannot be told apart")
	}
	if rows[0].runs != shownRuns {
		t.Errorf("`auto ls` runs_24h = %d while `auto show` lists %d trace rows of which %d ran "+
			"and %d were stopped at the condition — the same automation reporting two different "+
			"truths in two commands", rows[0].runs, len(ts), shownRuns, shownBlocked)
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
	rows := buildAutoRows(autos, traces, logbookSaid(map[string]int{"automation.storm": 1500}), cutoff)
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

// TestAutomationTraceKey pins invariant H-9 at the unit level: traces are keyed
// by the automation's config id, not its entity_id.
func TestAutomationTraceKey(t *testing.T) {
	tests := []struct {
		name string
		auto automationEntity
		want string
	}{
		{
			// The case that shipped broken: HA's UI assigns a millisecond
			// timestamp as the config id and derives entity_id from the alias,
			// so these two strings differ for essentially every UI automation.
			name: "config id differs from object id",
			auto: automationEntity{
				EntityID:   "automation.morning_alarm",
				Attributes: automationAttributes{ID: "1699887654321"},
			},
			want: "automation.1699887654321",
		},
		{
			name: "config id equals object id",
			auto: automationEntity{
				EntityID:   "automation.climate_schedule",
				Attributes: automationAttributes{ID: "climate_schedule"},
			},
			want: "automation.climate_schedule",
		},
		{
			// YAML automations may omit `id:`; HA then reports the object id as
			// item_id, so the entity address is the correct fallback.
			name: "no config id falls back to entity_id",
			auto: automationEntity{EntityID: "automation.legacy"},
			want: "automation.legacy",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := automationTraceKey(tt.auto); got != tt.want {
				t.Errorf("automationTraceKey() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestBuildAutoRows_ErrorsWhenConfigIDDiffers is the unit-level regression for
// R1. Before the fix, errors/last_err were looked up with the entity_id while
// HA keys the trace map by config id, so both silently read zero — and
// `auto ls --failing` reported nothing for a genuinely failing automation.
func TestBuildAutoRows_ErrorsWhenConfigIDDiffers(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)
	autos := []automationEntity{{
		EntityID:   "automation.oracle_missing_service",
		State:      "on",
		Attributes: automationAttributes{ID: "cfgid_missing_service"},
	}}
	oldest := time.Now().Add(-2 * time.Hour)
	newest := time.Now().Add(-10 * time.Minute)
	// Keyed the way HA actually returns it: "<domain>.<config id>". The oldest
	// error is listed FIRST, so a first-wins implementation picks the wrong one.
	traces := haapi.TraceListResult{
		"automation.cfgid_missing_service": {
			{Timestamp: haapi.TraceSummaryTimestamp{Start: oldest.Format(time.RFC3339Nano)}, Execution: "error", LastStep: "action/0"},
			{Timestamp: haapi.TraceSummaryTimestamp{Start: newest.Format(time.RFC3339Nano)}, Execution: "error", LastStep: "action/1"},
		},
	}

	rows := buildAutoRows(autos, traces, logbookSaid(map[string]int{}), cutoff)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].errors != 2 {
		t.Errorf("errors = %d, want 2 — traces are keyed by config id %q, not entity_id %q",
			rows[0].errors, "cfgid_missing_service", autos[0].EntityID)
	}
	// last_err must be the most recent error, not whichever arrived first.
	// shortenStep() drops the step index, so the timestamp is what discriminates.
	wantTime := formatShortTime(newest.Format(time.RFC3339Nano))
	oldTime := formatShortTime(oldest.Format(time.RFC3339Nano))
	if !strings.Contains(rows[0].lastErr, wantTime) {
		t.Errorf("lastErr = %q, want it to carry the most recent error's time %q "+
			"(the oldest error is at %q and is listed first)",
			rows[0].lastErr, wantTime, oldTime)
	}
}

func TestBuildAutoRows_FallbackToTraceCountWhenLogbookMissing(t *testing.T) {
	// The logbook could not be read at all, so the in-window traces stand in.
	cutoff := time.Now().Add(-24 * time.Hour)
	autos := []automationEntity{{EntityID: "automation.foo", State: "on"}}
	traces := haapi.TraceListResult{
		"automation.foo": {
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-1 * time.Hour).Format(time.RFC3339Nano)}, Execution: "finished"},
			{Timestamp: haapi.TraceSummaryTimestamp{Start: time.Now().Add(-48 * time.Hour).Format(time.RFC3339Nano)}, Execution: "finished"}, // outside window
		},
	}

	rows := buildAutoRows(autos, traces, logbookUnreadable, cutoff)
	if rows[0].runs != 1 {
		t.Errorf("runs = %d, want 1 (only one trace inside cutoff window)", rows[0].runs)
	}
}

func TestBuildAutoRows_NoTracesNoFires(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)
	autos := []automationEntity{{EntityID: "automation.idle", State: "on"}}

	rows := buildAutoRows(autos, nil, logbookSaid(map[string]int{}), cutoff)
	if rows[0].runs != 0 {
		t.Errorf("runs = %d, want 0", rows[0].runs)
	}
	if rows[0].errors != 0 {
		t.Errorf("errors = %d, want 0", rows[0].errors)
	}
}

func TestTraceResult_Error(t *testing.T) {
	tr := haapi.TraceSummary{Execution: "error"}
	if got := traceResult(tr); got != "error" {
		t.Errorf("traceResult(execution=error) = %q, want 'error'", got)
	}
}

func TestTraceResult_ErrorMsg(t *testing.T) {
	tr := haapi.TraceSummary{Execution: "finished", Error: "something broke"}
	if got := traceResult(tr); got != "error" {
		t.Errorf("traceResult(error msg set) = %q, want 'error'", got)
	}
}

func TestTraceResult_Finished(t *testing.T) {
	tr := haapi.TraceSummary{Execution: "finished"}
	if got := traceResult(tr); got != "finished" {
		t.Errorf("traceResult(finished) = %q, want 'finished'", got)
	}
}

func TestTraceResult_EmptyExecution(t *testing.T) {
	tr := haapi.TraceSummary{State: "stopped"}
	if got := traceResult(tr); got != "stopped" {
		t.Errorf("traceResult(empty execution) = %q, want 'stopped'", got)
	}
}

func TestIsTraceError_ErrorExecution(t *testing.T) {
	tr := haapi.TraceSummary{Execution: "error"}
	if !isTraceError(tr) {
		t.Error("isTraceError(error execution) = false, want true")
	}
}

func TestIsTraceError_ErrorMsg(t *testing.T) {
	tr := haapi.TraceSummary{Execution: "finished", Error: "failed"}
	if !isTraceError(tr) {
		t.Error("isTraceError(error msg) = false, want true")
	}
}

func TestIsTraceError_Clean(t *testing.T) {
	tr := haapi.TraceSummary{Execution: "finished"}
	if isTraceError(tr) {
		t.Error("isTraceError(finished) = true, want false")
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

// TestFilterAutosByPattern_AcceptsTheConfigIDHactlPrints pins invariant H-17 on
// `auto ls`: an identifier hactl prints for an automation is an identifier every
// hactl filter accepts for it.
//
// `auto show` prints the config id as config_id, `auto create` prints it as the
// id it just wrote, and `auto cat`/`diff`/`apply` key on it — so it is an
// identifier a caller can be holding. HA's UI mints a millisecond timestamp for
// it and derives the entity_id from the alias, so for a UI-authored automation
// it is a completely different string from the `id` column, and pasting it into
// `auto ls --pattern` returned nothing (D6/R2).
func TestFilterAutosByPattern_AcceptsTheConfigIDHactlPrints(t *testing.T) {
	// The two identifiers are kept apart, as HA keeps them apart, so a filter
	// that only ever matched the object id cannot pass by accident (TC-4).
	rows := []autoRow{
		{id: "morning_alarm", configID: "1678886400123"},
		{id: "evening_scene", configID: "cfgid_evening"},
		{id: "legacy_yaml"}, // authored in YAML without an `id:`
	}

	tests := []struct {
		name    string
		pattern string
		want    []string
	}{
		{"exact config id", "1678886400123", []string{"morning_alarm"}},
		{"glob over config ids", "cfgid_*", []string{"evening_scene"}},
		{"object id still matches", "morning_*", []string{"morning_alarm"}},
		{"entity_id still matches", "automation.evening_scene", []string{"evening_scene"}},
		{"config id that exists nowhere", "cfgid_absent", nil},
		// An automation with no config id must not be swept up by a glob that
		// would match the empty string.
		{"glob that would match an empty config id", "", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterAutosByPattern(rows, tt.pattern)
			got := make([]string, 0, len(result))
			for _, r := range result {
				got = append(got, r.id)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("filterAutosByPattern(%q) = %v, want %v", tt.pattern, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("filterAutosByPattern(%q) = %v, want %v", tt.pattern, got, tt.want)
				}
			}
		})
	}
}

// TestFilterAutosByPattern_AcceptsTheAliasHactlPrints pins the alias third of
// D-1 (docs/decisions.md): an automation is addressed by config `id:`, alias,
// or entity_id, everywhere. `auto show`/`cat`/`diff`/`apply`/`delete` all
// resolve the alias (HA carries it verbatim as friendly_name), `ent show`
// displays it as the automation's name, and `auto cat` prints it in the YAML —
// so a caller can be holding it, and `auto ls --pattern` answering "nothing"
// for it is hactl refusing an identifier its own commands accept (H-17).
func TestFilterAutosByPattern_AcceptsTheAliasHactlPrints(t *testing.T) {
	// The alias is human-cased with spaces — HA slugifies it into the object
	// id — so a filter that only ever matched the machine forms cannot pass by
	// accident (TC-4).
	rows := []autoRow{
		{id: "morning_alarm", configID: "1678886400123", alias: "Morgen Wecker"},
		{id: "evening_scene", configID: "cfgid_evening", alias: "Evening Scene"},
		{id: "legacy_yaml"}, // YAML-authored: no config id, no alias
	}

	tests := []struct {
		name    string
		pattern string
		want    []string
	}{
		{"exact alias", "Morgen Wecker", []string{"morning_alarm"}},
		{"alias substring, caller-cased", "morgen", []string{"morning_alarm"}},
		{"glob over aliases", "Evening *", []string{"evening_scene"}},
		{"alias that exists nowhere", "Nacht Wecker", nil},
		// An automation with no alias must not be swept up by a glob that
		// would match the empty string.
		{"empty pattern matches no alias-less row", "", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := filterAutosByPattern(rows, tt.pattern)
			got := make([]string, 0, len(result))
			for _, r := range result {
				got = append(got, r.id)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("filterAutosByPattern(%q) = %v, want %v", tt.pattern, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("filterAutosByPattern(%q) = %v, want %v", tt.pattern, got, tt.want)
				}
			}
		})
	}
}

// TestFilterAutosByPattern_MatchesEachAutomationOnce guards the shape of the
// fix: an automation whose object id, config id AND alias all match is still
// one row.
func TestFilterAutosByPattern_MatchesEachAutomationOnce(t *testing.T) {
	rows := []autoRow{{id: "cfgid_same", configID: "cfgid_same", alias: "cfgid_same"}}
	if got := filterAutosByPattern(rows, "*cfgid_same*"); len(got) != 1 {
		t.Errorf("filterAutosByPattern returned %d rows for one automation, want 1", len(got))
	}
}

// TestBuildAutoRows_CarriesTheConfigID checks the identifier actually reaches the
// filter: HA carries it as attributes.id, and if buildAutoRows drops it the
// filter above has nothing to match no matter how it is written.
func TestBuildAutoRows_CarriesTheConfigID(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)
	autos := []automationEntity{
		{EntityID: "automation.morning_alarm", State: "on", Attributes: automationAttributes{ID: "1678886400123", FriendlyName: "Morgen Wecker"}},
		{EntityID: "automation.legacy_yaml", State: "on"},
	}

	rows := buildAutoRows(autos, nil, logbookSaid(map[string]int{}), cutoff)
	byID := map[string]autoRow{}
	for _, r := range rows {
		byID[r.id] = r
	}
	if got := byID["morning_alarm"].configID; got != "1678886400123" {
		t.Errorf("configID = %q, want %q — HA reports it as attributes.id and `auto show` "+
			"prints it, so `auto ls --pattern` has to be able to match it (H-17)",
			got, "1678886400123")
	}
	if got := byID["legacy_yaml"].configID; got != "" {
		t.Errorf("configID = %q for an automation HA reports no id for, want empty", got)
	}
	// D-1: the alias travels the same way, or the filter has nothing to match.
	if got := byID["morning_alarm"].alias; got != "Morgen Wecker" {
		t.Errorf("alias = %q, want %q — HA reports it as friendly_name and every "+
			"`auto` target command resolves it, so `auto ls --pattern` has to be "+
			"able to match it (D-1/H-17)", got, "Morgen Wecker")
	}
}

// --- #54: restored / "ghost" automation surfacing ---

func TestBuildAutoRows_RestoredPropagates(t *testing.T) {
	cutoff := time.Now().Add(-24 * time.Hour)
	autos := []automationEntity{
		{EntityID: "automation.live", State: "on"},
		{EntityID: "automation.ghost", State: "unavailable", Attributes: automationAttributes{Restored: true}},
	}
	rows := buildAutoRows(autos, nil, logbookUnreadable, cutoff)
	byID := map[string]autoRow{}
	for _, r := range rows {
		byID[r.id] = r
	}
	if byID["live"].restored {
		t.Errorf("live automation must not be marked restored")
	}
	if !byID["ghost"].restored {
		t.Errorf("automation with restored:true attribute must propagate to row.restored")
	}
}

func TestFilterAutosRestored(t *testing.T) {
	rows := []autoRow{
		{id: "a", restored: false},
		{id: "b", restored: true},
		{id: "c", restored: false},
		{id: "d", restored: true},
	}
	result := filterAutosRestored(rows)
	if len(result) != 2 {
		t.Fatalf("expected 2 restored, got %d", len(result))
	}
	if result[0].id != "b" || result[1].id != "d" {
		t.Errorf("restored filter returned %q, %q; want b, d", result[0].id, result[1].id)
	}
}
