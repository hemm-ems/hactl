//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// Dashboard writes (invariant H-12).
//
// `docs/testing.md` recorded that `dash save` "can each be replaced with a stub
// without any test failing": the existing round-trip test reads back through
// `dash show --raw`, so hactl both writes and verifies, and the pre-existing
// create/delete tests assert on hactl's own echo. Everything below reads the
// stored config straight from HA's `lovelace/config` and compares the whole
// document, including keys the command never mentions.
// ============================================================================

// dashConfigFromHA reads a dashboard's stored config directly from HA.
func dashConfigFromHA(t *testing.T, inst *hatest.Instance, urlPath string) map[string]any {
	t.Helper()
	ws := writeWS(t, inst)
	raw, err := ws.DashboardConfigRaw(context.Background(), urlPath)
	if err != nil {
		t.Fatalf("reading dashboard config %q from HA: %v", urlPath, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parsing dashboard config %q: %v", urlPath, err)
	}
	return out
}

// dashEntryFromHA returns HA's own dashboard-list entry, or false if absent.
func dashEntryFromHA(t *testing.T, inst *hatest.Instance, urlPath string) (haapi.LovelaceDashboard, bool) {
	t.Helper()
	ws := writeWS(t, inst)
	list, err := ws.DashboardList(context.Background())
	if err != nil {
		t.Fatalf("listing dashboards: %v", err)
	}
	for _, d := range list {
		if d.URLPath == urlPath {
			return d, true
		}
	}
	return haapi.LovelaceDashboard{}, false
}

func writeJSONFile(t *testing.T, body string) string {
	t.Helper()
	p := t.TempDir() + "/dash.json"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDashCreateSaveDeleteRoundTrip drives the whole dashboard write family and
// checks each step against HA rather than against hactl's output.
func TestDashCreateSaveDeleteRoundTrip(t *testing.T) {
	inst := getWriteHA(t)
	const urlPath = "hactl-h12-roundtrip"
	ctx := context.Background()

	t.Cleanup(func() {
		ws := haapi.NewWSClient(inst.URL(), inst.Token())
		if err := ws.Connect(ctx); err != nil {
			return
		}
		defer func() { _ = ws.Close() }()
		if list, err := ws.DashboardList(ctx); err == nil {
			for _, d := range list {
				if d.URLPath == urlPath {
					_ = ws.DashboardDelete(ctx, d.ID)
				}
			}
		}
	})

	// --- create: dry-run writes nothing ---
	runHactlDir(t, inst.Dir(), "dash", "create", "--url-path", urlPath, "--title", "H12", "--icon", "mdi:home")
	if _, ok := dashEntryFromHA(t, inst, urlPath); ok {
		t.Fatal("dry-run create registered a dashboard in HA")
	}

	// --- create: confirmed write reaches HA, with the fields it was given ---
	runHactlDir(t, inst.Dir(), "dash", "create", "--url-path", urlPath,
		"--title", "H12 Round Trip", "--icon", "mdi:home", "--confirm")
	entry, ok := dashEntryFromHA(t, inst, urlPath)
	if !ok {
		t.Fatal("create did not reach HA: dashboard is not in lovelace/dashboards/list")
	}
	if entry.Title != "H12 Round Trip" {
		t.Errorf("HA stored title %q, want %q", entry.Title, "H12 Round Trip")
	}
	// The icon is a second witness: the command never echoes it back.
	if entry.Icon != "mdi:home" {
		t.Errorf("HA stored icon %q, want %q", entry.Icon, "mdi:home")
	}
	if entry.Mode != "storage" {
		t.Errorf("HA stored mode %q, want storage (a YAML-mode dashboard cannot be saved to)", entry.Mode)
	}

	// --- save: dry-run writes nothing ---
	const first = `{"views":[{"title":"One","path":"one","cards":[{"type":"markdown","content":"first"}]}]}`
	firstFile := writeJSONFile(t, first)
	runHactlDir(t, inst.Dir(), "dash", "save", urlPath, "--file", firstFile, "--confirm")
	stored := dashConfigFromHA(t, inst, urlPath)

	var want map[string]any
	if err := json.Unmarshal([]byte(first), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(stored, want) {
		t.Fatalf("save did not store the document byte-for-byte:\n stored: %s\n want:   %s",
			mustJSON(t, stored), mustJSON(t, want))
	}

	// --- save is a FULL replacement, and a dry-run over it changes nothing ---
	const second = `{"views":[{"title":"Two","path":"two","cards":[{"type":"markdown","content":"second"}]},` +
		`{"title":"Three","path":"three","cards":[]}]}`
	secondFile := writeJSONFile(t, second)
	runHactlDir(t, inst.Dir(), "dash", "save", urlPath, "--file", secondFile)
	if got := dashConfigFromHA(t, inst, urlPath); !reflect.DeepEqual(got, want) {
		t.Fatalf("dry-run save overwrote the stored config:\n got: %s", mustJSON(t, got))
	}

	runHactlDir(t, inst.Dir(), "dash", "save", urlPath, "--file", secondFile, "--confirm")
	var want2 map[string]any
	if err := json.Unmarshal([]byte(second), &want2); err != nil {
		t.Fatal(err)
	}
	got := dashConfigFromHA(t, inst, urlPath)
	if !reflect.DeepEqual(got, want2) {
		t.Fatalf("second save did not replace the whole document:\n stored: %s\n want:   %s",
			mustJSON(t, got), mustJSON(t, want2))
	}

	// --- delete: dry-run leaves it, confirm removes it from HA's own list ---
	runHactlDir(t, inst.Dir(), "dash", "delete", urlPath)
	if _, ok := dashEntryFromHA(t, inst, urlPath); !ok {
		t.Fatal("dry-run delete removed the dashboard from HA")
	}
	runHactlDir(t, inst.Dir(), "dash", "delete", urlPath, "--confirm")
	if _, ok := dashEntryFromHA(t, inst, urlPath); ok {
		t.Fatal("delete --confirm did not reach HA: dashboard is still listed")
	}
}

// TestDashReplaceRoundTrip proves `dash replace --confirm` rewrites exactly the
// values it reported and leaves the rest of the document identical.
func TestDashReplaceRoundTrip(t *testing.T) {
	inst := getWriteHA(t)
	const urlPath = "hactl-h12-replace"
	ctx := context.Background()

	ws := writeWS(t, inst)
	created, err := ws.DashboardCreate(ctx, haapi.DashboardCreateParams{
		URLPath: urlPath, Title: "H12 Replace", ShowInSidebar: true,
	})
	if err != nil {
		t.Fatalf("creating dashboard: %v", err)
	}
	t.Cleanup(func() { _ = ws.DashboardDelete(ctx, created.ID) })

	const body = `{"views":[{"title":"keep me","path":"v","cards":[` +
		`{"type":"entities","entities":["sensor.old_name","sensor.untouched"]},` +
		`{"type":"markdown","content":"sensor.old_name"}]}]}`
	if err := ws.DashboardConfigSave(ctx, urlPath, json.RawMessage(body)); err != nil {
		t.Fatalf("seeding dashboard config: %v", err)
	}
	before := dashConfigFromHA(t, inst, urlPath)

	// Dry-run must not write.
	runHactlDir(t, inst.Dir(), "dash", "replace", "sensor.old_name", "sensor.new_name", urlPath)
	if got := dashConfigFromHA(t, inst, urlPath); !reflect.DeepEqual(got, before) {
		t.Fatalf("dry-run replace wrote to HA:\n got: %s", mustJSON(t, got))
	}

	runHactlDir(t, inst.Dir(), "dash", "replace", "sensor.old_name", "sensor.new_name", urlPath, "--confirm")

	after := dashConfigFromHA(t, inst, urlPath)
	const wantBody = `{"views":[{"title":"keep me","path":"v","cards":[` +
		`{"type":"entities","entities":["sensor.new_name","sensor.untouched"]},` +
		`{"type":"markdown","content":"sensor.new_name"}]}]}`
	var want map[string]any
	if err := json.Unmarshal([]byte(wantBody), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, want) {
		t.Fatalf("replace stored a different document than it reported:\n stored: %s\n want:   %s",
			mustJSON(t, after), mustJSON(t, want))
	}
}
