//go:build livefire

package livefire

import (
	"encoding/json"
	"strings"
	"testing"
)

// The corpus. One assertion per confirmed finding, each citing its index in
// _archive/livefire-2026-07-30/findings_merged.json, each run against both the
// rig and (when configured) the real instance.
//
// A case belongs here once its defect is fixed. Cases are added as the work
// packages in FIXPLAN-livefire.md land, so the sweep grows into the acceptance
// gate rather than being written against a moving target at the end.

// Finding #46: `ent ls sensor` printed the same unfiltered listing as `ent ls`.
// cobra's nil Args is ArbitraryArgs, so the positional was accepted and handed
// to a Run that never read it — the caller got a full listing and no hint that
// their filter had been dropped.
func TestSweepListingRefusesASwallowedPositional(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		out, err := tgt.Read(t, "ent", "ls", "sensor")
		if err == nil {
			t.Fatalf("`ent ls sensor` succeeded; the positional was swallowed again:\n%s", out)
		}
		if code := ExitCode(err); code != 1 {
			t.Errorf("exit code %d, want 1 — a refused command must be detectable by exit code", code)
		}
		if out != "" {
			t.Errorf("a refused command wrote to stdout: %q", out)
		}
	})
}

// Finding #45/#92: an empty identifier resolved to a real, unrelated object —
// `auto delete ”` printed a plan to delete somebody's automation. The
// resolvers compared the reference with == against fields a restored ghost
// carries empty.
func TestSweepBlankIdentifierNeverResolves(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, args := range [][]string{
			{"auto", "show", ""},
			{"auto", "delete", ""},
			{"device", "show", ""},
			{"ent", "hist", "   "},
		} {
			out, err := tgt.Read(t, args...)
			if err == nil {
				t.Errorf("%v resolved a blank identifier:\n%s", args, out)
			}
			if strings.Contains(out, "dry-run") {
				t.Errorf("%v printed a PLAN for a blank identifier:\n%s", args, out)
			}
		}
	})
}

// Finding #33/#35/#62/#68/#80: an unknown subcommand under any family exited 0
// and printed the family's help, so a typo was indistinguishable from success
// to anything reading exit codes.
func TestSweepUnknownSubcommandFails(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, args := range [][]string{
			{"helper", "set"},
			{"dash", "frobnicate"},
			{"auto", "frobnicate"},
			{"ent", "frobnicate"},
		} {
			out, err := tgt.Read(t, args...)
			if err == nil {
				t.Errorf("%v exited 0 — a typo cannot be told from a success:\n%s", args, out)
			}
		}
	})
}

// Finding #85: table listings put a rendered wall clock in their machine
// contract — `"last_changed": "06:31"`, and `"07-28 11:52"` for anything older
// than today, which cannot be turned back into an instant at all.
//
// The assertion is on the SHAPE of every value in the document, not on a list
// of field names: a name list is what shipped the defect, because it forgets
// whichever field is added next.
func TestSweepNoRenderedClockReachesJSON(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, args := range [][]string{
			{"ent", "ls", "--top", "20"},
			{"auto", "ls", "--top", "20"},
			{"log", "--top", "20"},
		} {
			out := tgt.MustRead(t, append(args, "--json")...)
			var rows []map[string]any
			if err := json.Unmarshal([]byte(out), &rows); err != nil {
				t.Fatalf("%v --json is not a JSON array: %v\n%s", args, err, truncate(out))
			}
			for _, row := range rows {
				for field, value := range row {
					if passedThroughFromHA(field) {
						continue
					}
					if s, ok := value.(string); ok && looksLikeARenderedClock(s) {
						t.Errorf("%v --json carries a rendered clock in %q: %q — a machine cannot date it",
							args, field, s)
					}
				}
			}
		}
	})
}

// looksLikeARenderedClock matches the two short forms the table renders:
// "06:31" for today and "07-28 11:52" for anything older. An ISO8601 instant
// carries a date and an offset and matches neither.
func looksLikeARenderedClock(s string) bool {
	switch {
	case len(s) == 5 && s[2] == ':' && allDigits(s[:2]) && allDigits(s[3:]):
		return true
	case len(s) == 11 && s[2] == '-' && s[5] == ' ' && s[8] == ':':
		return true
	}
	return false
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}

func truncate(s string) string {
	if len(s) > 400 {
		return s[:400] + "…"
	}
	return s
}

// passedThroughFromHA names the fields whose value hactl copies from the wire
// rather than rendering.
//
// The defect this case exists for is hactl formatting an instant for a human
// and then handing that string to a machine. A field it never formatted cannot
// carry it. `state` is HA's own payload, and on a real instance it is routinely
// a wall clock that is genuinely the value: sensor.time reads "11:12",
// sensor.worldclock_sensor "09:11", sensor.gunstigste_stunde_heute "14:00" —
// all correct, none rendered by hactl, and their last_changed alongside is
// proper ISO8601 with an offset.
//
// This exemption was not designed; the live profile produced it. The rig's
// `realistic` fixture carries no time-valued sensor, so the rig passed this
// case while the real instance failed it five times over — the same shape of
// gap that let the 2026-07-30 findings exist at all, reproduced inside the
// harness built to catch them. Teaching the rig a time-valued sensor is rig
// capability R5 in FIXPLAN-livefire.md.
//
// It is one field and it is justified, which is the only kind of exemption
// this check may carry: the shape test still covers every other field,
// including whichever timestamp field is added next — a list of field names is
// what shipped the original defect.
func passedThroughFromHA(field string) bool {
	return field == "state"
}
