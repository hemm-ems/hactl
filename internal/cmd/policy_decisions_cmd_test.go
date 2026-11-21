package cmd

// CLI spec tests for D-63..D-68, the 2026-10-31 confirmations of D-1, D-52,
// D-55 and D-60, and the bug/test-gap findings of the phase-1 review pass
// (_wt/p1-findings.json), authored from docs/decisions.md, the manual's
// "Policy (permission guardrail)" section and the findings file. internal/
// policy, internal/glob and internal/cmd/policy.go were not read beyond
// exported signatures and doc comments; each test drives the real command tree
// through RunWithOutput against a stubbed Home Assistant (startCmdServer).

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hemm-ems/hactl/internal/mcpserver"
	"github.com/hemm-ems/hactl/internal/policy"
)

// policyRig is a stubbed Home Assistant shaped for the policy's live reads:
//
//   - light.hall: in the entity registry with NO own area and no labels, on
//     device devhall (area hall_id "Hall", label devlabel "Device Label") —
//     its area is inherited (H-8), its labels are not.
//   - light.kitchen: own area kitchen_id "Kitchen" and own label energy, on
//     the same device devhall — its own area wins over the device's.
//   - device_tracker.jan: attributes latitude, longitude, lx.
//   - lock.front_door: attributes battery_level, x.
//   - automation.morning: config id 1700000000000 (D-1).
//   - switch.disabled_one: in the entity registry only (disabled, no state).
//   - sensor.yaml_only: in /api/states only (a YAML entity without a
//     unique_id, so no registry entry).
//   - script.kino: a script, object id kino as `script ls` prints it.
//   - dashboards: the default ("") and url_path main; services light.turn_on,
//     lock.unlock; floor ground.
func policyRig(t *testing.T, body string) *cmdTestServer {
	t.Helper()
	states := []map[string]any{
		{"entity_id": "light.hall", "state": "on", "attributes": map[string]any{"friendly_name": "Hall"}},
		{"entity_id": "light.kitchen", "state": "on", "attributes": map[string]any{"friendly_name": "Kitchen"}},
		{"entity_id": "device_tracker.jan", "state": "home", "attributes": map[string]any{"latitude": 52.5, "longitude": 13.4, "lx": 1}},
		{"entity_id": "lock.front_door", "state": "locked", "attributes": map[string]any{"battery_level": 90, "x": 1}},
		{"entity_id": "automation.morning", "state": "on", "attributes": map[string]any{"id": "1700000000000", "friendly_name": "Morning"}},
		{"entity_id": "sensor.yaml_only", "state": "1", "attributes": map[string]any{"friendly_name": "YAML only"}},
		{"entity_id": "script.kino", "state": "off", "attributes": map[string]any{"friendly_name": "Kino"}},
	}
	for _, s := range states {
		s["last_changed"] = "2026-10-31T08:00:00+00:00"
		s["last_updated"] = "2026-10-31T08:00:00+00:00"
	}
	ws := map[string]any{
		"config/area_registry/list": []map[string]any{
			{"area_id": "hall_id", "name": "Hall"},
			{"area_id": "kitchen_id", "name": "Kitchen"},
		},
		"config/floor_registry/list": []map[string]any{{"floor_id": "ground", "name": "Ground", "level": 0}},
		"config/label_registry/list": []map[string]any{
			{"label_id": "devlabel", "name": "Device Label"},
			{"label_id": "energy", "name": "Energy"},
		},
		"config/device_registry/list": []map[string]any{
			{"id": "devhall", "name": "Hall Hub", "area_id": "hall_id", "labels": []string{"devlabel"}},
			{"id": "devplain", "name": "Plain", "area_id": nil, "labels": []string{}},
		},
		"config/entity_registry/list": []map[string]any{
			{"entity_id": "light.hall", "device_id": "devhall", "area_id": nil, "labels": []string{}},
			{"entity_id": "light.kitchen", "device_id": "devhall", "area_id": "kitchen_id", "labels": []string{"energy"}},
			{"entity_id": "device_tracker.jan", "device_id": nil, "area_id": nil, "labels": []string{}},
			{"entity_id": "lock.front_door", "device_id": nil, "area_id": nil, "labels": []string{}},
			{"entity_id": "automation.morning", "device_id": nil, "area_id": nil, "labels": []string{}},
			{"entity_id": "switch.disabled_one", "device_id": nil, "area_id": nil, "labels": []string{}, "disabled_by": "user"},
		},
		"lovelace/dashboards/list": []map[string]any{{"id": "dash1", "url_path": "main", "title": "Main", "mode": "storage"}},
	}
	httpH := map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(states)
		},
		"/api/services": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `[{"domain":"light","services":{"turn_on":{}}},{"domain":"lock","services":{"unlock":{}}}]`)
		},
	}
	ts := startCmdServer(t, ws, httpH)
	if body != "" {
		if err := os.WriteFile(filepath.Join(ts.dir, policy.FileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return ts
}

type lintDoc struct {
	Valid    bool `json:"valid"`
	Findings []struct {
		Code    string `json:"code"`
		Profile string `json:"profile"`
		Rule    string `json:"rule"`
		Line    int    `json:"line"`
		Message string `json:"message"`
	} `json:"findings"`
	Unchecked []struct {
		Kind    string `json:"kind"`
		Reason  string `json:"reason"`
		Profile string `json:"profile"`
		Rule    string `json:"rule"`
		Line    int    `json:"line"`
	} `json:"unchecked"`
}

func decodeLint(t *testing.T, out string) lintDoc {
	t.Helper()
	var d lintDoc
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("lint --json does not decode: %v\n%s", err, out)
	}
	return d
}

func (d lintDoc) codes() []string {
	out := make([]string, 0, len(d.Findings))
	for _, f := range d.Findings {
		out = append(out, fmt.Sprintf("%s@%d", f.Code, f.Line))
	}
	return out
}

// --- D-55 / H-8: the registry-backed Live ---------------------------------------

// TestPolicyCheckSelectorsReadTheRegistries — findings 14 and 23 (test-gaps):
// an entity's area is its own or else its device's (H-8); an entity does not
// inherit its device's labels; a device selector reads the device's own area
// and labels; values glob case-insensitively. Each case names the mutation it
// goes red under (finding 14's M1..M5, finding 23's).
func TestPolicyCheckSelectorsReadTheRegistries(t *testing.T) {
	ts := policyRig(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.hall": [control]
      "entity:light.kitchen": [control]
      "device:devhall": [edit]
      "entity:light.*": [read]
    where:
      "entity:light.hall": { area: hall_id }
      "entity:light.kitchen": { label: devlabel }
      "device:devhall": { area: hall, label: "DEVICE*" }
      "entity:light.*": { area: "hal*" }
`)
	for _, tc := range []struct {
		name, res, verb string
		exit            int
		line            int
		mutation        string
	}{
		{"entity inherits its device's area", "entity:light.hall", "control", 0, 7,
			"M1 / finding 23: EntityArea without the device fallback"},
		{"entity does not inherit its device's labels", "entity:light.kitchen", "control", 1, 0,
			"M2: EntityLabels also returning the device's labels (fails open)"},
		{"device selector reads the device, by name and by glob", "device:devhall", "edit", 0, 9,
			"M4/M5: a device where routed to EntityArea/EntityLabels; M3: exact case-sensitive values"},
		{"values glob case-insensitively, inherited area", "entity:light.hall", "read", 0, 10,
			"M3: where values exact and case-sensitive"},
		{"an entity's own area wins over its device's", "entity:light.kitchen", "read", 1, 0,
			"H-8: the device area used although the entity has its own"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runPolicyCLI(t, ts.dir, "policy", "check", tc.res, tc.verb, "--json")
			if cliExitCode(err) != tc.exit {
				t.Fatalf("exit %d (%v), want %d — red under %s\n%s", cliExitCode(err), err, tc.exit, tc.mutation, out)
			}
			m := decodeObject(t, out)
			wantField(t, tc.name, m, "line", tc.line)
			wantField(t, tc.name, m, "notes", []any{})
		})
	}
}

// --- lint: inventory and coverage --------------------------------------------------

// TestPolicyLintInventoryCoversRegistryOnlyEntitiesAndTheDefaultDashboard —
// findings 15 and 29 (test-gaps): entity ids come from states AND the entity
// registry (M33: a disabled entity's deny becomes a false no-match), dashboards
// are the url_paths plus the default (M35: `dashboard:` becomes a false
// no-match), and no-match applies to allows too (M27).
func TestPolicyLintInventoryCoversRegistryOnlyEntitiesAndTheDefaultDashboard(t *testing.T) {
	withOwnerTerminal(t) // lint against the whole instance is the owner's (D-173)
	ts := policyRig(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.*": [read]
      "entity:ligth.*": [read]
    deny:
      "entity:switch.disabled_one": [write]
      "dashboard:": [edit]
      "dashboard:main": [delete]
      "area:hall_id": [delete]
      "label:energy": [delete]
      "device:devhall": [delete]
      "service:lock.unlock": [control]
      "floor:ground": [delete]
`)
	out, err := runPolicyCLI(t, ts.dir, "policy", "lint", "--json")
	d := decodeLint(t, out)
	if got, want := d.codes(), []string{"no-match@8"}; !slices.Equal(got, want) {
		t.Errorf("findings %v, want %v — line 8 is the misspelt allow (red under M27); lines 10/11 under M33/M35\n%s", got, want, out)
	}
	if len(d.Unchecked) != 0 {
		t.Errorf("unchecked %+v, want none: every kind named here is enumerated from the instance", d.Unchecked)
	}
	if cliExitCode(err) != 1 {
		t.Errorf("exit %d, want 1 (a finding)", cliExitCode(err))
	}
}

// TestPolicyLintChecksAutomationsAgainstConfigIDs — D-1, confirmed for lint
// 2026-10-31: automation rules are compared with the config ids, not the
// entity object id `auto ls` prints.
func TestPolicyLintChecksAutomationsAgainstConfigIDs(t *testing.T) {
	withOwnerTerminal(t) // lint against the whole instance is the owner's (D-173)
	f := buildContractFixture(t)
	body := "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    allow:\n      \"automation:1700000000000\": [read]\n    deny:\n      \"automation:morning\": [delete]\n"
	if err := os.WriteFile(filepath.Join(f.dir, policy.FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	out, _ := runPolicyCLI(t, f.dir, "policy", "lint", "--json")
	d := decodeLint(t, out)
	if got, want := d.codes(), []string{"no-match@9"}; !slices.Equal(got, want) {
		t.Errorf("findings %v, want %v (the config id matches; the object id does not)\n%s", got, want, out)
	}
	for _, u := range d.Unchecked {
		if u.Kind == "automation" {
			t.Errorf("automation unchecked (%s); D-1 config ids are read from the instance", u.Reason)
		}
	}
}

// TestPolicyLintFileWithoutProfilesIsClean — finding 15/M20 (test-gap):
// implicit-allow-all is reported only when profiles are declared; `version: 1`
// alone lints clean, exit 0. Red under M20 (the finding for a profile-less file).
func TestPolicyLintFileWithoutProfilesIsClean(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), "version: 1\n")
	out, err := runPolicyCLI(t, dir, "policy", "lint", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("exit %d (%v), want 0\n%s", cliExitCode(err), err, out)
	}
	d := decodeLint(t, out)
	if len(d.Findings) != 0 || len(d.Unchecked) != 0 {
		t.Errorf("findings %+v unchecked %+v, want both empty", d.Findings, d.Unchecked)
	}
}

// --- D-65: below the id ---------------------------------------------------------------

// TestPolicyLintChecksAttributeNames — D-65 (finding 3): `attr/<name>` is
// checked against the attribute names in the live state of the entities the
// id matches (glob and case as in Matching); a pattern stopping at `attr` has
// nothing to check; an id matching no entity is one no-match, not two. Red
// today: the attr typo on line 9 and the attr no light has (line 14) lint
// clean.
func TestPolicyLintChecksAttributeNames(t *testing.T) {
	ts := policyRig(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*": [read]
    deny:
      "entity:device_tracker.*/attr/latitdue": [read]
      "entity:device_tracker.*/attr/latitude": [read]
      "entity:device_tracker.*/attr/LAT*": [read]
      "entity:device_tracker.*/attr": [read]
      "entity:lokc.*/attr/latitude": [read]
      "entity:light.*/attr/latitude": [read]
`)
	out, err := runPolicyCLI(t, ts.dir, "policy", "lint", "--json")
	d := decodeLint(t, out)
	if got, want := d.codes(), []string{"no-match@9", "no-match@13", "no-match@14"}; !slices.Equal(got, want) {
		t.Errorf("findings %v, want %v\n%s", got, want, out)
	}
	if len(d.Unchecked) != 0 {
		t.Errorf("unchecked %+v, want none: attr names are checked, not unchecked", d.Unchecked)
	}
	if cliExitCode(err) != 1 {
		t.Errorf("exit %d, want 1", cliExitCode(err))
	}
}

// TestPolicyLintListsFreeSubSegmentsAsUnchecked — D-65 (finding 3): a rule
// with a free segment other than attr (views/<path>) is one unchecked entry
// with reason `sub-resource not checked` and its profile, rule and line, even
// though dashboards are enumerated: exit 1, and --allow-partial exit 0 with
// the entry still listed. Red today: lint is clean, exit 0.
func TestPolicyLintListsFreeSubSegmentsAsUnchecked(t *testing.T) {
	withOwnerTerminal(t) // lint against the whole instance is the owner's (D-173)
	ts := policyRig(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    deny:
      "dashboard:main/views/secrte": [read]
`)
	want := []map[string]any{{"kind": "dashboard", "reason": "sub-resource not checked", "profile": "agent",
		"rule": `deny "dashboard:main/views/secrte" [read]`, "line": 7}}
	out, err := runPolicyCLI(t, ts.dir, "policy", "lint", "--json")
	if cliExitCode(err) != 1 {
		t.Errorf("lint: exit %d, want 1 (lint is never clean while such a rule exists)\n%s", cliExitCode(err), out)
	}
	m := decodeObject(t, out)
	wantField(t, "lint/free-sub", m, "findings", []any{})
	wantField(t, "lint/free-sub", m, "unchecked", want)

	out, err = runPolicyCLI(t, ts.dir, "policy", "lint", "--allow-partial", "--json")
	if cliExitCode(err) != 0 {
		t.Errorf("lint --allow-partial: exit %d (%v), want 0\n%s", cliExitCode(err), err, out)
	}
	wantField(t, "lint/free-sub/allow-partial", decodeObject(t, out), "unchecked", want)

	out, _ = runPolicyCLI(t, ts.dir, "policy", "lint")
	if !strings.Contains(out, "sub-resource not checked") {
		t.Errorf("lint prose does not list the unchecked rule:\n%s", out)
	}
}

// TestPolicyLintUnreachableTreatsAGlobSubAsASet — findings 10 and 18 (bugs),
// at the CLI: allows whose glob sub path still decides for a live attribute
// (battery_level, latitude) are not unreachable, and the denies match live
// attributes (x, lx), so the file lints clean. Red today: both allows are
// reported unreachable.
func TestPolicyLintUnreachableTreatsAGlobSubAsASet(t *testing.T) {
	withOwnerTerminal(t) // lint against the whole instance is the owner's (D-173)
	ts := policyRig(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:lock.*/attr/*": [read]
      "entity:device_tracker.jan/attr/l*": [read]
    deny:
      "entity:lock.*/attr/?": [read]
      "entity:device_tracker.jan/attr/l?": [read]
`)
	out, err := runPolicyCLI(t, ts.dir, "policy", "lint", "--json")
	d := decodeLint(t, out)
	if len(d.Findings) != 0 || len(d.Unchecked) != 0 || cliExitCode(err) != 0 {
		t.Errorf("exit %d findings %v unchecked %+v, want a clean lint (exit 0)\n%s", cliExitCode(err), d.codes(), d.Unchecked, out)
	}
}

// --- D-60: lint prose -------------------------------------------------------------------

// TestPolicyLintProseNamesEveryBrokenProblemAsFileLine — findings 11 and 33
// (bugs): in prose a broken file's findings are its problems as
// `<file>:<line>: <message>`, the form `policy show` uses, every one of them.
// Red today: lint prints `  broken  line 7: …` under a `file:` header.
func TestPolicyLintProseNamesEveryBrokenProblemAsFileLine(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyBroken)
	file := filepath.Join(dir, policy.FileName)
	jsonOut, _ := runPolicyCLI(t, dir, "policy", "lint", "--json")
	d := decodeLint(t, jsonOut)
	out, err := runPolicyCLI(t, dir, "policy", "lint")
	if cliExitCode(err) != 1 {
		t.Fatalf("exit %d, want 1", cliExitCode(err))
	}
	if len(d.Findings) != 3 {
		t.Fatalf("--json findings %+v, want the three problems", d.Findings)
	}
	for _, f := range d.Findings {
		want := fmt.Sprintf("%s:%d: %s", file, f.Line, f.Message)
		if !strings.Contains(out, want) {
			t.Errorf("lint prose lacks %q:\n%s", want, out)
		}
	}
}

// --- lint: one --timeout, not one per kind -----------------------------------------

// stalledHA accepts every connection and answers nothing until the test ends.
func stalledHA(t *testing.T) string {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs first (LIFO): handlers return, then Close
	return srv.URL
}

func runPolicyCLIWithTimeout(t *testing.T, dir, timeout string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"hactl"}, args...)
	full = append(full, "--dir", dir, "--timeout", timeout, "--tokensmax", "0")
	var buf bytes.Buffer
	err := RunWithOutput(full, &buf)
	return buf.String(), err
}

const everyHAKindPolicy = `version: 1
active: agent
profiles:
  agent:
    default: deny
    deny:
      "entity:lock.*": [write]
      "device:devhall": [delete]
      "area:hall_id": [delete]
      "label:energy": [delete]
      "automation:pg_*": [delete]
      "dashboard:main": [delete]
      "service:lock.unlock": [control]
`

// TestPolicyLintStopsAskingAfterTheFirstTransportFailure — finding 20
// (decided in the manual): after the first transport failure every Home
// Assistant-backed kind not yet read is unchecked with that same error, so a
// stalled host costs one --timeout, not one per kind. Red today: each kind
// dials on its own budget (7 kinds × 1s) and carries its own error text.
func TestPolicyLintStopsAskingAfterTheFirstTransportFailure(t *testing.T) {
	wantKinds := []string{"entity", "device", "area", "label", "automation", "dashboard", "service"}
	for _, tc := range []struct {
		name, url string
	}{
		{"stalled", stalledHA(t)},
		{"refused", closedHAURL(t)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := policyInstance(t, tc.url, everyHAKindPolicy)
			start := time.Now()
			out, err := runPolicyCLIWithTimeout(t, dir, "1s", "policy", "lint", "--json")
			elapsed := time.Since(start)
			if elapsed > 2500*time.Millisecond {
				t.Errorf("lint against a %s host took %v with --timeout 1s; want one timeout, not one per kind", tc.name, elapsed)
			}
			if cliExitCode(err) != 1 {
				t.Errorf("exit %d, want 1 (unchecked kinds)", cliExitCode(err))
			}
			d := decodeLint(t, out)
			var kinds, reasons []string
			for _, u := range d.Unchecked {
				kinds = append(kinds, u.Kind)
				reasons = append(reasons, u.Reason)
			}
			if !slices.Equal(kinds, wantKinds) {
				t.Errorf("unchecked kinds %v, want %v (kinds table order)", kinds, wantKinds)
			}
			for _, r := range reasons {
				if r != reasons[0] || r == "" {
					t.Errorf("unchecked reasons %q, want one and the same transport error for every kind", reasons)
					break
				}
			}
		})
	}
}

// --- D-66: partial at the CLI -------------------------------------------------------

// TestPolicyCheckPartialVerdict — D-66 (finding 7): `partial` and
// `partial_denies` in --json, one `partial: <rule> (line <n>)` line per
// descendant deny in prose, and the exit code is what the partial resolved to
// (1 for a write verb, 0 for read). Red today: the parent is allowed whole and
// the keys are missing.
func TestPolicyCheckPartialVerdict(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*": [read, edit]
    deny:
      "entity:*/registry/entity_id": [edit]
      "entity:device_tracker.*/attr/latitude": [read]
`)
	regDeny := `deny "entity:*/registry/entity_id" [edit]`
	latDeny := `deny "entity:device_tracker.*/attr/latitude" [read]`
	for _, tc := range []struct {
		res, verb string
		exit      int
		allowed   bool
		rule      string
		line      int
		partial   bool
		denies    []map[string]any
	}{
		{"entity:light.x", "edit", 1, false, regDeny, 9, true, []map[string]any{{"rule": regDeny, "line": 9}}},
		{"entity:device_tracker.jan", "read", 0, true, `allow "entity:*" [read edit]`, 7, true, []map[string]any{{"rule": latDeny, "line": 10}}},
		{"entity:light.x", "read", 0, true, `allow "entity:*" [read edit]`, 7, false, []map[string]any{}},
		{"entity:light.x/registry/entity_id", "edit", 1, false, regDeny, 9, false, []map[string]any{}},
	} {
		what := tc.res + " " + tc.verb
		t.Run(what, func(t *testing.T) {
			out, err := runPolicyCLI(t, dir, "policy", "check", tc.res, tc.verb, "--json")
			if cliExitCode(err) != tc.exit {
				t.Errorf("exit %d, want %d\n%s", cliExitCode(err), tc.exit, out)
			}
			m := decodeObject(t, out)
			requireKeys(t, what, m, append(slices.Clone(checkKeys), "partial", "partial_denies")...)
			wantField(t, what, m, "allowed", tc.allowed)
			wantField(t, what, m, "rule", tc.rule)
			wantField(t, what, m, "line", tc.line)
			wantField(t, what, m, "partial", tc.partial)
			wantField(t, what, m, "partial_denies", tc.denies)

			prose, _ := runPolicyCLI(t, dir, "policy", "check", tc.res, tc.verb)
			n := strings.Count(prose, "\npartial: ")
			if n != len(tc.denies) {
				t.Errorf("prose has %d `partial:` line(s), want %d:\n%s", n, len(tc.denies), prose)
			}
			for _, dd := range tc.denies {
				if want := fmt.Sprintf("partial: %s (line %d)", dd["rule"], dd["line"]); !strings.Contains(prose, want) {
					t.Errorf("prose lacks %q:\n%s", want, prose)
				}
			}
		})
	}
}

// --- D-63 / D-67 / D-68 at the CLI -------------------------------------------------

// TestPolicyCheckRefusesIDsTheGrammarRefuses — D-67 and D-68 (findings 4,
// 6): a resource with a dot segment, an ambiguous block segment, a character
// outside its kind's charset or trailing white space is refused — exit 1,
// nothing on stdout. Red today: each parses and gets a verdict (the trailing
// space slips past the exact deny).
func TestPolicyCheckRefusesIDsTheGrammarRefuses(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), `version: 1
active: agent
profiles:
  agent:
    allow:
      "file:**": [read]
    deny:
      "entity:lock.front_door": [read]
      "file:secrets.yaml": [read]
`)
	for _, res := range []string{
		"entity:lock.front_door ",
		"entity:lock.front_door\t",
		"file:./secrets.yaml",
		"file:packages/../secrets.yaml",
		"file:a/block/b/c.yaml",
		"file:block/x",
		"entity:light.wohn-zimmer",
		"area:küche",
		"label:agent ok",
		"device:abc\u00a0def",
	} {
		out, err := runPolicyCLI(t, dir, "policy", "check", res, "read")
		if cliExitCode(err) != 1 || out != "" {
			t.Errorf("policy check %q read: exit %d stdout %q, want a refusal (exit 1, nothing on stdout)", res, cliExitCode(err), out)
		}
	}
}

// TestPolicyShowReportsDecisionBrokenPatterns — D-63/D-67/D-68 under D-60:
// each such pattern is a broken-file problem `<file>:<line>: …`, exit 1.
func TestPolicyShowReportsDecisionBrokenPatterns(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:hallway": [read]
      "service:turn_on": [control]
      "file:./x.yaml": [read]
      "file:a/block": [read]
      "area:wohn-zimmer": [read]
      "entity:lock.* ": [read]
`)
	file := filepath.Join(dir, policy.FileName)
	out, err := runPolicyCLI(t, dir, "policy", "show")
	if cliExitCode(err) != 1 {
		t.Errorf("exit %d, want 1", cliExitCode(err))
	}
	for line := 7; line <= 12; line++ {
		if want := fmt.Sprintf("%s:%d: ", file, line); !strings.Contains(out, want) {
			t.Errorf("show does not report %q:\n%s", want, out)
		}
	}
}

// --- D-60: dangling symlink at the CLI ------------------------------------------------

// TestPolicyDanglingSymlinkIsNeverNoFile — finding 0 (bug): with a dangling
// policy.yaml symlink, show and lint report a problem naming the path (exit
// 1), check refuses (exit 1, nothing on stdout) — none says `no policy file`.
// Red today: all three answer as if there were no file (exit 0, allow-all).
func TestPolicyDanglingSymlinkIsNeverNoFile(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), "")
	link := filepath.Join(dir, policy.FileName)
	if err := os.Symlink(filepath.Join(dir, "gone", "policy.yaml"), link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	for _, args := range [][]string{{"policy", "show"}, {"policy", "lint"}, {"policy", "check", "entity:lock.x", "control"}} {
		out, err := runPolicyCLI(t, dir, args...)
		if cliExitCode(err) != 1 {
			t.Errorf("%v: exit %d, want 1\n%s", args, cliExitCode(err), out)
		}
		if strings.Contains(out, policy.RuleNoFile) {
			t.Errorf("%v says %q for a dangling policy.yaml:\n%s", args, policy.RuleNoFile, out)
		}
		text := out
		if err != nil {
			text += err.Error()
		}
		if !strings.Contains(text, link) {
			t.Errorf("%v does not name %s:\n%s", args, link, text)
		}
		if args[1] == "check" && out != "" {
			t.Errorf("check wrote a verdict for a policy it cannot read: %q", out)
		}
	}
}

// --- selection and show fields ----------------------------------------------------------

// TestPolicyCheckSelectedByOnEverySelection — findings 16 (M25) and 30
// (test-gaps): check --json carries selected_by for every row of the manual's
// table, and what_if true exactly under --as. Red under `SelectedBy: "active"`
// hard-coded in runPolicyCheck.
func TestPolicyCheckSelectedByOnEverySelection(t *testing.T) {
	withDefault := strings.Replace(policyManualExample, "active: agent                     # the profile in effect, for CLI and MCP alike\n", "", 1)
	for _, tc := range []struct {
		name, body string
		as         string
		selected   string
		whatIf     bool
	}{
		{"active", policyManualExample, "", "active", false},
		{"default-profile", withDefault, "", "default-profile", false},
		{"implicit-allow-all", "version: 1\nprofiles:\n  agent:\n    default: deny\n", "", "implicit-allow-all", false},
		{"no-file", "", "", "no-file", false},
		{"what-if", policyManualExample, "default", "what-if", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := policyInstance(t, closedHAURL(t), tc.body)
			args := []string{"policy", "check", "entity:sensor.t", "read", "--json"}
			if tc.as != "" {
				args = append(args, "--as", tc.as)
			}
			out, _ := runPolicyCLI(t, dir, args...)
			m := decodeObject(t, out)
			wantField(t, tc.name, m, "selected_by", tc.selected)
			wantField(t, tc.name, m, "what_if", tc.whatIf)
		})
	}
}

// TestPolicyShowListsTheDeclaredProfilesAndEmptySelectorKeys — finding 16
// (M17, M15; test-gap): show's prose names the declared profiles, and a
// selector's absent key renders as [] (never null) in --json.
func TestPolicyShowListsTheDeclaredProfilesAndEmptySelectorKeys(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyManualExample)
	out, err := runPolicyCLI(t, dir, "policy", "show")
	if cliExitCode(err) != 0 {
		t.Fatalf("exit %d", cliExitCode(err))
	}
	found := false
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "profiles") && strings.Contains(line, "agent") && strings.Contains(line, "default") {
			found = true
		}
	}
	if !found {
		t.Errorf("show prose has no line naming the declared profiles (agent, default) — red under M17:\n%s", out)
	}

	out, _ = runPolicyCLI(t, dir, "policy", "show", "--json")
	var doc struct {
		Rules []struct {
			Pattern string                     `json:"pattern"`
			Where   map[string]json.RawMessage `json:"where"`
		} `json:"rules"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	seen := false
	for _, r := range doc.Rules {
		if r.Pattern != "entity:light.*" {
			continue
		}
		seen = true
		if got := string(r.Where["label"]); got != "[]" {
			t.Errorf("where.label = %s, want [] — red under M15 (null)", got)
		}
	}
	if !seen {
		t.Errorf("no rule entity:light.* in show --json")
	}
}

// TestPolicyShowListsSameLineRulesByColumn — finding 9 (bug), D-52
// confirmation: `policy show` lists rules in file order — line, then column —
// in prose and --json. Red today: same-line rules sort by pattern text.
func TestPolicyShowListsSameLineRulesByColumn(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), `version: 1
active: agent
profiles:
  agent:
    default: deny
    deny: {"entity:lock.*": [write], "entity:*": [control]}
    allow: {"entity:zone.*": [read], "entity:all.*": [read]}
`)
	out, _ := runPolicyCLI(t, dir, "policy", "show", "--json")
	var doc struct {
		Rules []struct {
			Pattern string `json:"pattern"`
		} `json:"rules"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(doc.Rules))
	for _, r := range doc.Rules {
		got = append(got, r.Pattern)
	}
	want := []string{"entity:lock.*", "entity:*", "entity:zone.*", "entity:all.*"}
	if !slices.Equal(got, want) {
		t.Errorf("show --json rules %q, want %q", got, want)
	}
	prose, _ := runPolicyCLI(t, dir, "policy", "show")
	if a, b := strings.Index(prose, `"entity:zone.*"`), strings.Index(prose, `"entity:all.*"`); a < 0 || b < 0 || a > b {
		t.Errorf("show prose lists entity:all.* before entity:zone.* (the leftmost comes first):\n%s", prose)
	}
	check, _ := runPolicyCLI(t, dir, "policy", "check", "entity:lock.door", "control")
	if !strings.Contains(check, `rule: deny "entity:lock.*" [write] (line 6)`) {
		t.Errorf("check names the wrong same-line deny:\n%s", check)
	}
}

// --- D-55: contacts Home Assistant only when a selector can matter ------------------

// TestPolicyCheckIgnoresAMootSelector — findings 13, 17 and 32: with Home
// Assistant unreachable, a check decided by an earlier plain allow exits 0
// with no notes; against a counting stand-in it sends no request. Red today
// (finding 13): the later narrowed allow is still evaluated — a dial and a note.
func TestPolicyCheckIgnoresAMootSelector(t *testing.T) {
	body := `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*": [control]
      "entity:light.*": [control]
    where:
      "entity:light.*": { area: wohnzimmer }
`
	dir := policyInstance(t, closedHAURL(t), body)
	out, err := runPolicyCLI(t, dir, "policy", "check", "entity:light.sofa", "control", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("exit %d (%v), want 0\n%s", cliExitCode(err), err, out)
	}
	m := decodeObject(t, out)
	wantField(t, "moot", m, "line", 7)
	wantField(t, "moot", m, "notes", []any{})

	url, hits := countingHA(t)
	dir = policyInstance(t, url, body)
	_, _ = runPolicyCLI(t, dir, "policy", "check", "entity:light.sofa", "control")
	if n := hits.Load(); n != 0 {
		t.Errorf("a check decided by an earlier plain allow sent %d request(s) to Home Assistant, want 0", n)
	}
}

// --- `?` in --pattern: the shared engine -------------------------------------------------

// TestFilterQuestionMarkIsOneCharacter — findings 5 and 8: the filters share
// the engine, so `?` is one code point in --pattern too.
func TestFilterQuestionMarkIsOneCharacter(t *testing.T) {
	for _, tc := range []struct{ s, p string }{
		{"Küche", "K?che"},
		{"input_select.küche", "k?che"},
		{"input_select.küche", "input_select.k?che"},
	} {
		if !matchPattern(tc.s, tc.p) {
			t.Errorf("matchPattern(%q, %q) = false, want true (`?` is one character)", tc.s, tc.p)
		}
	}
}

// --- D-64: a pinned MCP server keeps its instance ------------------------------------------

// TestPinnedMCPServerRefusesAnotherInstanceDir — D-64 (finding 2), extending
// the no-call-time-selector tests to the MCP path end to end: a server pinned
// to an instance whose policy denies refuses a tool call that re-points --dir
// at another instance (no policy = allow-all) — runner never called — and
// HACTL_DIR cannot re-point it either. A call naming the pinned directory
// runs. Red today: `--dir <other>` wins and answers `allowed … no policy file`.
func TestPinnedMCPServerRefusesAnotherInstanceDir(t *testing.T) {
	pinned := policyInstance(t, closedHAURL(t), "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n")
	other := policyInstance(t, closedHAURL(t), "")

	var calls atomic.Int64
	srv := mcpserver.NewServer(mcpserver.Options{
		Runner: func(ctx context.Context, args []string, w io.Writer) error {
			calls.Add(1)
			return RunWithOutputContext(ctx, args, w)
		},
		ResolvePath:    FindCommandPath,
		Dir:            pinned,
		NoManualInject: true,
	})
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	srvSession, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srvSession.Wait() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	call := func(command string) (string, bool) {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hactl", Arguments: map[string]any{"command": command}})
		if err != nil {
			t.Fatalf("CallTool(%q): %v", command, err)
		}
		var sb strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				sb.WriteString(tc.Text)
			}
		}
		return sb.String(), res.IsError
	}

	for _, cmdline := range []string{
		"policy check entity:lock.x control --dir " + other,
		"policy check entity:lock.x control --dir=" + other,
		"--dir " + other + " policy check entity:lock.x control",
		"policy check entity:lock.x control --dir=",
		"policy check entity:lock.x control --dir ''",
		"policy check --as -- --dir " + other + " entity:lock.x control",
		"policy show --as -- --dir " + other,
	} {
		before := calls.Load()
		text, isErr := call(cmdline)
		if !isErr {
			t.Errorf("%q: not an error result; a pinned server must refuse another --dir (D-64)\n%s", cmdline, text)
		}
		if calls.Load() != before {
			t.Errorf("%q: the runner was called; a refused call runs nothing", cmdline)
		}
		if strings.Contains(text, "allowed:") || !strings.Contains(text, "pinned") {
			t.Errorf("%q: not refused as pinned (D-64), or answered from the other instance:\n%s", cmdline, text)
		}
	}

	t.Setenv("HACTL_DIR", other)
	for _, cmdline := range []string{
		"policy check entity:lock.x control",
		"policy check entity:lock.x control --dir " + pinned,
	} {
		before := calls.Load()
		text, _ := call(cmdline)
		if calls.Load() != before+1 {
			t.Errorf("%q: runner calls %d→%d, want the call to run", cmdline, before, calls.Load())
		}
		if !strings.Contains(text, "denied: entity:lock.x control") || !strings.Contains(text, "agent") {
			t.Errorf("%q with HACTL_DIR=<other>: want the pinned instance's verdict (denied, profile agent):\n%s", cmdline, text)
		}
	}
}
