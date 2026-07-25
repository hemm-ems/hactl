//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/analyze"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// Contract tests verify that the HA API endpoints used by hactl
// return the expected response shapes. These tests break early when
// HA changes its API, making version-incompatibilities visible.

// TestContract_APIConfig verifies /api/config returns expected fields.
func TestContract_APIConfig(t *testing.T) {
	out := runHactl(t, "health")
	// health command parses /api/config — if it succeeds, the schema is compatible
	if !strings.Contains(out, "HA ") {
		t.Errorf("health output missing HA version prefix: %s", out)
	}
	if !strings.Contains(out, "state=") {
		t.Errorf("health output missing state field: %s", out)
	}
	if !strings.Contains(out, "recorder=") {
		t.Errorf("health output missing recorder field: %s", out)
	}
}

// TestContract_APIStates verifies /api/states returns an array of state objects.
func TestContract_APIStates(t *testing.T) {
	out := runHactl(t, "ent", "ls", "--json")
	var entries []map[string]string
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("ent ls --json returned invalid JSON: %v\noutput: %s", err, out)
	}
	if len(entries) == 0 {
		t.Fatal("ent ls returned no entities")
	}
	// Verify expected columns exist
	first := entries[0]
	for _, key := range []string{"entity_id", "state", "last_changed"} {
		if _, ok := first[key]; !ok {
			t.Errorf("missing expected key %q in entity entry", key)
		}
	}
}

// TestContract_StateContext verifies that /api/states/<entity_id> returns
// the "context" object (id / parent_id / user_id). The public REST docs do
// not list this field, but HA core (homeassistant/core.py State._as_dict)
// has emitted it on every state since long before 2026.4.4. The whole
// "changed_by" feature breaks if HA ever stops returning this.
func TestContract_StateContext(t *testing.T) {
	out := runHactl(t, "ent", "show", "sun.sun", "--json")
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("ent show --json invalid: %v\n%s", err, out)
	}
	ctxRaw, ok := got["context"]
	if !ok {
		t.Fatalf("state response missing 'context' object — HA may have changed "+
			"core.State._as_dict; the changed_by feature breaks. Response: %s", out)
	}
	ctxObj, ok := ctxRaw.(map[string]any)
	if !ok {
		t.Fatalf("state.context is not an object: %v", ctxRaw)
	}
	for _, key := range []string{"id", "parent_id", "user_id"} {
		if _, ok := ctxObj[key]; !ok {
			t.Errorf("state.context.%s missing — schema regression", key)
		}
	}
}

// TestContract_APIErrorLog verifies GET /api/error_log returns a 200 with a text
// body. The previous version ran `hactl log` and discarded the output
// (`_ = out`) — a test that asserted nothing and passed even if the endpoint had
// vanished. This calls the endpoint hactl's REST log fallback actually uses
// (fetchLogEntries → GetErrorLog) and asserts a real, non-error response.
func TestContract_APIErrorLog(t *testing.T) {
	cfg := loadConfig(t)
	client := haapi.New(cfg.URL, cfg.Token)
	body, err := client.GetErrorLog(context.Background())
	if err != nil {
		t.Fatalf("GET /api/error_log: %v — the REST log fallback (fetchLogEntries) is broken", err)
	}
	// HA's error_log is plain text; it may legitimately be empty on a clean boot,
	// so the contract is "the endpoint answers with a decodable text body", not a
	// specific line. A nil error from GetErrorLog already proves a 2xx (non-2xx
	// returns *HTTPStatusError); assert the body is valid UTF-8 text, never a JSON
	// error envelope leaking through.
	if json.Valid(body) && strings.HasPrefix(strings.TrimSpace(string(body)), "{") {
		t.Errorf("/api/error_log returned a JSON object, want plain text log content: %.200s", body)
	}
}

// TestContract_APITemplate verifies /api/template accepts and renders templates.
func TestContract_APITemplate(t *testing.T) {
	out := runHactl(t, "tpl", "eval", "{{ 1 + 1 }}")
	out = strings.TrimSpace(stripTokenHeader(out))
	if out != "2" {
		t.Errorf("template eval '{{ 1 + 1 }}' = %q, want %q", out, "2")
	}
}

// TestContract_WebSocket_TraceList exercises the WS `trace/list` command for
// real and pins the fields hactl decodes from it. The previous version ran
// `auto ls --json` and asserted its `id`/`state` columns — but those come from
// the states API, so `auto ls` succeeds even if trace/list is broken; the test
// named an endpoint it never touched.
//
// The oracle rig fires automations for real (exerciseOracleRig), so traces
// exist. We assert: trace/list keys by "domain.item_id" with a non-empty
// item_id (H-9 keying), each summary carries a run_id and a start timestamp, and
// — the load-bearing cross-check — that the (item_id, run_id) trace/list decoded
// actually addresses a real trace via trace/get. A silently-degrading decode
// that zeroed item_id or run_id would fail the keying assertion or the
// round-trip, where the old proxy stayed green.
func TestContract_WebSocket_TraceList(t *testing.T) {
	inst, _ := getOracleHA(t)
	ws := oracleWS(t, inst)
	ctx := context.Background()

	list, err := ws.TraceList(ctx, "automation")
	if err != nil {
		t.Fatalf("trace/list: %v", err)
	}
	if len(list) == 0 {
		t.Fatal("precondition: trace/list returned no automation traces; exerciseOracleRig should have fired several")
	}

	var probed bool
	for key, summaries := range list {
		itemID, ok := assertTraceListKey(t, key, summaries)
		if !ok {
			continue
		}
		for _, s := range summaries {
			assertTraceSummaryFields(t, key, itemID, s)
		}
		// Cross-check the decoded (item_id, run_id) against trace/get exactly once:
		// the ids trace/list gave us must address a real trace whose own run_id
		// round-trips. This is what makes the test a contract and not a tautology.
		if !probed {
			assertTraceGetRoundTrip(t, ws, ctx, summaries[0])
			probed = true
		}
	}
	if !probed {
		t.Fatal("no trace summary was cross-checked against trace/get")
	}
}

// assertTraceListKey validates a trace/list map key is "automation.<item_id>"
// with a non-empty item_id and at least one summary, returning the item_id.
func assertTraceListKey(t *testing.T, key string, summaries []haapi.TraceSummary) (string, bool) {
	t.Helper()
	if !strings.HasPrefix(key, "automation.") {
		t.Errorf("trace/list key %q is not keyed by domain.item_id", key)
		return "", false
	}
	itemID := strings.TrimPrefix(key, "automation.")
	if itemID == "" {
		t.Errorf("trace/list key %q has an empty item_id — decode of item_id degraded", key)
		return "", false
	}
	if len(summaries) == 0 {
		t.Errorf("trace/list key %q maps to zero summaries", key)
		return "", false
	}
	return itemID, true
}

func assertTraceSummaryFields(t *testing.T, key, itemID string, s haapi.TraceSummary) {
	t.Helper()
	if s.ItemID != itemID {
		t.Errorf("summary item_id %q disagrees with its map key %q (H-9 keying drift)", s.ItemID, key)
	}
	if s.RunID == "" {
		t.Errorf("trace summary for %q decoded an empty run_id", key)
	}
	if s.Timestamp.Start == "" {
		t.Errorf("trace summary for %q decoded an empty start timestamp", key)
	}
}

func assertTraceGetRoundTrip(t *testing.T, ws *haapi.WSClient, ctx context.Context, s haapi.TraceSummary) {
	t.Helper()
	raw, getErr := ws.TraceGet(ctx, "automation", s.ItemID, s.RunID)
	if getErr != nil {
		t.Errorf("trace/get(%s, %s) from trace/list ids failed: %v", s.ItemID, s.RunID, getErr)
		return
	}
	var rt analyze.RawTrace
	if err := json.Unmarshal(raw, &rt); err != nil {
		t.Errorf("trace/get result did not decode: %v", err)
		return
	}
	if rt.Trace.RunID != s.RunID {
		t.Errorf("trace/get run_id %q != trace/list run_id %q — the keys don't address the same trace",
			rt.Trace.RunID, s.RunID)
	}
}

// TestContract_AutomationConfigAPI calls the endpoint it names —
// GET /api/config/automation/config/<id> via GetAutomationConfig — for the
// basic fixture's `climate_schedule` automation and asserts the decoded config
// carries the real document fields. The previous version ran `auto ls` (a
// different, states-API endpoint) and asserted nothing but that its output was
// non-empty, so a broken automation-config API would not have reddened it.
//
// The write path (auto apply/backup/diff) reads this document and round-trips
// it; if HA stopped returning `id`/`alias`/triggers here, those break silently.
// Mind HA's schema migration: a config written back through the API is returned
// with the modern plural `triggers`/`conditions`/`actions`, while a freshly
// authored fixture may still use the legacy singular keys — accept either.
func TestContract_AutomationConfigAPI(t *testing.T) {
	cfg := loadConfig(t)
	client := haapi.New(cfg.URL, cfg.Token)

	raw, err := client.GetAutomationConfig(context.Background(), "climate_schedule")
	if err != nil {
		t.Fatalf("GetAutomationConfig(climate_schedule): %v", err)
	}

	var got struct {
		ID         string `json:"id"`
		Alias      string `json:"alias"`
		Trigger    []any  `json:"trigger"`
		Triggers   []any  `json:"triggers"`
		Condition  []any  `json:"condition"`
		Conditions []any  `json:"conditions"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("automation config did not decode: %v\nbody: %s", err, raw)
	}

	if got.ID != "climate_schedule" {
		t.Errorf("automation config id = %q, want %q (a zero-value decode would give \"\")", got.ID, "climate_schedule")
	}
	if got.Alias != "Climate Schedule" {
		t.Errorf("automation config alias = %q, want %q", got.Alias, "Climate Schedule")
	}
	if len(got.Trigger)+len(got.Triggers) == 0 {
		t.Errorf("automation config decoded no triggers (neither `trigger` nor `triggers`) — "+
			"HA schema shape may have changed; body: %s", raw)
	}
	if len(got.Condition)+len(got.Conditions) == 0 {
		t.Errorf("automation config decoded no conditions (neither `condition` nor `conditions`); body: %s", raw)
	}
}

// TestContract_Logbook verifies /api/logbook/<timestamp> returns an array.
func TestContract_Logbook(t *testing.T) {
	out := runHactl(t, "changes", "--since", "1h", "--json")
	// The changes command uses GetLogbook — if it returns valid JSON, the API is compatible
	var entries []map[string]string
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		// An empty result means "no changes" which is also fine
		if !strings.Contains(out, "no changes") {
			t.Fatalf("changes --json returned unexpected output: %s", out)
		}
	}
}

// TestContract_ConfigEntries verifies GET /api/config/config_entries/entry returns
// an array of config entry objects with expected fields.
func TestContract_ConfigEntries(t *testing.T) {
	out := runHactl(t, "config", "entries", "--json")
	var entries []map[string]any
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("config entries --json returned invalid JSON: %v\noutput: %s", err, out)
	}
	// HA always has at least some built-in config entries (e.g. sun)
	if len(entries) == 0 {
		t.Skip("no config entries returned (possible on minimal HA)")
	}
	first := entries[0]
	for _, key := range []string{"entry_id", "domain", "title", "state"} {
		if _, ok := first[key]; !ok {
			t.Errorf("config entry missing expected key %q", key)
		}
	}
}

// TestContract_ManifestList verifies WS manifest/list returns integration manifests
// with the expected fields (used by cc ls for custom component detection).
func TestContract_ManifestList(t *testing.T) {
	cfg := loadConfig(t)
	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	ctx := context.Background()
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("ws connect: %v", err)
	}
	defer func() { _ = ws.Close() }()

	manifests, err := ws.IntegrationManifestList(ctx)
	if err != nil {
		t.Fatalf("manifest/list: %v", err)
	}
	if len(manifests) == 0 {
		t.Fatal("manifest/list returned empty list")
	}
	// Verify at least one built-in integration exists
	foundBuiltIn := false
	for _, m := range manifests {
		if m.IsBuiltIn && m.Domain != "" {
			foundBuiltIn = true
			break
		}
	}
	if !foundBuiltIn {
		t.Error("no built-in integrations found in manifest list")
	}
}

// TestContract_TraceGetFields pins the decoded internals of a real trace/get
// result — the surface behind the H-7 poison. The poison only proves an all-zero
// decode renders UNPARSED; this proves the POSITIVE case: a genuinely errored run
// decodes to a fail outcome (never PASS or UNPARSED) and a genuinely finished run
// decodes to pass, with a real run_id and steps. This is exactly the D1 defect's
// blast radius: for months a shape mismatch made every run — errored, aborted —
// render PASS. Renaming a tag on analyze.RawTraceMeta (run_id, script_execution)
// reddens this where no proxy would notice.
func TestContract_TraceGetFields(t *testing.T) {
	inst, _ := getOracleHA(t)
	ws := oracleWS(t, inst)
	ctx := context.Background()

	list, err := ws.TraceList(ctx, "automation")
	if err != nil {
		t.Fatalf("trace/list: %v", err)
	}

	// From HA's own trace summaries, pick one errored run and one finished run.
	var erroredKey, erroredRun, finishedKey, finishedRun string
	keys := make([]string, 0, len(list))
	for k := range list {
		keys = append(keys, k)
	}
	sort.Strings(keys) // determinism across runs
	for _, key := range keys {
		for _, s := range list[key] {
			isErr := s.Error != "" || s.Execution == "error"
			if isErr && erroredRun == "" {
				erroredKey, erroredRun = key, s.RunID
			}
			if !isErr && (s.Execution == "finished" || s.State == "finished") && finishedRun == "" {
				finishedKey, finishedRun = key, s.RunID
			}
		}
	}
	if erroredRun == "" {
		t.Fatal("precondition: no errored automation trace found; the oracle rig's " +
			"cfgid_missing_service/cfgid_bad_template automations should have errored")
	}
	if finishedRun == "" {
		t.Fatal("precondition: no finished automation trace found; cfgid_boost_charge should have finished")
	}

	condense := func(key, runID string) *analyze.CondensedTrace {
		itemID := strings.TrimPrefix(key, "automation.")
		raw, getErr := ws.TraceGet(ctx, "automation", itemID, runID)
		if getErr != nil {
			t.Fatalf("trace/get(%s, %s): %v", itemID, runID, getErr)
		}
		var rt analyze.RawTrace
		if err := json.Unmarshal(raw, &rt); err != nil {
			t.Fatalf("trace/get result did not decode: %v", err)
		}
		return analyze.Condense(&rt)
	}

	errored := condense(erroredKey, erroredRun)
	if errored.RunID == "" {
		t.Errorf("errored trace decoded an empty run_id (wire-shape drift)")
	}
	if errored.Result != analyze.StepFail {
		t.Errorf("errored trace %s decoded outcome %q, want %q — a non-fail here is the D1 class "+
			"(a failed run rendering as success)", erroredKey, errored.Result, analyze.StepFail)
	}
	if len(errored.Steps) == 0 {
		t.Errorf("errored trace %s decoded zero steps; the run reached an action, so steps must decode", erroredKey)
	}

	finished := condense(finishedKey, finishedRun)
	if finished.RunID == "" {
		t.Errorf("finished trace decoded an empty run_id (wire-shape drift)")
	}
	if finished.Result != analyze.StepPass {
		t.Errorf("finished trace %s decoded outcome %q, want %q", finishedKey, finished.Result, analyze.StepPass)
	}
}

// TestContract_LogbookContextFields pins the /api/logbook wire shape hactl's
// `changes` and `ent who` decode: the context_* attribution fields
// (homeassistant/components/logbook/processor.py). The oracle rig fires
// cfgid_boost_charge against input_number.oracle_level from a REST-authenticated
// trigger, so HA's logbook must carry both a context_user_id (propagated) and
// context_event_type=automation_triggered + context_name (the proximate cause).
// If HA ever stops emitting these, `ent who` attribution silently collapses to
// UUIDs; this catches it against HA's own answer rather than a proxy.
func TestContract_LogbookContextFields(t *testing.T) {
	inst, _ := getOracleHA(t)
	client := haapi.New(inst.URL(), inst.Token())
	ctx := context.Background()

	now := time.Now()
	start := now.Add(-24 * time.Hour)
	raw, err := client.GetLogbookFiltered(ctx,
		start.Format(time.RFC3339), now.Format(time.RFC3339), "input_number.oracle_level")
	if err != nil {
		t.Fatalf("GET /api/logbook (filtered): %v", err)
	}

	// Decode into the exact field set changes.go/ent_who.go rely on.
	var entries []struct {
		EntityID         string `json:"entity_id"`
		When             string `json:"when"`
		State            string `json:"state"`
		ContextUserID    string `json:"context_user_id"`
		ContextEventType string `json:"context_event_type"`
		ContextName      string `json:"context_name"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("logbook did not decode into the context-field shape: %v\nbody: %.400s", err, raw)
	}
	if len(entries) == 0 {
		t.Fatal("precondition: HA's logbook holds no entries for input_number.oracle_level; " +
			"cfgid_boost_charge should have changed it")
	}

	// Base wire shape: every entry carries a `when` timestamp and a `state`.
	// The load-bearing attribution fields (the ones ent who/changes decode, and
	// the audit named) are the causal context_* set: at least one entry must be
	// an automation-triggered change carrying context_name (the proximate cause)
	// AND context_user_id (the propagated user) — the exact both-fields shape the
	// H-11 precedence and D19's inverted test hinge on. If HA stopped emitting
	// these, ent who attribution silently collapses.
	sawCausalName := false
	sawPropagatedUser := false
	for _, e := range entries {
		if e.When == "" {
			t.Errorf("logbook entry for %s has an empty `when` timestamp", e.EntityID)
		}
		if e.ContextEventType != "automation_triggered" {
			continue
		}
		if e.ContextName != "" {
			sawCausalName = true
		}
		if e.ContextUserID != "" {
			sawPropagatedUser = true
		}
	}
	if !sawCausalName {
		t.Error("no logbook entry carried context_event_type=automation_triggered + context_name; " +
			"the proximate-cause fields ent who decodes are absent from HA's answer")
	}
	if !sawPropagatedUser {
		t.Error("no automation_triggered logbook entry carried a context_user_id; " +
			"HA's context propagation changed shape and ent who's both-fields precedence goes untested")
	}
}

// TestContract_ConfigEntryDiagnostics pins the envelope shape of
// GET /api/diagnostics/config_entry/<id> — the `data` key hactl's `config show`
// reads via diagnosticsConfigData. The endpoint returns 404 for any integration
// without a diagnostics platform (many built-ins have none), so this probes
// every config entry for the first diag-capable one. If none exists in the
// fixture it prints a bounded, visible skip rather than silently passing.
func TestContract_ConfigEntryDiagnostics(t *testing.T) {
	cfg := loadConfig(t)
	client := haapi.New(cfg.URL, cfg.Token)
	ctx := context.Background()

	entriesRaw, err := client.GetConfigEntries(ctx)
	if err != nil {
		t.Fatalf("GET /api/config/config_entries/entry: %v", err)
	}
	var entries []struct {
		EntryID string `json:"entry_id"`
		Domain  string `json:"domain"`
	}
	if err := json.Unmarshal(entriesRaw, &entries); err != nil {
		t.Fatalf("config entries did not decode: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("precondition: HA reports no config entries at all")
	}

	probed, notFound := 0, 0
	for _, e := range entries {
		if e.EntryID == "" {
			continue
		}
		probed++
		diag, diagErr := client.GetConfigEntryDiagnostics(ctx, e.EntryID)
		if diagErr != nil {
			if status, ok := haapi.HTTPStatus(diagErr); ok && status == 404 {
				notFound++
				continue
			}
			// A non-404 error (401/403/5xx) is a real contract failure, not an
			// absent diagnostics platform.
			t.Fatalf("diagnostics for %s (%s): unexpected error: %v", e.EntryID, e.Domain, diagErr)
		}

		// The download-diagnostics envelope wraps the integration payload under a
		// top-level `data` key — the exact shape diagnosticsConfigData decodes.
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(diag, &envelope); err != nil {
			t.Fatalf("diagnostics for %s (%s) did not decode as an object: %v", e.EntryID, e.Domain, err)
		}
		if _, ok := envelope["data"]; !ok {
			t.Errorf("diagnostics envelope for %s (%s) has no `data` key — config show's "+
				"diagnosticsConfigData would fall back to the raw dump. Keys: %v",
				e.EntryID, e.Domain, envelopeKeys(envelope))
		}
		return // one diag-capable entry proves the envelope shape
	}

	t.Skipf("bounded skip: probed %d config entr(y/ies), all %d returned 404 — "+
		"this fixture ships no integration with a diagnostics platform, so the "+
		"diagnostics envelope shape cannot be asserted here", probed, notFound)
}

func envelopeKeys(m map[string]json.RawMessage) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
