package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// The positive half of H-10 clause (4).
//
// TestJSONContract's assertNoRenderedClock says what a --json timestamp may
// NOT be. That alone is satisfied by a command that emits no timestamp at all,
// or one that emits a plausible-looking string naming no instant. These tests
// say what it MUST be, on the four commands the defect was reported against,
// by comparing against the wire value the fake HA sent: same instant, offset
// present, so a consumer can order two rows from different commands.
// ---------------------------------------------------------------------------

// assertSameInstant parses a --json timestamp and requires it to name the same
// instant as the wire value, with an explicit offset.
func assertSameInstant(t *testing.T, field, got, wire string) {
	t.Helper()
	if got == "" {
		t.Fatalf("%s: --json carries no timestamp at all", field)
	}
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("%s: %q is not RFC3339: %v", field, got, err)
	}
	// An offset is what makes the value an instant rather than a wall clock.
	// time.Parse accepts "Z" and "+HH:MM" and nothing else, so reaching here
	// already proves one is present; this pins the reason so a future relaxation
	// of the layout cannot pass silently.
	if !strings.ContainsAny(got, "Zz+") && !strings.Contains(got[10:], "-") {
		t.Errorf("%s: %q carries no UTC offset", field, got)
	}
	want, err := time.Parse(time.RFC3339Nano, wire)
	if err != nil {
		t.Fatalf("test fixture: wire value %q is not RFC3339: %v", wire, err)
	}
	if !parsed.Equal(want) {
		t.Errorf("%s: --json says %s, HA said %s — different instants", field, parsed, want)
	}
}

// runJSON runs a command against the shared read fixture and decodes stdout.
func runJSON(t *testing.T, dir string, args ...string) []byte {
	t.Helper()
	full := append([]string{"hactl"}, args...)
	full = append(full, "--dir", dir, "--json", "--tokensmax", "0")
	var buf bytes.Buffer
	if err := RunWithOutput(full, &buf); err != nil {
		t.Fatalf("%v failed: %v\n%s", args, err, buf.String())
	}
	return buf.Bytes()
}

// TestEntLsJSON_TimestampIsTheInstantHASent — `ent ls --json` reported
// `"last_changed": "06:31"` for an entity whose wire value was
// `2026-07-30T04:31:28.653662+00:00`, because the JSON reused the table cell.
func TestEntLsJSON_TimestampIsTheInstantHASent(t *testing.T) {
	f := buildContractFixture(t)

	var rows []map[string]any
	if err := json.Unmarshal(runJSON(t, f.dir, "ent", "ls"), &rows); err != nil {
		t.Fatalf("ent ls --json did not parse: %v", err)
	}
	var found bool
	for _, row := range rows {
		if row["entity_id"] != "light.kitchen" {
			continue
		}
		found = true
		got, _ := row["last_changed"].(string)
		assertSameInstant(t, "ent ls last_changed", got, "2026-01-01T09:00:00+00:00")
	}
	if !found {
		t.Fatal("light.kitchen missing from ent ls --json — fixture changed shape")
	}

	// The text table keeps the short form: the fix must not have moved the
	// full instant into the human view, which is the mirror-image regression.
	var text bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "ent", "ls", "--dir", f.dir, "--tokensmax", "0"}, &text); err != nil {
		t.Fatalf("ent ls failed: %v", err)
	}
	if strings.Contains(text.String(), "2026-01-01T09:00:00") {
		t.Errorf("the text table now prints the full ISO instant; the short form is the human one:\n%s", text.String())
	}
}

// TestEntHistJSON_TimestampIsTheInstantHASent — same defect on `ent hist`,
// whose `time` column came back as "02:18".
func TestEntHistJSON_TimestampIsTheInstantHASent(t *testing.T) {
	f := buildContractFixture(t)

	var rows []map[string]any
	if err := json.Unmarshal(runJSON(t, f.dir, "ent", "hist", "sensor.temp"), &rows); err != nil {
		t.Fatalf("ent hist --json did not parse: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("ent hist --json returned no rows — fixture changed shape")
	}
	got, _ := rows[0]["time"].(string)
	assertSameInstant(t, "ent hist time", got, "2026-01-01T10:00:00+00:00")
}

// TestLogJSON_TimestampCarriesAZone — `log --json` reported `"time": "08:07"`.
//
// A log entry's wire value carries no zone at all, so the assertion here is
// about the ZONE being attached rather than about matching an offset HA sent:
// the fixture's error_log line is a naive local stamp, and the machine form has
// to name the instant hactl means by it.
func TestLogJSON_TimestampCarriesAZone(t *testing.T) {
	f := buildContractFixture(t)

	var rows []map[string]any
	if err := json.Unmarshal(runJSON(t, f.dir, "log"), &rows); err != nil {
		t.Fatalf("log --json did not parse: %v", err)
	}
	if len(rows) == 0 {
		t.Fatal("log --json returned no rows — fixture changed shape")
	}
	got, _ := rows[0]["time"].(string)
	parsed, err := time.Parse(time.RFC3339Nano, got)
	if err != nil {
		t.Fatalf("log time %q is not RFC3339: %v", got, err)
	}
	// The fixture's line reads 2026-01-01 05:00:00.000 with no zone; hactl
	// takes a naive value to be local (clock.Parse), so the wall clock must
	// survive and the offset must be the reader's.
	if h, m := parsed.Hour(), parsed.Minute(); h != 5 || m != 0 {
		t.Errorf("log time %q: wall clock moved (want 05:00 local, got %02d:%02d)", got, h, m)
	}
	_, offset := parsed.Zone()
	_, wantOffset := time.Date(2026, 1, 1, 5, 0, 0, 0, time.Local).Zone() //nolint:gosmopolitan // the reader's zone is the point
	if offset != wantOffset {
		t.Errorf("log time %q: offset %d s, want the reader's %d s", got, offset, wantOffset)
	}
}

// TestLogShowJSON_TimestampCarriesAZone — `log show --json` emitted
// "2026-07-30 08:07:24.044": a date, a clock, a space instead of a T, and no
// zone marker at all, so a consumer could not tell local from UTC. It was
// local, two hours off the instant.
func TestLogShowJSON_TimestampCarriesAZone(t *testing.T) {
	f := buildContractFixture(t)

	var doc map[string]any
	if err := json.Unmarshal(runJSON(t, f.dir, "log", "show", f.logShowID), &doc); err != nil {
		t.Fatalf("log show --json did not parse: %v", err)
	}
	got, _ := doc["timestamp"].(string)
	if got == "" {
		t.Fatalf("log show --json carries no timestamp: %v", doc)
	}
	if _, err := time.Parse(time.RFC3339Nano, got); err != nil {
		t.Errorf("log show timestamp %q is not RFC3339-with-offset: %v", got, err)
	}
}

// TestTraceShowJSON_StepTimeCarriesAZone — the fifth command the shape gate
// found, and one the report did not name: a condensed trace step's `time` was
// clock.ShortSeconds' "08:00:00", a wall clock with no date inside a machine
// document. Nothing in the text renderer ever printed that field, so it existed
// only for machines and served none of them.
func TestTraceShowJSON_StepTimeCarriesAZone(t *testing.T) {
	f := buildContractFixture(t)

	var doc map[string]any
	if err := json.Unmarshal(runJSON(t, f.dir, "trace", "show", "script.wakeup/run2"), &doc); err != nil {
		t.Fatalf("trace show --json did not parse: %v", err)
	}
	steps, _ := doc["steps"].([]any)
	if len(steps) == 0 {
		t.Fatal("trace show --json returned no steps — fixture changed shape")
	}
	step, _ := steps[0].(map[string]any)
	got, _ := step["time"].(string)
	assertSameInstant(t, "trace show steps[0].time", got, "2026-01-01T07:00:00+00:00")
}

// ---------------------------------------------------------------------------
// H-10 meets H-21's encode half.
// ---------------------------------------------------------------------------

// TestEntShowJSON_WholeFloatAttributeStaysAFloat — `ent show --json` emitted
// `"max": 5000` for an entity whose wire value was `5000.0`.
//
// The assertion is on the BYTES, not on the decoded value: Go decodes `5000`
// and `5000.0` to the same float64, so a round-tripped comparison passes over
// exactly the defect. What broke was the literal's lexical form, which is what
// a type-aware consumer (Python's json.loads, a schema validator) reads.
//
// The non-integral control matters as much as the case under test: floats that
// are not whole always round-tripped correctly, which is why this survived a
// release — a fix that broke `12.7` while repairing `45.0` would look green
// against the case alone.
func TestEntShowJSON_WholeFloatAttributeStaysAFloat(t *testing.T) {
	// HA types a number.* entity's min/max/step as float on the wire, always;
	// the domaindecode oracle recorded `max: 100.0` from a live instance
	// (internal/integration/domaindecode_oracle_test.go).
	const wire = `{
		"entity_id": "number.setpoint",
		"state": "1200.0",
		"last_changed": "2026-01-01T09:00:00+00:00",
		"last_updated": "2026-01-01T09:00:00+00:00",
		"attributes": {
			"friendly_name": "Setpoint",
			"min": -5000.0,
			"max": 5000.0,
			"step": 0.1,
			"offset": 12.7,
			"mode": "box",
			"count": 3
		}
	}`

	ts := startCmdServer(t, map[string]any{}, map[string]http.HandlerFunc{
		"/api/states/number.setpoint": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, wire)
		},
	})

	out := string(runJSON(t, ts.dir, "ent", "show", "number.setpoint"))

	for _, want := range []string{
		`"max": 5000.0`,  // the defect: re-encoded as 5000
		`"min": -5000.0`, // and as -5000
		`"step": 0.1`,    // control: a non-integral float was never broken
		`"offset": 12.7`, // control
		`"count": 3`,     // control: an integer on the wire stays an integer
	} {
		if !strings.Contains(out, want) {
			t.Errorf("ent show --json lost the wire form of an attribute: want %s in output\n%s", want, out)
		}
	}
	// And it still parses, with the values intact.
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("ent show --json did not parse: %v\n%s", err, out)
	}
	attrs, _ := doc["attributes"].(map[string]any)
	if got, _ := attrs["max"].(float64); got != 5000 {
		t.Errorf("attributes.max = %v, want 5000", attrs["max"])
	}
}
