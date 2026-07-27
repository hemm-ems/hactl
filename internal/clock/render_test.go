package clock_test

import (
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
