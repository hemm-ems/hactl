package cmd

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// The companion's wireguard writes (manual "What each write command checks"):
// `companion wireguard config` / `up` / `down` check `edit` / `control` /
// `control system:wireguard` — the tunnel has no address of its own. The
// expectations below are written from that row, not read off the code.

// wireguardRig is Home Assistant and the companion on one server; it counts
// every POST to the companion's wireguard routes.
type wireguardRig struct {
	*cmdTestServer

	conf string // a .conf file to push

	postMu sync.Mutex
	posts  []string // "path?query" of every POST
}

func (r *wireguardRig) posted() []string {
	r.postMu.Lock()
	defer r.postMu.Unlock()
	return slices.Clone(r.posts)
}

func wireguardHandlers(record func(*http.Request)) map[string]http.HandlerFunc {
	h := map[string]http.HandlerFunc{}
	for _, route := range []string{"config", "start", "stop"} {
		h["/v1/wireguard/"+route] = func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if record != nil {
				record(r)
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"tunnel": r.URL.Query().Get("tunnel"), "status": "ok"})
		}
	}
	return h
}

func startWireguardRig(t *testing.T) *wireguardRig {
	t.Helper()
	r := &wireguardRig{}
	r.cmdTestServer = startCmdServer(t, map[string]any{}, wireguardHandlers(func(req *http.Request) {
		r.postMu.Lock()
		r.posts = append(r.posts, req.URL.Path+"?"+req.URL.RawQuery)
		r.postMu.Unlock()
	}))
	pointAtCompanion(t, r.cmdTestServer)
	r.conf = filepath.Join(r.dir, "peer.conf")
	if err := os.WriteFile(r.conf, []byte("[Interface]\nPrivateKey = x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return r
}

type wireguardCase struct {
	name   string
	args   func(r *wireguardRig) []string // the preview; --confirm is appended for the run
	verb   string                         // the manual's verb on system:wireguard
	post   string                         // the route the confirmed run posts to
	action string                         // completes "would …"
}

func wireguardCases() []wireguardCase {
	return []wireguardCase{
		{"config", func(r *wireguardRig) []string { return []string{"companion", "wireguard", "config", "-f", r.conf} }, "edit", "/v1/wireguard/config", "push config to tunnel wg0"},
		{"up", func(*wireguardRig) []string { return []string{"companion", "wireguard", "up"} }, "control", "/v1/wireguard/start", "start tunnel wg0"},
		{"down", func(*wireguardRig) []string { return []string{"companion", "wireguard", "down"} }, "control", "/v1/wireguard/stop", "stop tunnel wg0"},
	}
}

// TestWireguardRefusedByItsPlan: a deny on system:wireguard for the command's
// verb refuses the preview and the confirmed run alike — exit 1,
// policy_denied, the rule and its line named, the one-pair plan counted —
// and the companion is never asked to change the tunnel. A deny on the other
// verb, or on another system id, leaves it allowed, and the allowed preview
// says what it checked.
func TestWireguardRefusedByItsPlan(t *testing.T) {
	for _, tc := range wireguardCases() {
		t.Run(tc.name, func(t *testing.T) {
			rig := startWireguardRig(t)
			writePolicyFile(t, rig.dir, denyPolicy(`"system:wireguard": [`+tc.verb+`]`))
			preview := tc.args(rig)
			for _, args := range [][]string{preview, append(slices.Clone(preview), "--confirm")} {
				assertWireguardRefused(t, rig.dir, tc, args)
			}
			if got := rig.posted(); len(got) != 0 {
				t.Errorf("a refused plan posted to the companion: %q", got)
			}
			if audit := readAudit(t, rig.dir); len(audit) != 2 {
				t.Errorf("audit lines for a refused preview and a refused run: %d, want 2", len(audit))
			}
			assertWireguardAllowedBesides(t, rig, tc, preview)
		})
	}
}

// assertWireguardRefused runs args under a deny on the command's verb:
// exit 1, policy_denied, the rule and its line named, the one-pair plan
// counted.
func assertWireguardRefused(t *testing.T, dir string, tc wireguardCase, args []string) {
	t.Helper()
	want := tc.verb + ` system:wireguard — deny "system:wireguard" [` + tc.verb + `] (line 6)`
	out, err := runPolicyCLI(t, dir, args...)
	if cliExitCode(err) != 1 || policyErrorClass(err) != enforce.ClassDenied {
		t.Fatalf("%v: exit %d class %q (%v), want 1 policy_denied\n%s", args, cliExitCode(err), policyErrorClass(err), err, out)
	}
	if err.Error() != "policy_denied: 1 of 1 resources denied (profile agent)" {
		t.Errorf("%v: stderr line\n got: %q\nwant: the count summary", args, err.Error())
	}
	for _, line := range []string{
		"policy_denied: would " + tc.action + " — refused by policy, nothing was written",
		"  denied: " + want,
		"  checked: 1 resources, 1 denied",
	} {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("%v: the report lacks %q:\n%s", args, line, out)
		}
	}
}

// assertWireguardAllowedBesides: a deny on the other verb, or on another
// system id, leaves the preview allowed, and it says what it checked.
func assertWireguardAllowedBesides(t *testing.T, rig *wireguardRig, tc wireguardCase, preview []string) {
	t.Helper()
	other := "control"
	if tc.verb == "control" {
		other = "edit"
	}
	for _, rule := range []string{`"system:wireguard": [` + other + `]`, `"system:cache": [` + tc.verb + `]`} {
		writePolicyFile(t, rig.dir, denyPolicy(rule))
		out, err := runPolicyCLI(t, rig.dir, preview...)
		if err != nil {
			t.Errorf("deny %s refused %v: %v\n%s", rule, preview, err, out)
			continue
		}
		if !strings.Contains(out, "\n  policy: 1 checked, allowed (profile agent)\n") {
			t.Errorf("deny %s: the allowed preview does not say what it checked:\n%s", rule, out)
		}
	}
	if got := rig.posted(); len(got) != 0 {
		t.Errorf("a preview posted to the companion: %q", got)
	}
}

// TestWireguardAllowedRunPostsOnce: an allowed confirmed run sends its one
// write, and its JSON preview carries the plan's note in details (D-70).
func TestWireguardAllowedRunPostsOnce(t *testing.T) {
	for _, tc := range wireguardCases() {
		t.Run(tc.name, func(t *testing.T) {
			rig := startWireguardRig(t)
			writePolicyFile(t, rig.dir, policyAllowEverything)
			out, err := runPolicyCLI(t, rig.dir, append(tc.args(rig), "--json")...)
			if err != nil || !strings.Contains(out, `"policy": {`) || !strings.Contains(out, `"checked": 1`) {
				t.Errorf("--json preview: %v\n%s", err, out)
			}
			out, err = runPolicyCLI(t, rig.dir, append(tc.args(rig), "--confirm")...)
			if err != nil {
				t.Fatalf("confirmed run: %v\n%s", err, out)
			}
			if got, want := rig.posted(), []string{tc.post + "?tunnel=wg0"}; !slices.Equal(got, want) {
				t.Errorf("posted %q, want exactly %q", got, want)
			}
		})
	}
}

// TestWireguardWithoutAProfileIsUnchanged (D-58): with no policy file, and
// with one that puts no profile in effect, each preview prints exactly what
// it printed before the policy existed (the strings below were taken from
// the build before the wiring), contacts nothing, and writes no audit log.
func TestWireguardWithoutAProfileIsUnchanged(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"companion", "wireguard", "up"}, "dry-run: would start tunnel wg0\n  tunnel: wg0\nuse --confirm to apply\n"},
		{[]string{"companion", "wireguard", "down", "--tunnel", "wg1"}, "dry-run: would stop tunnel wg1\n  tunnel: wg1\nuse --confirm to apply\n"},
		{[]string{"companion", "wireguard", "config", "-f", "@conf"}, "dry-run: would push config to tunnel wg0\n  tunnel: wg0\n  bytes:  27\n  lines:  2\nuse --confirm to apply\n"},
		{[]string{"companion", "wireguard", "up", "--json"}, "{\n  \"details\": {\n    \"tunnel\": \"wg0\"\n  },\n  \"action\": \"start tunnel wg0\",\n  \"hint\": \"use --confirm to apply\",\n  \"dry_run\": true\n}\n"},
	}
	for name, body := range map[string]string{"no file": "", "implicit allow-all": policyImplicitAllow} {
		for _, tc := range cases {
			rig := startWireguardRig(t)
			if body != "" {
				writePolicyFile(t, rig.dir, body)
			}
			args := slices.Clone(tc.args)
			if i := slices.Index(args, "@conf"); i >= 0 {
				args[i] = rig.conf
			}
			out, err := runPolicyCLI(t, rig.dir, args...)
			if err != nil {
				t.Fatalf("%s %v: %v\n%s", name, tc.args, err, out)
			}
			if out != tc.want {
				t.Errorf("%s %v:\n got: %q\nwant: %q", name, tc.args, out, tc.want)
			}
			rig.mu.Lock()
			ws := len(rig.cmdCounts)
			rig.mu.Unlock()
			if ws != 0 || len(rig.posted()) != 0 {
				t.Errorf("%s %v: a preview contacted the instance (%d websocket types, posts %q)", name, tc.args, ws, rig.posted())
			}
			if _, statErr := os.Stat(filepath.Join(rig.dir, "cache", enforce.AuditFileName)); !os.IsNotExist(statErr) {
				t.Errorf("%s %v: an audit log was written", name, tc.args)
			}
		}
	}
}

// The family's confirmed runs join R-12's harness: the request layer's verdict
// on each POST must lie in the command's plan.
func init() {
	for _, tc := range wireguardCases() {
		args := []string{"companion", "wireguard", tc.name, "--confirm"}
		if tc.name == "config" {
			args = []string{"companion", "wireguard", "config", "-f", "<file>", "--confirm"}
		}
		confirmedCoverageCases = append(confirmedCoverageCases, confirmedCoverageCase{
			name: "companion wireguard " + tc.name, args: args, ws: map[string]any{}, http: wireguardHandlers(nil),
			setup: func(t *testing.T, ts *cmdTestServer, args []string) []string {
				t.Helper()
				pointAtCompanion(t, ts)
				if i := slices.Index(args, "<file>"); i >= 0 {
					file := filepath.Join(ts.dir, "peer.conf")
					if err := os.WriteFile(file, []byte("[Interface]\nPrivateKey = x\n"), 0o600); err != nil {
						t.Fatal(err)
					}
					args[i] = file
				}
				return args
			},
		})
	}
}
