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
	// These tests exercise command mechanics, not the agent protocol: piped
	// output would otherwise trigger manual injection and the first-family
	// --confirm guard.
	cmd.Env = append(os.Environ(), "HACTL_MANUAL_MODE=off")
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

// TestE2ERefReplaceCLI drives the full Go↔companion↔HA path for `ref scan` and
// `ref replace`: seed a literal entity reference into automations.yaml (in HA's
// default !include graph) via `auto create`, then scan for it, dry-run a rename
// (must not change the file), apply it, and confirm the literal moved. This is
// the class of boundary a mocked companion can't prove.
func TestE2ERefReplaceCLI(t *testing.T) {
	const (
		stale = "binary_sensor.ref_e2e_stale"
		fresh = "binary_sensor.ref_e2e_fresh"
	)
	content := fmt.Sprintf(`id: ref_e2e_probe
alias: Ref E2E Probe
mode: single
trigger:
  - platform: state
    entity_id: %s
condition: []
action:
  - delay: "00:00:01"
`, stale)
	f, err := os.CreateTemp(t.TempDir(), "ref-e2e-*.yaml")
	if err != nil {
		t.Fatalf("creating temp YAML: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writing temp YAML: %v", err)
	}
	f.Close()

	if out, execErr := runHactlE2E(t, "auto", "create", "--confirm", "-f", f.Name()); execErr != nil {
		t.Fatalf("hactl auto create failed (exit: %v):\n%s", execErr, out)
	}

	// Scan finds the literal in the config file it lives in.
	out, execErr := runHactlE2E(t, "ref", "scan", stale)
	if execErr != nil {
		t.Fatalf("hactl ref scan failed (exit: %v):\n%s", execErr, out)
	}
	// Scan output is source/location/path (the matched value is the target
	// itself, so it is not repeated as a column — same contract as `dash grep`).
	for _, want := range []string{"config", "automations.yaml", "trigger[0].entity_id"} {
		if !strings.Contains(out, want) {
			t.Fatalf("ref scan output missing %q:\n%s", want, out)
		}
	}

	// Dry-run reports the rename but must not change anything on disk.
	if out, execErr := runHactlE2E(t, "ref", "replace", stale, fresh); execErr != nil {
		t.Fatalf("hactl ref replace (dry-run) failed (exit: %v):\n%s", execErr, out)
	} else if !strings.Contains(out, "dry-run") {
		t.Fatalf("expected dry-run notice, got:\n%s", out)
	}
	if out, execErr := runHactlE2E(t, "ref", "scan", stale); execErr != nil {
		t.Fatalf("hactl ref scan after dry-run failed (exit: %v):\n%s", execErr, out)
	} else if !strings.Contains(out, "automations.yaml") {
		t.Fatalf("dry-run must not modify the file; reference no longer found:\n%s", out)
	}

	// Apply the rename for real.
	if out, execErr := runHactlE2E(t, "ref", "replace", stale, fresh, "--confirm"); execErr != nil {
		t.Fatalf("hactl ref replace --confirm failed (exit: %v):\n%s", execErr, out)
	} else if !strings.Contains(out, "renamed") {
		t.Fatalf("expected 'renamed' in confirm output, got:\n%s", out)
	}

	// The literal moved: the old id is gone, the new id is present.
	if out, execErr := runHactlE2E(t, "ref", "scan", stale); execErr != nil {
		t.Fatalf("hactl ref scan (old) failed (exit: %v):\n%s", execErr, out)
	} else if !strings.Contains(out, "not referenced") {
		t.Fatalf("expected %s to be gone after confirm, got:\n%s", stale, out)
	}
	if out, execErr := runHactlE2E(t, "ref", "scan", fresh); execErr != nil {
		t.Fatalf("hactl ref scan (new) failed (exit: %v):\n%s", execErr, out)
	} else if !strings.Contains(out, "automations.yaml") {
		t.Fatalf("expected %s in automations.yaml after confirm, got:\n%s", fresh, out)
	}
}

// TestE2ERefValidateCLI drives the full Go↔companion↔HA path for `ref validate`:
// seed an automation referencing a non-existent entity (dangling) plus a real,
// state-only entity (sun.sun, which exists in HA's live states but has no entity
// registry entry), then assert validate reports the ghost and not the live one.
// This proves both the /v1/ref/entities primitive over real config and that the
// live set is the registry∪states union (registry alone would flag sun.sun) —
// a boundary a mocked companion can't cover.
func TestE2ERefValidateCLI(t *testing.T) {
	const ghost = "binary_sensor.ref_validate_ghost"
	content := fmt.Sprintf(`id: ref_validate_probe
alias: Ref Validate Probe
mode: single
trigger:
  - platform: state
    entity_id: %s
condition:
  - condition: state
    entity_id: sun.sun
    state: above_horizon
action:
  - delay: "00:00:01"
`, ghost)
	f, err := os.CreateTemp(t.TempDir(), "ref-validate-e2e-*.yaml")
	if err != nil {
		t.Fatalf("creating temp YAML: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writing temp YAML: %v", err)
	}
	f.Close()

	if out, execErr := runHactlE2E(t, "auto", "create", "--confirm", "-f", f.Name()); execErr != nil {
		t.Fatalf("hactl auto create failed (exit: %v):\n%s", execErr, out)
	}

	out, execErr := runHactlE2E(t, "ref", "validate")
	if execErr != nil {
		t.Fatalf("hactl ref validate failed (exit: %v):\n%s", execErr, out)
	}
	if !strings.Contains(out, ghost) {
		t.Fatalf("expected dangling %s in validate output:\n%s", ghost, out)
	}
	// sun.sun is a live state-only entity; the registry∪states union must not
	// flag it. If this fails, the states half of the live set regressed.
	if strings.Contains(out, "sun.sun") {
		t.Fatalf("live state-only entity sun.sun must not be reported dangling:\n%s", out)
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

// TestE2EAutoCreateRefusesInvalidCLI verifies that `hactl auto create` runs the
// same validate_config check `auto apply` runs, and refuses a candidate HA
// rejects (broken Jinja in a condition template) in both dry-run and --confirm
// mode — nothing is created. Regression coverage for issue #68, where create
// skipped validation and wrote configs that loaded as `unavailable`.
func TestE2EAutoCreateRefusesInvalidCLI(t *testing.T) {
	// Unparseable Jinja in a condition template: HA's validate_config rejects
	// the conditions section.
	content := `id: e2e_broken_test
alias: E2E Broken Test
triggers: [{trigger: state, entity_id: sensor.does_not_exist}]
conditions: [{condition: template, value_template: "{{ broken"}]
actions: [{action: logbook.log, data: {name: x, message: y}}]
`
	f, err := os.CreateTemp(t.TempDir(), "auto-broken-*.yaml")
	if err != nil {
		t.Fatalf("creating temp YAML: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writing temp YAML: %v", err)
	}
	f.Close()

	for _, mode := range []struct {
		name string
		args []string
	}{
		{"dry-run", []string{"auto", "create", "-f", f.Name()}},
		{"confirm", []string{"auto", "create", "--confirm", "-f", f.Name()}},
	} {
		t.Run(mode.name, func(t *testing.T) {
			out, execErr := runHactlE2E(t, mode.args...)
			if execErr == nil {
				t.Fatalf("expected non-zero exit for invalid candidate, got success:\n%s", out)
			}
			if !strings.Contains(out, "HA rejected") {
				t.Errorf("expected HA rejection in output, got:\n%s", out)
			}
			if strings.Contains(out, "created automation") {
				t.Errorf("invalid candidate must not report 'created automation', got:\n%s", out)
			}
			if strings.Contains(out, "would create automation") {
				t.Errorf("invalid candidate must not report 'would create automation', got:\n%s", out)
			}
		})
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

// TestE2EAutoCatByEntityObjectIDCLI verifies that `hactl auto cat <id>`
// resolves an entity object id (as printed by `auto ls`) to the config id
// via /api/states before asking the companion, whose /v1/config/automation
// route keys on the config id — issue #70's exact repro, where the two
// differ because HA derives entity_id from the alias, not the config id.
func TestE2EAutoCatByEntityObjectIDCLI(t *testing.T) {
	ctx := context.Background()

	const autoID = "e2e_cat_mismatch_target"
	const alias = "E2E Cat Mismatch Alias"
	const entityID = "automation.e2e_cat_mismatch_alias"
	content := `id: ` + autoID + `
alias: ` + alias + `
mode: single
trigger:
  - platform: time
    at: "09:00:00"
action:
  - delay: "00:00:01"
`
	cr, err := testClient.CreateAutomationDef(ctx, content)
	if err != nil {
		t.Fatalf("seeding automation for cat-by-entity-id test: %v", err)
	}
	if cr.EntityID != entityID {
		t.Fatalf("expected entity_id %q from create, got %q (test fixture assumption stale)", entityID, cr.EntityID)
	}

	entityObjectID := strings.TrimPrefix(entityID, "automation.")
	out, execErr := runHactlE2E(t, "auto", "cat", entityObjectID)
	if execErr != nil {
		t.Fatalf("hactl auto cat <entity object id> failed (exit: %v):\n%s", execErr, out)
	}
	if !strings.Contains(out, "alias: "+alias) {
		t.Errorf("expected automation YAML with alias %q, got:\n%s", alias, out)
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

	// Override instanceDir just for this invocation. Manual delivery off:
	// this must reach the companion error path, not the --confirm guard
	// (and the injected how-to happens to contain "companion", which would
	// let the assertion below pass for the wrong reason).
	fullArgs := []string{"--dir", badDir, "auto", "create", "--confirm", "-f", f.Name()}
	cmd := exec.Command(hactlBin, fullArgs...) //nolint:gosec // binary built from source
	cmd.Env = append(os.Environ(), "HACTL_MANUAL_MODE=off")
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
