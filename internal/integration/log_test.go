//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// logProbe is one entry this file writes into HA's own system log so the
// assertions below have something whose level, logger and message they know.
//
// HA's boot log is not a fixture: what it contains varies by HA version, and a
// filter test that only ever sees ERROR rows cannot tell a working `--errors`
// from one that returns everything (TC-4 — a fixture must distinguish). Writing
// through `system_log.write` puts the entry in the same buffer
// `system_log/list` serves, which is where `log` reads from.
type logProbe struct {
	level   string // as passed to system_log.write
	logger  string // full dotted logger name
	message string // long enough that the table has to shorten it (see TestLogFullMessageReachesJSON)
}

// component is what hactl's --json reports for this probe: the FULL logger
// name, because that is the value --component matches against.
//
// It used to shorten the name to its last dot-segment, and the helper's own
// comment cited cmd/log.go's shortComponent as the reason — a test written from
// the code's model rather than from the source of truth beside it. HA's
// system_log/list reports `hactl.assertfloor.errsite`; hactl reported
// `errsite`; and this file's own haLogNames() had HA's answer in hand and
// shortened it to match (finding #16).
func (p logProbe) component() string { return p.logger }

// The error probe's message is 87 bytes with a two-byte character at offsets 56
// and 57 — where a `msg[:57]` cut lands. Both properties are load-bearing and
// neither is obvious from reading the sentence: under 60 bytes and
// TestLogFullMessageReachesJSON passes against the very defect it exists for,
// and with the umlaut anywhere else the byte slice never splits a character.
// The reference instance is German and produces this shape on its own; the rig
// carries it deliberately (capability R6).
var (
	errProbe = logProbe{
		level:   "error",
		logger:  "hactl.assertfloor.errsite",
		message: "assertion floor error probe, long enough to be cut, and über die 60-Byte-Grenze hinaus",
	}
	warnProbe = logProbe{
		level:   "warning",
		logger:  "hactl.assertfloor.warnsite",
		message: "assertion floor warning probe, also long enough that the display cap has to shorten it",
	}
)

// writeLogProbes injects both probes and returns once HA's own system_log/list
// reports them, so a later read cannot race the service call.
func writeLogProbes(t *testing.T, inst *hatest.Instance) {
	t.Helper()
	client := haapi.New(inst.URL(), inst.Token())
	ctx := context.Background()
	for _, p := range []logProbe{errProbe, warnProbe} {
		if err := client.CallService(ctx, "system_log", "write", map[string]any{
			"level":   p.level,
			"logger":  p.logger,
			"message": p.message,
		}); err != nil {
			t.Fatalf("system_log.write(%s, %s): %v", p.level, p.logger, err)
		}
	}

	names := haLogNames(t, inst)
	for _, p := range []logProbe{errProbe, warnProbe} {
		if _, ok := names[p.logger]; !ok {
			t.Fatalf("precondition: HA's own system_log/list does not hold the %s probe %q after writing it; "+
				"names present: %v", p.level, p.logger, names)
		}
	}
}

// haLogNames is HA's answer to "what is in your log": the full logger names
// system_log/list reports, which is the same source `log` reads.
func haLogNames(t *testing.T, inst *hatest.Instance) map[string]bool {
	t.Helper()
	ws := haapi.NewWSClient(inst.URL(), inst.Token())
	if err := ws.Connect(context.Background()); err != nil {
		t.Fatalf("ws connect for system_log/list: %v", err)
	}
	defer func() { _ = ws.Close() }()
	entries, err := ws.SystemLogList(context.Background())
	if err != nil {
		t.Fatalf("system_log/list: %v", err)
	}
	out := map[string]bool{}
	for _, e := range entries {
		out[e.Name] = true
	}
	return out
}

// logRow is one row of `log --json`.
type logRow struct {
	ID        string `json:"id"`
	Time      string `json:"time"`
	Level     string `json:"level"`
	Component string `json:"component"`
	Message   string `json:"message"`
}

func logRows(t *testing.T, args ...string) []logRow {
	t.Helper()
	raw := runHactl(t, append(args, "--top", "1000", "--json")...)
	var rows []logRow
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("hactl %v --json did not parse: %v\noutput:\n%s", args, err, raw)
	}
	return rows
}

func hasProbe(rows []logRow, p logProbe) bool {
	for _, r := range rows {
		if r.Component == p.component() && strings.Contains(r.Message, p.message) {
			return true
		}
	}
	return false
}

// TestLog proves `log` reports what Home Assistant's own system log holds.
//
// The body used to be `out := runHactl(t, "log"); _ = out`, which passed
// whether the command rendered HA's log, an empty table, or a table of records
// it made up — the exact liveness-only shape (M3) that let a stubbed write pass
// two tiers. The assertions are bounded by two reads of HA's own
// system_log/list, taken either side of the hactl run, so a log entry arriving
// mid-test cannot make this flake: whatever hactl printed must be a superset of
// what HA already held and a subset of what HA held afterwards.
func TestLog(t *testing.T) {
	writeLogProbes(t, ha)

	before := haLogNames(t, ha)
	rows := logRows(t, "log")
	after := haLogNames(t, ha)

	if !hasProbe(rows, errProbe) || !hasProbe(rows, warnProbe) {
		t.Errorf("log dropped a record HA's own system_log/list holds: error probe present=%v, "+
			"warning probe present=%v\nrows: %+v", hasProbe(rows, errProbe), hasProbe(rows, warnProbe), rows)
	}

	// Every component hactl printed must belong to a logger HA reported, and
	// every logger HA reported before the run must have reached the output.
	// Both directions matter: the first catches invention, the second catches a
	// filter or a decode that silently swallowed records.
	got := map[string]bool{}
	for _, r := range rows {
		if r.Level == "" || r.Component == "" {
			t.Errorf("log row has no level/component — a record decoded without content: %+v", r)
		}
		got[r.Component] = true
	}
	for c := range got {
		if !componentOf(after, c) {
			t.Errorf("log printed component %q, which matches no logger name in HA's system_log/list %v",
				c, keys(after))
		}
	}
	for name := range before {
		if !got[name] {
			t.Errorf("log omitted HA logger %q; hactl printed %v", name, keys(got))
		}
	}
}

// TestLogTextShowsTheLastSegment is the control for the case above: the machine
// value became the full logger name, and the reader's column must not have.
// A fix that showed both audiences the whole dotted name would satisfy every
// assertion in TestLog and widen the table by thirty characters per row.
func TestLogTextShowsTheLastSegment(t *testing.T) {
	writeLogProbes(t, ha)

	out := runHactl(t, "log", "--top", "1000")
	if strings.Contains(out, errProbe.logger) {
		t.Errorf("the text table prints the whole logger name %q:\n%s", errProbe.logger, out)
	}
	if !strings.Contains(out, lastSegment(errProbe.logger)) {
		t.Errorf("the text table lost the component column (%q):\n%s", lastSegment(errProbe.logger), out)
	}
}

// TestLogFullMessageReachesJSON is finding #14 against a real Home Assistant.
//
// Every list renderer in the family cut the message to 60 bytes while building
// the row, so `--json --full --tokensmax 0` still answered 60 characters and
// `log show <id>` was the only way to read a message hactl had received whole.
// The probe below is longer than the cut on purpose; a short one makes this
// case pass against the defect.
func TestLogFullMessageReachesJSON(t *testing.T) {
	writeLogProbes(t, ha)

	if len(errProbe.message) <= 60 {
		t.Fatalf("the probe message is %d bytes, which the 60-byte cut never reaches — "+
			"this case would pass against the defect it exists for", len(errProbe.message))
	}
	for _, args := range [][]string{{"log"}, {"log", "--unique"}} {
		rows := logRows(t, args...)
		var found bool
		for _, r := range rows {
			if r.Component != errProbe.logger {
				continue
			}
			found = true
			if r.Message != errProbe.message {
				t.Errorf("%v --json carries a shortened message:\n got %q\nwant %q",
					args, r.Message, errProbe.message)
			}
		}
		if !found {
			t.Errorf("%v --json does not hold the probe at all; rows: %+v", args, rows)
		}
	}
}

// TestLogErrors proves `--errors` narrows to ERROR and narrows correctly. It
// used to run the command and discard the result, so a filter that returned
// every level, or none at all, read identically.
func TestLogErrors(t *testing.T) {
	writeLogProbes(t, ha)

	rows := logRows(t, "log", "--errors")

	for _, r := range rows {
		if !strings.EqualFold(r.Level, "ERROR") {
			t.Errorf("log --errors returned a %s row — the level filter does not narrow: %+v", r.Level, r)
		}
	}
	// Soundness and completeness against records HA holds and whose level this
	// test chose: the ERROR probe must survive the filter, the WARNING probe
	// must not.
	if !hasProbe(rows, errProbe) {
		t.Errorf("log --errors dropped the ERROR record HA holds (%s: %q)\nrows: %+v",
			errProbe.logger, errProbe.message, rows)
	}
	if hasProbe(rows, warnProbe) {
		t.Errorf("log --errors kept the WARNING record (%s: %q) — the filter is a no-op",
			warnProbe.logger, warnProbe.message)
	}
}

func lastSegment(name string) string {
	if idx := strings.LastIndex(name, "."); idx >= 0 {
		return name[idx+1:]
	}
	return name
}

// componentOf reports whether hactl's reported component names a logger HA
// reported. It is now an exact lookup, because both sides carry the same value.
func componentOf(names map[string]bool, component string) bool {
	return names[component]
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func TestLogErrorsUnique(t *testing.T) {
	out := runHactl(t, "log", "--errors", "--unique")
	// Should show a table or empty result
	assertNotContains(t, out, "panic")
}

func TestLogComponent(t *testing.T) {
	// Filter by a component — homeassistant is always present in logs
	out := runHactl(t, "log", "--component", "homeassistant")
	assertNotContains(t, out, "panic")
}

func TestLogOutput(t *testing.T) {
	lines := runHactlLines(t, "log")
	// HA always produces some log output; at minimum we expect lines
	if len(lines) == 0 {
		t.Log("log returned empty output (possible on very fresh HA)")
	}
	for _, l := range lines {
		assertNotContains(t, l, "panic")
	}
}

func TestLogErrorsUniqueTable(t *testing.T) {
	out := runHactl(t, "log", "--errors", "--unique")
	if strings.TrimSpace(out) == "" {
		t.Log("no errors in log (expected for clean HA instance)")
		return
	}
	// If there are errors, output should have table format with a header
	if !strings.Contains(out, "count") && !strings.Contains(out, "message") && !strings.Contains(out, "no errors") {
		t.Errorf("log --errors --unique has unexpected format: %s", out)
	}
}
