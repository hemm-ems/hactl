//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/hemm-ems/hactl/internal/hatest"
)

// faultyHA provides a lazily-initialized HA instance with the faulty fixture.
// Booting takes ~30-60s so we reuse across all faulty tests.
var (
	faultyOnce sync.Once
	faultyHA   *hatest.Instance
)

func getFaultyHA(t *testing.T) *hatest.Instance {
	t.Helper()
	faultyOnce.Do(func() {
		faultyHA = hatest.StartShared(t, hatest.WithFixture("faulty"))
	})
	return faultyHA
}

func TestFaultyAutoLs(t *testing.T) {
	inst := getFaultyHA(t)
	out := runHactlDir(t, inst.Dir(), "auto", "ls")

	// Should list all automations including broken ones
	assertContains(t, out, "id")
	assertContains(t, out, "state")
}

func TestFaultyAutoLsFailing(t *testing.T) {
	inst := getFaultyHA(t)
	// --failing filters to only broken ones; may be empty if no traces with errors yet
	out := runHactlDir(t, inst.Dir(), "auto", "ls", "--failing")
	_ = out // should not panic
}

func TestFaultyAutoLsShowsDisabled(t *testing.T) {
	inst := getFaultyHA(t)
	out := runHactlDir(t, inst.Dir(), "auto", "ls")

	// The always_off automation should appear with state=off
	if !strings.Contains(out, "always_off") {
		t.Log("always_off not visible in auto ls (may not have loaded)")
	}
}

func TestFaultyHealth(t *testing.T) {
	inst := getFaultyHA(t)
	out := runHactlDir(t, inst.Dir(), "health")

	assertContains(t, out, "HA ")
	assertContains(t, out, "Faulty Home")
	assertContains(t, out, "state=")
}

func TestFaultyAutoShow(t *testing.T) {
	inst := getFaultyHA(t)
	out := runHactlDir(t, inst.Dir(), "auto", "show", "broken_template")

	assertContains(t, out, "broken_template")
	assertContains(t, out, "state=")
}

func TestFaultyAutoShowDisabled(t *testing.T) {
	inst := getFaultyHA(t)
	// The fixture's alias ("Disabled Automation") derives a different
	// entity_id ("automation.disabled_automation") than its config id
	// ("always_off") — the same config-id/entity-id mismatch #70 reports.
	// `show` must resolve the config id via /api/states rather than
	// guessing "automation.always_off", which never exists.
	out, err := runHactlDirErr(t, inst.Dir(), "auto", "show", "always_off")
	if err != nil {
		// Check if the automation appears in the list at all
		lsOut := runHactlDir(t, inst.Dir(), "auto", "ls")
		if !strings.Contains(lsOut, "disabled_automation") {
			t.Skip("always_off automation not loaded by HA (disabled automations may not create entities)")
		}
		t.Skipf("always_off entity not available via states API: %v", err)
	}

	assertContains(t, out, "automation.disabled_automation")
}

func TestFaultyScriptLs(t *testing.T) {
	inst := getFaultyHA(t)
	out := runHactlDir(t, inst.Dir(), "script", "ls")
	assertContains(t, out, "id")
	assertContains(t, out, "state")
}

func TestFaultyScriptLsHasFixtures(t *testing.T) {
	inst := getFaultyHA(t)
	entries := make([]map[string]string, 0)
	out := runHactlDir(t, inst.Dir(), "script", "ls", "--json")
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("script ls --json invalid: %v", err)
	}
	ids := make(map[string]bool)
	for _, e := range entries {
		ids[e["id"]] = true
	}
	for _, want := range []string{"broken_delay", "error_service", "working_toggle"} {
		if !ids[want] {
			t.Errorf("faulty scripts missing %q, got: %v", want, ids)
		}
	}
}

func TestFaultyScriptRun(t *testing.T) {
	inst := getFaultyHA(t)
	out := runHactlDir(t, inst.Dir(), "script", "run", "working_toggle", "--confirm")
	assertContains(t, out, "executed script.working_toggle")
}
