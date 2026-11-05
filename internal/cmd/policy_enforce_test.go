package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/manual"
	"github.com/hemm-ems/hactl/internal/mcpserver"
	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
	"github.com/hemm-ems/hactl/internal/policy/reqgate"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// Enforcement at the CLI (manual "Enforcement (writes)", H-29). label create is
// the reference command wired to the plan API; the request layer applies to
// every command.

const (
	policyDenySecretLabel = "version: 1\nactive: agent\nprofiles:\n  agent:\n    deny:\n      \"label:secret\": [create]\n"
	policyAllowEverything = "version: 1\nactive: agent\nprofiles:\n  agent: {}\n"
	policyImplicitAllow   = "version: 1\nprofiles:\n  agent:\n    default: deny\n"
)

func writePolicyFile(t *testing.T, dir, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, policy.FileName), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readAudit(t *testing.T, dir string) []map[string]any {
	t.Helper()
	f, err := os.Open(filepath.Clean(filepath.Join(dir, "cache", enforce.AuditFileName)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []map[string]any
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatalf("audit line: %v", err)
		}
		out = append(out, m)
	}
	return out
}

// TestLabelCreateRefusedByItsPlan: the reference command's plan — create on
// every label id, Home Assistant mints it (D-75) — is refused by a deny on one
// label id, in the preview and under --confirm alike, before anything is
// sent: the report on stdout, one stderr line, exit 1 (D-70); under --json the
// preview envelope with ok false (R-13).
func TestLabelCreateRefusedByItsPlan(t *testing.T) {
	ts := startCmdServer(t, map[string]any{"config/label_registry/create": map[string]any{"label_id": "garden", "name": "Garden"}}, nil)
	writePolicyFile(t, ts.dir, policyDenySecretLabel)

	out, err := runPolicyCLI(t, ts.dir, "label", "create", "Garden")
	want := "policy_denied: would create label — refused by policy, nothing was written\n" +
		"  denied: create label:* — deny \"label:secret\" [create] (line 6)\n" +
		"  checked: 1 resources, 1 denied\n" +
		"  profile: agent (selected by active)\n" +
		"  hint: the owner would change the rule on line 6 — ask the owner; a change goes through a draft and `hactl policy activate`\n"
	if out != want {
		t.Errorf("refused preview stdout\n got: %q\nwant: %q", out, want)
	}
	if cliExitCode(err) != 1 || policyErrorClass(err) != enforce.ClassDenied {
		t.Fatalf("exit %d class %q (%v), want 1 policy_denied", cliExitCode(err), policyErrorClass(err), err)
	}
	if err.Error() != `policy_denied: 1 of 1 resources denied (profile agent)` {
		t.Errorf("stderr line = %q", err.Error())
	}

	out, err = runPolicyCLI(t, ts.dir, "label", "create", "Garden", "--json")
	if cliExitCode(err) != 1 {
		t.Fatalf("--json preview exit %d", cliExitCode(err))
	}
	m := decodeObject(t, out)
	requireKeys(t, "refusal", m, "dry_run", "ok", "error", "action", "details", "hint")
	wantField(t, "refusal", m, "dry_run", true)
	wantField(t, "refusal", m, "ok", false)
	wantField(t, "refusal", m, "error", "policy_denied")
	wantField(t, "refusal", m, "action", "create label")
	pol, _ := m["details"].(map[string]any)["policy"].(map[string]any)
	requireKeys(t, "refusal.details.policy", pol, "denied", "checked", "profile", "selected_by", "level", "written")
	wantField(t, "details.policy", pol, "checked", 1)
	wantField(t, "details.policy", pol, "profile", "agent")
	wantField(t, "details.policy", pol, "selected_by", "active")
	wantField(t, "details.policy", pol, "level", "A")
	wantField(t, "details.policy", pol, "written", []any{})
	wantField(t, "details.policy", pol, "denied", []any{map[string]any{
		"resource": "label:*", "verb": "create", "rule": `deny "label:secret" [create]`, "line": float64(6),
		"partial": false, "kind_wide": true,
	}})
	if hint, _ := m["hint"].(string); strings.Contains(hint, "--confirm") || hint == "" {
		t.Errorf("hint %q", hint)
	}

	out, err = runPolicyCLI(t, ts.dir, "label", "create", "Garden", "--confirm", "--json")
	if cliExitCode(err) != 1 {
		t.Fatalf("--confirm exit %d (%v)\n%s", cliExitCode(err), err, out)
	}
	wantField(t, "confirm refusal", decodeObject(t, out), "dry_run", false)
	ts.mu.Lock()
	sent := ts.cmdCounts["config/label_registry/create"]
	ts.mu.Unlock()
	if sent != 0 {
		t.Errorf("a refused plan sent %d label creates", sent)
	}
	audit := readAudit(t, ts.dir)
	if len(audit) != 3 {
		t.Fatalf("audit lines = %d, want 3 (two previews, one confirm, all at the command layer): %v", len(audit), audit)
	}
	for i, l := range audit {
		if l["layer"] != "command" || l["allowed"] != false || l["command"] != "hactl label create Garden" || l["dry_run"] != (i < 2) {
			t.Errorf("audit line %d = %v", i, l)
		}
	}
}

// TestLabelCreateAllowedPlan: an allowed preview says what it checked (text
// and --json); the confirmed run writes, and the request layer agrees with the
// plan — both layers audited (D-71).
func TestLabelCreateAllowedPlan(t *testing.T) {
	ts := startCmdServer(t, map[string]any{"config/label_registry/create": map[string]any{"label_id": "garden", "name": "Garden"}}, nil)
	writePolicyFile(t, ts.dir, policyAllowEverything)

	out, err := runPolicyCLI(t, ts.dir, "label", "create", "Garden")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "  policy: 1 checked, allowed (profile agent)\n") {
		t.Errorf("allowed preview does not say what it checked:\n%s", out)
	}
	out, err = runPolicyCLI(t, ts.dir, "label", "create", "Garden", "--json")
	if err != nil {
		t.Fatal(err)
	}
	details, _ := decodeObject(t, out)["details"].(map[string]any)
	wantField(t, "preview details", details, "policy", map[string]any{"checked": float64(1), "profile": "agent"})

	if out, err = runPolicyCLI(t, ts.dir, "label", "create", "Garden", "--confirm"); err != nil {
		t.Fatalf("confirmed create: %v\n%s", err, out)
	}
	ts.mu.Lock()
	sent := ts.cmdCounts["config/label_registry/create"]
	ts.mu.Unlock()
	if sent != 1 {
		t.Errorf("label creates sent = %d, want 1", sent)
	}
	audit := readAudit(t, ts.dir)
	layers := make([]string, 0, len(audit))
	for _, l := range audit {
		layers = append(layers, fmt.Sprintf("%v/%v/%v", l["layer"], l["dry_run"], l["resource"]))
	}
	want := []string{"command/true/label:*", "command/true/label:*", "command/false/label:*", "request/false/label:*"}
	if !slices.Equal(layers, want) {
		t.Errorf("audit = %q, want %q", layers, want)
	}
}

// TestNoProfileIsByteIdentical (D-58): with no policy file, and with a file that
// puts no profile in effect, the reference command's preview is byte for byte
// the preview hactl printed before the policy existed, nothing is sent and no
// audit log appears.
func TestNoProfileIsByteIdentical(t *testing.T) {
	const before = "dry-run: would create label\n  name: Garden\nuse --confirm to apply\n"
	url, hits := countingHA(t)
	for name, body := range map[string]string{"no file": "", "implicit allow-all": policyImplicitAllow} {
		dir := policyInstance(t, url, body)
		out, err := runPolicyCLI(t, dir, "label", "create", "Garden")
		if err != nil || out != before {
			t.Errorf("%s: preview = %q, %v; want %q", name, out, err, before)
		}
		if _, statErr := os.Stat(filepath.Join(dir, "cache", enforce.AuditFileName)); !errors.Is(statErr, os.ErrNotExist) {
			t.Errorf("%s: an audit log was written", name)
		}
		if reqgate.Current() != nil || policySession != nil {
			t.Errorf("%s: a session outlived the invocation", name)
		}
	}
	if hits.Load() != 0 {
		t.Errorf("a preview without a profile contacted Home Assistant %d times", hits.Load())
	}
}

// TestBrokenPolicyRefusesEveryCommand (D-60): walks the live cobra tree with a
// broken policy.yaml — every runnable command outside the exempt list refuses
// with policy_broken before it sends anything; the exempt ones run. What is
// exempt is written here from the contract, never asked of policyExempt (the
// function under test): D-60's six (`policy show`, `policy lint`, `version`,
// `help`, `completion`, `rtfm`), every `completion <shell>`, the root, and a
// bare family group, which only prints its help (D-87) — `hactl log`, a group
// whose bare form reads the log, is not one.
func TestBrokenPolicyRefusesEveryCommand(t *testing.T) {
	url, hits := countingHA(t)
	dir := policyInstance(t, url, "version: 1\nactive: nobody\n")
	brokenPrefix := "policy_broken: " + filepath.Join(dir, policy.FileName) + ":"

	// Named by the contract, run first (the completion subtree is added by
	// cobra on the first run): each exempt one runs.
	if _, err := runPolicyCLI(t, dir, "completion", "bash"); err != nil {
		t.Errorf("completion bash on a broken file: %v", err)
	}
	if _, err := runPolicyCLI(t, dir, "ent"); err != nil {
		t.Errorf("the bare group `ent` on a broken file: %v", err)
	}
	out, err := runPolicyCLI(t, dir, "policy", "check", "entity:light.x", "control")
	if policyErrorClass(err) != enforce.ClassBroken || !strings.HasPrefix(err.Error(), brokenPrefix) || out != "" {
		t.Errorf("policy check on a broken file: %v, stdout %q — want the policy_broken refusal, not an answer", err, out)
	}

	var walk func(c *cobra.Command)
	var ran, completions, groups int
	walk = func(c *cobra.Command) {
		for _, sub := range c.Commands() {
			walk(sub)
		}
		// `hactl mcp` starts a server on stdin: TestMCPServerStartsOnABrokenPolicy.
		if !c.Runnable() || c.Hidden || c.CommandPath() == "hactl mcp" {
			return
		}
		ran++
		exempt := contractExemptOnABrokenPolicy(c)
		if exempt && strings.HasPrefix(c.CommandPath(), "hactl completion ") {
			completions++
		}
		if exempt && c.HasSubCommands() && c.HasParent() {
			groups++
		}
		assertBrokenPolicyRun(t, dir, c, exempt, hits.Load, brokenPrefix)
	}
	walk(rootCmd)
	if ran < 60 || completions == 0 || groups < 10 {
		t.Errorf("walked %d commands (%d completion shells, %d bare groups); the tree walk has stopped finding them", ran, completions, groups)
	}
}

// contractExemptOnABrokenPolicy is what still runs on a broken policy.yaml,
// written from D-60 and D-87 rather than asked of policyExempt: the six named
// commands, every `completion <shell>`, the root, and every bare family group
// — a command with subcommands, which only prints its help — except `hactl
// log`, whose bare form reads the log.
func contractExemptOnABrokenPolicy(c *cobra.Command) bool {
	path := c.CommandPath()
	switch {
	case slices.Contains([]string{"hactl policy show", "hactl policy lint", "hactl version", "hactl help", "hactl completion", "hactl rtfm"}, path),
		strings.HasPrefix(path, "hactl completion "), path == "hactl":
		return true
	case c.HasSubCommands() && path != "hactl log":
		return true
	}
	return false
}

// assertBrokenPolicyRun runs c on the broken policy in dir: an exempt command
// runs; any other refuses with policy_broken naming file:line before it
// contacts Home Assistant (hits counts its requests).
func assertBrokenPolicyRun(t *testing.T, dir string, c *cobra.Command, exempt bool, hits func() int64, brokenPrefix string) {
	t.Helper()
	path := c.CommandPath()
	args := validArgs(c)
	if args == nil {
		t.Errorf("%s: no positional count from 0 to 4 satisfies its Args", path)
		return
	}
	full := append(strings.Fields(strings.TrimPrefix(path, "hactl ")), args...)
	if path == "hactl" {
		full = nil
	}
	before := hits()
	_, err := runPolicyCLI(t, dir, full...)
	switch {
	case exempt && policyErrorClass(err) == enforce.ClassBroken:
		t.Errorf("%s: an exempt command was refused as policy_broken", path)
	case !exempt && policyErrorClass(err) != enforce.ClassBroken:
		t.Errorf("%s: not refused as policy_broken: %v", path, err)
	case !exempt && hits() != before:
		t.Errorf("%s: refused, but contacted Home Assistant first", path)
	case !exempt && !strings.HasPrefix(err.Error(), brokenPrefix):
		t.Errorf("%s: refusal %q does not name file:line", path, err)
	}
}

// TestMCPServerStartsOnABrokenPolicy (D-60 as refined, D-87): `hactl mcp`
// starts on a broken policy.yaml — it refuses each tool call instead (so
// `policy show` stays usable over MCP) — run through the CLI entry point with
// its stdin closed, it serves nothing and exits cleanly, never policy_broken.
func TestMCPServerStartsOnABrokenPolicy(t *testing.T) {
	url, _ := countingHA(t)
	dir := policyInstance(t, url, "version: 1\nactive: nobody\n")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	oldIn := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldIn; _ = r.Close() }()
	_, err = runPolicyCLI(t, dir, "mcp")
	if policyErrorClass(err) != "" || err != nil {
		t.Errorf("hactl mcp on a broken policy: %v, want the server to start and end with its input", err)
	}
}

// validArgs returns placeholder positionals cobra's Args validator accepts.
func validArgs(c *cobra.Command) []string {
	for n := range 5 {
		args := make([]string, n)
		for i := range args {
			args[i] = fmt.Sprintf("x%d", i)
		}
		if c.Args == nil || c.Args(c, args) == nil {
			return args
		}
	}
	return nil
}

// TestMCPRefusesEachCallOnABrokenPolicy (D-60, D-72): the server starts on a
// broken file and refuses each call outside the exempt list; it reads the file
// again on every call, so fixing it takes effect without a restart; and
// --allow-writes is asked first — a write on a read-only server gets that
// refusal, not the policy's.
func TestMCPRefusesEachCallOnABrokenPolicy(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/area_registry/list":    []any{map[string]any{"area_id": "hall", "name": "Hall"}},
		"config/label_registry/create": map[string]any{"label_id": "garden", "name": "Garden"},
	}, nil)
	writePolicyFile(t, ts.dir, "version: 1\nactive: nobody\n")
	call := mcpCaller(t, ts.dir, false)

	if text, isErr := call("area ls"); !isErr || !strings.Contains(text, "policy_broken:") {
		t.Errorf("area ls on a broken file: error %v\n%s", isErr, text)
	}
	if text, isErr := call("label create Garden"); !isErr || strings.Contains(text, "policy_broken") || !strings.Contains(text, "allow-writes") {
		t.Errorf("a write on a read-only server must be refused by --allow-writes first (D-72): error %v\n%s", isErr, text)
	}
	if text, _ := call("policy show"); !strings.Contains(text, "policy_broken: "+enforce.BrokenHint) {
		t.Errorf("policy show does not report the broken file over MCP:\n%s", text)
	}
	writePolicyFile(t, ts.dir, policyAllowEverything)
	if text, isErr := call("area ls"); isErr || !strings.Contains(text, "hall") {
		t.Errorf("after fixing the file the next call still refused: error %v\n%s", isErr, text)
	}
}

// TestMCPDecidesEachCallUnderTheFileAsItIsThen (D-72): a policy edited between
// two calls decides the second.
func TestMCPDecidesEachCallUnderTheFileAsItIsThen(t *testing.T) {
	ts := startCmdServer(t, map[string]any{"config/label_registry/create": map[string]any{"label_id": "garden", "name": "Garden"}}, nil)
	writePolicyFile(t, ts.dir, policyAllowEverything)
	call := mcpCaller(t, ts.dir, true)
	if text, isErr := call("label create Garden"); isErr || !strings.Contains(text, "allowed (profile agent)") {
		t.Fatalf("first call: error %v\n%s", isErr, text)
	}
	writePolicyFile(t, ts.dir, policyDenySecretLabel)
	if text, isErr := call("label create Garden"); !isErr || !strings.Contains(text, "policy_denied") {
		t.Errorf("the edited policy did not decide the next call: error %v\n%s", isErr, text)
	}
}

func mcpCaller(t *testing.T, dir string, allowWrites bool) func(string) (string, bool) {
	t.Helper()
	srv := mcpserver.NewServer(mcpserver.Options{
		Runner: RunWithOutputContext, ResolvePath: FindCommandPath, Dir: dir, NoManualInject: true, AllowWrites: allowWrites,
	})
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
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
	return func(command string) (string, bool) {
		t.Helper()
		res, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "hactl", Arguments: map[string]any{"command": command}})
		if err != nil {
			return err.Error(), true
		}
		var sb strings.Builder
		for _, c := range res.Content {
			if tc, ok := c.(*mcp.TextContent); ok {
				sb.WriteString(tc.Text)
			}
		}
		return sb.String(), res.IsError
	}
}

// TestRefusedPreviewRecordsNoWitness (R-11, D-80): the witness that authorises
// --confirm is recorded only for a preview that passed; a refused preview
// leaves none, so the confirm guard refuses a --confirm after it.
func TestRefusedPreviewRecordsNoWitness(t *testing.T) {
	ts := startCmdServer(t, nil, nil)
	cacheDir := filepath.Join(ts.dir, "cache")
	for _, tc := range []struct {
		body    string
		witness bool
	}{{policyDenySecretLabel, false}, {policyAllowEverything, true}} {
		_ = os.Remove(filepath.Join(cacheDir, witnessFileName))
		writePolicyFile(t, ts.dir, tc.body)
		_ = runExecute(t, "label", "create", "Garden", "--dir", ts.dir)
		got := hasWitness(cacheDir, manual.SessionKey(), "hactl label create", []string{"Garden"}, time.Now())
		if got != tc.witness {
			t.Errorf("policy %q: witness recorded = %v, want %v", tc.body, got, tc.witness)
		}
	}
}

// runExecute runs the CLI entry point (Execute) with args, discarding stdout
// and stderr, and returns its error.
func runExecute(t *testing.T, args ...string) error {
	t.Helper()
	oldOut, oldErr := os.Stdout, os.Stderr
	devnull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout, os.Stderr = devnull, devnull
	rootCmd.SetArgs(args)
	defer func() {
		os.Stdout, os.Stderr = oldOut, oldErr
		_ = devnull.Close()
		rootCmd.SetArgs(nil)
		flagDir = ""
		flagJSON = false
		resetSubcommandFlags()
	}()
	return Execute()
}

// TestRequestLayerRefusalEndsTheInvocation (H-29): a request the request layer
// refused fails the invocation even when the command carried on past it — a
// refusal is an error, never a log line.
func TestRequestLayerRefusalEndsTheInvocation(t *testing.T) {
	pol, err := policy.Load(policyInstance(t, closedHAURL(t), policyDenySecretLabel))
	if err != nil {
		t.Fatal(err)
	}
	sess, err := enforce.New(enforce.Options{Policy: pol, InstanceDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	refusal := sess.Check(context.Background(), reqmap.Request{Transport: reqmap.WS, Type: "config/label_registry/create", Params: map[string]any{"name": "x"}})
	if refusal == nil {
		t.Fatal("the denied request was allowed")
	}
	policySession = sess
	if got := finishPolicySession(nil); !errors.Is(got, refusal) {
		t.Errorf("a swallowed request-layer refusal ended the invocation with %v", got)
	}
	if policySession != nil {
		t.Error("the session outlived the invocation")
	}
	if got := finishPolicySession(nil); got != nil {
		t.Errorf("no session: %v", got)
	}
}

// TestCacheClearKeepsTheAuditLog (D-71): `cache clear` removes caches — the
// stable-id registry beside the log goes — never the owner's audit log or its
// previous generation, whose bytes stay as they were.
func TestCacheClearKeepsTheAuditLog(t *testing.T) {
	ts := startCmdServer(t, nil, nil)
	cache := filepath.Join(ts.dir, "cache")
	if err := os.MkdirAll(cache, 0o750); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		enforce.AuditFileName:        "{\"layer\":\"command\"}\n",
		enforce.AuditFileName + ".1": "{\"layer\":\"request\"}\n",
		"ids.json":                   "{}",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(cache, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runPolicyCLI(t, ts.dir, "cache", "clear")
	if err != nil || out != "cache cleared\n" {
		t.Fatalf("cache clear: %q, %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(cache, "ids.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("cache clear left ids.json (%v): the control the audit log is compared with", err)
	}
	for _, name := range []string{enforce.AuditFileName, enforce.AuditFileName + ".1"} {
		got, err := os.ReadFile(filepath.Clean(filepath.Join(cache, name)))
		if err != nil || string(got) != files[name] {
			t.Errorf("cache clear changed %s: %q, %v", name, got, err)
		}
	}
}

// --- R-12: every confirmed request is in the plan -------------------------------------

// confirmedCoverageCase is one --confirm command run under an allow-all
// profile against a fake Home Assistant. Each family registers its commands
// here as it wires their plans; the test asserts the request layer's
// (resource, verb) verdicts are a subset of the command's plan (R-12, D-80).
type confirmedCoverageCase struct {
	name string
	args []string // after `hactl`, --confirm included
	ws   map[string]any
	http map[string]http.HandlerFunc
	// setup, when set, prepares the rig before the run (a companion .env, an
	// input file) and returns the args to run (it gets its own copy).
	setup func(t *testing.T, ts *cmdTestServer, args []string) []string
	// start, when set, builds the instance instead of startCmdServer(ws,
	// http) and returns its directory and the full args to run (a family's
	// own rig: a companion beside Home Assistant, files to pass with -f).
	// setup is not used with it.
	start func(t *testing.T) (dir string, args []string)
}

var confirmedCoverageCases = []confirmedCoverageCase{
	{
		name: "label create",
		args: []string{"label", "create", "Garden", "--confirm"},
		ws:   map[string]any{"config/label_registry/create": map[string]any{"label_id": "garden", "name": "Garden"}},
	},
}

// coverageRecorder collects a run's plan and request-layer checks.
type coverageRecorder struct {
	plan, requests []reqmap.Check
}

func (r *coverageRecorder) PlanChecked(cs []reqmap.Check) { r.plan = append(r.plan, cs...) }

func (r *coverageRecorder) RequestChecked(_ reqmap.Request, cs []reqmap.Check) {
	r.requests = append(r.requests, cs...)
}

// uncoveredChecks returns the request checks no plan check covers: the same
// check, or a kind-wide check of the same verb, kind and sub (which covers
// every id of the kind).
func uncoveredChecks(plan, requests []reqmap.Check) []string {
	var out []string
	for _, rc := range requests {
		covered := slices.ContainsFunc(plan, func(pc reqmap.Check) bool {
			if pc.String() == rc.String() {
				return true
			}
			return pc.KindWide && pc.Verb == rc.Verb && pc.Resource.Kind == rc.Resource.Kind && pc.Resource.Sub == rc.Resource.Sub
		})
		if !covered {
			out = append(out, rc.String())
		}
	}
	return out
}

// TestEveryConfirmedRequestIsInThePlan (R-12): a plan one resource short is
// caught here, which a deny-everything profile cannot do.
func TestEveryConfirmedRequestIsInThePlan(t *testing.T) {
	for _, tc := range confirmedCoverageCases {
		t.Run(tc.name, func(t *testing.T) {
			var dir string
			args := slices.Clone(tc.args)
			if tc.start != nil {
				dir, args = tc.start(t)
			} else {
				ts := startCmdServer(t, tc.ws, tc.http)
				dir = ts.dir
				if tc.setup != nil {
					args = tc.setup(t, ts, args)
				}
			}
			writePolicyFile(t, dir, policyAllowEverything)
			rec := &coverageRecorder{}
			policyRecorder = rec
			defer func() { policyRecorder = nil }()
			out, err := runPolicyCLI(t, dir, args...)
			if err != nil {
				t.Fatalf("%v\n%s", err, out)
			}
			if len(rec.plan) == 0 || len(rec.requests) == 0 {
				t.Fatalf("vacuous: plan %d checks, requests %d checks — the command declared no plan or sent no write", len(rec.plan), len(rec.requests))
			}
			if missing := uncoveredChecks(rec.plan, rec.requests); len(missing) > 0 {
				t.Errorf("the confirmed run sent writes its plan does not hold: %q (plan %v)", missing, rec.plan)
			}
		})
	}
}

// TestUncoveredChecksCatchesAPlanOneResourceShort: the watched-to-fail half of
// R-12 — drop one resource from a plan, and the comparison names it.
func TestUncoveredChecksCatchesAPlanOneResourceShort(t *testing.T) {
	mk := func(v policy.Verb, s string) reqmap.Check {
		r, err := policy.ParseResource(s)
		if err != nil {
			t.Fatal(err)
		}
		return reqmap.Check{Verb: v, Resource: r}
	}
	requests := []reqmap.Check{mk(policy.Delete, "automation:heating"), mk(policy.Delete, "entity:automation.heating")}
	full := slices.Clone(requests)
	if got := uncoveredChecks(full, requests); len(got) != 0 {
		t.Errorf("a full plan reported %q", got)
	}
	if got := uncoveredChecks(full[:1], requests); !slices.Equal(got, []string{"delete entity:automation.heating"}) {
		t.Errorf("a plan one resource short: %q", got)
	}
	wide := []reqmap.Check{{Verb: policy.Delete, Resource: policy.Resource{Kind: policy.KindEntity}, KindWide: true}, full[0]}
	if got := uncoveredChecks(wide, requests); len(got) != 0 {
		t.Errorf("a kind-wide plan check does not cover its kind: %q", got)
	}
}

// TestWritePlanRefusesAnAddressTheGrammarRefuses (D-69): a plan resource hactl
// cannot address is denied as unmappable, never canonicalised — and the rest
// of the plan is still decided and reported with it.
func TestWritePlanRefusesAnAddressTheGrammarRefuses(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyAllowEverything)
	pol, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := enforce.New(enforce.Options{Policy: pol, InstanceDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	policySession = sess
	defer func() { policySession = nil }()
	var out strings.Builder
	plan := newWritePlan("delete automation").
		add(policy.Delete, policy.KindAutomation, "heating", "").
		add(policy.Edit, policy.KindEntity, "light. x", "registry/labels")
	note, err := checkWritePlan(&out, true, plan)
	var ref *enforce.DeniedError
	if note != nil || !errors.As(err, &ref) {
		t.Fatalf("checkWritePlan = %v, %v; want a refusal", note, err)
	}
	if ref.Checked != 2 || len(ref.Denied) != 1 || ref.Denied[0].Rule != "fail-closed: unmappable resource entity:light. x/registry/labels" {
		t.Errorf("refusal = %+v", ref)
	}
	if !strings.Contains(out.String(), "checked: 2 resources, 1 denied") {
		t.Errorf("report:\n%s", out.String())
	}

	note, err = checkWritePlan(&out, true, newWritePlan("delete automation").add(policy.Delete, policy.KindAutomation, "heating", ""))
	if err != nil || note == nil || note.String() != "1 checked, allowed (profile agent)" {
		t.Errorf("an addressable plan: %v, %v", note, err)
	}
}

// TestAddServiceCallPlansTheReach (D-59, D-73): the svc family's plan helper
// asks Home Assistant the call's reach through the session — the same
// question the request layer asks — and a deny on an entity in it refuses the
// plan; with no profile in effect it asks nothing and adds nothing (D-58).
func TestAddServiceCallPlansTheReach(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"extract_from_target": map[string]any{"referenced_entities": []string{"light.a", "lock.door"}},
	}, map[string]http.HandlerFunc{
		"/api/states": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = fmt.Fprint(w, `[{"entity_id":"light.a"},{"entity_id":"lock.door"}]`)
		},
	})
	plan := newWritePlan("call service").addServiceCall(context.Background(), "light", "turn_on", map[string]any{"area_id": "hall"})
	if len(plan.checks) != 0 || len(plan.failClosed) != 0 {
		t.Fatalf("no session, yet the plan holds %v %v", plan.checks, plan.failClosed)
	}
	ts.mu.Lock()
	asked := ts.cmdCounts["extract_from_target"]
	ts.mu.Unlock()
	if asked != 0 {
		t.Fatalf("no session, yet Home Assistant was asked the reach")
	}

	writePolicyFile(t, ts.dir, "version: 1\nactive: a\nprofiles:\n  a:\n    deny:\n      \"entity:light.a\": [control]\n")
	pol, err := policy.Load(ts.dir)
	if err != nil {
		t.Fatal(err)
	}
	ha := enforce.NewHAFetcher(context.Background(), ts.srv.URL, "test-token", nil)
	sess, err := enforce.New(enforce.Options{Policy: pol, InstanceDir: ts.dir, Fetcher: ha})
	if err != nil {
		t.Fatal(err)
	}
	policySession = sess
	defer func() { policySession = nil; sess.Close() }()
	plan = newWritePlan("call service").addServiceCall(context.Background(), "light", "turn_on", map[string]any{"area_id": "hall"})
	got := reqmap.Result{Checks: plan.checks}.Strings()
	// The rig answers no entity registry: which reached entity is a template
	// cannot be read, so the typed check is kind-wide, denies only (D-107).
	if !slices.Equal(got, []string{"control entity:light.a", "control service:light.turn_on", "control template:* (deny only)"}) {
		t.Errorf("plan = %q (an entity service: the lock outside its domain is not reached)", got)
	}
	var out strings.Builder
	_, err = checkWritePlan(&out, true, plan)
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) || ref.Denied[0].Resource != "entity:light.a" {
		t.Errorf("a deny on an entity in the reach did not refuse the plan: %v", err)
	}
}
