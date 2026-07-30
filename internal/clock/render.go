// Package clock holds the one way this repository turns a Home Assistant
// timestamp into a clock a person reads.
//
// It exists because there were five. `cmd.formatShortTime` parsed and
// converted; `analyze.FormatShortTimestamp` parsed and did not convert;
// `analyze.shortTimestamp` never parsed at all — it cut the ISO string on "T"
// and printed whatever zone the wire carried; and two more formatted a
// `time.Unix` value that happened to be local already.
//
// When the conversion bug was found and fixed, it was fixed in the renderer
// where it had been observed. The other four were not searched for, and
// `trace show` was reported as still displaying UTC the same day. A unit test
// pinned that renderer's UTC-in/UTC-out behaviour as correct, so the suite
// stayed green.
//
// Home Assistant reports timestamps in UTC. hactl's reader is in their own
// zone. There is no site where rendering the former as if it were the latter is
// right, which is what makes this collapsible into one function.
package clock

import (
	"strings"
	"time"
)

// wireLayouts are the timestamp shapes Home Assistant sends.
//
// Order matters only for ambiguity, and there is none here: the zoned forms are
// tried first so that a value carrying an offset is never mistaken for a naive
// local one.
var wireLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02 15:04:05.999999",
	"2006-01-02 15:04:05",
}

// Parse reads any timestamp shape Home Assistant sends and returns it located
// in the reader's zone.
//
// A value with no zone (HA's `system_log` entries and the REST `error_log`
// carry none) is taken to be local, because that is what hactl's own log
// pipeline produces via time.Unix. A value with an offset is converted.
func Parse(ts string) (time.Time, bool) {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return time.Time{}, false
	}
	for _, layout := range wireLayouts {
		t, err := time.Parse(layout, ts)
		if err != nil {
			continue
		}
		if layout == time.RFC3339Nano || layout == time.RFC3339 {
			//nolint:gosmopolitan // gosmopolitan guards server code against assuming the host's zone; hactl is a CLI and the host's zone is the reader's.
			t = t.Local()
		} else {
			// A naive wire value is already a local wall clock; re-locating it
			// keeps the same digits and makes the calendar-day comparison below
			// meaningful.
			//nolint:gosmopolitan // as above
			t = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), t.Nanosecond(), time.Local)
		}
		return t, true
	}
	return time.Time{}, false
}

// Short renders a timestamp as "15:04" when it falls on the reader's today and
// "01-02 15:04" otherwise, in the reader's zone. Unparseable input is returned
// verbatim so a wire change shows up as itself rather than as a plausible time.
func Short(ts string) string {
	t, ok := Parse(ts)
	if !ok {
		return ts
	}
	return format(t, "15:04", "01-02 15:04")
}

// ShortSeconds is Short with seconds, for views where two events inside the
// same minute have to be told apart — trace steps, log entries.
func ShortSeconds(ts string) string {
	t, ok := Parse(ts)
	if !ok {
		return ts
	}
	return format(t, "15:04:05", "01-02 15:04:05")
}

// ISO renders a timestamp for a MACHINE: the full instant, in the reader's
// zone, with its UTC offset. It is the counterpart of Short, and it exists
// because the two audiences were being served one string.
//
// `ent ls`, `ent hist`, `log` and `log show` built a format.Table whose cells
// were Short's output and then rendered that same table as JSON, so a machine
// consumer received `"last_changed": "06:31"` — no date, no year, no zone — for
// an entity whose wire value was `2026-07-30T04:31:28.653662+00:00`. For a
// value from another day it degraded to `"07-28 11:52"`, which is not even
// datable. `ent show --json` on the SAME entity emitted the full instant, so
// two commands disagreed about the shape of one field.
//
// Unparseable input is returned verbatim, exactly as Short does: a wire change
// must show up as itself rather than as a plausible timestamp. That is also why
// this does not fabricate a zone — Parse locates a naive value in the reader's
// zone, which is where hactl's own log pipeline produced it.
func ISO(ts string) string {
	t, ok := Parse(ts)
	if !ok {
		return ts
	}
	return t.Format(time.RFC3339Nano)
}

// format applies the today/not-today choice.
//
// Both sides of the comparison are in the reader's zone. They were not: the
// parsed value's calendar day (UTC) used to be compared against a local
// time.Now(), so between local midnight and the UTC offset a timestamp seconds
// old was stamped with yesterday's date.
func format(t time.Time, today, other string) string {
	now := time.Now()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format(today)
	}
	return t.Format(other)
}
