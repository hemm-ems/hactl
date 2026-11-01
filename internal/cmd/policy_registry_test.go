package cmd

import (
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// The registry family's plans (manual "What each write command checks"):
// entity and device registry fields at the finest address, area/floor/label
// creates kind-wide (D-75), deletes with their cascades (D-76), config entries
// and their flows (D-74, D-75, D-83, A-5), config show's probe (D-79) and the
// local cache (D-79). Every expected plan below is written from that table,
// not read off the code.

// registryRig is a small instance: two labels, two areas on two floors, three
// entities and two devices, two config entries — enough that every cascade has
// a holder to name and a non-holder to leave out.
//
//	light.a   area hall, label night, entry e1, device d1
//	light.b   no own area, entry e1, device d1
//	lock.door area attic, label night, entry e2
//	d1        area hall, label night, entries [e1]
//	d2        area attic, entries [e1 e2]  (a device of another entry too)
//	hall      floor ground, label night;  attic  floor top
type registryRig struct {
	*cmdTestServer

	restMu sync.Mutex
	rest   []string // "METHOD path" of every REST request
}

func (r *registryRig) restSent(key string) int {
	r.restMu.Lock()
	defer r.restMu.Unlock()
	n := 0
	for _, k := range r.rest {
		if k == key {
			n++
		}
	}
	return n
}

func (r *registryRig) wsSent(typ string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.cmdCounts[typ]
}

// sent counts a write by its key: a websocket type, or "METHOD /path".
func (r *registryRig) sent(key string) int {
	if strings.Contains(key, " ") {
		return r.restSent(key)
	}
	return r.wsSent(key)
}

func registryRigWS() map[string]any {
	return map[string]any{
		"config/label_registry/list": []any{
			map[string]any{"label_id": "night", "name": "Night"},
			map[string]any{"label_id": "secret", "name": "Secret"},
		},
		"config/area_registry/list": []any{
			map[string]any{"area_id": "hall", "name": "Hall", "floor_id": "ground", "labels": []string{"night"}},
			map[string]any{"area_id": "attic", "name": "Attic", "floor_id": "top", "labels": []string{}},
		},
		"config/floor_registry/list": []any{
			map[string]any{"floor_id": "ground", "name": "Ground", "level": 0},
			map[string]any{"floor_id": "top", "name": "Top", "level": 1},
		},
		"config/entity_registry/list": []any{
			map[string]any{"entity_id": "light.a", "area_id": "hall", "labels": []string{"night"}, "config_entry_id": "e1", "device_id": "d1", "unique_id": "ua", "platform": "hue"},
			map[string]any{"entity_id": "light.b", "labels": []string{}, "config_entry_id": "e1", "device_id": "d1", "unique_id": "ub", "platform": "hue"},
			map[string]any{"entity_id": "lock.door", "area_id": "attic", "labels": []string{"night"}, "config_entry_id": "e2", "unique_id": "uc", "platform": "zwave_js"},
		},
		"config/device_registry/list": []any{
			map[string]any{"id": "d1", "name": "Bridge", "area_id": "hall", "labels": []string{"night"}, "config_entries": []string{"e1"}},
			map[string]any{"id": "d2", "name": "Hub", "area_id": "attic", "labels": []string{}, "config_entries": []string{"e1", "e2"}},
		},
		"config/entity_registry/update": map[string]any{},
		"config/device_registry/update": map[string]any{},
		"config/label_registry/create":  map[string]any{"label_id": "garden", "name": "Garden"},
		"config/area_registry/create":   map[string]any{"area_id": "garden", "name": "Garden"},
		"config/floor_registry/create":  map[string]any{"floor_id": "cellar", "name": "Cellar"},
		"config/label_registry/delete":  nil,
		"config/area_registry/delete":   nil,
		"config/floor_registry/delete":  nil,
		"trace/list":                    []any{},
		"system_log/list":               []any{},
	}
}

const (
	rigEntries     = `[{"entry_id":"e1","domain":"hue","title":"Hue","state":"loaded","source":"user","supports_options":true},{"entry_id":"e2","domain":"zwave_js","title":"Z-Wave","state":"loaded","source":"user","supports_options":true}]`
	rigOptionsForm = `{"type":"form","flow_id":"of1","handler":"e1","step_id":"init","data_schema":[{"name":"interval","default":30}]}`
	rigConfigForm  = `{"type":"form","flow_id":"cf1","handler":"hue","step_id":"user","data_schema":[]}`
)

// registryRigHTTP answers Home Assistant's config entry and flow routes;
// record, when set, sees every REST request as "METHOD path".
func registryRigHTTP(record func(string)) map[string]http.HandlerFunc {
	h := func(body string, status int) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if record != nil {
				record(r.Method + " " + r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, body)
		}
	}
	return map[string]http.HandlerFunc{
		"/api/config/config_entries/entry":         h(rigEntries, http.StatusOK),
		"/api/config/config_entries/entry/":        h(`{"require_restart":false}`, http.StatusOK),
		"/api/config/config_entries/options/flow":  h(rigOptionsForm, http.StatusOK),
		"/api/config/config_entries/options/flow/": h(rigOptionsForm, http.StatusOK),
		"/api/config/config_entries/flow":          h(rigConfigForm, http.StatusOK),
		"/api/config/config_entries/flow/":         h(rigConfigForm, http.StatusOK),
		"/api/config/config_entries/flow_handlers": h(`["hue"]`, http.StatusOK),
		"/api/diagnostics/config_entry/":           h(`{"message":"not found"}`, http.StatusNotFound),
		"/api/error_log":                           h(``, http.StatusOK),
	}
}

func startRegistryRig(t *testing.T) *registryRig {
	t.Helper()
	r := &registryRig{}
	r.cmdTestServer = startCmdServer(t, registryRigWS(), registryRigHTTP(func(k string) {
		r.restMu.Lock()
		r.rest = append(r.rest, k)
		r.restMu.Unlock()
	}))
	return r
}

// denyPolicy is a profile `agent` whose deny rules start at line 6.
func denyPolicy(rules ...string) string {
	var b strings.Builder
	b.WriteString("version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n")
	for _, r := range rules {
		b.WriteString("      " + r + "\n")
	}
	return b.String()
}

// registryCase is one command of the family: its run (a preview, or the run
// itself for a command without --confirm), the plan the manual's table gives
// it, one deny that refuses it, and the write that refusal must keep back.
type registryCase struct {
	name     string
	args     []string
	confirm  bool     // the command has --confirm
	plan     []string // the manual's table, in any order
	deny     string   // a rule refusing one pair of the plan
	denied   string   // the refusal's stderr line after "policy_denied: "
	write    string   // a websocket type or "METHOD /path" that must not be sent
	allowedB string   // a rule on a sibling address that must NOT refuse it ("" = none)
}

func registryCases() []registryCase {
	return []registryCase{
		{
			name: "ent set-label", args: []string{"ent", "set-label", "light.a", "secret"}, confirm: true,
			plan:     []string{"edit entity:light.a/registry/labels"},
			deny:     `"entity:light.a/registry/labels": [edit]`,
			denied:   `edit entity:light.a/registry/labels — deny "entity:light.a/registry/labels" [edit] (line 6)`,
			write:    "config/entity_registry/update",
			allowedB: `"entity:light.a/registry/area_id": [edit]`,
		},
		{
			name: "ent set-label --remove", args: []string{"ent", "set-label", "light.a", "--remove", "night"}, confirm: true,
			plan:   []string{"edit entity:light.a/registry/labels"},
			deny:   `"entity:*/registry/labels": [edit]`,
			denied: `edit entity:light.a/registry/labels — deny "entity:*/registry/labels" [edit] (line 6)`,
			write:  "config/entity_registry/update",
		},
		{
			name: "ent set-area", args: []string{"ent", "set-area", "light.a", "attic"}, confirm: true,
			plan:     []string{"edit entity:light.a/registry/area_id"},
			deny:     `"entity:light.*/registry/area_id": [edit]`,
			denied:   `edit entity:light.a/registry/area_id — deny "entity:light.*/registry/area_id" [edit] (line 6)`,
			write:    "config/entity_registry/update",
			allowedB: `"entity:light.a/registry/labels": [edit]`,
		},
		{
			name: "ent set-area --clear", args: []string{"ent", "set-area", "light.a", "--clear"}, confirm: true,
			plan:   []string{"edit entity:light.a/registry/area_id"},
			deny:   `"entity:light.a": [edit]`,
			denied: `edit entity:light.a/registry/area_id — deny "entity:light.a" [edit] (line 6)`,
			write:  "config/entity_registry/update",
		},
		{
			name: "device set-area", args: []string{"device", "set-area", "Bridge", "attic"}, confirm: true,
			plan:     []string{"edit device:d1/registry/area_id"},
			deny:     `"device:d1/registry/area_id": [edit]`,
			denied:   `edit device:d1/registry/area_id — deny "device:d1/registry/area_id" [edit] (line 6)`,
			write:    "config/device_registry/update",
			allowedB: `"device:d1/registry/labels": [edit]`,
		},
		{
			name: "device set-area --clear", args: []string{"device", "set-area", "d1", "--clear"}, confirm: true,
			plan:   []string{"edit device:d1/registry/area_id"},
			deny:   `"device:*": [write]`,
			denied: `edit device:d1/registry/area_id — deny "device:*" [write] (line 6)`,
			write:  "config/device_registry/update",
		},
		{
			name: "device set-label", args: []string{"device", "set-label", "d1", "secret"}, confirm: true,
			plan:     []string{"edit device:d1/registry/labels"},
			deny:     `"device:d1/registry/labels": [edit]`,
			denied:   `edit device:d1/registry/labels — deny "device:d1/registry/labels" [edit] (line 6)`,
			write:    "config/device_registry/update",
			allowedB: `"label:secret": [edit]`, // assigning a label is not editing it
		},
		{
			name: "area create", args: []string{"area", "create", "Garden"}, confirm: true,
			plan:     []string{"create area:*"},
			deny:     `"area:secret": [create]`,
			denied:   `create area:* — deny "area:secret" [create] (line 6)`,
			write:    "config/area_registry/create",
			allowedB: `"area:*": [delete]`,
		},
		{
			name: "floor create", args: []string{"floor", "create", "Cellar"}, confirm: true,
			plan:   []string{"create floor:*"},
			deny:   `"floor:basement": [create]`,
			denied: `create floor:* — deny "floor:basement" [create] (line 6)`,
			write:  "config/floor_registry/create",
		},
		{
			name: "area delete", args: []string{"area", "delete", "hall"}, confirm: true,
			plan:     []string{"delete area:hall", "edit entity:light.a/registry/area_id", "edit device:d1/registry/area_id"},
			deny:     `"device:d1/registry/area_id": [edit]`,
			denied:   `edit device:d1/registry/area_id — deny "device:d1/registry/area_id" [edit] (line 6)`,
			write:    "config/area_registry/delete",
			allowedB: `"entity:light.b/registry/area_id": [edit]`, // inherits the device's area: not written
		},
		{
			name: "floor delete", args: []string{"floor", "delete", "ground"}, confirm: true,
			plan:     []string{"delete floor:ground", "edit area:hall"},
			deny:     `"area:hall": [edit]`,
			denied:   `edit area:hall — deny "area:hall" [edit] (line 6)`,
			write:    "config/floor_registry/delete",
			allowedB: `"area:attic": [edit]`,
		},
		{
			name: "label delete", args: []string{"label", "delete", "night"}, confirm: true,
			plan: []string{
				"delete label:night", "edit entity:light.a/registry/labels", "edit entity:lock.door/registry/labels",
				"edit device:d1/registry/labels", "edit area:hall",
			},
			deny:     `"entity:lock.*/registry/labels": [edit]`,
			denied:   `edit entity:lock.door/registry/labels — deny "entity:lock.*/registry/labels" [edit] (line 6)`,
			write:    "config/label_registry/delete",
			allowedB: `"entity:light.b/registry/labels": [edit]`,
		},
		{
			name: "config delete", args: []string{"config", "delete", "e1"}, confirm: true,
			plan: []string{
				"delete config_entry:e1", "delete entity:light.a", "delete entity:light.b",
				"delete device:d1", "delete device:d2",
			},
			deny:     `"device:d2": [delete]`, // a device of another entry too: still checked
			denied:   `delete device:d2 — deny "device:d2" [delete] (line 6)`,
			write:    "DELETE /api/config/config_entries/entry/e1",
			allowedB: `"entity:lock.door": [delete]`,
		},
		{
			name: "config options", args: []string{"config", "options", "e1"}, confirm: true,
			plan:     []string{"edit config_entry:e1/options"},
			deny:     `"config_entry:e1/options": [edit]`,
			denied:   `edit config_entry:e1/options — deny "config_entry:e1/options" [edit] (line 6)`,
			write:    "POST /api/config/config_entries/options/flow",
			allowedB: `"config_entry:e2/options": [edit]`,
		},
		{
			name: "config flow-start", args: []string{"config", "flow-start", "hue"}, confirm: true,
			plan: []string{"create config_entry:*"},
			// a deny on one id refuses a check on every id (D-75); create applies
			// at no part of an entry, so a deny there would decide nothing (D-110)
			deny:   `"config_entry:e2": [create]`,
			denied: `create config_entry:* — deny "config_entry:e2" [create] (line 6)`,
			write:  "POST /api/config/config_entries/flow",
		},
		{
			name: "config flow-step", args: []string{"config", "flow-step", "cf1"}, confirm: true,
			plan:   []string{"create config_entry:*", "edit config_entry:*"},
			deny:   `"config_entry:e2": [edit]`, // the flow may reconfigure any entry
			denied: `edit config_entry:* — deny "config_entry:e2" [edit] (line 6)`,
			// the preview's own GET runs the step: it must not be sent either
			write: "GET /api/config/config_entries/flow/cf1",
		},
		{
			name: "config flow-step --options", args: []string{"config", "flow-step", "of1", "--options"}, confirm: true,
			plan:     []string{"edit config_entry:*/options"},
			deny:     `"config_entry:e2/options": [edit]`, // which entry's flow is unknown (A-5)
			denied:   `edit config_entry:*/options — deny "config_entry:e2/options" [edit] (line 6)`,
			write:    "GET /api/config/config_entries/options/flow/of1",
			allowedB: `"config_entry:*": [create]`,
		},
		{
			name: "config flow-inspect", args: []string{"config", "flow-inspect", "cf1"},
			plan:   []string{"create config_entry:*", "edit config_entry:*"},
			deny:   `"config_entry:*": [create]`,
			denied: `create config_entry:* — deny "config_entry:*" [create] (line 6)`,
			write:  "GET /api/config/config_entries/flow/cf1",
		},
		{
			name: "config flow-inspect --options", args: []string{"config", "flow-inspect", "of1", "--options"},
			plan:     []string{"edit config_entry:*/options"},
			deny:     `"config_entry:*/options": [edit]`,
			denied:   `edit config_entry:*/options — deny "config_entry:*/options" [edit] (line 6)`,
			write:    "GET /api/config/config_entries/options/flow/of1",
			allowedB: `"config_entry:*/flow": [edit]`,
		},
		{
			name: "cache refresh", args: []string{"cache", "refresh"},
			plan:     []string{"edit system:cache"},
			deny:     `"system:cache": [edit]`,
			denied:   `edit system:cache — deny "system:cache" [edit] (line 6)`,
			write:    "trace/list", // its first request: refused before the refresh starts
			allowedB: `"system:cache": [delete]`,
		},
		{
			name: "cache clear", args: []string{"cache", "clear"},
			plan:     []string{"delete system:cache"},
			deny:     `"system:*": [delete]`,
			denied:   `delete system:cache — deny "system:*" [delete] (line 6)`,
			allowedB: `"system:cache": [edit]`,
		},
	}
}

func sortedCopy(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

// TestRegistryFamilyPlansAreTheManualsTable: each command's plan — recorded as
// the session checks it under an allow-all profile, in the preview and under
// --confirm alike (H-2) — is exactly the manual's row for it: the field-level
// address, the kind-wide create, every cascade holder and no non-holder.
func TestRegistryFamilyPlansAreTheManualsTable(t *testing.T) {
	for _, tc := range registryCases() {
		t.Run(tc.name, func(t *testing.T) {
			rig := startRegistryRig(t)
			writePolicyFile(t, rig.dir, policyAllowEverything)
			runs := [][]string{tc.args}
			if tc.confirm {
				runs = append(runs, append(slices.Clone(tc.args), "--confirm"))
			}
			for _, args := range runs {
				rec := &coverageRecorder{}
				policyRecorder = rec
				out, err := runPolicyCLI(t, rig.dir, args...)
				policyRecorder = nil
				if err != nil {
					t.Fatalf("%v: %v\n%s", args, err, out)
				}
				got := make([]string, 0, len(rec.plan))
				for _, c := range rec.plan {
					got = append(got, c.String())
				}
				if !slices.Equal(sortedCopy(got), sortedCopy(tc.plan)) {
					t.Errorf("%v: plan = %q, want the manual's %q", args, got, tc.plan)
				}
				if missing := uncoveredChecks(rec.plan, rec.requests); len(missing) > 0 {
					t.Errorf("%v: sent writes its plan does not hold: %q", args, missing)
				}
				if tc.confirm && !slices.Contains(args, "--confirm") &&
					!strings.Contains(out, fmt.Sprintf(" %d checked, allowed (profile agent)\n", len(tc.plan))) {
					t.Errorf("%v: the allowed preview does not say what it checked:\n%s", args, out)
				}
			}
		})
	}
}

// TestRegistryFamilyRefusesItsPlanNamingTheRule (E-7, D-70): one deny on one
// pair of a plan refuses the command — the preview and --confirm alike, before
// the first write — naming that pair and the rule; the write is never sent. A
// deny on a sibling address the command does not write leaves it allowed (the
// finest address, D-66; a cascade's non-holders are not in it).
func TestRegistryFamilyRefusesItsPlanNamingTheRule(t *testing.T) {
	for _, tc := range registryCases() {
		t.Run(tc.name, func(t *testing.T) {
			rig := startRegistryRig(t)
			writePolicyFile(t, rig.dir, denyPolicy(tc.deny))
			runs := [][]string{tc.args}
			if tc.confirm {
				runs = append(runs, append(slices.Clone(tc.args), "--confirm"))
			}
			for _, args := range runs {
				assertRefusedNamingTheRule(t, rig.dir, tc, args)
			}
			if tc.write != "" && rig.sent(tc.write) != 0 {
				t.Errorf("a refused plan sent %s %d times", tc.write, rig.sent(tc.write))
			}
			if tc.allowedB == "" {
				return
			}
			writePolicyFile(t, rig.dir, denyPolicy(tc.allowedB))
			if out, err := runPolicyCLI(t, rig.dir, tc.args...); err != nil {
				t.Errorf("a deny on an address the command does not write (%s) refused it: %v\n%s", tc.allowedB, err, out)
			}
		})
	}
}

// assertRefusedNamingTheRule runs args and holds the refusal to tc: exit 1,
// policy_denied, the stderr line and the report naming the denied pair and the
// rule, and the whole plan counted.
func assertRefusedNamingTheRule(t *testing.T, dir string, tc registryCase, args []string) {
	t.Helper()
	out, err := runPolicyCLI(t, dir, args...)
	if cliExitCode(err) != 1 || policyErrorClass(err) != enforce.ClassDenied {
		t.Fatalf("%v: exit %d class %q (%v), want 1 policy_denied\n%s", args, cliExitCode(err), policyErrorClass(err), err, out)
	}
	if want := fmt.Sprintf("policy_denied: 1 of %d resources denied (profile agent)", len(tc.plan)); err.Error() != want {
		t.Errorf("%v: stderr line\n got: %q\nwant: %q", args, err.Error(), want)
	}
	if !strings.Contains(out, "  denied: "+tc.denied) || !strings.Contains(out, "refused by policy, nothing was written") {
		t.Errorf("%v: the report does not name the denial:\n%s", args, out)
	}
	if !strings.Contains(out, fmt.Sprintf("checked: %d resources", len(tc.plan))) {
		t.Errorf("%v: the report does not count the whole plan (%d):\n%s", args, len(tc.plan), out)
	}
}

// TestRegistryFamilyRefusalJSONIsThePreviewEnvelope (D-70, R-13): under --json
// a refused cascade lists every denied pair of the plan in the preview
// envelope, the kind-wide flag on a check of every id.
func TestRegistryFamilyRefusalJSONIsThePreviewEnvelope(t *testing.T) {
	rig := startRegistryRig(t)
	writePolicyFile(t, rig.dir, denyPolicy(`"entity:*/registry/labels": [edit]`, `"label:*": [create]`))
	out, err := runPolicyCLI(t, rig.dir, "label", "delete", "night", "--json")
	if cliExitCode(err) != 1 {
		t.Fatalf("exit %d (%v)\n%s", cliExitCode(err), err, out)
	}
	m := decodeObject(t, out)
	wantField(t, "refusal", m, "dry_run", true)
	wantField(t, "refusal", m, "error", "policy_denied")
	wantField(t, "refusal", m, "action", "delete label")
	pol, _ := m["details"].(map[string]any)["policy"].(map[string]any)
	wantField(t, "details.policy", pol, "checked", 5)
	wantField(t, "details.policy", pol, "written", []any{})
	rule := `deny "entity:*/registry/labels" [edit]`
	wantField(t, "details.policy", pol, "denied", []any{
		map[string]any{"resource": "entity:light.a/registry/labels", "verb": "edit", "rule": rule, "line": 6, "partial": false},
		map[string]any{"resource": "entity:lock.door/registry/labels", "verb": "edit", "rule": rule, "line": 6, "partial": false},
	})

	out, err = runPolicyCLI(t, rig.dir, "area", "create", "Garden", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("a label create deny refused area create: %v\n%s", err, out)
	}
	writePolicyFile(t, rig.dir, denyPolicy(`"area:x*": [create]`))
	out, _ = runPolicyCLI(t, rig.dir, "area", "create", "Garden", "--confirm", "--json")
	m = decodeObject(t, out)
	wantField(t, "confirm refusal", m, "dry_run", false)
	pol, _ = m["details"].(map[string]any)["policy"].(map[string]any)
	wantField(t, "details.policy", pol, "denied", []any{map[string]any{
		"resource": "area:*", "verb": "create", "rule": `deny "area:x*" [create]`, "line": 6, "partial": false, "kind_wide": true,
	}})
}

// TestConfigShowProbeSkippedWhenDenied (D-79): config show is a read; its
// options-flow probe is a write checked before it is sent. Denied, the probe
// is skipped with a note naming the rule, nothing is started, and config show
// still answers, exit 0. Allowed, the probe runs and its abort is held to the
// same entry (the start's answer names the handler, D-83).
func TestConfigShowProbeSkippedWhenDenied(t *testing.T) {
	rig := startRegistryRig(t)
	writePolicyFile(t, rig.dir, denyPolicy(`"config_entry:e1/options": [edit]`))
	for _, args := range [][]string{
		{"config", "show", "e1", "--probe-options-flow"},
		{"config", "show", "e1", "--probe-options-flow", "--json"},
	} {
		out, err := runPolicyCLI(t, rig.dir, args...)
		if err != nil {
			t.Fatalf("%v: config show was refused for its probe: %v\n%s", args, err, out)
		}
		want := `options-flow probe skipped: policy_denied: edit config_entry:e1/options — deny "config_entry:e1/options" [edit] (line 6)`
		if slices.Contains(args, "--json") {
			note, _ := decodeObject(t, out)["note"].(string)
			out = note + " " + out
		}
		if !strings.Contains(out, want) {
			t.Errorf("%v: no note naming the rule\nwant: %s\n got: %s", args, want, out)
		}
		if !strings.Contains(out, "hue") {
			t.Errorf("%v: config show did not answer:\n%s", args, out)
		}
	}
	if n := rig.restSent("POST /api/config/config_entries/options/flow"); n != 0 {
		t.Errorf("a denied probe started %d options flows", n)
	}
	audit := readAudit(t, rig.dir)
	if len(audit) != 2 || audit[0]["layer"] != "command" || audit[0]["allowed"] != false || audit[0]["resource"] != "config_entry:e1/options" {
		t.Errorf("the denied probe is not audited as a command-layer decision: %v", audit)
	}

	writePolicyFile(t, rig.dir, denyPolicy(`"config_entry:e2/options": [edit]`))
	out, err := runPolicyCLI(t, rig.dir, "config", "show", "e1", "--probe-options-flow")
	if err != nil || !strings.Contains(out, "source: options_flow") || !strings.Contains(out, "interval: 30") {
		t.Fatalf("an allowed probe did not run: %v\n%s", err, out)
	}
	if rig.restSent("POST /api/config/config_entries/options/flow") != 1 || rig.restSent("DELETE /api/config/config_entries/options/flow/of1") != 1 {
		t.Errorf("the allowed probe's start and abort: %v", rig.rest)
	}
}

// TestCacheClearRefusedKeepsTheCache (D-79): a refused cache clear removes
// nothing — the check comes before the first file is touched.
func TestCacheClearRefusedKeepsTheCache(t *testing.T) {
	rig := startRegistryRig(t)
	ids := filepath.Join(rig.dir, "cache", "ids.json")
	if err := os.MkdirAll(filepath.Dir(ids), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ids, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	writePolicyFile(t, rig.dir, denyPolicy(`"system:cache": [delete]`))
	if _, err := runPolicyCLI(t, rig.dir, "cache", "clear"); policyErrorClass(err) != enforce.ClassDenied {
		t.Fatalf("cache clear under a delete deny: %v", err)
	}
	if _, err := os.Stat(ids); err != nil {
		t.Errorf("a refused cache clear removed ids.json: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rig.dir, "cache", "traces.db")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused cache clear opened the cache (%v)", err)
	}

	// Allowed, a write without --confirm prints what it printed before: the
	// `policy:` line is a preview's (D-95).
	writePolicyFile(t, rig.dir, denyPolicy(`"system:cache": [edit]`))
	if out, err := runPolicyCLI(t, rig.dir, "cache", "clear"); err != nil || out != "cache cleared\n" {
		t.Errorf("an allowed cache clear: %q, %v", out, err)
	}
	if _, err := os.Stat(ids); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an allowed cache clear left ids.json (%v)", err)
	}
}

// TestRegistryFamilyWithoutAProfileAsksNothingMore (D-58): with no policy
// file, and with one that puts no profile in effect, each preview sends the
// requests it sent before the policy existed and no other — the cascades'
// registry reads (entity and device lists) are a profile's, never a preview's
// — prints no policy line and writes no audit log.
func TestRegistryFamilyWithoutAProfileAsksNothingMore(t *testing.T) {
	cases := []struct {
		args []string
		ws   map[string]int // every websocket type the preview sends
		rest []string       // every REST request
	}{
		{[]string{"area", "delete", "hall"}, map[string]int{"config/area_registry/list": 1}, nil},
		{[]string{"floor", "delete", "ground"}, map[string]int{"config/floor_registry/list": 1}, nil},
		{[]string{"label", "delete", "night"}, map[string]int{"config/label_registry/list": 1}, nil},
		{[]string{"area", "create", "Garden"}, map[string]int{}, nil},
		{[]string{"config", "delete", "e1"}, map[string]int{}, []string{"GET /api/config/config_entries/entry"}},
		{[]string{"config", "flow-step", "cf1"}, map[string]int{}, []string{"GET /api/config/config_entries/flow/cf1"}},
		{[]string{"ent", "set-area", "light.a", "attic"}, map[string]int{"config/area_registry/list": 1, "config/entity_registry/list": 1}, nil},
	}
	for name, body := range map[string]string{"no file": "", "implicit allow-all": policyImplicitAllow} {
		for _, tc := range cases {
			rig := startRegistryRig(t)
			if body != "" {
				writePolicyFile(t, rig.dir, body)
			}
			out, err := runPolicyCLI(t, rig.dir, tc.args...)
			if err != nil {
				t.Fatalf("%s %v: %v\n%s", name, tc.args, err, out)
			}
			if strings.Contains(out, "policy:") {
				t.Errorf("%s %v: a preview without a profile mentions the policy:\n%s", name, tc.args, out)
			}
			rig.mu.Lock()
			got := maps.Clone(rig.cmdCounts)
			rig.mu.Unlock()
			if fmt.Sprint(got) != fmt.Sprint(tc.ws) {
				t.Errorf("%s %v: websocket requests %v, want exactly %v", name, tc.args, got, tc.ws)
			}
			rig.restMu.Lock()
			rest := slices.Clone(rig.rest)
			rig.restMu.Unlock()
			if !slices.Equal(rest, tc.rest) {
				t.Errorf("%s %v: REST requests %q, want exactly %q", name, tc.args, rest, tc.rest)
			}
			if _, statErr := os.Stat(filepath.Join(rig.dir, "cache", enforce.AuditFileName)); !errors.Is(statErr, os.ErrNotExist) {
				t.Errorf("%s %v: an audit log was written", name, tc.args)
			}
		}
	}
}

// The family's commands join R-12's harness: each runs under an allow-all
// profile against the rig, and the request layer's verdicts must be a subset
// of its plan. flow-inspect and config show's probe have no --confirm; they
// are write sites all the same (D-80) and are held to their plans here too.
func init() {
	for _, c := range []struct {
		name string
		args []string
	}{
		{"ent set-label", []string{"ent", "set-label", "light.a", "secret", "--remove", "night", "--confirm"}},
		{"ent set-area", []string{"ent", "set-area", "light.a", "attic", "--confirm"}},
		{"ent set-area --clear", []string{"ent", "set-area", "light.a", "--clear", "--confirm"}},
		{"device set-area", []string{"device", "set-area", "d1", "attic", "--confirm"}},
		{"device set-label", []string{"device", "set-label", "d1", "secret", "--confirm"}},
		{"area create", []string{"area", "create", "Garden", "--floor", "top", "--confirm"}},
		{"area delete", []string{"area", "delete", "hall", "--confirm"}},
		{"floor create", []string{"floor", "create", "Cellar", "--level", "-1", "--confirm"}},
		{"floor delete", []string{"floor", "delete", "ground", "--confirm"}},
		{"label delete", []string{"label", "delete", "night", "--confirm"}},
		{"config delete", []string{"config", "delete", "e1", "--confirm"}},
		{"config options", []string{"config", "options", "e1", "--confirm"}},
		{"config flow-start", []string{"config", "flow-start", "hue", "--confirm"}},
		{"config flow-step", []string{"config", "flow-step", "cf1", "--data", `{"host":"x"}`, "--confirm"}},
		{"config flow-step --options", []string{"config", "flow-step", "of1", "--options", "--confirm"}},
		{"config flow-inspect", []string{"config", "flow-inspect", "cf1"}},
		{"config flow-inspect --options", []string{"config", "flow-inspect", "of1", "--options"}},
		{"config show --probe-options-flow", []string{"config", "show", "e1", "--probe-options-flow"}},
	} {
		confirmedCoverageCases = append(confirmedCoverageCases, confirmedCoverageCase{
			name: c.name, args: c.args, ws: registryRigWS(), http: registryRigHTTP(nil),
		})
	}
}
