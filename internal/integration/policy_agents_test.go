//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hemm-ems/hactl/internal/hatest"
	"github.com/hemm-ems/hactl/internal/instancelock"
)

// ============================================================================
// W-5 (SPEC-policy-usp.md, D-151, D-152, D-160, D-164) and W-4 (D-148, D-149,
// D-163), end to end against a real Home Assistant: the owner makes two agent
// directories with `hactl agent add`, activates a different draft in each with
// `hactl policy activate` at a (pseudo-)terminal, and two MCP servers — each
// pinned to one agent's directory — then get exactly their own verdicts and
// their own audit logs. A write lock is one per Home Assistant: while one is
// held, a confirmed write from either directory waits for it.
//
// Everything runs through the built binary, as the owner and the agents do:
// the terminal gate is a property of the process, not of a function.
// Read-backs go to Home Assistant directly (H-12).
// ============================================================================

const alphaDraft = `# alpha may switch the lock state helper, nothing else
version: 1
active: switcher
profiles:
  switcher:
    default: deny
    allow:
      "*": [read]
      "entity:input_boolean.pg_policy_lock_state": [control]
      "service:input_boolean.*": [control]
`

const betaDraft = `# beta may create labels, and switch nothing
version: 1
active: labeller
profiles:
  labeller:
    default: deny
    allow:
      "*": [read]
      "label:*": [create]
`

// buildHactl builds the binary once per test.
func buildHactl(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "hactl")
	c := exec.CommandContext(context.Background(), "go", "build", "-o", bin, "./cmd/hactl") //nolint:gosec // the repo's own main package
	c.Dir = filepath.Join("..", "..")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return bin
}

// hactlEnv is the agents' and owner's environment: an isolated HOME (so the
// user cache dir holding the write lock and ~/.hactl are the test's), the
// manual delivery off.
func hactlEnv(home string) []string {
	return []string{"HOME=" + home, "XDG_CACHE_HOME=" + filepath.Join(home, ".cache"), "HACTL_MANUAL_MODE=off", "PATH=" + os.Getenv("PATH")}
}

// underTerminal runs bin args under a pseudo-terminal, answering each prompt
// in answers (prompt substring → line), and returns everything it printed.
func underTerminal(t *testing.T, bin string, env []string, answers [][2]string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, bin, args...) //nolint:gosec // the test's own build
	c.Env = env
	ptmx, err := pty.Start(c)
	if err != nil {
		t.Fatalf("pty start: %v", err)
	}
	defer func() { _ = ptmx.Close() }()
	var buf bytes.Buffer
	done := make(chan struct{})
	go func() {
		defer close(done)
		r := bufio.NewReader(ptmx)
		next := 0
		for {
			b, rerr := r.ReadByte()
			if rerr != nil {
				return
			}
			buf.WriteByte(b)
			if next < len(answers) && strings.Contains(buf.String(), answers[next][0]) {
				_, _ = ptmx.WriteString(answers[next][1] + "\r")
				next++
			}
		}
	}()
	err = c.Wait()
	_ = ptmx.Close()
	<-done
	return buf.String(), err
}

// mcpSession starts `hactl mcp --dir dir --allow-writes` — an agent's MCP
// server, pinned to its directory — and connects to it.
func mcpSession(t *testing.T, bin string, env []string, dir string) *mcp.ClientSession {
	t.Helper()
	c := exec.CommandContext(context.Background(), bin, "mcp", "--dir", dir, "--allow-writes", "--no-manual-inject") //nolint:gosec // the test's own build
	c.Env = env
	client := mcp.NewClient(&mcp.Implementation{Name: "e2e", Version: "0"}, nil)
	s, err := client.Connect(context.Background(), &mcp.CommandTransport{Command: c}, nil)
	if err != nil {
		t.Fatalf("connecting to the MCP server for %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// mcpRun calls the hactl tool and returns its text and whether it is an error.
func mcpRun(t *testing.T, s *mcp.ClientSession, command string) (string, bool) {
	t.Helper()
	res, err := s.CallTool(context.Background(), &mcp.CallToolParams{Name: "hactl", Arguments: map[string]any{"command": command}})
	if err != nil {
		t.Fatalf("CallTool(%q): %v", command, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

type auditLine struct {
	Command  string `json:"command"`
	Resource string `json:"resource"`
	Verb     string `json:"verb"`
	Allowed  bool   `json:"allowed"`
	Profile  string `json:"profile"`
}

func readAudit(t *testing.T, dir string) []auditLine {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "cache", "policy-audit.log")) //nolint:gosec // the test's own agent dir
	if err != nil {
		t.Fatalf("reading %s's audit log: %v", dir, err)
	}
	out := make([]auditLine, 0, strings.Count(string(data), "\n"))
	for l := range strings.SplitSeq(strings.TrimSpace(string(data)), "\n") {
		var a auditLine
		if err := json.Unmarshal([]byte(l), &a); err != nil {
			t.Fatalf("audit line %q: %v", l, err)
		}
		out = append(out, a)
	}
	return out
}

// agentsRig is one Home Assistant, its owner's instance, and the agents'
// directories under an isolated HOME.
type agentsRig struct {
	inst  *hatest.Instance
	bin   string
	home  string
	env   []string
	owner string
	ws    *rawWS
	dirs  map[string]string
	mcp   map[string]*mcp.ClientSession
}

const (
	agentLockEntity = "input_boolean.pg_policy_lock_state"
	agentLabelName  = "pg agent beta label"
)

var agentProfiles = map[string]string{"alpha": "switcher", "beta": "labeller"}

func newAgentsRig(t *testing.T) *agentsRig {
	t.Helper()
	inst := getPolicyEnforceHA(t)
	home := t.TempDir()
	r := &agentsRig{
		inst: inst, bin: buildHactl(t), home: home, env: hactlEnv(home),
		owner: pgInstanceDir(t, inst.URL(), inst.Token(), ""),
		ws:    dialRawWS(t, inst.URL(), inst.Token()),
		dirs:  map[string]string{}, mcp: map[string]*mcp.ClientSession{},
	}
	t.Cleanup(func() {
		pgCallService(t, inst, "input_boolean", "turn_on", map[string]any{"entity_id": agentLockEntity})
		for id, l := range pgLabels(t, r.ws) {
			if l.Name == agentLabelName {
				r.ws.send(t, map[string]any{"type": "config/label_registry/delete", "label_id": id})
			}
		}
	})
	pgCallService(t, inst, "input_boolean", "turn_on", map[string]any{"entity_id": agentLockEntity})
	return r
}

// addAgents: the owner adds alpha and beta at a terminal (D-160, D-164).
func (r *agentsRig) addAgents(t *testing.T) {
	t.Helper()
	for _, name := range []string{"alpha", "beta"} {
		dir := filepath.Join(r.home, "agents", name)
		out, err := underTerminal(t, r.bin, r.env, nil, "agent", "add", name, "--from", r.owner, "--path", dir)
		if err != nil {
			t.Fatalf("agent add %s: %v\n%s", name, err, out)
		}
		// HOME is the test's, so the directory prints as ~/… (as the manual shows it).
		if !strings.Contains(out, `a long-lived token of its own, "hactl agent `+name+`"`) ||
			!strings.Contains(out, "claude mcp add "+name+" -- hactl mcp --dir ~/agents/"+name+" --allow-writes") {
			t.Errorf("agent add %s output:\n%s", name, out)
		}
		envText, err := os.ReadFile(filepath.Join(dir, ".env")) //nolint:gosec // the test's agent dir
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(envText), r.inst.Token()) || !strings.Contains(string(envText), "HACTL_POLICY=required") {
			t.Errorf("%s's .env carries the owner's token or no requirement:\n%s", name, envText)
		}
		r.dirs[name] = dir
	}
	// Home Assistant's own token list tells the two apart (M-6).
	var tokens []struct {
		ClientName string `json:"client_name"`
		Type       string `json:"type"`
	}
	if err := json.Unmarshal(r.ws.mustSucceed(t, map[string]any{"type": "auth/refresh_tokens"}).Result, &tokens); err != nil {
		t.Fatal(err)
	}
	listed := map[string]bool{}
	for _, tk := range tokens {
		if tk.Type == "long_lived_access_token" {
			listed[tk.ClientName] = true
		}
	}
	for _, name := range []string{"alpha", "beta"} {
		if !listed["hactl agent "+name] {
			t.Errorf("Home Assistant lists no long-lived token \"hactl agent %s\"", name)
		}
	}
	for name, dir := range r.dirs {
		r.mcp[name] = mcpSession(t, r.bin, r.env, dir)
	}
}

// activateDrafts: each agent's draft, refused over its MCP server, activated
// by the owner at a terminal (D-149, D-163).
func (r *agentsRig) activateDrafts(t *testing.T) {
	t.Helper()
	for name, draft := range map[string]string{"alpha": alphaDraft, "beta": betaDraft} {
		if err := os.WriteFile(filepath.Join(r.dirs[name], "policy.draft.yaml"), []byte(draft), 0o600); err != nil {
			t.Fatal(err)
		}
		// The agent checks its draft (the skill's step 5): clean on this
		// instance. Only alpha's: lint reports beta's kind-wide `label:*`
		// [create] as no-match on an instance with no labels yet — a lint
		// question outside this package, recorded rather than pinned here.
		if text, isErr := mcpRun(t, r.mcp[name], "policy lint --file policy.draft.yaml"); name == "alpha" && (isErr || !strings.Contains(text, "(draft, not in effect)") || !strings.Contains(text, "clean")) {
			t.Errorf("%s's draft does not lint clean: isError=%v\n%s", name, isErr, text)
		}
		text, isErr := mcpRun(t, r.mcp[name], "policy activate policy.draft.yaml")
		if !isErr || !strings.Contains(text, "ask the owner; a change goes through a draft and `hactl policy activate`") {
			t.Errorf("MCP activate for %s: isError=%v\n%s", name, isErr, text)
		}
		out, err := underTerminal(t, r.bin, r.env, [][2]string{{"anything else cancels: ", agentProfiles[name]}},
			"--dir", r.dirs[name], "policy", "activate", "policy.draft.yaml")
		if err != nil || !strings.Contains(out, "activated: ") || !strings.Contains(out, "replaces: "+filepath.Join(r.dirs[name], "policy.yaml")+" (profile readonly)") {
			t.Fatalf("activate %s: %v\n%s", name, err, out)
		}
		if got, _ := os.ReadFile(filepath.Join(r.dirs[name], "policy.yaml")); string(got) != draft {
			t.Errorf("%s's policy.yaml is not its draft:\n%s", name, got)
		}
	}
}

// mcpVerdict asserts one tool call's verdict on an agent's pinned server.
func (r *agentsRig) mcpVerdict(t *testing.T, name, command string, allowed bool) {
	t.Helper()
	text, isErr := mcpRun(t, r.mcp[name], command)
	switch {
	case allowed && isErr:
		t.Errorf("%s may %q, but it was refused:\n%s", name, command, text)
	case !allowed && (!isErr || !strings.Contains(text, "policy_denied") || !strings.Contains(text, "profile "+agentProfiles[name])):
		t.Errorf("%s may not %q: isError=%v\n%s", name, command, isErr, text)
	}
}

// assertOwnAuditLogs: each directory's log holds its own profile's checks.
func (r *agentsRig) assertOwnAuditLogs(t *testing.T) {
	t.Helper()
	for name, want := range map[string]struct{ allowedRes, deniedRes string }{
		"alpha": {"entity:" + agentLockEntity, "label:*"},
		"beta":  {"label:*", "entity:" + agentLockEntity},
	} {
		profile := agentProfiles[name]
		var sawAllowed, sawDenied bool
		for _, l := range readAudit(t, r.dirs[name]) {
			if l.Profile != profile && l.Profile != "readonly" {
				t.Errorf("%s's audit log holds a check under profile %q", name, l.Profile)
			}
			sawAllowed = sawAllowed || (l.Resource == want.allowedRes && l.Allowed && l.Profile == profile)
			sawDenied = sawDenied || (l.Resource == want.deniedRes && !l.Allowed && l.Profile == profile)
		}
		if !sawAllowed || !sawDenied {
			t.Errorf("%s's audit log: allowed %s=%v, denied %s=%v", name, want.allowedRes, sawAllowed, want.deniedRes, sawDenied)
		}
	}
}

func (r *agentsRig) labelLanded(t *testing.T) bool {
	t.Helper()
	for _, l := range pgLabels(t, r.ws) {
		if l.Name == agentLabelName {
			return true
		}
	}
	return false
}

type writeResult struct {
	name string
	out  []byte
	err  error
	took time.Duration
}

// assertOneLockPerHA (D-152): while another hactl holds this Home Assistant's
// lock, a confirmed write from either agent directory waits for it, and both
// land once it is released.
func (r *agentsRig) assertOneLockPerHA(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", r.home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(r.home, ".cache"))
	lockDir, err := instancelock.DirForURL(r.inst.URL())
	if err != nil {
		t.Fatal(err)
	}
	held, err := instancelock.Acquire(context.Background(), lockDir, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	results := make(chan writeResult, 2)
	start := time.Now()
	for name, args := range map[string][]string{
		"alpha": {"--dir", r.dirs["alpha"], "--timeout", "60s", "svc", "call", "input_boolean.turn_off", "--data", `{"entity_id":"` + agentLockEntity + `"}`, "--confirm"},
		"beta":  {"--dir", r.dirs["beta"], "--timeout", "60s", "label", "create", agentLabelName, "--confirm"},
	} {
		go func() {
			c := exec.CommandContext(context.Background(), r.bin, args...) //nolint:gosec // the test's own build
			c.Env = r.env
			out, runErr := c.CombinedOutput()
			results <- writeResult{name, out, runErr, time.Since(start)}
		}()
	}
	time.Sleep(3 * time.Second)
	if s := pgStateOf(t, r.inst, agentLockEntity); s.State != "on" {
		t.Errorf("alpha's switch landed while another hactl held the lock for this Home Assistant (state %s)", s.State)
	}
	if r.labelLanded(t) {
		t.Error("beta's label landed while another hactl held the lock for this Home Assistant")
	}
	early := drainFinished(t, results)
	held.Release()
	for range 2 - early {
		res := <-results
		if res.err != nil {
			t.Errorf("%s's confirmed write after the lock was released: %v\n%s", res.name, res.err, res.out)
		}
		if res.took < 3*time.Second {
			t.Errorf("%s finished after %s, before the lock was released", res.name, res.took)
		}
	}
	if s := pgStateOf(t, r.inst, agentLockEntity); s.State != "off" {
		t.Errorf("alpha's switch did not land: state %s", s.State)
	}
	if !r.labelLanded(t) {
		t.Error("beta's label did not land")
	}
	if _, statErr := os.Stat(filepath.Join(lockDir, instancelock.LockFileName)); statErr != nil {
		t.Errorf("no lock file in %s: %v", lockDir, statErr)
	}
}

// drainFinished counts (and fails on) the writes that finished while the lock
// was held, without waiting for the others.
func drainFinished(t *testing.T, results chan writeResult) int {
	t.Helper()
	n := 0
	for {
		select {
		case res := <-results:
			t.Errorf("%s finished while the lock was held (%v):\n%s", res.name, res.err, res.out)
			n++
		default:
			return n
		}
	}
}

func TestE2ETwoAgentDirsOnOneHA(t *testing.T) {
	r := newAgentsRig(t)
	r.addAgents(t)

	svc := `svc call input_boolean.turn_off --data '{"entity_id":"` + agentLockEntity + `"}'`
	label := `label create "` + agentLabelName + `"`
	// Read-only until activated (D-150).
	if text, isErr := mcpRun(t, r.mcp["alpha"], svc); !isErr || !strings.Contains(text, "policy_denied") {
		t.Errorf("the starter let alpha preview a switch: isError=%v\n%s", isErr, text)
	}

	r.activateDrafts(t)

	// Each pinned server, exactly its own verdicts.
	r.mcpVerdict(t, "alpha", svc, true)
	r.mcpVerdict(t, "beta", svc, false)
	r.mcpVerdict(t, "beta", label, true)
	r.mcpVerdict(t, "alpha", label, false)
	// A pinned server cannot be pointed at the other agent's directory (D-64).
	if text, isErr := mcpRun(t, r.mcp["alpha"], "--dir "+r.dirs["beta"]+" "+label); !isErr {
		t.Errorf("alpha's server ran a call re-pointed at beta's directory:\n%s", text)
	}
	r.assertOwnAuditLogs(t)

	lsCmd := exec.CommandContext(context.Background(), r.bin, "agent", "ls", "--root", filepath.Join(r.home, "agents"), "--dir", r.owner) //nolint:gosec // the test's own build
	lsCmd.Env = r.env
	lsOut, err := lsCmd.CombinedOutput()
	if err != nil || !strings.Contains(string(lsOut), "switcher") || !strings.Contains(string(lsOut), "labeller") {
		t.Errorf("agent ls: %v\n%s", err, lsOut)
	}

	r.assertOneLockPerHA(t)
}
