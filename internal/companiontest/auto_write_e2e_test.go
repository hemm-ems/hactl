//go:build companion

package companiontest

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// The `auto` family's write half, end to end against a real Home Assistant and
// a real companion. These cases moved here from internal/integration when
// `auto apply`/`auto rollback` stopped writing through HA's own config endpoint
// (D-14, issue #128): that tier boots HA alone, so an instance without a
// companion is exactly where the commands now refuse — which is what the case
// left behind there asserts.

// storedAutomationsYAML reads automations.yaml as bytes on disk, through the
// companion's raw route rather than through anything that re-renders it. The
// byte-preservation claim is about the file, so the assertion has to read the
// file (H-12: a write is proven by reading it back from the instance, never
// through the command that made it).
func storedAutomationsYAML(t *testing.T) string {
	t.Helper()
	return readConfigFileE2E(t, "automations.yaml")
}

// automationEntry returns the block of automations.yaml belonging to id, from
// its `- id:` line to the next one.
func automationEntry(t *testing.T, file, id string) string {
	t.Helper()
	lines := strings.Split(file, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, "- ") && strings.Contains(line, id) {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(line, "- ") {
			return strings.Join(lines[start:i], "\n")
		}
	}
	if start < 0 {
		t.Fatalf("no entry for %s in automations.yaml:\n%s", id, file)
	}
	return strings.Join(lines[start:], "\n")
}

// TestE2EAutoApplyWritesOnlyItsOwnEntryCLI is issue #128's acceptance criterion
// and finding #93's other half, on a real instance.
//
// `auto apply --confirm` used to POST to HA's `/api/config/automation/config/`,
// and HA's storage collection re-dumps the whole automations.yaml with its own
// serializer: one confirmed apply came back having reformatted every other
// automation in the file. The bystander entry seeded in main_test.go carries a
// folded block scalar, a quoted `'single'` and keys in neither the author's nor
// alphabetical order — everything a re-serializer normalizes — so "byte
// identical" is a claim with something to lose.
func TestE2EAutoApplyWritesOnlyItsOwnEntryCLI(t *testing.T) {
	before := storedAutomationsYAML(t)
	bystanderBefore := automationEntry(t, before, "seeded_bystander_auto")

	// Start from what is stored, edit one line. This is the round trip the
	// manual teaches (`auto cat > file`), and it is what makes the diff below
	// a statement about one line rather than about formatting.
	stored, err := runHactlE2E(t, "auto", "cat", "seeded_test_auto")
	if err != nil {
		t.Fatalf("auto cat: %v\n%s", err, stored)
	}
	const newAlias = "Seeded Test Automation edited"
	edited := strings.Replace(stored, `alias: "Seeded Test Automation"`, `alias: "`+newAlias+`"`, 1)
	if edited == stored {
		t.Fatalf("the alias line is not where this case expects it:\n%s", stored)
	}
	candidate := filepath.Join(t.TempDir(), "seeded_test_auto.yaml")
	if writeErr := os.WriteFile(candidate, []byte(edited), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	// The preview: one line changed, and it says so as a number.
	plan, err := runHactlE2E(t, "auto", "apply", "seeded_test_auto", "-f", candidate, "--json")
	if err != nil {
		t.Fatalf("auto apply dry run: %v\n%s", err, plan)
	}
	var preview struct {
		Details struct {
			ChangedLines int `json:"changed_lines"`
		} `json:"details"`
		DryRun bool `json:"dry_run"`
	}
	if jsonErr := json.Unmarshal([]byte(plan), &preview); jsonErr != nil {
		t.Fatalf("preview --json does not parse: %v\n%s", jsonErr, plan)
	}
	if !preview.DryRun {
		t.Error("a run without --confirm reported dry_run: false")
	}
	// Finding #94: this used to be len(diff.Lines) — every context line and
	// every "… N unchanged lines …" marker counted as a change, so a one-line
	// alias edit reported 14.
	if preview.Details.ChangedLines != 2 {
		t.Errorf("changed_lines = %d for a one-line edit, want 2 (one removed, one added)",
			preview.Details.ChangedLines)
	}

	out, err := runHactlE2E(t, "auto", "apply", "seeded_test_auto", "-f", candidate, "--confirm")
	if err != nil {
		t.Fatalf("auto apply --confirm: %v\n%s", err, out)
	}

	after := storedAutomationsYAML(t)
	if got := automationEntry(t, after, "seeded_bystander_auto"); got != bystanderBefore {
		t.Errorf("a confirmed apply on one automation changed another one's bytes.\n--- before:\n%s\n--- after:\n%s",
			bystanderBefore, got)
	}
	// And the file outside the two entries — the leading comment, the spacing —
	// is the same document.
	if strings.Count(after, "\n- ") != strings.Count(before, "\n- ") {
		t.Errorf("the entry count changed: %d before, %d after", strings.Count(before, "\n- "), strings.Count(after, "\n- "))
	}
	// The edit landed, read back from HA rather than from hactl's echo (H-12).
	if !strings.Contains(automationEntry(t, after, "seeded_test_auto"), newAlias) {
		t.Errorf("the confirmed apply did not write the new alias:\n%s", automationEntry(t, after, "seeded_test_auto"))
	}
	client := haapi.New(haURL, haToken)
	raw, err := client.GetAutomationConfig(context.Background(), "seeded_test_auto")
	if err != nil {
		t.Fatalf("reading the config back from HA: %v", err)
	}
	var live map[string]any
	if jsonErr := json.Unmarshal(raw, &live); jsonErr != nil {
		t.Fatalf("HA's config for the automation does not parse: %v", jsonErr)
	}
	if live["alias"] != newAlias {
		t.Errorf("HA holds alias %v, want %q — the file changed but HA never read it", live["alias"], newAlias)
	}

	// --- rollback restores what it took -------------------------------------
	//
	// Exactly, and with no fold. The old integration test had to canonicalize
	// HA's singular/plural key migration away before comparing, because writing
	// through the Config API rewrote the schema; nothing rewrites it now.
	rollback, err := runHactlE2E(t, "auto", "rollback", "seeded_test_auto", "--confirm")
	if err != nil {
		t.Fatalf("auto rollback --confirm: %v\n%s", err, rollback)
	}
	restored := storedAutomationsYAML(t)
	if got, want := entryValues(t, restored, "seeded_test_auto"), entryValues(t, before, "seeded_test_auto"); got != want {
		t.Errorf("rollback did not restore the entry.\n--- want:\n%s\n--- got:\n%s", want, got)
	}
	if got := automationEntry(t, restored, "seeded_bystander_auto"); got != bystanderBefore {
		t.Errorf("the rollback changed the bystander's bytes:\n--- before:\n%s\n--- after:\n%s", bystanderBefore, got)
	}

	// --- and the cycle is byte-stable from here -----------------------------
	//
	// The one thing a write does to the entry it touches is re-serialize it in
	// the companion's canonical style: the seeded entry indents its sequences
	// four spaces and comes back indented two, once. That is the residual of
	// D-14's entry-granular contract, and it is worth pinning rather than
	// asserting away, because "the entry keeps changing on every write" and
	// "the entry was normalized once" look identical in a single measurement.
	stable := automationEntry(t, restored, "seeded_test_auto")
	if _, applyErr := runHactlE2E(t, "auto", "apply", "seeded_test_auto", "-f", candidate, "--confirm"); applyErr != nil {
		t.Fatalf("second apply: %v", applyErr)
	}
	if _, rollErr := runHactlE2E(t, "auto", "rollback", "seeded_test_auto", "--confirm"); rollErr != nil {
		t.Fatalf("second rollback: %v", rollErr)
	}
	if got := automationEntry(t, storedAutomationsYAML(t), "seeded_test_auto"); got != stable {
		t.Errorf("a second apply+rollback cycle moved the entry's bytes again — the write is not idempotent."+
			"\n--- was:\n%s\n--- now:\n%s", stable, got)
	}
}

// entryValues parses one entry out of automations.yaml and renders it in a
// canonical form, so a comparison is about what the automation IS rather than
// about how it is laid out. Used only where the layout is separately asserted.
func entryValues(t *testing.T, file, id string) string {
	t.Helper()
	var parsed any
	if err := yaml.Unmarshal([]byte(automationEntry(t, file, id)), &parsed); err != nil {
		t.Fatalf("parsing the %s entry: %v", id, err)
	}
	out, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		t.Fatalf("rendering the %s entry: %v", id, err)
	}
	return string(out)
}

// TestE2EAutoApplyPreviewRefusesWhatConfirmRefusesCLI — H-2 on the rerouted
// command: the preview goes through the same companion route as the confirmed
// write, so an id the write cannot resolve is refused before a plan exists.
func TestE2EAutoApplyPreviewRefusesWhatConfirmRefusesCLI(t *testing.T) {
	candidate := filepath.Join(t.TempDir(), "bogus.yaml")
	if err := os.WriteFile(candidate, []byte("alias: Nope\ntrigger: []\ncondition: []\naction: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runHactlE2E(t, "auto", "apply", "totally_bogus_automation_xyz", "-f", candidate)
	if err == nil {
		t.Fatalf("the preview planned an apply against an id that names no automation:\n%s", out)
	}
	if strings.Contains(out, "dry-run") || strings.Contains(out, "use --confirm") {
		t.Errorf("a refused preview printed a plan:\n%s", out)
	}
}
