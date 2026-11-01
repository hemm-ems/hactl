package cmd

import (
	"slices"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
)

// TestHelperDomainsAreThePolicyHelperDomains — `policy lint` enumerates the
// storage helpers with `helper ls`'s domain set and resolves their owners with
// the policy's (D-115, D-116): one set, two copies, held in lockstep.
func TestHelperDomainsAreThePolicyHelperDomains(t *testing.T) {
	got := slices.Sorted(slices.Values(helperDomains))
	want := slices.Sorted(slices.Values(policy.HelperDomains()))
	if !slices.Equal(got, want) {
		t.Errorf("helper ls domains %v, policy.HelperDomains %v — keep them in lockstep", got, want)
	}
}

// TestPolicyLintHelperIDsAreDomainSlashObjectID — a storage helper is checked
// by `<domain>/<object id>`, a YAML helper by the companion's `<domain>/<id>`;
// a helper-domain entity the companion does not list is still known, and a
// non-helper domain contributes nothing (D-116).
func TestPolicyLintHelperIDsAreDomainSlashObjectID(t *testing.T) {
	body := `version: 1
active: agent
profiles:
  agent:
    deny:
      "helper:input_number/target_temp": [delete]
      "helper:input_boolean/guest_mode": [delete]
      "helper:sensor/tpl_temp": [delete]
`
	dir := inventoryRig(t, body, true)
	d, fs := lintFindings(t, dir)
	noMatch := make([]int, 0, 1)
	for _, f := range withCode(fs, "no-match") {
		noMatch = append(noMatch, f.Line)
	}
	if !slices.Equal(noMatch, []int{8}) {
		t.Errorf("no-match on lines %v, want [8] (sensor is no helper domain); findings %+v", noMatch, fs)
	}
	for _, u := range d.Unchecked {
		if u.Kind == "helper" {
			t.Errorf("helper unchecked with the companion up: %+v", u)
		}
	}
}
