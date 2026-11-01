package cmd

import (
	"slices"
	"strings"
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

// inventoryRegistries are registries for inventoryRig's entities:
// input_boolean.guest_mode is a helper renamed in the UI (unique_id guest_uid)
// in area hall with label energy; input_number.target_temp has no area of its
// own and sits on dev1 (area kitchen, label energy); sensor.tpl_temp is a
// template entity whose block prefixes its unique_id.
func inventoryRegistries() map[string]any {
	return map[string]any{
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "input_boolean.guest_mode", "platform": "input_boolean", "unique_id": "guest_uid", "device_id": nil, "area_id": "hall", "labels": []string{"energy"}},
			{"entity_id": "input_number.target_temp", "platform": "input_number", "unique_id": "target_temp", "device_id": "dev1", "area_id": nil, "labels": []string{}},
			{"entity_id": "sensor.tpl_temp", "platform": "template", "unique_id": "blk-tpl_temp", "device_id": nil, "area_id": nil, "labels": []string{}},
		},
		"config/device_registry/list": []map[string]any{
			{"id": "dev1", "name": "Thermostat", "area_id": "kitchen", "labels": []string{"energy"}},
		},
		"config/area_registry/list":  []map[string]any{{"area_id": "hall", "name": "Hall"}, {"area_id": "kitchen", "name": "Kitchen"}},
		"config/label_registry/list": []map[string]any{{"label_id": "energy", "name": "Energy"}},
		"config/floor_registry/list": []any{},
	}
}

// TestPolicyLintResolvesOwnersAndSelectorsFromTheRegistries — the CLI's
// registry read carries what lint resolves with: a helper's platform and
// unique_id, a template entity's, an entity's own area and labels, a
// device's area and labels (D-114, D-115), and a YAML helper the states do
// not hold yet (D-116).
func TestPolicyLintResolvesOwnersAndSelectorsFromTheRegistries(t *testing.T) {
	for _, tc := range []struct {
		name, code, rules, want string
	}{
		{"helper owns its entity", "half-protected", `    deny:
      "helper:input_boolean/guest_mode": [delete]
`, `deny "helper:input_boolean/guest_mode" [delete] protects the helper's config, but its entity input_boolean.guest_mode can still be changed`},
		{"an entity names its helper by unique_id", "half-protected", `    deny:
      "entity:input_boolean.guest_mode": [write]
`, `its config helper:input_boolean/guest_uid can still be changed (delete helper:input_boolean/guest_uid is allowed)`},
		{"template owns its entity", "half-protected", `    deny:
      "template:tpl_temp": [write]
`, `deny "template:tpl_temp" [write] protects the template's config, but its entity sensor.tpl_temp can still be changed`},
		{"an entity's own area", "self-grant", `    default: deny
    allow:
      "entity:input_boolean.*": [control]
      "entity:*": [edit]
    where:
      "entity:input_boolean.*": { label: energy }
      "entity:*": { area: hall }
`, `allow "entity:*" [edit] where area=hall (line 8) lets it change labels on 1 of the entities this allow matches (e.g. input_boolean.guest_mode)`},
		{"an entity's own labels", "self-grant", `    default: deny
    allow:
      "entity:input_boolean.*": [control]
      "entity:*": [edit]
    where:
      "entity:input_boolean.*": { area: hall }
      "entity:*": { label: energy }
`, `allow "entity:*" [edit] where label=energy (line 8) lets it change area_id on 1 of the entities this allow matches (e.g. input_boolean.guest_mode)`},
		{"an entity's area from its device", "self-grant", `    default: deny
    allow:
      "entity:input_number.*": [control]
      "entity:*": [edit]
    where:
      "entity:input_number.*": { label: energy }
      "entity:*": { area: kitchen }
`, `allow "entity:*" [edit] where area=kitchen (line 8) lets it change labels on 1 of the entities this allow matches (e.g. input_number.target_temp)`},
		{"a device's labels", "self-grant", `    default: deny
    allow:
      "device:dev*": [delete]
      "device:*": [edit]
    where:
      "device:dev*": { area: kitchen }
      "device:*": { label: energy }
`, `allow "device:*" [edit] where label=energy (line 8) lets it change area_id on 1 of the devices this allow matches (e.g. dev1)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := inventoryRigWith(t, "version: 1\nactive: agent\nprofiles:\n  agent:\n"+tc.rules, true, inventoryRegistries())
			_, fs := lintFindings(t, dir)
			var msgs []string
			for _, f := range withCode(fs, tc.code) {
				msgs = append(msgs, f.Message)
			}
			if !slices.ContainsFunc(msgs, func(m string) bool { return strings.Contains(m, tc.want) }) {
				t.Errorf("%s findings %q, want one holding %q; all findings %+v", tc.code, msgs, tc.want, fs)
			}
		})
	}
}

// TestPolicyLintKnowsAYAMLHelperWithoutAnEntity — a helper the companion lists
// but the states do not hold is still a live id, not no-match (D-116).
func TestPolicyLintKnowsAYAMLHelperWithoutAnEntity(t *testing.T) {
	dir := inventoryRig(t, "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"helper:input_boolean/yaml_only\": [delete]\n", true)
	if _, fs := lintFindings(t, dir); len(withCode(fs, "no-match")) != 0 {
		t.Errorf("no-match for a YAML helper the companion lists: %+v", withCode(fs, "no-match"))
	}
}
