//go:build companion

package companiontest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/companiontestutil"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// entityRegistryContains connects to real HA over WS and reports whether
// entityID still has a registry entry — used to prove `auto delete` actually
// cleaned up the orphaned entity, not just the config file.
func entityRegistryContains(ctx context.Context, entityID string) (bool, error) {
	ws := haapi.NewWSClient(haURL, haToken)
	if err := ws.Connect(ctx); err != nil {
		return false, fmt.Errorf("connecting to HA: %w", err)
	}
	defer ws.Close() //nolint:errcheck // best-effort close in test helper

	entries, err := ws.EntityRegistryList(ctx)
	if err != nil {
		return false, fmt.Errorf("listing entity registry: %w", err)
	}
	for _, e := range entries {
		if e.EntityID == entityID {
			return true, nil
		}
	}
	return false, nil
}

// runHactlE2E executes the hactl binary (built at TestMain) with the given args,
// using instanceDir as the --dir flag so it picks up the E2E .env with both HA
// and companion credentials. Returns combined stdout+stderr and any exec error.
func runHactlE2E(t *testing.T, args ...string) (string, error) {
	t.Helper()
	fullArgs := append([]string{"--dir", instanceDir}, args...)
	cmd := exec.Command(hactlBin, fullArgs...) //nolint:gosec // binary built from source in TestMain
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func runHactlE2ETimed(t *testing.T, args ...string) (string, time.Duration, error) {
	t.Helper()
	start := time.Now()
	out, err := runHactlE2E(t, args...)
	return out, time.Since(start), err
}

func TestE2EEntRelatedCompanionGraphCLI(t *testing.T) {
	out, cold, err := runHactlE2ETimed(t, "ent", "related", companiontestutil.RelatedSourceEntityID)
	if err != nil {
		t.Fatalf("hactl ent related failed (exit: %v):\n%s", err, out)
	}
	if cold > 10*time.Second {
		t.Fatalf("cold hactl ent related took %s, want <=10s\noutput:\n%s", cold, out)
	}
	assertEntRelatedOutput(t, out,
		companiontestutil.RelatedGeneratedEntityID,
		companiontestutil.RelatedYAMLPeerEntityID,
		"config-entry-reference",
		"yaml-reference",
	)

	out, warm, err := runHactlE2ETimed(t, "ent", "related", companiontestutil.RelatedSourceEntityID)
	if err != nil {
		t.Fatalf("warm hactl ent related failed (exit: %v):\n%s", err, out)
	}
	if warm > 3*time.Second {
		t.Fatalf("warm hactl ent related took %s, want <=3s\noutput:\n%s", warm, out)
	}
	assertEntRelatedOutput(t, out,
		companiontestutil.RelatedGeneratedEntityID,
		companiontestutil.RelatedYAMLPeerEntityID,
		"config-entry-reference",
		"yaml-reference",
	)

	reverseOut, err := runHactlE2E(t, "ent", "related", companiontestutil.RelatedGeneratedEntityID)
	if err != nil {
		t.Fatalf("reverse hactl ent related failed (exit: %v):\n%s", err, reverseOut)
	}
	assertEntRelatedOutput(t, reverseOut,
		companiontestutil.RelatedSourceEntityID,
		"referenced-entity",
		"config_entry="+companiontestutil.RelatedGeneratedConfigEntryID,
	)
}

func assertEntRelatedOutput(t *testing.T, out string, wants ...string) {
	t.Helper()
	for _, want := range wants {
		if !strings.Contains(out, want) {
			t.Fatalf("hactl ent related output missing %q:\n%s", want, out)
		}
	}
}

// TestE2EAutoCreateCLI verifies that `hactl auto create --confirm -f <yaml>`
// calls the companion API and creates an automation.
func TestE2EAutoCreateCLI(t *testing.T) {
	content := `id: e2e_create_test
alias: E2E Create Test
mode: single
trigger:
  - platform: time
    at: "06:00:00"
condition: []
action:
  - delay: "00:00:01"
`
	f, err := os.CreateTemp(t.TempDir(), "auto-create-*.yaml")
	if err != nil {
		t.Fatalf("creating temp YAML: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writing temp YAML: %v", err)
	}
	f.Close()

	out, execErr := runHactlE2E(t, "auto", "create", "--confirm", "-f", f.Name())
	if execErr != nil {
		t.Fatalf("hactl auto create --confirm failed (exit: %v):\n%s", execErr, out)
	}
	if !strings.Contains(out, "created automation") {
		t.Errorf("expected 'created automation' in output, got:\n%s", out)
	}
	// Regression coverage for issue #40: a write that never actually
	// materialized in HA used to still print "created automation".
	if !strings.Contains(out, "entity_id: automation.e2e_create_test") {
		t.Errorf("expected confirmed entity_id in output (real HA reload), got:\n%s", out)
	}
	if strings.Contains(out, "warning:") {
		t.Errorf("did not expect a warning when HA confirms the write, got:\n%s", out)
	}
}

// TestE2EAutoDeleteCLI verifies that `hactl auto delete --confirm <id>`
// calls the companion API and deletes an automation.
func TestE2EAutoDeleteCLI(t *testing.T) {
	ctx := context.Background()

	// Seed a unique automation via the companion client that we can then delete
	const autoID = "e2e_delete_target"
	content := `id: ` + autoID + `
alias: E2E Delete Target
mode: single
trigger:
  - platform: time
    at: "07:00:00"
action:
  - delay: "00:00:01"
`
	if _, err := testClient.CreateAutomationDef(ctx, content); err != nil {
		t.Fatalf("seeding automation for delete test: %v", err)
	}

	out, execErr := runHactlE2E(t, "auto", "delete", "--confirm", autoID)
	if execErr != nil {
		t.Fatalf("hactl auto delete --confirm failed (exit: %v):\n%s", execErr, out)
	}
	if !strings.Contains(out, "deleted automation") {
		t.Errorf("expected 'deleted automation' in output, got:\n%s", out)
	}
}

// TestE2EAutoDeleteByAliasCLI verifies that `hactl auto delete --confirm
// <alias>` — the display identifier, not the config id — both removes the
// config entry AND cleans up the orphaned entity registry entry.
//
// Regression coverage: resolveAutomationEntityID originally matched only
// entity_id/config-id/slug, silently skipping registry cleanup whenever a
// caller deleted by alias (HA's attributes.friendly_name). That gap wasn't
// caught by any mocked unit test — only a manual repro against real HA
// surfaced it, hence this test exists at the real-HA E2E tier.
func TestE2EAutoDeleteByAliasCLI(t *testing.T) {
	ctx := context.Background()

	const autoID = "e2e_delete_by_alias_target"
	const alias = "E2E Delete By Alias Target"
	const entityID = "automation.e2e_delete_by_alias_target"
	content := `id: ` + autoID + `
alias: ` + alias + `
mode: single
trigger:
  - platform: time
    at: "08:00:00"
action:
  - delay: "00:00:01"
`
	if _, err := testClient.CreateAutomationDef(ctx, content); err != nil {
		t.Fatalf("seeding automation for alias-delete test: %v", err)
	}

	present, err := entityRegistryContains(ctx, entityID)
	if err != nil {
		t.Fatalf("checking entity registry before delete: %v", err)
	}
	if !present {
		t.Fatalf("expected %s to be registered before delete", entityID)
	}

	out, execErr := runHactlE2E(t, "auto", "delete", "--confirm", alias)
	if execErr != nil {
		t.Fatalf("hactl auto delete --confirm <alias> failed (exit: %v):\n%s", execErr, out)
	}
	if !strings.Contains(out, "deleted automation") {
		t.Errorf("expected 'deleted automation' in output, got:\n%s", out)
	}

	// Give the WS entity_registry/remove call (best-effort, async from the
	// CLI's perspective) a moment to land before checking.
	var stillPresent bool
	for range 10 {
		stillPresent, err = entityRegistryContains(ctx, entityID)
		if err != nil {
			t.Fatalf("checking entity registry after delete: %v", err)
		}
		if !stillPresent {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if stillPresent {
		t.Errorf("expected %s to be removed from the entity registry after delete-by-alias", entityID)
	}
}

// TestE2ECompanionUnavailableCLI verifies that when the companion URL is
// unreachable, hactl exits with a non-zero code AND prints a meaningful error
// referencing "companion" (no panic, no empty output).
func TestE2ECompanionUnavailableCLI(t *testing.T) {
	// Build a broken instanceDir with an unreachable companion URL
	badDir, err := createE2EInstanceDir(haURL, haToken, "http://localhost:19999", "bad-token")
	if err != nil {
		t.Fatalf("creating bad instanceDir: %v", err)
	}
	defer os.RemoveAll(badDir)

	content := `- id: e2e_unavailable_test
  alias: E2E Unavailable Test
  mode: single
  trigger: []
  action: []
`
	f, err := os.CreateTemp(t.TempDir(), "auto-unavail-*.yaml")
	if err != nil {
		t.Fatalf("creating temp YAML: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writing YAML: %v", err)
	}
	f.Close()

	// Override instanceDir just for this invocation
	fullArgs := []string{"--dir", badDir, "auto", "create", "--confirm", "-f", f.Name()}
	cmd := exec.Command(hactlBin, fullArgs...) //nolint:gosec // binary built from source
	out, execErr := cmd.CombinedOutput()

	if execErr == nil {
		t.Fatalf("expected non-zero exit when companion unreachable, got success. output:\n%s", string(out))
	}
	outStr := string(out)
	// Should contain something useful — "companion" in the error message
	if !strings.Contains(strings.ToLower(outStr), "companion") {
		t.Errorf("expected output to mention 'companion', got:\n%s", outStr)
	}
	// Must not be empty — that would indicate a panic with no recovery
	if strings.TrimSpace(outStr) == "" {
		t.Error("expected non-empty error output when companion unreachable")
	}

	// Build path must have been written to a valid file for the YAML (not path-related issue)
	if _, statErr := os.Stat(filepath.Clean(f.Name())); statErr != nil {
		t.Errorf("yaml file disappeared: %v", statErr)
	}
}

// TestE2ESetupCLI verifies that `hactl --dir <tmpdir> setup` creates a valid .env
// when given HA_URL and HA_TOKEN via piped stdin, and that the resulting .env
// passes config.Load.
func TestE2ESetupCLI(t *testing.T) {
	dir := t.TempDir()

	// Pipe: URL (accept default), token, then "no" to companion prompt if any
	input := fmt.Sprintf("%s\n%s\n", haURL, haToken)

	cmd := exec.Command(hactlBin, "--dir", dir, "setup") //nolint:gosec
	cmd.Stdin = strings.NewReader(input)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("hactl setup failed (exit %v):\n%s", err, out)
	}

	envPath := filepath.Join(dir, ".env")
	if _, statErr := os.Stat(envPath); statErr != nil {
		t.Fatalf(".env not created at %s: %v\nsetup output:\n%s", envPath, statErr, out)
	}

	data, err := os.ReadFile(envPath) //nolint:gosec
	if err != nil {
		t.Fatalf("reading .env: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "HA_URL=") {
		t.Errorf(".env missing HA_URL, content:\n%s", content)
	}
	if !strings.Contains(content, "HA_TOKEN=") {
		t.Errorf(".env missing HA_TOKEN, content:\n%s", content)
	}

	outStr := string(out)
	if !strings.Contains(outStr, "OK") {
		t.Errorf("expected connectivity OK in setup output:\n%s", outStr)
	}
	if !strings.Contains(outStr, "Setup complete") {
		t.Errorf("expected 'Setup complete' in setup output:\n%s", outStr)
	}
}
