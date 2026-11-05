//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// policyDir is an instance directory for the shared HA (its URL and token) holding
// content as policy.yaml. The shared instance directory itself is never
// given a policy file.
func policyDir(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	env := "HA_URL=" + ha.URL() + "\nHA_TOKEN=" + ha.Token() + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// automationIDsFromHA reads automation config ids and entity object ids from
// Home Assistant's own /api/states, not through hactl.
func automationIDsFromHA(t *testing.T) map[string]string {
	t.Helper()
	body, err := haapi.New(ha.URL(), ha.Token()).GetStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var states []struct {
		EntityID   string `json:"entity_id"`
		Attributes struct {
			ID string `json:"id"`
		} `json:"attributes"`
	}
	if err := json.Unmarshal(body, &states); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{} // config id -> object id
	for _, s := range states {
		if obj, ok := strings.CutPrefix(s.EntityID, "automation."); ok && s.Attributes.ID != "" {
			out[s.Attributes.ID] = obj
		}
	}
	return out
}

// TestPolicyLintAgainstLiveHA checks every enumerated kind against a real
// instance: a misspelt pattern is no-match, a real one is not — and an
// automation is addressed by its config id (D-1), not by the entity object
// id `auto ls` prints, which HA derives from the alias.
func TestPolicyLintAgainstLiveHA(t *testing.T) {
	ids := automationIDsFromHA(t)
	obj, ok := ids["alarm_morning"]
	if !ok {
		t.Fatalf("the basic fixture's automation alarm_morning is not in /api/states (config ids: %v)", ids)
	}
	if obj == "alarm_morning" {
		t.Fatal("HA gave alarm_morning the object id alarm_morning: the fixture no longer has an automation whose two ids differ, so this test would prove nothing about which one lint reads")
	}

	dir := policyDir(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:automation.*": [read]
      "automation:alarm_morning": [edit]
      "service:automation.trigger": [control]
    deny:
      "entity:lokc.*": [write]
      "automation:`+obj+`": [delete]
      "service:light.turn_onn": [control]
      "area:nowhere_xyz": [delete]
`)
	out, err := runHactlDirErr(t, dir, "policy", "lint", "--json")
	var doc struct {
		Valid    bool `json:"valid"`
		Findings []struct {
			Code string `json:"code"`
			Rule string `json:"rule"`
		} `json:"findings"`
		Unchecked []struct {
			Kind   string `json:"kind"`
			Reason string `json:"reason"`
		} `json:"unchecked"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &doc); jsonErr != nil {
		t.Fatalf("policy lint --json: %v\n%s", jsonErr, out)
	}
	if err == nil {
		t.Fatalf("lint with four misspelt patterns exited 0\n%s", out)
	}
	if len(doc.Unchecked) != 0 {
		t.Errorf("unchecked = %+v: entity, automation, service and area are all enumerated from a live HA", doc.Unchecked)
	}
	got := make([]string, 0, len(doc.Findings))
	for _, f := range doc.Findings {
		got = append(got, f.Code+" "+f.Rule)
	}
	sort.Strings(got)
	want := []string{
		`no-match deny "area:nowhere_xyz" [delete]`,
		`no-match deny "automation:` + obj + `" [delete]`,
		`no-match deny "entity:lokc.*" [write]`,
		`no-match deny "service:light.turn_onn" [control]`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("findings =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// TestPolicyCheckEvaluatesWhereAgainstLiveRegistries: a where: selector is
// evaluated against the real registries — evaluated (no note) and, for an
// automation entity with no area, not holding.
func TestPolicyCheckEvaluatesWhereAgainstLiveRegistries(t *testing.T) {
	dir := policyDir(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:automation.*": [control]
    where:
      "entity:automation.*": { area: nowhere_xyz }
`)
	out, err := runHactlDirErr(t, dir, "policy", "check", "entity:automation.climate_schedule", "control", "--json")
	var doc struct {
		Allowed bool     `json:"allowed"`
		Rule    string   `json:"rule"`
		Notes   []string `json:"notes"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &doc); jsonErr != nil {
		t.Fatalf("policy check --json: %v\n%s", jsonErr, out)
	}
	var ec interface{ ExitCode() int }
	if err == nil || !errors.As(err, &ec) || ec.ExitCode() != 1 {
		t.Errorf("err = %v, want the exit-1 denial", err)
	}
	if doc.Allowed || doc.Rule != "default: deny" || len(doc.Notes) != 0 {
		t.Errorf("verdict = %+v, want denied by default: deny with the selector evaluated (no note)", doc)
	}
}
