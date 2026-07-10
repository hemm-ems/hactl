package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// --- ref validate ---

// refEntitiesServer stubs the companion GET /v1/ref/entities with a fixed body.
func refEntitiesServer(t *testing.T, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ref/entities" {
			t.Fatalf("unexpected companion path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
}

// withRefFlag sets a package-level bool flag for one test and restores it.
func withRefFlag(t *testing.T, p *bool, v bool) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

func statesHandler(body string) map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, body)
		},
	}
}

func TestRunRefValidate_ReportsDanglingFiltersServicesAndStateOnly(t *testing.T) {
	// Config side: one dangling entity, one service (key=service), one state-only
	// entity (sun.sun, live via /api/states but absent from the registry), one live.
	companionSrv := refEntitiesServer(t, `{"entities":[
		{"location":"automations.yaml","path":"[0].trigger[0].entity_id","key":"entity_id","matched_value":"sensor.gone"},
		{"location":"automations.yaml","path":"[0].action[0].service","key":"service","matched_value":"light.turn_on"},
		{"location":"automations.yaml","path":"[0].condition[0].entity_id","key":"entity_id","matched_value":"sun.sun"},
		{"location":"configuration.yaml","path":"[0].entity_id","key":"entity_id","matched_value":"sensor.real"}
	]}`)
	defer companionSrv.Close()

	// Dashboard side: a bare-list dangling entity (proves entities[] + TerminalKey),
	// a live nested entity, and a tap_action.service that must not be flagged.
	dashCfg := map[string]any{"views": []any{map[string]any{"cards": []any{
		map[string]any{"type": "entities", "entities": []any{"sensor.dash_gone", map[string]any{"entity": "sensor.real"}}},
		map[string]any{"type": "button", "tap_action": map[string]any{"action": "call-service", "service": "script.turn_on"}},
	}}}}

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list":    []any{},
		"lovelace/config":             dashCfg,
		"config/entity_registry/list": []any{map[string]any{"entity_id": "sensor.real"}},
	}, statesHandler(`[{"entity_id":"sun.sun"},{"entity_id":"sensor.real"}]`))
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runRefValidate(context.Background(), &buf); err != nil {
		t.Fatalf("runRefValidate failed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"sensor.gone", "sensor.dash_gone", "2 dangling reference(s) to 2 entity(ies)"} {
		if !strings.Contains(out, want) {
			t.Errorf("validate output missing %q\n%s", want, out)
		}
	}
	// Services (key filter) and live entities (union) must never appear.
	for _, notWant := range []string{"light.turn_on", "sun.sun", "sensor.real", "script.turn_on"} {
		if strings.Contains(out, notWant) {
			t.Errorf("validate output should not contain %q (filtered or live)\n%s", notWant, out)
		}
	}
}

func TestRunRefValidate_NoDanglingReportsTemplateBlindSpot(t *testing.T) {
	companionSrv := refEntitiesServer(t, `{"entities":[]}`)
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list":    []any{},
		"lovelace/config":             map[string]any{"views": []any{}},
		"config/entity_registry/list": []any{map[string]any{"entity_id": "sensor.real"}},
	}, statesHandler(`[{"entity_id":"sensor.real"}]`))
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)

	var buf bytes.Buffer
	if err := runRefValidate(context.Background(), &buf); err != nil {
		t.Fatalf("runRefValidate failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "no dangling references found") {
		t.Errorf("output = %q, want happy-path message", out)
	}
	if !strings.Contains(out, "templates") {
		t.Errorf("happy path must disclose the template blind spot\n%s", out)
	}
}

func TestRunRefValidate_RegistryOnlyRefusedWithoutAllowPartial(t *testing.T) {
	companionSrv := refEntitiesServer(t, `{"entities":[]}`)
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list":    []any{},
		"lovelace/config":             map[string]any{"views": []any{}},
		"config/entity_registry/list": []any{map[string]any{"entity_id": "sensor.real"}},
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "nope", http.StatusNotFound) },
	})
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)

	// States unavailable + registry-only → refuse unless --allow-partial.
	var buf bytes.Buffer
	err := runRefValidate(context.Background(), &buf)
	if err == nil || !strings.Contains(err.Error(), "allow-partial") {
		t.Fatalf("expected allow-partial refusal, got %v", err)
	}

	// With --allow-partial it proceeds against the registry alone.
	withRefFlag(t, &flagRefAllowPartial, true)
	buf.Reset()
	if err := runRefValidate(context.Background(), &buf); err != nil {
		t.Fatalf("with --allow-partial: %v", err)
	}
}

func TestRunRefValidate_ExitCodeFlagReturnsNonZero(t *testing.T) {
	companionSrv := refEntitiesServer(t, `{"entities":[
		{"location":"automations.yaml","path":"[0].trigger[0].entity_id","key":"entity_id","matched_value":"sensor.gone"}
	]}`)
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list":    []any{},
		"lovelace/config":             map[string]any{"views": []any{}},
		"config/entity_registry/list": []any{map[string]any{"entity_id": "sensor.real"}},
	}, statesHandler(`[{"entity_id":"sensor.real"}]`))
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
	withRefFlag(t, &flagRefExitCode, true)

	var buf bytes.Buffer
	err := runRefValidate(context.Background(), &buf)
	var ec interface{ ExitCode() int }
	if !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Fatalf("expected ExitCode()==1 error, got %v", err)
	}
	// The report is still printed before the sentinel error is returned.
	if !strings.Contains(buf.String(), "sensor.gone") {
		t.Errorf("report should print before exit-code error\n%s", buf.String())
	}
}

func TestConfigScanGateError(t *testing.T) {
	boom := errors.New("companion unreachable")

	// No error from the scan -> never a gate error.
	if got := configScanGateError(nil, true, false); got != nil {
		t.Errorf("nil scan error should not gate, got %v", got)
	}
	// Interactive (no --exit-code): a scan failure is only a warning, not fatal.
	if got := configScanGateError(boom, false, false); got != nil {
		t.Errorf("without --exit-code the scan failure must not be fatal, got %v", got)
	}
	// --exit-code without --allow-partial: a scan failure must fail the gate.
	if got := configScanGateError(boom, true, false); got == nil {
		t.Error("--exit-code with a failed config scan must return an error (vacuous gate)")
	}
	// --exit-code with --allow-partial: explicitly opted into a partial gate.
	if got := configScanGateError(boom, true, true); got != nil {
		t.Errorf("--allow-partial should permit a partial gate, got %v", got)
	}
}
