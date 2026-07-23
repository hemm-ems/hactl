//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// ============================================================================
// H-8 — Distinguishability, and the trace-identity defects it exposes.
//
// HA keys automation traces by the automation's config `id:`. The entity_id is
// a separate string derived from the alias. Any code that uses one where the
// other belongs is wrong — but only observably wrong when the two differ AND a
// trace exists. The older fixtures had the first property and never the second.
// ============================================================================

// TestOracleFixtureIsDistinguishing guards the fixture itself. If someone later
// adds an automation whose config id happens to equal its object_id, the
// identity tests below would silently stop testing anything. This fails first
// and says why.
func TestOracleFixtureIsDistinguishing(t *testing.T) {
	inst, _ := getOracleHA(t)
	client := haapi.New(inst.URL(), inst.Token())

	raw, err := client.GetStates(context.Background())
	if err != nil {
		t.Fatalf("get states: %v", err)
	}
	var states []struct {
		EntityID   string `json:"entity_id"`
		Attributes struct {
			ID string `json:"id"`
		} `json:"attributes"`
	}
	if err := json.Unmarshal(raw, &states); err != nil {
		t.Fatalf("decode states: %v", err)
	}

	checked := 0
	for _, s := range states {
		if !strings.HasPrefix(s.EntityID, "automation.") || s.Attributes.ID == "" {
			continue
		}
		objectID := strings.TrimPrefix(s.EntityID, "automation.")
		if s.Attributes.ID == objectID {
			t.Errorf("H-8 violated: automation %s has config id %q equal to its object_id; "+
				"this fixture entry cannot distinguish a config-id bug from an entity-id bug",
				s.EntityID, s.Attributes.ID)
		}
		checked++
	}
	if checked < 4 {
		t.Fatalf("H-8: expected >=4 automations with a config id, found %d", checked)
	}
}

// TestOracleFixtureIsExercised guards H-8's second clause: a distinguishing
// fixture nobody fires proves nothing. HA must actually hold traces, including
// errored ones, before any trace assertion below can fail for the right reason.
func TestOracleFixtureIsExercised(t *testing.T) {
	inst, _ := getOracleHA(t)

	counts := oracleTraceItemIDs(t, inst, "automation")
	if len(counts) == 0 {
		t.Fatal("H-8: HA holds no automation traces; the fixture was never exercised")
	}
	for _, want := range []string{"cfgid_boost_charge", "cfgid_missing_service", "cfgid_blocked_cond"} {
		if counts[want] == 0 {
			t.Errorf("H-8: no traces for %s; expected the rig to have fired it (got %v)", want, counts)
		}
	}
	errored := oracleErroredTraceItemIDs(t, inst, "automation")
	if len(errored) == 0 {
		t.Fatal("H-8: no errored automation traces exist; --failing cannot be tested")
	}
}

// TestAutoShowFindsTracesForDivergentConfigID is the direct R1 regression test.
//
// HA's own trace/list is the oracle: whatever item_ids it reports traces for,
// `auto show` must be able to show them — addressed by either identifier.
func TestAutoShowFindsTracesForDivergentConfigID(t *testing.T) {
	inst, _ := getOracleHA(t)

	haCounts := oracleTraceItemIDs(t, inst, "automation")
	if haCounts["cfgid_boost_charge"] == 0 {
		t.Fatal("precondition: HA has no traces for cfgid_boost_charge")
	}

	// Both spellings must work: the config id HA keys traces by, and the
	// entity object_id a user reads off `auto ls`.
	for _, ref := range []string{"cfgid_boost_charge", "oracle_boost_charge"} {
		t.Run(ref, func(t *testing.T) {
			out := runHactlDir(t, inst.Dir(), "auto", "show", ref)
			if strings.Contains(out, "traces: none") {
				t.Errorf("auto show %s reported 'traces: none', but HA holds %d traces "+
					"for config id cfgid_boost_charge (entity automation.oracle_boost_charge).\n"+
					"Full output:\n%s", ref, haCounts["cfgid_boost_charge"], out)
			}
			if !strings.Contains(out, "trc:") {
				t.Errorf("auto show %s minted no trc: stable ID, so `trace show` has nothing "+
					"to resolve.\nFull output:\n%s", ref, out)
			}
		})
	}
}

// TestAutoLsFailingMatchesHA asserts --failing against HA's own notion of an
// errored trace, and — critically — also asserts the exclusion: an automation
// whose condition merely blocked the action did NOT fail.
func TestAutoLsFailingMatchesHA(t *testing.T) {
	inst, _ := getOracleHA(t)

	erroredItemIDs := oracleErroredTraceItemIDs(t, inst, "automation")
	if len(erroredItemIDs) == 0 {
		t.Fatal("precondition: HA reports no errored automation traces")
	}

	out := runHactlDir(t, inst.Dir(), "auto", "ls", "--failing", "--top", "100", "--tokensmax=0")

	// Map HA's config ids to the entity object_ids hactl's `id` column shows.
	wantObjectIDs := map[string]string{
		"cfgid_missing_service": "oracle_missing_service",
		"cfgid_bad_template":    "oracle_bad_template",
	}
	for _, itemID := range erroredItemIDs {
		obj, known := wantObjectIDs[itemID]
		if !known {
			continue
		}
		if !strings.Contains(out, itemID) && !strings.Contains(out, obj) {
			t.Errorf("auto ls --failing omitted %s (HA has an errored trace for it).\nFull output:\n%s",
				itemID, out)
		}
	}
	// Exclusion: a blocked condition is not a failure.
	if strings.Contains(out, "oracle_blocked_cond") || strings.Contains(out, "cfgid_blocked_cond") {
		t.Errorf("auto ls --failing wrongly included the condition-blocked automation "+
			"(its run has no error).\nFull output:\n%s", out)
	}
}

// TestAutoLsErrorCountsMatchHA checks the errors column against HA's traces.
func TestAutoLsErrorCountsMatchHA(t *testing.T) {
	inst, _ := getOracleHA(t)

	erroredItemIDs := oracleErroredTraceItemIDs(t, inst, "automation")
	if len(erroredItemIDs) == 0 {
		t.Fatal("precondition: HA reports no errored automation traces")
	}

	raw := runHactlDir(t, inst.Dir(), "auto", "ls", "--json", "--top", "100")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("auto ls --json did not parse: %v\noutput:\n%s", err, raw)
	}

	total := 0
	for _, r := range rows {
		switch v := r["errors"].(type) {
		case string:
			if v != "" && v != "0" {
				total++
			}
		case float64:
			if v > 0 {
				total++
			}
		}
	}
	if total == 0 {
		t.Errorf("auto ls reports a zero errors column for every automation, but HA has "+
			"errored traces for %v.\nJSON output:\n%s", erroredItemIDs, raw)
	}
}

// TestTraceShowResolvesByEntityIDForm covers the trace.go half of R1: the
// command's own usage string documents `<domain>.<item_id>/<run_id>`, and a
// user reading `auto ls` only ever sees the entity object_id.
func TestTraceShowResolvesByEntityIDForm(t *testing.T) {
	inst, _ := getOracleHA(t)

	ws := oracleWS(t, inst)
	res, err := ws.TraceList(context.Background(), "automation")
	if err != nil {
		t.Fatalf("trace/list: %v", err)
	}
	var runID string
	for key, list := range res {
		if strings.HasSuffix(key, ".cfgid_boost_charge") && len(list) > 0 {
			runID = list[0].RunID
		}
	}
	if runID == "" {
		t.Fatal("precondition: no run_id for cfgid_boost_charge")
	}

	// The object_id spelling is what a user can actually discover from the CLI.
	ref := "automation.oracle_boost_charge/" + runID
	out, err := runHactlDirErr(t, inst.Dir(), "trace", "show", ref)
	if err != nil {
		t.Errorf("trace show %s failed: %v\noutput:\n%s\n"+
			"HA holds this run under config id cfgid_boost_charge; the object_id form "+
			"is the only spelling the CLI surfaces.", ref, err, out)
	}
}

// TestScriptTracesStillWork is the control group. Scripts have no id/entity-id
// split, so a fix aimed at automations must not regress them — and a fix that
// only works here proves nothing.
func TestScriptTracesStillWork(t *testing.T) {
	inst, _ := getOracleHA(t)

	counts := oracleTraceItemIDs(t, inst, "script")
	if counts["oracle_script_broken"] == 0 {
		t.Fatal("precondition: HA has no traces for script oracle_script_broken")
	}
	out := runHactlDir(t, inst.Dir(), "script", "show", "oracle_script_broken")
	if strings.Contains(out, "traces: none") {
		t.Errorf("script show reported 'traces: none' but HA holds %d traces.\noutput:\n%s",
			counts["oracle_script_broken"], out)
	}
}
