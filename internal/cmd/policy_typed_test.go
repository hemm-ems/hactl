package cmd

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/enforce"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// The typed write commands' plans (manual, Enforcement: "What each write
// command checks"; D-78) — automation, script, template and helper apply,
// create, delete and rollback — refused before the first write, the same in
// the preview and under --confirm, naming the rule.

// typedRollbackRename restores a backup whose own id is not the automation it
// is filed under (a hidden rename, E-5): it is the newest backup of heating.
var typedRollbackRename = typedCommand{"auto rollback rename", []string{"auto", "rollback", "heating"}}

func writeRenameBackup(t *testing.T, r *typedRig) {
	t.Helper()
	r.write(t, "backups/2026-11-01T10-00-00.000000_heating.yaml", typedFiles["backup_rename.yaml"])
}

// typedPolicy is a one-rule deny profile: the rule is on line 6.
func typedPolicy(pattern, verb string) string {
	return fmt.Sprintf("version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      %q: [%s]\n", pattern, verb)
}

type typedRefusal struct {
	cmd    typedCommand
	setup  func(t *testing.T, r *typedRig)
	policy string
	action string
	denied []string // the report's `denied:` lines, after "  denied: "
	checks int
}

// runTypedRefusal runs the preview and the confirmed run under tc.policy and
// requires both to refuse identically — the report on stdout naming every
// denied pair and its rule, the one stderr line, exit 1 — with nothing written:
// no request but GETs reached the fake, no registry entry was removed. Both
// checks are audited at the command layer, as a preview and as a confirmed run.
func runTypedRefusal(t *testing.T, tc typedRefusal) {
	t.Helper()
	t.Setenv("HACTL_MANUAL_MODE", "off")
	r := newTypedRig(t)
	if tc.setup != nil {
		tc.setup(t, r)
	}
	writePolicyFile(t, r.ts.dir, tc.policy)
	var want strings.Builder
	fmt.Fprintf(&want, "policy_denied: would %s — refused by policy, nothing was written\n", tc.action)
	for _, d := range tc.denied {
		want.WriteString("  denied: " + d + "\n")
	}
	fmt.Fprintf(&want, "  checked: %d resources, %d denied\n  profile: agent (selected by active)\n", tc.checks, len(tc.denied))

	r.requests()
	r.resetWS()
	var previewOut string
	for _, confirm := range []bool{false, true} {
		var extra []string
		if confirm {
			extra = []string{"--confirm"}
		}
		out, err := runPolicyCLI(t, r.ts.dir, tc.cmd.argsIn(t, r, extra...)...)
		// The report, then its one hint line (D-118, built from the causes;
		// its wording is TestRefusalHintNamesTheCauseAndTheWayForward's).
		report, hint, _ := strings.Cut(out, "  hint: ")
		if report != want.String() || !strings.HasSuffix(hint, " — ask the owner; a change goes through a draft and `hactl policy activate`\n") || strings.Count(hint, "\n") != 1 {
			t.Errorf("confirm %v: stdout\n got: %q\nwant: %q + one hint line", confirm, out, want.String())
		}
		if cliExitCode(err) != 1 || policyErrorClass(err) != enforce.ClassDenied {
			t.Fatalf("confirm %v: exit %d class %q (%v), want 1 policy_denied", confirm, cliExitCode(err), policyErrorClass(err), err)
		}
		if wantErr := fmt.Sprintf("policy_denied: %d of %d resources denied (profile agent)", len(tc.denied), tc.checks); err.Error() != wantErr {
			t.Errorf("confirm %v: stderr line %q, want %q", confirm, err.Error(), wantErr)
		}
		if confirm && out != previewOut {
			t.Errorf("the confirmed run refused differently from its preview:\n%s\n---\n%s", previewOut, out)
		}
		previewOut = out
	}
	log := r.requests()
	for _, l := range log {
		if !strings.HasPrefix(l, http.MethodGet+" ") {
			t.Errorf("a refused plan sent %q", l)
		}
	}
	if n := r.ts.commandCount("config/entity_registry/remove"); n != 0 {
		t.Errorf("a refused plan removed %d registry entries", n)
	}
	var cmdLines []string
	for _, l := range readAudit(t, r.ts.dir) {
		if l["layer"] != "command" {
			t.Errorf("a refused plan reached the request layer: %v", l)
			continue
		}
		cmdLines = append(cmdLines, fmt.Sprintf("%v", l["dry_run"]))
	}
	if len(cmdLines) != 2*tc.checks || cmdLines[0] != "true" || cmdLines[len(cmdLines)-1] != "false" {
		t.Errorf("audit (dry_run per command line) = %v, want %d preview lines then %d confirmed", cmdLines, tc.checks, tc.checks)
	}
}

// TestAutoApplyRefusedByItsPlan: edit on the target's config; + create on the
// file's id when it differs (E-5, D-78) — a deny on the new id stops the
// rename; a file without an id is refused under any profile (R-6).
func TestAutoApplyRefusedByItsPlan(t *testing.T) {
	for _, tc := range []typedRefusal{
		{
			cmd: typedCommands[0], policy: typedPolicy("automation:heating/config", "edit"), action: "apply automation", checks: 1,
			denied: []string{`edit automation:heating/config — deny "automation:heating/config" [edit] (line 6)`},
		},
		{
			cmd: typedCommands[1], policy: typedPolicy("automation:heating_v*", "create"), action: "apply automation", checks: 2,
			denied: []string{`create automation:heating_v2 — deny "automation:heating_v*" [create] (line 6)`},
		},
		{
			cmd: typedCommands[2], policy: policyAllowEverything, action: "apply automation", checks: 2,
			denied: []string{"create automation:(no id) — fail-closed: automation without a config id"},
		},
	} {
		t.Run(tc.cmd.name, func(t *testing.T) { runTypedRefusal(t, tc) })
	}
}

// TestAutoCreateRefusedByItsPlan: create on the id the file declares.
func TestAutoCreateRefusedByItsPlan(t *testing.T) {
	runTypedRefusal(t, typedRefusal{
		cmd: typedCommands[3], policy: typedPolicy("automation:hall", "create"), action: "create automation", checks: 1,
		denied: []string{`create automation:hall — deny "automation:hall" [create] (line 6)`},
	})
}

// TestAutoDeleteRefusedByItsPlan: delete on the verified config id + the
// registry cleanup after it — a deny on the live entity alone refuses; an
// automation found only by alias or entity id is refused (D-78).
func TestAutoDeleteRefusedByItsPlan(t *testing.T) {
	for _, tc := range []typedRefusal{
		{
			cmd: typedCommands[4], policy: typedPolicy("entity:automation.*", "delete"), action: "delete automation", checks: 2,
			denied: []string{`delete entity:automation.heating — deny "entity:automation.*" [delete] (line 6)`},
		},
		{
			cmd: typedCommands[4], policy: typedPolicy("automation:heat*", "delete"), action: "delete automation", checks: 2,
			denied: []string{`delete automation:heating — deny "automation:heat*" [delete] (line 6)`},
		},
		{
			cmd: typedCommands[5], policy: policyAllowEverything, action: "delete automation", checks: 2,
			denied: []string{"delete automation:(no id) — fail-closed: automation has no config id"},
		},
	} {
		t.Run(tc.cmd.name+" "+tc.denied[0], func(t *testing.T) { runTypedRefusal(t, tc) })
	}
}

// TestAutoRollbackRefusedByItsPlan: edit on the automation the backup
// restores; + create on the backup's own id when it differs (E-5).
func TestAutoRollbackRefusedByItsPlan(t *testing.T) {
	for _, tc := range []typedRefusal{
		{
			cmd: typedCommands[6], policy: typedPolicy("automation:heating", "edit"), action: "roll back automation", checks: 1,
			denied: []string{`edit automation:heating/config — deny "automation:heating" [edit] (line 6)`},
		},
		{
			cmd: typedRollbackRename, setup: writeRenameBackup, policy: typedPolicy("automation:heating_old", "create"),
			action: "roll back automation", checks: 2,
			denied: []string{`create automation:heating_old — deny "automation:heating_old" [create] (line 6)`},
		},
	} {
		t.Run(tc.cmd.name, func(t *testing.T) { runTypedRefusal(t, tc) })
	}
}

// TestRollbackRefusedByItsPlan: the deprecated alias checks what auto
// rollback checks.
func TestRollbackRefusedByItsPlan(t *testing.T) {
	runTypedRefusal(t, typedRefusal{
		cmd: typedCommands[7], policy: typedPolicy("automation:*/config", "edit"), action: "roll back automation", checks: 1,
		denied: []string{`edit automation:heating/config — deny "automation:*/config" [edit] (line 6)`},
	})
}

// TestScriptApplyRefusedByItsPlan: the reload hactl sends after the write is
// in the plan — a deny on it refuses before the companion PUT, so no script is
// written and left unloaded.
func TestScriptApplyRefusedByItsPlan(t *testing.T) {
	runTypedRefusal(t, typedRefusal{
		cmd: typedCommands[8], policy: typedPolicy("service:script.reload", "control"), action: "apply script", checks: 2,
		denied: []string{`control service:script.reload — deny "service:script.reload" [control] (line 6)`},
	})
}

// TestScriptCreateRefusedByItsPlan: create on the file's top-level key.
func TestScriptCreateRefusedByItsPlan(t *testing.T) {
	runTypedRefusal(t, typedRefusal{
		cmd: typedCommands[9], policy: typedPolicy("script:*", "create"), action: "create script", checks: 1,
		denied: []string{`create script:morning — deny "script:*" [create] (line 6)`},
	})
}

// TestScriptDeleteRefusedByItsPlan: the registry cleanup is in the preview's
// plan, so a deny on the script entity refuses the preview.
func TestScriptDeleteRefusedByItsPlan(t *testing.T) {
	runTypedRefusal(t, typedRefusal{
		cmd: typedCommands[10], policy: typedPolicy("entity:script.wakeup", "delete"), action: "delete script", checks: 2,
		denied: []string{`delete entity:script.wakeup — deny "entity:script.wakeup" [delete] (line 6)`},
	})
}

// TestTplCreateRefusedByItsPlan: create on every unique_id the file declares,
// a bare item or each item of a block; an entity without one is refused under
// any profile (D-78).
func TestTplCreateRefusedByItsPlan(t *testing.T) {
	for _, tc := range []typedRefusal{
		{
			cmd: typedCommands[11], policy: typedPolicy("template:hall_*", "create"), action: "create template", checks: 1,
			denied: []string{`create template:hall_temp — deny "template:hall_*" [create] (line 6)`},
		},
		{
			cmd: typedCommands[12], policy: typedPolicy("template:c_*", "create"), action: "create template", checks: 3,
			denied: []string{`create template:c_on — deny "template:c_*" [create] (line 6)`},
		},
		{
			cmd: typedCommands[13], policy: policyAllowEverything, action: "create template", checks: 2,
			denied: []string{"create template:(no unique_id) — fail-closed: template entity without unique_id"},
		},
	} {
		t.Run(tc.cmd.name, func(t *testing.T) { runTypedRefusal(t, tc) })
	}
}

// TestTplDeleteRefusedByItsPlan: delete on the template + every registry entry
// of its unique_id.
func TestTplDeleteRefusedByItsPlan(t *testing.T) {
	runTypedRefusal(t, typedRefusal{
		cmd: typedCommands[14], policy: typedPolicy("entity:sensor.*", "delete"), action: "delete template", checks: 2,
		denied: []string{`delete entity:sensor.room_temp — deny "entity:sensor.*" [delete] (line 6)`},
	})
}

// TestHelperCreateRefusedByItsPlan: create on helper:<domain>/<id>, checked
// before the wiring probe.
func TestHelperCreateRefusedByItsPlan(t *testing.T) {
	runTypedRefusal(t, typedRefusal{
		cmd: typedCommands[15], policy: typedPolicy("helper:input_boolean/*", "create"), action: "create helper", checks: 1,
		denied: []string{`create helper:input_boolean/party_mode — deny "helper:input_boolean/*" [create] (line 6)`},
	})
}

// TestHelperDeleteRefusedByItsPlan: delete on helper:<domain>/<id> + its
// registry entry.
func TestHelperDeleteRefusedByItsPlan(t *testing.T) {
	for _, tc := range []typedRefusal{
		{
			cmd: typedCommands[16], policy: typedPolicy("helper:input_boolean/guest_mode", "delete"), action: "delete helper", checks: 2,
			denied: []string{`delete helper:input_boolean/guest_mode — deny "helper:input_boolean/guest_mode" [delete] (line 6)`},
		},
		{
			cmd: typedCommands[16], policy: typedPolicy("entity:input_boolean.*", "delete"), action: "delete helper", checks: 2,
			denied: []string{`delete entity:input_boolean.guest_mode — deny "entity:input_boolean.*" [delete] (line 6)`},
		},
	} {
		t.Run(tc.denied[0], func(t *testing.T) { runTypedRefusal(t, tc) })
	}
}

// typedChecked is each allowed command's plan size under allow-all.
var typedChecked = map[string]int{
	"auto apply": 1, "auto apply rename": 2, "auto create": 1, "auto delete": 2,
	"auto rollback": 1, "rollback": 1, "script apply": 2, "script create": 1, "script delete": 2,
	"tpl create": 1, "tpl create block": 3, "tpl delete": 2, "helper create": 1, "helper delete": 2,
}

// TestTypedPlansAreTheSameInPreviewAndConfirm (H-2's parity): under allow-all
// every typed command's preview says what it checked, and the confirmed run
// checks exactly the same (resource, verb) pairs in the same order before it
// writes — the audit's command lines, preview against confirmed.
func TestTypedPlansAreTheSameInPreviewAndConfirm(t *testing.T) {
	t.Setenv("HACTL_MANUAL_MODE", "off")
	for _, c := range typedCommands {
		n, ok := typedChecked[c.name]
		if !ok {
			continue // refused under any profile: the refusal tests hold it
		}
		t.Run(c.name, func(t *testing.T) {
			r := newTypedRig(t)
			writePolicyFile(t, r.ts.dir, policyAllowEverything)
			out, err := runPolicyCLI(t, r.ts.dir, c.argsIn(t, r)...)
			if err != nil {
				t.Fatalf("preview: %v\n%s", err, out)
			}
			if !regexp.MustCompile(fmt.Sprintf(`\n  policy: +%d checked, allowed \(profile agent\)\n`, n)).MatchString(out) {
				t.Errorf("the allowed preview does not say it checked %d:\n%s", n, out)
			}
			if out, err = runPolicyCLI(t, r.ts.dir, c.argsIn(t, r, "--confirm")...); err != nil {
				t.Fatalf("confirmed: %v\n%s", err, out)
			}
			var preview, confirmed []string
			for _, l := range readAudit(t, r.ts.dir) {
				if l["layer"] != "command" {
					continue
				}
				pair := fmt.Sprintf("%v %v %v", l["verb"], l["resource"], l["allowed"])
				if l["dry_run"] == true {
					preview = append(preview, pair)
				} else {
					confirmed = append(confirmed, pair)
				}
			}
			if len(preview) != n || !slices.Equal(preview, confirmed) {
				t.Errorf("preview plan %q, confirmed plan %q (want %d pairs, the same)", preview, confirmed, n)
			}
		})
	}
}

// TestHelperDeleteNamesTheCheckedDomain: while a profile is in effect the
// delete carries the domain its plan checked, so the companion acts on exactly
// helper:<domain>/<id> and the request layer can map it; without one it is
// sent as it always was (the D-58 golden holds that half).
func TestHelperDeleteNamesTheCheckedDomain(t *testing.T) {
	t.Setenv("HACTL_MANUAL_MODE", "off")
	r := newTypedRig(t)
	writePolicyFile(t, r.ts.dir, policyAllowEverything)
	if out, err := runPolicyCLI(t, r.ts.dir, "helper", "delete", "guest_mode", "--confirm"); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got := r.sentWrites(r.requests()); !slices.Equal(got, []string{
		"DELETE /v1/config/helper?domain=input_boolean&id=guest_mode",
		"ws config/entity_registry/remove ×1",
	}) {
		t.Errorf("sent %q", got)
	}
}

// The R-12 registration: each allowed typed --confirm run, on the family's rig,
// under allow-all — its request-layer verdicts must be a subset of its plan.
func init() {
	cases := make([]typedCommand, 0, len(typedCommands)+1)
	for _, c := range typedCommands {
		if _, ok := typedChecked[c.name]; ok {
			cases = append(cases, c)
		}
	}
	cases = append(cases, typedRollbackRename)
	for _, c := range cases {
		confirmedCoverageCases = append(confirmedCoverageCases, confirmedCoverageCase{
			name: c.name,
			start: func(t *testing.T) (string, []string) {
				t.Helper()
				t.Setenv("HACTL_MANUAL_MODE", "off")
				r := newTypedRig(t)
				if c.name == typedRollbackRename.name {
					writeRenameBackup(t, r)
				}
				return r.ts.dir, c.argsIn(t, r, "--confirm")
			},
		})
	}
}

// TestTypedPlansReadBodiesAsTheRequestLayerDoes: on the edge shapes a candidate
// file can take, each create plan names exactly the pairs the request layer
// maps the companion request carrying the same bytes to — and refuses exactly
// when it refuses (D-78, R-6). Two readings of one body that disagree would
// leave either a plan short of its write (caught late, by the request layer,
// as a hactl bug) or a preview refusing what the write would not.
func TestTypedPlansReadBodiesAsTheRequestLayerDoes(t *testing.T) {
	type tc struct {
		name, path, body string
		plan             *writePlan
	}
	cases := make([]tc, 0, 24)
	for _, body := range []string{
		"id: hall\nalias: Hall\n", "id: 1700000000000\n", "id: ''\n", "alias: no id\n",
		"- id: hall\n", "", "id: [a]\n", "id: \"x y\"\n",
	} {
		cases = append(cases, tc{"automation " + body, "/v1/config/automation", body, autoCreatePlan([]byte(body))})
	}
	for _, body := range []string{
		"unique_id: a\nstate: '1'\n", "name: no uid\n", "unique_id: 7\n",
		"sensor:\n- unique_id: a\n- unique_id: b\nlight:\n- unique_id: c\n",
		"sensor:\n- unique_id: a\n- name: b\n", "sensor: {unique_id: a}\n", "sensor:\n- just a string\n",
		"sensor: []\n", "triggers: []\nsensor:\n- unique_id: t\n", "", "- unique_id: a\n",
	} {
		cases = append(cases, tc{"template " + body, "/v1/config/template?domain=sensor", body, tplCreatePlan(body)})
	}
	for _, body := range []string{"morning:\n  sequence: []\n", "script.morning:\n  sequence: []\n", "a: {}\nb: {}\n", "", "- a\n"} {
		cases = append(cases, tc{"script " + body, "/v1/config/script", body, scriptCreatePlan(body)})
	}
	for _, c := range cases {
		res := reqmap.Classify(reqmap.Request{Transport: reqmap.Companion, Method: http.MethodPost, Path: c.path, Body: []byte(c.body)}, &reqmap.Facts{})
		var plan []string
		for _, ch := range c.plan.checks {
			plan = append(plan, ch.String())
		}
		switch {
		case res.Refused():
			if len(c.plan.failClosed) == 0 {
				t.Errorf("%q: the request layer refuses (%s), the plan allows %q", c.name, res.Rule, plan)
			}
		case res.Disposition != reqmap.Write:
			t.Errorf("%q: the request is %v, not a write", c.name, res.Disposition)
		case len(c.plan.failClosed) > 0:
			t.Errorf("%q: the plan refuses %v, the request layer maps %q", c.name, c.plan.failClosed, res.Strings())
		case !slices.Equal(plan, res.Strings()):
			t.Errorf("%q: plan %q, request layer %q", c.name, plan, res.Strings())
		}
	}
}

// TestTypedDeleteRefusedByADenyOnItsConfig (D-125, the §3.5 bypass lens):
// deleting an automation, script, template or helper deletes its config, so
// a `write` deny on that config refuses the delete — the part is denied, so
// all of it is.
func TestTypedDeleteRefusedByADenyOnItsConfig(t *testing.T) {
	part := " — a part of it is denied, so all of it is"
	for _, tc := range []typedRefusal{
		{
			cmd: typedCommands[4], policy: typedPolicy("automation:heating/config", "write"), action: "delete automation", checks: 2,
			denied: []string{`delete automation:heating — deny "automation:heating/config" [write] (line 6)` + part},
		},
		{
			cmd: typedCommands[10], policy: typedPolicy("script:wakeup/config", "write"), action: "delete script", checks: 2,
			denied: []string{`delete script:wakeup — deny "script:wakeup/config" [write] (line 6)` + part},
		},
		{
			cmd: typedCommands[14], policy: typedPolicy("template:room_temp/config", "write"), action: "delete template", checks: 2,
			denied: []string{`delete template:room_temp — deny "template:room_temp/config" [write] (line 6)` + part},
		},
		{
			cmd: typedCommands[16], policy: typedPolicy("helper:input_boolean/guest_mode/config", "write"), action: "delete helper", checks: 2,
			denied: []string{`delete helper:input_boolean/guest_mode — deny "helper:input_boolean/guest_mode/config" [write] (line 6)` + part},
		},
	} {
		t.Run(tc.cmd.name, func(t *testing.T) { runTypedRefusal(t, tc) })
	}
}
