//go:build livefire

package livefire

import (
	"encoding/json"
	"strings"
	"testing"
)

// WP9 — the trace and flow families, and rig capability R7: a config entry with
// an options flow and a selector-typed schema.
//
// Findings #66 #71 #72 #82 #83 #84. Read-only on the live profile: the only
// flows started there are transient (HA discards a form step nobody submits),
// and the one entry whose options flow is opened is `pg_*`-owned. The rig
// profile creates its own entry, which is what R7 is — the rig's config entries
// all come from `default_config` and not one of them has an options flow, so
// #82/#83 had no shape to fail against.

// TestSweepTraceShowAcceptsEveryAutomationIdentifier — finding #66.
//
// The manual's own automation paragraph names `trace show` in the list of
// commands that accept the config id, the alias, the entity_id and the object
// id. All four were answered with `invalid trace ID format`. The identifiers
// are taken from `auto ls`/`auto show` here rather than written down, because
// the law is "an identifier hactl PRINTS is an identifier hactl accepts" (H-17)
// and a hand-written id proves a weaker thing.
func TestSweepTraceShowAcceptsEveryAutomationIdentifier(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		ref, ok := anAutomationWithATrace(t, tgt)
		if !ok {
			// The rig's automations have never fired, so Home Assistant has
			// stored no trace to address — a case that skipped here would be
			// green by construction, which is the whole subject of
			// FIXPLAN-livefire.md §1. Firing one is a rig write and belongs to
			// the rig alone.
			if tgt.Profile != Rig {
				t.Skip("no automation on this instance has a stored trace to address")
			}
			ref, ok = triggerARigAutomation(t, tgt)
			if !ok {
				t.Skip("could not make the rig produce a trace")
			}
		}

		for _, form := range identifierFormsOf(t, tgt, ref) {
			t.Run(form.kind, func(t *testing.T) {
				out, err := tgt.Read(t, "trace", "show", form.value, "--tokensmax", "0")
				if err != nil {
					t.Fatalf("`trace show %q` (%s) failed: %v\n%s", form.value, form.kind, err, truncate(out))
				}
				if strings.TrimSpace(out) == "" {
					t.Errorf("`trace show %q` printed nothing", form.value)
				}
			})
		}

		// The boundary: a reference that names no automation and is no trace
		// address is still an error, and the message says which of the two
		// things it failed to be.
		stderr, err := tgt.ReadDiagnostic(t, "trace", "show", "pg-w9-not-a-thing")
		if err == nil {
			t.Error("a reference that names nothing must fail")
		}
		if !strings.Contains(stderr, "neither a trace address") {
			t.Errorf("the refusal does not say what it looked for:\n%s", truncate(stderr))
		}
	})
}

// TestSweepATimeColumnHasOneShape — finding #71.
//
// A trace table showed `07-29 01:15` on four rows and a bare `01:15` on the
// fifth, whose run started today. The assertion is on the SHAPE of the column
// rather than on particular values, because which rows fall on today is a
// property of when the sweep runs.
func TestSweepATimeColumnHasOneShape(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, tc := range []struct {
			name   string
			args   []string
			column int
		}{
			{"ent_ls_last_changed", []string{"ent", "ls", "--top", "20", "--tokensmax", "0"}, -1},
			{"changes", []string{"changes", "--top", "20", "--tokensmax", "0"}, 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				assertUniformTimeColumn(t, tgt.MustRead(t, tc.args...), tc.column)
			})
		}
	})
}

// TestSweepACountNamesTheWindowItCounted — finding #72.
//
// `auto ls --since 1h --json` answered under the key `runs_24h`. Both windows
// are asserted in one case, because the defect is that the name did not move
// with the flag — checking either alone proves nothing.
func TestSweepACountNamesTheWindowItCounted(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, tc := range []struct{ since, want string }{{"1h", "runs_1h"}, {"24h", "runs_24h"}} {
			since, want := tc.since, tc.want
			rows := readRows(t, tgt, "auto", "ls", "--top", "1", "--since", since, "--json", "--tokensmax", "0")
			if len(rows) == 0 {
				t.Skip("this instance has no automations to count")
			}
			if _, ok := rows[0][want]; !ok {
				t.Errorf("--since %s produced keys %v, want %q", since, keysOf(rows[0]), want)
			}
			if want != "runs_24h" {
				if _, stale := rows[0]["runs_24h"]; stale {
					t.Errorf("--since %s still answers under runs_24h", since)
				}
			}
		}
	})
}

// TestSweepFlowInspectTypesItsFields — findings #82 and #83.
//
// Every selector-backed field rendered as "string": a template, a 28-value
// enum, a device picker. And the Default column was empty beside a field whose
// current value HA had sent in `description.suggested_value` — the one thing an
// options flow carries that a fresh config flow does not.
//
// The entry is the profile's own (R7): the live instance has a `pg_`-owned
// template helper, and the rig creates one, since nothing in `default_config`
// offers an options flow at all.
func TestSweepFlowInspectTypesItsFields(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		entry, ok := templateHelperEntry(t, tgt)
		if !ok {
			t.Skip("R7: no template helper config entry on this instance to open an options flow on")
		}

		// The dry run first, and not only for form: a family's FIRST --confirm
		// from a non-TTY caller is refused until the family's how-to has reached
		// the session (confirmGuard). Every write case in this tier opens the
		// same way, and a case that skipped it would be testing the guard.
		if plan, planErr := tgt.Read(t, "config", "options", entry, "--tokensmax", "0"); planErr != nil {
			t.Fatalf("dry run of `config options %s` failed: %v\n%s", entry, planErr, truncate(plan))
		}
		started := readDoc(t, tgt, "config", "options", entry, "--confirm", "--json", "--tokensmax", "0")
		flowID, _ := started["flow_id"].(string)
		if flowID == "" {
			t.Fatalf("`config options %s` returned no flow_id: %v", entry, started)
		}

		out := tgt.MustRead(t, "config", "flow-inspect", flowID, "--options", "--tokensmax", "0")
		if strings.Contains(out, " string ") {
			t.Errorf("a selector-backed field still renders as an unconstrained string:\n%s", truncate(out))
		}
		// The template selector is the one every template helper's form has, and
		// its suggested value is the entry's current state expression.
		if !strings.Contains(out, "template") {
			t.Errorf("no field is typed by its selector:\n%s", truncate(out))
		}
		if !strings.Contains(out, "{{") {
			t.Errorf("the current value HA suggests never reached the Default column:\n%s", truncate(out))
		}
	})
}

// TestSweepFlowSiblingsExplainTheSame404 — finding #84.
//
// `flow-step` on an unknown id explained that flows expire; `flow-inspect` on
// the same id handed back HA's bare 404. One condition, one explanation — and
// it names the cause a caller cannot guess, the id belonging to the other
// endpoint.
func TestSweepFlowSiblingsExplainTheSame404(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, args := range [][]string{
			{"config", "flow-inspect", "pg-w9-no-such-flow"},
			{"config", "flow-step", "pg-w9-no-such-flow", "--data", "{}"},
		} {
			stderr, err := tgt.ReadDiagnostic(t, args...)
			if err == nil {
				t.Errorf("%v must fail on an unknown flow id", args)
			}
			for _, want := range []string{"never existed", "expired", "--options"} {
				if !strings.Contains(stderr, want) {
					t.Errorf("%v does not explain the 404 (%q):\n%s", args, want, truncate(stderr))
				}
			}
		}
	})
}

// --- helpers ---------------------------------------------------------------

// identifierForm is one of the interchangeable names D-1 decided on.
type identifierForm struct{ kind, value string }

// identifierFormsOf reads every identifier form `auto show` prints for one
// automation, so the case asserts H-17 against what hactl itself displays.
func identifierFormsOf(t *testing.T, tgt Target, ref string) []identifierForm {
	t.Helper()
	doc := readDoc(t, tgt, "auto", "show", ref, "--json", "--tokensmax", "0")
	forms := []identifierForm{{"object_id", ref}}
	if v, ok := doc["config_id"].(string); ok && v != "" {
		forms = append(forms, identifierForm{"config_id", v})
	}
	if v, ok := doc["entity_id"].(string); ok && v != "" {
		forms = append(forms, identifierForm{"entity_id", v})
	}
	if v, ok := doc["alias"].(string); ok && v != "" {
		forms = append(forms, identifierForm{"alias", v})
	}
	return forms
}

// anAutomationWithATrace finds an automation the instance has actually stored a
// run for, since `trace show` can only answer about one of those.
func anAutomationWithATrace(t *testing.T, tgt Target) (string, bool) {
	t.Helper()
	for _, row := range readRows(t, tgt, "auto", "ls", "--top", "50", "--json", "--tokensmax", "0") {
		id, _ := row["id"].(string)
		if id == "" {
			continue
		}
		if out, err := tgt.Read(t, "auto", "show", id, "--json", "--tokensmax", "0"); err == nil {
			var doc map[string]any
			if json.Unmarshal([]byte(out), &doc) == nil {
				if traces, ok := doc["traces"].([]any); ok && len(traces) > 0 {
					return id, true
				}
			}
		}
	}
	return "", false
}

// triggerARigAutomation fires one of the rig's automations so Home Assistant
// stores a trace for it, and returns the identifier it fired.
//
// It is the rig half of R7's sibling problem: the fixture's automations exist
// and have never run, so every question about a RUN was unanswerable there and
// the case above could only ever be proved on the live profile. The write is
// deliberately confined to the rig — Target.Write's guard would refuse it on a
// real house, which is the intent.
func triggerARigAutomation(t *testing.T, tgt Target) (string, bool) {
	t.Helper()
	rows := readRows(t, tgt, "auto", "ls", "--top", "5", "--json", "--tokensmax", "0")
	if len(rows) == 0 {
		return "", false
	}
	id, _ := rows[0]["id"].(string)
	if id == "" {
		return "", false
	}
	entity := "automation." + id
	data := `{"entity_id":"` + entity + `"}`
	// Dry run first: confirmGuard refuses a family's first --confirm from a
	// non-TTY caller.
	if _, err := tgt.Read(t, "svc", "call", "automation.trigger", "-d", data, "--tokensmax", "0"); err != nil {
		return "", false
	}
	if _, err := tgt.Write(t, []string{entity},
		[]string{"svc", "call", "automation.trigger", "-d", "--confirm", "--tokensmax", "0", data},
		[]string{"svc", "call", "automation.trigger", "-d", data, "--confirm", "--tokensmax", "0"}); err != nil {
		return "", false
	}
	// HA writes the trace as the run finishes; the poll is bounded and the
	// failure is a skip rather than a red case, because a rig that will not
	// produce a trace is a missing capability and not a defect in the product.
	for range 10 {
		if _, ok := anAutomationWithATrace(t, tgt); ok {
			return id, true
		}
	}
	return "", false
}

// templateHelperEntry returns a template config entry to open an options flow
// on, creating one on the rig where none exists.
//
// On the live profile it must be `pg_`-owned: an options flow is transient and
// changes nothing, but the safety rail is about what a case may TARGET, not
// only about what it changes.
func templateHelperEntry(t *testing.T, tgt Target) (string, bool) {
	t.Helper()
	for _, row := range readRows(t, tgt, "config", "entries", "--full", "--json", "--tokensmax", "0") {
		domain, _ := row["domain"].(string)
		title, _ := row["title"].(string)
		id, _ := row["entry_id"].(string)
		if domain != "template" || id == "" {
			continue
		}
		if tgt.Profile == Live && !strings.HasPrefix(title, "pg_") {
			continue
		}
		return id, true
	}
	if tgt.Profile == Live {
		return "", false
	}
	return createRigTemplateHelper(t, tgt)
}

// createRigTemplateHelper builds the rig's R7 shape: a template binary_sensor
// helper, created through Home Assistant's own config-flow API, which is the
// only way a config entry with an options flow comes into existence.
//
// It is a WRITE, and it belongs to the rig alone — the guard in Target.Write
// would refuse it on the live profile, which is exactly the intent.
func createRigTemplateHelper(t *testing.T, tgt Target) (string, bool) {
	t.Helper()
	// The dry run first — confirmGuard refuses a family's first --confirm from a
	// non-TTY caller, which is what made this return exit 1 with an empty stdout
	// the first time it ran.
	if plan, planErr := tgt.Read(t, "config", "flow-start", "template", "--tokensmax", "0"); planErr != nil {
		t.Logf("R7: dry run of `config flow-start template` failed: %v\n%s", planErr, truncate(plan))
		return "", false
	}
	out, err := tgt.Read(t, "config", "flow-start", "template", "--confirm", "--json", "--tokensmax", "0")
	if err != nil {
		stderr, _ := tgt.ReadDiagnostic(t, "config", "flow-start", "template", "--confirm", "--json")
		t.Logf("R7: `config flow-start template` failed: %v\n%s", err, truncate(stderr))
		return "", false
	}
	var started map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &started); jsonErr != nil {
		t.Logf("R7: `config flow-start template` is not JSON: %v\n%s", jsonErr, truncate(out))
		return "", false
	}
	flowID, _ := started["flow_id"].(string)
	if flowID == "" {
		t.Logf("R7: `config flow-start template` returned no flow_id: %v", started)
		return "", false
	}
	// The template integration opens with a menu of entity kinds.
	if _, err := tgt.Read(t, "config", "flow-step", flowID, "--data",
		`{"next_step_id":"binary_sensor"}`, "--confirm", "--json", "--tokensmax", "0"); err != nil {
		t.Logf("R7: stepping to binary_sensor failed: %v", err)
		return "", false
	}
	created := readDoc(t, tgt, "config", "flow-step", flowID, "--data",
		`{"name":"pg_w9_rig_helper","state":"{{ true }}"}`, "--confirm", "--json", "--tokensmax", "0")
	if typ, _ := created["type"].(string); typ != "create_entry" {
		t.Logf("R7: the template flow did not create an entry: %v", created)
		return "", false
	}
	for _, row := range readRows(t, tgt, "config", "entries", "--full", "--json", "--tokensmax", "0") {
		if domain, _ := row["domain"].(string); domain == "template" {
			if id, _ := row["entry_id"].(string); id != "" {
				return id, true
			}
		}
	}
	return "", false
}

// assertUniformTimeColumn requires every value in a table's time column to have
// the same shape. col is the column index, or -1 for the last one.
func assertUniformTimeColumn(t *testing.T, out string, col int) {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 3 {
		t.Skip("not enough rows on this instance to compare a column against itself")
	}
	var widths []int
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) == 0 || strings.HasPrefix(line, "…") {
			continue
		}
		idx := col
		if idx < 0 {
			idx = len(fields) - 1
		}
		if idx >= len(fields) {
			continue
		}
		cell := fields[idx]
		// A dated cell is "MM-DD HH:MM" — two fields once split on spaces — so
		// the shape is read from the pair, not from one of them.
		width := len(cell)
		if idx+1 < len(fields) && len(cell) == 5 && cell[2] == '-' {
			width = len(cell) + 1 + len(fields[idx+1])
		}
		widths = append(widths, width)
	}
	for i, w := range widths {
		if w != widths[0] {
			t.Errorf("row %d's time cell is %d characters and row 0's is %d — the column mixes the "+
				"dated and the bare form:\n%s", i, w, widths[0], truncate(out))
			return
		}
	}
}

// readRows runs a --json listing and decodes it as an array of objects.
func readRows(t *testing.T, tgt Target, args ...string) []map[string]any {
	t.Helper()
	out := tgt.MustRead(t, args...)
	var rows []map[string]any
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("%v --json is not an array of objects: %v\n%s", args, err, truncate(out))
	}
	return rows
}

// readDoc runs a --json command and decodes it as one object.
func readDoc(t *testing.T, tgt Target, args ...string) map[string]any {
	t.Helper()
	out := tgt.MustRead(t, args...)
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v --json is not an object: %v\n%s", args, err, truncate(out))
	}
	return doc
}

func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
