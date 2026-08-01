package cmd

import (
	"context"
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- Issue #71 (H4): helper ls must union YAML-sourced helpers (companion's
// per-domain files) with storage-backed helper entities discovered live via
// /api/states, distinguished by a "source" column. Most real instances create
// helpers in the UI, so listing YAML alone reports "no helpers" while dozens
// exist. See TestRunHelperLs_WithCompanion / TestRunHelperLs_Empty in
// ws_cmd_test.go for the single-source (YAML-only) precedent these extend.

// helperEnv starts a companion mock (YAML helpers) and an HA mock (states, for
// storage-backed helper discovery), and returns a dir with both wired into
// its .env via COMPANION_URL / HA_URL.
func helperEnv(t *testing.T, companionHandler, haHandler http.HandlerFunc) string {
	t.Helper()
	companionSrv := httptest.NewServer(companionHandler)
	t.Cleanup(companionSrv.Close)
	haSrv := httptest.NewServer(haHandler)
	t.Cleanup(haSrv.Close)

	dir := t.TempDir()
	env := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=tok\nCOMPANION_URL=%s\n", haSrv.URL, companionSrv.URL)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// helperStatesHandler serves a fixed /api/states body and 404s everything else.
func helperStatesHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/states" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, body)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// helperCompanionHandler serves a fixed /v1/config/helpers body and 404s
// everything else.
func helperCompanionHandler(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config/helpers" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, body)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func TestRunHelperLs_UnionsStorageAndYAML(t *testing.T) {
	companionBody := `{"helpers":[{"id":"guest_mode","name":"Guest Mode","domain":"input_boolean"}]}`
	statesBody := `[
		{"entity_id":"input_boolean.guest_mode","state":"off","attributes":{"friendly_name":"Guest Mode"}},
		{"entity_id":"input_number.target_temp","state":"21","attributes":{"friendly_name":"Target Temp","editable":true}},
		{"entity_id":"sensor.not_a_helper","state":"1","attributes":{}}
	]`
	dir := helperEnv(t, helperCompanionHandler(companionBody), helperStatesHandler(statesBody))
	withFlagDir(t, dir)
	old := flagHelperDomain
	flagHelperDomain = ""
	defer func() { flagHelperDomain = old }()

	var buf bytes.Buffer
	if err := runHelperLs(listingCmd(context.Background(), "helper", "ls"), &buf); err != nil {
		t.Fatalf("runHelperLs: %v", err)
	}
	out := buf.String()

	if !strings.Contains(out, "guest_mode") {
		t.Errorf("output missing YAML helper 'guest_mode': %q", out)
	}
	if !strings.Contains(out, "input_number.target_temp") {
		t.Errorf("output missing storage helper 'input_number.target_temp': %q", out)
	}
	if !strings.Contains(out, "yaml") {
		t.Errorf("output missing 'yaml' source marker: %q", out)
	}
	if !strings.Contains(out, "storage") {
		t.Errorf("output missing 'storage' source marker: %q", out)
	}
	if strings.Contains(out, "not_a_helper") {
		t.Errorf("non-helper-domain entity leaked into listing: %q", out)
	}
	// input_boolean.guest_mode is both a companion YAML helper and a live
	// state; it must be reported once (as "yaml"), not duplicated.
	if n := strings.Count(out, "guest_mode"); n != 1 {
		t.Errorf("guest_mode should be deduped to one row (yaml wins), appeared %d times: %q", n, out)
	}
}

func TestRunHelperLs_StorageOnly_NoLongerReportsNoHelpers(t *testing.T) {
	// Regression for the exact bug in issue #71: an instance with zero
	// YAML-configured helpers but many UI-created ones must not say "no
	// helpers".
	companionBody := `{"helpers":[]}`
	statesBody := `[
		{"entity_id":"input_boolean.night_mode","state":"on","attributes":{"friendly_name":"Night Mode","editable":true}},
		{"entity_id":"counter.laundry_loads","state":"3","attributes":{"friendly_name":"Laundry Loads","editable":true}},
		{"entity_id":"input_button.restart_router","state":"unknown","attributes":{"friendly_name":"Restart Router"}}
	]`
	dir := helperEnv(t, helperCompanionHandler(companionBody), helperStatesHandler(statesBody))
	withFlagDir(t, dir)
	old := flagHelperDomain
	flagHelperDomain = ""
	defer func() { flagHelperDomain = old }()

	var buf bytes.Buffer
	if err := runHelperLs(listingCmd(context.Background(), "helper", "ls"), &buf); err != nil {
		t.Fatalf("runHelperLs: %v", err)
	}
	out := buf.String()

	if strings.Contains(out, "no helpers") {
		t.Fatalf("storage-backed helpers must not be reported as 'no helpers': %q", out)
	}
	for _, want := range []string{"night_mode", "laundry_loads", "restart_router"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing storage helper %q: %q", want, out)
		}
	}
	// input_button has no YAML equivalent — every row here must be storage.
	if strings.Contains(out, "yaml") {
		t.Errorf("no YAML helpers were configured; 'yaml' source should not appear: %q", out)
	}
}

func TestRunHelperLs_DomainFilter_ReachesStorageOnlyDomain(t *testing.T) {
	// input_button has no companion YAML support (see hactl-companion
	// routes/helpers.py ALLOWED_DOMAINS) but --domain must still find it via
	// storage discovery.
	companionBody := `{"helpers":[{"id":"guest_mode","name":"Guest Mode","domain":"input_boolean"}]}`
	statesBody := `[
		{"entity_id":"input_boolean.guest_mode","state":"off","attributes":{}},
		{"entity_id":"input_button.restart_router","state":"unknown","attributes":{"friendly_name":"Restart Router"}}
	]`
	dir := helperEnv(t, helperCompanionHandler(companionBody), helperStatesHandler(statesBody))
	withFlagDir(t, dir)
	old := flagHelperDomain
	flagHelperDomain = "input_button"
	defer func() { flagHelperDomain = old }()

	var buf bytes.Buffer
	if err := runHelperLs(listingCmd(context.Background(), "helper", "ls"), &buf); err != nil {
		t.Fatalf("runHelperLs --domain input_button: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "restart_router") {
		t.Errorf("output missing input_button helper: %q", out)
	}
	if strings.Contains(out, "guest_mode") {
		t.Errorf("domain filter should exclude input_boolean helper: %q", out)
	}
}

func TestRunHelperLs_JSON_Mixed(t *testing.T) {
	companionBody := `{"helpers":[{"id":"guest_mode","name":"Guest Mode","domain":"input_boolean"}]}`
	statesBody := `[{"entity_id":"input_number.target_temp","state":"21","attributes":{"friendly_name":"Target Temp","editable":true}}]`
	dir := helperEnv(t, helperCompanionHandler(companionBody), helperStatesHandler(statesBody))
	withFlagDir(t, dir)
	withFlagJSON(t, true)
	old := flagHelperDomain
	flagHelperDomain = ""
	defer func() { flagHelperDomain = old }()

	var buf bytes.Buffer
	if err := runHelperLs(listingCmd(context.Background(), "helper", "ls"), &buf); err != nil {
		t.Fatalf("runHelperLs JSON: %v", err)
	}
	v := assertValidJSON(t, buf.String())
	arr, ok := v.([]any)
	if !ok {
		t.Fatalf("output is not a JSON array: %s", buf.String())
	}
	if len(arr) != 2 {
		t.Fatalf("expected 2 rows (1 yaml + 1 storage), got %d: %s", len(arr), buf.String())
	}
	sources := make(map[string]bool, 2)
	for _, item := range arr {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("row is not an object: %v", item)
		}
		src, _ := row["source"].(string)
		sources[src] = true
	}
	if !sources["yaml"] || !sources["storage"] {
		t.Errorf("expected both 'yaml' and 'storage' sources in JSON output, got: %s", buf.String())
	}
}

// TestRunHelperLs_SourceFollowsEditableNotTheCodePath — finding #104.
//
// The three arms are the whole rule, and the third is why this case exists at
// all. `helper ls` used to label every row the companion had not returned
// `storage`, so the column reported which of hactl's two reads had produced the
// row. On an instance whose helper domains are written inline in
// configuration.yaml the companion returns nothing, and all 222 helpers were
// announced as created in the Home Assistant UI — 42 of them wrongly, and 10
// unknowably.
//
// The fixture carries what a real HA sends, which is the reason the rig never
// caught this: no fixture in this repository set `editable` on a helper, so
// hactl's invented value was never contradicted by anything it read.
func TestRunHelperLs_SourceFollowsEditableNotTheCodePath(t *testing.T) {
	// No companion rows at all: the shape of an instance that configures its
	// helper domains inline, which is the one the defect was found on.
	companionBody := `{"helpers":[]}`
	statesBody := `[
		{"entity_id":"input_boolean.ui_made","state":"off","attributes":{"friendly_name":"UI Made","editable":true}},
		{"entity_id":"input_number.inline_yaml","state":"21","attributes":{"friendly_name":"Inline YAML","editable":false}},
		{"entity_id":"input_button.a_restored_ghost","state":"unavailable","attributes":{"friendly_name":"Ghost","restored":true}}
	]`
	dir := helperEnv(t, helperCompanionHandler(companionBody), helperStatesHandler(statesBody))
	withFlagDir(t, dir)
	withFlagJSON(t, true)
	old := flagHelperDomain
	flagHelperDomain = ""
	defer func() { flagHelperDomain = old }()

	var buf bytes.Buffer
	if err := runHelperLs(listingCmd(context.Background(), "helper", "ls"), &buf); err != nil {
		t.Fatalf("runHelperLs: %v", err)
	}
	arr, ok := assertValidJSON(t, buf.String()).([]any)
	if !ok {
		t.Fatalf("output is not a JSON array: %s", buf.String())
	}

	got := map[string]string{}
	for _, item := range arr {
		row, isObj := item.(map[string]any)
		if !isObj {
			t.Fatalf("row is not an object: %v", item)
		}
		id, _ := row["id"].(string)
		src, _ := row["source"].(string)
		got[id] = src
	}

	for _, c := range []struct{ id, want, why string }{
		{"input_boolean.ui_made", "storage", "editable:true is a storage collection"},
		{"input_number.inline_yaml", "yaml", "editable:false is a YAML definition, wherever the file lives"},
		{"input_button.a_restored_ghost", "", "a restored ghost carries no editable, and absent is not evidence"},
	} {
		if src, listed := got[c.id]; !listed {
			t.Errorf("%s is missing from the listing entirely", c.id)
		} else if src != c.want {
			t.Errorf("%s: source=%q, want %q — %s", c.id, src, c.want, c.why)
		}
	}
}

func TestRunHelperLs_JSON_Empty(t *testing.T) {
	dir := helperEnv(t, helperCompanionHandler(`{"helpers":[]}`), helperStatesHandler(`[]`))
	withFlagDir(t, dir)
	withFlagJSON(t, true)
	old := flagHelperDomain
	flagHelperDomain = ""
	defer func() { flagHelperDomain = old }()

	var buf bytes.Buffer
	if err := runHelperLs(listingCmd(context.Background(), "helper", "ls"), &buf); err != nil {
		t.Fatalf("runHelperLs JSON empty: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

// TestRunHelperLs_JSON_Empty_HAUnreachable covers the H3 requirement that
// --json stay valid even on the "storage fetch failed" path (HA down/404):
// the command must fall back to whatever the YAML source found (nothing
// here) and still emit "[]", never bare prose or a hard error.
func TestRunHelperLs_JSON_Empty_HAUnreachable(t *testing.T) {
	dir := helperEnv(t, helperCompanionHandler(`{"helpers":[]}`), func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "not found", http.StatusNotFound)
	})
	withFlagDir(t, dir)
	withFlagJSON(t, true)
	old := flagHelperDomain
	flagHelperDomain = ""
	defer func() { flagHelperDomain = old }()

	var buf bytes.Buffer
	if err := runHelperLs(listingCmd(context.Background(), "helper", "ls"), &buf); err != nil {
		t.Fatalf("runHelperLs JSON, HA unreachable: %v", err)
	}
	assertEmptyJSONArray(t, buf.String())
}

// TestRunHelperLs_PatternAndNameFilters — --pattern narrows by id across BOTH
// sources (a yaml row's bare slug and a storage row's entity_id), and --name
// narrows by display name; per D-1 --pattern never matches a display name.
func TestRunHelperLs_PatternAndNameFilters(t *testing.T) {
	companionBody := `{"helpers":[{"id":"guest_mode","name":"Guest Mode","domain":"input_boolean"}]}`
	statesBody := `[{"entity_id":"input_number.guest_limit","state":"3","attributes":{"friendly_name":"Besucherlimit"}},
		{"entity_id":"timer.pizza","state":"idle","attributes":{"friendly_name":"Pizza Timer"}}]`

	run := func(set func()) string {
		t.Helper()
		dir := helperEnv(t, helperCompanionHandler(companionBody), helperStatesHandler(statesBody))
		withFlagDir(t, dir)
		oldP, oldN := flagHelperPattern, flagHelperName
		flagHelperPattern, flagHelperName = "", ""
		defer func() { flagHelperPattern, flagHelperName = oldP, oldN }()
		set()
		var buf bytes.Buffer
		if err := runHelperLs(listingCmd(context.Background(), "helper", "ls"), &buf); err != nil {
			t.Fatalf("runHelperLs: %v", err)
		}
		return buf.String()
	}

	out := run(func() { flagHelperPattern = "guest" })
	for _, want := range []string{"guest_mode", "guest_limit"} {
		if !strings.Contains(out, want) {
			t.Errorf("--pattern guest: output missing %q: %q", want, out)
		}
	}
	if strings.Contains(out, "pizza") {
		t.Errorf("--pattern guest: pizza row survived: %q", out)
	}

	// D-1: a display name is not an identifier, so --pattern must not match it.
	out = run(func() { flagHelperPattern = "Besucherlimit" })
	if !strings.Contains(out, "no helpers") {
		t.Errorf("--pattern on a display name must match nothing, got: %q", out)
	}

	out = run(func() { flagHelperName = "besucher" })
	if !strings.Contains(out, "guest_limit") || strings.Contains(out, "guest_mode") {
		t.Errorf("--name besucher: want only the Besucherlimit row, got: %q", out)
	}
}

func TestFilterHelperRowsByDomain(t *testing.T) {
	rows := []helperRow{
		{ID: "guest_mode", Domain: "input_boolean", Source: "yaml"},
		{ID: "input_number.target_temp", Domain: "input_number", Source: "storage"},
	}
	got := filterHelperRowsByDomain(rows, "input_number")
	if len(got) != 1 || got[0].ID != "input_number.target_temp" {
		t.Errorf("filterHelperRowsByDomain(input_number) = %+v, want only the input_number row", got)
	}
}
