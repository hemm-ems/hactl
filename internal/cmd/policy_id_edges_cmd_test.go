package cmd

// CLI spec tests for Jan's 2026-10-31 refinements of D-68 (refused characters
// at the edges of every id and free sub segment, control and format
// characters anywhere, interior spaces where Home Assistant allows them) and
// D-65 (an all-wildcard free sub segment counts as checked), authored from
// docs/decisions.md and the manual's "Policy (permission guardrail)" section.
// The package-level vectors are internal/policy/id_edges_spec_test.go; here
// the same rules are held at the command: an invalid resource is a refusal
// (exit 1, nothing on stdout), a broken pattern is a broken file naming its
// line, and lint's exit code follows the unchecked list.

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
)

const policyIDEdgesBody = `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "automation:pg heating": [read]
      "file:packages/my heating.yaml": [read]
      "entity:device_tracker.jan/attr/last seen": [read]
    deny:
      "entity:lock.front_door": [read]
`

// TestPolicyCheckRefusesIDEdgeCharacters — D-68 refined, resource half: a
// resource with leading/trailing white space, or a control or format
// character anywhere, in its id or a free sub segment, is invalid: exit 1,
// the reason on stderr, nothing on stdout — prose and --json alike. The
// trailing space on `entity:lock.front_door ` is the one that slipped past an
// exact deny.
func TestPolicyCheckRefusesIDEdgeCharacters(t *testing.T) {
	// A file valid under every reading of D-68, so a refusal here is the
	// resource's, never the file's; default allow, so a verdict on any of
	// these resources would be exit 0 and the exit code tells them apart too.
	dir := policyInstance(t, closedHAURL(t), `version: 1
active: agent
profiles:
  agent:
    deny:
      "entity:lock.front_door": [read]
`)
	if out, err := runPolicyCLI(t, dir, "policy", "check", "entity:lock.front_door", "read"); cliExitCode(err) != 1 || !strings.HasPrefix(out, "denied:") {
		t.Fatalf("control: policy check entity:lock.front_door read: exit %d stdout %q, want a denial verdict", cliExitCode(err), out)
	}
	for _, res := range []string{
		"entity:lock.front_door ",
		"entity:\u00a0lock.front_door",
		"automation:pg heating ",
		"automation: pg heating",
		"automation:pg\u200bheating",
		"automation:pg_heating\u202e",
		"script:\ufeffmorning",
		"device:abc\x00def",
		"template:uid\u2060_1",
		"config_entry:01J\u00adABC",
		"dashboard:main\x1b",
		"file:packages/my heating.yaml\u3000",
		"file:packages/heating.yaml /block/climate",
		"entity:device_tracker.jan/attr/latitude ",
		"entity:device_tracker.jan/attr/\u200dlatitude",
		"dashboard:main/views/home\t",
		"dashboard:/views/ home",
		"file:x.yaml/block/clim\u200bate",
		"system:reload/automation\u00a0",
		"system:reload/auto\x7fmation",
	} {
		for _, args := range [][]string{{res, "read"}, {res, "read", "--json"}} {
			out, err := runPolicyCLI(t, dir, append([]string{"policy", "check"}, args...)...)
			if cliExitCode(err) != 1 {
				t.Errorf("policy check %q: exit %d, want 1 (D-68 refined: an invalid resource is refused)", args, cliExitCode(err))
				continue
			}
			if out != "" {
				t.Errorf("policy check %q: a refusal wrote to stdout (a refusal carries no verdict): %q", args, out)
			}
		}
	}
}

// TestPolicyCheckAcceptsInteriorSpacesWhereHAAllowsThem — D-68 refined: an
// interior space in an automation config id, a file path or an attribute name
// is part of the id; the check answers it with a verdict (exit 0, allowed by
// the rule naming it), while the same space in an entity id is a refusal.
func TestPolicyCheckAcceptsInteriorSpacesWhereHAAllowsThem(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyIDEdgesBody)
	for _, tc := range []struct{ res, pattern string }{
		{"automation:pg heating", "automation:pg heating"},
		{"file:packages/my heating.yaml", "file:packages/my heating.yaml"},
		{"entity:device_tracker.jan/attr/last seen", "entity:device_tracker.jan/attr/last seen"},
	} {
		out, err := runPolicyCLI(t, dir, "policy", "check", tc.res, "read", "--json")
		if cliExitCode(err) != 0 {
			t.Errorf("policy check %q read: exit %d (%v), want 0 — an interior space is allowed here (D-68 refined)\n%s", tc.res, cliExitCode(err), err, out)
			continue
		}
		m := decodeObject(t, out)
		wantField(t, "check/"+tc.res, m, "allowed", true)
		if rule, _ := m["rule"].(string); !strings.Contains(rule, tc.pattern) {
			t.Errorf("policy check %q: rule %q, want the allow on %q", tc.res, rule, tc.pattern)
		}
	}
	out, err := runPolicyCLI(t, dir, "policy", "check", "entity:lock.front door", "read")
	if cliExitCode(err) != 1 || out != "" {
		t.Errorf("policy check \"entity:lock.front door\": exit %d stdout %q, want a refusal (entity ids stay HA's charset)", cliExitCode(err), out)
	}
}

// TestPolicyBrokenByAnEdgeCharacterNamesTheLine — D-68 refined, pattern half
// (D-60): a pattern whose free sub segment ends in a space is a broken file —
// `policy check` refuses naming <file>:<line>, `policy lint` reports it as
// `broken` with that line and exits 1. The interior-space rules beside it are
// no problem.
func TestPolicyBrokenByAnEdgeCharacterNamesTheLine(t *testing.T) {
	body := `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "automation:pg heating": [read]
    deny:
      "dashboard:main/views/home ": [read]
      "entity:*/attr/\u200blatitude": [read]
`
	dir := policyInstance(t, closedHAURL(t), body)
	file := filepath.Join(dir, policy.FileName)

	out, err := runPolicyCLI(t, dir, "policy", "check", "automation:pg heating", "read")
	if cliExitCode(err) != 1 || out != "" {
		t.Errorf("policy check on a broken file: exit %d stdout %q, want a refusal (exit 1, nothing on stdout)", cliExitCode(err), out)
	}
	if err != nil {
		for _, want := range []string{file + ":9", file + ":10"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q does not name %s", err, want)
			}
		}
		if strings.Contains(err.Error(), file+":7") {
			t.Errorf("refusal %q names line 7, whose interior space is allowed", err)
		}
	}

	out, err = runPolicyCLI(t, dir, "policy", "lint", "--json")
	if cliExitCode(err) != 1 {
		t.Errorf("policy lint on a broken file: exit %d, want 1", cliExitCode(err))
	}
	d := decodeLint(t, out)
	if d.Valid {
		t.Errorf("lint --json valid=true, want false\n%s", out)
	}
	if got, want := d.codes(), []string{"broken@9", "broken@10"}; !slices.Equal(got, want) {
		t.Errorf("lint findings %v, want %v\n%s", got, want, out)
	}
}

// TestPolicyLintAllWildcardFreeSubsAreChecked — D-65 refined at the CLI: a
// file whose free sub segments are all wildcards (views/*, reload/*,
// attr/*), over kinds the rig enumerates, lints clean — exit 0 without
// --allow-partial. One literal character beside a wildcard (`views/a*`)
// makes that rule unchecked again: exit 1, and --allow-partial exit 0 with
// the entry listed.
func TestPolicyLintAllWildcardFreeSubsAreChecked(t *testing.T) {
	clean := `version: 1
active: agent
profiles:
  agent:
    default: deny
    deny:
      "dashboard:main/views/*": [read]
      "dashboard:/views/*": [edit]
      "system:reload/*": [control]
      "entity:device_tracker.*/attr/*": [read]
`
	ts := policyRig(t, clean)
	out, err := runPolicyCLI(t, ts.dir, "policy", "lint", "--json")
	if cliExitCode(err) != 0 {
		t.Errorf("lint of all-wildcard free subs: exit %d (%v), want 0 (D-65 refined: nothing to misspell, so nothing unchecked)\n%s", cliExitCode(err), err, out)
	}
	m := decodeObject(t, out)
	wantField(t, "lint/all-wildcard", m, "findings", []any{})
	wantField(t, "lint/all-wildcard", m, "unchecked", []any{})

	ts = policyRig(t, clean+"      \"dashboard:main/views/a*\": [read]\n")
	want := []map[string]any{{"kind": "dashboard", "reason": "sub-resource not checked", "profile": "agent",
		"rule": `deny "dashboard:main/views/a*" [read]`, "line": 11}}
	out, err = runPolicyCLI(t, ts.dir, "policy", "lint", "--json")
	if cliExitCode(err) != 1 {
		t.Errorf("lint with views/a*: exit %d, want 1 (a literal character stays unchecked)\n%s", cliExitCode(err), out)
	}
	wantField(t, "lint/mixed", decodeObject(t, out), "unchecked", want)
	out, err = runPolicyCLI(t, ts.dir, "policy", "lint", "--allow-partial", "--json")
	if cliExitCode(err) != 0 {
		t.Errorf("lint --allow-partial with views/a*: exit %d (%v), want 0\n%s", cliExitCode(err), err, out)
	}
	wantField(t, "lint/mixed/allow-partial", decodeObject(t, out), "unchecked", want)
}
