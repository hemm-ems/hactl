package cmd

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/policy"
)

// Implementation tests for the CLI half of D-63..D-68 and the second review's
// fixes. The binding spec tests are in policy_cmd_test.go.

// D-66: policy check shows a partial verdict in prose and --json; its exit
// code is what it resolved to.
func TestPolicyCheckPartialVerdict(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), `version: 1
active: p
profiles:
  p:
    default: deny
    allow:
      "entity:*": [read, edit]
    deny:
      "entity:*/registry/entity_id": [edit]
      "entity:device_tracker.*/attr/latitude": [read]
`)
	type doc struct {
		Allowed       bool   `json:"allowed"`
		Rule          string `json:"rule"`
		Line          int    `json:"line"`
		Partial       bool   `json:"partial"`
		PartialDenies []struct {
			Rule string `json:"rule"`
			Line int    `json:"line"`
		} `json:"partial_denies"`
	}
	check := func(res, verb string) (doc, int, string) {
		t.Helper()
		out, err := runPolicyCLI(t, dir, "policy", "check", res, verb, "--json")
		var d doc
		if jerr := json.Unmarshal([]byte(out), &d); jerr != nil {
			t.Fatalf("check %s %s --json: %v\n%s", res, verb, jerr, out)
		}
		prose, _ := runPolicyCLI(t, dir, "policy", "check", res, verb)
		return d, cliExitCode(err), prose
	}

	d, code, prose := check("entity:light.x", "edit")
	if code != 1 || d.Allowed || !d.Partial || d.Line != 9 || len(d.PartialDenies) != 1 ||
		d.PartialDenies[0].Rule != `deny "entity:*/registry/entity_id" [edit]` {
		t.Errorf("edit on a parent of a denied field: exit %d %+v", code, d)
	}
	if !strings.HasPrefix(prose, "denied: entity:light.x edit") ||
		!strings.Contains(prose, `partial: deny "entity:*/registry/entity_id" [edit] (line 9)`) {
		t.Errorf("prose:\n%s", prose)
	}

	d, code, prose = check("entity:device_tracker.jan", "read")
	if code != 0 || !d.Allowed || !d.Partial || d.Line != 7 || len(d.PartialDenies) != 1 || d.PartialDenies[0].Line != 10 {
		t.Errorf("read on a parent of a denied attribute: exit %d %+v", code, d)
	}
	if !strings.HasPrefix(prose, "allowed: entity:device_tracker.jan read") || !strings.Contains(prose, "partial: ") {
		t.Errorf("prose:\n%s", prose)
	}

	out, _ := runPolicyCLI(t, dir, "policy", "check", "entity:light.x", "read", "--json")
	if !strings.Contains(out, `"partial": false`) || !strings.Contains(out, `"partial_denies": []`) {
		t.Errorf("a whole verdict must carry partial false and partial_denies []:\n%s", out)
	}
}

// D-60: a policy.yaml that is a dangling symlink is broken, never "no policy
// file": show and lint report it (exit 1), check refuses (exit 1, no stdout).
func TestPolicyDanglingSymlinkIsBroken(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), "")
	if err := os.Symlink(filepath.Join(dir, "moved.yaml"), filepath.Join(dir, policy.FileName)); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"policy", "show"}, {"policy", "lint"}, {"policy", "check", "entity:lock.x", "control"}} {
		out, err := runPolicyCLI(t, dir, args...)
		if cliExitCode(err) != 1 || strings.Contains(out, "no policy file (looked for") || strings.Contains(out, "everything is allowed") {
			t.Errorf("%v: exit %d, stdout:\n%s", args, cliExitCode(err), out)
		}
		if args[1] == "check" && out != "" {
			t.Errorf("check on a broken file printed a verdict:\n%s", out)
		}
		if args[1] != "check" && !strings.Contains(out, filepath.Join(dir, policy.FileName)+": ") {
			t.Errorf("%v does not name the path:\n%s", args, out)
		}
	}
}

// stalledHA accepts connections and never answers.
func stalledHA(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var conns []net.Conn
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			c, aerr := ln.Accept()
			if aerr != nil {
				return
			}
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	return "http://" + ln.Addr().String()
}

// Manual "policy lint": after the first transport failure lint stops asking;
// every Home Assistant-backed kind not yet read is unchecked with that same
// error, so a stalled host costs one --timeout, not one per kind.
func TestPolicyLintStopsAskingAfterTheFirstTransportFailure(t *testing.T) {
	dir := policyInstance(t, stalledHA(t), `version: 1
active: p
profiles:
  p:
    default: deny
    deny:
      "entity:lock.*": [write]
      "device:abc": [write]
      "area:kitchen": [write]
      "automation:pg_*": [write]
      "script:x": [write]
      "service:lock.*": [control]
      "system:logs": [read]
`)
	start := time.Now()
	var buf strings.Builder
	err := RunWithOutput([]string{"hactl", "policy", "lint", "--json", "--dir", dir, "--timeout", "1s"}, &buf)
	elapsed := time.Since(start)
	if cliExitCode(err) != 1 {
		t.Fatalf("exit %d, want 1", cliExitCode(err))
	}
	if elapsed > 2500*time.Millisecond {
		t.Errorf("lint took %v with --timeout 1s: one stalled host must cost one timeout, not one per kind", elapsed)
	}
	var doc struct {
		Unchecked []struct {
			Kind   string `json:"kind"`
			Reason string `json:"reason"`
		} `json:"unchecked"`
	}
	if jerr := json.Unmarshal([]byte(buf.String()), &doc); jerr != nil {
		t.Fatalf("%v\n%s", jerr, buf.String())
	}
	kinds := make([]string, 0, len(doc.Unchecked))
	reason := ""
	for _, u := range doc.Unchecked {
		kinds = append(kinds, u.Kind)
		if reason == "" {
			reason = u.Reason
		}
		if u.Reason != reason {
			t.Errorf("unchecked %s: reason %q, want the first failure's %q", u.Kind, u.Reason, reason)
		}
	}
	if strings.Join(kinds, ",") != "entity,device,area,automation,script,service" {
		t.Errorf("unchecked kinds = %v, want every HA-backed kind (system needs no HA)", kinds)
	}
}

// D-55: a selector placed after a matching plain allow can change neither the
// verdict nor the rule named, so policy check does not contact Home Assistant
// for it.
func TestPolicyCheckSkipsASelectorAnEarlierAllowMakesMoot(t *testing.T) {
	url, hits := countingHA(t)
	dir := policyInstance(t, url, `version: 1
active: p
profiles:
  p:
    default: deny
    allow:
      "entity:*": [control]
      "entity:light.*": [control]
    where:
      "entity:light.*": { area: wohnzimmer }
`)
	out, err := runPolicyCLI(t, dir, "policy", "check", "entity:light.sofa", "control", "--json")
	if cliExitCode(err) != 0 || !strings.Contains(out, `"line": 7`) || !strings.Contains(out, `"notes": []`) {
		t.Errorf("exit %d\n%s", cliExitCode(err), out)
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("policy check sent %d request(s) to Home Assistant for a selector that cannot change the verdict", n)
	}
}

// D-68 / D-67 / D-63 on the CLI: an invalid resource is refused (exit 1,
// nothing on stdout); a pattern breaking them is a broken file.
func TestPolicyCheckRefusesIDsTheGrammarRefuses(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), "version: 1\n")
	for _, res := range []string{"entity:lock.front_door ", "file:./secrets.yaml", "file:pkgs/block/a/x.yaml", "file:a.yaml/block", "area:wohn zimmer"} {
		out, err := runPolicyCLI(t, dir, "policy", "check", res, "read")
		if cliExitCode(err) != 1 || out != "" {
			t.Errorf("check %q: exit %d stdout %q, want a refusal", res, cliExitCode(err), out)
		}
	}
	broken := policyInstance(t, closedHAURL(t), "version: 1\nprofiles:\n  p:\n    deny:\n      \"entity:front_door\": [read]\n")
	out, err := runPolicyCLI(t, broken, "policy", "lint")
	if cliExitCode(err) != 1 || !strings.Contains(out, filepath.Join(broken, policy.FileName)+":5: ") || !strings.Contains(out, "D-63") {
		t.Errorf("lint on a domain-less literal: exit %d\n%s", cliExitCode(err), out)
	}
}
