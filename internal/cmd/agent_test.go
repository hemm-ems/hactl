package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/policy"
)

// agentRig is a Home Assistant stand-in that mints minted (or refuses, when
// minted is a wsErrorResponse) and answers GET /api/ for the tokens in good.
func agentRig(t *testing.T, minted any, good ...string) (*cmdTestServer, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var seen []string
	ts := startCmdServer(t, map[string]any{"auth/long_lived_access_token": minted}, map[string]http.HandlerFunc{
		"/api/": func(w http.ResponseWriter, r *http.Request) {
			tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			mu.Lock()
			seen = append(seen, tok)
			mu.Unlock()
			if slices.Contains(good, tok) {
				_, _ = w.Write([]byte(`{"message":"API running."}`))
				return
			}
			http.Error(w, "401: Unauthorized", http.StatusUnauthorized)
		},
	})
	env := "HA_URL=" + ts.srv.URL + "\nHA_TOKEN=owner-token\nCOMPANION_URL=http://companion.local:8099\nCOMPANION_TOKEN=owner-secret\nTZ=Europe/Berlin\n"
	if err := os.WriteFile(filepath.Join(ts.dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	return ts, &seen
}

// agentAddIn runs agent add in-process with the given flags.
func agentAddIn(t *testing.T, from, path, token, name, stdin string) (string, error) {
	t.Helper()
	withOwnerTerminal(t)
	saved := []string{flagAgentFrom, flagAgentPath, flagAgentToken}
	flagAgentFrom, flagAgentPath, flagAgentToken = from, path, token
	defer func() { flagAgentFrom, flagAgentPath, flagAgentToken = saved[0], saved[1], saved[2] }()
	var out bytes.Buffer
	err := runAgentAdd(context.Background(), strings.NewReader(stdin), &out, name)
	return out.String(), err
}

// D-160, D-164, D-150: agent add mints a token of its own, writes the .env
// with the source's URLs and the requirement, and the read-only starter.
func TestAgentAddCreatesTheAgentsDirectory(t *testing.T) {
	home := isolateUserCache(t)
	ts, _ := agentRig(t, "minted-token", "minted-token")
	out, err := agentAddIn(t, ts.dir, "", "", "kitchen", "")
	if err != nil {
		t.Fatalf("agent add: %v\n%s", err, out)
	}
	dir := filepath.Join(home, ".hactl", "agents", "kitchen")
	if p := ts.cmdParams["auth/long_lived_access_token"]; p == nil || p["client_name"] != "hactl agent kitchen" || p["lifespan"] != float64(3650) {
		t.Errorf("token request = %v, want client_name \"hactl agent kitchen\", lifespan 3650", p)
	}
	if n := ts.cmdCounts["auth/long_lived_access_token"]; n != 1 {
		t.Errorf("minted %d tokens, want exactly 1", n)
	}
	cfg, loadErr := config.Load(dir)
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if cfg.URL != ts.srv.URL || cfg.Token != "minted-token" || cfg.CompanionURL != "http://companion.local:8099" || cfg.TZ != "Europe/Berlin" {
		t.Errorf("agent .env: %+v", cfg)
	}
	env := readFile(t, filepath.Join(dir, ".env"))
	if strings.Contains(env, "owner-token") || strings.Contains(env, "owner-secret") || strings.Contains(env, "COMPANION_TOKEN") {
		t.Errorf("the owner's secrets reached the agent's .env:\n%s", env)
	}
	if req, reqErr := config.PolicyRequired(dir); !req || reqErr != nil {
		t.Errorf("HACTL_POLICY=required not written: %v %v", req, reqErr)
	}
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, ".env"): 0o600} {
		info, statErr := os.Stat(path)
		if statErr != nil || info.Mode().Perm() != want {
			t.Errorf("%s: %v, want mode %v", path, statErr, want)
		}
	}
	pol, err := policy.Load(dir)
	if err != nil {
		t.Fatalf("the starter does not load: %v", err)
	}
	if pol.Active() != "readonly" || !pol.InEffect() {
		t.Errorf("starter: active %q in effect %v", pol.Active(), pol.InEffect())
	}
	for _, v := range []policy.Verb{policy.Control, policy.Edit, policy.Create, policy.Delete} {
		for _, r := range []policy.Resource{{Kind: policy.KindEntity, ID: "light.x"}, {Kind: policy.KindAutomation, ID: "a", Sub: "config"}, {Kind: policy.KindService, ID: "light.turn_on"}} {
			if pol.Decide(r, v, nil).Allowed {
				t.Errorf("the starter allows %s %s", v, r)
			}
		}
	}
	if !pol.Decide(policy.Resource{Kind: policy.KindEntity, ID: "lock.front"}, policy.Read, nil).Allowed {
		t.Error("the starter does not allow reading")
	}
	for _, want := range []string{
		"created ~/.hactl/agents/kitchen  (from ",
		"  token:   a long-lived token of its own, \"hactl agent kitchen\"\n",
		"  policy:  read-only until you activate one (HACTL_POLICY=required)\n",
		"  mcp:     claude mcp add kitchen -- hactl mcp --dir ~/.hactl/agents/kitchen --allow-writes\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
}

// D-164 (orchestrator 2026-11-05): a path that exists at all is refused,
// nothing changed, no token made.
func TestAgentAddRefusesAnExistingDirectory(t *testing.T) {
	isolateUserCache(t)
	ts, _ := agentRig(t, "minted-token", "minted-token")
	for name, setup := range map[string]func(string) error{
		"empty dir": func(p string) error { return os.Mkdir(p, 0o700) },
		"file":      func(p string) error { return os.WriteFile(p, []byte("x"), 0o600) },
	} {
		target := filepath.Join(t.TempDir(), "agent")
		if err := setup(target); err != nil {
			t.Fatal(err)
		}
		_, err := agentAddIn(t, ts.dir, target, "", "kitchen", "")
		if cliExitCode(err) != 1 || !strings.Contains(err.Error(), "already exists") {
			t.Errorf("%s: err = %v", name, err)
		}
		if entries, _ := os.ReadDir(target); len(entries) != 0 {
			t.Errorf("%s: something was written into it", name)
		}
	}
	if n := ts.cmdCounts["auth/long_lived_access_token"]; n != 0 {
		t.Errorf("a refused agent add minted %d tokens", n)
	}
}

func TestAgentAddRefusesWhatItCannotUse(t *testing.T) {
	isolateUserCache(t)
	ts, _ := agentRig(t, "minted-token", "minted-token")
	for _, bad := range []string{"Kitchen", "-x", "a/b", "..", strings.Repeat("a", 65), "küche"} {
		if _, err := agentAddIn(t, ts.dir, "", "", bad, ""); err == nil || !strings.Contains(err.Error(), "not an agent name") {
			t.Errorf("name %q: err = %v", bad, err)
		}
	}
	if _, err := agentAddIn(t, ts.dir, "", "abc123", "kitchen", ""); err == nil || !strings.Contains(err.Error(), "--token takes only -") {
		t.Errorf("--token <literal>: err = %v", err)
	}
	if n := ts.cmdCounts["auth/long_lived_access_token"]; n != 0 {
		t.Errorf("minted %d tokens on refused input", n)
	}
}

// --token - reads the token instead of minting one, and checks it first.
func TestAgentAddTakesAPastedToken(t *testing.T) {
	home := isolateUserCache(t)
	ts, seen := agentRig(t, "minted-token", "pasted-token")
	out, err := agentAddIn(t, ts.dir, "", "-", "kitchen", "pasted-token\n")
	if err != nil {
		t.Fatalf("agent add --token -: %v\n%s", err, out)
	}
	if n := ts.cmdCounts["auth/long_lived_access_token"]; n != 0 {
		t.Errorf("--token - still minted %d tokens", n)
	}
	cfg, err := config.Load(filepath.Join(home, ".hactl", "agents", "kitchen"))
	if err != nil || cfg.Token != "pasted-token" {
		t.Errorf("agent token = %v, %v", cfg, err)
	}
	if len(*seen) == 0 || (*seen)[0] != "pasted-token" {
		t.Errorf("the pasted token was not checked against Home Assistant first: %v", *seen)
	}

	// A token Home Assistant refuses writes nothing.
	_, err = agentAddIn(t, ts.dir, "", "-", "other", "wrong-token\n")
	if err == nil || !strings.Contains(err.Error(), "does not authenticate") {
		t.Errorf("a bad pasted token: err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".hactl", "agents", "other")); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("a bad pasted token still created the directory")
	}
}

// When Home Assistant will not mint a token, nothing is written and the
// answer points to --token -.
func TestAgentAddWhenHomeAssistantWillNotMint(t *testing.T) {
	home := isolateUserCache(t)
	ts, _ := agentRig(t, wsErrorResponse{Code: "unauthorized", Message: "Unauthorized"})
	_, err := agentAddIn(t, ts.dir, "", "", "kitchen", "")
	if cliExitCode(err) != 1 || !strings.Contains(err.Error(), "--token -") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Errorf("err = %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(home, ".hactl", "agents", "kitchen")); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("the directory was created although no token was made")
	}
}

// D-149, D-164: agent add without a terminal is refused before anything.
func TestAgentAddRefusedWithoutATerminalCLI(t *testing.T) {
	home := isolateUserCache(t)
	ts, _ := agentRig(t, "minted-token", "minted-token")
	c := hactlProcess(t, "--dir", ts.dir, "agent", "add", "kitchen")
	c.Env = append(c.Env, "HOME="+home)
	var stderr bytes.Buffer
	c.Stdin, c.Stdout, c.Stderr = strings.NewReader(""), &bytes.Buffer{}, &stderr
	err := c.Run()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(stderr.String(), "agent add needs a terminal: only the owner adds an agent") {
		t.Errorf("agent add without a terminal: %v\n%s", err, stderr.String())
	}
	if ts.cmdCounts["auth/long_lived_access_token"] != 0 {
		t.Error("minted a token without a terminal")
	}
	if _, statErr := os.Stat(filepath.Join(home, ".hactl", "agents", "kitchen")); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("created the directory without a terminal")
	}
}

// D-167: agent ls lists each agent directory with its own profile, its
// requirement and its last audit line; REQUIRED is a JSON boolean.
func TestAgentLsListsEachAgentsOwnPolicy(t *testing.T) {
	isolateUserCache(t)
	root := t.TempDir()
	mk := func(name, env, pol string) string {
		d := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Join(d, "cache"), 0o700); err != nil {
			t.Fatal(err)
		}
		if env != "" {
			_ = os.WriteFile(filepath.Join(d, ".env"), []byte(env), 0o600)
		}
		if pol != "" {
			_ = os.WriteFile(filepath.Join(d, policy.FileName), []byte(pol), 0o600)
		}
		return d
	}
	env := "HA_URL=http://127.0.0.1:9\nHA_TOKEN=x\nHACTL_POLICY=required\n"
	a := mk("alpha", env, starterPolicy)
	_ = os.WriteFile(filepath.Join(a, "cache", "policy-audit.log"), []byte(
		`{"time":"2026-11-05T10:00:00+01:00","command":"hactl svc call","dry_run":true,"layer":"command","resource":"entity:light.x","verb":"control","allowed":true,"rule":"r","line":1,"profile":"readonly"}`+"\n"+
			`{"time":"2026-11-05T10:01:00+01:00","command":"hactl svc call","dry_run":true,"layer":"command","resource":"entity:lock.y","verb":"control","allowed":false,"rule":"r","line":2,"profile":"readonly"}`+"\n"), 0o600)
	mk("beta", "HA_URL=http://127.0.0.1:9\nHA_TOKEN=x\n", "version: 1\nactive: nobody\n")
	mk("gamma", "HA_URL=http://127.0.0.1:9\nHA_TOKEN=x\n", "")
	mk("not-an-agent", "", "")

	out, err := runPolicyCLI(t, a, "agent", "ls", "--root", root)
	if err != nil {
		t.Fatalf("agent ls: %v\n%s", err, out)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 4 || !strings.HasPrefix(lines[0], "NAME") {
		t.Fatalf("agent ls:\n%s", out)
	}
	for i, want := range [][]string{
		{"alpha", "readonly", "yes", "2026-11-05T10:01:00+01:00 denied control entity:lock.y"},
		{"beta", "broken", "no", "never"},
		{"gamma", "none", "no", "never"},
	} {
		for _, w := range want {
			if !strings.Contains(lines[i+1], w) {
				t.Errorf("row %d %q lacks %q", i+1, lines[i+1], w)
			}
		}
	}
	out, err = runPolicyCLI(t, a, "agent", "ls", "--root", root, "--json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &rows); jsonErr != nil || len(rows) != 3 {
		t.Fatalf("agent ls --json: %v\n%s", jsonErr, out)
	}
	if rows[0]["required"] != true || rows[1]["required"] != false {
		t.Errorf("required is not a JSON boolean: %v / %v", rows[0]["required"], rows[1]["required"])
	}
	if lw, ok := rows[0]["last_write_check"].(map[string]any); !ok || lw["resource"] != "entity:lock.y" {
		t.Errorf("last_write_check = %v", rows[0]["last_write_check"])
	}
	if rows[2]["last_write_check"] != nil || rows[2]["policy"] != "missing" {
		t.Errorf("gamma = %v", rows[2])
	}

	out, err = runPolicyCLI(t, a, "agent", "ls", "--root", filepath.Join(root, "nope"))
	if err != nil || !strings.Contains(out, "no agents under") {
		t.Errorf("a missing root: %v %q", err, out)
	}
}
