//go:build integration

package integration

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"

	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// Harness for the W-4/W-5 blind spec tests (policy_setup_spec_test.go,
// policy_agents_spec_test.go). Written from the contract only — docs/manual.md
// "Setting up an agent: draft, review, activate" and "Several agents",
// docs/skills/policy-setup.md, docs/decisions.md D-148..D-152,
// SPEC-policy-usp.md W-4/W-5 and M-4..M-6 — before any of it existed.
//
// These tests need a real process, not the in-process RunWithOutput: whether
// stdin and stdout are a terminal is the contract of `policy activate` and
// `agent add` (D-149), an MCP server is a process, and `~/.hactl/agents` and
// "the user's cache directory" (D-152) are only the test's own when HOME is.
// So the binary is built once from source, and every process gets an
// isolated HOME.
// ============================================================================

var (
	setupBinOnce sync.Once
	setupBin     string
	setupBinDir  string
	errSetupBin  error
)

// hactlSetupBin builds the hactl binary once per package run.
func hactlSetupBin(t *testing.T) string {
	t.Helper()
	setupBinOnce.Do(func() {
		// Not t.TempDir: the binary outlives the first test; TestMain removes it.
		dir, err := os.MkdirTemp("", "hactl-setup-spec-bin-") //nolint:usetesting // shared across tests, removed in TestMain
		if err != nil {
			errSetupBin = err
			return
		}
		setupBinDir = dir
		bin := filepath.Join(dir, "hactl")
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "github.com/hemm-ems/hactl/cmd/hactl") //nolint:gosec // fixed go CLI, test-owned path
		if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
			errSetupBin = fmt.Errorf("go build: %w\n%s", buildErr, out)
			return
		}
		setupBin = bin
	})
	if errSetupBin != nil {
		t.Fatalf("building hactl: %v", errSetupBin)
	}
	return setupBin
}

// setupHome is an isolated HOME for one test: `~/.hactl/agents` and the
// user's cache directory (os.UserCacheDir: $HOME/Library/Caches on macOS,
// $XDG_CACHE_HOME on Linux, which setupEnv points into it) live inside it.
func setupHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	// macOS hands out /var/folders/… whose real path is /private/var/…; every
	// comparison below is between real paths.
	resolved, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// setupEnv is the environment of every process these tests start: the
// parent's, with HOME (and the XDG directories) moved into home, no HACTL_DIR,
// and HACTL_MANUAL_MODE=off so the confirm guard stays out of command
// mechanics (as runPolicyE2E does in the companion tier).
func setupEnv(home string, extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		switch k {
		case "HOME", "XDG_CACHE_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME",
			"HACTL_DIR", "HACTL_MANUAL_MODE", "HACTL_SESSION":
			continue
		}
		env = append(env, kv)
	}
	env = append(env,
		"HOME="+home,
		"XDG_CACHE_HOME="+filepath.Join(home, ".cache"),
		"XDG_CONFIG_HOME="+filepath.Join(home, ".config"),
		"HACTL_MANUAL_MODE=off",
	)
	return append(env, extra...)
}

// procResult is one finished process: both streams and the exit code.
type procResult struct {
	Stdout, Stderr string
	Code           int
}

func (r procResult) String() string {
	return fmt.Sprintf("exit %d\nstdout:\n%s\nstderr:\n%s", r.Code, r.Stdout, r.Stderr)
}

// runSetupBin runs the binary with no terminal anywhere: stdin is stdinData
// through a pipe (or /dev/null when nil), stdout and stderr are pipes. That is
// exactly the situation of an agent's shell tool and of Claude Code's `!`
// prefix (M-5: `test -t 0` and `test -t 1` both false there).
func runSetupBin(t *testing.T, home, cwd string, stdinData *string, args ...string) procResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, hactlSetupBin(t), args...) //nolint:gosec // binary built from source above
	cmd.Dir = cwd
	cmd.Env = setupEnv(home)
	if stdinData != nil {
		cmd.Stdin = strings.NewReader(*stdinData)
	}
	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	err := cmd.Run()
	res := procResult{Stdout: out.String(), Stderr: errOut.String()}
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		res.Code = ee.ExitCode()
	default:
		t.Fatalf("running hactl %v: %v", args, err)
	}
	return res
}

// execBin is the binary as an unstarted command with the isolated
// environment, for a caller that wires the streams itself.
func execBin(t *testing.T, home, cwd string, args ...string) *exec.Cmd {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), hactlSetupBin(t), args...) //nolint:gosec // binary built from source above
	cmd.Dir = cwd
	cmd.Env = setupEnv(home)
	return cmd
}

// ---------------------------------------------------------------------------
// A pseudo-terminal session: the owner at a terminal of their own.
// ---------------------------------------------------------------------------

type ptySession struct {
	cmd        *exec.Cmd
	master     *os.File
	mu         sync.Mutex
	buf        bytes.Buffer
	readerDone chan struct{}
	exited     chan struct{}
	waitErr    error
}

type ptyWriter struct{ s *ptySession }

func (w ptyWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	defer w.s.mu.Unlock()
	return w.s.buf.Write(p)
}

// startPTY runs the binary with stdin, stdout and stderr on one pty, as its
// controlling terminal — what a real terminal gives a command.
func startPTY(t *testing.T, home, cwd string, args ...string) *ptySession {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), hactlSetupBin(t), args...) //nolint:gosec // binary built from source above
	cmd.Dir = cwd
	cmd.Env = setupEnv(home, "TERM=xterm")
	master, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 60, Cols: 220})
	if err != nil {
		t.Fatalf("starting hactl %v on a pty: %v", args, err)
	}
	s := &ptySession{cmd: cmd, master: master, readerDone: make(chan struct{}), exited: make(chan struct{})}
	go func() {
		_, _ = io.Copy(ptyWriter{s}, master)
		close(s.readerDone)
	}()
	go func() {
		s.waitErr = cmd.Wait()
		close(s.exited)
	}()
	t.Cleanup(func() {
		select {
		case <-s.exited:
		default:
			_ = cmd.Process.Kill()
			<-s.exited
		}
		_ = master.Close()
	})
	return s
}

var reANSI = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b[()][A-Za-z0-9]|\x1b[=>]`)

// transcript is everything the session printed so far (and the echo of what
// was typed), without colour codes and carriage returns.
func (s *ptySession) transcript() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return strings.ReplaceAll(reANSI.ReplaceAllString(s.buf.String(), ""), "\r", "")
}

func (s *ptySession) size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Len()
}

func (s *ptySession) hasExited() bool {
	select {
	case <-s.exited:
		return true
	default:
		return false
	}
}

// waitQuiet returns once the session has printed something and then nothing
// for quiet — a prompt waiting for the owner — or has exited, or max elapsed
// (reported as false).
func (s *ptySession) waitQuiet(quiet, maxWait time.Duration) bool {
	deadline := time.Now().Add(maxWait)
	last, lastChange := -1, time.Now()
	for time.Now().Before(deadline) {
		if s.hasExited() {
			return true
		}
		n := s.size()
		if n != last {
			last, lastChange = n, time.Now()
		} else if n > 0 && time.Since(lastChange) >= quiet {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// typeLine types text and Enter, as the owner would.
func (s *ptySession) typeLine(t *testing.T, text string) {
	t.Helper()
	if _, err := s.master.Write([]byte(text + "\r")); err != nil {
		t.Fatalf("typing %q: %v", text, err)
	}
}

// wait waits for the process to exit and returns its exit code; ok is false
// when it did not exit within maxWait.
func (s *ptySession) wait(maxWait time.Duration) (code int, ok bool) {
	select {
	case <-s.exited:
	case <-time.After(maxWait):
		return -1, false
	}
	select { // drain what the process printed last
	case <-s.readerDone:
	case <-time.After(2 * time.Second):
	}
	if s.cmd.ProcessState == nil {
		return -1, true
	}
	return s.cmd.ProcessState.ExitCode(), true
}

// runPTYNoInput runs a command on a pty that is expected to finish without
// asking anything.
func runPTYNoInput(t *testing.T, home, cwd string, args ...string) (string, int) {
	t.Helper()
	s := startPTY(t, home, cwd, args...)
	code, ok := s.wait(90 * time.Second)
	if !ok {
		t.Fatalf("hactl %v on a terminal did not finish within 90s — is it waiting for input the contract does not ask for?\n%s", args, s.transcript())
	}
	return s.transcript(), code
}

// ---------------------------------------------------------------------------
// An MCP client: `hactl mcp --dir <dir> --allow-writes` over stdio.
// ---------------------------------------------------------------------------

type mcpClient struct {
	cmd    *exec.Cmd
	in     io.WriteCloser
	lines  chan string
	stderr *bytes.Buffer
	nextID int
}

// startMCPServer starts a server pinned to dir with writes allowed, completes
// the handshake, and spends the first tool call (the one the manual is
// injected into) on `version`, so the calls a test makes carry only their own
// output.
func startMCPServer(t *testing.T, home, dir string) *mcpClient {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), hactlSetupBin(t), "mcp", "--dir", dir, "--allow-writes") //nolint:gosec // binary built from source above
	cmd.Dir = dir
	cmd.Env = setupEnv(home)
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	c := &mcpClient{cmd: cmd, in: in, lines: make(chan string, 64), stderr: &bytes.Buffer{}}
	cmd.Stderr = c.stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting hactl mcp: %v", err)
	}
	go func() {
		br := bufio.NewReaderSize(out, 1<<20)
		for {
			line, readErr := br.ReadString('\n')
			if strings.TrimSpace(line) != "" {
				c.lines <- strings.TrimRight(line, "\n")
			}
			if readErr != nil {
				close(c.lines)
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = c.in.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})

	c.request(t, "initialize", map[string]any{
		"protocolVersion": "2025-06-18", "capabilities": map[string]any{},
		"clientInfo": map[string]any{"name": "policy-setup-spec", "version": "0"},
	})
	c.notify(t, "notifications/initialized")
	c.call(t, "version")
	return c
}

func (c *mcpClient) notify(t *testing.T, method string) {
	t.Helper()
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": method})
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		t.Fatalf("writing %s: %v", method, err)
	}
}

func (c *mcpClient) request(t *testing.T, method string, params any) map[string]any {
	t.Helper()
	c.nextID++
	id := c.nextID
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		t.Fatalf("writing %s: %v", method, err)
	}
	timeout := time.After(2 * time.Minute)
	for {
		select {
		case line, ok := <-c.lines:
			if !ok {
				t.Fatalf("hactl mcp closed stdout before answering %s\nstderr:\n%s", method, c.stderr.String())
			}
			var msg map[string]any
			if err := json.Unmarshal([]byte(line), &msg); err != nil {
				t.Fatalf("hactl mcp wrote a non-JSON line: %.300q", line)
			}
			if msg["id"] != float64(id) {
				continue
			}
			if e, has := msg["error"]; has {
				t.Fatalf("%s answered a JSON-RPC error: %v", method, e)
			}
			res, _ := msg["result"].(map[string]any)
			return res
		case <-timeout:
			t.Fatalf("hactl mcp did not answer %s within 2m\nstderr:\n%s", method, c.stderr.String())
		}
	}
}

// call runs one command line through the server's single `hactl` tool and
// returns the result's text and whether it is an error result.
func (c *mcpClient) call(t *testing.T, command string) (string, bool) {
	t.Helper()
	res := c.request(t, "tools/call", map[string]any{"name": "hactl", "arguments": map[string]any{"command": command}})
	var sb strings.Builder
	content, _ := res["content"].([]any)
	for _, item := range content {
		if block, ok := item.(map[string]any); ok {
			if text, ok := block["text"].(string); ok {
				sb.WriteString(text)
			}
		}
	}
	isErr, _ := res["isError"].(bool)
	return sb.String(), isErr
}

// ---------------------------------------------------------------------------
// Instance directories, files and Home Assistant read-backs.
// ---------------------------------------------------------------------------

// setupInstanceDir is an instance directory for inst, holding policy as
// policy.yaml (none when ""), with HACTL_POLICY=required when required, and
// extraEnv lines appended to .env. Its path is a real path.
func setupInstanceDir(t *testing.T, inst *hatest.Instance, policy string, required bool, extraEnv ...string) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	var env strings.Builder
	env.WriteString("HA_URL=" + inst.URL() + "\nHA_TOKEN=" + inst.Token() + "\n")
	if required {
		env.WriteString("HACTL_POLICY=required\n")
	}
	for _, l := range extraEnv {
		env.WriteString(l + "\n")
	}
	writeFileT(t, filepath.Join(dir, ".env"), env.String())
	if policy != "" {
		writeFileT(t, filepath.Join(dir, "policy.yaml"), policy)
	}
	return dir
}

func writeFileT(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFileT(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

func fileExists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// treeSnapshot maps every regular file under root (relative path) to the
// sha256 of its content. skip, when non-nil, leaves a relative path out.
func treeSnapshot(t *testing.T, root string, skip func(rel string) bool) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if skip != nil && rel != "." && skip(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type().IsRegular() {
			b, readErr := os.ReadFile(filepath.Clean(path)) //nolint:gosec // G122: a test-owned temporary tree
			if readErr != nil {
				return readErr
			}
			sum := sha256.Sum256(b)
			out[rel] = hex.EncodeToString(sum[:])
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return out
}

// diffSnapshots names every path added, removed or changed between a and b.
func diffSnapshots(a, b map[string]string) []string {
	var diff []string
	for p, h := range a {
		switch hb, ok := b[p]; {
		case !ok:
			diff = append(diff, "removed "+p)
		case hb != h:
			diff = append(diff, "changed "+p)
		}
	}
	for p := range b {
		if _, ok := a[p]; !ok {
			diff = append(diff, "added "+p)
		}
	}
	sort.Strings(diff)
	return diff
}

// skipCacheButAudit leaves out hactl's own cache directory (id caches, manual
// state) except the audit log, which a read must not grow either.
func skipCacheButAudit(rel string) bool {
	return strings.HasPrefix(rel, "cache"+string(filepath.Separator)) && rel != filepath.Join("cache", "policy-audit.log")
}

// skipCache leaves out hactl's own cache directory entirely — for a test
// whose own dry runs legitimately append audit lines.
func skipCache(rel string) bool {
	return rel == "cache" || strings.HasPrefix(rel, "cache"+string(filepath.Separator))
}

// haStates reads /api/states straight from Home Assistant.
func haStates(t *testing.T, inst *hatest.Instance) []string {
	t.Helper()
	status, body := pgHTTP(t, inst, http.MethodGet, "/api/states", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/states: %d %s", status, body)
	}
	var rows []struct {
		EntityID string `json:"entity_id"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("states did not parse: %v", err)
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.EntityID)
	}
	sort.Strings(ids)
	return ids
}

func withPrefix(ids []string, prefix string) []string {
	var out []string
	for _, id := range ids {
		if strings.HasPrefix(id, prefix) {
			out = append(out, id)
		}
	}
	return out
}

// haRefreshToken is one row of HA's auth/refresh_tokens.
type haRefreshToken struct {
	ID         string `json:"id"`
	ClientName string `json:"client_name"`
	Type       string `json:"type"`
}

// haRefreshTokens lists the owner's tokens, read over HA's own websocket.
func haRefreshTokens(t *testing.T, inst *hatest.Instance) []haRefreshToken {
	t.Helper()
	ws := dialRawWS(t, inst.URL(), inst.Token())
	env := ws.mustSucceed(t, map[string]any{"type": "auth/refresh_tokens"})
	var rows []haRefreshToken
	if err := json.Unmarshal(env.Result, &rows); err != nil {
		t.Fatalf("auth/refresh_tokens did not parse: %v", err)
	}
	return rows
}

// deleteRefreshTokensNamed removes every token named name (test cleanup).
func deleteRefreshTokensNamed(t *testing.T, inst *hatest.Instance, name string) {
	t.Helper()
	ws := dialRawWS(t, inst.URL(), inst.Token())
	for _, rt := range haRefreshTokens(t, inst) {
		if rt.ClientName == name {
			ws.send(t, map[string]any{"type": "auth/delete_refresh_token", "refresh_token_id": rt.ID})
		}
	}
}

// haTokenWorks reports whether HA accepts token on its REST API.
func haTokenWorks(t *testing.T, inst *hatest.Instance, token string) bool {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, inst.URL()+"/api/", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/: %v", err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// haAreasNamed returns the ids of the areas whose name starts with prefix,
// read straight from HA.
func haAreasNamed(t *testing.T, inst *hatest.Instance, prefix string) []string {
	t.Helper()
	ws := dialRawWS(t, inst.URL(), inst.Token())
	var ids []string
	for id, row := range pgAreas(t, ws) {
		if strings.HasPrefix(row.Name, prefix) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// deleteAreasNamed removes the areas whose name starts with prefix (cleanup).
func deleteAreasNamed(t *testing.T, inst *hatest.Instance, prefix string) {
	t.Helper()
	ws := dialRawWS(t, inst.URL(), inst.Token())
	for _, id := range haAreasNamed(t, inst, prefix) {
		ws.send(t, map[string]any{"type": "config/area_registry/delete", "area_id": id})
	}
}

// parseEnvFile reads KEY=VALUE lines.
func parseEnvFile(t *testing.T, path string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for line := range strings.SplitSeq(readFileT(t, path), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(k)] = strings.Trim(strings.TrimSpace(v), `"'`)
	}
	return out
}

// readAuditLog parses <dir>/cache/policy-audit.log (D-71); nil when absent.
func readAuditLog(t *testing.T, dir string) []pgAuditLine {
	t.Helper()
	b, err := os.ReadFile(filepath.Clean(auditLogPath(dir)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("reading audit log: %v", err)
	}
	var out []pgAuditLine
	for line := range strings.SplitSeq(strings.TrimSpace(string(b)), "\n") {
		if line == "" {
			continue
		}
		var l pgAuditLine
		if err := json.Unmarshal([]byte(line), &l); err != nil {
			t.Fatalf("audit line is not JSON: %v\n%s", err, line)
		}
		out = append(out, l)
	}
	return out
}
