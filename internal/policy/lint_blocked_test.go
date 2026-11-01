package policy

import (
	"strconv"
	"strings"
	"testing"
)

// retestPolicy is the policy of the live re-test (2026-11-01) as the author
// left it, with the no-renames rule's and view two's verbs as placeholders:
// the author first wrote both as [write].
const retestPolicy = `version: 1
active: agent

profiles:
  agent:
    default: deny
    allow:
      "*": [read]
      "automation:pg_*": [edit, create, delete]
      "entity:automation.pg_*": [control, delete]
      "entity:input_boolean.pg_*": [control, delete]
      "script:pg_core_script": [control, edit]
      "entity:script.pg_core_script": [control]
      "template:pg_*": [edit]
      "entity:sensor.pg_*": [control]
      "entity:binary_sensor.pg_*": [control]
      "entity:*.pg_*/registry/labels": [edit]
      "entity:input_boolean.pg_*/registry/area_id": [edit]
      "dashboard:pg-policytest": [create, delete]
      "dashboard:pg-policytest/views/one": [edit]
      "file:automations.yaml": [edit]
      "service:input_boolean.*": [control]
      "service:automation.*": [control]
      "service:script.*": [control]
      "service:template.reload": [control]
    where:
      "entity:input_boolean.pg_*": { area: pg_core_area }
    deny:
      "entity:lock.tr844_vehicle_lock": [read, write]
      "automation:pg_v9_auto_1": [write]
      "entity:automation.pg_v9_verify_auto": [write]
      "entity:automation.pg_core_auto_sleeper": [write]
      "automation:pg_core_auto_2": [write]
      "entity:*/registry/entity_id": [RENAMES]
      "entity:cover.*": [write]
      "entity:climate.*": [write]
      "dashboard:pg-policytest/views/two": [VIEWTWO]
      "service:homeassistant.restart": [control]
      "service:homeassistant.stop": [control]
      "area:*": [create, delete]
      "floor:*": [create, delete]
      "label:*": [create, delete]

  readonly:
    default: deny
    allow:
      "*": [read]
    deny:
      "*": [write]
`

// lineIn is the 1-based line of the first line of body holding s.
func lineIn(t *testing.T, body, s string) int {
	t.Helper()
	for i, l := range strings.Split(body, "\n") {
		if strings.Contains(l, s) {
			return i + 1
		}
	}
	t.Fatalf("no line holds %q", s)
	return 0
}

// TestLintBlockedByPart (D-127): an allow whose write verb a deny on a part
// always refuses from above (D-125) is reported on the allow's line, naming
// both rules — and, where one exists, the narrower deny. Static: no live
// instance is needed.
func TestLintBlockedByPart(t *testing.T) {
	retest := func(renames, viewTwo string) string {
		return strings.NewReplacer("RENAMES", renames, "VIEWTWO", viewTwo).Replace(retestPolicy)
	}
	type want struct {
		allow string // the allow's pattern, as it starts its line
		msg   string
	}
	for _, tc := range []struct {
		name string
		body string
		want []want
	}{
		{
			// N-1: the no-renames rule as [write] holds delete on every
			// entity, so both delete allows are dead for delete; control is
			// not held, and the labels allow sits beside the deny, not above.
			name: "the re-test file, no-renames as [write]",
			body: retest("write", "edit"),
			want: []want{
				{`"entity:automation.pg_*"`, `never grants delete where deny "entity:*/registry/entity_id" [write] (line RENAMES) covers a part: ` +
					`a part of it is denied, so all of it is — if the deny should only stop changes to the part itself, narrow it to [edit]`},
				{`"entity:input_boolean.pg_*": [control`, `never grants delete where deny "entity:*/registry/entity_id" [write] (line RENAMES) covers a part: ` +
					`a part of it is denied, so all of it is — if the deny should only stop changes to the part itself, narrow it to [edit]`},
			},
		},
		{
			// N-1b: a view deny [write] holds create and delete on the
			// dashboard (creating or deleting it creates or deletes the view).
			name: "the re-test file, view two as [write]",
			body: retest("edit", "write"),
			want: []want{
				{`"dashboard:pg-policytest": [create`, `never grants create or delete where deny "dashboard:pg-policytest/views/two" [write] (line VIEWTWO) covers a part: ` +
					`a part of it is denied, so all of it is — if the deny should only stop changes to the part itself, narrow it to [edit]`},
			},
		},
		{name: "the re-test file as the author left it", body: retest("edit", "edit")},
		{
			name: "a config deny holds the automation's delete",
			body: agent("    default: deny\n    allow:\n      \"automation:pg_*\": [edit, delete]\n    deny:\n      \"automation:pg_x/config\": [write]\n"),
			want: []want{{`"automation:pg_*"`, `never grants delete where deny "automation:pg_x/config" [write] (line 9) covers a part: ` +
				`a part of it is denied, so all of it is — if the deny should only stop changes to the part itself, narrow it to [edit]`}},
		},
		{
			// A deny on state is about the switch itself: no narrower deny.
			name: "a state deny holds the entity's control",
			body: agent("    default: deny\n    allow:\n      \"entity:light.*\": [control]\n    deny:\n      \"entity:light.desk/state\": [control]\n"),
			want: []want{{`"entity:light.*"`, `never grants control where deny "entity:light.desk/state" [control] (line 9) covers a part: ` +
				`a part of it is denied, so all of it is`}},
		},
		{
			name: "a block deny holds the file's edit",
			body: agent("    default: deny\n    allow:\n      \"file:automations.yaml\": [edit]\n    deny:\n      \"file:automations.yaml/block/x\": [edit]\n"),
			want: []want{{`"file:automations.yaml"`, `never grants edit where deny "file:automations.yaml/block/x" [edit] (line 9) covers a part: ` +
				`a part of it is denied, so all of it is`}},
		},
		{
			// D-127 (corrected): `**` matches whole segments, so a literal segment
			// before it must still pair up with the other side's.
			name: "a ** allow and a block deny in a file it cannot match",
			body: agent("    default: deny\n    allow:\n      \"file:packages/**\": [edit]\n    deny:\n      \"file:automations.yaml/block/b\": [edit]\n      \"file:scripts/x.yaml/block/b\": [edit]\n"),
		},
		{
			name: "a ** allow and a block deny in a file it matches",
			body: agent("    default: deny\n    allow:\n      \"file:packages/**\": [edit]\n    deny:\n      \"file:packages/sub/x.yaml/block/b\": [edit]\n"),
			want: []want{{`"file:packages/**"`, `never grants edit where deny "file:packages/sub/x.yaml/block/b" [edit] (line 9) covers a part: ` +
				`a part of it is denied, so all of it is`}},
		},
		{
			name: "a bare ** allow overlaps every block deny",
			body: agent("    default: deny\n    allow:\n      \"file:**\": [edit]\n    deny:\n      \"file:automations.yaml/block/b\": [edit]\n"),
			want: []want{{`"file:**"`, `never grants edit where deny "file:automations.yaml/block/b" [edit] (line 9) covers a part: ` +
				`a part of it is denied, so all of it is`}},
		},
		{
			name: "a deny on other ids overlaps nothing",
			body: agent("    default: deny\n    allow:\n      \"entity:light.*\": [delete]\n    deny:\n      \"entity:cover.*/registry/entity_id\": [write]\n"),
		},
		{
			// edit on an entity is asked at its registry fields, beside the
			// field deny, never above it.
			name: "a field deny beside the field edit",
			body: agent("    default: deny\n    allow:\n      \"entity:light.*\": [edit]\n    deny:\n      \"entity:*/registry/entity_id\": [write]\n"),
		},
		{
			name: "a profile without default: deny",
			body: agent("    allow:\n      \"entity:automation.pg_*\": [delete]\n    deny:\n      \"entity:*/registry/entity_id\": [write]\n"),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := Lint(mustLoad(t, tc.body), nil)
			got := findingsOf(rep, CodeBlockedByPart)
			if len(got) != len(tc.want) {
				t.Fatalf("%d blocked-by-part findings, want %d: %+v", len(got), len(tc.want), got)
			}
			for i, w := range tc.want {
				msg := w.msg
				for placeholder, rule := range map[string]string{
					"line RENAMES": `"entity:*/registry/entity_id"`,
					"line VIEWTWO": `"dashboard:pg-policytest/views/two"`,
				} {
					if strings.Contains(msg, placeholder) {
						msg = strings.ReplaceAll(msg, placeholder, "line "+strconv.Itoa(lineIn(t, tc.body, rule)))
					}
				}
				if got[i].Line != lineIn(t, tc.body, w.allow) || got[i].Profile != "agent" || !strings.HasPrefix(got[i].Rule, "allow ") {
					t.Errorf("finding %d on line %d (%s, %s), want the allow on line %d", i, got[i].Line, got[i].Profile, got[i].Rule, lineIn(t, tc.body, w.allow))
				}
				if got[i].Message != msg {
					t.Errorf("finding %d\n got: %s\nwant: %s", i, got[i].Message, msg)
				}
			}
		})
	}
}
