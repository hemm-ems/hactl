package cmd

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// entRenameStub wires the sources runEntRename touches: WS registries +
// dashboards, and a companion /v1/ref/scan.
func entRenameStub(t *testing.T, scanBody string) string {
	t.Helper()
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, scanBody)
	}))
	t.Cleanup(companionSrv.Close)

	ts := startCmdServer(t, map[string]any{
		"config/entity_registry/list": []any{
			map[string]any{"entity_id": "sensor.old", "unique_id": "u-old", "platform": "template"},
			map[string]any{"entity_id": "sensor.taken", "unique_id": "u-taken", "platform": "template"},
		},
		"lovelace/dashboards/list": []any{},
		"lovelace/config":          dashboardConfigWith("sensor.old"),
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	return ts.dir
}

// TestEntRenameDryRunRefusesUnresolvable — H-2 (confirm.manifest row): a
// malformed new id, an old id the registry does not hold, and a collision
// with an existing id all end the command before any plan is printed. The
// registry resolution is correctness, not courtesy: HA silently has no entry
// to rename for state-only entities (oracle: "Entity not found"), and it
// refuses collisions server-side ("already registered") — the preview must
// fail exactly where the confirmed run would.
func TestEntRenameDryRunRefusesUnresolvable(t *testing.T) {
	dir := entRenameStub(t, `{"target":"sensor.old","hits":[]}`)
	withFlagDir(t, dir)

	cases := []struct {
		name, oldID, newID, wantErr string
	}{
		{"malformed new id", "sensor.old", "nodomain", "not one Home Assistant accepts"},
		// The five shapes a live instance refused at confirm time while the
		// preview printed "would rename … references: 2" at exit 0 — the same
		// five, re-keyed to this stub's registry. HA's answers were
		// "Invalid entity ID" for four and "New entity ID should be same
		// domain" for the cross-domain one (measured 2026-07-31); a preview
		// that accepts them promises a rename --confirm cannot perform.
		{"a space", "sensor.old", "sensor.pg w5 bad", "not one Home Assistant accepts"},
		{"uppercase and punctuation", "sensor.old", "sensor.PG_w5_Bad!", "not one Home Assistant accepts"},
		{"a multi-byte character", "sensor.old", "sensor.pg_w5_🔥bad", "not one Home Assistant accepts"},
		{"more than one dot", "sensor.old", "sensor.pg.w5.bad", "not one Home Assistant accepts"},
		{"a doubled underscore", "sensor.old", "sensor.pg__w5", "not one Home Assistant accepts"},
		{"a trailing underscore", "sensor.old", "sensor.pg_w5_", "not one Home Assistant accepts"},
		{"across domains", "sensor.old", "switch.old", "same domain"},
		{"identical ids", "sensor.old", "sensor.old", "identical"},
		{"unknown old id", "sensor.ghost", "sensor.new", `"sensor.ghost" not found in the registry`},
		{"collision with existing id", "sensor.old", "sensor.taken", `"sensor.taken" already exists`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			err := runEntRename(context.Background(), &buf, tc.oldID, tc.newID)
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("err = %v, want it to contain %q", err, tc.wantErr)
			}
			if buf.Len() > 0 {
				t.Errorf("refusal printed a plan first: %q", buf.String())
			}
		})
	}
}

// TestEntRenameDryRunPlan — the preview is ONE plan carrying both halves:
// the registry rename and the count of references the confirmed run would
// rewrite (config hits + dashboard hits).
func TestEntRenameDryRunPlan(t *testing.T) {
	dir := entRenameStub(t,
		`{"target":"sensor.old","hits":[{"location":"automations.yaml","path":"[0].trigger[0].entity_id","matched_value":"sensor.old"}]}`)
	withFlagDir(t, dir)

	var buf bytes.Buffer
	if err := runEntRename(context.Background(), &buf, "sensor.old", "sensor.new"); err != nil {
		t.Fatalf("runEntRename dry-run: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"dry-run: would rename entity",
		"sensor.old",
		"sensor.new",
		"references:", // 1 config + 1 dashboard
		"2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run plan missing %q:\n%s", want, out)
		}
	}

	// --json: the same single plan object (H-10) — never two documents.
	buf.Reset()
	withFlagJSON(t, true)
	if err := runEntRename(context.Background(), &buf, "sensor.old", "sensor.new"); err != nil {
		t.Fatalf("runEntRename dry-run --json: %v", err)
	}
	obj, ok := assertValidJSON(t, buf.String()).(map[string]any)
	if !ok {
		t.Fatalf("dry-run JSON is not one object: %s", buf.String())
	}
	details, _ := obj["details"].(map[string]any)
	if got, _ := details["references"].(float64); got != 2 {
		t.Errorf("references = %v, want 2", details["references"])
	}
}
