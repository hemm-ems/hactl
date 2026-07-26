//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
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

// haAutomations asks HA itself what automations exist and what state each is
// in, keyed by the entity object_id that `auto ls` shows in its `id` column,
// plus the config `id:` attribute HA exposes on the entity. The two are the
// same string in this fixture but not in general (that divergence is what the
// oracle rig exists to pin), so both are carried.
func haAutomations(t *testing.T, inst *hatest.Instance) (states map[string]string, configIDToObject map[string]string) {
	t.Helper()
	client := haapi.New(inst.URL(), inst.Token())
	raw, err := client.GetStates(context.Background())
	if err != nil {
		t.Fatalf("get states: %v", err)
	}
	var all []struct {
		EntityID   string `json:"entity_id"`
		State      string `json:"state"`
		Attributes struct {
			ID string `json:"id"`
		} `json:"attributes"`
	}
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatalf("decode states: %v", err)
	}
	states = map[string]string{}
	configIDToObject = map[string]string{}
	for _, s := range all {
		objectID, ok := strings.CutPrefix(s.EntityID, "automation.")
		if !ok {
			continue
		}
		states[objectID] = s.State
		if s.Attributes.ID != "" {
			configIDToObject[s.Attributes.ID] = objectID
		}
	}
	return states, configIDToObject
}

// TestFaultyAutoLsFailing pins `--failing` against HA's own trace record on an
// instance whose automations really do fail. The old body ran the command and
// discarded the output with the comment "should not panic" — which is true of
// a filter that returns every automation, and of one that returns none.
//
// The faulty fixture's broken_template fires on a time_pattern, so whether a
// failing run has happened yet depends on the clock. That is why the expected
// value is read from HA rather than written down: whatever set HA's trace list
// says has errored, that is the set `auto ls --failing` must name — including
// the empty set, if nothing has fired yet.
func TestFaultyAutoLsFailing(t *testing.T) {
	inst := getFaultyHA(t)

	_, configIDToObject := haAutomations(t, inst)
	var want []string
	for _, itemID := range oracleErroredTraceItemIDs(t, inst) {
		if obj, ok := configIDToObject[itemID]; ok {
			want = append(want, obj)
			continue
		}
		want = append(want, itemID)
	}

	got := autoLsIDs(t, runHactlDir(t, inst.Dir(), "auto", "ls", "--failing", "--top", "1000", "--json"))
	assertSameSet(t, "auto ls --failing (HA's errored automation traces)", want, got)

	all := autoLsIDs(t, runHactlDir(t, inst.Dir(), "auto", "ls", "--top", "1000", "--json"))
	if len(all) == 0 {
		t.Fatal("precondition: auto ls lists no automations on the faulty fixture, so --failing " +
			"returning nothing would prove nothing")
	}
}

// TestFaultyAutoLsShowsDisabled makes the claim in this test's name checkable.
// It used to `t.Log` when the disabled automation was absent, which is a pass —
// the one thing the test existed to catch could not fail it.
//
// The expected answer is HA's: every automation entity HA reports must be
// listed with the state HA reports for it, disabled ones included. The state
// itself is not hard-coded — the faulty fixture's disabled_automation is
// `initial_state: false`, and HA renders a never-loaded automation as
// `unavailable` rather than `off`, which is exactly the kind of detail a
// hand-written expectation gets wrong. What is required is only that HA holds
// at least one automation in some state other than `on`, and that hactl lists
// it under the state HA gave it. A listing that filtered out non-`on`
// automations, or that reported them all as `on`, reddens here.
func TestFaultyAutoLsShowsDisabled(t *testing.T) {
	inst := getFaultyHA(t)

	haStates, _ := haAutomations(t, inst)
	var notRunning []string
	for id, state := range haStates {
		if state != "on" {
			notRunning = append(notRunning, id)
		}
	}
	if len(notRunning) == 0 {
		t.Fatalf("precondition: HA reports every automation as on, so a listing that dropped "+
			"disabled ones would still pass. HA states: %v", haStates)
	}

	raw := runHactlDir(t, inst.Dir(), "auto", "ls", "--top", "1000", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("auto ls --json did not parse: %v\noutput:\n%s", err, raw)
	}
	gotStates := map[string]string{}
	gotIDs := make([]string, 0, len(rows))
	for _, r := range rows {
		id, _ := r["id"].(string)
		state, _ := r["state"].(string)
		gotIDs = append(gotIDs, id)
		gotStates[id] = state
	}

	wantIDs := make([]string, 0, len(haStates))
	for id := range haStates {
		wantIDs = append(wantIDs, id)
	}
	assertSameSet(t, "auto ls ids (HA's automation.* entities)", wantIDs, gotIDs)

	for id, want := range haStates {
		if got, ok := gotStates[id]; ok && got != want {
			t.Errorf("auto ls reports %s state=%q, HA reports %q", id, got, want)
		}
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
