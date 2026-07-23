//go:build companion

package companiontest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// ============================================================================
// Companion-tier write round trips (invariant H-12).
//
// `docs/testing.md` recorded that `script apply`'s backup and validation
// helpers "can be replaced with a stub without any test failing", and that
// `tpl create|delete` and `helper create|delete` were in the same position.
// The client-level tests in companion_test.go drive companion's own API and
// read back through companion, which proves companion self-consistent — it
// says nothing about the hactl CLI, and nothing about whether Home Assistant
// ever parsed what was written.
//
// Everything below goes through the hactl binary with `--confirm` and reads
// back from HA's own `/api/states` — HA had to load the file to have an
// entity at all — plus the stored document, compared whole. Each case asserts
// at least one field the CLI never echoes (an icon, a unit) as an independent
// witness that the entire document landed and not just the field the renderer
// happens to show.
// ============================================================================

// haStateOf reads one entity straight from HA's REST API. ok is false when HA
// has no such entity (404), which is how "the write did not materialize" and
// "the delete took effect" are both told apart from a transport failure.
func haStateOf(t *testing.T, entityID string) (state string, attrs map[string]any, ok bool) {
	t.Helper()
	raw, err := haapi.New(haURL, haToken).GetState(context.Background(), entityID)
	if err != nil {
		if strings.Contains(err.Error(), "404") || strings.Contains(strings.ToLower(err.Error()), "not found") {
			return "", nil, false
		}
		t.Fatalf("reading %s from HA: %v", entityID, err)
	}
	var st struct {
		EntityID   string         `json:"entity_id"`
		State      string         `json:"state"`
		Attributes map[string]any `json:"attributes"`
	}
	if err := json.Unmarshal(raw, &st); err != nil {
		t.Fatalf("parsing %s state: %v (raw: %s)", entityID, err, raw)
	}
	return st.State, st.Attributes, true
}

// waitForHAState polls until HA reports the entity (or stops reporting it).
// Config writes reach HA through a reload the CLI has already waited on, but
// entity registration is a further async hop inside HA.
func waitForHAState(t *testing.T, entityID string, want bool) (string, map[string]any) {
	t.Helper()
	var state string
	var attrs map[string]any
	for range 40 {
		var ok bool
		state, attrs, ok = haStateOf(t, entityID)
		if ok == want {
			return state, attrs
		}
		time.Sleep(250 * time.Millisecond)
	}
	if want {
		t.Fatalf("HA never reported %s", entityID)
	}
	t.Fatalf("HA still reports %s (state=%q)", entityID, state)
	return "", nil
}

// registryHas reports whether HA's entity registry still holds entityID.
// A delete that only removes the YAML leaves the registry entry behind, and
// HA then reports the entity forever as `unavailable` / `restored: true`.
func registryHas(t *testing.T, entityID string) bool {
	t.Helper()
	ws := haapi.NewWSClient(haURL, haToken)
	if err := ws.Connect(context.Background()); err != nil {
		t.Fatalf("connecting to HA: %v", err)
	}
	defer func() { _ = ws.Close() }()
	entries, err := ws.EntityRegistryList(context.Background())
	if err != nil {
		t.Fatalf("listing entity registry: %v", err)
	}
	for _, e := range entries {
		if e.EntityID == entityID {
			return true
		}
	}
	return false
}

// assertNoGhost asserts a delete left nothing behind: no registry entry and no
// state. Retries because the registry removal is a separate WS round trip the
// CLI fires after reporting the delete.
func assertNoGhost(t *testing.T, entityID string) {
	t.Helper()
	for range 40 {
		if !registryHas(t, entityID) {
			if _, _, ok := haStateOf(t, entityID); !ok {
				return
			}
		}
		time.Sleep(250 * time.Millisecond)
	}
	state, attrs, _ := haStateOf(t, entityID)
	t.Fatalf("delete left a ghost: %s is still registered (state=%q restored=%v)",
		entityID, state, attrs["restored"])
}

// attrString reads one attribute as a string, failing the test when it is
// missing — an absent witness attribute means the document was not written
// whole, which is exactly what H-12 exists to catch.
func attrString(t *testing.T, entityID string, attrs map[string]any, key string) string {
	t.Helper()
	v, ok := attrs[key]
	if !ok {
		t.Fatalf("%s: HA has no %q attribute; attributes: %v", entityID, key, attrs)
	}
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%s: attribute %q is %T (%v), want string", entityID, key, v, v)
	}
	return s
}

// writeTempYAML writes content to a temp file and returns its path.
func writeTempYAML(t *testing.T, name, content string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
	return p
}

// yamlDoc parses a YAML document into a comparable map.
func yamlDoc(t *testing.T, content string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := yaml.Unmarshal([]byte(content), &m); err != nil {
		t.Fatalf("parsing YAML: %v (content: %q)", err, content)
	}
	return m
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatalf("marshaling: %v", err)
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// script create | apply | delete
// ---------------------------------------------------------------------------

// TestE2EScriptWriteRoundTripCLI drives `script create`, `script apply` and
// `script delete` through the hactl binary and checks each step against HA
// rather than against hactl's own echo. `icon` is the independent witness:
// no step of the CLI ever prints it, and HA only knows it if the whole
// document reached the config file and was parsed.
func TestE2EScriptWriteRoundTripCLI(t *testing.T) {
	const (
		id       = "h12_script_roundtrip"
		entityID = "script." + id
	)
	ctx := context.Background()

	created := "" +
		id + ":\n" +
		"  alias: H12 Script Round Trip\n" +
		"  icon: mdi:test-tube\n" +
		"  mode: single\n" +
		"  description: created by the H-12 companion-tier gate\n" +
		"  sequence:\n" +
		"    - delay: \"00:00:01\"\n"
	createFile := writeTempYAML(t, "script-create.yaml", created)

	t.Cleanup(func() {
		_, _ = testClient.DeleteScriptDef(ctx, id)
	})

	// --- create: dry-run writes nothing ---
	if out, err := runHactlE2E(t, "script", "create", "-f", createFile); err != nil {
		t.Fatalf("script create (dry-run) failed (exit %v):\n%s", err, out)
	}
	if _, _, ok := haStateOf(t, entityID); ok {
		t.Fatal("dry-run create materialized a script in HA")
	}

	// --- create: confirmed write reaches HA whole ---
	out, err := runHactlE2E(t, "script", "create", "--confirm", "-f", createFile)
	if err != nil {
		t.Fatalf("script create --confirm failed (exit %v):\n%s", err, out)
	}
	// The companion reports whether HA reloaded, and hactl used to drop that
	// on the floor: a definition HA never read still printed "created script".
	if strings.Contains(out, "warning:") {
		t.Errorf("HA confirmed nothing after create:\n%s", out)
	}
	_, attrs := waitForHAState(t, entityID, true)
	if got := attrString(t, entityID, attrs, "friendly_name"); got != "H12 Script Round Trip" {
		t.Errorf("HA friendly_name = %q, want %q", got, "H12 Script Round Trip")
	}
	if got := attrString(t, entityID, attrs, "mode"); got != "single" {
		t.Errorf("HA mode = %q, want single", got)
	}
	// The witness: the CLI prints only `created script "<id>"`.
	if got := attrString(t, entityID, attrs, "icon"); got != "mdi:test-tube" {
		t.Errorf("HA icon = %q, want mdi:test-tube — the document did not land whole", got)
	}

	// The stored document is the document we handed over, key for key. The
	// companion returns a script keyed by its id, the same shape `script
	// apply -f` accepts, so the round trip is literal.
	stored, err := testClient.GetScriptDef(ctx, id)
	if err != nil {
		t.Fatalf("reading stored script: %v", err)
	}
	wantDoc := yamlDoc(t, created)
	if gotDoc := yamlDoc(t, stored.Content); !reflect.DeepEqual(gotDoc, wantDoc) {
		t.Fatalf("stored script is not the document written:\n stored: %s\n want:   %s",
			mustJSON(t, gotDoc), mustJSON(t, wantDoc))
	}

	// --- apply: dry-run changes nothing ---
	applied := "" +
		id + ":\n" +
		"  alias: H12 Script Applied\n" +
		"  icon: mdi:flask\n" +
		"  mode: restart\n" +
		"  sequence:\n" +
		"    - delay: \"00:00:02\"\n"
	applyFile := writeTempYAML(t, "script-apply.yaml", applied)

	if out, err := runHactlE2E(t, "script", "apply", id, "-f", applyFile); err != nil {
		t.Fatalf("script apply (dry-run) failed (exit %v):\n%s", err, out)
	}
	afterDry, err := testClient.GetScriptDef(ctx, id)
	if err != nil {
		t.Fatalf("reading script after dry-run apply: %v", err)
	}
	if !reflect.DeepEqual(yamlDoc(t, afterDry.Content), wantDoc) {
		t.Fatalf("dry-run apply rewrote the stored script:\n got: %s", mustJSON(t, yamlDoc(t, afterDry.Content)))
	}

	// --- apply: confirmed write is a FULL replacement ---
	if out, err := runHactlE2E(t, "script", "apply", id, "--confirm", "-f", applyFile); err != nil {
		t.Fatalf("script apply --confirm failed (exit %v):\n%s", err, out)
	}
	wantApplied := yamlDoc(t, applied)
	gotApplied, err := testClient.GetScriptDef(ctx, id)
	if err != nil {
		t.Fatalf("reading script after apply: %v", err)
	}
	if got := yamlDoc(t, gotApplied.Content); !reflect.DeepEqual(got, wantApplied) {
		t.Fatalf("apply did not replace the whole document (a dropped key must disappear):\n got:  %s\n want: %s",
			mustJSON(t, got), mustJSON(t, wantApplied))
	}
	// HA reloaded and sees the new document, witness included.
	for range 40 {
		_, attrs = waitForHAState(t, entityID, true)
		if attrs["mode"] == "restart" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if got := attrString(t, entityID, attrs, "friendly_name"); got != "H12 Script Applied" {
		t.Errorf("after apply HA friendly_name = %q, want %q", got, "H12 Script Applied")
	}
	if got := attrString(t, entityID, attrs, "mode"); got != "restart" {
		t.Errorf("after apply HA mode = %q, want restart", got)
	}
	if got := attrString(t, entityID, attrs, "icon"); got != "mdi:flask" {
		t.Errorf("after apply HA icon = %q, want mdi:flask", got)
	}

	// --- apply writes a backup, so a rollback has something to restore from ---
	backups, _ := filepath.Glob(filepath.Join(instanceDir, "backups", "*_script_"+id+".yaml"))
	if len(backups) == 0 {
		t.Errorf("apply --confirm wrote no backup under %s", filepath.Join(instanceDir, "backups"))
	} else {
		data, readErr := os.ReadFile(backups[0]) //nolint:gosec // path from our own glob
		if readErr != nil {
			t.Errorf("reading backup: %v", readErr)
		} else if !reflect.DeepEqual(yamlDoc(t, string(data)), wantDoc) {
			t.Errorf("backup does not hold the pre-apply document:\n got: %s", mustJSON(t, yamlDoc(t, string(data))))
		}
	}

	// --- delete: dry-run leaves it, confirm removes it from HA ---
	if out, err := runHactlE2E(t, "script", "delete", id); err != nil {
		t.Fatalf("script delete (dry-run) failed (exit %v):\n%s", err, out)
	}
	if _, _, ok := haStateOf(t, entityID); !ok {
		t.Fatal("dry-run delete removed the script from HA")
	}
	if out, err := runHactlE2E(t, "script", "delete", "--confirm", id); err != nil {
		t.Fatalf("script delete --confirm failed (exit %v):\n%s", err, out)
	}
	if _, err := testClient.GetScriptDef(ctx, id); err == nil {
		t.Error("delete --confirm left the script in the config file")
	}
	assertNoGhost(t, entityID)

	// The seeded neighbour in the same file is untouched — a delete must not
	// take the rest of scripts.yaml with it.
	if _, err := testClient.GetScriptDef(ctx, "seeded_test_script"); err != nil {
		t.Errorf("deleting %s disturbed its neighbour in scripts.yaml: %v", id, err)
	}
}

// ---------------------------------------------------------------------------
// tpl create | delete
// ---------------------------------------------------------------------------

// TestE2ETplWriteRoundTripCLI drives `tpl create`/`tpl delete` through the
// binary for both shapes the command accepts — a bare entity item and a full
// trigger-based block — and checks HA's own state each time. The neighbour
// assertions are the permanent gate for the v2026.7.3 regression, where
// creating a trigger-based entry corrupted the rest of template.yaml.
func TestE2ETplWriteRoundTripCLI(t *testing.T) {
	ctx := context.Background()

	const (
		stateID       = "h12_tpl_state"
		stateEntity   = "sensor.h12_tpl_state_sensor"
		triggerID     = "h12_tpl_trigger"
		triggerEntity = "sensor.h12_tpl_trigger_sensor"
	)

	t.Cleanup(func() {
		_, _ = testClient.DeleteTemplate(ctx, stateID)
		_, _ = testClient.DeleteTemplate(ctx, triggerID)
	})

	// --- bare entity item ---
	item := "" +
		"name: H12 Tpl State Sensor\n" +
		"unique_id: " + stateID + "\n" +
		"state: \"{{ 21 + 21 }}\"\n" +
		"unit_of_measurement: W\n" +
		"icon: mdi:calculator\n"
	itemFile := writeTempYAML(t, "tpl-item.yaml", item)

	if out, err := runHactlE2E(t, "tpl", "create", "-f", itemFile); err != nil {
		t.Fatalf("tpl create (dry-run) failed (exit %v):\n%s", err, out)
	}
	if _, err := testClient.GetTemplate(ctx, stateID); err == nil {
		t.Fatal("dry-run create wrote the template to template.yaml")
	}

	out, err := runHactlE2E(t, "tpl", "create", "--confirm", "-f", itemFile)
	if err != nil {
		t.Fatalf("tpl create --confirm failed (exit %v):\n%s", err, out)
	}
	// Same as scripts: without the reload confirmation this command reported
	// success for a template.yaml no `template:` key ever !include'd.
	if strings.Contains(out, "warning:") {
		t.Errorf("HA confirmed nothing after create:\n%s", out)
	}
	state, attrs := waitForHAState(t, stateEntity, true)
	if state != "42" {
		t.Errorf("HA state = %q, want 42 (the template was not evaluated)", state)
	}
	if got := attrString(t, stateEntity, attrs, "friendly_name"); got != "H12 Tpl State Sensor" {
		t.Errorf("HA friendly_name = %q, want %q", got, "H12 Tpl State Sensor")
	}
	// Two witnesses the CLI never prints.
	if got := attrString(t, stateEntity, attrs, "unit_of_measurement"); got != "W" {
		t.Errorf("HA unit_of_measurement = %q, want W", got)
	}
	if got := attrString(t, stateEntity, attrs, "icon"); got != "mdi:calculator" {
		t.Errorf("HA icon = %q, want mdi:calculator", got)
	}

	stored, storedErr := testClient.GetTemplate(ctx, stateID)
	if storedErr != nil {
		t.Fatalf("reading stored template: %v", storedErr)
	}
	if got, want := yamlDoc(t, stored.Content), yamlDoc(t, item); !reflect.DeepEqual(got, want) {
		t.Fatalf("stored template is not the document written:\n stored: %s\n want:   %s",
			mustJSON(t, got), mustJSON(t, want))
	}

	// --- full trigger-based block, alongside the entry just created ---
	block := "" +
		"triggers:\n" +
		"  - trigger: state\n" +
		"    entity_id: sun.sun\n" +
		"sensor:\n" +
		"  - name: H12 Tpl Trigger Sensor\n" +
		"    unique_id: " + triggerID + "\n" +
		"    state: \"{{ trigger.to_state.state if trigger is defined else 'idle' }}\"\n" +
		"    icon: mdi:flash\n"
	blockFile := writeTempYAML(t, "tpl-block.yaml", block)

	if out, err := runHactlE2E(t, "tpl", "create", "--confirm", "-f", blockFile); err != nil {
		t.Fatalf("tpl create --confirm (trigger block) failed (exit %v):\n%s", err, out)
	}
	if _, err := testClient.GetTemplate(ctx, triggerID); err != nil {
		t.Fatalf("trigger-based template was not stored: %v", err)
	}

	// The regression gate: the neighbours in template.yaml still parse and
	// still hold their own documents.
	afterBlock, err := testClient.GetTemplate(ctx, stateID)
	if err != nil {
		t.Fatalf("creating a trigger-based block corrupted the state-based neighbour: %v", err)
	}
	if got, want := yamlDoc(t, afterBlock.Content), yamlDoc(t, item); !reflect.DeepEqual(got, want) {
		t.Fatalf("creating a trigger-based block rewrote its neighbour:\n got:  %s\n want: %s",
			mustJSON(t, got), mustJSON(t, want))
	}
	if _, err := testClient.GetTemplate(ctx, "seeded_test_sensor"); err != nil {
		t.Errorf("the seeded template no longer resolves after two creates: %v", err)
	}
	// HA still loads the file: a corrupted template.yaml takes every template
	// entity down with it, so the first neighbour must still be alive.
	if _, _, ok := haStateOf(t, stateEntity); !ok {
		t.Errorf("%s vanished from HA after the trigger-based create — template.yaml no longer loads", stateEntity)
	}

	// --- delete: dry-run leaves it, confirm removes it ---
	if out, err := runHactlE2E(t, "tpl", "delete", triggerID); err != nil {
		t.Fatalf("tpl delete (dry-run) failed (exit %v):\n%s", err, out)
	}
	if _, err := testClient.GetTemplate(ctx, triggerID); err != nil {
		t.Fatalf("dry-run delete removed the template: %v", err)
	}
	if out, err := runHactlE2E(t, "tpl", "delete", "--confirm", triggerID); err != nil {
		t.Fatalf("tpl delete --confirm failed (exit %v):\n%s", err, out)
	}
	if _, err := testClient.GetTemplate(ctx, triggerID); err == nil {
		t.Error("delete --confirm left the template in template.yaml")
	}
	assertNoGhost(t, triggerEntity)
	// ...and again, the neighbour survives the delete.
	if _, err := testClient.GetTemplate(ctx, stateID); err != nil {
		t.Errorf("deleting the trigger-based block disturbed its neighbour: %v", err)
	}
	if _, _, ok := haStateOf(t, stateEntity); !ok {
		t.Errorf("%s vanished from HA after the delete — template.yaml no longer loads", stateEntity)
	}
}

// ---------------------------------------------------------------------------
// helper create | delete
// ---------------------------------------------------------------------------

// TestE2EHelperWriteRoundTripCLI drives `helper create`/`helper delete`
// through the binary. `icon` is again the witness: `helper create` prints the
// id, the domain and the entity_id, never the icon, so HA reporting it proves
// the whole mapping was written into input_boolean.yaml and parsed.
func TestE2EHelperWriteRoundTripCLI(t *testing.T) {
	ctx := context.Background()

	const (
		id       = "h12_helper_toggle"
		entityID = "input_boolean." + id
	)

	t.Cleanup(func() {
		_, _ = testClient.DeleteHelper(ctx, id)
	})

	content := "" +
		id + ":\n" +
		"  name: H12 Helper Toggle\n" +
		"  icon: mdi:toggle-switch\n" +
		"  initial: true\n"
	file := writeTempYAML(t, "helper.yaml", content)

	// --- dry-run writes nothing ---
	if out, err := runHactlE2E(t, "helper", "create", "input_boolean", "-f", file); err != nil {
		t.Fatalf("helper create (dry-run) failed (exit %v):\n%s", err, out)
	}
	if _, _, ok := haStateOf(t, entityID); ok {
		t.Fatal("dry-run create materialized a helper in HA")
	}

	// --- confirmed write reaches HA whole ---
	out, err := runHactlE2E(t, "helper", "create", "input_boolean", "--confirm", "-f", file)
	if err != nil {
		t.Fatalf("helper create --confirm failed (exit %v):\n%s", err, out)
	}
	if !strings.Contains(out, "entity_id: "+entityID) {
		t.Errorf("create did not confirm the live entity_id, got:\n%s", out)
	}
	_, attrs := waitForHAState(t, entityID, true)
	if got := attrString(t, entityID, attrs, "friendly_name"); got != "H12 Helper Toggle" {
		t.Errorf("HA friendly_name = %q, want %q", got, "H12 Helper Toggle")
	}
	if got := attrString(t, entityID, attrs, "icon"); got != "mdi:toggle-switch" {
		t.Errorf("HA icon = %q, want mdi:toggle-switch — the document did not land whole", got)
	}

	stored, err := testClient.GetHelper(ctx, id)
	if err != nil {
		t.Fatalf("reading stored helper: %v", err)
	}
	if got, want := yamlDoc(t, stored.Content), yamlDoc(t, content); !reflect.DeepEqual(got, want) {
		t.Fatalf("stored helper is not the document written:\n stored: %s\n want:   %s",
			mustJSON(t, got), mustJSON(t, want))
	}

	// --- delete: dry-run leaves it, confirm removes it from HA ---
	if out, err := runHactlE2E(t, "helper", "delete", id); err != nil {
		t.Fatalf("helper delete (dry-run) failed (exit %v):\n%s", err, out)
	}
	if _, _, ok := haStateOf(t, entityID); !ok {
		t.Fatal("dry-run delete removed the helper from HA")
	}
	if out, err := runHactlE2E(t, "helper", "delete", "--confirm", id); err != nil {
		t.Fatalf("helper delete --confirm failed (exit %v):\n%s", err, out)
	}
	if _, err := testClient.GetHelper(ctx, id); err == nil {
		t.Error("delete --confirm left the helper in input_boolean.yaml")
	}
	assertNoGhost(t, entityID)
}

// ---------------------------------------------------------------------------
// dry-run honesty at the companion tier (Wave 2c)
// ---------------------------------------------------------------------------

// TestE2EDryRunRejectsFabricatedTargetCLI is the companion half of the
// dry-run-honesty gate: a preview for a target that does not exist must fail
// exactly where the confirmed run would. The manual tells agents to stop at
// the first miss, so a confident "would delete X" plan at exit 0 turns a typo
// into a verified plan.
func TestE2EDryRunRejectsFabricatedTargetCLI(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want string // substring the error must name
	}{
		{"script delete", []string{"script", "delete", "no_such_script_h12"}, "no_such_script_h12"},
		{"script apply", []string{"script", "apply", "no_such_script_h12"}, "no_such_script_h12"},
		{"tpl delete", []string{"tpl", "delete", "no_such_template_h12"}, "no_such_template_h12"},
		{"helper delete", []string{"helper", "delete", "no_such_helper_h12"}, "no_such_helper_h12"},
		{"auto delete", []string{"auto", "delete", "no_such_automation_h12"}, "no_such_automation_h12"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := tc.args
			if tc.name == "script apply" {
				args = append(args, "-f", writeTempYAML(t, "apply.yaml", "sequence:\n  - delay: \"00:00:01\"\n"))
			}
			out, err := runHactlE2E(t, args...)
			if err == nil {
				t.Fatalf("dry-run planned a write against a target that does not exist:\n%s", out)
			}
			if !strings.Contains(out, tc.want) {
				t.Errorf("error does not name the missing target %q:\n%s", tc.want, out)
			}
			if strings.Contains(out, "use --confirm") {
				t.Errorf("dry-run offered --confirm for a target that does not exist:\n%s", out)
			}
		})
	}
}

// TestE2ECreateDryRunValidatesInputCLI covers the other half: a create preview
// reads its input file and refuses what the confirmed run would refuse. The
// live instance of this was found by hand — `helper create -f` needs a keyed
// mapping with exactly one top-level key, a bare `name:`/`icon:` mapping is a
// 400 from the companion, and the dry run previewed the invalid file happily.
func TestE2ECreateDryRunValidatesInputCLI(t *testing.T) {
	cases := []struct {
		name string
		args func(path string) []string
		body string
	}{
		{
			name: "helper create with an unkeyed mapping",
			body: "name: Not Keyed\nicon: mdi:alert\n",
			args: func(p string) []string { return []string{"helper", "create", "input_boolean", "-f", p} },
		},
		{
			name: "script create without a sequence",
			body: "h12_bad_script:\n  alias: No Sequence\n",
			args: func(p string) []string { return []string{"script", "create", "-f", p} },
		},
		{
			name: "script create with unparseable YAML",
			body: "h12_bad_script:\n  alias: [unclosed\n",
			args: func(p string) []string { return []string{"script", "create", "-f", p} },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := writeTempYAML(t, "create.yaml", tc.body)
			out, err := runHactlE2E(t, tc.args(p)...)
			if err == nil {
				t.Fatalf("dry-run previewed an input the confirmed run rejects:\n%s", out)
			}
			if strings.Contains(out, "use --confirm") {
				t.Errorf("dry-run offered --confirm for an invalid input:\n%s", out)
			}
		})
	}
}

// TestE2EConfirmedRunRejectsWhatDryRunRejectsCLI closes the loop: for each
// case above, `--confirm` must fail too. A dry run that refuses while the
// confirmed run succeeds would be just as dishonest in the other direction.
func TestE2EConfirmedRunRejectsWhatDryRunRejectsCLI(t *testing.T) {
	p := writeTempYAML(t, "create.yaml", "name: Not Keyed\nicon: mdi:alert\n")
	for _, args := range [][]string{
		{"script", "delete", "--confirm", "no_such_script_h12"},
		{"tpl", "delete", "--confirm", "no_such_template_h12"},
		{"helper", "delete", "--confirm", "no_such_helper_h12"},
		{"helper", "create", "input_boolean", "--confirm", "-f", p},
	} {
		t.Run(strings.Join(args[:2], " "), func(t *testing.T) {
			out, err := runHactlE2E(t, args...)
			if err == nil {
				t.Fatalf("--confirm succeeded where the dry run must fail:\n%s", out)
			}
		})
	}
}
