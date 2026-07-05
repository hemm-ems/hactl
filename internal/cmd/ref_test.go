package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeRefEnv writes a .env wiring both HA (WS/REST) and the companion so
// connectRefSources resolves the companion via COMPANION_URL discovery.
func writeRefEnv(t *testing.T, dir, haURL, companionURL string) {
	t.Helper()
	env := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=test-token\nCOMPANION_URL=%s\nCOMPANION_TOKEN=test-token\n", haURL, companionURL)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
}

// dashboardConfigWith returns a minimal Lovelace config whose single card
// references entityID, so jsonwalk finds it at views[0].cards[0].entity.
func dashboardConfigWith(entityID string) map[string]any {
	return map[string]any{
		"views": []any{map[string]any{"cards": []any{map[string]any{"entity": entityID}}}},
	}
}

func withRefConfirm(t *testing.T, v bool) {
	t.Helper()
	old := flagRefConfirm
	flagRefConfirm = v
	t.Cleanup(func() { flagRefConfirm = old })
}

func TestRunRefScan_MergesConfigAndDashboardHits(t *testing.T) {
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ref/scan" {
			t.Fatalf("unexpected companion path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("target"); got != "sensor.old" {
			t.Fatalf("target query = %q, want sensor.old", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"target":"sensor.old","hits":[{"location":"automations.yaml","path":"[0].trigger[0].entity_id","matched_value":"sensor.old"}]}`)
	}))
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/config":          dashboardConfigWith("sensor.old"),
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runRefScan(context.Background(), &buf, "sensor.old"); err != nil {
		t.Fatalf("runRefScan failed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"config", "automations.yaml", "[0].trigger[0].entity_id",
		"dashboard", "(default)", "views[0].cards[0].entity",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("scan output missing %q\n%s", want, out)
		}
	}
}

func TestRunRefScan_NoReferences(t *testing.T) {
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"target":"sensor.absent","hits":[]}`)
	}))
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/config":          dashboardConfigWith("sensor.other"),
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runRefScan(context.Background(), &buf, "sensor.absent"); err != nil {
		t.Fatalf("runRefScan failed: %v", err)
	}
	if !strings.Contains(buf.String(), "not referenced") {
		t.Errorf("output = %q, want 'not referenced'", buf.String())
	}
}

// refReplaceServer stubs the companion /v1/ref/replace, capturing the dry_run
// flag it received and returning the given status + one config change.
func refReplaceServer(t *testing.T, status string, gotDryRun *any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ref/replace" {
			t.Fatalf("unexpected companion path: %s", r.URL.Path)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decoding replace body: %v", err)
		}
		*gotDryRun = body["dry_run"]
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"status":%q,"changes":[{"location":"automations.yaml","path":"[0].trigger[0].entity_id","before":"sensor.old","after":"sensor.new"}]}`, status)
	}))
}

func TestRunRefReplace_DryRunReportsAndDoesNotWrite(t *testing.T) {
	var gotDryRun any = "unset"
	companionSrv := refReplaceServer(t, "dry_run", &gotDryRun)
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/info":            map[string]any{"mode": "storage"},
		"lovelace/config":          dashboardConfigWith("sensor.old"),
		"lovelace/config/save":     map[string]any{},
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
	withRefConfirm(t, false)

	var buf bytes.Buffer
	if err := runRefReplace(context.Background(), &buf, "sensor.old", "sensor.new"); err != nil {
		t.Fatalf("runRefReplace failed: %v", err)
	}

	if gotDryRun != true {
		t.Errorf("companion dry_run = %v, want true", gotDryRun)
	}
	if n := ts.commandCount("lovelace/config/save"); n != 0 {
		t.Errorf("dashboard saved %d time(s) in dry-run, want 0", n)
	}
	out := buf.String()
	for _, want := range []string{
		"dry-run", "sensor.old", "sensor.new",
		"config", "automations.yaml", "dashboard", "(default)", "pending", "use --confirm",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q\n%s", want, out)
		}
	}
}

func TestRunRefReplace_ConfirmSavesStorageDashboard(t *testing.T) {
	var gotDryRun any = "unset"
	companionSrv := refReplaceServer(t, "applied", &gotDryRun)
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/info":            map[string]any{"mode": "storage"},
		"lovelace/config":          dashboardConfigWith("sensor.old"),
		"lovelace/config/save":     map[string]any{},
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
	withRefConfirm(t, true)

	var buf bytes.Buffer
	if err := runRefReplace(context.Background(), &buf, "sensor.old", "sensor.new"); err != nil {
		t.Fatalf("runRefReplace failed: %v", err)
	}

	if gotDryRun != false {
		t.Errorf("companion dry_run = %v, want false", gotDryRun)
	}
	if n := ts.commandCount("lovelace/config/save"); n != 1 {
		t.Errorf("dashboard saved %d time(s) on confirm, want 1", n)
	}
	out := buf.String()
	for _, want := range []string{"renamed", "config", "applied", "dashboard", "saved"} {
		if !strings.Contains(out, want) {
			t.Errorf("confirm output missing %q\n%s", want, out)
		}
	}
}

func TestRunRefReplace_ConfirmSkipsNonStorageDashboard(t *testing.T) {
	// Companion applies config changes; the dashboard is auto-generated and
	// must be reported but never saved.
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"applied","changes":[]}`)
	}))
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/info":            map[string]any{"mode": "auto-gen"},
		"lovelace/config":          dashboardConfigWith("sensor.old"),
		"lovelace/config/save":     map[string]any{},
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
	withRefConfirm(t, true)

	var buf bytes.Buffer
	if err := runRefReplace(context.Background(), &buf, "sensor.old", "sensor.new"); err != nil {
		t.Fatalf("runRefReplace failed: %v", err)
	}

	if n := ts.commandCount("lovelace/config/save"); n != 0 {
		t.Errorf("non-storage dashboard saved %d time(s), want 0", n)
	}
	if !strings.Contains(buf.String(), "skipped: not storage-mode") {
		t.Errorf("output missing non-storage skip note\n%s", buf.String())
	}
}
