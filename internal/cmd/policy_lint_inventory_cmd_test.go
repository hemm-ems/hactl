package cmd

// CLI spec tests for the manual's "policy lint" that the earlier suites leave
// open: the entity inventory includes states-only entities, script rules are
// compared with the ids `script ls` prints, the stop-after-the-first-transport-
// failure rule holds when a WebSocket-backed kind fails first, and prose never
// prints `clean` while anything is unchecked.

import (
	"slices"
	"strings"
	"testing"
	"time"
)

// TestPolicyLintEntityInventoryIncludesStatesOnlyEntities — the manual compares
// entity rules with "the entity_ids in states and the entity registry": a YAML
// entity without a unique_id is in states only and is not a no-match.
func TestPolicyLintEntityInventoryIncludesStatesOnlyEntities(t *testing.T) {
	ts := policyRig(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.*": [read]
    deny:
      "entity:sensor.yaml_only": [write]
`)
	out, _ := runPolicyCLI(t, ts.dir, "policy", "lint", "--json")
	d := decodeLint(t, out)
	if got := d.codes(); len(got) != 0 {
		t.Errorf("findings %v, want none: sensor.yaml_only is in /api/states\n%s", got, out)
	}
}

// TestPolicyLintChecksScriptsAgainstTheIDsScriptLsPrints — script rules are
// compared with the object ids `script ls` prints (kino), not the entity id
// (script.kino).
func TestPolicyLintChecksScriptsAgainstTheIDsScriptLsPrints(t *testing.T) {
	ts := policyRig(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.*": [read]
    deny:
      "script:kino": [delete]
      "script:script.kino": [delete]
`)
	out, _ := runPolicyCLI(t, ts.dir, "policy", "lint", "--json")
	d := decodeLint(t, out)
	if got, want := d.codes(), []string{"no-match@10"}; !slices.Equal(got, want) {
		t.Errorf("findings %v, want %v (kino matches the script ls id; script.kino matches nothing)\n%s", got, want, out)
	}
	for _, u := range d.Unchecked {
		if u.Kind == "script" {
			t.Errorf("script unchecked (%s); script ids are read from the instance", u.Reason)
		}
	}
}

// TestPolicyLintStopsAskingWhenAWebSocketKindFailsFirst — manual "policy
// lint": every Home Assistant-backed kind not yet read is "listed unchecked
// with that same error". Here the first kind read is WebSocket-backed (device,
// area); the states-backed kinds after it (automation, script) must not dial
// Home Assistant again, so they carry the same reason and a stalled host costs
// one --timeout.
func TestPolicyLintStopsAskingWhenAWebSocketKindFailsFirst(t *testing.T) {
	for _, pc := range []struct {
		name, body string
		kinds      []string
	}{
		{"device+automation", "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    deny:\n      \"device:devhall\": [delete]\n      \"automation:pg_*\": [delete]\n", []string{"device", "automation"}},
		{"area+script", "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    deny:\n      \"area:hall_id\": [delete]\n      \"script:kino\": [delete]\n", []string{"area", "script"}},
	} {
		for _, host := range []struct {
			name, url string
		}{
			{"stalled", stalledHA(t)},
			{"refused", closedHAURL(t)},
		} {
			t.Run(pc.name+"/"+host.name, func(t *testing.T) {
				dir := policyInstance(t, host.url, pc.body)
				start := time.Now()
				out, err := runPolicyCLIWithTimeout(t, dir, "1s", "policy", "lint", "--json")
				if elapsed := time.Since(start); elapsed > 2500*time.Millisecond {
					t.Errorf("lint took %v with --timeout 1s; want one timeout, not one per kind", elapsed)
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
				if !slices.Equal(kinds, pc.kinds) {
					t.Errorf("unchecked kinds %v, want %v\n%s", kinds, pc.kinds, out)
				}
				for _, r := range reasons {
					if r == "" || r != reasons[0] {
						t.Errorf("unchecked reasons %q, want one and the same transport error for every kind", reasons)
						break
					}
				}
			})
		}
	}
}

// TestPolicyLintProseNeverSaysCleanWhileSomethingIsUnchecked — the manual: "an
// unchecked kind is never reported clean"; D-65: lint is never "clean" without
// --allow-partial while such a rule exists — and --allow-partial accepts the
// unchecked entries, it does not make them clean either.
func TestPolicyLintProseNeverSaysCleanWhileSomethingIsUnchecked(t *testing.T) {
	body := "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    deny:\n      \"file:**\": [write]\n"
	dir := policyInstance(t, closedHAURL(t), body)
	for _, args := range [][]string{{"policy", "lint"}, {"policy", "lint", "--allow-partial"}} {
		out, _ := runPolicyCLI(t, dir, args...)
		if !strings.Contains(out, "unchecked") {
			t.Errorf("%v does not list the unchecked kind:\n%s", args, out)
		}
		for line := range strings.SplitSeq(out, "\n") {
			if strings.TrimSpace(line) == "clean" {
				t.Errorf("%v prints `clean` while a kind is unchecked:\n%s", args, out)
			}
		}
	}
}
