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

// The fake HA below deliberately serves what a real HA serves (captured from
// HA 2026.7.4, internal/integration/lovelace_oracle_test.go): no `lovelace/info`
// fixture at all, because that route answers only {"resource_mode": ...} and
// says nothing about the default dashboard's state. The previous fixtures fed
// the fake a `mode` field HA never emits, so the unit tier certified a check
// that was vacuous against every real instance (D71).

func TestRunRefReplace_DryRunReportsAndDoesNotWrite(t *testing.T) {
	var gotDryRun any = "unset"
	companionSrv := refReplaceServer(t, "dry_run", &gotDryRun)
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
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
	// D71: the default dashboard HAS a stored config here, so no row may claim
	// it was skipped for its mode.
	if strings.Contains(out, "skipped") {
		t.Errorf("stored default dashboard must not be reported as skipped\n%s", out)
	}
}

func TestRunRefReplace_ConfirmSavesStoredDefaultDashboard(t *testing.T) {
	// D71 regression: the default dashboard has a stored config (lovelace/config
	// answers), so a confirmed replace must rewrite it — not report
	// "skipped: not storage-mode" on the strength of a field HA never sent.
	var gotDryRun any = "unset"
	companionSrv := refReplaceServer(t, "applied", &gotDryRun)
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
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

func TestRunRefReplace_AutoGeneratedDefaultIsACompleteAnswer(t *testing.T) {
	// The auto-generated default has no stored config, so it holds no
	// references — zero hits there is the complete truth, not a partial scan.
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"dry_run","changes":[{"location":"automations.yaml","path":"[0].trigger[0].entity_id","before":"sensor.old","after":"sensor.new"}]}`)
	}))
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/config":          wsErrorResponse{Code: "config_not_found", Message: "No config found."},
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
	withRefConfirm(t, false)

	var buf bytes.Buffer
	if err := runRefReplace(context.Background(), &buf, "sensor.old", "sensor.new"); err != nil {
		t.Fatalf("an auto-generated default must not fail the rename: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "automations.yaml") {
		t.Errorf("config change missing from report\n%s", out)
	}
	if strings.Contains(out, "(default)") {
		t.Errorf("auto-generated default must contribute no rows\n%s", out)
	}
}

func TestRunRefReplace_UnscannableDashboardRefusesUnlessAllowPartial(t *testing.T) {
	// lovelace/config fails with something other than config_not_found: hactl
	// cannot know whether the default dashboard references the old value, so
	// the scan is partial — loud failure unless --allow-partial (D-6).
	var companionCalls int
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		companionCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"applied","changes":[{"location":"automations.yaml","path":"[0].trigger[0].entity_id","before":"sensor.old","after":"sensor.new"}]}`)
	}))
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/config":          wsErrorResponse{Code: "error", Message: "boom"},
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)

	// Confirmed run: refuse before anything is written anywhere.
	withRefConfirm(t, true)
	var buf bytes.Buffer
	err := runRefReplace(context.Background(), &buf, "sensor.old", "sensor.new")
	if err == nil || !strings.Contains(err.Error(), "--allow-partial") {
		t.Fatalf("expected loud partial-scan refusal naming --allow-partial, got %v", err)
	}
	if !strings.Contains(err.Error(), "(default)") {
		t.Errorf("refusal must name the unscannable dashboard, got: %v", err)
	}
	if companionCalls != 0 {
		t.Errorf("companion was called %d time(s) before the refusal; config files may have been written", companionCalls)
	}

	// Dry run refuses the same way (H-2: the preview fails where --confirm would).
	withRefConfirm(t, false)
	buf.Reset()
	if err := runRefReplace(context.Background(), &buf, "sensor.old", "sensor.new"); err == nil {
		t.Fatal("dry run must refuse where the confirmed run refuses")
	}

	// --allow-partial: proceed over what could be read.
	withRefFlag(t, &flagRefAllowPartial)
	buf.Reset()
	if err := runRefReplace(context.Background(), &buf, "sensor.old", "sensor.new"); err != nil {
		t.Fatalf("with --allow-partial: %v", err)
	}
	if !strings.Contains(buf.String(), "automations.yaml") {
		t.Errorf("partial run must still report the config change\n%s", buf.String())
	}
}

func TestRunRefReplace_YamlDefaultHitsRefuseUnlessAllowPartial(t *testing.T) {
	// A YAML-mode default dashboard IS retrievable (oracle: lovelace/config
	// answers with ui-lovelace.yaml's content, and lovelace/dashboards/list
	// carries it as url_path "lovelace", mode "yaml") but not writable
	// (lovelace/config/save → "Not supported"). References found there cannot
	// be renamed, so a confirmed run refuses rather than silently leaving
	// dangling references — unless --allow-partial (D-6).
	var companionCalls int
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		companionCalls++
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"applied","changes":[]}`)
	}))
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{map[string]any{
			"url_path": "lovelace", "title": "Overview", "mode": "yaml",
		}},
		"lovelace/config":      dashboardConfigWith("sensor.old"),
		"lovelace/config/save": map[string]any{},
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)

	// Confirmed run: refuse before anything is written anywhere.
	withRefConfirm(t, true)
	var buf bytes.Buffer
	err := runRefReplace(context.Background(), &buf, "sensor.old", "sensor.new")
	if err == nil || !strings.Contains(err.Error(), "--allow-partial") {
		t.Fatalf("expected loud refusal naming --allow-partial for yaml-mode hits, got %v", err)
	}
	if companionCalls != 0 {
		t.Errorf("companion was called %d time(s) before the refusal", companionCalls)
	}
	if n := ts.commandCount("lovelace/config/save"); n != 0 {
		t.Errorf("yaml dashboard saved %d time(s), want 0", n)
	}

	// With --allow-partial the run proceeds and reports the skip honestly.
	withRefFlag(t, &flagRefAllowPartial)
	buf.Reset()
	if err := runRefReplace(context.Background(), &buf, "sensor.old", "sensor.new"); err != nil {
		t.Fatalf("with --allow-partial: %v", err)
	}
	if n := ts.commandCount("lovelace/config/save"); n != 0 {
		t.Errorf("yaml dashboard saved %d time(s) under --allow-partial, want 0", n)
	}
	if !strings.Contains(buf.String(), "skipped: yaml-mode") {
		t.Errorf("output missing honest yaml-mode skip note\n%s", buf.String())
	}
	// The yaml default is one dashboard, listed as "lovelace" — it must not
	// also be scanned a second time under the "(default)" pseudo-target.
	if strings.Contains(buf.String(), "(default)") {
		t.Errorf("yaml default double-scanned as the (default) pseudo-target\n%s", buf.String())
	}
}

func TestRunRefReplace_YamlDefaultDryRunFailsWhereConfirmWould(t *testing.T) {
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"status":"dry_run","changes":[]}`)
	}))
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{map[string]any{
			"url_path": "lovelace", "title": "Overview", "mode": "yaml",
		}},
		"lovelace/config":      dashboardConfigWith("sensor.old"),
		"lovelace/config/save": map[string]any{},
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
	withRefConfirm(t, false)

	var buf bytes.Buffer
	err := runRefReplace(context.Background(), &buf, "sensor.old", "sensor.new")
	if err == nil || !strings.Contains(err.Error(), "--allow-partial") {
		t.Fatalf("dry run must fail where --confirm would (H-2), got %v", err)
	}
	// The plan is still rendered first, so the caller sees what is stuck.
	if !strings.Contains(buf.String(), "skipped: yaml-mode") {
		t.Errorf("dry-run plan must still report the yaml-mode rows\n%s", buf.String())
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

// withRefFlag enables a package-level bool flag for one test and restores it.
func withRefFlag(t *testing.T, p *bool) {
	t.Helper()
	old := *p
	*p = true
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
	}, statesHandler(`[{"entity_id":"sun.sun","state":"above_horizon"},{"entity_id":"sensor.real","state":"21.5"}]`))
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
	}, statesHandler(`[{"entity_id":"sensor.real","state":"21.5"}]`))
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

	// With --allow-partial it proceeds against the registry alone — and says so
	// in the report body, like every other source of the sweep. A degraded live
	// set the reader cannot see is the same defect whichever half went missing.
	withRefFlag(t, &flagRefAllowPartial)
	buf.Reset()
	if err := runRefValidate(context.Background(), &buf); err != nil {
		t.Fatalf("with --allow-partial: %v", err)
	}
	for _, want := range []string{"partial sweep", "live states"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("the report body must state the missing live-states half (%q)\n%s", want, buf.String())
		}
	}
}

// unreadableRegistryValidateServer stands up a fake HA that answers /api/states
// normally but fails config/entity_registry/list. Every other source of the
// sweep is readable, so the registry is the only thing making the answer
// partial. The companion reports one dangling config reference, so a run that
// proceeds has something to print.
func unreadableRegistryValidateServer(t *testing.T) {
	t.Helper()
	companionSrv := refEntitiesServer(t, `{"entities":[
		{"location":"automations.yaml","path":"[0].trigger[0].entity_id","key":"entity_id","matched_value":"sensor.gone"}
	]}`)
	t.Cleanup(companionSrv.Close)

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list":    []any{},
		"lovelace/config":             map[string]any{"views": []any{}},
		"config/entity_registry/list": wsErrorResponse{Code: "error", Message: "registry boom"},
	}, statesHandler(`[{"entity_id":"sensor.real","state":"21.5"}]`))
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
}

// TestRunRefValidate_UnreadableRegistryRefusesUnlessAllowPartial is D-7's rule on
// the source it missed. The entity registry is the third thing `ref validate`
// reads, and a registry that will not list (a transient WS failure, a token whose
// permissions differ for that call) left the live set short every disabled and
// currently-unloaded entity — announced only at slog.Warn, which a machine
// consumer cannot see. So `--exit-code`/`--json` received a verdict computed from
// an entity set hactl knew was incomplete, and nothing on stdout said so.
//
// The degradation runs toward false positives rather than false negatives, which
// makes the verdict wrong rather than falsely green — but wrong silently is still
// what the gate exists to prevent, so the posture is the shared one.
func TestRunRefValidate_UnreadableRegistryRefusesUnlessAllowPartial(t *testing.T) {
	tests := []struct {
		name        string
		flags       []*bool
		wantRefusal bool
		// wantBody are strings the report body must carry when the run proceeds.
		wantBody []string
		// notInBody must be absent from the report body.
		notInBody []string
		// wantWarn requires the scope on stderr instead of in the body, which is
		// where it has to go when the body's shape is a machine contract.
		wantWarn bool
	}{
		{
			name:        "exit_code_refuses",
			flags:       []*bool{&flagRefExitCode},
			wantRefusal: true,
		},
		{
			name:        "json_refuses",
			flags:       []*bool{&flagJSON},
			wantRefusal: true,
		},
		{
			name:  "exit_code_with_allow_partial_proceeds",
			flags: []*bool{&flagRefExitCode, &flagRefAllowPartial},
			// The mirror-image trap: an acknowledged partial must still be a
			// usable answer, so the dangling reference is reported, not withheld.
			wantBody: []string{"partial sweep", "entity registry", "sensor.gone"},
		},
		{
			name:  "json_with_allow_partial_proceeds",
			flags: []*bool{&flagJSON, &flagRefAllowPartial},
			// --json is a machine contract: the scope goes to stderr so the
			// document's shape does not change (H-10).
			wantBody:  []string{"sensor.gone"},
			notInBody: []string{"partial sweep"},
			wantWarn:  true,
		},
		{
			name:  "plain_text_answers_and_states_the_scope_in_the_body",
			flags: nil,
			// No --exit-code and no --json: a person can see the scope line, so
			// the run answers rather than refusing — the registry half is not
			// the stricter live-states half.
			wantBody: []string{"partial sweep", "entity registry", "registry boom", "sensor.gone"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			unreadableRegistryValidateServer(t)
			for _, f := range tc.flags {
				withRefFlag(t, f)
			}
			logBuf := captureDefaultLogger(t)

			var buf bytes.Buffer
			err := runRefValidate(context.Background(), &buf)

			if tc.wantRefusal {
				assertPartialSweepRefused(t, err, buf.String(),
					"--allow-partial", "the entity registry", "registry boom")
				return
			}
			assertValidateProceeded(t, err, buf.String(), tc.wantBody, tc.notInBody)
			if tc.wantWarn {
				assertPartialSweepWarning(t, logBuf.String())
			}
		})
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
	}, statesHandler(`[{"entity_id":"sensor.real","state":"21.5"}]`))
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
	withRefFlag(t, &flagRefExitCode)

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

func TestValidateScanGateError(t *testing.T) {
	boom := errors.New("companion unreachable")

	// No error from the scan -> never a gate error.
	if got := validateScanGateError("config files", nil, true, false); got != nil {
		t.Errorf("nil scan error should not gate, got %v", got)
	}
	// Interactive (neither --exit-code nor --json): a scan failure is reported in
	// the body, not fatal.
	if got := validateScanGateError("config files", boom, false, false); got != nil {
		t.Errorf("an interactive scan failure must not be fatal, got %v", got)
	}
	// Answering a machine, without --allow-partial: a scan failure must refuse.
	got := validateScanGateError("config files", boom, true, false)
	if got == nil {
		t.Fatal("a machine-gated run with a failed scan must return an error (vacuous gate)")
	}
	for _, want := range []string{"config files", "--allow-partial", "nothing was certified"} {
		if !strings.Contains(got.Error(), want) {
			t.Errorf("refusal missing %q: %v", want, got)
		}
	}
	// Machine-gated with --allow-partial: explicitly opted into a partial answer.
	if got := validateScanGateError("config files", boom, true, true); got != nil {
		t.Errorf("--allow-partial should permit a partial answer, got %v", got)
	}
}

// unscannableValidateServer stands up a fake HA whose only dashboard is the
// default and whose lovelace/config fails with something other than
// config_not_found — hactl cannot know what that dashboard references, so the
// sweep is partial. The companion reports one dangling config reference, so a
// run that proceeds has something to print.
func unscannableValidateServer(t *testing.T, lovelace any) {
	t.Helper()
	companionSrv := refEntitiesServer(t, `{"entities":[
		{"location":"automations.yaml","path":"[0].trigger[0].entity_id","key":"entity_id","matched_value":"sensor.gone"}
	]}`)
	t.Cleanup(companionSrv.Close)

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list":    []any{},
		"lovelace/config":             lovelace,
		"config/entity_registry/list": []any{map[string]any{"entity_id": "sensor.real"}},
	}, statesHandler(`[{"entity_id":"sensor.real","state":"21.5"}]`))
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
}

// assertPartialSweepRefused requires a partial sweep to end the command outright
// — non-zero, naming what it could not read and the escape hatch, with nothing
// written to the report, because a refusal that still prints a table has
// certified something. wantIn are the substrings the refusal must name, so every
// source of the sweep is held to one assertion shape rather than one per source.
func assertPartialSweepRefused(t *testing.T, err error, out string, wantIn ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a refusal, got nil (output: %q)", out)
	}
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		t.Fatalf("a partial sweep must refuse outright, not certify with a dangling-ref verdict: %v", err)
	}
	for _, want := range wantIn {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal missing %q: %v", want, err)
		}
	}
	if out != "" {
		t.Errorf("nothing may be certified before the refusal, got output:\n%s", out)
	}
}

// assertValidateProceeded requires the run to have answered (the --exit-code
// dangling-reference verdict is still an answer) and the report body to state
// the scope the caller can see.
func assertValidateProceeded(t *testing.T, err error, out string, wantBody, notInBody []string) {
	t.Helper()
	var ec interface{ ExitCode() int }
	if err != nil && !errors.As(err, &ec) {
		t.Fatalf("the run must proceed, got: %v", err)
	}
	for _, want := range wantBody {
		if !strings.Contains(out, want) {
			t.Errorf("report body missing %q\n%s", want, out)
		}
	}
	for _, notWant := range notInBody {
		if strings.Contains(out, notWant) {
			t.Errorf("report body must not contain %q\n%s", notWant, out)
		}
	}
}

// TestRunRefValidate_UnscannableDashboardRefusesUnlessAllowPartial is D-7: the
// tool whose entire job is to be a CI gate must not certify a tree it could only
// partly read. The refusal is gated by machine-readability — --exit-code makes
// the answer a verdict, --json makes it a document (H-10), and a stderr warning
// is invisible to both.
func TestRunRefValidate_UnscannableDashboardRefusesUnlessAllowPartial(t *testing.T) {
	unreadable := wsErrorResponse{Code: "error", Message: "boom"}

	tests := []struct {
		name        string
		flags       []*bool
		wantRefusal bool
		// wantBody are strings the report body must carry when the run proceeds.
		wantBody []string
		// notInBody must be absent from the report body.
		notInBody []string
		// wantWarn requires the scope on stderr instead of in the body, which is
		// where it has to go when the body's shape is a machine contract.
		wantWarn bool
	}{
		{
			name:        "exit_code_refuses",
			flags:       []*bool{&flagRefExitCode},
			wantRefusal: true,
		},
		{
			name:        "json_refuses",
			flags:       []*bool{&flagJSON},
			wantRefusal: true,
		},
		{
			name:     "exit_code_with_allow_partial_proceeds",
			flags:    []*bool{&flagRefExitCode, &flagRefAllowPartial},
			wantBody: []string{"partial sweep", "0 of 1 dashboard(s) scanned", "sensor.gone"},
		},
		{
			name:     "json_with_allow_partial_proceeds",
			flags:    []*bool{&flagJSON, &flagRefAllowPartial},
			wantBody: []string{"sensor.gone"},
			// --json is a machine contract: the scope goes to stderr so the
			// document's shape does not change (H-10).
			notInBody: []string{"partial sweep"},
			wantWarn:  true,
		},
		{
			name:  "plain_text_reports_the_partial_scope_in_the_body",
			flags: nil,
			wantBody: []string{
				"partial sweep", "0 of 1 dashboard(s) scanned", "(default)", "boom", "sensor.gone",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			unscannableValidateServer(t, unreadable)
			for _, f := range tc.flags {
				withRefFlag(t, f)
			}
			logBuf := captureDefaultLogger(t)

			var buf bytes.Buffer
			err := runRefValidate(context.Background(), &buf)

			if tc.wantRefusal {
				assertPartialSweepRefused(t, err, buf.String(), "--allow-partial", "(default)", "dashboard(s)")
				return
			}
			assertValidateProceeded(t, err, buf.String(), tc.wantBody, tc.notInBody)
			if tc.wantWarn {
				assertPartialSweepWarning(t, logBuf.String())
			}
		})
	}
}

// assertPartialSweepWarning requires a WARN line stating the sweep was partial.
// It is the --json path's only channel for the scope, so it must be at a level
// the caller sees; captureDefaultLogger listens at Info, so a Debug fails this.
func assertPartialSweepWarning(t *testing.T, log string) {
	t.Helper()
	for line := range strings.SplitSeq(strings.TrimSpace(log), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "partial sweep") {
			return
		}
	}
	t.Errorf("no WARN line reporting a partial sweep; log was:\n%s", log)
}

// TestRunRefValidate_AutoGeneratedDefaultIsNotAPartialSweep is D-7 point 3: the
// auto-generated default holds no config, so zero references there is the whole
// truth about it. Classifying it as unscannable would make `ref validate
// --exit-code` refuse on every fresh Home Assistant.
func TestRunRefValidate_AutoGeneratedDefaultIsNotAPartialSweep(t *testing.T) {
	autoGenerated := wsErrorResponse{Code: "config_not_found", Message: "No config found."}

	for _, tc := range []struct {
		name  string
		flags []*bool
	}{
		{name: "exit_code", flags: []*bool{&flagRefExitCode}},
		{name: "json", flags: []*bool{&flagJSON}},
		{name: "plain_text", flags: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			unscannableValidateServer(t, autoGenerated)
			for _, f := range tc.flags {
				withRefFlag(t, f)
			}

			var buf bytes.Buffer
			err := runRefValidate(context.Background(), &buf)

			// The only error allowed here is the dangling-reference verdict
			// itself (sensor.gone is genuinely dangling under --exit-code).
			var ec interface{ ExitCode() int }
			if err != nil && !errors.As(err, &ec) {
				t.Fatalf("an auto-generated default must not make the sweep partial: %v", err)
			}
			out := buf.String()
			if !strings.Contains(out, "sensor.gone") {
				t.Errorf("the dangling config reference must still be reported\n%s", out)
			}
			if strings.Contains(out, "partial") {
				t.Errorf("an auto-generated default is a complete answer of zero, not a partial sweep\n%s", out)
			}
		})
	}
}

// TestRunRefScan_UnscannableDashboardStillAnswers is D-7 point 5: a search tool
// answers "where is X?", so a dashboard it could not read is reported at WARN —
// visible at the level users actually run at, never slog.Debug — but is never
// fatal and never changes the stdout/--json shape.
func TestRunRefScan_UnscannableDashboardStillAnswers(t *testing.T) {
	companionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, `{"target":"sensor.old","hits":[{"location":"automations.yaml","path":"[0].trigger[0].entity_id","matched_value":"sensor.old"}]}`)
	}))
	defer companionSrv.Close()

	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/config":          wsErrorResponse{Code: "error", Message: "boom"},
	}, nil)
	writeRefEnv(t, ts.dir, ts.srv.URL, companionSrv.URL)
	withFlagDir(t, ts.dir)
	logBuf := captureDefaultLogger(t)

	var buf bytes.Buffer
	if err := runRefScan(context.Background(), &buf, "sensor.old"); err != nil {
		t.Fatalf("a search command must not fail on an unreadable dashboard: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "automations.yaml") {
		t.Errorf("the config hit must still be reported\n%s", out)
	}
	// The skip is a slog.Warn on stderr, never a row or a prose line on stdout.
	if strings.Contains(out, "partial") || strings.Contains(out, "boom") {
		t.Errorf("ref scan must not change its stdout shape for a skipped dashboard\n%s", out)
	}
	assertPartialScanWarning(t, logBuf.String())
}

// assertPartialScanWarning requires a WARN line saying the dashboard answer is
// partial. captureDefaultLogger listens at Info, so a slog.Debug — what both
// walks used to emit (D-7) — is invisible here and fails this.
func assertPartialScanWarning(t *testing.T, log string) {
	t.Helper()
	for line := range strings.SplitSeq(strings.TrimSpace(log), "\n") {
		if strings.Contains(line, "level=WARN") && strings.Contains(line, "answer is partial") {
			return
		}
	}
	t.Errorf("no WARN line reporting a partial dashboard answer; log was:\n%s", log)
}
