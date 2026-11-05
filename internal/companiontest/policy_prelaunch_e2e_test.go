//go:build companion

package companiontest

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

// Companion-tier blind spec tests for the pre-merge fixes (PLAN-policy-
// prelaunch-fixes §3.2), written from the contract only — D-116 (B-10) and
// D-123 (B-18) — without reading lint.go's inventory or helper.go's delete.

// TestE2EPolicyLintEnumeratesHelpersTemplatesAndConfigEntriesCLI — D-116 on a
// real instance and companion: helper ids come from `helper ls`'s sources,
// template unique_ids from the companion's list, config entry ids from Home
// Assistant; none of the three kinds is unchecked, and only the misspelt ids
// are no-match.
func TestE2EPolicyLintEnumeratesHelpersTemplatesAndConfigEntriesCLI(t *testing.T) {
	const helperID = "e2e_lint_inventory_helper"
	file := writeTempYAML(t, "helper.yaml", helperID+":\n  name: E2E Lint Inventory Helper\n")
	if out, err := runHactlE2E(t, "helper", "create", "input_boolean", "-f", file, "--confirm"); err != nil {
		t.Fatalf("creating the YAML helper: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = runHactlE2E(t, "helper", "delete", helperID, "--confirm") })

	policy := pgE2EPolicy(`    deny:
      "helper:input_boolean/` + helperID + `": [delete]
      "helper:input_boolean/no_such_helper_xyz": [delete]
      "template:seeded_test_sensor": [delete]
      "template:no_such_template_xyz": [delete]
      "config_entry:no_such_entry_xyz": [delete]
`)
	dir := policyE2EDir(t, policy)
	stdout, stderr, _ := runPolicyE2E(t, dir, nil, "policy", "lint", "--json")
	var doc struct {
		Findings []struct {
			Code string `json:"code"`
			Line int    `json:"line"`
			Rule string `json:"rule"`
		} `json:"findings"`
		Unchecked []struct {
			Kind   string `json:"kind"`
			Reason string `json:"reason"`
		} `json:"unchecked"`
	}
	if err := json.Unmarshal([]byte(stdout), &doc); err != nil {
		t.Fatalf("policy lint --json: %v\n%s%s", err, stdout, stderr)
	}
	for _, u := range doc.Unchecked {
		if u.Kind == "helper" || u.Kind == "template" || u.Kind == "config_entry" {
			t.Errorf("%s is unchecked (%s) on an instance with a companion", u.Kind, u.Reason)
		}
	}
	var got []int
	for _, f := range doc.Findings {
		if f.Code == "no-match" {
			got = append(got, f.Line)
		}
	}
	slices.Sort(got)
	want := []int{
		policyLineOfE2E(t, policy, "no_such_helper_xyz"),
		policyLineOfE2E(t, policy, "no_such_template_xyz"),
		policyLineOfE2E(t, policy, "no_such_entry_xyz"),
	}
	if !slices.Equal(got, want) {
		t.Errorf("no-match on lines %v, want %v (only the misspelt ids)\n%s", got, want, stdout)
	}
}

// TestE2EHelperDeleteNamesItsDomainWithoutAPolicyCLI — D-123 on a real
// instance without a policy: the dry run names the helper's domain, the
// confirmed delete removes exactly that helper, and a storage helper is still
// refused before any delete. (The two-domain slug that ended in a 409 needs
// two wired YAML helper domains; this tier wires input_boolean only, so the
// request's `domain=` is held by TestHelperDeleteNamesItsDomainWithoutAPolicy
// at the unit tier.)
func TestE2EHelperDeleteNamesItsDomainWithoutAPolicyCLI(t *testing.T) {
	const helperID = "e2e_delete_domain_helper"
	file := writeTempYAML(t, "helper.yaml", helperID+":\n  name: E2E Delete Domain Helper\n")
	if out, err := runHactlE2E(t, "helper", "create", "input_boolean", "-f", file, "--confirm"); err != nil {
		t.Fatalf("creating the YAML helper: %v\n%s", err, out)
	}

	preview, err := runHactlE2E(t, "helper", "delete", helperID)
	if err != nil {
		t.Fatalf("helper delete dry run: %v\n%s", err, preview)
	}
	if !strings.Contains(preview, "input_boolean") {
		t.Errorf("the dry run does not name the domain it resolved:\n%s", preview)
	}
	if out, confirmErr := runHactlE2E(t, "helper", "delete", helperID, "--confirm"); confirmErr != nil {
		t.Fatalf("helper delete --confirm without a policy: %v\n%s", confirmErr, out)
	}
	listed, err := runHactlE2E(t, "helper", "ls", "--json", "--top", "0")
	if err != nil {
		t.Fatalf("helper ls --json: %v\n%s", err, listed)
	}
	if strings.Contains(listed, `"`+helperID+`"`) {
		t.Errorf("the deleted helper is still listed:\n%s", listed)
	}

	id := createUIHelper(t, "input_boolean", map[string]any{"name": "E2E Delete Domain Storage"})
	entityID := "input_boolean." + id
	waitForHelperShow(t, entityID)
	if out, err := runHactlE2E(t, "helper", "delete", entityID, "--confirm"); err == nil {
		t.Errorf("a storage helper was deleted without a policy:\n%s", out)
	}
	if _, err := runHactlE2E(t, "helper", "show", entityID); err != nil {
		t.Errorf("the storage helper is gone after a refused delete")
	}
}
