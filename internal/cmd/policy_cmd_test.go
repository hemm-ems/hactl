package cmd

// CLI spec tests for `hactl policy show|check|lint` (phase 1), authored from
// the contract only: docs/manual.md "Policy (permission guardrail)",
// docs/decisions.md D-51, D-58, D-60, D-61, D-62, and internal/policy's
// exported constants (Disclaimer, the Rule* texts, the lint codes). Every test
// drives the real command tree through RunWithOutput with `--dir`, so it reads
// what a caller reads: stdout, and the error that becomes stderr + exit code.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/policy"
)

// policyManualExample is the manual's "Policy file format" example, verbatim.
// The manual's --json examples quote its lines: deny "entity:lock.*" on 16,
// allow "entity:light.*" on 12.
const policyManualExample = `# <instance>/policy.yaml
version: 1
active: agent                     # the profile in effect, for CLI and MCP alike
profiles:
  default:                        # in effect when there is no active:
    deny:
      "entity:lock.*":                  [write]
  agent:
    default: deny
    allow:
      "entity:*":                       [read]
      "entity:light.*":                 [control]
      "automation:pg_*":                [read, edit, create]
      "entity:*/registry/labels":       [edit]
    deny:
      "entity:lock.*":                  [read, write]
      "entity:device_tracker.*/attr/latitude":  [read]
      "file:**":                        [write]
    where:
      "entity:light.*": { area: wohnzimmer }
`

// policyBroken has three D-60 problems: an unknown profile in active: (line
// 2), an unknown kind (line 7), an unknown verb (line 9).
const policyBroken = `version: 1
active: agnet
profiles:
  agent:
    default: deny
    allow:
      "entiy:*": [read]
    deny:
      "entity:lock.*": [wirte]
`

// closedHAURL is the URL of a Home Assistant that is not there: a listener
// that was opened and closed again, so a connection is refused at once.
func closedHAURL(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	return url
}

// countingHA is a Home Assistant stand-in that answers nothing useful and
// counts every request it receives, HTTP or WebSocket upgrade alike.
func countingHA(t *testing.T) (string, *atomic.Int64) {
	t.Helper()
	var n atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n.Add(1)
		http.Error(w, "not here", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	return srv.URL, &n
}

// policyInstance makes an instance directory with a .env pointing at haURL
// and, when body is not empty, a policy.yaml holding body.
func policyInstance(t *testing.T, haURL, body string) string {
	t.Helper()
	dir := t.TempDir()
	env := fmt.Sprintf("HA_URL=%s\nHA_TOKEN=test-token\n", haURL)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, policy.FileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runPolicyCLI runs `hactl <args> --dir <dir>` in process and returns stdout
// and the error (nil = exit 0). Prose is uncapped so an assertion never reads
// a truncation; --json is never capped anyway.
func runPolicyCLI(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	full := append([]string{"hactl"}, args...)
	full = append(full, "--dir", dir, "--timeout", "3s", "--tokensmax", "0")
	var buf bytes.Buffer
	err := RunWithOutput(full, &buf)
	return buf.String(), err
}

// cliExitCode is the exit code cmd/hactl/main.go makes of err.
func cliExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

func decodeObject(t *testing.T, out string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
		t.Fatalf("stdout is not one JSON object: %v\n%s", err, out)
	}
	return m
}

func requireKeys(t *testing.T, what string, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if _, ok := m[k]; !ok {
			t.Errorf("%s: --json lacks the documented key %q (have %v)", what, k, mapKeys(m))
		}
	}
}

func mapKeys(m map[string]any) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	slices.Sort(ks)
	return ks
}

func wantField(t *testing.T, what string, m map[string]any, key string, want any) {
	t.Helper()
	got, ok := m[key]
	if !ok {
		t.Errorf("%s: no %q in --json", what, key)
		return
	}
	gj, _ := json.Marshal(got)
	wj, _ := json.Marshal(want)
	if string(gj) != string(wj) {
		t.Errorf("%s: %q = %s, want %s", what, key, gj, wj)
	}
}

var showKeys = []string{"file", "exists", "valid", "problems", "level", "disclaimer", "enforced",
	"profile", "selected_by", "what_if", "allow_all", "default", "profiles", "rules"}

var checkKeys = []string{"resource", "verb", "allowed", "rule", "line", "profile",
	"selected_by", "what_if", "level", "enforced", "notes"}

var lintKeys = []string{"file", "exists", "valid", "level", "findings", "unchecked"}

// --- policy show ---------------------------------------------------------------

// TestPolicyShowWithoutAFile: D-58 — `no policy file`, allow-all, exit 0;
// the level-A disclaimer and `not enforced yet` are printed regardless.
func TestPolicyShowWithoutAFile(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), "")

	out, err := runPolicyCLI(t, dir, "policy", "show")
	if cliExitCode(err) != 0 {
		t.Fatalf("policy show (no file): exit %d (%v), want 0\n%s", cliExitCode(err), err, out)
	}
	for _, want := range []string{"no policy file", policy.Disclaimer, "not enforced yet"} {
		if !strings.Contains(out, want) {
			t.Errorf("policy show (no file) does not say %q:\n%s", want, out)
		}
	}

	out, err = runPolicyCLI(t, dir, "policy", "show", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("policy show --json (no file): exit %d (%v)", cliExitCode(err), err)
	}
	m := decodeObject(t, out)
	requireKeys(t, "show/no-file", m, showKeys...)
	wantField(t, "show/no-file", m, "exists", false)
	wantField(t, "show/no-file", m, "profile", "")
	wantField(t, "show/no-file", m, "selected_by", "no-file")
	wantField(t, "show/no-file", m, "allow_all", true)
	wantField(t, "show/no-file", m, "default", "allow")
	wantField(t, "show/no-file", m, "profiles", []string{})
	wantField(t, "show/no-file", m, "rules", []any{})
	wantField(t, "show/no-file", m, "level", "A")
	wantField(t, "show/no-file", m, "disclaimer", policy.Disclaimer)
	wantField(t, "show/no-file", m, "enforced", false)
	wantField(t, "show/no-file", m, "what_if", false)
}

// TestPolicyShowJSONForTheManualExample: the manual's --json shape, field by
// field, on the manual's own example file.
func TestPolicyShowJSONForTheManualExample(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyManualExample)
	out, err := runPolicyCLI(t, dir, "policy", "show", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("policy show --json: exit %d (%v)\n%s", cliExitCode(err), err, out)
	}
	m := decodeObject(t, out)
	what := "show/manual-example"
	requireKeys(t, what, m, showKeys...)
	wantField(t, what, m, "file", filepath.Join(dir, policy.FileName))
	wantField(t, what, m, "exists", true)
	wantField(t, what, m, "valid", true)
	wantField(t, what, m, "problems", []any{})
	wantField(t, what, m, "level", "A")
	wantField(t, what, m, "disclaimer", policy.Disclaimer)
	wantField(t, what, m, "enforced", false)
	wantField(t, what, m, "profile", "agent")
	wantField(t, what, m, "selected_by", "active")
	wantField(t, what, m, "what_if", false)
	wantField(t, what, m, "allow_all", false)
	wantField(t, what, m, "default", "deny")
	wantField(t, what, m, "profiles", []string{"agent", "default"})

	type ruleJSON struct {
		Effect   string          `json:"effect"`
		Pattern  string          `json:"pattern"`
		Verbs    []string        `json:"verbs"`
		Expanded []string        `json:"expanded"`
		Where    json.RawMessage `json:"where"`
		Line     int             `json:"line"`
	}
	var doc struct {
		Rules []ruleJSON `json:"rules"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("rules do not decode: %v", err)
	}
	all := []string{"read", "control", "edit", "create", "delete"}
	want := []ruleJSON{
		{"deny", "entity:lock.*", []string{"read", "write"}, all, json.RawMessage(`null`), 16},
		{"deny", "entity:device_tracker.*/attr/latitude", []string{"read"}, []string{"read"}, json.RawMessage(`null`), 17},
		{"deny", "file:**", []string{"write"}, []string{"control", "edit", "create", "delete"}, json.RawMessage(`null`), 18},
		{"allow", "entity:*", []string{"read"}, []string{"read"}, json.RawMessage(`null`), 11},
		{"allow", "entity:light.*", []string{"control"}, []string{"control"}, json.RawMessage(`{"area":["wohnzimmer"],"label":[]}`), 12},
		{"allow", "automation:pg_*", []string{"read", "edit", "create"}, []string{"read", "edit", "create"}, json.RawMessage(`null`), 13},
		{"allow", "entity:*/registry/labels", []string{"edit"}, []string{"edit"}, json.RawMessage(`null`), 14},
	}
	if len(doc.Rules) != len(want) {
		t.Fatalf("rules: %d, want %d (deny rules first, then allow rules, each in file order)\n%s", len(doc.Rules), len(want), out)
	}
	for i, w := range want {
		g := doc.Rules[i]
		var gw, ww any
		_ = json.Unmarshal(g.Where, &gw)
		_ = json.Unmarshal(w.Where, &ww)
		gwj, _ := json.Marshal(gw)
		wwj, _ := json.Marshal(ww)
		if g.Effect != w.Effect || g.Pattern != w.Pattern || g.Line != w.Line ||
			!slices.Equal(g.Verbs, w.Verbs) || !slices.Equal(g.Expanded, w.Expanded) || string(gwj) != string(wwj) {
			t.Errorf("rule %d = %+v (where %s), want %+v (where %s)", i, g, gwj, w, wwj)
		}
	}
}

// TestPolicyShowProseForTheManualExample: the prose names the file, the
// disclaimer verbatim, the profile and how it was selected, the default, every
// rule with its line (denies before allows), the declared profiles and
// `not enforced yet`.
func TestPolicyShowProseForTheManualExample(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyManualExample)
	out, err := runPolicyCLI(t, dir, "policy", "show")
	if cliExitCode(err) != 0 {
		t.Fatalf("policy show: exit %d (%v)\n%s", cliExitCode(err), err, out)
	}
	for _, want := range []string{
		filepath.Join(dir, policy.FileName), policy.Disclaimer, "agent", "active", "deny", "not enforced yet", "default",
		`deny "entity:lock.*" [read write]`, `deny "entity:device_tracker.*/attr/latitude" [read]`, `deny "file:**" [write]`,
		`allow "entity:*" [read]`, `allow "entity:light.*" [control] where area=wohnzimmer`,
		`allow "automation:pg_*" [read edit create]`, `allow "entity:*/registry/labels" [edit]`,
		"16", "17", "18", "11", "12", "13", "14",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("policy show does not print %q:\n%s", want, out)
		}
	}
	lastDeny := strings.Index(out, `deny "file:**" [write]`)
	firstAllow := strings.Index(out, `allow "entity:*" [read]`)
	if lastDeny < 0 || firstAllow < 0 || lastDeny > firstAllow {
		t.Errorf("policy show lists an allow before a deny (deny rules come first):\n%s", out)
	}
	if strings.Contains(out, "ALLOW-ALL") {
		t.Errorf("policy show says ALLOW-ALL for a file whose active profile is in effect:\n%s", out)
	}
}

// TestPolicyShowSaysAllowAllLoudly: D-61 — profiles declared, no active:, no
// default profile: nothing is in effect, and show says ALLOW-ALL.
func TestPolicyShowSaysAllowAllLoudly(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), "version: 1\nprofiles:\n  agent:\n    default: deny\n")
	out, err := runPolicyCLI(t, dir, "policy", "show")
	if cliExitCode(err) != 0 {
		t.Fatalf("policy show: exit %d (%v)\n%s", cliExitCode(err), err, out)
	}
	if !strings.Contains(out, "ALLOW-ALL") {
		t.Errorf("policy show does not say ALLOW-ALL for an implicit allow-all:\n%s", out)
	}
	out, err = runPolicyCLI(t, dir, "policy", "show", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("policy show --json: exit %d (%v)", cliExitCode(err), err)
	}
	m := decodeObject(t, out)
	wantField(t, "show/implicit", m, "selected_by", "implicit-allow-all")
	wantField(t, "show/implicit", m, "allow_all", true)
	wantField(t, "show/implicit", m, "profile", "")
	wantField(t, "show/implicit", m, "exists", true)
	wantField(t, "show/implicit", m, "profiles", []string{"agent"})
}

// TestPolicyShowSelectsTheDefaultProfile: D-61 — no active:, a profile named
// default: that one is in effect.
func TestPolicyShowSelectsTheDefaultProfile(t *testing.T) {
	body := strings.Replace(policyManualExample, "active: agent                     # the profile in effect, for CLI and MCP alike\n", "", 1)
	dir := policyInstance(t, closedHAURL(t), body)
	out, err := runPolicyCLI(t, dir, "policy", "show", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("policy show --json: exit %d (%v)\n%s", cliExitCode(err), err, out)
	}
	m := decodeObject(t, out)
	wantField(t, "show/default-profile", m, "profile", "default")
	wantField(t, "show/default-profile", m, "selected_by", "default-profile")
	wantField(t, "show/default-profile", m, "default", "allow")
	wantField(t, "show/default-profile", m, "allow_all", false)
}

// TestPolicyShowReportsABrokenFile: D-60 — show reports every problem as
// <file>:<line>: <message> and exits 1; --json says valid false with the
// problems, and an empty view.
func TestPolicyShowReportsABrokenFile(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyBroken)
	file := filepath.Join(dir, policy.FileName)

	out, err := runPolicyCLI(t, dir, "policy", "show")
	if cliExitCode(err) != 1 {
		t.Fatalf("policy show (broken): exit %d, want 1\n%s", cliExitCode(err), out)
	}
	for _, line := range []int{2, 7, 9} {
		if want := fmt.Sprintf("%s:%d: ", file, line); !strings.Contains(out, want) {
			t.Errorf("policy show (broken) does not report %q:\n%s", want, out)
		}
	}

	out, err = runPolicyCLI(t, dir, "policy", "show", "--json")
	if cliExitCode(err) != 1 {
		t.Fatalf("policy show --json (broken): exit %d, want 1", cliExitCode(err))
	}
	m := decodeObject(t, out)
	what := "show/broken"
	requireKeys(t, what, m, showKeys...)
	wantField(t, what, m, "valid", false)
	wantField(t, what, m, "exists", true)
	wantField(t, what, m, "profile", "")
	wantField(t, what, m, "selected_by", "")
	wantField(t, what, m, "default", "")
	wantField(t, what, m, "allow_all", false)
	wantField(t, what, m, "profiles", []string{})
	wantField(t, what, m, "rules", []any{})
	wantField(t, what, m, "disclaimer", policy.Disclaimer)
	probs, _ := m["problems"].([]any)
	lines := make([]int, 0, len(probs))
	for _, p := range probs {
		pm, _ := p.(map[string]any)
		ln, _ := pm["line"].(float64)
		msg, _ := pm["message"].(string)
		if msg == "" {
			t.Errorf("problem without a message: %v", pm)
		}
		lines = append(lines, int(ln))
	}
	for _, want := range []int{2, 7, 9} {
		if !slices.Contains(lines, want) {
			t.Errorf("problems %v lack line %d", lines, want)
		}
	}
	if !slices.IsSorted(lines) {
		t.Errorf("problems are not ordered by line: %v", lines)
	}
}

// TestPolicyShowAsIsAWhatIf: D-62 — --as shows another declared profile,
// marked what-if, and the next plain show is unchanged.
func TestPolicyShowAsIsAWhatIf(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyManualExample)

	out, err := runPolicyCLI(t, dir, "policy", "show", "--as", "default", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("policy show --as default --json: exit %d (%v)\n%s", cliExitCode(err), err, out)
	}
	m := decodeObject(t, out)
	wantField(t, "show/as", m, "profile", "default")
	wantField(t, "show/as", m, "selected_by", "what-if")
	wantField(t, "show/as", m, "what_if", true)
	wantField(t, "show/as", m, "default", "allow")

	out, err = runPolicyCLI(t, dir, "policy", "show", "--as", "default")
	if cliExitCode(err) != 0 {
		t.Fatalf("policy show --as default: exit %d (%v)", cliExitCode(err), err)
	}
	if !strings.Contains(out, "what-if") {
		t.Errorf("policy show --as does not mark its output what-if:\n%s", out)
	}

	out, err = runPolicyCLI(t, dir, "policy", "show", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("policy show --json: exit %d (%v)", cliExitCode(err), err)
	}
	m = decodeObject(t, out)
	wantField(t, "show/after-as", m, "profile", "agent")
	wantField(t, "show/after-as", m, "selected_by", "active")
	wantField(t, "show/after-as", m, "what_if", false)
}

// TestPolicyAsRefusesAnUndeclaredProfile: --as names a declared profile; any
// other name, and any name with no file, is refused — exit 1, nothing on
// stdout — on both show and check.
func TestPolicyAsRefusesAnUndeclaredProfile(t *testing.T) {
	withFile := policyInstance(t, closedHAURL(t), policyManualExample)
	noFile := policyInstance(t, closedHAURL(t), "")
	for _, tc := range []struct {
		name   string
		dir    string
		args   []string
		errHas []string
	}{
		{"show unknown", withFile, []string{"policy", "show", "--as", "agnet"}, []string{"agnet", "agent", "default"}},
		{"check unknown", withFile, []string{"policy", "check", "entity:lock.front_door", "read", "--as", "agnet"}, []string{"agnet", "agent", "default"}},
		{"show no file", noFile, []string{"policy", "show", "--as", "default"}, nil},
		{"check no file", noFile, []string{"policy", "check", "entity:lock.front_door", "read", "--as", "default"}, nil},
		{"show unknown json", withFile, []string{"policy", "show", "--as", "agnet", "--json"}, []string{"agnet"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := runPolicyCLI(t, tc.dir, tc.args...)
			if cliExitCode(err) != 1 {
				t.Fatalf("exit %d, want 1 (refusal)", cliExitCode(err))
			}
			if out != "" {
				t.Errorf("a refusal wrote to stdout: %q", out)
			}
			// The refusal is about the profile (and, for an unknown one, names
			// the declared profiles — As's doc), not about something else.
			for _, s := range tc.errHas {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("refusal %q does not name %q", err, s)
				}
			}
		})
	}
}

// --- policy check --------------------------------------------------------------

// TestPolicyCheckExitCodeIsTheVerdict: exit 0 allowed, exit 1 denied, and a
// denial still prints its verdict (a refusal is exit 1 WITHOUT one).
func TestPolicyCheckExitCodeIsTheVerdict(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyManualExample)

	out, err := runPolicyCLI(t, dir, "policy", "check", "entity:sensor.temp", "read")
	if cliExitCode(err) != 0 {
		t.Fatalf("allowed check: exit %d (%v), want 0\n%s", cliExitCode(err), err, out)
	}
	first := strings.SplitN(out, "\n", 2)[0]
	if !strings.HasPrefix(first, "allowed: entity:sensor.temp read") {
		t.Errorf("allowed check's first line = %q, want `allowed: entity:sensor.temp read`", first)
	}
	for _, want := range []string{`allow "entity:*" [read]`, "11", "agent", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("allowed check does not name %q:\n%s", want, out)
		}
	}

	out, err = runPolicyCLI(t, dir, "policy", "check", "entity:lock.front_door", "control")
	if cliExitCode(err) != 1 {
		t.Fatalf("denied check: exit %d, want 1\n%s", cliExitCode(err), out)
	}
	first = strings.SplitN(out, "\n", 2)[0]
	if !strings.HasPrefix(first, "denied: entity:lock.front_door control") {
		t.Errorf("denied check's first line = %q, want `denied: entity:lock.front_door control`", first)
	}
	for _, want := range []string{`deny "entity:lock.*" [read write]`, "16", "agent", "active"} {
		if !strings.Contains(out, want) {
			t.Errorf("denied check does not name %q:\n%s", want, out)
		}
	}
}

// TestPolicyCheckJSON: the manual's check --json shape, for a deny, an allow
// and a profile default (line 0).
func TestPolicyCheckJSON(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyManualExample)
	for _, tc := range []struct {
		resource, verb string
		exit           int
		allowed        bool
		rule           string
		line           int
	}{
		{"entity:lock.front_door", "control", 1, false, `deny "entity:lock.*" [read write]`, 16},
		{"automation:pg_heating", "edit", 0, true, `allow "automation:pg_*" [read edit create]`, 13},
		{"automation:heating", "delete", 1, false, policy.RuleDefaultDeny, 0},
		{"entity:device_tracker.jan/attr/latitude", "read", 1, false, `deny "entity:device_tracker.*/attr/latitude" [read]`, 17},
	} {
		t.Run(tc.resource+" "+tc.verb, func(t *testing.T) {
			out, err := runPolicyCLI(t, dir, "policy", "check", tc.resource, tc.verb, "--json")
			if cliExitCode(err) != tc.exit {
				t.Fatalf("exit %d (%v), want %d\n%s", cliExitCode(err), err, tc.exit, out)
			}
			m := decodeObject(t, out)
			what := "check " + tc.resource + " " + tc.verb
			requireKeys(t, what, m, checkKeys...)
			wantField(t, what, m, "resource", tc.resource)
			wantField(t, what, m, "verb", tc.verb)
			wantField(t, what, m, "allowed", tc.allowed)
			wantField(t, what, m, "rule", tc.rule)
			wantField(t, what, m, "line", tc.line)
			wantField(t, what, m, "profile", "agent")
			wantField(t, what, m, "selected_by", "active")
			wantField(t, what, m, "what_if", false)
			wantField(t, what, m, "level", "A")
			wantField(t, what, m, "enforced", false)
			wantField(t, what, m, "notes", []any{})
		})
	}
}

// TestPolicyCheckWithoutAFileAllowsEverything: D-58 — rule `no policy file`,
// exit 0, selected_by no-file.
func TestPolicyCheckWithoutAFileAllowsEverything(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), "")
	out, err := runPolicyCLI(t, dir, "policy", "check", "entity:lock.front_door", "delete", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("exit %d (%v), want 0\n%s", cliExitCode(err), err, out)
	}
	m := decodeObject(t, out)
	wantField(t, "check/no-file", m, "allowed", true)
	wantField(t, "check/no-file", m, "rule", policy.RuleNoFile)
	wantField(t, "check/no-file", m, "line", 0)
	wantField(t, "check/no-file", m, "selected_by", "no-file")
	wantField(t, "check/no-file", m, "profile", "")
}

// TestPolicyCheckImplicitAllowAll: D-61 — the implicit allow-all decides with
// its own rule text.
func TestPolicyCheckImplicitAllowAll(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), "version: 1\nprofiles:\n  agent:\n    default: deny\n")
	out, err := runPolicyCLI(t, dir, "policy", "check", "entity:lock.front_door", "delete", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("exit %d (%v), want 0\n%s", cliExitCode(err), err, out)
	}
	m := decodeObject(t, out)
	wantField(t, "check/implicit", m, "allowed", true)
	wantField(t, "check/implicit", m, "rule", policy.RuleImplicit)
	wantField(t, "check/implicit", m, "selected_by", "implicit-allow-all")
}

// TestPolicyCheckRefusesWithoutAVerdict: an invalid resource or verb, `write`,
// a missing argument, or a broken file is refused — exit 1, nothing on stdout,
// the reason in the error (stderr). A broken file is named as file:line.
func TestPolicyCheckRefusesWithoutAVerdict(t *testing.T) {
	good := policyInstance(t, closedHAURL(t), policyManualExample)
	broken := policyInstance(t, closedHAURL(t), policyBroken)
	for _, tc := range []struct {
		name    string
		dir     string
		args    []string
		errHas  string
		jsonToo bool
	}{
		{"unknown kind", good, []string{"entiy:lock.front_door", "read"}, "", true},
		{"wildcard resource", good, []string{"entity:lock.*", "read"}, "", true},
		{"unknown sub-resource", good, []string{"entity:lock.front_door/attrs/x", "read"}, "", false},
		{"write is not a verb to check", good, []string{"entity:lock.front_door", "write"}, "", true},
		{"unknown verb", good, []string{"entity:lock.front_door", "wirte"}, "", false},
		{"uppercase verb", good, []string{"entity:lock.front_door", "Read"}, "", false},
		{"one argument", good, []string{"entity:lock.front_door"}, "", false},
		{"broken file", broken, []string{"entity:lock.front_door", "read"}, filepath.Join(broken, policy.FileName) + ":2", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			variants := [][]string{tc.args}
			if tc.jsonToo {
				variants = append(variants, append(slices.Clone(tc.args), "--json"))
			}
			for _, a := range variants {
				out, err := runPolicyCLI(t, tc.dir, append([]string{"policy", "check"}, a...)...)
				if cliExitCode(err) != 1 {
					t.Errorf("policy check %v: exit %d, want 1", a, cliExitCode(err))
					continue
				}
				if out != "" {
					t.Errorf("policy check %v: a refusal wrote to stdout (a refusal carries no verdict): %q", a, out)
				}
				if tc.errHas != "" && !strings.Contains(err.Error(), tc.errHas) {
					t.Errorf("policy check %v: error %q does not name %q", a, err, tc.errHas)
				}
			}
		})
	}
}

// TestPolicyCheckAsIsAWhatIf: D-62 — the what-if verdict starts with a
// `what-if: --as <p>` line and is marked in --json; the effective answer is
// unchanged afterwards.
func TestPolicyCheckAsIsAWhatIf(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyManualExample)

	out, err := runPolicyCLI(t, dir, "policy", "check", "entity:lock.front_door", "read", "--as", "default")
	if cliExitCode(err) != 0 {
		t.Fatalf("what-if check: exit %d (%v), want 0 (default profile allows read)\n%s", cliExitCode(err), err, out)
	}
	if first := strings.SplitN(out, "\n", 2)[0]; !strings.HasPrefix(first, "what-if: --as default") {
		t.Errorf("what-if check's first line = %q, want `what-if: --as default`", first)
	}
	if !strings.Contains(out, "allowed: entity:lock.front_door read") {
		t.Errorf("what-if check lacks its verdict line:\n%s", out)
	}

	out, err = runPolicyCLI(t, dir, "policy", "check", "entity:lock.front_door", "read", "--as", "default", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("what-if check --json: exit %d (%v)", cliExitCode(err), err)
	}
	m := decodeObject(t, out)
	wantField(t, "check/as", m, "what_if", true)
	wantField(t, "check/as", m, "selected_by", "what-if")
	wantField(t, "check/as", m, "profile", "default")
	wantField(t, "check/as", m, "rule", policy.RuleDefaultAllow)

	out, err = runPolicyCLI(t, dir, "policy", "check", "entity:lock.front_door", "read", "--json")
	if cliExitCode(err) != 1 {
		t.Fatalf("effective check after a what-if: exit %d, want 1 (agent denies)\n%s", cliExitCode(err), out)
	}
	m = decodeObject(t, out)
	wantField(t, "check/after-as", m, "profile", "agent")
	wantField(t, "check/after-as", m, "what_if", false)
	wantField(t, "check/after-as", m, "selected_by", "active")
}

// TestPolicyCheckContactsHomeAssistantOnlyForASelector: "It contacts Home
// Assistant only when a where: selector has to be evaluated." A question no
// narrowed allow matches sends zero requests.
func TestPolicyCheckContactsHomeAssistantOnlyForASelector(t *testing.T) {
	url, hits := countingHA(t)
	dir := policyInstance(t, url, policyManualExample)
	for _, q := range [][]string{
		{"entity:sensor.temp", "read"},
		{"entity:lock.front_door", "control"},
		{"automation:pg_heating", "edit"},
		{"file:configuration.yaml", "edit"},
	} {
		out, err := runPolicyCLI(t, dir, append([]string{"policy", "check"}, q...)...)
		if out == "" {
			t.Errorf("policy check %v printed no verdict (err %v)", q, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("policy check without a selector to evaluate sent %d request(s) to Home Assistant, want 0", n)
	}
}

// TestPolicyCheckSelectorWithoutHomeAssistantDoesNotGrant: D-55 / manual —
// Home Assistant unreachable: the narrowed allow does not match, the verdict
// (not a refusal) comes from the default, and a note says so.
func TestPolicyCheckSelectorWithoutHomeAssistantDoesNotGrant(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyManualExample)
	out, err := runPolicyCLI(t, dir, "policy", "check", "entity:light.wohnzimmer_decke", "control", "--json")
	if cliExitCode(err) != 1 {
		t.Fatalf("exit %d (%v), want 1 (denied)\n%s", cliExitCode(err), err, out)
	}
	if out == "" {
		t.Fatalf("an unreachable selector produced a refusal (empty stdout); the manual wants a verdict with a note")
	}
	m := decodeObject(t, out)
	wantField(t, "check/where-unreachable", m, "allowed", false)
	wantField(t, "check/where-unreachable", m, "rule", policy.RuleDefaultDeny)
	wantField(t, "check/where-unreachable", m, "line", 0)
	notes, _ := m["notes"].([]any)
	if len(notes) != 1 {
		t.Fatalf("notes = %v, want exactly one (the narrowed allow on entity:light.*)", m["notes"])
	}
	if s, _ := notes[0].(string); !strings.Contains(s, `"entity:light.*"`) {
		t.Errorf("note %q does not name the narrowed allow", s)
	}
}

// --- policy lint -----------------------------------------------------------------

// TestPolicyLintWithoutAFile: D-58 — nothing to report, exit 0.
func TestPolicyLintWithoutAFile(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), "")
	out, err := runPolicyCLI(t, dir, "policy", "lint", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("policy lint (no file): exit %d (%v), want 0\n%s", cliExitCode(err), err, out)
	}
	m := decodeObject(t, out)
	requireKeys(t, "lint/no-file", m, lintKeys...)
	wantField(t, "lint/no-file", m, "exists", false)
	wantField(t, "lint/no-file", m, "findings", []any{})
	wantField(t, "lint/no-file", m, "unchecked", []any{})
	wantField(t, "lint/no-file", m, "level", "A")
}

// TestPolicyLintReportsABrokenFile: D-60 — one `broken` finding per problem,
// with its line, file-level (no profile, no rule), exit 1, valid false.
func TestPolicyLintReportsABrokenFile(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyBroken)
	out, err := runPolicyCLI(t, dir, "policy", "lint", "--json")
	if cliExitCode(err) != 1 {
		t.Fatalf("policy lint (broken): exit %d, want 1\n%s", cliExitCode(err), out)
	}
	m := decodeObject(t, out)
	requireKeys(t, "lint/broken", m, lintKeys...)
	wantField(t, "lint/broken", m, "valid", false)
	wantField(t, "lint/broken", m, "file", filepath.Join(dir, policy.FileName))
	findings, _ := m["findings"].([]any)
	lines := make([]int, 0, len(findings))
	for _, f := range findings {
		fm, _ := f.(map[string]any)
		if fm["code"] != policy.CodeBroken {
			t.Errorf("finding %v on a broken file, want only %q (no live checks run)", fm, policy.CodeBroken)
		}
		if fm["profile"] != "" || fm["rule"] != "" {
			t.Errorf("broken finding %v carries a profile/rule; it is file-level", fm)
		}
		ln, _ := fm["line"].(float64)
		lines = append(lines, int(ln))
	}
	if !slices.Equal(lines, []int{2, 7, 9}) {
		t.Errorf("broken findings at lines %v, want [2 7 9] (one per problem, by line)", lines)
	}

	out, err = runPolicyCLI(t, dir, "policy", "lint")
	if cliExitCode(err) != 1 {
		t.Fatalf("policy lint (broken, prose): exit %d, want 1", cliExitCode(err))
	}
	if want := filepath.Join(dir, policy.FileName) + ":7: "; !strings.Contains(out, want) {
		t.Errorf("policy lint prose does not report %q:\n%s", want, out)
	}
}

// TestPolicyLintUncheckedKindsAndAllowPartial: template/helper/config_entry/
// file are never enumerated in phase 1 — listed as unchecked `not enumerated`,
// exit 1; --allow-partial accepts them (still listed), exit 0.
func TestPolicyLintUncheckedKindsAndAllowPartial(t *testing.T) {
	body := "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    deny:\n      \"file:**\": [write]\n"
	dir := policyInstance(t, closedHAURL(t), body)

	out, err := runPolicyCLI(t, dir, "policy", "lint", "--json")
	if cliExitCode(err) != 1 {
		t.Fatalf("lint with an unchecked kind: exit %d, want 1\n%s", cliExitCode(err), out)
	}
	m := decodeObject(t, out)
	wantField(t, "lint/unchecked", m, "valid", true)
	wantField(t, "lint/unchecked", m, "findings", []any{})
	wantField(t, "lint/unchecked", m, "unchecked", []map[string]any{{"kind": "file", "reason": "not enumerated", "profile": "", "rule": "", "line": 0}})

	out, err = runPolicyCLI(t, dir, "policy", "lint", "--allow-partial", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("lint --allow-partial: exit %d (%v), want 0\n%s", cliExitCode(err), err, out)
	}
	m = decodeObject(t, out)
	wantField(t, "lint/allow-partial", m, "unchecked", []map[string]any{{"kind": "file", "reason": "not enumerated", "profile": "", "rule": "", "line": 0}})

	out, err = runPolicyCLI(t, dir, "policy", "lint")
	if cliExitCode(err) != 1 {
		t.Fatalf("lint prose: exit %d, want 1", cliExitCode(err))
	}
	if !strings.Contains(out, "unchecked") || !strings.Contains(out, "file") {
		t.Errorf("lint prose does not list the unchecked kind:\n%s", out)
	}
}

// TestPolicyLintAllowPartialNeverHidesAFinding: a redundant-allow (a check
// that needs no live data) keeps exit 1 under --allow-partial.
func TestPolicyLintAllowPartialNeverHidesAFinding(t *testing.T) {
	body := "version: 1\nactive: agent\nprofiles:\n  agent:\n    allow:\n      \"file:packages/**\": [edit]\n"
	dir := policyInstance(t, closedHAURL(t), body)
	for _, args := range [][]string{{"policy", "lint", "--json"}, {"policy", "lint", "--allow-partial", "--json"}} {
		out, err := runPolicyCLI(t, dir, args...)
		if cliExitCode(err) != 1 {
			t.Errorf("%v: exit %d, want 1 (a finding)", args, cliExitCode(err))
		}
		m := decodeObject(t, out)
		findings, _ := m["findings"].([]any)
		if len(findings) != 1 {
			t.Fatalf("%v: findings %v, want the one redundant-allow", args, m["findings"])
		}
		f, _ := findings[0].(map[string]any)
		want := map[string]any{"code": policy.CodeRedundantAllow, "profile": "agent",
			"rule": `allow "file:packages/**" [edit]`, "line": float64(6)}
		for k, v := range want {
			if f[k] != v {
				t.Errorf("%v: finding %s = %v, want %v", args, k, f[k], v)
			}
		}
		if s, _ := f["message"].(string); s == "" {
			t.Errorf("%v: finding has no message", args)
		}
	}

	out, err := runPolicyCLI(t, dir, "policy", "lint")
	if cliExitCode(err) != 1 {
		t.Fatalf("lint prose: exit %d, want 1", cliExitCode(err))
	}
	for _, want := range []string{policy.CodeRedundantAllow, "agent", `allow "file:packages/**" [edit]`, "6"} {
		if !strings.Contains(out, want) {
			t.Errorf("lint prose does not name %q:\n%s", want, out)
		}
	}
}

// TestPolicyLintSystemIsItsFixedSet: "system — its fixed set": lint checks
// system rules without Home Assistant, so an unreachable instance leaves
// nothing unchecked, and an allow every live match of which is denied is
// reported unreachable.
func TestPolicyLintSystemIsItsFixedSet(t *testing.T) {
	body := "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    allow:\n      \"system:reload\": [control]\n    deny:\n      \"system:re*\": [control]\n"
	dir := policyInstance(t, closedHAURL(t), body)
	out, err := runPolicyCLI(t, dir, "policy", "lint", "--json")
	if cliExitCode(err) != 1 {
		t.Fatalf("exit %d, want 1 (an unreachable finding)\n%s", cliExitCode(err), out)
	}
	m := decodeObject(t, out)
	wantField(t, "lint/system", m, "unchecked", []any{})
	findings, _ := m["findings"].([]any)
	if len(findings) != 1 {
		t.Fatalf("findings %v, want one unreachable", m["findings"])
	}
	f, _ := findings[0].(map[string]any)
	if f["code"] != policy.CodeUnreachable || f["rule"] != `allow "system:reload" [control]` || f["line"] != float64(7) {
		t.Errorf("finding = %v, want unreachable on allow \"system:reload\" [control] line 7", f)
	}
}

// --- D-62: no call-time profile selector ----------------------------------------

func walkCommands(c *cobra.Command, fn func(*cobra.Command)) {
	fn(c)
	for _, sub := range c.Commands() {
		walkCommands(sub, fn)
	}
}

func hasFlag(c *cobra.Command, name string) bool {
	return c.Flags().Lookup(name) != nil || c.PersistentFlags().Lookup(name) != nil ||
		c.InheritedFlags().Lookup(name) != nil
}

// TestNoCallTimeProfileSelectorExists: D-62 — no --profile flag on any
// command in the tree (mcp included), and --as exists exactly on `policy show`
// and `policy check`.
func TestNoCallTimeProfileSelectorExists(t *testing.T) {
	var all, withProfile, withAs []string
	walkCommands(rootCmd, func(c *cobra.Command) {
		all = append(all, c.CommandPath())
		if hasFlag(c, "profile") {
			withProfile = append(withProfile, c.CommandPath())
		}
		if hasFlag(c, "as") {
			withAs = append(withAs, c.CommandPath())
		}
	})
	if len(all) < 50 {
		t.Fatalf("walked %d commands, want the whole tree", len(all))
	}
	for _, want := range []string{"hactl policy show", "hactl policy check", "hactl policy lint", "hactl mcp"} {
		if !slices.Contains(all, want) {
			t.Errorf("command %q is not in the tree", want)
		}
	}
	if len(withProfile) != 0 {
		t.Errorf("commands offering --profile (D-62 forbids a call-time selector): %v", withProfile)
	}
	slices.Sort(withAs)
	if !slices.Equal(withAs, []string{"hactl policy check", "hactl policy show"}) {
		t.Errorf("--as is offered on %v, want exactly [hactl policy check hactl policy show]", withAs)
	}
}

// TestProfileFlagIsRefused: passing --profile is an unknown flag, not a
// silently ignored one (H-25) — exit 1, nothing on stdout.
func TestProfileFlagIsRefused(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyManualExample)
	for _, args := range [][]string{
		{"policy", "show", "--profile", "default"},
		{"policy", "check", "entity:lock.front_door", "read", "--profile", "default"},
	} {
		out, err := runPolicyCLI(t, dir, args...)
		if err == nil {
			t.Errorf("%v succeeded; --profile must not exist (D-62)", args)
		}
		if out != "" {
			t.Errorf("%v wrote to stdout: %q", args, out)
		}
	}
}

// TestHACTLProfileEnvironmentHasNoEffect: D-62 — HACTL_PROFILE does not
// select a profile for show or check.
func TestHACTLProfileEnvironmentHasNoEffect(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyManualExample)
	t.Setenv("HACTL_PROFILE", "default")

	out, err := runPolicyCLI(t, dir, "policy", "show", "--json")
	if cliExitCode(err) != 0 {
		t.Fatalf("policy show --json: exit %d (%v)\n%s", cliExitCode(err), err, out)
	}
	m := decodeObject(t, out)
	wantField(t, "show/env", m, "profile", "agent")
	wantField(t, "show/env", m, "selected_by", "active")
	wantField(t, "show/env", m, "what_if", false)

	// Under `default`, read on a lock is allowed; under the file's `agent`
	// it is denied — the verdict must be agent's.
	out, err = runPolicyCLI(t, dir, "policy", "check", "entity:lock.front_door", "read", "--json")
	if cliExitCode(err) != 1 {
		t.Fatalf("check under HACTL_PROFILE=default: exit %d, want 1 (agent denies)\n%s", cliExitCode(err), out)
	}
	m = decodeObject(t, out)
	wantField(t, "check/env", m, "profile", "agent")
	wantField(t, "check/env", m, "selected_by", "active")
}

// --- phase 1 scope ----------------------------------------------------------------

// TestPolicyIsNotEnforcedYet: "In this version no command other than policy
// show|check|lint reads the file" — a profile that denies everything changes
// nothing about a read such as `area ls`.
func TestPolicyIsNotEnforcedYet(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/area_registry/list": []any{map[string]any{"area_id": "wohnzimmer", "name": "Wohnzimmer"}},
	}, nil)
	deny := "version: 1\nactive: locked\nprofiles:\n  locked:\n    default: deny\n    deny:\n      \"*\": [read, write]\n"
	if err := os.WriteFile(filepath.Join(ts.dir, policy.FileName), []byte(deny), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := runPolicyCLI(t, ts.dir, "area", "ls")
	if err != nil {
		t.Fatalf("area ls under a deny-all policy failed: %v — phase 1 enforces nothing", err)
	}
	if !strings.Contains(out, "wohnzimmer") {
		t.Errorf("area ls under a deny-all policy lost its row:\n%s", out)
	}
}

// TestPolicyFamilyHasNoExplainYet: `policy explain` is phase 2+; the family
// refuses it like any unknown subcommand (H-22).
func TestPolicyFamilyHasNoExplainYet(t *testing.T) {
	dir := policyInstance(t, closedHAURL(t), policyManualExample)
	out, err := runPolicyCLI(t, dir, "policy", "explain", "area", "ls")
	if err == nil {
		t.Errorf("policy explain succeeded; it is not part of phase 1:\n%s", out)
	}
	if out != "" {
		t.Errorf("policy explain wrote to stdout: %q", out)
	}
}
