//go:build integration

package integration

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// H-18 — `runs_24h` counts runs, and it counts the same runs `auto show` lists.
//
// A RUN is a trigger whose conditions passed: the automation entered its
// actions. Home Assistant fires EVENT_AUTOMATION_TRIGGERED — the logbook's only
// record of an automation — at exactly that moment, after the conditions are
// evaluated and before the action script starts. A trigger the conditions
// blocked is therefore traced (`script_execution: failed_conditions`) but never
// logged, and it is not a run.
//
// Everything below derives that number twice from HA (its logbook and its
// traces) and requires hactl's column to equal both. Nothing here is computed
// by re-running hactl's own counting rule.
// ============================================================================

const (
	// The mixed automation: three condition-blocked triggers and two real runs
	// in the same window (testdata/fixtures/oracle/automations.yaml).
	oracleGatedConfigID = "cfgid_gated_charge"
	// The all-blocked automation: every trigger stopped at the condition.
	oracleBlockedConfigID = "cfgid_blocked_cond"
	oracleBlockedEntityID = "automation.oracle_blocked_cond"

	// HA's own word for a run its conditions stopped
	// (homeassistant/components/automation/__init__.py: script_execution_set).
	haFailedConditions = "failed_conditions"

	// `auto show` renders the last 5 traces, so a reconciliation against its
	// table is only exact while HA holds no more than that.
	autoShowTraceLimit = 5
)

// autoLsRuns24h returns hactl's runs_24h column per automation object id.
// Table listings serialize the rendered row, so the values arrive as strings.
func autoLsRuns24h(t *testing.T, dir string) map[string]int {
	t.Helper()
	raw := runHactlDir(t, dir, "auto", "ls", "--json", "--top", "1000")
	var rows []map[string]string
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("auto ls --json did not parse: %v\noutput:\n%s", err, raw)
	}
	out := map[string]int{}
	for _, r := range rows {
		n, err := strconv.Atoi(r["runs_24h"])
		if err != nil {
			t.Fatalf("auto ls reported a non-numeric runs_24h %q for %q", r["runs_24h"], r["id"])
		}
		out[r["id"]] = n
	}
	if len(out) == 0 {
		t.Fatal("precondition: auto ls returned no automations")
	}
	return out
}

// haRunsAndBlocked splits the traces HA holds for one automation inside the
// window into runs and condition-blocked triggers, using HA's own
// script_execution word.
func haRunsAndBlocked(t *testing.T, inst *hatest.Instance, configID string, since time.Duration) (runs, blocked, total int) {
	t.Helper()
	cutoff := time.Now().Add(-since)
	for _, tr := range oracleTracesFor(t, inst, "automation", configID) {
		started, err := time.Parse(time.RFC3339Nano, tr.Timestamp.Start)
		if err != nil || !started.After(cutoff) {
			continue
		}
		total++
		if tr.Execution == haFailedConditions {
			blocked++
		} else {
			runs++
		}
	}
	return runs, blocked, total
}

// TestOracleGatedFixtureHasBothOutcomes guards the fixture (TC-4). An automation
// whose runs are all blocked, and one whose runs all executed, are both
// satisfied by "count every trace" — only an automation with BOTH in one window
// can see a counting rule at all. If the rig ever stops producing the mix, this
// fails first and says so, instead of leaving the tests below vacuously green.
func TestOracleGatedFixtureHasBothOutcomes(t *testing.T) {
	inst, _ := getOracleHA(t)

	runs, blocked, total := haRunsAndBlocked(t, inst, oracleGatedConfigID, 24*time.Hour)
	if runs == 0 || blocked == 0 {
		t.Fatalf("H-18 fixture lapsed: HA holds %d traces for %s — %d executed, %d blocked by "+
			"condition. Both must be non-zero, or runs_24h cannot be distinguished from a raw "+
			"trace count.", total, oracleGatedConfigID, runs, blocked)
	}
	if total > autoShowTraceLimit {
		t.Fatalf("H-18 fixture lapsed: HA holds %d traces for %s, more than the %d `auto show` "+
			"renders — the cross-command reconciliation below can no longer be exact",
			total, oracleGatedConfigID, autoShowTraceLimit)
	}

	// The all-blocked automation is the other end of the axis.
	runs, blocked, total = haRunsAndBlocked(t, inst, oracleBlockedConfigID, 24*time.Hour)
	if blocked == 0 || runs != 0 {
		t.Fatalf("H-18 fixture lapsed: %s should be blocked on every trigger; HA holds %d traces "+
			"— %d executed, %d blocked", oracleBlockedConfigID, total, runs, blocked)
	}
}

// TestRuns24hMatchesHAsOwnRunCount is the H-18 gate, applied to every automation
// the instance holds.
//
// It reconciles three numbers: HA's logbook (one entry per run), HA's traces
// (every trigger, carrying the outcome), and hactl's runs_24h column. The first
// two are independent HA sources — their agreement is what establishes the
// definition of a run, so the expectation is never hactl's own model of it.
func TestRuns24hMatchesHAsOwnRunCount(t *testing.T) {
	inst, _ := getOracleHA(t)
	const window = 24 * time.Hour

	got := autoLsRuns24h(t, inst.Dir())
	configIDs := oracleAutomationConfigIDs(t, inst)

	checkedBlocked := 0
	for entityID, configID := range configIDs {
		objectID := strings.TrimPrefix(entityID, "automation.")
		runs, blocked, total := haRunsAndBlocked(t, inst, configID, window)
		logbook := oracleLogbookRunCount(t, inst, entityID, window)
		if blocked > 0 {
			checkedBlocked++
		}

		// HA against HA: the logbook and the traces must describe the same
		// number of runs. If this fails, HA's own semantics moved and the rule
		// below has to be re-derived — not patched.
		if runs != logbook {
			t.Errorf("%s: HA's traces report %d runs (%d of %d triggers blocked by condition) "+
				"but HA's logbook reports %d. The two HA sources disagree, so the definition "+
				"of a run that H-18 pins no longer holds.",
				entityID, runs, blocked, total, logbook)
			continue
		}

		if got[objectID] != runs {
			t.Errorf("auto ls runs_24h = %d for %s, want %d.\n"+
				"HA holds %d traces for config id %s: %d executed, %d stopped at the condition "+
				"(script_execution=%s), and HA's logbook records %d runs. A trigger the "+
				"conditions blocked is not a run.",
				got[objectID], entityID, runs, total, configID, runs, blocked,
				haFailedConditions, logbook)
		}
	}
	if checkedBlocked == 0 {
		t.Fatal("precondition: no automation in the instance has a condition-blocked trigger; " +
			"this test cannot distinguish counting runs from counting traces")
	}
}

// TestRuns24hIsZeroWhenEveryTriggerWasBlocked is the direct D65 regression.
//
// Every trigger of cfgid_blocked_cond stops at its condition, so HA's logbook
// holds nothing for it and the count falls through to the automation's stored
// traces. That fallback counted every trigger, so the column reported runs for
// an automation that never ran — while for any automation with at least one real
// run it reported the logbook's number, which excludes blocked triggers. One
// column, two meanings, depending on the data.
func TestRuns24hIsZeroWhenEveryTriggerWasBlocked(t *testing.T) {
	inst, _ := getOracleHA(t)
	const window = 24 * time.Hour

	runs, blocked, total := haRunsAndBlocked(t, inst, oracleBlockedConfigID, window)
	if blocked == 0 {
		t.Fatalf("precondition: HA holds no condition-blocked trace for %s (%d traces total)",
			oracleBlockedConfigID, total)
	}
	logbook := oracleLogbookRunCount(t, inst, oracleBlockedEntityID, window)
	if logbook != 0 {
		t.Fatalf("precondition: HA's logbook records %d runs for %s; it should record none",
			logbook, oracleBlockedEntityID)
	}

	got := autoLsRuns24h(t, inst.Dir())
	objectID := strings.TrimPrefix(oracleBlockedEntityID, "automation.")
	if got[objectID] != 0 {
		t.Errorf("auto ls runs_24h = %d for %s, want 0 — HA traced %d triggers for it and "+
			"stopped every one of them at the condition (%d executed), and HA's logbook "+
			"records no run at all.",
			got[objectID], oracleBlockedEntityID, total, runs)
	}
}

// autoShowTraceResults returns the outcome word `auto show` prints for each row
// of its trace table.
func autoShowTraceResults(t *testing.T, dir, ref string) []string {
	t.Helper()
	raw := runHactlDir(t, dir, "auto", "show", ref, "--json")
	var show struct {
		EntityID string `json:"entity_id"`
		ConfigID string `json:"config_id"`
		Traces   []struct {
			Result string `json:"result"`
		} `json:"traces"`
	}
	if err := json.Unmarshal([]byte(raw), &show); err != nil {
		t.Fatalf("auto show %s --json did not parse: %v\noutput:\n%s", ref, err, raw)
	}
	out := make([]string, 0, len(show.Traces))
	for _, tr := range show.Traces {
		out = append(out, tr.Result)
	}
	return out
}

// TestAutoShowTraceTableReconcilesWithRuns24h is the cross-command half of H-18
// (H-11 class): the same automation must not report two different truths in two
// commands.
//
// `auto show` lists every trigger HA traced; `auto ls` counts the runs. The two
// numbers legitimately differ — and the difference must be exactly the rows
// `auto show` itself marks failed_conditions, with nothing else unaccounted for.
//
// Applied to every automation whose whole trace history still fits in the table
// `auto show` renders, so the reconciliation is exact rather than approximate.
func TestAutoShowTraceTableReconcilesWithRuns24h(t *testing.T) {
	inst, _ := getOracleHA(t)
	const window = 24 * time.Hour

	runs24h := autoLsRuns24h(t, inst.Dir())
	configIDs := oracleAutomationConfigIDs(t, inst)

	checkedMixed, checkedAllBlocked := 0, 0
	for entityID, configID := range configIDs {
		haRuns, haBlocked, haTotal := haRunsAndBlocked(t, inst, configID, window)
		if haTotal == 0 || haTotal > autoShowTraceLimit {
			// Nothing traced, or more traces than the table shows — the
			// comparison would be against a different population.
			continue
		}
		switch {
		case haRuns > 0 && haBlocked > 0:
			checkedMixed++
		case haBlocked > 0:
			checkedAllBlocked++
		}

		t.Run(strings.TrimPrefix(entityID, "automation."), func(t *testing.T) {
			results := autoShowTraceResults(t, inst.Dir(), configID)
			if len(results) != haTotal {
				t.Fatalf("auto show lists %d traces for %s but HA holds %d in the window; the "+
					"reconciliation would compare different populations", len(results), configID, haTotal)
			}
			shownRuns, shownBlocked := 0, 0
			for _, r := range results {
				if r == haFailedConditions {
					shownBlocked++
				} else {
					shownRuns++
				}
			}
			if shownBlocked != haBlocked {
				t.Errorf("`auto show` marks %d of %s's traces %s, HA marks %d — the table's own "+
					"outcome column is what makes the count reconcilable, so it must be HA's word.",
					shownBlocked, configID, haFailedConditions, haBlocked)
			}
			got := runs24h[strings.TrimPrefix(entityID, "automation.")]
			if got != shownRuns {
				t.Errorf("`auto ls` reports runs_24h=%d for %s while `auto show` lists %d trace "+
					"rows of which %d ran and %d were stopped at the condition (%v). The same "+
					"automation reports two different truths in two commands.",
					got, entityID, len(results), shownRuns, shownBlocked, results)
			}
		})
	}

	// TC-4: an automation whose traces all ran, or all blocked, is satisfied by
	// either counting rule. Only the two cases below can see the difference.
	if checkedMixed == 0 {
		t.Error("precondition: no automation had both a real run and a condition-blocked " +
			"trigger in one window; counting runs and counting triggers cannot be told apart")
	}
	if checkedAllBlocked == 0 {
		t.Error("precondition: no automation had every trigger blocked; the case where the " +
			"logbook has nothing to say about an automation is not exercised")
	}
}
