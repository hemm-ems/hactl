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
	message string // kept short: the renderer truncates at 60 chars
}

// component is what hactl's table shows for this probe: the logger's last
// dot-segment (cmd/log.go: shortComponent).
func (p logProbe) component() string {
	if idx := strings.LastIndex(p.logger, "."); idx >= 0 {
		return p.logger[idx+1:]
	}
	return p.logger
}

var (
	errProbe  = logProbe{level: "error", logger: "hactl.assertfloor.errsite", message: "assertion floor error probe"}
	warnProbe = logProbe{level: "warning", logger: "hactl.assertfloor.warnsite", message: "assertion floor warning probe"}
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
		if !got[lastSegment(name)] {
			t.Errorf("log omitted HA logger %q (component %q); hactl printed %v",
				name, lastSegment(name), keys(got))
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

// componentOf reports whether any logger name in names shortens to component.
func componentOf(names map[string]bool, component string) bool {
	for name := range names {
		if lastSegment(name) == component {
			return true
		}
	}
	return false
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
