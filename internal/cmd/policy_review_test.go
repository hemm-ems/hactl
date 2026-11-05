package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const reviewDraft = `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:*": [read]
      "entity:light.*": [control]
      "service:light.*": [control]
      "automation:pg_*": [edit, create, delete]
    deny:
      "entity:lock.*": [read, write]
      "entity:*/registry/entity_id": [edit]
`

type reviewJSON struct {
	File     string `json:"file"`
	Draft    bool   `json:"draft"`
	Profile  string `json:"profile"`
	AllowAll bool   `json:"allow_all"`
	Indirect string `json:"indirect"`
	Grants   []struct {
		Kind, Verb, Sub, Label string
		Allowed, Total         int
		IDs                    []string
		Unchecked              string
	} `json:"grants"`
	Create []struct {
		Kind  string
		Any   bool
		Rules []struct {
			Rule string
			Line int
		}
	} `json:"create"`
	Never []struct {
		Rule      string
		Line      int
		Kind      string
		Matched   int
		IDs       []string
		Unchecked string
	} `json:"never"`
	Lint struct {
		Findings  []json.RawMessage `json:"findings"`
		Unchecked []json.RawMessage `json:"unchecked"`
	} `json:"lint"`
}

func (d reviewJSON) grant(kind, verb, sub string) (allowed, total int, ids []string, ok bool) {
	for _, g := range d.Grants {
		if g.Kind == kind && g.Verb == verb && g.Sub == sub {
			return g.Allowed, g.Total, g.IDs, true
		}
	}
	return 0, 0, nil, false
}

// D-162: review counts what the draft's profile allows on the live instance —
// real ids from Home Assistant, decided by the same evaluator enforcement uses.
func TestPolicyReviewCountsTheDraftOnTheInstance(t *testing.T) {
	ts := policyRig(t, "")
	if err := os.WriteFile(filepath.Join(ts.dir, "policy.draft.yaml"), []byte(reviewDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runPolicyCLI(t, ts.dir, "policy", "review", "--file", "policy.draft.yaml", "--json")
	if err != nil {
		t.Fatalf("review: %v\n%s", err, out)
	}
	var d reviewJSON
	if err := json.Unmarshal([]byte(out), &d); err != nil {
		t.Fatalf("review --json: %v\n%s", err, out)
	}
	if !d.Draft || d.Profile != "agent" || d.AllowAll || d.Indirect != "checked" {
		t.Errorf("header: draft=%v profile=%q allow_all=%v indirect=%q", d.Draft, d.Profile, d.AllowAll, d.Indirect)
	}
	// The rig's entities: states (7) ∪ registry (6) = 8 distinct ids.
	if a, n, ids, ok := d.grant("entity", "control", ""); !ok || n != 8 || a != 2 || !slices.Equal(ids, []string{"light.hall", "light.kitchen"}) {
		t.Errorf("entity switch: %d of %d %v (ok=%v), want the two lights of 8", a, n, ids, ok)
	}
	if a, n, ids, _ := d.grant("entity", "read", ""); n != 8 || a != 7 || slices.Contains(ids, "lock.front_door") {
		t.Errorf("entity see: %d of %d %v, want everything but the lock", a, n, ids)
	}
	// A deny on a part (registry/entity_id) makes edit of the whole registry partial: denied.
	if a, _, _, _ := d.grant("entity", "edit", "registry"); a != 0 {
		t.Errorf("entity edit: %d allowed, want 0 (default deny, and a part is denied)", a)
	}
	if a, n, _, _ := d.grant("automation", "edit", "config"); n != 1 || a != 0 {
		t.Errorf("automation edit: %d of %d, want 0 of 1 (1700000000000 is not pg_*)", a, n)
	}
	if a, n, ids, _ := d.grant("service", "control", ""); n != 2 || a != 1 || !slices.Equal(ids, []string{"light.turn_on"}) {
		t.Errorf("service call: %d of %d %v, want light.turn_on of 2", a, n, ids)
	}
	if a, n, ids, _ := d.grant("dashboard", "delete", ""); n != 2 || a != 0 || len(ids) != 0 {
		t.Errorf("dashboard delete: %d of %d, want 0 of 2 (default and main)", a, n)
	}
	var autoCreate bool
	for _, c := range d.Create {
		if c.Kind == "automation" {
			autoCreate = true
			if c.Any || len(c.Rules) != 1 || c.Rules[0].Line != 10 {
				t.Errorf("automation create: %+v, want only the pg_* allow on line 10", c)
			}
		}
		if c.Kind == "script" && (c.Any || len(c.Rules) != 0) {
			t.Errorf("script create: %+v, want no", c)
		}
	}
	if !autoCreate {
		t.Error("no create row for automations")
	}
	if len(d.Never) != 2 || d.Never[0].Line != 12 || d.Never[0].Matched != 1 || !slices.Equal(d.Never[0].IDs, []string{"lock.front_door"}) || d.Never[1].Matched != 8 {
		t.Errorf("never: %+v", d.Never)
	}

	text, err := runPolicyCLI(t, ts.dir, "policy", "review", "--file", "policy.draft.yaml")
	if err != nil {
		t.Fatalf("review text: %v", err)
	}
	for _, want := range []string{
		"review: " + filepath.Join(ts.dir, "policy.draft.yaml") + " (draft, not in effect)\n",
		"profile: agent (selected by active), default: deny\n",
		"entities (8):\n",
		"  switch: 2 of 8 — light 2 of 2\n",
		"  call: 1 of 2 — light 1 of 1\n",
		"  create: only what allow \"automation:pg_*\" [edit create delete] (line 10) allows\n",
		"  deny \"entity:lock.*\" [read write] (line 12): 1 entities — lock.front_door\n",
		"(a part of each)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("review text lacks %q:\n%s", want, text)
		}
	}
}

// D-162: with no profile in effect there is nothing to count, exit 0; a
// broken draft is exit 1 as show reports one; an unreachable Home Assistant
// is exit 1 — a review that cannot count says nothing.
func TestPolicyReviewEdges(t *testing.T) {
	ts := policyRig(t, "")
	out, err := runPolicyCLI(t, ts.dir, "policy", "review")
	if err != nil || !strings.Contains(out, "ALLOW-ALL") {
		t.Errorf("review without a policy: err=%v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(ts.dir, "bad.yaml"), []byte("version: 1\nactive: nobody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runPolicyCLI(t, ts.dir, "policy", "review", "--file", "bad.yaml")
	if cliExitCode(err) != 1 || !strings.Contains(out, "policy_broken: "+filepath.Join(ts.dir, "bad.yaml")) {
		t.Errorf("review of a broken draft: exit %d\n%s", cliExitCode(err), out)
	}
	dead := instanceAt(t, closedHAURL(t))
	if err := os.WriteFile(filepath.Join(dead, "d.yaml"), []byte(reviewDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err = runPolicyCLI(t, dead, "policy", "review", "--file", "d.yaml")
	if cliExitCode(err) != 1 || !strings.Contains(err.Error(), "not reachable") || out != "" {
		t.Errorf("review with Home Assistant down: exit %d err %v\n%s", cliExitCode(err), err, out)
	}
}
