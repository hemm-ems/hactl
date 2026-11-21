//go:build integration

package integration

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// W-5: one instance directory per agent. Blind spec tests written from the
// contract only — docs/manual.md "Several agents" and "Setting up an agent"
// step 1, D-136, D-150..D-152, SPEC-policy-usp.md W-5 and M-4/M-6 — and W-4.5,
// the refusal hint. No implementation was read. What `agent add` claims to
// have made is read back from the directory and from Home Assistant's own
// token list.
// ============================================================================

// agentCompanionURL is the source instance's companion URL, which `agent add`
// copies. Nothing in these tests talks to it.
const agentCompanionURL = "http://127.0.0.1:9"

// agentSourceDir is the instance an agent is made from: HA_URL, HA_TOKEN and
// a COMPANION_URL, no policy.
func agentSourceDir(t *testing.T, inst *hatest.Instance) string {
	t.Helper()
	return setupInstanceDir(t, inst, "", false, "COMPANION_URL="+agentCompanionURL)
}

// agentTokenName is the name of the token `agent add` asks HA for (manual:
// `token: a long-lived token of its own, "hactl agent kitchen-helper"`).
func agentTokenName(name string) string { return "hactl agent " + name }

// addAgent runs `hactl agent add <name> --from <src> [extra…]` at a terminal
// (it "needs a terminal", like activate), as the owner, and returns the
// transcript. The contract shows no question it asks when Home Assistant
// mints the token, so none is answered.
func addAgent(t *testing.T, inst *hatest.Instance, home, src, name string, extra ...string) string {
	t.Helper()
	t.Cleanup(func() { deleteRefreshTokensNamed(t, inst, agentTokenName(name)) })
	args := append([]string{"agent", "add", name, "--from", src}, extra...)
	out, code := runPTYNoInput(t, home, home, args...)
	if code != 0 {
		t.Fatalf("hactl %v at a terminal: exit %d\n%s", args, code, out)
	}
	return out
}

// defaultAgentDir is where `agent add <name>` puts an agent without --path.
func defaultAgentDir(home, name string) string {
	return filepath.Join(home, ".hactl", "agents", name)
}

// expandHome turns a printed `~/…` into a path under home.
func expandHome(home, p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return p
}

func samePath(t *testing.T, a, b string) bool {
	t.Helper()
	ra, errA := filepath.EvalSymlinks(a)
	rb, errB := filepath.EvalSymlinks(b)
	return errA == nil && errB == nil && ra == rb
}

// TestPolicyAgentsSpecAddCreatesAnAgentDirectory — manual: "`agent add
// <name>` copies `HA_URL` and `COMPANION_URL` from the source instance
// (`--from <dir>` …), writes `HACTL_POLICY=required` (D-136), and gives the
// agent **a token of its own** (M-6), so Home Assistant's own records tell the
// agents apart. It asks Home Assistant for one"; the console example: `token:
// a long-lived token of its own, "hactl agent kitchen-helper"`, `next: give
// the agent \`hactl rtfm --skill policy-setup\“, `mcp: claude mcp add
// kitchen-helper -- hactl mcp --dir ~/.hactl/agents/kitchen-helper
// --allow-writes`. D-151: "one instance directory per agent (own `.env`,
// token, `policy.yaml`, audit log)".
func TestPolicyAgentsSpecAddCreatesAnAgentDirectory(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	home := setupHome(t)
	src := agentSourceDir(t, inst)
	const name = "pg-spec-kitchen"
	out := addAgent(t, inst, home, src, name)
	dir := defaultAgentDir(home, name)

	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		t.Fatalf("agent add did not create %s (default: ~/.hactl/agents/<name>)\n%s", dir, out)
	}
	env := parseEnvFile(t, filepath.Join(dir, ".env"))
	if env["HA_URL"] != inst.URL() {
		t.Errorf(".env HA_URL = %q, want the source's %q", env["HA_URL"], inst.URL())
	}
	if env["COMPANION_URL"] != agentCompanionURL {
		t.Errorf(".env COMPANION_URL = %q, want the source's %q", env["COMPANION_URL"], agentCompanionURL)
	}
	if env["HACTL_POLICY"] != "required" {
		t.Errorf(".env HACTL_POLICY = %q, want required (D-136, M-4)", env["HACTL_POLICY"])
	}
	switch tok := env["HA_TOKEN"]; {
	case tok == "":
		t.Error(".env has no HA_TOKEN")
	case tok == inst.Token():
		t.Error(".env HA_TOKEN is the source's token — the agent must get a token of its own (M-6)")
	case !haTokenWorks(t, inst, tok):
		t.Error("Home Assistant does not accept the agent's HA_TOKEN")
	}
	if !slices.ContainsFunc(haRefreshTokens(t, inst), func(rt haRefreshToken) bool {
		return rt.ClientName == agentTokenName(name) && rt.Type == "long_lived_access_token"
	}) {
		t.Errorf("Home Assistant lists no long-lived token named %q — its records cannot tell the agent apart", agentTokenName(name))
	}
	if !fileExists(filepath.Join(dir, "policy.yaml")) {
		t.Error("agent add wrote no starter policy.yaml (D-150)")
	}

	m := regexp.MustCompile(`claude mcp add ` + regexp.QuoteMeta(name) + ` -- hactl mcp --dir (\S+) --allow-writes`).FindStringSubmatch(out)
	if m == nil {
		t.Errorf("agent add printed no MCP config line `claude mcp add %s -- hactl mcp --dir <dir> --allow-writes`\n%s", name, out)
	} else if !samePath(t, expandHome(home, m[1]), dir) {
		t.Errorf("the MCP line pins %q, want the agent's directory %s", m[1], dir)
	}
	if !strings.Contains(out, "hactl rtfm --skill policy-setup") {
		t.Errorf("agent add does not point the owner at `hactl rtfm --skill policy-setup`\n%s", out)
	}
}

// TestPolicyAgentsSpecStarterPolicyIsReadOnly — D-150: "`agent add` writes a
// starter `policy.yaml`: one active profile `readonly` that denies every
// write. The agent can inventory and draft"; manual step 1: "its
// `policy.yaml` holds one profile that denies every write, so it can look at
// everything it needs to propose rules and change nothing." Checked as
// verdicts of every kind, then on real commands read back from HA.
func TestPolicyAgentsSpecStarterPolicyIsReadOnly(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	home := setupHome(t)
	const name = "pg-spec-starter"
	addAgent(t, inst, home, agentSourceDir(t, inst), name)
	dir := defaultAgentDir(home, name)
	run := func(args ...string) procResult {
		t.Helper()
		return runSetupBin(t, home, dir, nil, append([]string{"--dir", dir}, args...)...)
	}

	var show struct {
		Valid      bool     `json:"valid"`
		Profile    string   `json:"profile"`
		SelectedBy string   `json:"selected_by"`
		Profiles   []string `json:"profiles"`
	}
	res := run("policy", "show", "--json")
	if err := json.Unmarshal([]byte(res.Stdout), &show); err != nil {
		t.Fatalf("policy show --json in the agent's directory: %v\n%s", err, res)
	}
	if !show.Valid || show.Profile != "readonly" || show.SelectedBy != "active" || !slices.Equal(show.Profiles, []string{"readonly"}) {
		t.Errorf("starter policy: valid=%v profile=%q selected_by=%q profiles=%v, want one active profile readonly",
			show.Valid, show.Profile, show.SelectedBy, show.Profiles)
	}

	states := haStates(t, inst)
	lights, locks := withPrefix(states, "light."), withPrefix(states, "lock.")
	if len(lights) == 0 || len(locks) == 0 {
		t.Fatalf("fixture: %d lights, %d locks", len(lights), len(locks))
	}
	light := lights[0]
	for _, r := range []string{
		"entity:" + light, "entity:" + locks[0], "automation:pg_spec_auto", "script:pg_policy_allowed",
		"dashboard:lovelace", "area:pg_spec_area", "label:pg_spec_label", "helper:input_boolean/pg_policy_lock_state",
		"file:configuration.yaml", "service:light.turn_on",
	} {
		var v struct {
			Allowed bool `json:"allowed"`
		}
		c := run("policy", "check", r, "write", "--json")
		if err := json.Unmarshal([]byte(c.Stdout), &v); err != nil || v.Allowed || c.Code != 1 {
			t.Errorf("starter: policy check %s write: exit %d %s, want denied (every write)", r, c.Code, strings.TrimSpace(c.Stdout+c.Stderr))
		}
	}
	for _, r := range []string{"entity:" + light, "entity:" + locks[0], "dashboard:lovelace", "area:pg_spec_area"} {
		if c := run("policy", "check", r, "read"); c.Code != 0 {
			t.Errorf("starter: policy check %s read: exit %d, want allowed — the agent can look at everything\n%s", r, c.Code, c)
		}
	}

	// Real reads work and hide nothing.
	for _, args := range [][]string{{"ent", "ls", "--domain", "light"}, {"area", "ls"}, {"policy", "lint", "--allow-partial"}} {
		r := run(args...)
		if r.Code != 0 || strings.Contains(r.Stdout+r.Stderr, "hidden by policy") {
			t.Errorf("starter: hactl %v must read everything: %s", args, r)
		}
	}
	if r := run("ent", "ls", "--domain", "light"); !strings.Contains(r.Stdout, light) {
		t.Errorf("starter: ent ls --domain light does not list %s\n%s", light, r.Stdout)
	}

	// Real writes are refused and change nothing.
	beforeState := pgStateOf(t, inst, light).State
	data := `{"entity_id":"` + light + `"}`
	for _, args := range [][]string{
		{"svc", "call", "light.toggle", "-d", data},
		{"svc", "call", "light.toggle", "-d", data, "--confirm"},
		{"area", "create", "PG W5 Starter"},
		{"area", "create", "PG W5 Starter", "--confirm"},
	} {
		r := run(args...)
		if r.Code != 1 || !strings.Contains(r.Stdout, "policy_denied: would ") || !strings.Contains(r.Stdout, "profile: readonly") {
			t.Errorf("starter: hactl %v: want the policy_denied refusal under profile readonly\n%s", args, r)
		}
	}
	if after := pgStateOf(t, inst, light).State; after != beforeState {
		t.Errorf("starter: a refused light.toggle switched %s (%s → %s)", light, beforeState, after)
	}
	if ids := haAreasNamed(t, inst, "PG W5 Starter"); len(ids) > 0 {
		t.Errorf("starter: a refused area create made %v", ids)
		deleteAreasNamed(t, inst, "PG W5 Starter")
	}
}

// agentLsRow is the `agent ls` row for name, split into fields: NAME DIR PROFILE REQUIRED LAST WRITE CHECK.
func agentLsRow(out, name string) []string {
	for l := range strings.SplitSeq(out, "\n") {
		f := strings.Fields(l)
		if len(f) > 0 && f[0] == name {
			return f
		}
	}
	return nil
}

// TestPolicyAgentsSpecAgentLs — manual: "`agent ls` lists the agent
// directories under `~/.hactl/agents/` (or `--root`)", with the columns
// `NAME DIR PROFILE REQUIRED LAST WRITE CHECK` and a fresh agent's row
// `kitchen-helper ~/.hactl/agents/kitchen-helper readonly yes never`; W-5:
// "lists the agent dirs, active profile and last audit line". `--path`
// chooses the directory, so an agent made elsewhere is listed under that
// root, not the default one.
func TestPolicyAgentsSpecAgentLs(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	home := setupHome(t)
	src := agentSourceDir(t, inst)
	const here, there = "pg-spec-ls-here", "pg-spec-ls-there"
	addAgent(t, inst, home, src, here)
	elsewhere := filepath.Join(home, "elsewhere")
	if err := os.MkdirAll(elsewhere, 0o750); err != nil {
		t.Fatal(err)
	}
	thereDir := filepath.Join(elsewhere, there)
	addAgent(t, inst, home, src, there, "--path", thereDir)
	if !fileExists(filepath.Join(thereDir, ".env")) || fileExists(defaultAgentDir(home, there)) {
		t.Fatalf("agent add --path %s did not put the agent there", thereDir)
	}

	ls := runSetupBin(t, home, home, nil, "agent", "ls")
	if ls.Code != 0 {
		t.Fatalf("agent ls: %s", ls)
	}
	header := strings.Fields(strings.SplitN(strings.TrimSpace(ls.Stdout), "\n", 2)[0])
	if want := []string{"NAME", "DIR", "PROFILE", "REQUIRED", "LAST", "WRITE", "CHECK"}; !slices.Equal(header, want) {
		t.Errorf("agent ls header = %v, want %v", header, want)
	}
	row := agentLsRow(ls.Stdout, here)
	if row == nil {
		t.Fatalf("agent ls does not list %s\n%s", here, ls.Stdout)
	}
	if len(row) < 5 || !samePath(t, expandHome(home, row[1]), defaultAgentDir(home, here)) ||
		row[2] != "readonly" || row[3] != "yes" || row[len(row)-1] != "never" {
		t.Errorf("agent ls row for a fresh agent = %v, want [%s <its dir> readonly yes never]", row, here)
	}
	if agentLsRow(ls.Stdout, there) != nil {
		t.Errorf("agent ls (default root) lists %s, which lives under %s\n%s", there, elsewhere, ls.Stdout)
	}

	rooted := runSetupBin(t, home, home, nil, "agent", "ls", "--root", elsewhere)
	if r := agentLsRow(rooted.Stdout, there); r == nil || r[2] != "readonly" {
		t.Errorf("agent ls --root %s does not list %s as readonly\n%s", elsewhere, there, rooted)
	}

	// A write check — here a refused dry run — is the agent's last audit line.
	hereDir := defaultAgentDir(home, here)
	if r := runSetupBin(t, home, hereDir, nil, "--dir", hereDir, "area", "create", "PG W5 Ls"); r.Code != 1 {
		t.Fatalf("starter: area create (dry run) not refused: %s", r)
	}
	if len(readAuditLog(t, hereDir)) == 0 {
		t.Fatalf("the refused dry run left no line in %s (D-71)", auditLogPath(hereDir))
	}
	after := runSetupBin(t, home, home, nil, "agent", "ls")
	if r := agentLsRow(after.Stdout, here); r == nil || r[len(r)-1] == "never" {
		t.Errorf("agent ls after a write check still says LAST WRITE CHECK never: %v\n%s", r, after.Stdout)
	}
}

// TestPolicyAgentsSpecAddNeedsATerminal — manual: "`agent add` … Like
// `activate`, it needs a terminal"; D-148: hactl writes policy.yaml only in
// `policy activate` and `agent add`'s starter, "both of which need a
// terminal". Run as an agent would — no terminal — it makes no directory and
// asks Home Assistant for no token.
func TestPolicyAgentsSpecAddNeedsATerminal(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	home := setupHome(t)
	const name = "pg-spec-noterm"
	t.Cleanup(func() { deleteRefreshTokensNamed(t, inst, agentTokenName(name)) })
	res := runSetupBin(t, home, home, nil, "agent", "add", name, "--from", agentSourceDir(t, inst))
	if res.Code == 0 {
		t.Errorf("agent add without a terminal exited 0\n%s", res)
	}
	if !strings.Contains(strings.ToLower(res.Stdout+res.Stderr), "terminal") {
		t.Errorf("the refusal does not say it needs a terminal\n%s", res)
	}
	assertNoAgentMade(t, inst, home, name)
}

// TestPolicyAgentsSpecAddOverMCPIsRefused — an MCP server has no terminal, so
// `agent add` (which "needs a terminal") cannot run there; asked of a server
// that allows writes, it makes nothing.
func TestPolicyAgentsSpecAddOverMCPIsRefused(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	home := setupHome(t)
	src := agentSourceDir(t, inst)
	const name = "pg-spec-mcp"
	t.Cleanup(func() { deleteRefreshTokensNamed(t, inst, agentTokenName(name)) })
	c := startMCPServer(t, home, src)
	text, isErr := c.call(t, "agent add "+name+" --from "+src)
	if !isErr {
		t.Errorf("agent add over MCP was not an error result:\n%s", text)
	}
	assertNoAgentMade(t, inst, home, name)
}

func assertNoAgentMade(t *testing.T, inst *hatest.Instance, home, name string) {
	t.Helper()
	if fileExists(defaultAgentDir(home, name)) {
		t.Errorf("a refused agent add created %s", defaultAgentDir(home, name))
	}
	if slices.ContainsFunc(haRefreshTokens(t, inst), func(rt haRefreshToken) bool { return rt.ClientName == agentTokenName(name) }) {
		t.Errorf("a refused agent add minted the token %q in Home Assistant", agentTokenName(name))
	}
}

// ---------------------------------------------------------------------------
// Two agents on one Home Assistant
// ---------------------------------------------------------------------------

const (
	w5Kitchen = "pg_w5_kitchen"
	w5Garden  = "pg_w5_garden"
)

// w5KitchenPolicy may create areas; w5GardenPolicy may not.
var (
	w5KitchenPolicy = "version: 1\nactive: " + w5Kitchen + "\nprofiles:\n  " + w5Kitchen + ":\n    default: deny\n    allow:\n      \"entity:*\": [read]\n      \"area:*\": [create, delete]\n"
	w5GardenPolicy  = "version: 1\nactive: " + w5Garden + "\nprofiles:\n  " + w5Garden + ":\n    default: deny\n    allow:\n      \"entity:*\": [read]\n    deny:\n      \"area:*\": [create]\n"
)

// TestPolicyAgentsSpecTwoAgentsOwnVerdictsAndAudit — D-151: "one instance
// directory per agent (own `.env`, token, `policy.yaml`, audit log) … Keeps
// D-62 (the file picks the profile) and D-64 (a pinned MCP server)"; manual:
// "each agent gets exactly its own rules"; W-5 acceptance: "two pinned MCP
// servers on one HA, different profiles, gives each one exactly its own
// verdicts and audit log". The same command, in the same order, on the CLI and
// through each agent's own pinned server.
func TestPolicyAgentsSpecTwoAgentsOwnVerdictsAndAudit(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	home := setupHome(t)
	kitchen := setupInstanceDir(t, inst, w5KitchenPolicy, true)
	garden := setupInstanceDir(t, inst, w5GardenPolicy, true)
	gardenRule := `deny "area:*" [create] (line ` + strconv.Itoa(policyLineOf(t, w5GardenPolicy, `"area:*"`)) + ")"
	t.Cleanup(func() { deleteAreasNamed(t, inst, "PG W5 Two") })

	k := runSetupBin(t, home, kitchen, nil, "--dir", kitchen, "area", "create", "PG W5 Two")
	if k.Code != 0 || !allowedPreviewLine(w5Kitchen).MatchString(k.Stdout) {
		t.Errorf("kitchen: area create (dry run) want allowed under %s\n%s", w5Kitchen, k)
	}
	g := runSetupBin(t, home, garden, nil, "--dir", garden, "area", "create", "PG W5 Two")
	if g.Code != 1 || !strings.Contains(g.Stdout, gardenRule) || !strings.Contains(g.Stdout, "profile: "+w5Garden) {
		t.Errorf("garden: area create (dry run) want refused by %s under %s\n%s", gardenRule, w5Garden, g)
	}

	mk := startMCPServer(t, home, kitchen)
	mg := startMCPServer(t, home, garden)
	for range 2 {
		kt, kErr := mk.call(t, `area create "PG W5 Two"`)
		if kErr || !strings.Contains(kt, "allowed (profile "+w5Kitchen+")") || strings.Contains(kt, w5Garden) {
			t.Errorf("kitchen's MCP server: want allowed under %s only (isError=%v)\n%s", w5Kitchen, kErr, kt)
		}
		gt, gErr := mg.call(t, `area create "PG W5 Two"`)
		if !gErr || !strings.Contains(gt, "policy_denied") || !strings.Contains(gt, w5Garden) || strings.Contains(gt, w5Kitchen) {
			t.Errorf("garden's MCP server: want the refusal under %s only (isError=%v)\n%s", w5Garden, gErr, gt)
		}
	}
	if ids := haAreasNamed(t, inst, "PG W5 Two"); len(ids) > 0 {
		t.Errorf("dry runs created areas: %v", ids)
	}

	for _, tc := range []struct {
		dir, profile, other string
		allowed             bool
	}{
		{kitchen, w5Kitchen, w5Garden, true},
		{garden, w5Garden, w5Kitchen, false},
	} {
		lines := readAuditLog(t, tc.dir)
		if len(lines) < 3 {
			t.Errorf("%s's audit log holds %d lines, want one per write check (1 CLI + 2 MCP dry runs)", tc.profile, len(lines))
		}
		for _, l := range lines {
			if l.Profile != tc.profile {
				t.Errorf("%s's audit log holds a line of profile %q: %+v", tc.profile, l.Profile, l)
			}
		}
		if !slices.ContainsFunc(lines, func(l pgAuditLine) bool { return l.Allowed == tc.allowed }) {
			t.Errorf("%s's audit log has no line with allowed=%v", tc.profile, tc.allowed)
		}
		raw, _ := os.ReadFile(filepath.Clean(auditLogPath(tc.dir)))
		if strings.Contains(string(raw), tc.other) {
			t.Errorf("%s's audit log mentions the other agent's profile %s", tc.profile, tc.other)
		}
	}
}

// ---------------------------------------------------------------------------
// W-4.5: the refusal hint sends the agent to the owner and the draft flow
// ---------------------------------------------------------------------------

// reOwnerHint is W-4.5's hint: "ask the owner; a change goes through a draft
// and `hactl policy activate`" (backticks optional in the printed text).
var reOwnerHint = regexp.MustCompile("ask the owner; a change goes through a draft and `?hactl policy activate`?")

// TestPolicyAgentsSpecRefusalHintSendsToTheOwner — W-4.5: "The refusal hint
// changes from 'do not edit policy.yaml' to 'ask the owner; a change goes
// through a draft and `hactl policy activate`'." Checked on a policy_denied
// refusal (text and --json, D-118) and — STRICTER READING — on the
// policy_missing refusal's `hint:` line (D-136), the other refusal an agent's
// directory meets.
//
// NOTE: docs/manual.md (D-118, "Enforcement (writes)") still says the hint
// "always ends ` — report it; do not edit policy.yaml`"; the contract commit
// did not change that sentence. This test follows W-4.5.
func TestPolicyAgentsSpecRefusalHintSendsToTheOwner(t *testing.T) {
	home := setupHome(t)
	policy := "version: 1\nactive: pg_hint\nprofiles:\n  pg_hint:\n    deny:\n      \"area:*\": [create]\n"
	dir := setupInstanceDir(t, ha, policy, false)

	res := runSetupBin(t, home, dir, nil, "--dir", dir, "area", "create", "PG W5 Hint")
	if res.Code != 1 {
		t.Fatalf("area create under a create deny: %s", res)
	}
	lines := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
	hint := lines[len(lines)-1]
	if !strings.HasPrefix(hint, "  hint: ") || !reOwnerHint.MatchString(hint) || strings.Contains(hint, "do not edit policy.yaml") {
		t.Errorf("policy_denied hint = %q, want it to end in %q, not `do not edit policy.yaml`", hint, "ask the owner; a change goes through a draft and `hactl policy activate`")
	}
	j := runSetupBin(t, home, dir, nil, "--dir", dir, "area", "create", "PG W5 Hint", "--json")
	var doc struct {
		Hint string `json:"hint"`
	}
	if err := json.Unmarshal([]byte(j.Stdout), &doc); err != nil {
		t.Fatalf("--json refusal: %v\n%s", err, j)
	}
	if !reOwnerHint.MatchString(doc.Hint) || strings.Contains(doc.Hint, "do not edit policy.yaml") {
		t.Errorf("--json hint = %q, want the owner/draft/activate hint", doc.Hint)
	}

	t.Run("policy_missing (stricter reading)", func(t *testing.T) {
		req := setupInstanceDir(t, ha, "", true)
		m := runSetupBin(t, home, req, nil, "--dir", req, "ent", "ls")
		if m.Code != 1 {
			t.Fatalf("ent ls with HACTL_POLICY=required and no policy.yaml: %s", m)
		}
		hintLine := lineMatching(m.Stderr, `^\s*hint:`)
		if !reOwnerHint.MatchString(hintLine) {
			t.Errorf("policy_missing hint line = %q, want the owner/draft/activate hint", hintLine)
		}
	})
}
