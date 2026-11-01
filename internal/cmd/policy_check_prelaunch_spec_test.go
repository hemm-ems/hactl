package cmd

// Blind spec tests for the pre-merge fixes to `policy check` and `policy show`
// after the live test of 2026-11-01 (PLAN-policy-prelaunch-fixes §3.2), written
// from the contract only — docs/decisions.md D-110 (B-1), D-111 (B-3), D-112
// (B-6), D-120 (B-14) and the manual's "Policy reference" — without reading
// internal/policy's evaluator or internal/cmd/policy.go's renderers. Every test
// drives the real command tree through RunWithOutput, as a caller does.

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
)

// liveTestRule is the live test's "no renames" rule (F-1), verbatim, in a
// profile that allows everything else.
const liveTestRule = "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"entity:*/registry/entity_id\": [write]\n"

// lineOf is the 1-based line of the first line of body holding needle.
func lineOf(t *testing.T, body, needle string) int {
	t.Helper()
	for i, l := range strings.Split(body, "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	t.Fatalf("no line holds %q:\n%s", needle, body)
	return 0
}

func stringsOf(v any) []string {
	list, _ := v.([]any)
	out := make([]string, 0, len(list))
	for _, x := range list {
		s, _ := x.(string)
		out = append(out, s)
	}
	return out
}

// --- B-1 (D-110) at the command line -------------------------------------------

// TestPolicyCheckLiveTestRuleLeavesSwitchingAlone — F-1 verbatim at `policy
// check`: control on an entity is allowed, the rename the rule is about is
// denied.
func TestPolicyCheckLiveTestRuleLeavesSwitchingAlone(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), liveTestRule)
	out, err := runPolicyCLI(t, dir, "policy", "check", "entity:light.x", "control")
	if cliExitCode(err) != 0 || !strings.HasPrefix(out, "allowed: entity:light.x control") {
		t.Errorf("control entity:light.x under the no-renames rule: exit %d (%v)\n%s", cliExitCode(err), err, out)
	}
	if strings.Contains(out, "partial:") {
		t.Errorf("control entity:light.x is partial; control does not apply at registry/entity_id:\n%s", out)
	}
	out, err = runPolicyCLI(t, dir, "policy", "check", "entity:light.x/registry/entity_id", "edit")
	if cliExitCode(err) != 1 || !strings.HasPrefix(out, "denied: entity:light.x/registry/entity_id edit") {
		t.Errorf("the rename is no longer denied: exit %d (%v)\n%s", cliExitCode(err), err, out)
	}
}

// TestSvcCallPreviewLiveTestRuleLeavesSwitchingAlone — F-1 at a dry-run `svc
// call`: the live test's switch was refused naming the no-renames rule.
func TestSvcCallPreviewLiveTestRuleLeavesSwitchingAlone(t *testing.T) {
	rig := startSvcRig(t, reachOf("light.a"))
	writePolicyFile(t, rig.dir, liveTestRule)
	out, err := runPolicyCLI(t, rig.dir, "svc", "call", "light.turn_on", "--data", `{"entity_id":"light.a"}`)
	if err != nil {
		t.Fatalf("svc call light.turn_on on light.a refused under the no-renames rule: %v\n%s", err, out)
	}
	if !hasPolicyNote(out, 2) {
		t.Errorf("the allowed preview does not say what it checked:\n%s", out)
	}
}

// TestPolicyShowExpandsWritePerAddress — D-110 (1): `expanded` is the write
// verbs that apply at the rule's pattern; a verb written by name is kept.
func TestPolicyShowExpandsWritePerAddress(t *testing.T) {
	body := `version: 1
active: agent
profiles:
  agent:
    deny:
      "entity:*/registry/entity_id": [write]
      "entity:lock.*": [write]
      "system:reload/*": [write]
      "script:*/run": [write]
      "device:*": [write]
      "file:**": [write]
      "automation:pg_*": [read, write]
      "entity:*/state": [control]
`
	dir := policyInstance(t, closedHAURL(t), body)
	out, err := runPolicyCLI(t, dir, "policy", "show", "--json")
	if err != nil {
		t.Fatalf("policy show --json: %v\n%s", err, out)
	}
	var doc struct {
		Rules []struct {
			Pattern  string   `json:"pattern"`
			Expanded []string `json:"expanded"`
		} `json:"rules"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	want := map[string][]string{
		"entity:*/registry/entity_id": {"edit"},
		"entity:lock.*":               {"control", "edit", "create", "delete"},
		"system:reload/*":             {},
		"script:*/run":                {"control"},
		"device:*":                    {"edit", "delete"},
		"file:**":                     {"edit"},
		"automation:pg_*":             {"read", "control", "edit", "create", "delete"},
		"entity:*/state":              {"control"},
	}
	for _, r := range doc.Rules {
		w, ok := want[r.Pattern]
		if !ok {
			continue
		}
		delete(want, r.Pattern)
		if got := r.Expanded; !slices.Equal(got, w) {
			t.Errorf("%s: expanded %q, want %q", r.Pattern, got, w)
		}
	}
	for p := range want {
		t.Errorf("policy show lists no rule %s", p)
	}
}

// TestPolicyCheckNotesAVerbThatAppliesNowhereThere — D-110 (4): a concrete
// write verb no command asks for at or below the resource answers as before
// and adds a note, in text and in --json `notes`.
func TestPolicyCheckNotesAVerbThatAppliesNowhereThere(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyAllowEverything)
	const note = "control does not apply at entity:light.x/state: no command asks for it there"
	out, _ := runPolicyCLI(t, dir, "policy", "check", "entity:light.x/state", "control")
	if !strings.Contains(out, "note: "+note) {
		t.Errorf("text answer lacks `note: %s`:\n%s", note, out)
	}
	out, _ = runPolicyCLI(t, dir, "policy", "check", "entity:light.x/state", "control", "--json")
	if notes := stringsOf(decodeObject(t, out)["notes"]); !slices.Contains(notes, note) {
		t.Errorf("--json notes %q lack %q", notes, note)
	}
	// Control applies at the entity itself: no note there.
	out, _ = runPolicyCLI(t, dir, "policy", "check", "entity:light.x", "control", "--json")
	if notes := stringsOf(decodeObject(t, out)["notes"]); len(notes) != 0 {
		t.Errorf("control entity:light.x carries notes %q; control applies there", notes)
	}
}

// --- B-3 (D-111) -----------------------------------------------------------------

// TestPolicyCheckNotesThatATypedControlAllowGrantsNothing — D-111: control on
// an automation, script, helper or template address decided by an allow adds
// the note; the verdict and `enforced` are unchanged.
func TestPolicyCheckNotesThatATypedControlAllowGrantsNothing(t *testing.T) {
	body := `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "automation:pg_*": [control]
      "script:pg_*": [control]
      "helper:input_boolean/pg_*": [control]
      "template:pg_*": [control]
`
	dir := policyInstance(t, closedHAURL(t), body)
	for _, res := range []string{"automation:pg_x", "script:pg_x", "helper:input_boolean/pg_x", "template:pg_x"} {
		t.Run(res, func(t *testing.T) {
			note := "control on " + res + " is checked for its denies only — this allow grants nothing; a service call is decided by the entity's own verdict"
			out, err := runPolicyCLI(t, dir, "policy", "check", res, "control")
			if cliExitCode(err) != 0 || !strings.HasPrefix(out, "allowed: "+res+" control") {
				t.Errorf("the verdict changed: exit %d\n%s", cliExitCode(err), out)
			}
			if !strings.Contains(out, "note: "+note) {
				t.Errorf("text answer lacks `note: %s`:\n%s", note, out)
			}
			out, _ = runPolicyCLI(t, dir, "policy", "check", res, "control", "--json")
			if notes := stringsOf(decodeObject(t, out)["notes"]); !slices.Contains(notes, note) {
				t.Errorf("--json notes %q lack the note", notes)
			}
		})
	}
}

// --- B-6 (D-112) -----------------------------------------------------------------

type unmetWhere struct {
	Rule   string `json:"rule"`
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

func decodeUnmet(t *testing.T, what string, v any) []unmetWhere {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out []unmetWhere
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("%s: unmet_where %s does not decode: %v", what, raw, err)
	}
	return out
}

// TestPolicyCheckSaysWhichWhereWasNotMet — D-112 on policyRig's registries:
// light.kitchen (own area kitchen_id, label energy), light.hall (no own area or
// labels; its device devhall is in hall_id), device_tracker.jan (no area, no
// device). A denial by the default names each narrowed allow that matched,
// was tried and did not hold, with the first failing key's reason, area
// before label; `policy check --json` always carries unmet_where.
func TestPolicyCheckSaysWhichWhereWasNotMet(t *testing.T) {
	body := `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.*": [control]
      "entity:light.k*": [control]
      "entity:device_tracker.*": [control]
    where:
      "entity:light.*": { label: pg_core_label }
      "entity:light.k*": { area: [wohnzimmer, buero], label: energy }
      "entity:device_tracker.*": { area: hall_id }
`
	lLight, lLightK, lTracker := lineOf(t, body, `"entity:light.*": [control]`), lineOf(t, body, `"entity:light.k*": [control]`), lineOf(t, body, `"entity:device_tracker.*": [control]`)
	for _, tc := range []struct {
		resource string
		want     []unmetWhere
	}{
		{"entity:light.kitchen", []unmetWhere{
			{`allow "entity:light.*" [control] where label=pg_core_label`, lLight, "light.kitchen has no label pg_core_label"},
			{`allow "entity:light.k*" [control] where area=wohnzimmer|buero label=energy`, lLightK, "light.kitchen is in area kitchen_id, not wohnzimmer or buero"},
		}},
		{"entity:light.hall", []unmetWhere{
			{`allow "entity:light.*" [control] where label=pg_core_label`, lLight, "light.hall has no label pg_core_label"},
		}},
		{"entity:device_tracker.jan", []unmetWhere{
			{`allow "entity:device_tracker.*" [control] where area=hall_id`, lTracker, "device_tracker.jan has no area"},
		}},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			ts := policyRig(t, body)
			out, err := runPolicyCLI(t, ts.dir, "policy", "check", tc.resource, "control", "--json")
			if cliExitCode(err) != 1 {
				t.Fatalf("exit %d (%v), want 1 (denied by the default)\n%s", cliExitCode(err), err, out)
			}
			m := decodeObject(t, out)
			wantField(t, tc.resource, m, "rule", "default: deny")
			if got := decodeUnmet(t, tc.resource, m["unmet_where"]); !slices.Equal(got, tc.want) {
				t.Errorf("unmet_where\n got %+v\nwant %+v", got, tc.want)
			}

			text, _ := runPolicyCLI(t, ts.dir, "policy", "check", tc.resource, "control")
			for _, u := range tc.want {
				line := fmt.Sprintf("where not met: %s (line %d) — %s", u.Rule, u.Line, u.Reason)
				if !strings.Contains(text, line) {
					t.Errorf("text answer lacks %q:\n%s", line, text)
				}
			}
			if rule := strings.Index(text, "rule: "); rule < 0 || strings.Index(text, "where not met:") < rule {
				t.Errorf("the `where not met:` lines must follow the `rule:` line:\n%s", text)
			}
		})
	}
}

// TestPolicyCheckUnmetWhereIsEmptyWhenAnAllowGranted — unmet_where is
// non-empty only when no allow granted; `[]` otherwise, and on every --json
// answer. A selector that could not be evaluated stays a note, not an entry.
func TestPolicyCheckUnmetWhereIsEmptyWhenAnAllowGranted(t *testing.T) {
	body := `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.k*": [control]
      "entity:light.*": [control]
    where:
      "entity:light.k*": { label: pg_core_label }
`
	ts := policyRig(t, body)
	out, err := runPolicyCLI(t, ts.dir, "policy", "check", "entity:light.kitchen", "control", "--json")
	if err != nil {
		t.Fatalf("a plain allow after a failed narrowed one must grant: %v\n%s", err, out)
	}
	if got, ok := decodeObject(t, out)["unmet_where"]; !ok || len(decodeUnmet(t, "granted", got)) != 0 {
		t.Errorf("unmet_where = %v (present %v), want [] when an allow granted", got, ok)
	}

	// Home Assistant unreachable: the selector is not evaluated — a note, and
	// no unmet_where entry.
	dir := policyInstance(t, closedHAURL(t), `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.*": [control]
    where:
      "entity:light.*": { label: pg_core_label }
`)
	out, _ = runPolicyCLI(t, dir, "policy", "check", "entity:light.kitchen", "control", "--json")
	m := decodeObject(t, out)
	if got, ok := m["unmet_where"]; !ok || len(decodeUnmet(t, "unreachable", got)) != 0 {
		t.Errorf("unmet_where = %v (present %v), want [] — an unevaluated selector is a note", got, ok)
	}
	if notes := stringsOf(m["notes"]); len(notes) == 0 {
		t.Errorf("an unevaluated selector left no note")
	}
}

// TestRefusalSaysWhichWhereWasNotMet — D-112: a refusal's denied[] entry and
// its audit line carry unmet_where when non-empty, and the text refusal adds
// `    where not met: <rule> (line <n>) — <reason>` under that denied: line.
func TestRefusalSaysWhichWhereWasNotMet(t *testing.T) {
	rig := startSvcRig(t, reachOf("light.a"))
	body := svcPolicy(
		`    default: deny`,
		`    allow:`,
		`      "service:light.*": [control]`,
		`      "entity:light.*": [control]`,
		`    where:`,
		`      "entity:light.*": { label: pg_core_label }`,
	)
	writePolicyFile(t, rig.dir, body)
	rule := `allow "entity:light.*" [control] where label=pg_core_label`
	line := lineOf(t, body, `"entity:light.*": [control]`)
	reason := "light.a has no label pg_core_label"
	args := []string{"svc", "call", "light.turn_on", "--data", `{"entity_id":"light.a"}`}

	out, err := runPolicyCLI(t, rig.dir, args...)
	wantRefused(t, "preview", out, err, "control entity:light.a — default: deny")
	wantLine := fmt.Sprintf("  denied: control entity:light.a — default: deny\n    where not met: %s (line %d) — %s\n", rule, line, reason)
	if !strings.Contains(out, wantLine) {
		t.Errorf("the refusal does not say which where: was not met; want\n%s\ngot\n%s", wantLine, out)
	}

	out, _ = runPolicyCLI(t, rig.dir, append(args, "--json")...)
	pol, _ := decodeObject(t, out)["details"].(map[string]any)["policy"].(map[string]any)
	denied, _ := pol["denied"].([]any)
	if len(denied) != 1 {
		t.Fatalf("denied = %v", pol["denied"])
	}
	d, _ := denied[0].(map[string]any)
	want := []unmetWhere{{rule, line, reason}}
	if got := decodeUnmet(t, "denied[0]", d["unmet_where"]); !slices.Equal(got, want) {
		t.Errorf("denied[0].unmet_where = %+v, want %+v", got, want)
	}

	var audited bool
	for _, l := range readAudit(t, rig.dir) {
		if l["resource"] == "entity:light.a" && l["allowed"] == false {
			audited = true
			if got := decodeUnmet(t, "audit", l["unmet_where"]); !slices.Equal(got, want) {
				t.Errorf("audit line unmet_where = %+v, want %+v", got, want)
			}
		}
		if l["resource"] == "service:light.turn_on" {
			if _, has := l["unmet_where"]; has {
				t.Errorf("an audit line without an unmet selector carries unmet_where: %v", l)
			}
		}
	}
	if !audited {
		t.Errorf("no audit line for the denied entity")
	}
}

// --- B-14 (D-120) ----------------------------------------------------------------

// TestPolicyCheckWriteAnswersEveryApplicableVerb — D-120: `write` answers one
// verdict per write verb that applies at or below the resource, in the order
// control, edit, create, delete; exit 0 only when all are allowed.
func TestPolicyCheckWriteAnswersEveryApplicableVerb(t *testing.T) {
	body := `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.*": [control]
`
	dir := policyInstance(t, closedHAURL(t), body)
	out, err := runPolicyCLI(t, dir, "policy", "check", "entity:light.sofa", "write")
	if cliExitCode(err) != 1 {
		t.Fatalf("one verb denied: exit %d (%v), want 1\n%s", cliExitCode(err), err, out)
	}
	line := lineOf(t, body, `"entity:light.*"`)
	wantLines := []string{
		"denied: entity:light.sofa write",
		fmt.Sprintf(`control: allowed — allow "entity:light.*" [control] (line %d)`, line),
		"edit: denied — default: deny",
		"create: denied — default: deny",
		"delete: denied — default: deny",
		"profile: agent (selected by active)",
	}
	lines := strings.Split(out, "\n")
	at := 0
	for _, l := range lines {
		if at < len(wantLines) && strings.HasPrefix(l, wantLines[at]) {
			at++
		}
	}
	if at != len(wantLines) {
		t.Errorf("text answer lacks, in order, %q (matched %d):\n%s", wantLines, at, out)
	}
	if !strings.Contains(out, "enforced: ") {
		t.Errorf("text answer lacks its enforced: line:\n%s", out)
	}

	allowAll := policyInstance(t, closedHAURL(t), policyAllowEverything)
	out, err = runPolicyCLI(t, allowAll, "policy", "check", "entity:light.sofa", "write")
	if cliExitCode(err) != 0 || !strings.HasPrefix(out, "allowed: entity:light.sofa write") {
		t.Errorf("all allowed: exit %d (%v)\n%s", cliExitCode(err), err, out)
	}
}

// TestPolicyCheckWriteJSONHoldsEachVerbsOwnDocument — D-120: `verdicts` holds
// one object per applicable verb, each exactly what `policy check <resource>
// <verb> --json` prints; a one-verb answer has no `verdicts` key. The typed
// address carries its deny-only control inside.
func TestPolicyCheckWriteJSONHoldsEachVerbsOwnDocument(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyManualExample)
	for _, tc := range []struct {
		resource string
		verbs    []string
	}{
		{"entity:light.sofa", []string{"control", "edit", "create", "delete"}},
		{"automation:pg_heating", []string{"control", "edit", "create", "delete"}},
		{"script:pg_x/run", []string{"control"}},
		{"file:packages/x.yaml", []string{"edit"}},
		{"config_entry:e1", []string{"edit", "create", "delete"}},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			out, err := runPolicyCLI(t, dir, "policy", "check", tc.resource, "write", "--json")
			m := decodeObject(t, out)
			requireKeys(t, "write", m, "resource", "verb", "allowed", "profile", "selected_by", "what_if", "level", "enforced", "verdicts")
			wantField(t, "write", m, "resource", tc.resource)
			wantField(t, "write", m, "verb", "write")
			verdicts, _ := m["verdicts"].([]any)
			if len(verdicts) != len(tc.verbs) {
				t.Fatalf("verdicts: %d, want %d (%v)\n%s", len(verdicts), len(tc.verbs), tc.verbs, out)
			}
			allAllowed := true
			for i, v := range tc.verbs {
				one, _ := runPolicyCLI(t, dir, "policy", "check", tc.resource, v, "--json")
				oneDoc := decodeObject(t, one)
				if _, has := oneDoc["verdicts"]; has {
					t.Errorf("the one-verb answer for %s carries a verdicts key", v)
				}
				gj, _ := json.Marshal(verdicts[i])
				wj, _ := json.Marshal(oneDoc)
				if string(gj) != string(wj) {
					t.Errorf("verdicts[%d] is not `policy check %s %s --json`:\n got %s\nwant %s", i, tc.resource, v, gj, wj)
				}
				if a, _ := oneDoc["allowed"].(bool); !a {
					allAllowed = false
				}
			}
			wantField(t, "write", m, "allowed", allAllowed)
			if wantExit := map[bool]int{true: 0, false: 1}[allAllowed]; cliExitCode(err) != wantExit {
				t.Errorf("exit %d, want %d (0 only when every verb is allowed)", cliExitCode(err), wantExit)
			}
		})
	}
}

// TestPolicyCheckWriteRefusesWhereNothingApplies — D-120: a resource at which
// no write verb applies is refused: exit 1, nothing on stdout, the reason.
func TestPolicyCheckWriteRefusesWhereNothingApplies(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyAllowEverything)
	out, err := runPolicyCLI(t, dir, "policy", "check", "entity:light.sofa/attr/brightness", "write")
	if cliExitCode(err) != 1 || out != "" {
		t.Fatalf("exit %d, stdout %q; want 1 and nothing", cliExitCode(err), out)
	}
	if want := "write applies to nothing at entity:light.sofa/attr/brightness: no command changes it"; !strings.Contains(err.Error(), want) {
		t.Errorf("error %q lacks %q", err, want)
	}
}
