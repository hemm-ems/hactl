package clock_test

import (
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/clock"
)

// inBerlin pins the reader's zone so every assertion below means the same thing
// in CI (which runs UTC) as on a developer's machine, and fails at any hour
// rather than only inside a window.
func inBerlin(t *testing.T) {
	t.Helper()
	loc, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Skipf("no tzdata available: %v", err)
	}
	t.Setenv("TZ", "Europe/Berlin")
	//nolint:gosmopolitan // pinning the reader's zone is exactly what this test asserts about
	time.Local = loc
}

// TestShortConvertsTheWireZone is the whole point of the package: Home
// Assistant reports UTC, the reader is not in UTC.
func TestShortConvertsTheWireZone(t *testing.T) {
	inBerlin(t)
	for _, tc := range []struct {
		in, want string
	}{
		{"2026-04-16T09:42:00+00:00", "04-16 11:42"},
		{"2026-04-16T09:42:00.123456+00:00", "04-16 11:42"},
		{"2026-04-16T09:42:00Z", "04-16 11:42"},
		{"2026-04-16T09:42:00+02:00", "04-16 09:42"},
		{"", ""},
		{"not-a-time", "not-a-time"},
	} {
		if got := clock.Short(tc.in); got != tc.want {
			t.Errorf("Short(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// TestSameDayIsDecidedInTheReaderVsZone — the second half of the original
// defect, and the one that only bit inside a window.
//
// A UTC-located value's calendar day was compared against a local time.Now(),
// so between local midnight and the UTC offset a timestamp seconds old was
// stamped with yesterday's date. That is what reddened the golden tests when
// the suite happened to be run at 00:07.
func TestSameDayIsDecidedInTheReaderZone(t *testing.T) {
	inBerlin(t)
	now := time.Now()

	// 22:30 UTC today is 00:30 tomorrow in CEST — the exact window.
	utcOfLocalToday := now.UTC().Format("2006-01-02T15:04:05Z")
	got := clock.Short(utcOfLocalToday)
	if len(got) != 5 {
		t.Errorf("Short(%q) = %q — a timestamp from right now must render as a bare clock, not a dated one", utcOfLocalToday, got)
	}

	old := now.AddDate(0, 0, -3).UTC().Format("2006-01-02T15:04:05Z")
	if got := clock.Short(old); len(got) == 5 {
		t.Errorf("Short(%q) = %q — a three-day-old timestamp must carry its date", old, got)
	}
}

// TestNaiveWireValueKeepsItsDigits — HA's system_log entries and the REST
// error_log carry no zone, and hactl's own log pipeline builds them from
// time.Unix, which is already local. Re-interpreting those as UTC would shift
// them by the offset in the wrong direction.
func TestNaiveWireValueKeepsItsDigits(t *testing.T) {
	inBerlin(t)
	if got, want := clock.ShortSeconds("2026-04-16 09:42:07.123"), "04-16 09:42:07"; got != want {
		t.Errorf("ShortSeconds(naive) = %q, want %q", got, want)
	}
}

// TestShortColumnIsUniformDownTheColumn — live-fire #71.
//
// `auto show`'s trace table printed four rows as `07-29 01:15` and the fifth,
// which started today, as a bare `01:15`. The abbreviation is a relative
// rendering and the column is where its reference point is invisible: a reader
// who does not know the convention cannot date that row, cannot tell it from a
// row whose date was lost, and cannot compare it with the one above it.
//
// The fixtures are built from today's CALENDAR DAY at fixed clock times rather
// than by subtracting from now, so the case means the same thing when it runs at
// 00:44 as at noon — the first draft used now-1h and crossed midnight.
func TestShortColumnIsUniformDownTheColumn(t *testing.T) {
	inBerlin(t)
	morning := todayAt(9, 15)
	afternoon := todayAt(13, 45)
	// Another day of THIS year: on 1 January "yesterday" is last year, which
	// is the year tier's case (TestShortColumnNamesTheYearAcrossYears), not
	// this one.
	yesterday := morning.AddDate(0, 0, -1)
	if yesterday.Year() != morning.Year() {
		yesterday = morning.AddDate(0, 0, 1)
	}

	mixed := clock.ShortColumn([]string{
		yesterday.Format(time.RFC3339),
		afternoon.Format(time.RFC3339),
	})
	for i, got := range mixed {
		if len(got) != len("01-02 15:04") {
			t.Errorf("row %d of a mixed column renders %q — one row carries a date and another does not", i, got)
		}
	}

	// The convenience survives where it is unambiguous: a column in which
	// everything happened today is a column of clock times, and adding today's
	// date to every row of it is noise the reader cannot use.
	allToday := clock.ShortColumn([]string{
		morning.Format(time.RFC3339),
		afternoon.Format(time.RFC3339),
	})
	for i, got := range allToday {
		if len(got) != len("15:04") {
			t.Errorf("row %d of an all-today column renders %q, want the bare clock", i, got)
		}
	}
}

// todayAt builds an instant on the reader's current calendar day.
func todayAt(hour, minute int) time.Time {
	now := time.Now()
	//nolint:gosmopolitan // the reader's zone is what this package renders in
	return time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, time.Local)
}

// TestShortColumnPassesThroughWhatItCannotParse holds the rule this package
// applies everywhere: a wire change shows up as itself, never as a plausible
// time. A column of one unparseable value must not make the parseable ones
// change shape either.
func TestShortColumnPassesThroughWhatItCannotParse(t *testing.T) {
	inBerlin(t)
	got := clock.ShortColumn([]string{"", "not a timestamp", todayAt(9, 15).Format(time.RFC3339)})
	if got[0] != "" || got[1] != "not a timestamp" {
		t.Errorf("unparseable values were rewritten: %q", got)
	}
	if len(got[2]) != len("15:04") {
		t.Errorf("an all-today column rendered %q beside unparseable neighbours", got[2])
	}
}

// TestShortColumnNamesTheYearAcrossYears is the D-38 rule one unit up: a column
// drops the year only when every value falls in the reader's current year. A
// monthly long-term statistics table over two years (D-47) rendered two rows
// `01-01 00:00` for two different Januaries.
func TestShortColumnNamesTheYearAcrossYears(t *testing.T) {
	inBerlin(t)
	now := time.Now()
	//nolint:gosmopolitan // the reader's zone is what this package renders in
	lastYear := time.Date(now.Year()-1, time.January, 1, 0, 0, 0, 0, time.Local)
	//nolint:gosmopolitan // as above
	thisYear := time.Date(now.Year(), time.January, 1, 0, 0, 0, 0, time.Local)

	got := clock.ShortColumn([]string{lastYear.Format(time.RFC3339), thisYear.Format(time.RFC3339)})
	want := []string{lastYear.Format("2006-01-02 15:04"), thisYear.Format("2006-01-02 15:04")}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("row %d renders %q, want %q — a column spanning two years must name the year on every row", i, got[i], want[i])
		}
	}
}

// TestLongNamesTheYearInTheReaderZone pins Long: the full date, converted from
// the wire's UTC into the reader's zone (the start of a recorder history or of
// long-term statistics may lie in another year).
func TestLongNamesTheYearInTheReaderZone(t *testing.T) {
	inBerlin(t)
	if got := clock.Long("2025-03-01T23:30:00+00:00"); got != "2025-03-02 00:30" {
		t.Errorf("Long(2025-03-01T23:30Z) = %q, want 2025-03-02 00:30 (Berlin, UTC+1)", got)
	}
	if got := clock.Long("not a timestamp"); got != "not a timestamp" {
		t.Errorf("Long rewrote an unparseable value: %q", got)
	}
}

// TestShortColumnTellsTheRepeatedHourApart: when the clocks go back, 02:00
// happens twice. Both rows rendered `10-25 02:00`; the column now carries the
// zone abbreviation on every row so the two hours are distinguishable.
func TestShortColumnTellsTheRepeatedHourApart(t *testing.T) {
	inBerlin(t)
	got := clock.ShortColumn([]string{
		"2026-10-25T00:00:00+00:00", // 02:00 CEST
		"2026-10-25T01:00:00+00:00", // 02:00 CET
		"2026-10-25T02:00:00+00:00", // 03:00 CET
	})
	want := []string{"2026-10-25 02:00 CEST", "2026-10-25 02:00 CET", "2026-10-25 03:00 CET"}
	if time.Now().Year() == 2026 {
		want = []string{"10-25 02:00 CEST", "10-25 02:00 CET", "10-25 03:00 CET"}
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %q, want %q", i, got[i], want[i])
		}
	}

	// No collision, no zone: the abbreviation is not noise added everywhere.
	plain := clock.ShortColumn([]string{"2026-10-25T02:00:00+00:00", "2026-10-25T03:00:00+00:00"})
	for _, p := range plain {
		if strings.Contains(p, "CET") {
			t.Errorf("a column without a repeated hour carries the zone: %q", p)
		}
	}
}
