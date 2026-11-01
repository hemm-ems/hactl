//go:build companion

package companiontest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// ============================================================================
// Permission policy, phase 2 stage 2b: writes are enforced — the commands that
// need the companion (`ent rename`, `ref replace`, `auto apply`, `script
// apply`) and the confirm guard, which only the real binary runs. Written from
// the contract only: docs/manual.md "Enforcement (writes)" and its per-command
// table, D-70, D-73, D-77, D-78, D-80, R-3, R-11. The HA-only half is
// internal/integration/policy_enforce_test.go.
//
// Each refusal is proven on the binary's own streams and exit code — the
// report on stdout, one summary line on stderr, exit 1 — and then by reading
// Home Assistant and the config files back outside hactl (H-12).
// ============================================================================

const pgE2EProfile = "pg_agent"

// policyE2EDir is an instance directory beside the tier's own, with the same
// HA and companion credentials, holding policy as policy.yaml.
func policyE2EDir(t *testing.T, policy string) string {
	t.Helper()
	dir, err := createE2EInstanceDir(haURL, haToken, compURL, companionToken)
	if err != nil {
		t.Fatalf("creating policy instance dir: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	writePolicyE2E(t, dir, policy)
	return dir
}

func writePolicyE2E(t *testing.T, dir, policy string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(policy), 0o600); err != nil {
		t.Fatal(err)
	}
}

// pgE2EPolicy is a one-profile policy file whose profile body is indented by
// the caller.
func pgE2EPolicy(body string) string {
	return fmt.Sprintf("version: 1\nactive: %s\nprofiles:\n  %s:\n%s", pgE2EProfile, pgE2EProfile, body)
}

func policyLineOfE2E(t *testing.T, policy, needle string) int {
	t.Helper()
	for i, l := range strings.Split(policy, "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	t.Fatalf("policy text holds no line with %q:\n%s", needle, policy)
	return 0
}

// runPolicyE2E runs the hactl binary against dir and returns both streams and
// the exit code. env is appended to the process environment; without any,
// HACTL_MANUAL_MODE=off keeps the confirm guard out of command mechanics,
// as runHactlE2E does.
func runPolicyE2E(t *testing.T, dir string, env []string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	if env == nil {
		env = []string{"HACTL_MANUAL_MODE=off"}
	}
	fullArgs := append([]string{"--dir", dir}, args...)
	cmd := exec.CommandContext(t.Context(), hactlBin, fullArgs...) //nolint:gosec // binary built from source in TestMain
	cmd.Env = append(os.Environ(), env...)
	var out, errOut strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	assertNoDegenerateE2EOutput(t, fullArgs, out.String()+errOut.String())
	var ee *exec.ExitError
	switch {
	case err == nil:
		return out.String(), errOut.String(), 0
	case errors.As(err, &ee):
		return out.String(), errOut.String(), ee.ExitCode()
	default:
		t.Fatalf("running hactl %v: %v", args, err)
		return "", "", -1
	}
}

// e2eDenied is one entry of details.policy.denied (D-70).
type e2eDenied struct {
	Resource string `json:"resource"`
	Verb     string `json:"verb"`
	Rule     string `json:"rule"`
	Line     int    `json:"line"`
	Partial  bool   `json:"partial"`
	KindWide bool   `json:"kind_wide"`
}

func (d e2eDenied) textLine() string {
	s := "denied: " + d.Verb + " " + d.Resource + " — " + d.Rule
	if d.Line > 0 {
		s += fmt.Sprintf(" (line %d)", d.Line)
	}
	return s
}

type e2eRefusal struct {
	DryRun  *bool  `json:"dry_run"`
	OK      *bool  `json:"ok"`
	Error   string `json:"error"`
	Action  string `json:"action"`
	Hint    string `json:"hint"`
	Details struct {
		Policy *struct {
			Denied     []e2eDenied     `json:"denied"`
			Checked    int             `json:"checked"`
			Profile    string          `json:"profile"`
			SelectedBy string          `json:"selected_by"`
			Level      string          `json:"level"`
			Written    json.RawMessage `json:"written"`
		} `json:"policy"`
	} `json:"details"`
}

func decodeE2ERefusal(t *testing.T, what, out string, wantDryRun bool) e2eRefusal {
	t.Helper()
	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &keys); err != nil {
		t.Fatalf("%s: --json refusal is not one JSON object: %v\n%s", what, err, out)
	}
	got := make([]string, 0, len(keys))
	for k := range keys {
		got = append(got, k)
	}
	slices.Sort(got)
	if want := []string{"action", "details", "dry_run", "error", "hint", "ok"}; !slices.Equal(got, want) {
		t.Errorf("%s: refusal envelope keys = %v, want exactly %v (R-13)", what, got, want)
	}
	var r e2eRefusal
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%s: refusal did not decode: %v\n%s", what, err, out)
	}
	if r.DryRun == nil || *r.DryRun != wantDryRun || r.OK == nil || *r.OK || r.Error != "policy_denied" {
		t.Errorf("%s: dry_run=%v ok=%v error=%q, want dry_run=%v ok=false error=policy_denied", what, r.DryRun, r.OK, r.Error, wantDryRun)
	}
	if strings.Contains(r.Hint, "--confirm") {
		t.Errorf("%s: hint suggests --confirm on a policy refusal: %q", what, r.Hint)
	}
	p := r.Details.Policy
	if p == nil {
		t.Fatalf("%s: no details.policy:\n%s", what, out)
	}
	if p.Level != "A" || p.Profile == "" || p.SelectedBy == "" || len(p.Denied) == 0 || p.Checked < len(p.Denied) {
		t.Errorf("%s: details.policy = level %q profile %q selected_by %q denied %d checked %d",
			what, p.Level, p.Profile, p.SelectedBy, len(p.Denied), p.Checked)
	}
	if strings.TrimSpace(string(p.Written)) != "[]" {
		t.Errorf("%s: written = %s, want [] — a refusal of the plan has written nothing", what, p.Written)
	}
	return r
}

// assertE2ERefused: the text preview exits 1 with the report on stdout and the
// summary line on stderr; the --json preview and the --json --confirm run are
// both the refusal envelope and deny the same list (preview parity, D-80;
// HACTL_MANUAL_MODE=off stands in for a present witness). Every wanted denial
// is in the list.
func assertE2ERefused(t *testing.T, dir string, args []string, want ...e2eDenied) {
	t.Helper()
	stdout, stderr, code := runPolicyE2E(t, dir, nil, args...)
	if code != 1 {
		t.Fatalf("hactl %v: exit %d, want 1 — a denied write is refused in its dry run\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
	}
	if !strings.Contains(stdout, "policy_denied: would ") || !strings.Contains(stdout, "nothing was written") {
		t.Errorf("hactl %v: no refusal report on stdout:\n%s", args, stdout)
	}
	if strings.Contains(stdout, "dry-run: would") {
		t.Errorf("hactl %v: a refused preview still printed its plan:\n%s", args, stdout)
	}
	for _, w := range want {
		if !strings.Contains(stdout, w.textLine()) {
			t.Errorf("hactl %v: report misses %q:\n%s", args, w.textLine(), stdout)
		}
	}

	jsonArgs := append(slices.Clone(args), "--json")
	jout, _, jcode := runPolicyE2E(t, dir, nil, jsonArgs...)
	if jcode != 1 {
		t.Fatalf("hactl %v: exit %d, want 1\n%s", jsonArgs, jcode, jout)
	}
	preview := decodeE2ERefusal(t, fmt.Sprintf("hactl %v", jsonArgs), jout, true)
	for _, w := range want {
		if !slices.Contains(preview.Details.Policy.Denied, w) {
			t.Errorf("hactl %v: denied = %+v, want it to hold %+v", jsonArgs, preview.Details.Policy.Denied, w)
		}
	}
	if len(preview.Details.Policy.Denied) > 0 {
		first := preview.Details.Policy.Denied[0]
		summary := "policy_denied: " + strings.TrimPrefix(first.textLine(), "denied: ")
		found := slices.ContainsFunc(strings.Split(stderr, "\n"), func(l string) bool {
			if first.Line > 0 {
				return l == summary
			}
			return strings.HasPrefix(l, summary)
		})
		if !found {
			t.Errorf("hactl %v: stderr has no summary line %q (D-70):\n%s", args, summary, stderr)
		}
	}

	confirmArgs := append(slices.Clone(args), "--confirm", "--json")
	cout, _, ccode := runPolicyE2E(t, dir, nil, confirmArgs...)
	if ccode != 1 {
		t.Fatalf("hactl %v: exit %d, want 1 — --confirm refuses what the preview refused\n%s", confirmArgs, ccode, cout)
	}
	confirmed := decodeE2ERefusal(t, fmt.Sprintf("hactl %v", confirmArgs), cout, false)
	if !slices.Equal(preview.Details.Policy.Denied, confirmed.Details.Policy.Denied) ||
		preview.Details.Policy.Checked != confirmed.Details.Policy.Checked {
		t.Errorf("preview parity broken for %v:\n preview %+v (checked %d)\n confirm %+v (checked %d)", args,
			preview.Details.Policy.Denied, preview.Details.Policy.Checked,
			confirmed.Details.Policy.Denied, confirmed.Details.Policy.Checked)
	}
}

// assertE2EAllowed runs an allowed write: the preview passes with the policy
// line, the confirmed run succeeds.
func assertE2EAllowed(t *testing.T, dir string, args ...string) string {
	t.Helper()
	stdout, stderr, code := runPolicyE2E(t, dir, nil, args...)
	if code != 0 {
		t.Fatalf("hactl %v: allowed preview exit %d\nstdout:\n%s\nstderr:\n%s", args, code, stdout, stderr)
	}
	if !strings.Contains(stdout, "checked, allowed (profile "+pgE2EProfile+")") {
		t.Errorf("hactl %v: allowed preview lacks `policy: <n> checked, allowed (profile %s)`:\n%s", args, pgE2EProfile, stdout)
	}
	cout, cerr, ccode := runPolicyE2E(t, dir, nil, append(slices.Clone(args), "--confirm")...)
	if ccode != 0 {
		t.Fatalf("hactl %v --confirm: allowed write exit %d\nstdout:\n%s\nstderr:\n%s", args, ccode, cout, cerr)
	}
	return cout
}

// openE2EPolicy denies nothing these tests touch: a profile is in effect, so
// the checks run, and allow what they check.
var openE2EPolicy = pgE2EPolicy("    deny:\n      \"entity:lock.*\": [write]\n")

// ---------------------------------------------------------------------------
// E-5 + E-7: ent rename needs both names and every location it rewrites.
// ---------------------------------------------------------------------------

func TestE2EPolicyEntRenameNeedsBothNamesCLI(t *testing.T) {
	const (
		key    = "pg_policy_rn_src"
		src    = "input_boolean." + key
		dst    = "input_boolean.pg_policy_rn_dst"
		autoID = "pg_policy_rn_auto"
	)
	ctx := context.Background()
	if _, err := testClient.CreateHelper(ctx, key+":\n  name: PG Policy Rename Source\n", "input_boolean"); err != nil {
		t.Fatalf("seeding helper %s: %v", src, err)
	}
	t.Cleanup(func() { _, _ = testClient.DeleteHelper(ctx, key) })
	t.Cleanup(func() {
		ws := haapi.NewWSClient(haURL, haToken)
		if err := ws.Connect(ctx); err == nil {
			_ = ws.EntityRegistryUpdate(ctx, dst, map[string]any{"new_entity_id": src})
			_ = ws.Close()
		}
	})
	if _, err := testClient.CreateAutomationDef(ctx, fmt.Sprintf(`id: %s
alias: PG Policy Rename Auto
mode: single
trigger:
  - platform: state
    entity_id: %s
action:
  - delay: "00:00:01"
`, autoID, src)); err != nil {
		t.Fatalf("seeding automation %s: %v", autoID, err)
	}
	t.Cleanup(func() { _, _ = testClient.DeleteAutomationDef(ctx, autoID) })
	waitForHAState(t, src)

	nothingRenamed := func(t *testing.T) {
		t.Helper()
		if _, ok, err := registryEntryE2E(ctx, src); err != nil || !ok {
			t.Errorf("refused rename: %s is gone from HA's registry (err %v)", src, err)
		}
		if _, ok, err := registryEntryE2E(ctx, dst); err != nil || ok {
			t.Errorf("refused rename: %s exists in HA's registry (err %v)", dst, err)
		}
		cfg, err := haAutomationConfig(ctx, autoID)
		if err != nil {
			t.Fatalf("reading %s from HA: %v", autoID, err)
		}
		if !strings.Contains(string(cfg), src) || strings.Contains(string(cfg), dst) {
			t.Errorf("refused rename rewrote automation %s: %s", autoID, cfg)
		}
	}
	args := []string{"ent", "rename", src, dst}

	for _, tc := range []struct {
		name, pattern, verb, resource string
	}{
		{"create on the new id", "entity:" + dst, "create", "entity:" + dst},
		{"edit on the old id", "entity:*/registry/entity_id", "edit", "entity:" + src + "/registry/entity_id"},
		{"one rewritten file", "file:automations.yaml", "edit", "file:automations.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := pgE2EPolicy(fmt.Sprintf("    deny:\n      %q: [%s]\n", tc.pattern, tc.verb))
			assertE2ERefused(t, policyE2EDir(t, policy), args, e2eDenied{
				Resource: tc.resource, Verb: tc.verb,
				Rule: fmt.Sprintf("deny %q [%s]", tc.pattern, tc.verb),
				Line: policyLineOfE2E(t, policy, fmt.Sprintf("%q", tc.pattern)),
			})
			nothingRenamed(t)
		})
	}

	assertE2EAllowed(t, policyE2EDir(t, openE2EPolicy), args...)
	if _, ok, err := registryEntryE2E(ctx, dst); err != nil || !ok {
		t.Errorf("allowed rename: %s is not in HA's registry (err %v)", dst, err)
	}
	if cfg, err := haAutomationConfig(ctx, autoID); err != nil || !strings.Contains(string(cfg), dst) {
		t.Errorf("allowed rename did not rewrite %s (err %v): %s", autoID, err, cfg)
	}
}

// ---------------------------------------------------------------------------
// E-5 for auto apply: a different id: is a rename; no id: is refused.
// ---------------------------------------------------------------------------

func TestE2EPolicyAutoApplyRenameNeedsCreateCLI(t *testing.T) {
	const (
		id      = "pg_policy_ap"
		renamed = "pg_policy_ap_renamed"
	)
	ctx := context.Background()
	body := func(idLine, alias string) string {
		return idLine + "alias: " + alias + "\nmode: single\ntrigger:\n  - platform: time\n    at: \"03:00:00\"\naction:\n  - delay: \"00:00:01\"\n"
	}
	if _, err := testClient.CreateAutomationDef(ctx, body("id: "+id+"\n", "PG Policy Apply")); err != nil {
		t.Fatalf("seeding automation %s: %v", id, err)
	}
	t.Cleanup(func() {
		_, _ = testClient.DeleteAutomationDef(ctx, id)
		_, _ = testClient.DeleteAutomationDef(ctx, renamed)
	})
	aliasInHA := func() string {
		raw, err := haAutomationConfig(ctx, id)
		if err != nil {
			t.Fatalf("reading %s from HA: %v", id, err)
		}
		var cfg struct {
			Alias string `json:"alias"`
		}
		if err := json.Unmarshal(raw, &cfg); err != nil {
			t.Fatalf("automation config did not parse: %v", err)
		}
		return cfg.Alias
	}

	policy := pgE2EPolicy(fmt.Sprintf("    deny:\n      \"automation:%s\": [create]\n", renamed))
	dir := policyE2EDir(t, policy)

	renamedFile := writeTempYAML(t, "renamed.yaml", body("id: "+renamed+"\n", "PG Policy Apply Renamed"))
	assertE2ERefused(t, dir, []string{"auto", "apply", id, "-f", renamedFile}, e2eDenied{
		Resource: "automation:" + renamed, Verb: "create",
		Rule: fmt.Sprintf(`deny "automation:%s" [create]`, renamed),
		Line: policyLineOfE2E(t, policy, `"automation:`+renamed+`"`),
	})
	if got := aliasInHA(); got != "PG Policy Apply" {
		t.Errorf("refused apply changed %s's alias to %q", id, got)
	}
	if _, err := haAutomationConfig(ctx, renamed); err == nil {
		t.Errorf("refused apply created automation %s", renamed)
	}

	// No id: at all — the write's identity is not pinned, so it is refused
	// while a profile is in effect (D-78), fail-closed: no rule decided it.
	idless := writeTempYAML(t, "idless.yaml", body("", "PG Policy Apply Idless"))
	for _, confirm := range []bool{false, true} {
		args := []string{"auto", "apply", id, "-f", idless, "--json"}
		if confirm {
			args = append(args, "--confirm")
		}
		out, _, code := runPolicyE2E(t, dir, nil, args...)
		if code != 1 {
			t.Errorf("hactl %v: exit %d, want 1 — an apply with no id: is refused under a profile\n%s", args, code, out)
			continue
		}
		r := decodeE2ERefusal(t, fmt.Sprintf("hactl %v", args), out, !confirm)
		if !slices.ContainsFunc(r.Details.Policy.Denied, func(d e2eDenied) bool {
			return strings.HasPrefix(d.Rule, "fail-closed: ") && d.Line == 0
		}) {
			t.Errorf("hactl %v: no fail-closed denial in %+v", args, r.Details.Policy.Denied)
		}
	}
	if got := aliasInHA(); got != "PG Policy Apply" {
		t.Errorf("refused id-less apply changed %s's alias to %q", id, got)
	}

	// The same id: under the same profile is a plain edit, allowed.
	same := writeTempYAML(t, "same.yaml", body("id: "+id+"\n", "PG Policy Apply Edited"))
	assertE2EAllowed(t, dir, "auto", "apply", id, "-f", same)
	if got := aliasInHA(); got != "PG Policy Apply Edited" {
		t.Errorf("allowed apply: alias in HA is %q, want PG Policy Apply Edited", got)
	}
}

// ---------------------------------------------------------------------------
// E-7 + R-3 + D-66: ref replace is all or nothing.
// ---------------------------------------------------------------------------

func TestE2EPolicyRefReplaceAllOrNothingCLI(t *testing.T) {
	const (
		oldID    = "binary_sensor.pg_policy_e7_old"
		newID    = "binary_sensor.pg_policy_e7_new"
		autoID   = "pg_policy_e7_auto"
		scriptID = "pg_policy_e7_script"
	)
	ctx := context.Background()
	if _, err := testClient.CreateAutomationDef(ctx, fmt.Sprintf(`id: %s
alias: PG Policy E7 Auto
mode: single
trigger:
  - platform: state
    entity_id: %s
action:
  - delay: "00:00:01"
`, autoID, oldID)); err != nil {
		t.Fatalf("seeding automation: %v", err)
	}
	t.Cleanup(func() { _, _ = testClient.DeleteAutomationDef(ctx, autoID) })
	if _, err := testClient.CreateScriptDef(ctx, fmt.Sprintf(`%s:
  alias: PG Policy E7 Script
  mode: single
  sequence:
    - condition: state
      entity_id: %s
      state: "on"
`, scriptID, oldID)); err != nil {
		t.Fatalf("seeding script: %v", err)
	}
	t.Cleanup(func() { _, _ = testClient.DeleteScriptDef(ctx, scriptID) })

	untouched := func(t *testing.T) {
		t.Helper()
		for _, file := range []string{"automations.yaml", "scripts.yaml"} {
			content := readConfigFileE2E(t, file)
			if !strings.Contains(content, oldID) || strings.Contains(content, newID) {
				t.Errorf("refused ref replace rewrote %s — all or nothing (E-7):\n%s", file, content)
			}
		}
	}
	args := []string{"ref", "replace", oldID, newID}

	for _, tc := range []struct {
		name, pattern, resource string
		partial                 bool
	}{
		// One of the two files the rewrite touches is denied: neither is written.
		{"one denied file", "file:scripts.yaml", "file:scripts.yaml", false},
		// R-3: the change in automations.yaml also maps to its typed owner.
		{"typed owner", "automation:" + autoID, "automation:" + autoID + "/config", false},
		// D-66: a file is addressed whole, so a denied block it does not even
		// rewrite refuses the edit of the file.
		{"denied block elsewhere in the file", "file:scripts.yaml/block/pg_policy_untouched", "file:scripts.yaml", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			policy := pgE2EPolicy(fmt.Sprintf("    deny:\n      %q: [edit]\n", tc.pattern))
			assertE2ERefused(t, policyE2EDir(t, policy), args, e2eDenied{
				Resource: tc.resource, Verb: "edit", Partial: tc.partial,
				Rule: fmt.Sprintf("deny %q [edit]", tc.pattern),
				Line: policyLineOfE2E(t, policy, fmt.Sprintf("%q", tc.pattern)),
			})
			untouched(t)
		})
	}

	assertE2EAllowed(t, policyE2EDir(t, openE2EPolicy), args...)
	for _, file := range []string{"automations.yaml", "scripts.yaml"} {
		if content := readConfigFileE2E(t, file); strings.Contains(content, oldID) || !strings.Contains(content, newID) {
			t.Errorf("allowed ref replace did not rewrite %s:\n%s", file, content)
		}
	}
}

// ---------------------------------------------------------------------------
// E-2 / D-73: the services hactl sends itself are checked like any service.
// ---------------------------------------------------------------------------

func TestE2EPolicyScriptApplyNeedsReloadServiceCLI(t *testing.T) {
	const id = "pg_policy_sa"
	ctx := context.Background()
	if _, err := testClient.CreateScriptDef(ctx, id+":\n  alias: PG Policy SA\n  mode: single\n  sequence:\n    - delay: \"00:00:01\"\n"); err != nil {
		t.Fatalf("seeding script: %v", err)
	}
	t.Cleanup(func() { _, _ = testClient.DeleteScriptDef(ctx, id) })
	storedAlias := func() string {
		got, err := testClient.GetScriptDef(ctx, id)
		if err != nil {
			t.Fatalf("reading %s: %v", id, err)
		}
		def, ok := yamlDoc(t, got.Content)[id].(map[string]any)
		if !ok {
			t.Fatalf("stored script %s is not keyed by its id:\n%s", id, got.Content)
		}
		alias, _ := def["alias"].(string)
		return alias
	}
	candidate := writeTempYAML(t, "sa.yaml", id+":\n  alias: PG Policy SA Applied\n  mode: single\n  sequence:\n    - delay: \"00:00:02\"\n")
	args := []string{"script", "apply", id, "-f", candidate}

	base := "    default: deny\n    allow:\n      \"script:pg_policy_*\": [read, edit]\n"
	assertE2ERefused(t, policyE2EDir(t, pgE2EPolicy(base)), args, e2eDenied{
		Resource: "service:script.reload", Verb: "control", Rule: "default: deny", Line: 0,
	})
	if got := storedAlias(); got != "PG Policy SA" {
		t.Errorf("refused script apply wrote %s (alias %q)", id, got)
	}

	assertE2EAllowed(t, policyE2EDir(t, pgE2EPolicy(base+"      \"service:script.reload\": [control]\n")), args...)
	if got := storedAlias(); got != "PG Policy SA Applied" {
		t.Errorf("allowed script apply: alias %q, want PG Policy SA Applied", got)
	}
}

// ---------------------------------------------------------------------------
// R-11 / D-80: the confirm guard and the policy meet.
// ---------------------------------------------------------------------------

// pgWitnessLabel is the label the confirm-guard tests try to create.
const pgWitnessLabel = "PG Policy Witness Label"

// witnessLabelExists reads HA's label registry outside hactl.
func witnessLabelExists(t *testing.T) bool {
	t.Helper()
	ctx := context.Background()
	ws := haapi.NewWSClient(haURL, haToken)
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("connecting to HA: %v", err)
	}
	defer ws.Close() //nolint:errcheck // best-effort close in test helper
	labels, err := ws.LabelRegistryList(ctx)
	if err != nil {
		t.Fatalf("listing labels: %v", err)
	}
	return slices.ContainsFunc(labels, func(l haapi.LabelEntry) bool { return l.Name == pgWitnessLabel })
}

// cleanupWitnessLabel removes the label should a refusal have let it through.
func cleanupWitnessLabel(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		ctx := context.Background()
		ws := haapi.NewWSClient(haURL, haToken)
		if err := ws.Connect(ctx); err != nil {
			return
		}
		defer ws.Close() //nolint:errcheck // best-effort close in test helper
		labels, _ := ws.LabelRegistryList(ctx)
		for _, l := range labels {
			if l.Name == pgWitnessLabel {
				_ = ws.LabelRegistryDelete(ctx, l.LabelID)
			}
		}
	})
}

// witnessDenyPolicy refuses every label create (D-75: one create-deny on a
// label id is a deny on the kind).
var witnessDenyPolicy = pgE2EPolicy("    deny:\n      \"label:pg_policy_nothing\": [create]\n")

// guardEnv runs the binary with the confirm guard live, in its own session.
func guardEnv(prefix string) []string {
	return []string{"HACTL_MANUAL_MODE=progressive", fmt.Sprintf("HACTL_SESSION=%s-%d", prefix, time.Now().UnixNano())}
}

// TestE2EPolicyRefusedPreviewRecordsNoWitnessCLI — R-11 with the confirm guard
// live: a refused preview records no witness, so the --confirm after it is
// refused by the guard, before the policy is asked.
func TestE2EPolicyRefusedPreviewRecordsNoWitnessCLI(t *testing.T) {
	cleanupWitnessLabel(t)
	dir := policyE2EDir(t, witnessDenyPolicy)
	env := guardEnv("pg-policy-r11")
	if out, _, code := runPolicyE2E(t, dir, env, "label", "create", pgWitnessLabel); code != 1 || !strings.Contains(out, "policy_denied") {
		t.Fatalf("preview under a kind-wide create deny: exit %d, want a policy refusal\n%s", code, out)
	}
	out, errOut, code := runPolicyE2E(t, dir, env, "label", "create", pgWitnessLabel, "--confirm")
	if code != 1 {
		t.Fatalf("--confirm after a refused preview: exit %d, want 1\n%s%s", code, out, errOut)
	}
	if !strings.Contains(errOut, "--confirm refused") {
		t.Errorf("--confirm after a refused preview was not refused by the confirm guard:\nstdout:\n%s\nstderr:\n%s", out, errOut)
	}
	if strings.Contains(out+errOut, "policy_denied") {
		t.Errorf("the guard refuses before the policy is asked — no policy_denied expected:\nstdout:\n%s\nstderr:\n%s", out, errOut)
	}
	if witnessLabelExists(t) {
		t.Errorf("label %q was created", pgWitnessLabel)
	}
}

// TestE2EPolicyPassedPreviewPromisesNothingCLI — D-80 with the confirm guard
// live: a preview that passed authorises the --confirm only under the same
// policy. --confirm re-reads the file, is refused by the deny added since,
// and says the preview had passed.
func TestE2EPolicyPassedPreviewPromisesNothingCLI(t *testing.T) {
	cleanupWitnessLabel(t)
	dir := policyE2EDir(t, openE2EPolicy)
	env := guardEnv("pg-policy-d80")
	if out, errOut, code := runPolicyE2E(t, dir, env, "label", "create", pgWitnessLabel); code != 0 {
		t.Fatalf("allowed preview: exit %d\n%s%s", code, out, errOut)
	}
	writePolicyE2E(t, dir, witnessDenyPolicy)
	out, errOut, code := runPolicyE2E(t, dir, env, "label", "create", pgWitnessLabel, "--confirm")
	if code != 1 {
		t.Fatalf("--confirm after the policy changed: exit %d, want 1\n%s%s", code, out, errOut)
	}
	want := e2eDenied{
		Resource: "label:*", Verb: "create", KindWide: true,
		Rule: `deny "label:pg_policy_nothing" [create]`,
		Line: policyLineOfE2E(t, witnessDenyPolicy, `"label:pg_policy_nothing"`),
	}
	if !strings.Contains(out, want.textLine()) {
		t.Errorf("--confirm was not refused by the changed policy (want %q):\nstdout:\n%s\nstderr:\n%s", want.textLine(), out, errOut)
	}
	if all := strings.ToLower(out + errOut); !strings.Contains(all, "preview") || !strings.Contains(all, "passed") {
		t.Errorf("the refusal does not say the preview had passed (D-80):\nstdout:\n%s\nstderr:\n%s", out, errOut)
	}
	if witnessLabelExists(t) {
		t.Errorf("label %q was created", pgWitnessLabel)
	}
}
