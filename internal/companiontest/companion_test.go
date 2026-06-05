//go:build companion

package companiontest

import (
	"context"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/config"
)

func TestWireGuardStatusAndConfig(t *testing.T) {
	ctx := context.Background()

	// No tunnel configured yet → inactive. Exercises the client's WG status
	// path through the real Ingress-authed companion (a bare bearer token 401s).
	st, err := testClient.WireGuardStatus(ctx, "wg0")
	if err != nil {
		t.Fatalf("wireguard status: %v", err)
	}
	if st.State != "inactive" {
		t.Errorf("state = %q, want inactive", st.State)
	}

	// Pushing a config persists it (to /data/wireguard) and returns configured.
	// We don't start it here — bringing the interface up needs NET_ADMIN, which
	// the dockerized WG handshake suite in the companion repo already covers.
	conf := "[Interface]\nPrivateKey = WErlf9aEKj0DhPait1rk5OgOV2RGeikMkX2dbK8gxiHo=\nAddress = 10.6.0.2/24\n\n" +
		"[Peer]\nPublicKey = FE5OhQCNLLxF1OdDBIDMf5ktc8sEFngHoxy2o5iMhxs=\nAllowedIPs = 10.6.0.0/24\n"
	res, err := testClient.WireGuardConfig(ctx, "wg0", conf)
	if err != nil {
		t.Fatalf("wireguard config: %v", err)
	}
	if res.Status != "configured" || res.Tunnel != "wg0" {
		t.Errorf("config response = %+v, want configured/wg0", res)
	}
}

func TestHealth(t *testing.T) {
	h, err := testClient.Health(context.Background())
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if h.Status != "ok" {
		t.Errorf("status = %q, want ok", h.Status)
	}
	if h.Version == "" {
		t.Error("version is empty")
	}
}

func TestListConfigFiles(t *testing.T) {
	files, err := testClient.ListConfigFiles(context.Background())
	if err != nil {
		t.Fatalf("list files: %v", err)
	}
	if len(files.Files) == 0 {
		t.Fatal("no config files returned")
	}
	found := false
	for _, f := range files.Files {
		if f == "configuration.yaml" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("configuration.yaml not in file list: %v", files.Files)
	}
}

func TestReadConfigFile(t *testing.T) {
	f, err := testClient.ReadConfigFile(context.Background(), "configuration.yaml")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if f.Content == "" {
		t.Error("empty content for configuration.yaml")
	}
	if f.Path != "configuration.yaml" {
		t.Errorf("path = %q, want configuration.yaml", f.Path)
	}
}

func TestSecretsYamlDenied(t *testing.T) {
	_, err := testClient.ReadConfigFile(context.Background(), "secrets.yaml")
	if err == nil {
		t.Error("expected error reading secrets.yaml, got nil")
	}
}

func TestPathTraversal(t *testing.T) {
	_, err := testClient.ReadConfigFile(context.Background(), "../../../etc/passwd")
	if err == nil {
		t.Error("expected error for path traversal, got nil")
	}
}

func TestDryRun(t *testing.T) {
	ctx := context.Background()
	f, err := testClient.ReadConfigFile(ctx, "configuration.yaml")
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	wr, err := testClient.WriteConfigFile(ctx, "configuration.yaml", f.Content, true)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if wr.Status != "dry_run" {
		t.Errorf("status = %q, want dry_run", wr.Status)
	}
}

func TestWriteNewFile(t *testing.T) {
	ctx := context.Background()
	content := "hactl_e2e_test:\n  key: value\n"

	wr, err := testClient.WriteConfigFile(ctx, "hactl-e2e-test.yaml", content, false)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if wr.Status != "applied" {
		t.Errorf("status = %q, want applied", wr.Status)
	}

	// Verify readable
	f, err := testClient.ReadConfigFile(ctx, "hactl-e2e-test.yaml")
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if f.Content == "" {
		t.Error("written file has empty content on readback")
	}
}

// --- Template CRUD integration tests ---

func TestListTemplates(t *testing.T) {
	r, err := testClient.ListTemplates(context.Background())
	if err != nil {
		t.Fatalf("list templates: %v", err)
	}
	// We seeded one template sensor
	if len(r.Templates) == 0 {
		t.Fatal("expected at least 1 template (seeded)")
	}
	found := false
	for _, tpl := range r.Templates {
		if tpl.UniqueID == "seeded_test_sensor" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("seeded_test_sensor not found in templates: %+v", r.Templates)
	}
}

func TestCreateAndGetTemplate(t *testing.T) {
	ctx := context.Background()
	content := "name: E2E Test Sensor\nunique_id: e2e_test_tpl_sensor\nstate: \"{{ 42 }}\"\nunit_of_measurement: \"W\"\n"

	cr, err := testClient.CreateTemplate(ctx, content, "sensor")
	if err != nil {
		t.Fatalf("create template: %v", err)
	}
	if cr.Status != "created" {
		t.Errorf("status = %q, want created", cr.Status)
	}
	if cr.UniqueID == "" {
		t.Fatal("unique_id is empty after create")
	}

	// Get it back
	got, err := testClient.GetTemplate(ctx, cr.UniqueID)
	if err != nil {
		t.Fatalf("get template: %v", err)
	}
	if got.UniqueID != cr.UniqueID {
		t.Errorf("unique_id = %q, want %q", got.UniqueID, cr.UniqueID)
	}
	if got.Content == "" {
		t.Error("content is empty")
	}

	// Update with dry-run
	updated := "name: E2E Updated\nunique_id: " + cr.UniqueID + "\nstate: \"{{ 99 }}\"\n"
	wr, err := testClient.WriteTemplate(ctx, cr.UniqueID, updated, true)
	if err != nil {
		t.Fatalf("write template dry_run: %v", err)
	}
	if wr.Status != "dry_run" {
		t.Errorf("write status = %q, want dry_run", wr.Status)
	}

	// Delete it
	del, err := testClient.DeleteTemplate(ctx, cr.UniqueID)
	if err != nil {
		t.Fatalf("delete template: %v", err)
	}
	if del.Status != "deleted" {
		t.Errorf("delete status = %q, want deleted", del.Status)
	}
}

// --- Script CRUD integration tests ---

func TestListScriptDefs(t *testing.T) {
	r, err := testClient.ListScriptDefs(context.Background())
	if err != nil {
		t.Fatalf("list scripts: %v", err)
	}
	if len(r.Scripts) == 0 {
		t.Fatal("expected at least 1 script (seeded)")
	}
	found := false
	for _, s := range r.Scripts {
		if s.ID == "seeded_test_script" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("seeded_test_script not found in scripts: %+v", r.Scripts)
	}
}

func TestCreateAndGetScript(t *testing.T) {
	ctx := context.Background()
	content := "e2e_test_script:\n  alias: E2E Test Script\n  mode: single\n  sequence:\n    - delay:\n        seconds: 1\n"

	cr, err := testClient.CreateScriptDef(ctx, content)
	if err != nil {
		t.Fatalf("create script: %v", err)
	}
	if cr.Status != "created" {
		t.Errorf("status = %q, want created", cr.Status)
	}
	if cr.ID == "" {
		t.Fatal("id is empty after create")
	}

	// Get it back
	got, err := testClient.GetScriptDef(ctx, cr.ID)
	if err != nil {
		t.Fatalf("get script: %v", err)
	}
	if got.ID != cr.ID {
		t.Errorf("id = %q, want %q", got.ID, cr.ID)
	}
	if got.Content == "" {
		t.Error("content is empty")
	}

	// Update with dry-run
	updated := "e2e_test_script:\n  alias: E2E Updated Script\n  mode: single\n  sequence:\n    - delay:\n        seconds: 2\n"
	wr, err := testClient.WriteScriptDef(ctx, cr.ID, updated, true)
	if err != nil {
		t.Fatalf("write script dry_run: %v", err)
	}
	if wr.Status != "dry_run" {
		t.Errorf("write status = %q, want dry_run", wr.Status)
	}

	// Delete it
	del, err := testClient.DeleteScriptDef(ctx, cr.ID)
	if err != nil {
		t.Fatalf("delete script: %v", err)
	}
	if del.Status != "deleted" {
		t.Errorf("delete status = %q, want deleted", del.Status)
	}
}

// --- Automation CRUD integration tests ---

func TestListAutomationDefs(t *testing.T) {
	r, err := testClient.ListAutomationDefs(context.Background())
	if err != nil {
		t.Fatalf("list automations: %v", err)
	}
	if len(r.Automations) == 0 {
		t.Fatal("expected at least 1 automation (seeded)")
	}
	found := false
	for _, a := range r.Automations {
		if a.ID == "seeded_test_auto" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("seeded_test_auto not found in automations: %+v", r.Automations)
	}
}

func TestCreateAndGetAutomation(t *testing.T) {
	ctx := context.Background()
	content := "id: e2e_test_auto_created\nalias: E2E Test Auto\nmode: single\ntrigger:\n  - platform: time\n    at: \"12:00:00\"\naction:\n  - delay:\n      seconds: 1\n"

	cr, err := testClient.CreateAutomationDef(ctx, content)
	if err != nil {
		t.Fatalf("create automation: %v", err)
	}
	if cr.Status != "created" {
		t.Errorf("status = %q, want created", cr.Status)
	}
	if cr.ID == "" {
		t.Fatal("id is empty after create")
	}

	// Get it back
	got, err := testClient.GetAutomationDef(ctx, cr.ID)
	if err != nil {
		t.Fatalf("get automation: %v", err)
	}
	if got.ID != cr.ID {
		t.Errorf("id = %q, want %q", got.ID, cr.ID)
	}
	if got.Content == "" {
		t.Error("content is empty")
	}

	// Update with dry-run
	updated := "id: e2e_test_auto_created\nalias: E2E Updated Auto\nmode: single\ntrigger:\n  - platform: time\n    at: \"13:00:00\"\naction:\n  - delay:\n      seconds: 2\n"
	wr, err := testClient.WriteAutomationDef(ctx, cr.ID, updated, true)
	if err != nil {
		t.Fatalf("write automation dry_run: %v", err)
	}
	if wr.Status != "dry_run" {
		t.Errorf("write status = %q, want dry_run", wr.Status)
	}

	// Delete it
	del, err := testClient.DeleteAutomationDef(ctx, cr.ID)
	if err != nil {
		t.Fatalf("delete automation: %v", err)
	}
	if del.Status != "deleted" {
		t.Errorf("delete status = %q, want deleted", del.Status)
	}
}

// --- Companion Discovery Tests ---

// TestDiscovery_ExplicitURL verifies discovery with explicit COMPANION_URL in config.
func TestDiscovery_ExplicitURL(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		URL:          haURL,
		Token:        companionToken,
		CompanionURL: compURL,
	}

	url, err := companion.Discover(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("discovery with explicit URL: %v", err)
	}
	if url != compURL {
		t.Errorf("discovered URL = %q, want %q", url, compURL)
	}
}

// TestDiscovery_NoConfig_NoWS verifies discovery fails gracefully without config or WS.
func TestDiscovery_NoConfig_NoWS(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		URL:   haURL,
		Token: companionToken,
	}

	_, err := companion.Discover(ctx, cfg, nil)
	if err == nil {
		t.Error("expected error when no COMPANION_URL and no WS, got nil")
	}
}

// TestDiscovery_WrongURL verifies health check fails with wrong companion URL.
func TestDiscovery_WrongURL(t *testing.T) {
	ctx := context.Background()
	// Use a non-existent URL
	badClient := companion.New("http://127.0.0.1:1", companionToken)
	_, err := badClient.Health(ctx)
	if err == nil {
		t.Error("expected error for unreachable companion, got nil")
	}
}

// TestDiscovery_HealthStatus verifies the discovered companion responds to health check.
func TestDiscovery_HealthStatus(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{
		URL:          haURL,
		Token:        companionToken,
		CompanionURL: compURL,
	}

	url, err := companion.Discover(ctx, cfg, nil)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}

	cc := companion.New(url, companionToken)
	h, err := cc.Health(ctx)
	if err != nil {
		t.Fatalf("health after discovery: %v", err)
	}
	if h.Status != "ok" {
		t.Errorf("health status = %q, want ok", h.Status)
	}
	if h.Version == "" {
		t.Error("companion version is empty")
	}
}

// TestVersionCompat verifies the actual companion version is parseable.
func TestVersionCompat(t *testing.T) {
	ctx := context.Background()
	h, err := testClient.Health(ctx)
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if h.Version == "" {
		t.Skip("companion version is empty")
	}

	// Verify the companion version is semver-like
	parts := splitVersion(h.Version)
	if len(parts) < 2 {
		t.Errorf("companion version %q is not semver-like", h.Version)
	}
}

// TestVersionCompat_MajorDiff verifies version mismatch detection logic.
func TestVersionCompat_MajorDiff(t *testing.T) {
	tests := []struct {
		name   string
		v1, v2 string
		compat bool
	}{
		{"same", "1.0.0", "1.0.0", true},
		{"minor_diff", "1.2.0", "1.5.0", true},
		{"major_diff_1", "2.0.0", "1.0.0", true},
		{"major_diff_2", "3.0.0", "1.0.0", true},
		{"major_diff_3_incompatible", "4.0.0", "1.0.0", false},
		{"major_diff_5_incompatible", "6.0.0", "1.0.0", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m1 := parseMajorVersion(tt.v1)
			m2 := parseMajorVersion(tt.v2)
			diff := m1 - m2
			if diff < 0 {
				diff = -diff
			}
			isCompat := diff <= 2
			if isCompat != tt.compat {
				t.Errorf("v1=%q major=%d, v2=%q major=%d, diff=%d, compat=%v, want %v",
					tt.v1, m1, tt.v2, m2, diff, isCompat, tt.compat)
			}
		})
	}
}

// splitVersion splits a version string for basic validation.
func splitVersion(v string) []string {
	v = strings.TrimPrefix(v, "v")
	return strings.Split(v, ".")
}

// parseMajorVersion extracts the major version number.
func parseMajorVersion(v string) int {
	v = strings.TrimPrefix(v, "v")
	parts := strings.SplitN(v, ".", 2)
	if len(parts) == 0 {
		return -1
	}
	n := 0
	for _, c := range parts[0] {
		if c < '0' || c > '9' {
			return -1
		}
		n = n*10 + int(c-'0')
	}
	return n
}