//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// Config-flow / config-entry writes (invariant H-12).
//
// `config flow-start`/`flow-step`/`options`/`delete --confirm` create and remove
// real config entries. The confirmed path only echoes HA's flow response
// (`renderFlowResult`) and `delete` prints "deleted config entry" the moment the
// call returns nil — so an assertion on hactl's own output holds whether or not
// anything reached HA. Stubbing StartConfigFlowOnce / StepFlow /
// DeleteConfigEntry to canned success left the old skip-heavy tests green: this
// was the last pre-H-4 stubbable mutation surface (T2 / W7).
//
// Everything below reads the config-entry list (and options) straight from HA
// over its REST API and never through hactl's flow rendering path.
//
// Domain choice: met_eireann. Its config flow is a single `user` step whose
// fields are Required-with-default (name/latitude/longitude/elevation, seeded
// from HA's own configured location), so `flow-step --data {}` completes to
// create_entry with no network access and no credentials. The default `met`
// weather entry (created by default_config onboarding) carries
// supports_options=true and drives the options round-trip.
//
// These tests create and delete real config entries, so — like the other H-12
// write families — they run against the dedicated mutating instance
// (getWriteHA), whose teardown is registered in main_test.go. Each asserts its
// own cleanup so nothing leaks into the container this package shares.
// ============================================================================

const (
	flowDomain    = "met_eireann"
	optionsDomain = "met"
)

// configEntriesFromHA reads HA's own config-entry list over REST, so no part of
// the expectation travels through the flow write path under test.
func configEntriesFromHA(ctx context.Context, t *testing.T, inst *hatest.Instance) []map[string]any {
	t.Helper()
	client := haapi.New(inst.URL(), inst.Token())
	raw, err := client.GetConfigEntries(ctx)
	if err != nil {
		t.Fatalf("reading config entries from HA: %v", err)
	}
	var entries []map[string]any
	if err := json.Unmarshal(raw, &entries); err != nil {
		t.Fatalf("parsing config entries: %v", err)
	}
	return entries
}

func configEntryByDomain(ctx context.Context, t *testing.T, inst *hatest.Instance, domain string) (map[string]any, bool) {
	t.Helper()
	for _, e := range configEntriesFromHA(ctx, t, inst) {
		if e["domain"] == domain {
			return e, true
		}
	}
	return nil, false
}

// configEntryExists answers only the question its callers ask — whether HA still
// lists the entry. It used to return the entry too; nothing ever read it, so the
// signature promised more than any caller wanted.
func configEntryExists(ctx context.Context, t *testing.T, inst *hatest.Instance, entryID string) bool {
	t.Helper()
	for _, e := range configEntriesFromHA(ctx, t, inst) {
		if e["entry_id"] == entryID {
			return true
		}
	}
	return false
}

// TestConfigFlowCreateDeleteRoundTrip drives flow-start → flow-step →
// create_entry → delete, checking each mutation against HA's own config-entry
// list rather than against hactl's echo.
func TestConfigFlowCreateDeleteRoundTrip(t *testing.T) {
	inst := getWriteHA(t)
	client := haapi.New(inst.URL(), inst.Token())
	ctx := context.Background()

	// Precondition: the domain must not already be configured, or flow-start
	// aborts with already_configured and never reaches create_entry.
	if _, ok := configEntryByDomain(ctx, t, inst, flowDomain); ok {
		t.Fatalf("precondition failed: %s is already configured on this instance", flowDomain)
	}

	// Assert cleanup even if an assertion below fails midway: an orphaned entry
	// leaves a weather entity in the registry that later tests would see.
	t.Cleanup(func() {
		if e, ok := configEntryByDomain(ctx, t, inst, flowDomain); ok {
			id, _ := e["entry_id"].(string)
			if _, err := client.DeleteConfigEntry(ctx, id); err != nil {
				t.Errorf("cleanup: deleting %s entry %s: %v", flowDomain, id, err)
			}
		}
	})

	// --- flow-start dry-run must not create anything ---
	runHactlDir(t, inst.Dir(), "config", "flow-start", flowDomain)
	if _, ok := configEntryByDomain(ctx, t, inst, flowDomain); ok {
		t.Fatal("dry-run flow-start created a config entry")
	}

	// --- flow-start --confirm returns a live form ---
	startOut := runHactlDir(t, inst.Dir(), "config", "flow-start", flowDomain, "--confirm", "--json")
	var start map[string]any
	if err := json.Unmarshal([]byte(startOut), &start); err != nil {
		t.Fatalf("flow-start --json invalid: %v\n%s", err, startOut)
	}
	if start["type"] != "form" {
		t.Fatalf("expected a form from flow-start, got type=%v: %s", start["type"], startOut)
	}
	flowID, _ := start["flow_id"].(string)
	if flowID == "" {
		t.Fatalf("flow-start returned no flow_id: %s", startOut)
	}

	// flow-inspect reads the same live flow back (read path, over HA REST).
	inspectOut := runHactlDir(t, inst.Dir(), "config", "flow-inspect", flowID)
	assertContains(t, inspectOut, flowID)

	// What the form proposes, submitted as is (flowFormValues: HA 2026.11
	// suggests met_eireann's location and elevation rather than defaulting them).
	formData, err := json.Marshal(flowFormValues(t, []byte(startOut)))
	if err != nil {
		t.Fatal(err)
	}

	// --- flow-step dry-run must not complete the flow (no entry appears) ---
	runHactlDir(t, inst.Dir(), "config", "flow-step", flowID, "--data", string(formData))
	if _, ok := configEntryByDomain(ctx, t, inst, flowDomain); ok {
		t.Fatal("dry-run flow-step created a config entry")
	}

	// --- flow-step --confirm completes the flow to create_entry ---
	stepOut := runHactlDir(t, inst.Dir(), "config", "flow-step", flowID, "--data", string(formData), "--confirm", "--json")
	var step map[string]any
	if err := json.Unmarshal([]byte(stepOut), &step); err != nil {
		t.Fatalf("flow-step --json invalid: %v\n%s", err, stepOut)
	}
	if step["type"] != "create_entry" {
		t.Fatalf("expected create_entry from flow-step, got type=%v: %s", step["type"], stepOut)
	}

	// --- the entry now exists in HA's own list, with witnessed fields ---
	entry, ok := configEntryByDomain(ctx, t, inst, flowDomain)
	if !ok {
		t.Fatal("flow-step create_entry did not reach HA: no config entry for " + flowDomain)
	}
	entryID, _ := entry["entry_id"].(string)
	if entryID == "" {
		t.Fatalf("HA config entry has no entry_id: %v", entry)
	}
	// Title and state are witnesses hactl never echoed as fields of the entry:
	// met_eireann titles the entry itself — after HA's configured location
	// ("Home") up to HA 2026.10, with the fixed "Met Éireann" from 2026.11 —
	// and HA reports it loaded once the integration set up successfully.
	if title := entry["title"]; title != "Home" && title != "Met Éireann" {
		t.Errorf("HA stored title %v, want Home (HA ≤ 2026.10) or Met Éireann (HA ≥ 2026.11)", title)
	}
	if entry["state"] != "loaded" {
		t.Errorf("HA stored state %v, want loaded", entry["state"])
	}

	// --- delete dry-run leaves the entry in place ---
	runHactlDir(t, inst.Dir(), "config", "delete", entryID)
	if !configEntryExists(ctx, t, inst, entryID) {
		t.Fatal("dry-run delete removed the entry from HA")
	}

	// --- delete --confirm removes it from HA's own list ---
	runHactlDir(t, inst.Dir(), "config", "delete", entryID, "--confirm")
	if configEntryExists(ctx, t, inst, entryID) {
		t.Fatal("delete --confirm did not reach HA: entry is still listed")
	}
}

// metOptionsElevation reads the met entry's current elevation straight from HA:
// it opens an options flow and reads the value HA seeds the form's `elevation`
// field with (the entry's stored value), then aborts the flow so nothing
// dangles. This never travels through hactl's flow write path.
func metOptionsElevation(t *testing.T, client *haapi.Client, entryID string) float64 {
	t.Helper()
	ctx := context.Background()
	raw, err := client.StartOptionsFlow(ctx, entryID)
	if err != nil {
		t.Fatalf("opening options flow to read elevation: %v", err)
	}
	if id := flowIDOf(raw); id != "" {
		_ = client.AbortOptionsFlow(ctx, id)
	}
	v, ok := flowFormValues(t, raw)["elevation"]
	if !ok {
		t.Fatalf("met options form has no elevation value: %s", string(raw))
	}
	elev, ok := v.(float64)
	if !ok {
		t.Fatalf("met options form's elevation is a %T (%v), not a number", v, v)
	}
	return elev
}

// flowFormValues is what a flow form proposes for each field: its `default`
// or, where HA seeds the form with suggestions instead (met from HA 2026.11:
// add_suggested_values_to_schema), its `description.suggested_value`. A
// caller that means "submit it unchanged" has to send these: HA fills a
// missing required key from a default, never from a suggestion.
func flowFormValues(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var parsed struct {
		DataSchema []struct {
			Name        string `json:"name"`
			Default     any    `json:"default"`
			Description struct {
				SuggestedValue any `json:"suggested_value"`
			} `json:"description"`
		} `json:"data_schema"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("parsing flow form: %v\n%s", err, raw)
	}
	values := map[string]any{}
	for _, f := range parsed.DataSchema {
		switch {
		case f.Default != nil:
			values[f.Name] = f.Default
		case f.Description.SuggestedValue != nil:
			values[f.Name] = f.Description.SuggestedValue
		}
	}
	return values
}

func flowIDOf(raw []byte) string {
	var v struct {
		FlowID string `json:"flow_id"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.FlowID
}

// setMetElevationDirect writes the met entry's elevation straight through HA's
// own options flow (start + step), used to restore the original value.
func setMetElevationDirect(t *testing.T, client *haapi.Client, entryID string, elevation float64) error {
	t.Helper()
	ctx := context.Background()
	raw, err := client.StartOptionsFlow(ctx, entryID)
	if err != nil {
		return fmt.Errorf("opening options flow: %w", err)
	}
	flow, err := haapi.ParseFlowResult(raw)
	if err != nil {
		return fmt.Errorf("parsing options flow: %w", err)
	}
	values := flowFormValues(t, raw)
	values["elevation"] = elevation
	payload, err := json.Marshal(values)
	if err != nil {
		return err
	}
	if _, err := client.StepFlow(ctx, flow.FlowID, true, payload); err != nil {
		return fmt.Errorf("stepping options flow: %w", err)
	}
	return nil
}

// TestConfigOptionsRoundTrip drives `config options --confirm` +
// `config flow-step --options --confirm` against the default met entry and
// proves the new option value is persisted by reading it straight back from HA
// (a fresh options flow's seeded default), then restores the original.
func TestConfigOptionsRoundTrip(t *testing.T) {
	inst := getWriteHA(t)
	client := haapi.New(inst.URL(), inst.Token())
	ctx := context.Background()

	entry, ok := configEntryByDomain(ctx, t, inst, optionsDomain)
	if !ok {
		// Visible, bounded skip: the options round-trip needs an options-capable
		// entry, and default_config's met weather entry is the cheap one. If a
		// future HA image stops shipping it, say so loudly rather than pass empty.
		t.Skipf("no %q config entry on this HA image; options round-trip needs an options-capable entry", optionsDomain)
	}
	if entry["supports_options"] != true {
		t.Skipf("%q entry does not advertise supports_options on this HA version", optionsDomain)
	}
	entryID, _ := entry["entry_id"].(string)

	before := metOptionsElevation(t, client, entryID)
	const newElev float64 = 4242
	if before == newElev {
		t.Fatalf("precondition failed: elevation is already %v", newElev)
	}

	// Restore is asserted: a silent failure would leave the shared instance with
	// a bogus elevation that no other test resets.
	t.Cleanup(func() {
		if err := setMetElevationDirect(t, client, entryID, before); err != nil {
			t.Errorf("cleanup: restoring elevation to %v: %v", before, err)
			return
		}
		if got := metOptionsElevation(t, client, entryID); got != before {
			t.Errorf("cleanup: elevation is %v after restore, want %v", got, before)
		}
	})

	// --- options dry-run must not start a flow or change stored options ---
	runHactlDir(t, inst.Dir(), "config", "options", entryID)
	if got := metOptionsElevation(t, client, entryID); got != before {
		t.Fatalf("dry-run options changed the stored elevation: %v -> %v", before, got)
	}

	// --- options --confirm returns a live options form ---
	optOut := runHactlDir(t, inst.Dir(), "config", "options", entryID, "--confirm", "--json")
	var opt map[string]any
	if err := json.Unmarshal([]byte(optOut), &opt); err != nil {
		t.Fatalf("options --json invalid: %v\n%s", err, optOut)
	}
	if opt["type"] != "form" {
		t.Fatalf("expected a form from options, got type=%v: %s", opt["type"], optOut)
	}
	flowID, _ := opt["flow_id"].(string)
	if flowID == "" {
		t.Fatalf("options returned no flow_id: %s", optOut)
	}

	// --- flow-step --options --confirm submits the new elevation and persists it ---
	// The form's own values with the new elevation: since HA 2026.11 met
	// suggests its values instead of defaulting them, so a partial submit is
	// refused for the missing required location (flowFormValues).
	values := flowFormValues(t, []byte(optOut))
	values["elevation"] = int(newElev)
	data, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	stepOut := runHactlDir(t, inst.Dir(), "config", "flow-step", flowID,
		"--options", "--data", string(data), "--confirm", "--json")
	var step map[string]any
	if err := json.Unmarshal([]byte(stepOut), &step); err != nil {
		t.Fatalf("options flow-step --json invalid: %v\n%s", err, stepOut)
	}
	if step["type"] != "create_entry" {
		t.Fatalf("options step did not persist (want create_entry): %s", stepOut)
	}

	// --- read the new elevation back FROM HA (fresh options flow seeded default) ---
	after := metOptionsElevation(t, client, entryID)
	if after != newElev {
		t.Fatalf("options flow did not reach HA: stored elevation is %v, want %v", after, newElev)
	}
}
