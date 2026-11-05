//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

// Docker-tier blind spec tests for the pre-merge fixes (PLAN-policy-prelaunch-
// fixes §3.2), written from the contract only — D-113 (B-7) and D-122 (B-17) —
// without reading lint.go or svc.go.

// TestPolicyLintChecksWhereValuesAgainstLiveRegistries — D-113 against a real
// instance: a `where:` label and area that match nothing in the live
// registries are no-match on the where: entry's line, naming the narrowed
// allow; `*` counts as checked.
func TestPolicyLintChecksWhereValuesAgainstLiveRegistries(t *testing.T) {
	body := `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:automation.*": [control]
      "entity:light.*": [control]
      "entity:*": [read]
    where:
      "entity:automation.*": { label: no_such_label_xyz }
      "entity:light.*": { area: no_such_area_xyz }
      "entity:*": { area: "*" }
`
	dir := policyDir(t, body)
	// The owner's lint, at a terminal: the whole instance (D-173).
	out, _ := ownerLint(t, dir)
	var doc struct {
		Findings []struct {
			Code    string `json:"code"`
			Rule    string `json:"rule"`
			Line    int    `json:"line"`
			Message string `json:"message"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("policy lint --json: %v\n%s", err, out)
	}
	want := map[int]string{
		policyLineOf(t, body, "{ label: no_such_label_xyz }"): `where: label "no_such_label_xyz" matches no label on this instance (by id or name)`,
		policyLineOf(t, body, "{ area: no_such_area_xyz }"):   `where: area "no_such_area_xyz" matches no area on this instance (by id or name)`,
	}
	for _, f := range doc.Findings {
		if f.Code != "no-match" || !strings.Contains(f.Message, "where:") {
			continue
		}
		msg, ok := want[f.Line]
		if !ok {
			t.Errorf("where: no-match on line %d (%s): %s", f.Line, f.Rule, f.Message)
			continue
		}
		delete(want, f.Line)
		if !strings.Contains(f.Message, msg) {
			t.Errorf("line %d: %q lacks %q", f.Line, f.Message, msg)
		}
	}
	for line, msg := range want {
		t.Errorf("no where: no-match on line %d (%s)\n%s", line, msg, out)
	}
}

// TestSvcCallRefusesNullDataWithoutAPolicy — D-122 on a real instance: `--data
// null` is refused in the dry run, exit non-zero, nothing on stdout.
func TestSvcCallRefusesNullDataWithoutAPolicy(t *testing.T) {
	out, err := runHactlErr(t, "svc", "call", "homeassistant.check_config", "--data", "null")
	if err == nil {
		t.Fatalf("svc call --data null previewed:\n%s", out)
	}
	if out != "" {
		t.Errorf("stdout %q, want nothing", out)
	}
	if want := "invalid --data: want a JSON object ({…}), got null"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q lacks %q", err, want)
	}
}
