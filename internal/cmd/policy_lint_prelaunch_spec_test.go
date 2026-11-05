package cmd

// Blind spec tests for the new `policy lint` findings and inventories of the
// pre-merge fixes (PLAN-policy-prelaunch-fixes §3.2), written from the contract
// only — docs/decisions.md D-110 (verb-not-applicable), D-111 (no-effect),
// D-113 (where: values), D-114 (self-grant), D-115 (half-protected), D-116
// (helper, template and config_entry inventories) and the manual's "policy
// lint" — without reading internal/policy/lint.go or the CLI's inventory code.
// Each test runs `policy lint --json` against a stubbed Home Assistant.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
)

type lintFinding struct {
	Code, Profile, Rule, Message string
	Line                         int
}

func lintFindings(t *testing.T, dir string) (lintDoc, []lintFinding) {
	t.Helper()
	out, _ := runPolicyCLI(t, dir, "policy", "lint", "--json")
	d := decodeLint(t, out)
	fs := make([]lintFinding, 0, len(d.Findings))
	for _, f := range d.Findings {
		fs = append(fs, lintFinding{f.Code, f.Profile, f.Rule, f.Message, f.Line})
	}
	return d, fs
}

func withCode(fs []lintFinding, code string) []lintFinding {
	var out []lintFinding
	for _, f := range fs {
		if f.Code == code {
			out = append(out, f)
		}
	}
	return out
}

func agentProfile(rules string) string {
	return "version: 1\nactive: agent\nprofiles:\n  agent:\n" + rules
}

// --- B-1: verb-not-applicable (D-110) ------------------------------------------

// TestPolicyLintReportsAVerbThatAppliesNowhereTheRuleReaches — D-110 (2): a
// write verb written by name that applies nowhere the pattern reaches, and a
// `write` that applies to nothing, are lint findings — not a broken file. A
// `write` that expands to something, and a verb that applies, are not; nor is
// a deny's verb that changes its part from above (D-125), while the same verb
// on an allow is.
func TestPolicyLintReportsAVerbThatAppliesNowhereTheRuleReaches(t *testing.T) {
	body := agentProfile(`    allow:
      "entity:lock.*/state": [control]
    deny:
      "entity:*/registry/entity_id": [control]
      "system:reload/*": [write]
      "entity:sensor.*/state": [control]
      "entity:*/registry/labels": [write]
      "entity:lock.*": [control]
`)
	ts := policyRig(t, body)
	_, fs := lintFindings(t, ts.dir)
	got := withCode(fs, "verb-not-applicable")
	want := map[int]string{
		lineOf(t, body, `"entity:*/registry/entity_id"`): `"control" does not apply at entity:*/registry/entity_id — no command asks for it there, so the rule decides nothing for it (control on entity is checked at: entity:<id>)`,
		lineOf(t, body, `"system:reload/*"`):             `"write" applies to nothing at system:reload/* — no command asks for a write there, so the rule decides nothing for writes`,
		lineOf(t, body, `"entity:lock.*/state"`):         `"control" does not apply at entity:lock.*/state`,
	}
	for _, f := range got {
		msg, ok := want[f.Line]
		if !ok {
			t.Errorf("verb-not-applicable on line %d (%s): that rule's verbs apply — %s", f.Line, f.Rule, f.Message)
			continue
		}
		delete(want, f.Line)
		if !strings.Contains(f.Message, msg) || f.Profile != "agent" {
			t.Errorf("line %d: profile %q message %q, want profile agent and a message containing %q", f.Line, f.Profile, f.Message, msg)
		}
	}
	for line, msg := range want {
		t.Errorf("no verb-not-applicable on line %d (%s); findings %+v", line, msg, fs)
	}
	if d, _ := lintFindings(t, ts.dir); !d.Valid {
		t.Errorf("an inapplicable verb made the file invalid; it is a lint finding, not a broken file")
	}
}

// --- B-3: no-effect (D-111) ------------------------------------------------------

// TestPolicyLintReportsATypedControlAllowAsNoEffect — D-111: an allow naming
// control on an automation, helper or template address grants nothing; the
// message points at the entity. `write` there and a script allow are not
// flagged.
func TestPolicyLintReportsATypedControlAllowAsNoEffect(t *testing.T) {
	body := agentProfile(`    default: deny
    allow:
      "automation:pg_*": [control]
      "helper:input_boolean/pg_*": [control, edit]
      "helper:*/pg_*": [control]
      "template:pg_*": [control]
      "automation:other_*": [write]
      "script:pg_*": [control]
`)
	ts := policyRig(t, body)
	_, fs := lintFindings(t, ts.dir)
	want := map[int]string{
		lineOf(t, body, `"automation:pg_*"`):           `control on automation:pg_* is checked for denies only, so this allow grants nothing for control — grant switching on the entity instead: allow "entity:automation.*": [control], and its service`,
		lineOf(t, body, `"helper:input_boolean/pg_*"`): `control on helper:input_boolean/pg_* is checked for denies only, so this allow grants nothing for control — grant switching on the entity instead: allow "entity:input_boolean.*": [control], and its service`,
		lineOf(t, body, `"helper:*/pg_*"`):             `allow "entity:*.*": [control]`,
		lineOf(t, body, `"template:pg_*"`):             `allow "entity:*.*": [control]`,
	}
	for _, f := range withCode(fs, "no-effect") {
		msg, ok := want[f.Line]
		if !ok {
			t.Errorf("no-effect on line %d (%s), which is not a typed control allow", f.Line, f.Rule)
			continue
		}
		delete(want, f.Line)
		if !strings.Contains(f.Message, msg) {
			t.Errorf("line %d: message %q lacks %q", f.Line, f.Message, msg)
		}
	}
	for line, msg := range want {
		t.Errorf("no no-effect finding on line %d (want %q); findings %+v", line, msg, fs)
	}
}

// --- B-7: where: values (D-113) --------------------------------------------------

// TestPolicyLintChecksWhereValues — D-113 against policyRig's registries
// (areas hall_id "Hall", kitchen_id "Kitchen"; labels devlabel "Device Label",
// energy "Energy"): a misspelt label or area is no-match on the where: entry's
// line; a value matching an id, a name, a glob, or only `*` is not.
func TestPolicyLintChecksWhereValues(t *testing.T) {
	withOwnerTerminal(t) // lint against the whole instance is the owner's (D-173)
	body := agentProfile(`    default: deny
    allow:
      "entity:light.*": [control]
      "entity:lock.*": [control]
      "entity:device_tracker.*": [control]
      "device:*": [delete]
    where:
      "entity:light.*": { label: pg_core_lable }
      "entity:lock.*": { area: kitchn }
      "entity:device_tracker.*": { area: Kitchen, label: "Device*" }
      "device:*": { area: "*", label: energy }
`)
	ts := policyRig(t, body)
	_, fs := lintFindings(t, ts.dir)
	labelLine := lineOf(t, body, `{ label: pg_core_lable }`)
	areaLine := lineOf(t, body, `{ area: kitchn }`)
	want := map[int]string{
		labelLine: `where: label "pg_core_lable" matches no label on this instance (by id or name)`,
		areaLine:  `where: area "kitchn" matches no area on this instance (by id or name)`,
	}
	for _, f := range withCode(fs, "no-match") {
		if !strings.Contains(f.Message, "where:") {
			continue
		}
		msg, ok := want[f.Line]
		if !ok {
			t.Errorf("where: no-match on line %d: %s — that value matches", f.Line, f.Message)
			continue
		}
		delete(want, f.Line)
		if f.Message != msg && !strings.Contains(f.Message, msg) {
			t.Errorf("line %d: message %q, want %q", f.Line, f.Message, msg)
		}
		if f.Profile != "agent" || !strings.HasPrefix(f.Rule, "allow ") {
			t.Errorf("line %d: profile %q rule %q, want the narrowed allow's profile and rule", f.Line, f.Profile, f.Rule)
		}
	}
	for line, msg := range want {
		t.Errorf("no no-match on line %d (%s); findings %+v", line, msg, fs)
	}
}

// --- B-8: self-grant (D-114) -----------------------------------------------------

// TestPolicyLintReportsASelfGrantingWhere — D-114, on policyRig: light.hall
// and light.kitchen both sit on device devhall (area hall_id); light.kitchen
// has label energy. Each case is a narrowed allow plus an allow that lets the
// agent change what the selector reads.
func TestPolicyLintReportsASelfGrantingWhere(t *testing.T) {
	withOwnerTerminal(t) // lint against the whole instance is the owner's (D-173)
	for _, tc := range []struct {
		name, rules        string
		narrowed, granting string // patterns: the narrowed allow, the allow that satisfies it
		grantRule, field   string
	}{
		{"label", `    allow:
      "entity:light.*": [control]
      "entity:*/registry/labels": [edit]
    where:
      "entity:light.*": { label: energy }
`, "entity:light.*", "entity:*/registry/labels", `allow "entity:*/registry/labels" [edit]`, "labels"},
		{"entity area", `    allow:
      "entity:light.*": [control]
      "entity:light.*/registry/area_id": [edit]
    where:
      "entity:light.*": { area: hall_id }
`, "entity:light.*", "entity:light.*/registry/area_id", `allow "entity:light.*/registry/area_id" [edit]`, "area_id"},
		{"device area", `    allow:
      "entity:light.*": [control]
      "device:*/registry/area_id": [edit]
    where:
      "entity:light.*": { area: Hall }
`, "entity:light.*", "device:*/registry/area_id", `allow "device:*/registry/area_id" [edit]`, "area_id"},
		{"device label", `    allow:
      "device:*": [delete]
      "device:*/registry/labels": [edit]
    where:
      "device:*": { label: devlabel }
`, "device:*", "device:*/registry/labels", `allow "device:*/registry/labels" [edit]`, "labels"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := agentProfile("    default: deny\n" + tc.rules)
			ts := policyRig(t, body)
			_, fs := lintFindings(t, ts.dir)
			got := withCode(fs, "self-grant")
			if len(got) != 1 {
				t.Fatalf("self-grant findings %+v, want one; all findings %+v", got, fs)
			}
			f := got[0]
			if wantLine := lineOf(t, body, `"`+tc.narrowed+`": [`); f.Line != wantLine {
				t.Errorf("line %d, want %d — the narrowed allow's line", f.Line, wantLine)
			}
			grantLine := lineOf(t, body, `"`+tc.granting+`": [`)
			for _, want := range []string{
				"the agent can satisfy this where: itself",
				fmt.Sprintf("%s (line %d) lets it change %s", tc.grantRule, grantLine, tc.field),
				"a guardrail does not stop that",
				"to close it, deny the field edit: deny \"",
				"/registry/" + tc.field + `": [edit]`,
			} {
				if !strings.Contains(f.Message, want) {
					t.Errorf("message %q lacks %q", f.Message, want)
				}
			}
		})
	}
}

// TestPolicyLintSelfGrantNeedsAnEffectiveGrant — D-114: a granting allow a
// deny overrides grants nothing, and outside `default: deny` the narrowed
// allow is redundant-allow already — neither is a self-grant.
func TestPolicyLintSelfGrantNeedsAnEffectiveGrant(t *testing.T) {
	for name, body := range map[string]string{
		"deny overrides the grant": agentProfile(`    default: deny
    allow:
      "entity:light.*": [control]
      "entity:*/registry/labels": [edit]
    deny:
      "entity:light.*/registry/labels": [edit]
    where:
      "entity:light.*": { label: energy }
`),
		"no default deny": agentProfile(`    allow:
      "entity:light.*": [control]
      "entity:*/registry/labels": [edit]
    where:
      "entity:light.*": { label: energy }
`),
	} {
		t.Run(name, func(t *testing.T) {
			ts := policyRig(t, body)
			if _, fs := lintFindings(t, ts.dir); len(withCode(fs, "self-grant")) != 0 {
				t.Errorf("self-grant reported: %+v", withCode(fs, "self-grant"))
			}
		})
	}
}

// --- B-9a: half-protected (D-115) ------------------------------------------------

// TestPolicyLintReportsAHalfProtectedAutomation — D-115 on policyRig's
// automation.morning, whose config id is 1700000000000: a deny on the config
// alone, or on the entity alone, names the missing counterpart; both together
// are clean.
func TestPolicyLintReportsAHalfProtectedAutomation(t *testing.T) {
	for _, tc := range []struct {
		name, rules string
		want        []string // message fragments; nil = no finding
	}{
		{"config only", `      "automation:1700000000000": [write]
`, []string{
			`deny "automation:1700000000000" [write] protects the automation's config, but its entity automation.morning can still be changed (edit entity:automation.morning/registry/`,
			`is allowed) — to protect both, also deny "entity:automation.morning": [write]`,
		}},
		{"entity only", `      "entity:automation.morning": [write]
`, []string{
			`deny "entity:automation.morning" [write] protects the entity, but its config automation:1700000000000 can still be changed (edit automation:1700000000000/config is allowed) — to protect both, also deny "automation:1700000000000": [write]`,
		}},
		{"both", `      "automation:1700000000000": [write]
      "entity:automation.morning": [write]
`, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := agentProfile("    deny:\n" + tc.rules)
			ts := policyRig(t, body)
			_, fs := lintFindings(t, ts.dir)
			got := withCode(fs, "half-protected")
			if tc.want == nil {
				if len(got) != 0 {
					t.Errorf("half-protected reported although both addresses are denied: %+v", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("half-protected findings %+v, want one; all %+v", got, fs)
			}
			if got[0].Line != 6 {
				t.Errorf("line %d, want 6 (the deny)", got[0].Line)
			}
			for _, w := range tc.want {
				if !strings.Contains(got[0].Message, w) {
					t.Errorf("message %q lacks %q", got[0].Message, w)
				}
			}
		})
	}
}

// --- B-10: helper, template and config_entry are enumerated (D-116) ---------------

// inventoryRig is policyRig's shape plus a companion (same server) serving the
// YAML helpers and the template list, storage helpers in the states, and the
// config-entry list `config entries` prints. companionUp=false leaves the
// companion routes unanswered. Its registries are empty.
func inventoryRig(t *testing.T, body string, companionUp bool) string {
	t.Helper()
	return inventoryRigWith(t, body, companionUp, map[string]any{
		"config/entity_registry/list": []any{}, "config/device_registry/list": []any{},
		"config/area_registry/list": []any{}, "config/label_registry/list": []any{}, "config/floor_registry/list": []any{},
	})
}

// inventoryRigWith is inventoryRig with the websocket answers ws — the
// registries lint resolves selectors and owners against. The companion lists
// one YAML helper with an entity in the states (guest_mode) and one without
// (yaml_only: renamed, or not loaded yet).
func inventoryRigWith(t *testing.T, body string, companionUp bool, ws map[string]any) string {
	t.Helper()
	states := `[
	 {"entity_id":"input_boolean.guest_mode","state":"off","attributes":{"friendly_name":"Guest Mode"}},
	 {"entity_id":"input_number.target_temp","state":"21","attributes":{"friendly_name":"Target Temp","editable":true}},
	 {"entity_id":"sensor.tpl_temp","state":"20","attributes":{"friendly_name":"Tpl Temp"}}
	]`
	handlers := map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, states)
		},
		"/api/config/config_entries/entry": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, rigEntries)
		},
	}
	if companionUp {
		handlers["/v1/config/helpers"] = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"helpers":[{"id":"guest_mode","name":"Guest Mode","domain":"input_boolean"},`+
				`{"id":"yaml_only","name":"YAML Only","domain":"input_boolean"}]}`)
		}
		handlers["/v1/config/templates"] = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"templates":[{"unique_id":"tpl_temp","name":"Tpl Temp","domain":"sensor","trigger":false}]}`)
		}
	} else {
		down := func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "companion down", http.StatusBadGateway) }
		handlers["/v1/config/helpers"] = down
		handlers["/v1/config/templates"] = down
	}
	ts := startCmdServer(t, ws, handlers)
	pointAtCompanion(t, ts)
	if err := os.WriteFile(filepath.Join(ts.dir, policy.FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return ts.dir
}

const inventoryPolicy = `version: 1
active: agent
profiles:
  agent:
    deny:
      "helper:input_boolean/guest_mode": [delete]
      "helper:input_number/target_temp": [delete]
      "helper:input_boolean/no_such_helper": [delete]
      "template:tpl_temp": [delete]
      "template:no_such_template": [delete]
      "config_entry:e1": [delete]
      "config_entry:no_such_entry": [delete]
`

// TestPolicyLintEnumeratesHelpersTemplatesAndConfigEntries — D-116: helper ids
// come from the same two sources as `helper ls` (YAML through the companion,
// storage from the states), template unique_ids from the companion's list,
// config entry ids from the list `config entries` prints. None of the three is
// unchecked; a misspelt id is no-match; file stays `not enumerated`.
func TestPolicyLintEnumeratesHelpersTemplatesAndConfigEntries(t *testing.T) {
	dir := inventoryRig(t, inventoryPolicy+"      \"file:packages/x.yaml\": [edit]\n", true)
	d, fs := lintFindings(t, dir)
	for _, u := range d.Unchecked {
		if u.Kind == "helper" || u.Kind == "template" || u.Kind == "config_entry" {
			t.Errorf("%s is unchecked (%s); it has a source", u.Kind, u.Reason)
		}
	}
	var fileUnchecked bool
	for _, u := range d.Unchecked {
		if u.Kind == "file" && u.Reason == "not enumerated" {
			fileUnchecked = true
		}
	}
	if !fileUnchecked {
		t.Errorf("file is no longer listed as not enumerated: %+v", d.Unchecked)
	}
	noMatch := make([]int, 0, 3)
	for _, f := range withCode(fs, "no-match") {
		noMatch = append(noMatch, f.Line)
	}
	slices.Sort(noMatch)
	want := []int{
		lineOf(t, inventoryPolicy, "no_such_helper"),
		lineOf(t, inventoryPolicy, "no_such_template"),
		lineOf(t, inventoryPolicy, "no_such_entry"),
	}
	if !slices.Equal(noMatch, want) {
		t.Errorf("no-match on lines %v, want %v (the three misspelt ids, nothing else); findings %+v", noMatch, want, fs)
	}
}

// TestPolicyLintWithoutTheCompanionListsHelperAndTemplateUnchecked — D-116:
// without the companion, helper is checked against its storage half and
// listed unchecked with the companion's error; template is unchecked.
func TestPolicyLintWithoutTheCompanionListsHelperAndTemplateUnchecked(t *testing.T) {
	dir := inventoryRig(t, inventoryPolicy, false)
	d, _ := lintFindings(t, dir)
	kinds := map[string]string{}
	for _, u := range d.Unchecked {
		if u.Rule == "" {
			kinds[u.Kind] = u.Reason
		}
	}
	for _, k := range []string{"helper", "template"} {
		reason, ok := kinds[k]
		if !ok {
			t.Errorf("%s is not listed unchecked without the companion: %+v", k, d.Unchecked)
			continue
		}
		if reason == "not enumerated" {
			t.Errorf("%s is unchecked as `not enumerated`; the reason is the companion's error", k)
		}
	}
	if _, ok := kinds["config_entry"]; ok {
		t.Errorf("config_entry is unchecked without the companion; it comes from Home Assistant")
	}
}
