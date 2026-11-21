package cmd

import (
	"encoding/json"
	"fmt"
	"io"
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
	File     string        `json:"file"`
	Draft    bool          `json:"draft"`
	Profile  string        `json:"profile"`
	AllowAll bool          `json:"allow_all"`
	Indirect string        `json:"indirect"`
	Grants   []reviewGrant `json:"grants"`
	Create   []struct {
		Kind  string `json:"kind"`
		Any   bool   `json:"any"`
		Rules []struct {
			Rule string `json:"rule"`
			Line int    `json:"line"`
		} `json:"rules"`
	} `json:"create"`
	Never []struct {
		Rule      string   `json:"rule"`
		Line      int      `json:"line"`
		Kind      string   `json:"kind"`
		Matched   int      `json:"matched"`
		IDs       []string `json:"ids"`
		Unchecked string   `json:"unchecked"`
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

// reviewRig is the policy rig with reviewDraft beside its (absent) policy.
func reviewRig(t *testing.T) *cmdTestServer {
	t.Helper()
	ts := policyRig(t, "")
	if err := os.WriteFile(filepath.Join(ts.dir, "policy.draft.yaml"), []byte(reviewDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	return ts
}

func decodeReview(t *testing.T, ts *cmdTestServer) reviewJSON {
	t.Helper()
	out, err := runPolicyCLI(t, ts.dir, "policy", "review", "--file", "policy.draft.yaml", "--json")
	if err != nil {
		t.Fatalf("review: %v\n%s", err, out)
	}
	var d reviewJSON
	if jsonErr := json.Unmarshal([]byte(out), &d); jsonErr != nil {
		t.Fatalf("review --json: %v\n%s", jsonErr, out)
	}
	return d
}

// D-162: review counts what the draft's profile allows on the live instance —
// real ids from Home Assistant, decided by the same evaluator enforcement uses.
func TestPolicyReviewCountsTheDraftOnTheInstance(t *testing.T) {
	d := decodeReview(t, reviewRig(t))
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
}

// D-162: create is answered from the rules, never is every deny with what it
// reaches here.
func TestPolicyReviewCreateAndNever(t *testing.T) {
	d := decodeReview(t, reviewRig(t))
	creates := map[string]string{}
	for _, c := range d.Create {
		creates[c.Kind] = fmt.Sprintf("any=%v rules=%d", c.Any, len(c.Rules))
		if c.Kind == "automation" && (len(c.Rules) != 1 || c.Rules[0].Line != 10) {
			t.Errorf("automation create: %+v, want only the pg_* allow on line 10", c)
		}
	}
	if creates["automation"] != "any=false rules=1" || creates["script"] != "any=false rules=0" {
		t.Errorf("create rows: %v", creates)
	}
	if len(d.Never) != 2 || d.Never[0].Line != 12 || d.Never[0].Matched != 1 || !slices.Equal(d.Never[0].IDs, []string{"lock.front_door"}) || d.Never[1].Matched != 8 {
		t.Errorf("never: %+v", d.Never)
	}
}

// D-162: the text answer, for the owner.
func TestPolicyReviewText(t *testing.T) {
	ts := reviewRig(t)
	text, err := runPolicyCLI(t, ts.dir, "policy", "review", "--file", "policy.draft.yaml")
	if err != nil {
		t.Fatalf("review text: %v", err)
	}
	for _, want := range []string{
		"review: " + filepath.Join(ts.dir, "policy.draft.yaml") + " (draft, not in effect)\n",
		"profile: agent (selected by active), default: deny\n",
		"entities (8):\n",
		"  switch: 2 of 8 — light.hall, light.kitchen\n",
		"  call: 1 of 2 — light.turn_on\n",
		"  see: 7 of 8 — all but lock.front_door\n",
		"  create: only what allow \"automation:pg_*\" [edit create delete] (line 10) allows\n",
		"never: deny \"entity:lock.*\" [read write] (line 12): 1 entities — lock.front_door\n",
		"(a part of each)",
		"lint: 1 finding(s), 0 unchecked\n",
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
	if writeErr := os.WriteFile(filepath.Join(ts.dir, "bad.yaml"), []byte("version: 1\nactive: nobody\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	out, err = runPolicyCLI(t, ts.dir, "policy", "review", "--file", "bad.yaml")
	if cliExitCode(err) != 1 || !strings.Contains(out, "policy_broken: "+filepath.Join(ts.dir, "bad.yaml")) {
		t.Errorf("review of a broken draft: exit %d\n%s", cliExitCode(err), out)
	}
	dead := instanceAt(t, closedHAURL(t))
	if writeErr := os.WriteFile(filepath.Join(dead, "d.yaml"), []byte(reviewDraft), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	out, err = runPolicyCLI(t, dead, "policy", "review", "--file", "d.yaml")
	if cliExitCode(err) != 1 || !strings.Contains(err.Error(), "not reachable") || out != "" {
		t.Errorf("review with Home Assistant down: exit %d err %v\n%s", cliExitCode(err), err, out)
	}
}

// D-162: a line names at most ten ids, then `…, +<k> more`; a grant that
// reaches nearly everything names what it does not reach instead.
func TestPolicyReviewNamesTheShorterList(t *testing.T) {
	ids := []string{"a.1", "a.2", "a.3", "a.4", "a.5", "a.6", "a.7", "a.8", "a.9", "a.10", "a.11", "a.12"}
	if got, want := namesText("entity", ids), "a.1, a.2, a.3, a.4, a.5, a.6, a.7, a.8, a.9, a.10, …, +2 more"; got != want {
		t.Errorf("namesText = %q, want %q", got, want)
	}
	for _, tc := range []struct {
		g    reviewGrant
		want string
	}{
		{reviewGrant{Kind: "entity", Allowed: 0, Total: 3, denied: []string{"x.1", "x.2", "x.3"}}, ""},
		{reviewGrant{Kind: "entity", Allowed: 3, Total: 3, IDs: []string{"x.1", "x.2", "x.3"}}, " — all"},
		{reviewGrant{Kind: "entity", Allowed: 2, Total: 3, IDs: []string{"x.1", "x.2"}, denied: []string{"x.3"}}, " — all but x.3"},
		{reviewGrant{Kind: "entity", Allowed: 1, Total: 3, IDs: []string{"x.1"}, denied: []string{"x.2", "x.3"}}, " — x.1"},
		{reviewGrant{Kind: "dashboard", Allowed: 1, Total: 2, IDs: []string{""}, denied: []string{"main"}}, " — (default)"},
	} {
		if got := grantNamesText(tc.g); got != tc.want {
			t.Errorf("grantNamesText(%d of %d) = %q, want %q", tc.g.Allowed, tc.g.Total, got, tc.want)
		}
	}
}

const hidesLocks = `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "*": [read]
    deny:
      "entity:lock.*": [read]
`

// withoutOwnerTerminal forces the agent's side of ownerRun: no terminal.
func withoutOwnerTerminal(t *testing.T) {
	t.Helper()
	old := ownerTerminalCheck
	ownerTerminalCheck = func() bool { return false }
	t.Cleanup(func() { ownerTerminalCheck = old })
}

// D-172: the owner's review — at a terminal — counts the real instance even
// when the instance's own profile hides part of it from reads: its counts
// equal those of the same draft on an instance with no policy at all.
func TestPolicyReviewOwnerCountsTheRealInstance(t *testing.T) {
	withOwnerTerminal(t)
	free := decodeReview(t, reviewRig(t))

	ts := policyRig(t, hidesLocks)
	if err := os.WriteFile(filepath.Join(ts.dir, "policy.draft.yaml"), []byte(reviewDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	owner := decodeReview(t, ts)
	if !slices.EqualFunc(free.Grants, owner.Grants, func(a, b reviewGrant) bool {
		return a.Kind == b.Kind && a.Verb == b.Verb && a.Allowed == b.Allowed && a.Total == b.Total && slices.Equal(a.IDs, b.IDs)
	}) {
		t.Errorf("the owner's review under a profile hiding lock.* differs from the unfiltered one:\nfree  %+v\nowner %+v", free.Grants, owner.Grants)
	}
	if len(owner.Never) == 0 || owner.Never[0].Matched != free.Never[0].Matched {
		t.Errorf("never: owner %+v, unfiltered %+v", owner.Never, free.Never)
	}
	text, err := runPolicyCLI(t, ts.dir, "policy", "review", "--file", "policy.draft.yaml")
	if err != nil || strings.Contains(text, "hidden by policy") || !strings.Contains(text, "lock.front_door") {
		t.Errorf("the owner's review: %v\n%s", err, text)
	}
}

// D-172: anyone else's review — an agent's shell, an MCP server — runs under
// the session like every read: it counts only what the profile lets it read,
// names nothing hidden, and the trailer says how much was hidden.
func TestPolicyReviewWithoutATerminalSeesOnlyWhatTheProfileReads(t *testing.T) {
	withoutOwnerTerminal(t)
	ts := policyRig(t, hidesLocks)
	if err := os.WriteFile(filepath.Join(ts.dir, "policy.draft.yaml"), []byte(reviewDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	// The review is a document, so the trailer goes to stderr (D-139).
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldErr := os.Stderr
	os.Stderr = w
	text, err := runPolicyCLI(t, ts.dir, "policy", "review", "--file", "policy.draft.yaml")
	os.Stderr = oldErr
	_ = w.Close()
	stderr, _ := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		t.Fatalf("the agent's review: %v\n%s", err, text)
	}
	if strings.Contains(text, "lock.front_door") || strings.Contains(string(stderr), "lock.front_door") {
		t.Errorf("the agent's review names an entity its profile hides:\n%s", text)
	}
	if !strings.Contains(text, "entities (7):") || !strings.Contains(string(stderr), "hidden by policy: 1 (profile agent)") {
		t.Errorf("the agent's review does not count only what it may read, or does not say so (stderr %q):\n%s", stderr, text)
	}
}
