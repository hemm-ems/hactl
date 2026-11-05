//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"slices"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// Policy addressing — the oracles behind the two NEEDS-ORACLE markers in
// dev/surfaces/policy.manifest (permission policy, phase 2). Both answered
// yes against HA 2026.10.4; the markers are resolved as D-83 and D-84 in
// docs/decisions.md.
//
// A write is checked against the resource it touches, so the check is only as
// right as the address the request is mapped to. Two rows map a request to an
// address through an identifier HA hands back, and whether that identifier IS
// the address was an assumption:
//
//  1. An options-flow step, inspect and abort carry only a flow_id. The
//     manifest maps them to edit config_entry:{handler}/options, reading
//     `handler` from the start answer or from GET options/flow/{flow_id}. That
//     is right only if an options flow's handler is the entry_id; if HA put
//     the integration's domain there (as a config flow does), the address
//     would be wrong and those requests must fail closed instead.
//  2. svc call script.<x> / script run map to script:<config id>/run, the
//     config id read from the entity registry. That is right only if a YAML
//     script's registry unique_id is its config key — and stays so when the
//     entity is renamed in the registry, which is exactly when entity id and
//     config key diverge.
//
// Both tests are distinguishing by construction: met's entry_id is HA-minted
// and never equals "met", and each script's alias slugs to something other
// than its key, and the test renames the entity so key and entity id differ.
// ============================================================================

func startPolicyOracleHA(t *testing.T) *hatest.Instance {
	t.Helper()
	opts := []hatest.Option{hatest.WithFixture("policyoracle")}
	if img := os.Getenv("HACTL_HA_IMAGE"); img != "" {
		opts = append(opts, hatest.WithImage(img))
	}
	inst := hatest.Start(t, opts...)
	waitForRunning(t, inst)
	return inst
}

// policyOracleGET reads a REST path straight from HA, outside every hactl
// client path, returning status and body verbatim.
func policyOracleGET(t *testing.T, inst *hatest.Instance, path string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, inst.URL()+path, http.NoBody)
	if err != nil {
		t.Fatalf("building GET %s: %v", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+inst.Token())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading GET %s: %v", path, err)
	}
	return resp.StatusCode, body
}

// flowAnswer is the part of a flow answer the address depends on.
type flowAnswer struct {
	FlowID     string `json:"flow_id"`
	Handler    any    `json:"handler"` // any: the oracle must see a non-string too
	Type       string `json:"type"`
	StepID     string `json:"step_id"`
	DataSchema []struct {
		Name        string          `json:"name"`
		Required    bool            `json:"required"`
		Default     json.RawMessage `json:"default"`
		Description struct {
			SuggestedValue json.RawMessage `json:"suggested_value"`
		} `json:"description"`
	} `json:"data_schema"`
}

func decodeFlowAnswer(t *testing.T, what string, raw []byte) (flowAnswer, map[string]any) {
	t.Helper()
	var fa flowAnswer
	if err := json.Unmarshal(raw, &fa); err != nil {
		t.Fatalf("%s: answer did not parse: %v\n%s", what, err, raw)
	}
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		t.Fatalf("%s: answer did not parse as an object: %v\n%s", what, err, raw)
	}
	return fa, all
}

// TestOracleOptionsFlowHandlerIsEntryID answers marker 1: an options flow's
// `handler` — in the start answer, in GET options/flow/{flow_id}, and in a
// step's answer — is the config entry's entry_id, not its domain.
func TestOracleOptionsFlowHandlerIsEntryID(t *testing.T) {
	inst := startPolicyOracleHA(t)
	client := haapi.New(inst.URL(), inst.Token())
	ctx := context.Background()

	entry, ok := configEntryByDomain(ctx, t, inst, optionsDomain)
	if !ok {
		t.Fatalf("no %q config entry: the oracle needs an options-capable entry HA created itself", optionsDomain)
	}
	if entry["supports_options"] != true {
		t.Fatalf("%q entry does not advertise supports_options: %v", optionsDomain, entry)
	}
	entryID, _ := entry["entry_id"].(string)
	if entryID == "" || entryID == optionsDomain {
		t.Fatalf("entry_id %q cannot tell entry_id from domain apart; the oracle would prove nothing", entryID)
	}
	t.Logf("config entry: domain=%q entry_id=%q", entry["domain"], entryID)

	mustBeEntryID := func(what string, fa flowAnswer, all map[string]any) {
		t.Helper()
		h, present := all["handler"]
		if !present {
			t.Errorf("%s: answer carries no `handler` key at all — the manifest cannot read the address from it", what)
			return
		}
		if h != entryID {
			t.Errorf("%s: handler = %#v, entry_id = %q, domain = %q — handler is NOT the entry_id; "+
				"the options rows must fail closed instead of addressing config_entry:{handler}", what, h, entryID, optionsDomain)
		}
		if fa.FlowID == entryID {
			t.Errorf("%s: flow_id equals entry_id (%q) — the rig cannot tell them apart", what, entryID)
		}
	}

	// --- start: POST options/flow {"handler": entry_id} ---
	startRaw, err := client.StartOptionsFlow(ctx, entryID)
	if err != nil {
		t.Fatalf("starting options flow: %v", err)
	}
	t.Logf("OBSERVED start answer: %s", startRaw)
	start, startAll := decodeFlowAnswer(t, "start", startRaw)
	if start.FlowID == "" {
		t.Fatalf("start answered no flow_id (type %q) — nothing to inspect: %s", start.Type, startRaw)
	}
	mustBeEntryID("start answer", start, startAll)

	// --- inspect: GET options/flow/{flow_id} ---
	getStatus, getRaw := policyOracleGET(t, inst, "/api/config/config_entries/options/flow/"+start.FlowID)
	t.Logf("OBSERVED GET options/flow/{flow_id}: status %d body %s", getStatus, getRaw)
	if getStatus != http.StatusOK {
		t.Fatalf("GET options/flow/%s answered %d: %s", start.FlowID, getStatus, getRaw)
	}
	got, gotAll := decodeFlowAnswer(t, "GET", getRaw)
	mustBeEntryID("GET options/flow/{flow_id}", got, gotAll)
	if got.FlowID != start.FlowID {
		t.Errorf("GET answered flow_id %q for flow %q", got.FlowID, start.FlowID)
	}

	// --- abort: DELETE options/flow/{flow_id}; the flow is gone afterwards ---
	if err = client.AbortOptionsFlow(ctx, start.FlowID); err != nil {
		t.Fatalf("aborting options flow: %v", err)
	}
	goneStatus, goneRaw := policyOracleGET(t, inst, "/api/config/config_entries/options/flow/"+start.FlowID)
	t.Logf("OBSERVED GET after abort: status %d body %s", goneStatus, goneRaw)
	if goneStatus == http.StatusOK {
		t.Errorf("GET of an aborted options flow still answers 200: %s", goneRaw)
	}

	// --- step: a fresh flow submitted with its own seeded values ---
	// A field's seeded value is its default or, where HA suggests instead of
	// defaulting (met from HA 2026.11), its description.suggested_value.
	// The answer of the step is what the manifest's POST options/flow/{flow_id}
	// row would see after the fact; recorded so a create_entry without a
	// handler is noticed rather than assumed.
	freshRaw, err := client.StartOptionsFlow(ctx, entryID)
	if err != nil {
		t.Fatalf("starting second options flow: %v", err)
	}
	fresh, freshAll := decodeFlowAnswer(t, "second start", freshRaw)
	mustBeEntryID("second start answer", fresh, freshAll)
	if fresh.Type != "form" {
		t.Fatalf("second start answered type %q, want a form to submit: %s", fresh.Type, freshRaw)
	}
	payload := map[string]json.RawMessage{}
	for _, f := range fresh.DataSchema {
		seeded := f.Default
		if len(seeded) == 0 {
			seeded = f.Description.SuggestedValue
		}
		if len(seeded) == 0 {
			if f.Required {
				t.Fatalf("required field %q has no seeded value; cannot submit unchanged: %s", f.Name, freshRaw)
			}
			continue
		}
		payload[f.Name] = seeded
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encoding step payload: %v", err)
	}
	stepRaw, err := client.StepFlow(ctx, fresh.FlowID, true, body)
	if err != nil {
		t.Fatalf("stepping options flow with its own defaults %s: %v", body, err)
	}
	t.Logf("OBSERVED step answer (payload %s): %s", body, stepRaw)
	step, stepAll := decodeFlowAnswer(t, "step", stepRaw)
	mustBeEntryID(fmt.Sprintf("step answer (type %s)", step.Type), step, stepAll)
}

// TestOracleYAMLScriptUniqueIDIsConfigKey answers marker 2: a YAML script's
// entity-registry unique_id is its config key, before and after the entity is
// renamed in the registry and across script.reload, and the config API
// addresses the script by that key.
func TestOracleYAMLScriptUniqueIDIsConfigKey(t *testing.T) {
	inst := startPolicyOracleHA(t)
	ctx := context.Background()
	ws := oracleWS(t, inst)
	client := haapi.New(inst.URL(), inst.Token())

	const (
		key     = "policy_oracle_key"
		second  = "policy_oracle_second"
		renamed = "script.policy_oracle_renamed"
	)

	scriptRows := func(when string) map[string]haapi.EntityRegistryEntry {
		t.Helper()
		entries, err := ws.EntityRegistryList(ctx)
		if err != nil {
			t.Fatalf("%s: entity registry list: %v", when, err)
		}
		out := map[string]haapi.EntityRegistryEntry{}
		for _, e := range entries {
			if e.Platform == "script" {
				out[e.EntityID] = e
				t.Logf("OBSERVED %s: registry entity_id=%q platform=%q unique_id=%q config_entry_id=%q name=%q original_name=%q",
					when, e.EntityID, e.Platform, e.UniqueID, e.ConfigEntryID, e.Name, e.OrigName)
			}
		}
		return out
	}

	// --- as loaded: entity id from the key, unique_id == key ---
	before := scriptRows("as loaded")
	for _, k := range []string{key, second} {
		e, ok := before["script."+k]
		if !ok {
			t.Fatalf("as loaded: no registry row script.%s (script rows: %v) — HA did not derive the entity id from the key",
				k, slices.Collect(maps.Keys(before)))
		}
		if e.UniqueID != k {
			t.Errorf("as loaded: script.%s has unique_id %q, want its config key %q", k, e.UniqueID, k)
		}
	}

	// --- renamed in the registry: entity id and key now differ ---
	if err := ws.EntityRegistryUpdate(ctx, "script."+key, map[string]any{"new_entity_id": renamed}); err != nil {
		t.Fatalf("renaming script.%s to %s: %v", key, renamed, err)
	}
	after := scriptRows("after rename")
	if _, still := after["script."+key]; still {
		t.Fatalf("after rename: script.%s is still registered — the rename did not take", key)
	}
	if e, ok := after[renamed]; !ok {
		t.Fatalf("after rename: no registry row %s", renamed)
	} else if e.UniqueID != key {
		t.Errorf("after rename: %s has unique_id %q, want the config key %q — a renamed script would not map back", renamed, e.UniqueID, key)
	}

	// --- script.reload re-reads scripts.yaml: the renamed row must be kept,
	// not a fresh script.<key> minted beside it ---
	if _, err := client.CallService(ctx, "script", "reload", map[string]any{}); err != nil {
		t.Fatalf("script.reload: %v", err)
	}
	reloaded := scriptRows("after reload")
	if _, minted := reloaded["script."+key]; minted {
		t.Errorf("after reload: HA minted a fresh script.%s beside the renamed row", key)
	}
	if e, ok := reloaded[renamed]; !ok {
		t.Errorf("after reload: renamed row %s is gone", renamed)
	} else if e.UniqueID != key {
		t.Errorf("after reload: %s has unique_id %q, want %q", renamed, e.UniqueID, key)
	}
	if _, ok := currentStates(t, inst)[renamed]; !ok {
		t.Errorf("after reload: %s has no state — the renamed entity is not the live script", renamed)
	}

	assertScriptConfigAddressedByKey(t, inst, key, "Alias Slugs Elsewhere", "policy_oracle_renamed")
	assertScriptServiceFollowsKey(t, inst, key, "policy_oracle_renamed")
}

// assertScriptConfigAddressedByKey: the config API answers the script at its
// config key, and not at the renamed entity's object id.
func assertScriptConfigAddressedByKey(t *testing.T, inst *hatest.Instance, key, alias, renamedObjectID string) {
	t.Helper()
	status, cfgRaw := policyOracleGET(t, inst, "/api/config/script/config/"+key)
	t.Logf("OBSERVED GET /api/config/script/config/%s: status %d body %s", key, status, cfgRaw)
	if status != http.StatusOK {
		t.Errorf("config API answered %d for the unique_id %q: %s", status, key, cfgRaw)
	} else {
		var cfg struct {
			Alias string `json:"alias"`
		}
		if err := json.Unmarshal(cfgRaw, &cfg); err != nil {
			t.Fatalf("script config did not parse: %v", err)
		}
		if cfg.Alias != alias {
			t.Errorf("config at %q has alias %q — it is not the script the registry row names", key, cfg.Alias)
		}
	}
	renamedStatus, renamedRaw := policyOracleGET(t, inst, "/api/config/script/config/"+renamedObjectID)
	t.Logf("OBSERVED GET /api/config/script/config/%s: status %d body %s", renamedObjectID, renamedStatus, renamedRaw)
	if renamedStatus != http.StatusNotFound {
		t.Errorf("config API answered %d for the renamed entity's object id — the entity id addresses the config too, "+
			"so the registry is not the only path back to the key: %s", renamedStatus, renamedRaw)
	}
}

// assertScriptServiceFollowsKey: the per-script service keeps the config key
// after the rename, and none is registered under the renamed object id — svc
// call script.<x> names the config key directly, with no registry lookup.
func assertScriptServiceFollowsKey(t *testing.T, inst *hatest.Instance, key, renamedObjectID string) {
	t.Helper()
	svcStatus, svcRaw := policyOracleGET(t, inst, "/api/services")
	if svcStatus != http.StatusOK {
		t.Fatalf("GET /api/services answered %d", svcStatus)
	}
	type serviceDomain struct {
		Domain   string                     `json:"domain"`
		Services map[string]json.RawMessage `json:"services"`
	}
	var domains []serviceDomain
	if err := json.Unmarshal(svcRaw, &domains); err != nil {
		t.Fatalf("services did not parse: %v", err)
	}
	i := slices.IndexFunc(domains, func(d serviceDomain) bool { return d.Domain == "script" })
	if i < 0 {
		t.Fatalf("GET /api/services lists no script domain")
	}
	services := domains[i].Services
	t.Logf("OBSERVED script services after rename: %v", slices.Sorted(maps.Keys(services)))
	if _, ok := services[key]; !ok {
		t.Errorf("after rename: no service script.%s — the per-script service does not follow the config key", key)
	}
	if _, ok := services[renamedObjectID]; ok {
		t.Errorf("after rename: a service script.%s exists — the per-script service follows the entity id", renamedObjectID)
	}
}
