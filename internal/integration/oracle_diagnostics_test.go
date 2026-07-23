//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// H-11 — hactl never invents an identifier, and every count it reports
// reconciles with the count its source reported.
//
// These tests target the five read-surface defects fixed alongside this
// file: log --component matching only a truncated logger name (#1), --unique
// dropping HA's own pre-aggregated counts (#2), ent who/changes attributing
// automation/script/device-caused changes to the propagated user instead of
// the proximate cause (#3), cc ls fabricating custom components from
// built-in update.* entities (#4), and log show resolving IDs from a foreign
// namespace (#5). Each assertion is checked against what HA itself reports
// (invariant H-9), never a hand-picked expectation.
// ============================================================================

// --- defect #1: log --component must match the FULL logger name -----------

// TestLogComponentFilterMatchesHA checks --component against HA's own logger
// names (oracleLogNames). Before the fix, systemLogToEntries truncated the
// component to its last dot-segment before filtering ever ran, so
// `--component automation` matched nothing even though HA held automation
// error entries — only a filter value that happened to equal the logger's
// last segment (e.g. "zha") ever worked.
func TestLogComponentFilterMatchesHA(t *testing.T) {
	inst, _ := getOracleHA(t)

	haNames := oracleLogNames(t, inst)
	var matching []string
	for name := range haNames {
		if strings.Contains(strings.ToLower(name), "automation") {
			matching = append(matching, name)
		}
	}
	if len(matching) == 0 {
		t.Fatal("precondition: HA holds no logger names containing \"automation\"; " +
			"the oracle rig's cfgid_missing_service/cfgid_bad_template automations should have logged errors")
	}

	raw := runHactlDir(t, inst.Dir(), "log", "--component", "automation", "--top", "1000", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("log --component automation --json did not parse: %v\noutput:\n%s", err, raw)
	}
	if len(rows) == 0 {
		t.Errorf("log --component automation returned no rows, but HA reports %d matching logger name(s): %v",
			len(matching), matching)
	}

	// Negative control: a component substring that cannot match anything.
	rawNone := runHactlDir(t, inst.Dir(), "log", "--component", "zzz_definitely_not_a_real_component", "--top", "1000", "--json")
	var rowsNone []map[string]any
	if err := json.Unmarshal([]byte(rawNone), &rowsNone); err != nil {
		t.Fatalf("log --component <absent> --json did not parse: %v\noutput:\n%s", err, rawNone)
	}
	if len(rowsNone) != 0 {
		t.Errorf("log --component <absent> returned %d rows, want 0:\n%s", len(rowsNone), rawNone)
	}
}

// --- defect #2: log --unique must sum HA's own counts, sorted descending --

// oracleErrorLogTotal sums Count across every ERROR-level system_log record,
// directly from HA's own system_log/list — the ground truth for defect #2.
func oracleErrorLogTotal(t *testing.T, inst *hatest.Instance) int {
	t.Helper()
	ws := oracleWS(t, inst)
	entries, err := ws.SystemLogList(context.Background())
	if err != nil {
		t.Fatalf("oracle system_log/list: %v", err)
	}
	total := 0
	for _, e := range entries {
		if !strings.EqualFold(e.Level, "ERROR") {
			continue
		}
		c := e.Count
		if c <= 0 {
			c = 1
		}
		total += c
	}
	return total
}

func TestLogUniqueCountsReconcileWithHA(t *testing.T) {
	inst, _ := getOracleHA(t)

	haTotal := oracleErrorLogTotal(t, inst)
	if haTotal == 0 {
		t.Fatal("precondition: HA reports no ERROR-level system_log entries")
	}

	raw := runHactlDir(t, inst.Dir(), "log", "--errors", "--unique", "--top", "1000", "--json")
	var rows []struct {
		Count string `json:"count"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("log --errors --unique --json did not parse: %v\noutput:\n%s", err, raw)
	}
	if len(rows) == 0 {
		t.Fatalf("log --errors --unique returned no rows, but HA reports a total ERROR count of %d", haTotal)
	}

	hactlTotal := 0
	prev := -1
	for i, r := range rows {
		n, convErr := strconv.Atoi(r.Count)
		if convErr != nil {
			t.Fatalf("row %d count %q is not an integer: %v", i, r.Count, convErr)
		}
		if prev >= 0 && n > prev {
			t.Errorf("log --unique not sorted descending by count: row %d has count %d after row %d's count %d",
				i, n, i-1, prev)
		}
		prev = n
		hactlTotal += n
	}

	// This is the direct defect #2 regression: before the fix, DeduplicateLogs
	// counted merged RECORDS (g.Count++) instead of summing each record's own
	// HA-reported Count, so a message HA reports with count=3 showed as 1.
	if hactlTotal != haTotal {
		t.Errorf("log --errors --unique total count = %d, want %d (HA's own summed ERROR count from system_log/list)",
			hactlTotal, haTotal)
	}
}

// --- defect #3: ent who must attribute to the proximate cause, not the ----
// --- propagated user id -----------------------------------------------------

// TestEntWhoAttributesAutomationOverPropagatedUser is the direct regression
// for defect #3. The oracle rig's cfgid_boost_charge automation is fired by a
// real state trigger from a REST-authenticated toggle, so the resulting
// input_number.oracle_level change carries BOTH context_user_id (the
// long-lived token's owning user, propagated from the trigger) AND
// context_event_type=automation_triggered + context_name (the automation
// that actually made the change) — exactly the both-fields-present case the
// old rule order got backwards.
func TestEntWhoAttributesAutomationOverPropagatedUser(t *testing.T) {
	inst, _ := getOracleHA(t)

	raw := runHactlDir(t, inst.Dir(), "ent", "who", "input_number.oracle_level",
		"--since", "24h", "--top", "1000", "--json")

	var got struct {
		Events []struct {
			ChangedBy        string `json:"changed_by"`
			ContextEventType string `json:"context_event_type"`
			ContextUserID    string `json:"context_user_id"`
			ContextName      string `json:"context_name"`
		} `json:"events"`
	}
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatalf("ent who --json did not parse: %v\noutput:\n%s", err, raw)
	}
	if len(got.Events) == 0 {
		t.Fatal("precondition: ent who reports no events for input_number.oracle_level; " +
			"the oracle rig should have fired cfgid_boost_charge against it")
	}

	autoEvents := 0
	bothFieldsCase := false
	for _, e := range got.Events {
		if e.ContextEventType != "automation_triggered" {
			continue
		}
		autoEvents++
		if e.ContextUserID != "" {
			bothFieldsCase = true
		}
		if !strings.HasPrefix(e.ChangedBy, "Automation:") {
			t.Errorf("event with context_event_type=automation_triggered (context_name=%q, context_user_id=%q) "+
				"labeled %q, want prefix 'Automation:'", e.ContextName, e.ContextUserID, e.ChangedBy)
		}
	}
	if autoEvents == 0 {
		t.Fatal("precondition: no automation_triggered events found for input_number.oracle_level; " +
			"cannot test the propagated-user-id precedence defect")
	}
	if !bothFieldsCase {
		t.Fatal("precondition: no automation_triggered event also carried a context_user_id " +
			"(the both-fields-present case); HA's context propagation for cfgid_boost_charge did not " +
			"produce what this test needs")
	}
}

// --- defect #4: cc ls/show must never invent a custom component -----------

// TestCCLsMatchesHACustomIntegrations set-equals cc ls against HA's own
// manifest/list (is_built_in=false) and separately confirms none of HA's own
// update.* entities leak into the output — the exact fabrication mechanism
// defect #4 fixed. The oracle fixture enables `demo:`, which ships update.*
// entities shaped exactly like a HACS component update, and ships zero
// genuinely custom integrations, so the correct answer is the empty set.
func TestCCLsMatchesHACustomIntegrations(t *testing.T) {
	inst, _ := getOracleHA(t)
	want := oracleCustomIntegrations(t, inst)

	raw := runHactlDir(t, inst.Dir(), "cc", "ls", "--top", "1000", "--json")
	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("cc ls --json did not parse: %v\noutput:\n%s", err, raw)
	}
	got := make([]string, 0, len(rows))
	for _, r := range rows {
		if v, ok := r["domain"].(string); ok {
			got = append(got, v)
		}
	}
	assertSameSet(t, "cc ls domains", want, got)

	// Cross-check against HA's own update.* entities directly: none of their
	// derived domains may appear in cc ls unless oracleCustomIntegrations also
	// named them (it doesn't, here — demo is entirely built-in).
	client := haapi.New(inst.URL(), inst.Token())
	statesRaw, err := client.GetStates(context.Background())
	if err != nil {
		t.Fatalf("get states: %v", err)
	}
	var states []struct {
		EntityID string `json:"entity_id"`
	}
	if err := json.Unmarshal(statesRaw, &states); err != nil {
		t.Fatalf("decode states: %v", err)
	}
	checkedPhantom := false
	for _, s := range states {
		if !strings.HasPrefix(s.EntityID, "update.") {
			continue
		}
		checkedPhantom = true
		phantomDomain := strings.TrimPrefix(s.EntityID, "update.")
		if slicesContains(got, phantomDomain) && !slicesContains(want, phantomDomain) {
			t.Errorf("cc ls invented %q as a custom component from %s (a built-in integration's update entity, "+
				"per HA's own manifest/list)", phantomDomain, s.EntityID)
		}
	}
	if !checkedPhantom {
		t.Fatal("precondition: HA reports no update.* entities; the demo integration's fabrication trigger is absent")
	}
}

// TestCCShowRejectsBuiltInDomain is the `cc show` half of defect #4: even a
// domain that genuinely exists as an integration must be refused if HA's own
// manifest/list marks it built-in.
func TestCCShowRejectsBuiltInDomain(t *testing.T) {
	inst, _ := getOracleHA(t)

	ws := oracleWS(t, inst)
	manifests, err := ws.IntegrationManifestList(context.Background())
	if err != nil {
		t.Fatalf("oracle manifest/list: %v", err)
	}
	var builtIn string
	for _, m := range manifests {
		if m.IsBuiltIn {
			builtIn = m.Domain
			break
		}
	}
	if builtIn == "" {
		t.Fatal("precondition: HA reports no built-in integrations (unexpected)")
	}

	_, err = runHactlDirErr(t, inst.Dir(), "cc", "show", builtIn, "--json")
	if err == nil {
		t.Errorf("cc show %s (a built-in integration per HA's own manifest/list) should have errored, but succeeded", builtIn)
	}
}

// --- defect #5: log show must reject a foreign-namespace ID, and --json ---
// --- must produce parseable output on both cc show and log show -----------

// stableIDPattern extracts a "<prefix>:<hash>" stable ID from hactl's text
// output (e.g. "trc:a7").
var stableIDPattern = regexp.MustCompile(`\b([a-z]+):([0-9a-f]{2,})\b`)

func extractStableID(t *testing.T, out, prefix string) string {
	t.Helper()
	for _, m := range stableIDPattern.FindAllStringSubmatch(out, -1) {
		if m[1] == prefix {
			return m[1] + ":" + m[2]
		}
	}
	t.Fatalf("no %s: stable ID found in output:\n%s", prefix, out)
	return ""
}

// TestLogShowRejectsForeignNamespaceID mints a genuine trc: ID (from `auto
// show`, a command owned elsewhere but usable here as a fixture) and checks
// `log show` refuses it. pkg/ids.Registry stores every prefix's short IDs in
// one flat reverse map, so without an explicit "log:" prefix check, `log
// show` would resolve a trc: (or anom:) key too — and for anom: keys, whose
// shape ("entity_id|type|start_time") happens to match a log key's own
// pipe-delimited 3-part shape, print fabricated timestamp/component/message
// fields lifted from an unrelated record.
func TestLogShowRejectsForeignNamespaceID(t *testing.T) {
	inst, _ := getOracleHA(t)

	haCounts := oracleTraceItemIDs(t, inst, "automation")
	if haCounts["cfgid_boost_charge"] == 0 {
		t.Fatal("precondition: HA has no traces for cfgid_boost_charge")
	}

	out := runHactlDir(t, inst.Dir(), "auto", "show", "cfgid_boost_charge")
	trcID := extractStableID(t, out, "trc")

	showOut, err := runHactlDirErr(t, inst.Dir(), "log", "show", trcID)
	if err == nil {
		t.Errorf("log show %s (a trc: ID) should have been rejected, but succeeded:\n%s", trcID, showOut)
	}
}

// TestLogShowJSONParses mints a real log: ID from a genuine oracle error log
// entry and round-trips it through `log show --json`.
func TestLogShowJSONParses(t *testing.T) {
	inst, _ := getOracleHA(t)

	raw := runHactlDir(t, inst.Dir(), "log", "--errors", "--top", "5", "--json")
	var rows []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("log --errors --json did not parse: %v\noutput:\n%s", err, raw)
	}
	if len(rows) == 0 {
		t.Fatal("precondition: log --errors returned no rows to mint an ID from")
	}
	id := rows[0].ID
	if id == "" {
		t.Fatal("precondition: first log row has no id")
	}

	showRaw := runHactlDir(t, inst.Dir(), "log", "show", id, "--json")
	var shown map[string]any
	if err := json.Unmarshal([]byte(showRaw), &shown); err != nil {
		t.Fatalf("log show %s --json did not parse: %v\noutput:\n%s", id, err, showRaw)
	}
	if shown["id"] != id {
		t.Errorf("log show --json id = %v, want %q", shown["id"], id)
	}
}

// slicesContains is a tiny local helper.
func slicesContains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
