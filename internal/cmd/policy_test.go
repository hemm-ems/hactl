package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"

	"github.com/hemm-ems/hactl/internal/mcpserver"
	"github.com/hemm-ems/hactl/internal/policy"
)

// policyFixtureInstance is a contract fixture (a stubbed Home Assistant with
// light.kitchen in area kitchen_id "Kitchen", label energy, and
// automation.morning whose config id is 1700000000000) with content written
// as its policy.yaml; content "" writes no file.
func policyFixtureInstance(t *testing.T, content string) string {
	t.Helper()
	f := buildContractFixture(t)
	if content != "" {
		if err := os.WriteFile(filepath.Join(f.dir, policy.FileName), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return f.dir
}

// runPolicy runs `hactl policy <args…> --dir dir` and returns stdout, the
// exit code main would use, and the error.
func runPolicy(t *testing.T, dir string, args ...string) (string, int, error) {
	t.Helper()
	full := append([]string{"hactl", "policy"}, args...)
	full = append(full, "--dir", dir, "--tokensmax", "0")
	var buf bytes.Buffer
	err := RunWithOutput(full, &buf)
	code := 0
	if err != nil {
		code = 1
		var ec interface{ ExitCode() int }
		if errors.As(err, &ec) {
			code = ec.ExitCode()
		}
	}
	return buf.String(), code, err
}

const policyAgentFile = `version: 1
active: agent
profiles:
  default:
    deny:
      "entity:lock.*": [write]
  agent:
    default: deny
    allow:
      "entity:*": [read]
      "entity:light.*": [control]
    deny:
      "entity:lock.*": [read, write]
    where:
      "entity:light.*": { area: Kitchen }
`

// D-58: no file, no change.
func TestPolicyNoFile(t *testing.T) {
	dir := policyFixtureInstance(t, "")

	out, code, err := runPolicy(t, dir, "show")
	if code != 0 || !strings.Contains(out, "no policy file") || !strings.Contains(out, policy.Disclaimer) || !strings.Contains(out, "not enforced yet") {
		t.Errorf("show: code=%d err=%v\n%s", code, err, out)
	}
	out, code, _ = runPolicy(t, dir, "show", "--json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("show --json: %v\n%s", err, out)
	}
	if code != 0 || doc["exists"] != false || doc["selected_by"] != "no-file" || doc["allow_all"] != true ||
		doc["default"] != "allow" || doc["enforced"] != false || doc["disclaimer"] != policy.Disclaimer || doc["level"] != "A" {
		t.Errorf("show --json no file = %v", doc)
	}

	out, code, _ = runPolicy(t, dir, "check", "entity:lock.front_door", "control")
	if code != 0 || !strings.Contains(out, "allowed: entity:lock.front_door control") || !strings.Contains(out, "rule: "+policy.RuleNoFile) {
		t.Errorf("check: code=%d\n%s", code, out)
	}

	out, code, _ = runPolicy(t, dir, "lint")
	if code != 0 {
		t.Errorf("lint with no file: code=%d, want 0\n%s", code, out)
	}
}

// D-60: a broken file is reported by show and lint, refused by check.
func TestPolicyBrokenFile(t *testing.T) {
	dir := policyFixtureInstance(t, "version: 1\nprofiles:\n  agent:\n    deny:\n      \"entity:lock.*\": [wirte]\n")
	file := filepath.Join(dir, policy.FileName)

	out, code, _ := runPolicy(t, dir, "show")
	if code != 1 || !strings.Contains(out, file+`:5: verbs for "entity:lock.*": unknown verb "wirte"`) {
		t.Errorf("show: code=%d, want 1 and the problem at file:line\n%s", code, out)
	}

	out, code, _ = runPolicy(t, dir, "show", "--json")
	var doc struct {
		Valid    bool   `json:"valid"`
		Profile  string `json:"profile"`
		AllowAll bool   `json:"allow_all"`
		Problems []struct {
			Line    int    `json:"line"`
			Message string `json:"message"`
		} `json:"problems"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("show --json: %v\n%s", err, out)
	}
	if code != 1 || doc.Valid || doc.AllowAll || doc.Profile != "" || len(doc.Problems) != 1 || doc.Problems[0].Line != 5 {
		t.Errorf("show --json broken: code=%d doc=%+v", code, doc)
	}

	out, code, err := runPolicy(t, dir, "check", "entity:lock.x", "control")
	if code != 1 || out != "" || err == nil || !strings.Contains(err.Error(), file+":5:") {
		t.Errorf("check on a broken file: code=%d stdout=%q err=%v — want a refusal (exit 1, empty stdout, file:line on stderr)", code, out, err)
	}

	out, code, _ = runPolicy(t, dir, "lint", "--json")
	var lint struct {
		Valid    bool `json:"valid"`
		Findings []struct {
			Code string `json:"code"`
			Line int    `json:"line"`
		} `json:"findings"`
	}
	if err := json.Unmarshal([]byte(out), &lint); err != nil {
		t.Fatalf("lint --json: %v\n%s", err, out)
	}
	if code != 1 || lint.Valid || len(lint.Findings) != 1 || lint.Findings[0].Code != policy.CodeBroken || lint.Findings[0].Line != 5 {
		t.Errorf("lint broken: code=%d %+v", code, lint)
	}

	// Lint's prose names each problem exactly as show does (D-60).
	out, code, _ = runPolicy(t, dir, "lint")
	if code != 1 || !strings.Contains(out, file+`:5: verbs for "entity:lock.*": unknown verb "wirte"`) {
		t.Errorf("lint prose: code=%d, want the problem as <file>:<line>: <message>\n%s", code, out)
	}
}

// The exit code is the verdict: 0 allowed, 1 denied with a verdict on stdout;
// a refusal is exit 1 with nothing on stdout.
func TestPolicyCheckVerdicts(t *testing.T) {
	dir := policyFixtureInstance(t, policyAgentFile)

	out, code, _ := runPolicy(t, dir, "check", "entity:lock.front_door", "control")
	if code != 1 || !strings.HasPrefix(out, "denied: entity:lock.front_door control\n") ||
		!strings.Contains(out, `rule: deny "entity:lock.*" [read write] (line 13)`) || !strings.Contains(out, "profile: agent (selected by active)") {
		t.Errorf("denied: code=%d\n%s", code, out)
	}

	out, code, _ = runPolicy(t, dir, "check", "entity:sensor.temp", "read", "--json")
	var doc struct {
		Rule       string   `json:"rule"`
		SelectedBy string   `json:"selected_by"`
		WhatIf     bool     `json:"what_if"`
		Allowed    bool     `json:"allowed"`
		Enforced   bool     `json:"enforced"`
		Line       int      `json:"line"`
		Level      string   `json:"level"`
		Notes      []string `json:"notes"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("check --json: %v\n%s", err, out)
	}
	if code != 0 || !doc.Allowed || doc.Rule != `allow "entity:*" [read]` || doc.Line != 10 || doc.SelectedBy != "active" ||
		doc.WhatIf || doc.Enforced || doc.Level != "A" || doc.Notes == nil {
		t.Errorf("allowed --json: code=%d %+v", code, doc)
	}

	// where: evaluated live against the registries — light.kitchen is in
	// area kitchen_id, named "Kitchen".
	out, code, _ = runPolicy(t, dir, "check", "entity:light.kitchen", "control")
	if code != 0 || !strings.Contains(out, `allow "entity:light.*" [control] where area=Kitchen`) {
		t.Errorf("where selector: code=%d\n%s", code, out)
	}

	for _, args := range [][]string{
		{"check", "entity:lock.*", "control"},
		{"check", "entity:lock.x", "write"},
		{"check", "entitiy:lock.x", "read"},
		{"check", "entity:lock.x", "read", "--as", "nope"},
	} {
		out, code, err := runPolicy(t, dir, args...)
		if code != 1 || out != "" || err == nil {
			t.Errorf("%v: code=%d stdout=%q err=%v — want a refusal", args, code, out, err)
		}
	}
}

// D-61: the implicit default is said loudly.
func TestPolicyShowAllowAllIsLoud(t *testing.T) {
	dir := policyFixtureInstance(t, "version: 1\nprofiles:\n  agent: {default: deny}\n")
	out, code, _ := runPolicy(t, dir, "show")
	if code != 0 || !strings.Contains(out, "ALLOW-ALL") || !strings.Contains(out, "agent") {
		t.Errorf("implicit: code=%d\n%s", code, out)
	}
	out, code, _ = runPolicy(t, dir, "lint", "--allow-partial")
	if code != 1 || !strings.Contains(out, policy.CodeImplicitAllowAll) {
		t.Errorf("lint implicit: code=%d\n%s", code, out)
	}
}

// --as means the same read-only what-if on both commands that offer it (H-25,
// flagcontract surface): it evaluates the named declared profile, says so,
// and leaves the effective profile as the file's active: says.
func TestPolicyAsIsTheSameWhatIfOnShowAndCheck(t *testing.T) {
	dir := policyFixtureInstance(t, policyAgentFile)

	out, code, _ := runPolicy(t, dir, "show", "--as", "default")
	if code != 0 || !strings.Contains(out, "what-if: --as default") || !strings.Contains(out, "effective profile is still agent") ||
		!strings.Contains(out, "profile: default (selected by what-if)") {
		t.Errorf("show --as: code=%d\n%s", code, out)
	}
	out, _, _ = runPolicy(t, dir, "show", "--as", "default", "--json")
	var show map[string]any
	if err := json.Unmarshal([]byte(out), &show); err != nil {
		t.Fatal(err)
	}
	if show["profile"] != "default" || show["selected_by"] != "what-if" || show["what_if"] != true {
		t.Errorf("show --as --json = %v", show)
	}

	// lock read: denied under agent (effective), allowed under default (what-if).
	out, code, _ = runPolicy(t, dir, "check", "entity:lock.x", "read", "--as", "default")
	if code != 0 || !strings.HasPrefix(out, "what-if: --as default") {
		t.Errorf("check --as default: code=%d\n%s", code, out)
	}
	out, code, _ = runPolicy(t, dir, "check", "entity:lock.x", "read")
	if code != 1 || strings.Contains(out, "what-if") {
		t.Errorf("check without --as after a what-if: code=%d — the effective profile must be unchanged\n%s", code, out)
	}

	// No file: any --as is refused on both.
	empty := policyFixtureInstance(t, "")
	for _, args := range [][]string{{"show", "--as", "agent"}, {"check", "entity:x.y", "read", "--as", "agent"}} {
		if out, code, _ := runPolicy(t, empty, args...); code != 1 || out != "" {
			t.Errorf("%v with no file: code=%d stdout=%q, want a refusal", args, code, out)
		}
	}
}

// D-62: the agent cannot choose its own profile at call time. No command in
// the tree declares a --profile flag (hactl mcp included), the environment
// does not move the profile, and a pinned MCP server cannot be re-pointed at
// another instance's policy per call (D-64).
func TestPolicyHasNoCallTimeProfileSelector(t *testing.T) {
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		for _, fs := range []*pflag.FlagSet{c.Flags(), c.PersistentFlags(), c.InheritedFlags()} {
			fs.VisitAll(func(f *pflag.Flag) {
				if strings.Contains(strings.ToLower(f.Name), "profile") {
					t.Errorf("%s declares --%s: only the file's active: selects the profile (D-62)", c.CommandPath(), f.Name)
				}
			})
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)

	dir := policyFixtureInstance(t, policyAgentFile)
	t.Setenv("HACTL_PROFILE", "default")
	out, code, _ := runPolicy(t, dir, "check", "entity:lock.x", "read", "--json")
	if code != 1 || !strings.Contains(out, `"profile": "agent"`) {
		t.Errorf("HACTL_PROFILE moved the profile: code=%d\n%s", code, out)
	}

	// D-64: nor can a call choose the instance, and with it the policy file,
	// on an MCP server pinned with --dir. Another instance here has no policy
	// at all, so re-pointing would answer "allowed: no policy file".
	other := policyFixtureInstance(t, "")
	call := pinnedMCP(t, dir)
	for _, cmd := range []string{
		"policy check entity:lock.x control --json --dir " + other,
		"policy check entity:lock.x control --json --dir=" + other,
	} {
		res, text := call(cmd)
		if !res.IsError || strings.Contains(text, "no policy file") || !strings.Contains(text, "pinned") {
			t.Errorf("pinned server answered %q from another instance:\n%s", cmd, text)
		}
	}
	res, text := call("policy check entity:lock.x control --json --dir " + dir)
	if !strings.Contains(text, `"profile": "agent"`) || !res.IsError {
		t.Errorf("--dir naming the pinned instance itself must run (denied, exit 1):\n%s", text)
	}
	t.Setenv("HACTL_DIR", other)
	if _, text := call("policy check entity:lock.x control --json"); !strings.Contains(text, `"profile": "agent"`) {
		t.Errorf("HACTL_DIR re-pointed a pinned server:\n%s", text)
	}
}

// pinnedMCP serves `hactl mcp --dir dir` in process and returns a tool caller
// with the result and its text.
func pinnedMCP(t *testing.T, dir string) func(command string) (*mcp.CallToolResult, string) {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	srv := mcpserver.NewServer(mcpserver.Options{
		Runner: RunWithOutputContext, ResolvePath: FindCommandPath, NoManualInject: true, Dir: dir,
	})
	srvSession, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srvSession.Wait() })
	session, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return func(command string) (*mcp.CallToolResult, string) {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hactl", Arguments: map[string]any{"command": command}})
		if err != nil {
			t.Fatalf("CallTool(%q): %v", command, err)
		}
		var sb strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				sb.WriteString(tc.Text)
			}
		}
		return res, sb.String()
	}
}

// Lint against the stubbed instance: a misspelt deny and an automation
// written with the id `auto ls` prints rather than its config id (D-1) are
// both no-match; unenumerated kinds are unchecked and fail without
// --allow-partial.
func TestPolicyLintAgainstTheInstance(t *testing.T) {
	dir := policyFixtureInstance(t, `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.*": [control]
      "automation:1700000000000": [edit]
    deny:
      "entity:lokc.*": [write]
      "automation:morning": [delete]
      "file:**": [write]
`)
	out, code, _ := runPolicy(t, dir, "lint", "--json")
	var doc struct {
		Valid    bool `json:"valid"`
		Findings []struct {
			Code string `json:"code"`
			Rule string `json:"rule"`
			Line int    `json:"line"`
		} `json:"findings"`
		Unchecked []struct {
			Kind   string `json:"kind"`
			Reason string `json:"reason"`
		} `json:"unchecked"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("lint --json: %v\n%s", err, out)
	}
	got := make([]string, 0, len(doc.Findings))
	for _, f := range doc.Findings {
		got = append(got, f.Code+"@"+f.Rule)
	}
	want := []string{
		`no-match@deny "entity:lokc.*" [write]`,
		`no-match@deny "automation:morning" [delete]`,
	}
	if code != 1 || !doc.Valid || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("lint: code=%d findings=%v, want %v\n%s", code, got, want, out)
	}
	if len(doc.Unchecked) != 1 || doc.Unchecked[0].Kind != "file" || doc.Unchecked[0].Reason != "not enumerated" {
		t.Errorf("unchecked = %+v, want file: not enumerated", doc.Unchecked)
	}

	// --allow-partial accepts unchecked kinds, never findings.
	clean := policyFixtureInstance(t, "version: 1\nprofiles:\n  default:\n    deny:\n      \"file:**\": [write]\n      \"entity:light.*\": [delete]\n")
	if out, code, _ := runPolicy(t, clean, "lint"); code != 1 || !strings.Contains(out, "unchecked: 1 ") {
		t.Errorf("unchecked without --allow-partial: code=%d\n%s", code, out)
	}
	if out, code, _ := runPolicy(t, clean, "lint", "--allow-partial"); code != 0 || !strings.Contains(out, "file: not enumerated") {
		t.Errorf("--allow-partial: code=%d\n%s", code, out)
	}
}
