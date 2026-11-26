//go:build integration

package integration

import (
	"fmt"
	"strings"
	"testing"
)

// TestPolicyEnforceRunsWithoutCompanion — D-207 without a companion: a start
// is checked by its stored config read from Home Assistant's config API.
// Under a profile that denies the front door: a script of scripts.yaml that
// touches nothing denied starts; the goodnight-shaped one (a mark, then
// lock.lock on the front door) is refused naming the lock and its run path,
// and its first step never runs; so is triggering the automation that locks
// it; a script and an automation a package defines — which the config API does
// not serve — fail closed, saying why.
func TestPolicyEnforceRunsWithoutCompanion(t *testing.T) {
	inst := getPolicyRunHA(t)
	runOracleReset(t, inst)
	t.Cleanup(func() { runOracleReset(t, inst) })
	pol := fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    deny:
      "entity:lock.front_door": [control]
`, pgProfile, pgProfile)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), pol)
	lockDenied := pgDenied{
		Resource: "entity:lock.front_door", Verb: "control",
		Rule: `deny "entity:lock.front_door" [control]`,
		Line: policyLineOf(t, pol, `"entity:lock.front_door"`),
	}

	t.Run("allowed script starts", func(t *testing.T) {
		assertPolicyAllowedPreview(t, dir, "script", "run", "pg_run_inner")
		if out, err := runHactlDirErr(t, dir, "script", "run", "pg_run_inner", "--confirm"); err != nil {
			t.Fatalf("allowed script run --confirm without a companion refused: %v\n%s", err, out)
		}
		runOracleWait(t, inst, "input_boolean.pg_run_inner_ran", "on", "the allowed script ran")
	})

	for name, c := range map[string]struct {
		args []string
		via  string
	}{
		"goodnight script": {[]string{"script", "run", "pg_run_goodnight"}, "(run of script.pg_run_goodnight: action at /sequence/1)"},
		"automation.trigger": {pgSvc("automation.trigger", pgEntityData("automation.pg_run_night_lockup")),
			"(run of automation.pg_run_night_lockup: action at /actions/0)"},
	} {
		t.Run(name, func(t *testing.T) {
			assertPolicyRefused(t, dir, c.args, lockDenied)
			out, _ := runHactlDirErr(t, dir, c.args...)
			if !strings.Contains(out, lockDenied.textLine()+" "+c.via) {
				t.Errorf("hactl %v: the refusal does not name the run path %q:\n%s", c.args, c.via, out)
			}
			runOracleStays(t, inst, "input_boolean.pg_run_night_ran", "off", "the refused goodnight never ran")
		})
	}

	// A config nobody can read refuses the call itself: the request is the
	// denial's resource, the run and the reason follow the rule.
	for name, c := range map[string]struct {
		args           []string
		service, run   string
		kind, id, file string
	}{
		"package script": {[]string{"script", "run", "pg_run_pkg_script"}, "script/turn_on", "script.pg_run_pkg_script",
			"script", "pg_run_pkg_script", "scripts.yaml"},
		"package automation": {pgSvc("automation.trigger", pgEntityData("automation.pg_run_pkg_auto")), "automation/trigger", "automation.pg_run_pkg_auto",
			"automation", "pg_run_pkg_auto", "automations.yaml"},
	} {
		t.Run(name, func(t *testing.T) {
			assertPolicyRefused(t, dir, c.args, pgDenied{
				Resource: "rest POST /api/services/" + c.service, Verb: "control",
				Rule: fmt.Sprintf("fail-closed: stored config unreadable (run of %s: %s %s is not readable without the companion: "+
					"Home Assistant's config API serves only %s, and it is defined elsewhere (a package, another include))", c.run, c.kind, c.id, c.file),
			})
		})
	}
	runOracleStays(t, inst, "input_boolean.pg_run_auto_ran", "off", "no refused automation ran")

	// A profile that denies nothing refuses nothing a run does (D-207, the
	// gate D-218 gives a candidate write too): the same unreadable package
	// configs pass.
	open := pgInstanceDir(t, inst.URL(), inst.Token(), fmt.Sprintf(`version: 1
active: %s
profiles:
  %s:
    allow:
      "entity:*": [read]
`, pgProfile, pgProfile))
	for _, args := range [][]string{
		{"script", "run", "pg_run_pkg_script"},
		pgSvc("automation.trigger", pgEntityData("automation.pg_run_pkg_auto")),
	} {
		assertPolicyAllowedPreview(t, open, args...)
	}
}
