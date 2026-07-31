package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

// TestBooleanColumnsRenderAsJSONBooleans is the proof behind every `proven:`
// row in dev/surfaces/boolcell.manifest.
//
// Finding #59 reported `dash ls --json` answering `"admin": "true"`. The
// mechanism is that a cell is a string — a text table is made of strings — and
// renderJSON re-uses the cell, so the human wording IS the machine's value
// unless SetMachine says otherwise. `"false"` and `"no"` are non-empty strings,
// which makes the obvious `if row["admin"]` read every row as true; `boolCell`'s
// `""`/`"yes"` happens to coerce correctly and is still the wrong type, so a
// consumer comparing against `true` reads every ghost as not a ghost.
//
// The case is written over a TABLE of commands rather than per command, so
// adding a command to the surface means adding a row here — the shape of proof
// the boolcell surface exists to demand.
func TestBooleanColumnsRenderAsJSONBooleans(t *testing.T) {
	statesJSON, _ := json.Marshal([]map[string]any{
		{"entity_id": "sensor.live", "state": "21.5", "last_changed": "2026-01-01T10:00:00Z"},
		{
			"entity_id":    "sensor.ghost",
			"state":        "unavailable",
			"last_changed": "2026-01-01T09:00:00Z",
			"attributes":   map[string]any{"restored": true},
		},
		{"entity_id": "automation.live", "state": "on", "last_changed": "2026-01-01T10:00:00Z"},
		{
			"entity_id":    "automation.ghost",
			"state":        "unavailable",
			"last_changed": "2026-01-01T09:00:00Z",
			"attributes":   map[string]any{"restored": true},
		},
	})
	entriesJSON, _ := json.Marshal([]map[string]any{
		{"entry_id": "e1", "domain": "mqtt", "title": "MQTT", "state": "loaded", "supports_options": true},
		{"entry_id": "e2", "domain": "hue", "title": "Hue", "state": "loaded", "supports_options": false},
	})

	for _, tc := range []struct {
		name    string
		run     func(context.Context, *bytes.Buffer) error
		columns []string
		before  func(t *testing.T)
	}{
		{
			name: "dash ls",
			run:  func(ctx context.Context, buf *bytes.Buffer) error { return runDashLs(ctx, buf) },
			// Two columns, and only one was fixed in the first draft of this
			// work — which is the whole reason the surface keys a site per
			// rendered expression rather than per function.
			columns: []string{"sidebar", "admin"},
		},
		{
			// Unfiltered on purpose: --restored shows ONLY ghosts, so the
			// column could never be observed false. The column appears because
			// the listing contains one.
			name: "ent ls",
			//nolint:contextcheck // ctx reaches the command through listingCmd, which is how cobra carries it
			run:     func(ctx context.Context, buf *bytes.Buffer) error { return runEntLs(listingCmd(ctx, "ent", "ls"), buf) },
			columns: []string{restoredColumn},
			before: func(t *testing.T) {
				t.Helper()
				setFlagForTest(t, &flagEntDomain, "")
				setFlagForTest(t, &flagEntRestored, false)
			},
		},
		{
			name: "auto ls",
			//nolint:contextcheck // ctx reaches the command through listingCmd, which is how cobra carries it
			run: func(ctx context.Context, buf *bytes.Buffer) error {
				return runAutoLs(listingCmd(ctx, "auto", "ls"), buf)
			},
			columns: []string{restoredColumn},
			before: func(t *testing.T) {
				t.Helper()
				setFlagForTest(t, &flagAutoPattern, "")
				setFlagForTest(t, &flagAutoLabel, "")
				setFlagForTest(t, &flagAutoFailing, false)
				setFlagForTest(t, &flagAutoRestored, false)
				setFlagForTest(t, &flagSince, "24h")
			},
		},
		{
			name: "config entries",
			//nolint:contextcheck // ctx reaches the command through listingCmd, which is how cobra carries it
			run: func(ctx context.Context, buf *bytes.Buffer) error {
				return runConfigEntries(listingCmd(ctx, "config", "entries"), buf)
			},
			columns: []string{"options"},
			before: func(t *testing.T) {
				t.Helper()
				setFlagForTest(t, &flagConfigDomain, "")
			},
		},
		{
			// --all, because the default listing drops every ignored issue and
			// the `ignored` column would then be constant.
			name:    "issues --all",
			run:     func(ctx context.Context, buf *bytes.Buffer) error { return runIssues(ctx, buf) },
			columns: []string{"fixable", "ignored"},
			before: func(t *testing.T) {
				t.Helper()
				setFlagForTest(t, &flagIssuesAll, true)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts := startCmdServer(t, map[string]any{
				"lovelace/dashboards/list": []map[string]any{
					{"id": "a", "url_path": "dash-a", "title": "A", "mode": "storage",
						"require_admin": true, "show_in_sidebar": true},
					{"id": "b", "url_path": "dash-b", "title": "B", "mode": "storage",
						"require_admin": false, "show_in_sidebar": false},
				},
				"repairs/list_issues": map[string]any{"issues": []map[string]any{
					{"domain": "recorder", "issue_id": "a", "severity": "error", "is_fixable": true, "ignored": false},
					{"domain": "mqtt", "issue_id": "b", "severity": "warning", "is_fixable": false, "ignored": true},
				}},
			}, map[string]http.HandlerFunc{
				"/api/states": func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(statesJSON)
				},
				"/api/config/config_entries/entry": func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(entriesJSON)
				},
				"/api/logbook/": func(w http.ResponseWriter, _ *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					_, _ = fmt.Fprint(w, "[]")
				},
			})
			withFlagDir(t, ts.dir)
			setFlagForTest(t, &flagJSON, true)
			if tc.before != nil {
				tc.before(t)
			}

			var buf bytes.Buffer
			if err := tc.run(context.Background(), &buf); err != nil {
				t.Fatalf("%s failed: %v", tc.name, err)
			}
			var rows []map[string]any
			if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
				t.Fatalf("%s --json does not parse as an array: %v\n%s", tc.name, err, buf.String())
			}
			if len(rows) == 0 {
				t.Fatalf("%s produced no rows, so the columns below are unexercised:\n%s",
					tc.name, buf.String())
			}
			for _, column := range tc.columns {
				assertBooleanColumn(t, tc.name, column, rows)
			}
		})
	}
}

// assertBooleanColumn fails unless every row carries column as a JSON boolean,
// and unless the fixture shows it in both states — an assertion satisfied by a
// constant column proves only that one value survived.
func assertBooleanColumn(t *testing.T, command, column string, rows []map[string]any) {
	t.Helper()
	var sawTrue, sawFalse bool
	for _, row := range rows {
		value, present := row[column]
		if !present {
			t.Errorf("%s --json has no %q column", command, column)
			return
		}
		b, isBool := value.(bool)
		if !isBool {
			t.Errorf("%s --json reports %s as %T (%v); a boolean column is a JSON boolean",
				command, column, value, value)
			continue
		}
		sawTrue = sawTrue || b
		sawFalse = sawFalse || !b
	}
	// `false` is the value the string form got wrong in the direction that
	// matters, so a fixture that never produces one proves the wrong half.
	if !sawTrue || !sawFalse {
		t.Errorf("%s --json never reports %s as both true and false (true=%v false=%v); "+
			"the fixture does not exercise the column", command, column, sawTrue, sawFalse)
	}
}

// setFlagForTest sets a global command flag for the duration of a test and
// restores it afterwards.
func setFlagForTest[T any](t *testing.T, flag *T, value T) {
	t.Helper()
	old := *flag
	t.Cleanup(func() { *flag = old })
	*flag = value
}
