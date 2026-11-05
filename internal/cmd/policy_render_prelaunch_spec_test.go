package cmd

// Blind spec tests for what the policy family and every refusal render after
// the pre-merge fixes (PLAN-policy-prelaunch-fixes §3.2), written from the
// contract only — docs/decisions.md D-117 (B-11), D-118 (B-12), D-119 (B-13),
// D-121 (B-15), D-124 (B-5), the manual's "Output conventions" (B-4) and its
// "Policy" sections — without reading internal/cmd/policy.go, policy_enforce.go
// or the refusal renderer.

import (
	"bytes"
	"fmt"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/manual"
	"github.com/hemm-ems/hactl/internal/policy"
)

const ownerLine = "policy_broken: policy.yaml belongs to the owner; only the owner changes it, through `hactl policy activate` — report this, do not edit it"

const ownerHint = "policy.yaml belongs to the owner; only the owner changes it, through `hactl policy activate` — report this, do not edit it"

// --- B-4: policy show is a document ---------------------------------------------

// TestPolicyShowIsNeverCapped — manual "Output conventions": documents are
// never capped, `policy show` among them. A policy far longer than the default
// 500-token cap is printed whole, with every rule, under the default cap.
func TestPolicyShowIsNeverCapped(t *testing.T) {
	var b strings.Builder
	b.WriteString("version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    deny:\n")
	const n = 150
	for i := range n {
		fmt.Fprintf(&b, "      \"entity:light.pg_long_rule_%03d\": [write]\n", i)
	}
	dir := policyInstance(t, closedHAURL(t), b.String())
	var buf bytes.Buffer
	// No --tokensmax: the default cap is what a caller gets.
	err := RunWithOutput([]string{"hactl", "policy", "show", "--dir", dir, "--timeout", "3s"}, &buf)
	if err != nil {
		t.Fatalf("policy show: %v", err)
	}
	out := buf.String()
	for i := range n {
		if rule := fmt.Sprintf(`deny "entity:light.pg_long_rule_%03d" [write]`, i); !strings.Contains(out, rule) {
			t.Fatalf("policy show stopped before rule %d (%s) — it was capped:\n…%s", i, rule, out[max(0, len(out)-400):])
		}
	}
	if strings.Contains(out, "capped at") {
		t.Errorf("policy show carries a cap hint:\n…%s", out[max(0, len(out)-400):])
	}
}

// --- B-11: one broken-file wording (D-117) ---------------------------------------

// brokenProblems is policyBroken's three problems as the one wording renders
// them: `policy_broken: <file>:<line>: <message>`; the verb typo carries its
// "did you mean", and so does the kind typo (`entiy` → `entity`).
func brokenProblemPrefixes(dir string) []string {
	file := filepath.Join(dir, policy.FileName)
	return []string{
		"policy_broken: " + file + ":2: ",
		"policy_broken: " + file + ":7: ",
		"policy_broken: " + file + ":9: ",
	}
}

func assertBrokenWording(t *testing.T, what, text string, dir string) {
	t.Helper()
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for _, prefix := range brokenProblemPrefixes(dir) {
		found := false
		for _, l := range lines {
			if strings.HasPrefix(l, prefix) {
				found = true
			}
		}
		if !found {
			t.Errorf("%s: no line starting %q:\n%s", what, prefix, text)
		}
	}
	file := filepath.Join(dir, policy.FileName)
	for _, l := range lines {
		if strings.HasPrefix(l, "policy_broken: "+file+":9: ") && !strings.HasSuffix(l, ` — did you mean "write"?`) {
			t.Errorf("%s: the verb typo `wirte` has no did-you-mean: %q", what, l)
		}
		if strings.HasPrefix(l, "policy_broken: "+file+":7: ") && !strings.HasSuffix(l, ` — did you mean "entity"?`) {
			t.Errorf("%s: the kind typo `entiy` has no did-you-mean: %q", what, l)
		}
	}
	if last := lines[len(lines)-1]; last != ownerLine {
		t.Errorf("%s: the output does not close with the owner line; last line %q", what, last)
	}
	if strings.Contains(text, "BROKEN") {
		t.Errorf("%s: a second wording (BROKEN) survives:\n%s", what, text)
	}
}

// TestBrokenPolicyHasOneWordingEverywhere — D-117: show and lint (stdout),
// check and every refused command (stderr), and an MCP refusal all print
// `policy_broken: <file>:<line>: <message>` per problem and close with the
// owner line; show and lint's stderr line is `policy_broken: <n> problem(s)
// in <file>`.
func TestBrokenPolicyHasOneWordingEverywhere(t *testing.T) {
	url, _ := countingHA(t)
	dir := policyInstance(t, url, policyBroken)
	file := filepath.Join(dir, policy.FileName)

	for _, args := range [][]string{{"policy", "show"}, {"policy", "lint"}} {
		out, err := runPolicyCLI(t, dir, args...)
		if cliExitCode(err) != 1 {
			t.Fatalf("%v: exit %d, want 1", args, cliExitCode(err))
		}
		assertBrokenWording(t, strings.Join(args, " "), out, dir)
		if want := "policy_broken: 3 problem(s) in " + file; err.Error() != want {
			t.Errorf("%v: stderr line %q, want %q", args, err.Error(), want)
		}
	}
	for _, args := range [][]string{{"policy", "check", "entity:light.x", "control"}, {"health"}, {"area", "ls"}} {
		out, err := runPolicyCLI(t, dir, args...)
		if cliExitCode(err) != 1 || out != "" {
			t.Errorf("%v: exit %d, stdout %q; want 1 and nothing", args, cliExitCode(err), out)
		}
		if err != nil {
			assertBrokenWording(t, strings.Join(args, " ")+" (stderr)", err.Error(), dir)
		}
	}
	ts := startCmdServer(t, map[string]any{"config/area_registry/list": []any{}}, nil)
	writePolicyFile(t, ts.dir, policyBroken)
	call := mcpCaller(t, ts.dir, false)
	text, isErr := call("area ls")
	if !isErr {
		t.Errorf("MCP area ls on a broken file was not refused:\n%s", text)
	}
	// The MCP server prefixes a refused call's text with "error: "; what
	// follows is the command's own stderr.
	assertBrokenWording(t, "MCP refusal", strings.TrimPrefix(text, "error: "), ts.dir)
}

// TestBrokenPolicyJSONCarriesTheOwnerHint — D-117: show keeps `problems`
// (message without the `<file>:<line>:` prefix, did-you-mean included) and
// lint keeps code `broken`; both add `hint` with the owner sentence, only
// when `valid` is false.
func TestBrokenPolicyJSONCarriesTheOwnerHint(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyBroken)
	out, _ := runPolicyCLI(t, dir, "policy", "show", "--json")
	m := decodeObject(t, out)
	wantField(t, "show", m, "hint", ownerHint)
	problems, _ := m["problems"].([]any)
	var verbMsg string
	for _, p := range problems {
		pm, _ := p.(map[string]any)
		if pm["line"] == float64(9) {
			verbMsg, _ = pm["message"].(string)
		}
	}
	if !strings.HasSuffix(verbMsg, ` — did you mean "write"?`) || strings.Contains(verbMsg, policy.FileName+":") {
		t.Errorf("show problem on line 9 = %q, want the message with its did-you-mean and no file prefix", verbMsg)
	}
	out, _ = runPolicyCLI(t, dir, "policy", "lint", "--json")
	wantField(t, "lint", decodeObject(t, out), "hint", ownerHint)

	valid := policyInstance(t, closedHAURL(t), policyAllowEverything)
	for _, args := range [][]string{{"policy", "show", "--json"}, {"policy", "lint", "--json"}} {
		out, _ = runPolicyCLI(t, valid, args...)
		if _, has := decodeObject(t, out)["hint"]; has {
			t.Errorf("%v: a valid file's document carries hint", args)
		}
	}
}

// TestBrokenPolicySuggestsTheNearestWord — D-117: an unknown verb, key, kind,
// sub-resource word or registry field name within two edits of a valid word of
// that position ends ` — did you mean "<word>"?`; farther away, nothing.
func TestBrokenPolicySuggestsTheNearestWord(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
	}{
		{"verb", "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"entity:lock.*\": [wirte]\n", `did you mean "write"?`},
		{"profile key", "version: 1\nactive: agent\nprofiles:\n  agent:\n    allows:\n      \"entity:lock.*\": [read]\n", `did you mean "allow"?`},
		{"sub word", "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"entity:*/registy/labels\": [edit]\n", `did you mean "registry"?`}, //nolint:misspell // the typo is the input
		{"registry field", "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"entity:*/registry/lables\": [edit]\n", `did you mean "labels"?`},
		{"kind", "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"entiy:lock.*\": [edit]\n", `did you mean "entity"?`},
		{"too far", "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"entity:lock.*\": [frobnicate]\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := policyInstance(t, closedHAURL(t), tc.body)
			out, err := runPolicyCLI(t, dir, "policy", "show")
			if cliExitCode(err) != 1 {
				t.Fatalf("exit %d, want 1 (broken)\n%s", cliExitCode(err), out)
			}
			prefix := "policy_broken: " + filepath.Join(dir, policy.FileName) + ":"
			var problem string
			for l := range strings.SplitSeq(out, "\n") {
				if strings.HasPrefix(l, prefix) {
					problem = l
				}
			}
			switch {
			case problem == "":
				t.Errorf("no problem line:\n%s", out)
			case tc.want == "" && strings.Contains(problem, "did you mean"):
				t.Errorf("a word three or more edits away got a suggestion: %q", problem)
			case tc.want != "" && !strings.HasSuffix(problem, " — "+tc.want):
				t.Errorf("problem %q does not end %q", problem, " — "+tc.want)
			}
		})
	}
}

// --- B-12 / B-13: the refusal's hint and its stderr line (D-118, D-119) ----------

// lastLine is the last non-empty line of s.
func lastLine(s string) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	return lines[len(lines)-1]
}

func assertHint(t *testing.T, what, out, wantHint string) {
	t.Helper()
	if got := lastLine(out); got != "  hint: "+wantHint {
		t.Errorf("%s: last line %q, want %q\n%s", what, got, "  hint: "+wantHint, out)
	}
}

// TestRefusalHintNamesTheCauseAndTheWayForward — D-118 and D-119 for an
// explicit deny, the default, and a mixed plan: the closing hint line (and the
// JSON hint) is built from the causes, and stderr is a count only.
func TestRefusalHintNamesTheCauseAndTheWayForward(t *testing.T) {
	t.Run("explicit deny", func(t *testing.T) {
		ts := startCmdServer(t, nil, nil)
		writePolicyFile(t, ts.dir, policyDenySecretLabel)
		const hint = "the owner would change the rule on line 6 — ask the owner; a change goes through a draft and `hactl policy activate`"
		out, err := runPolicyCLI(t, ts.dir, "label", "create", "Garden")
		assertHint(t, "label create", out, hint)
		if err == nil || err.Error() != "policy_denied: 1 of 1 resources denied (profile agent)" {
			t.Errorf("stderr line %v, want the count summary", err)
		}
		out, _ = runPolicyCLI(t, ts.dir, "label", "create", "Garden", "--json")
		wantField(t, "label create --json", decodeObject(t, out), "hint", hint)
	})
	t.Run("default deny", func(t *testing.T) {
		ts := startCmdServer(t, nil, nil)
		writePolicyFile(t, ts.dir, "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n")
		out, err := runPolicyCLI(t, ts.dir, "label", "create", "Garden")
		assertHint(t, "label create", out, "nothing allows label:*; the owner would add an allow — ask the owner; a change goes through a draft and `hactl policy activate`")
		if err == nil || err.Error() != "policy_denied: 1 of 1 resources denied (profile agent)" {
			t.Errorf("stderr line %v", err)
		}
	})
	t.Run("mixed plan", func(t *testing.T) {
		rig := startRegistryRig(t)
		writePolicyFile(t, rig.dir, denyPolicy(`"device:d1/registry/area_id": [edit]`))
		const hint = "the owner would change the rule on line 6; allowed: 2 of 3 checked resources, but the command is refused as a whole — ask the owner; a change goes through a draft and `hactl policy activate`"
		out, err := runPolicyCLI(t, rig.dir, "area", "delete", "hall")
		assertHint(t, "area delete", out, hint)
		if err == nil || err.Error() != "policy_denied: 1 of 3 resources denied (profile agent)" {
			t.Errorf("stderr line %v, want `policy_denied: 1 of 3 resources denied (profile agent)`", err)
		}
		if strings.Contains(err.Error(), "device:d1") {
			t.Errorf("stderr repeats a denial: %v", err)
		}
		out, _ = runPolicyCLI(t, rig.dir, "area", "delete", "hall", "--json")
		wantField(t, "area delete --json", decodeObject(t, out), "hint", hint)
	})
	t.Run("several defaults", func(t *testing.T) {
		rig := startSvcRig(t, reachOf("light.a", "light.b"))
		writePolicyFile(t, rig.dir, svcPolicy(`    default: deny`))
		out, err := runPolicyCLI(t, rig.dir, "svc", "call", "light.turn_on", "--data", `{"area_id":"hall"}`)
		re := regexp.MustCompile(`^  hint: nothing allows \S+ \(and 2 more\); the owner would add an allow — ask the owner; a change goes through a draft and ` + "`hactl policy activate`$")
		if !re.MatchString(lastLine(out)) {
			t.Errorf("last line %q, want %s\n%s", lastLine(out), re, out)
		}
		if err == nil || err.Error() != "policy_denied: 3 of 3 resources denied (profile agent)" {
			t.Errorf("stderr line %v", err)
		}
	})
	t.Run("fail-closed", func(t *testing.T) {
		rig := startSvcRig(t, wsErrorResponse{Code: "invalid_format", Message: "bad target"})
		writePolicyFile(t, rig.dir, policyAllowEverything)
		out, _ := runPolicyCLI(t, rig.dir, "svc", "call", "light.turn_on", "--data", `{"area_id":"hall"}`)
		hint := lastLine(out)
		if !strings.HasPrefix(hint, "  hint: hactl could not check ") || !strings.Contains(hint, ", so it refused") ||
			!strings.HasSuffix(hint, " — ask the owner; a change goes through a draft and `hactl policy activate`") {
			t.Errorf("fail-closed hint %q", hint)
		}
	})
}

// TestRefusalHintSaysThePreviewHadPassed — D-118's first part: a --confirm
// refused after its preview passed leads the hint with that.
func TestRefusalHintSaysThePreviewHadPassed(t *testing.T) {
	rig := startSvcRig(t, reachOf("light.a"))
	writePolicyFile(t, rig.dir, policyAllowEverything)
	args := []string{"svc", "call", "light.turn_on", "--data", `{"area_id":"hall"}`}
	if out, err := runPolicyCLI(t, rig.dir, args...); err != nil {
		t.Fatalf("allowed preview: %v\n%s", err, out)
	}
	recordWitness(stateCacheDir(rig.dir), manual.SessionKey(), "hactl svc call", []string{"light.turn_on"}, time.Now())
	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "entity:light.*": [control]`))
	out, _ := runPolicyCLI(t, rig.dir, append(args, "--confirm")...)
	assertHint(t, "refused --confirm", out, "the preview had passed, and something it depends on changed since (the policy, an area or label, a service call's reach); "+
		"the owner would change the rule on line 6; allowed: 1 of 2 checked resources, but the command is refused as a whole — ask the owner; a change goes through a draft and `hactl policy activate`")
}

// --- B-5: no decision id in anything rendered (D-124) ----------------------------

var decisionID = regexp.MustCompile(`\bD-[0-9]+\b`)

// TestRenderedPolicyOutputCitesNoDecisionID — D-124: no decision id in what a
// command renders — stdout, stderr, JSON, MCP text, the audit log — across
// the policy family and the refusal paths that used to cite "(D-60)",
// "(D-66)", "(D-80)": a broken file, a partial denial, a recheck at --confirm,
// a fail-closed refusal, a not-enforced typed control.
func TestRenderedPolicyOutputCitesNoDecisionID(t *testing.T) {
	var rendered []string
	add := func(what, out string, err error) {
		rendered = append(rendered, what+" (stdout): "+out)
		if err != nil {
			rendered = append(rendered, what+" (stderr): "+err.Error())
		}
	}

	manualDir := policyInstance(t, closedHAURL(t), policyManualExample)
	for _, args := range [][]string{
		{"policy", "show"}, {"policy", "show", "--json"}, {"policy", "lint"}, {"policy", "lint", "--json"},
		{"policy", "check", "entity:device_tracker.jan", "read"},
		{"policy", "check", "entity:device_tracker.jan", "read", "--json"},
		{"policy", "check", "automation:heating", "control"},
		{"policy", "check", "automation:heating", "control", "--json"},
		{"policy", "check", "entity:light.x/attr/brightness", "write"},
	} {
		out, err := runPolicyCLI(t, manualDir, args...)
		add(strings.Join(args, " "), out, err)
	}
	brokenDir := policyInstance(t, closedHAURL(t), policyBroken)
	for _, args := range [][]string{
		{"policy", "show"}, {"policy", "show", "--json"}, {"policy", "lint"}, {"policy", "lint", "--json"},
		{"policy", "check", "entity:light.x", "control"}, {"health"},
	} {
		out, err := runPolicyCLI(t, brokenDir, args...)
		add("broken: "+strings.Join(args, " "), out, err)
	}

	// A partial denial: a dashboard title change under a view deny.
	stored := map[string]any{"title": "Wall", "views": []any{dashView("home", "light.a"), dashView("secret", "lock.door")}}
	dr := dashRig(t, stored)
	writePolicyFile(t, dr.ts.dir, agentPolicy("    deny:\n      \"dashboard:wall-panel/views/secret\": [edit]\n"))
	title := writeDashFile(t, map[string]any{"title": "Renamed", "views": stored["views"]})
	for _, extra := range [][]string{nil, {"--json"}} {
		out, err := runPolicyCLI(t, dr.ts.dir, append([]string{"dash", "save", "wall-panel", "-f", title}, extra...)...)
		add("partial dash save", out, err)
	}

	// A recheck at --confirm after a passing preview.
	rig := startSvcRig(t, reachOf("light.a"))
	writePolicyFile(t, rig.dir, policyAllowEverything)
	svcArgs := []string{"svc", "call", "light.turn_on", "--data", `{"area_id":"hall"}`}
	if out, err := runPolicyCLI(t, rig.dir, svcArgs...); err != nil {
		t.Fatalf("allowed preview: %v\n%s", err, out)
	}
	recordWitness(stateCacheDir(rig.dir), manual.SessionKey(), "hactl svc call", []string{"light.turn_on"}, time.Now())
	writePolicyFile(t, rig.dir, svcPolicy(`    deny:`, `      "entity:light.*": [control]`))
	for _, extra := range [][]string{{"--confirm"}, {"--confirm", "--json"}} {
		out, err := runPolicyCLI(t, rig.dir, append(slices.Clone(svcArgs), extra...)...)
		add("recheck", out, err)
	}

	// Fail-closed.
	fc := startSvcRig(t, wsErrorResponse{Code: "invalid_format", Message: "bad target"})
	writePolicyFile(t, fc.dir, policyAllowEverything)
	for _, extra := range [][]string{nil, {"--json"}} {
		out, err := runPolicyCLI(t, fc.dir, append(slices.Clone(svcArgs), extra...)...)
		add("fail-closed", out, err)
	}

	// MCP: a broken file and a refusal.
	ts := startCmdServer(t, map[string]any{"config/area_registry/list": []any{}}, nil)
	writePolicyFile(t, ts.dir, policyBroken)
	text, _ := mcpCaller(t, ts.dir, false)("area ls")
	rendered = append(rendered, "MCP broken: "+text)
	writePolicyFile(t, ts.dir, policyDenySecretLabel)
	text, _ = mcpCaller(t, ts.dir, true)("label create Garden")
	rendered = append(rendered, "MCP refusal: "+text)

	// The audit log of every run above.
	for _, dir := range []string{dr.ts.dir, rig.dir, fc.dir, ts.dir} {
		for i, l := range readAudit(t, dir) {
			rendered = append(rendered, fmt.Sprintf("audit %s #%d: %v", filepath.Base(dir), i, l))
		}
	}

	for _, r := range rendered {
		if m := decisionID.FindString(r); m != "" {
			t.Errorf("rendered output cites decision id %s:\n%s", m, r)
		}
	}
}

// --- B-15: the delivered how-to is small; the reference on request (D-121) -------

// TestDeliveredPolicyHowToIsSmall — D-121: what the family delivers is the
// how-to only, at most 6 KiB, without the reference and without decision ids
// (D-124); no family and not the core delivers `### Policy reference`.
func TestDeliveredPolicyHowToIsSmall(t *testing.T) {
	howTo, _ := manual.FamilyText("policy", nil)
	if len(howTo) > 6144 {
		t.Errorf("the delivered policy block is %d bytes, want at most 6144", len(howTo))
	}
	if !strings.Contains(howTo, "### Policy (permission guardrail)") {
		t.Errorf("the delivered policy block lacks its how-to section")
	}
	if strings.Contains(howTo, "### Policy reference") {
		t.Errorf("the delivered policy block carries the reference")
	}
	if m := decisionID.FindString(howTo); m != "" {
		t.Errorf("the delivered how-to cites decision id %s", m)
	}
	if strings.Contains(manual.CoreText(), "### Policy reference") {
		t.Errorf("the core text carries the policy reference — it would be delivered to everyone")
	}
	for _, f := range manual.Families() {
		if text, _ := manual.FamilyText(f, nil); strings.Contains(text, "### Policy reference") {
			t.Errorf("family %q delivers the policy reference", f)
		}
	}
}

// TestRtfmReferenceFetchesAFamilysReference — D-121: `rtfm --reference policy`
// prints the reference section; a family without one says so; --families
// marks the family that has one; plain `rtfm` still prints it.
func TestRtfmReferenceFetchesAFamilysReference(t *testing.T) {
	run := func(args ...string) (string, error) {
		var buf bytes.Buffer
		err := RunWithOutput(append([]string{"hactl", "rtfm"}, args...), &buf)
		return buf.String(), err
	}
	out, err := run("--reference", "policy")
	if err != nil {
		t.Fatalf("rtfm --reference policy: %v", err)
	}
	for _, want := range []string{"### Policy reference", "#### Worked example: protect one automation", "| `verb-not-applicable` |"} {
		if !strings.Contains(out, want) {
			t.Errorf("rtfm --reference policy lacks %q", want)
		}
	}
	if strings.Contains(out, "## Quick routing") {
		t.Errorf("rtfm --reference policy leaks the core")
	}

	out, err = run("--reference", "auto")
	if err != nil || !strings.Contains(out, "(no reference section for 'auto')") {
		t.Errorf("rtfm --reference auto: %v\n%s", err, out)
	}
	if _, unknownErr := run("--reference", "nosuch"); unknownErr == nil {
		t.Errorf("rtfm --reference with an unknown family must fail like --family")
	}

	out, err = run("--families")
	if err != nil {
		t.Fatal(err)
	}
	var policyLine string
	for l := range strings.SplitSeq(out, "\n") {
		if strings.Contains(l, "policy") {
			policyLine = l
		}
	}
	if !regexp.MustCompile(`\+ reference ~\d+ tok`).MatchString(policyLine) {
		t.Errorf("rtfm --families does not mark policy's reference: %q", policyLine)
	}

	out, _ = run()
	if !strings.Contains(out, "### Policy reference") {
		t.Errorf("plain rtfm no longer prints the policy reference")
	}
}
