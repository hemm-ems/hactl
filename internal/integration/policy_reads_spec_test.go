//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// Permission policy W-2: reads are enforced. Written from the contract only —
// docs/manual.md "Enforcement (reads)" and "**Writes and reads are
// enforced.**", docs/decisions.md D-54, D-58, D-66, D-138..D-147, and
// SPEC-policy-usp.md "W-2 contract" — before the enforcement existed.
//
// The truth every expectation is measured against is read from Home Assistant
// outside hactl (raw REST and websocket, policy_enforce_helpers_test.go), so
// a filter hactl installs in its own transports can neither hide the rows the
// test counts nor refuse the test's own setup (H-12).
//
// stderr is part of the contract here (the `hidden by policy` trailer moves to
// stderr under --json, D-139; a refusal's summary line is on stderr, D-119),
// and the in-process entry point captures stdout only — so these tests build
// the real binary and run it the way a caller does.
// ============================================================================

// pgReadsTrailerRe is the trailer line (D-139): `hidden by policy: N[,
// masked: M] (profile p)`.
var pgReadsTrailerRe = regexp.MustCompile(`^hidden by policy: (\d+)(?:, masked: (\d+))? \(profile ([^)]*)\)$`)

// pgReadsSummaryRe is a refusal's stderr summary line (D-119); a request-layer
// refusal may append its own suffix.
var pgReadsSummaryRe = regexp.MustCompile(`^policy_denied: \d+ of \d+ resources denied \(profile ` + regexp.QuoteMeta(pgProfile) + `\)`)

// pgReadsRun is one run of the real binary.
type pgReadsRun struct {
	args   []string
	stdout string
	stderr string
	code   int
}

func (r pgReadsRun) String() string {
	return fmt.Sprintf("hactl %s → exit %d\n--- stdout ---\n%s\n--- stderr ---\n%s", strings.Join(r.args, " "), r.code, r.stdout, r.stderr)
}

// pgReadsBinary builds cmd/hactl into the test's temp dir. Go's build cache
// makes every build after the first one a link.
func pgReadsBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "hactl")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/hactl") //nolint:gosec // G204: fixed go CLI, the output path is the test's own temp dir
	build.Dir = filepath.Join("..", "..")                                      // the module root, from internal/integration
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building hactl: %v\n%s", err, out)
	}
	return bin
}

// pgReadsExec runs the binary against the instance directory dir, with the
// manual injection off (it would put the manual on stderr) and nothing from
// the caller's own HACTL_* environment.
func pgReadsExec(t *testing.T, bin, dir string, args ...string) pgReadsRun {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	c := exec.CommandContext(ctx, bin, args...) //nolint:gosec // G204: the binary this test built, test-owned arguments
	env := make([]string, 0, len(os.Environ())+3)
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "HACTL_") {
			env = append(env, kv)
		}
	}
	env = append(env, "HACTL_DIR="+dir, "HACTL_MANUAL_MODE=off", "HACTL_LOG_LEVEL=error")
	c.Env = env
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	err := c.Run()
	code := 0
	if err != nil {
		var ee *exec.ExitError
		if !errors.As(err, &ee) {
			t.Fatalf("running hactl %v: %v", args, err)
		}
		code = ee.ExitCode()
	}
	return pgReadsRun{args: args, stdout: stdout.String(), stderr: stderr.String(), code: code}
}

// pgReadsOK runs the binary and fails the test unless it exits 0.
func pgReadsOK(t *testing.T, bin, dir string, args ...string) pgReadsRun {
	t.Helper()
	r := pgReadsExec(t, bin, dir, args...)
	if r.code != 0 {
		t.Fatalf("expected exit 0:\n%s", r)
	}
	return r
}

// pgReadsLines is s split into lines with trailing white space and empty
// lines removed.
func pgReadsLines(s string) []string {
	var out []string
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimRight(l, " \t\r"); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// pgReadsTrailers returns every line of s that has the trailer's form.
func pgReadsTrailers(s string) []string {
	var out []string
	for _, l := range pgReadsLines(s) {
		if pgReadsTrailerRe.MatchString(strings.TrimSpace(l)) {
			out = append(out, strings.TrimSpace(l))
		}
	}
	return out
}

func pgReadsTrailer(hidden, masked int) string {
	if masked == 0 {
		return fmt.Sprintf("hidden by policy: %d (profile %s)", hidden, pgProfile)
	}
	return fmt.Sprintf("hidden by policy: %d, masked: %d (profile %s)", hidden, masked, pgProfile)
}

// assertProseTrailer: prose output ends with exactly want as its last line,
// and carries no other trailer (D-139).
func assertProseTrailer(t *testing.T, r pgReadsRun, want string) {
	t.Helper()
	lines := pgReadsLines(r.stdout)
	if len(lines) == 0 || strings.TrimSpace(lines[len(lines)-1]) != want {
		t.Errorf("prose output does not end with %q (D-139: the trailer is the last stdout line):\n%s", want, r)
	}
	if got := pgReadsTrailers(r.stdout); len(got) != 1 {
		t.Errorf("stdout carries %d trailer lines %v, want exactly one:\n%s", len(got), got, r)
	}
}

// assertJSONTrailer: under --json the trailer is on stderr, and stdout holds
// no trailer (the JSON keeps its shape, D-139).
func assertJSONTrailer(t *testing.T, r pgReadsRun, want string) {
	t.Helper()
	if got := pgReadsTrailers(r.stderr); !slices.Contains(got, want) {
		t.Errorf("--json: stderr trailer lines %v, want %q (D-139: the line goes to stderr under --json):\n%s", got, want, r)
	}
	if strings.Contains(r.stdout, "hidden by policy") {
		t.Errorf("--json: stdout carries the trailer — the JSON must keep its shape (D-139):\n%s", r)
	}
}

// assertNoPolicyTraces: nothing of read enforcement on either stream.
func assertNoPolicyTraces(t *testing.T, r pgReadsRun) {
	t.Helper()
	for _, s := range []string{"hidden by policy", "<denied>"} {
		if strings.Contains(r.stdout, s) || strings.Contains(r.stderr, s) {
			t.Errorf("output carries %q although no profile is in effect (D-58):\n%s", s, r)
		}
	}
	// The class at the start of a line — the fixture's script.pg_policy_denied
	// is an ordinary row.
	if pgReadsClassRe.MatchString(r.stdout) || pgReadsClassRe.MatchString(r.stderr) {
		t.Errorf("output carries a policy_denied refusal although no profile is in effect (D-58):\n%s", r)
	}
}

var pgReadsClassRe = regexp.MustCompile(`(?m)^\s*policy_denied`)

// pgReadsRefusal is the --json refusal of a read: the refusal envelope of a
// write (manual: "refused like a denied write"). Only the fields the contract
// fixes for a read are decoded.
type pgReadsRefusal struct {
	OK      *bool  `json:"ok"`
	Error   string `json:"error"`
	Hint    string `json:"hint"`
	Details struct {
		Policy *struct {
			Denied     []pgDenied `json:"denied"`
			Profile    string     `json:"profile"`
			SelectedBy string     `json:"selected_by"`
		} `json:"policy"`
	} `json:"details"`
}

// assertReadRefused runs args under dir in text and --json and checks the
// refusal of a read (manual "Enforcement (reads)": exit 1, policy_denied, the
// rule and its line). When ruleKnown is false only the resource and verb are
// checked — the contract does not fix which rule a refusal names there.
// leaks are strings no output path may carry (D-138).
func assertReadRefused(t *testing.T, bin, dir string, args []string, want pgDenied, ruleKnown bool, leaks ...string) {
	t.Helper()

	// --- text ---
	r := pgReadsExec(t, bin, dir, args...)
	if r.code != 1 {
		t.Errorf("a denied read must be refused with exit 1:\n%s", r)
	}
	wantLine := "denied: " + want.Verb + " " + want.Resource + " — "
	if ruleKnown {
		wantLine = want.textLine()
	}
	if !strings.Contains(r.stdout, wantLine) {
		t.Errorf("the refusal report on stdout misses %q:\n%s", wantLine, r)
	}
	if !strings.Contains(r.stdout, "policy_denied") {
		t.Errorf("the refusal report on stdout does not name the class policy_denied:\n%s", r)
	}
	if first := strings.SplitN(r.stderr, "\n", 2)[0]; !pgReadsSummaryRe.MatchString(first) {
		t.Errorf("stderr's first line %q is not the summary `policy_denied: <m> of <n> resources denied (profile %s)` (D-119):\n%s",
			first, pgProfile, r)
	}
	pgReadsAssertNoLeak(t, r, leaks...)

	// --- --json ---
	jr := pgReadsExec(t, bin, dir, append(slices.Clone(args), "--json")...)
	if jr.code != 1 {
		t.Errorf("--json: a denied read must be refused with exit 1:\n%s", jr)
	}
	pgReadsAssertNoLeak(t, jr, leaks...)
	var ref pgReadsRefusal
	if err := json.Unmarshal([]byte(jr.stdout), &ref); err != nil {
		t.Errorf("--json: the refusal is not one JSON object: %v\n%s", err, jr)
		return
	}
	if ref.Error != "policy_denied" {
		t.Errorf("--json: error = %q, want policy_denied:\n%s", ref.Error, jr)
	}
	if ref.OK == nil || *ref.OK {
		t.Errorf("--json: ok = %v, want false:\n%s", ref.OK, jr)
	}
	if strings.Contains(ref.Hint, "--confirm") {
		t.Errorf("--json: the hint suggests --confirm on a policy refusal: %q", ref.Hint)
	}
	p := ref.Details.Policy
	if p == nil {
		t.Errorf("--json: no details.policy in the refusal:\n%s", jr)
		return
	}
	if p.Profile != pgProfile || p.SelectedBy != "active" {
		t.Errorf("--json: details.policy profile %q selected_by %q, want %q / active", p.Profile, p.SelectedBy, pgProfile)
	}
	found := false
	for _, d := range p.Denied {
		if d.Resource != want.Resource || d.Verb != want.Verb {
			continue
		}
		if ruleKnown && (d.Rule != want.Rule || d.Line != want.Line) {
			t.Errorf("--json: %s %s is denied by %q (line %d), want %q (line %d)", d.Verb, d.Resource, d.Rule, d.Line, want.Rule, want.Line)
		}
		found = true
	}
	if !found {
		t.Errorf("--json: details.policy.denied = %v, want an entry for %s %s", p.Denied, want.Verb, want.Resource)
	}
}

// pgReadsAssertNoLeak: no output path of a run carries any of leaks (D-138).
func pgReadsAssertNoLeak(t *testing.T, r pgReadsRun, leaks ...string) {
	t.Helper()
	for _, l := range leaks {
		if l == "" {
			continue
		}
		if strings.Contains(r.stdout, l) || strings.Contains(r.stderr, l) {
			t.Errorf("a denied value %q reached the output (D-138: no output path may carry it):\n%s", l, r)
		}
	}
}

// pgRawStates is HA's /api/states, read outside hactl.
func pgRawStates(t *testing.T, inst *hatest.Instance) []map[string]any {
	t.Helper()
	status, body := pgHTTP(t, inst, http.MethodGet, "/api/states", nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/states: %d %s", status, body)
	}
	var states []map[string]any
	if err := json.Unmarshal(body, &states); err != nil {
		t.Fatalf("/api/states did not parse: %v", err)
	}
	return states
}

// pgRawLockIDs is every lock entity HA has a state for, sorted.
func pgRawLockIDs(t *testing.T, inst *hatest.Instance) []string {
	t.Helper()
	var locks []string
	for _, s := range pgRawStates(t, inst) {
		if id, _ := s["entity_id"].(string); strings.HasPrefix(id, "lock.") {
			locks = append(locks, id)
		}
	}
	sort.Strings(locks)
	if !slices.Contains(locks, "lock.pg_policy_lock") {
		t.Fatalf("policy rig: HA has no lock.pg_policy_lock among its locks %v — the fixture changed", locks)
	}
	return locks
}

// pgReadsJSONArray decodes a --json listing as an array of objects.
func pgReadsJSONArray(t *testing.T, r pgReadsRun) []map[string]any {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(r.stdout), &rows); err != nil {
		t.Fatalf("--json listing is not a JSON array of objects: %v\n%s", err, r)
	}
	return rows
}

func pgReadsRowKeys(row map[string]any) []string {
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// pgReadsEntityProfile is case 1's profile: everything an entity listing
// reads is readable — the registries it joins included, so the only rows
// hidden are the locks — except every lock.
const pgReadsEntityProfile = `version: 1
active: pg_agent
profiles:
  pg_agent:
    default: deny
    allow:
      "entity:*": [read]
      "device:*": [read]
      "area:*": [read]
      "floor:*": [read]
      "label:*": [read]
    deny:
      "entity:lock.*": [read]
`

// ---------------------------------------------------------------------------
// Case 1 — a listing drops the rows it may not read and says how many.
// ---------------------------------------------------------------------------

// TestPolicyReadsSpecListingHidesDeniedRows — `ent ls` under a profile that
// may read every entity except the locks: no lock row, the prose ends with
// `hidden by policy: N (profile p)` where N is the number of locks Home
// Assistant really has; `--json` keeps its shape, has no lock row, and puts
// the same line on stderr (D-54, D-139).
func TestPolicyReadsSpecListingHidesDeniedRows(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	bin := pgReadsBinary(t)
	locks := pgRawLockIDs(t, inst)
	plain := pgInstanceDir(t, inst.URL(), inst.Token(), "")
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), pgReadsEntityProfile)

	// Without a policy the locks are listed — the filtered run below would
	// otherwise prove nothing.
	base := pgReadsOK(t, bin, plain, "ent", "ls", "--full")
	for _, l := range locks {
		if !strings.Contains(base.stdout, l) {
			t.Fatalf("without a policy `ent ls --full` does not list %s — the case is inert:\n%s", l, base)
		}
	}
	baseJSON := pgReadsJSONArray(t, pgReadsOK(t, bin, plain, "ent", "ls", "--json"))
	if len(baseJSON) == 0 {
		t.Fatal("without a policy `ent ls --json` is empty — the case is inert")
	}

	// --- prose ---
	r := pgReadsOK(t, bin, dir, "ent", "ls", "--full")
	for _, l := range locks {
		if strings.Contains(r.stdout, l) || strings.Contains(r.stderr, l) {
			t.Errorf("`ent ls` under a profile denying read on lock.* still shows %s:\n%s", l, r)
		}
	}
	if !strings.Contains(r.stdout, "light.") {
		t.Errorf("`ent ls` under the profile lists no light — it hid more than the locks:\n%s", r)
	}
	assertProseTrailer(t, r, pgReadsTrailer(len(locks), 0))

	// --- --json ---
	jr := pgReadsOK(t, bin, dir, "ent", "ls", "--json")
	rows := pgReadsJSONArray(t, jr)
	if len(rows) == 0 {
		t.Fatalf("`ent ls --json` under the profile is empty:\n%s", jr)
	}
	if got, want := pgReadsRowKeys(rows[0]), pgReadsRowKeys(baseJSON[0]); !slices.Equal(got, want) {
		t.Errorf("`ent ls --json` row keys under the profile %v, without a policy %v — the JSON must keep its shape (D-139)", got, want)
	}
	for _, row := range rows {
		if id, _ := row["entity_id"].(string); strings.HasPrefix(id, "lock.") {
			t.Errorf("`ent ls --json` under the profile still holds the row %s", id)
		}
	}
	if len(rows) != len(baseJSON)-len(locks) {
		t.Errorf("`ent ls --json` holds %d rows under the profile, %d without — want exactly the %d locks fewer", len(rows), len(baseJSON), len(locks))
	}
	assertJSONTrailer(t, jr, pgReadsTrailer(len(locks), 0))
}

// ---------------------------------------------------------------------------
// Case 2 — reading one denied thing is refused like a denied write.
// ---------------------------------------------------------------------------

// TestPolicyReadsSpecSingleDeniedReadIsRefused — `ent show lock.pg_policy_lock`
// under the case-1 profile: exit 1, policy_denied, the denied list names
// `read entity:lock.pg_policy_lock` with the rule and its line, and nothing of
// the lock's state reaches either stream.
func TestPolicyReadsSpecSingleDeniedReadIsRefused(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	bin := pgReadsBinary(t)
	const lock = "lock.pg_policy_lock"
	raw := pgStateOf(t, inst, lock)
	name, _ := raw.Attributes["friendly_name"].(string)
	if name == "" {
		t.Fatalf("policy rig: %s has no friendly_name — the leak check would be inert", lock)
	}
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), pgReadsEntityProfile)

	assertReadRefused(t, bin, dir, []string{"ent", "show", lock}, pgDenied{
		Resource: "entity:" + lock, Verb: "read",
		Rule: `deny "entity:lock.*" [read]`,
		Line: policyLineOf(t, pgReadsEntityProfile, `"entity:lock.*"`),
	}, true, name)
}

// ---------------------------------------------------------------------------
// Cases 3 and 4 — a denied part is masked, not dropped.
// ---------------------------------------------------------------------------

// TestPolicyReadsSpecDeniedAttributeIsMasked — with entity read allowed and
// `deny "entity:light.*/attr/brightness": [read]`, `ent show <light>` prints
// `<denied>` for brightness in prose and in --json (the key kept), every other
// attribute as Home Assistant has it, and the trailer counts one masked part
// (D-140).
func TestPolicyReadsSpecDeniedAttributeIsMasked(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	bin := pgReadsBinary(t)
	rig := buildPGRig(t, inst)
	light := rig.Lights[0]
	pgCallService(t, inst, "light", "turn_on", map[string]any{"entity_id": light, "brightness": 123})
	raw := pgStateOf(t, inst, light)
	if b, ok := raw.Attributes["brightness"]; !ok || b == nil {
		t.Fatalf("policy rig: %s has no brightness after turn_on (attributes %v) — the mask would be inert", light, raw.Attributes)
	}

	policy := `version: 1
active: pg_agent
profiles:
  pg_agent:
    allow:
      "entity:*": [read]
    deny:
      "entity:light.*/attr/brightness": [read]
`
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
	plain := pgInstanceDir(t, inst.URL(), inst.Token(), "")

	// --- prose ---
	r := pgReadsOK(t, bin, dir, "ent", "show", light, "--full")
	brightLine := regexp.MustCompile(`(?m)^brightness:\s+(.*)$`)
	if m := brightLine.FindStringSubmatch(r.stdout); m == nil || strings.TrimSpace(m[1]) != "<denied>" {
		t.Errorf("`ent show %s --full`: brightness line %v, want `brightness: <denied>` (D-140):\n%s", light, m, r)
	}
	assertProseTrailer(t, r, pgReadsTrailer(0, 1))

	// --- --json ---
	jr := pgReadsOK(t, bin, dir, "ent", "show", light, "--json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(jr.stdout), &doc); err != nil {
		t.Fatalf("`ent show --json` is not one JSON object: %v\n%s", err, jr)
	}
	attrs, ok := doc["attributes"].(map[string]any)
	if !ok {
		t.Fatalf("`ent show --json` has no attributes object:\n%s", jr)
	}
	if v, present := attrs["brightness"]; !present || v != "<denied>" {
		t.Errorf("`ent show --json`: attributes.brightness = %v (present %v), want the key kept with \"<denied>\" (D-140)", v, present)
	}
	rawAttrs := pgStateOf(t, inst, light).Attributes
	for k, want := range rawAttrs {
		if k == "brightness" {
			continue
		}
		if got, present := attrs[k]; !present || !reflect.DeepEqual(got, want) {
			t.Errorf("`ent show --json`: attribute %s = %v (present %v), Home Assistant has %v — only the denied part may change", k, got, present, want)
		}
	}
	if len(attrs) != len(rawAttrs) {
		t.Errorf("`ent show --json` has %d attributes, Home Assistant %d — a masked attribute keeps its key, nothing is added", len(attrs), len(rawAttrs))
	}
	if doc["state"] != raw.State {
		t.Errorf("`ent show --json`: state %v, Home Assistant %q — the state is not denied here", doc["state"], raw.State)
	}
	base := pgReadsOK(t, bin, plain, "ent", "show", light, "--json")
	var baseDoc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(base.stdout), &baseDoc); err != nil {
		t.Fatalf("`ent show --json` without a policy: %v\n%s", err, base)
	}
	var gotDoc map[string]json.RawMessage
	_ = json.Unmarshal([]byte(jr.stdout), &gotDoc)
	if got, want := sortedKeys(gotDoc), sortedKeys(baseDoc); !slices.Equal(got, want) {
		t.Errorf("`ent show --json` keys under the profile %v, without a policy %v — the shape must not change", got, want)
	}
	assertJSONTrailer(t, jr, pgReadsTrailer(0, 1))
}

// TestPolicyReadsSpecDeniedStateIsMasked — `deny
// "entity:input_boolean.pg_policy_lock_state/state": [read]`: the state reads
// `<denied>` in `ent show` (prose and --json) and in an `ent ls --json` row,
// and the trailer counts it masked (D-140).
func TestPolicyReadsSpecDeniedStateIsMasked(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	bin := pgReadsBinary(t)
	const ib = "input_boolean.pg_policy_lock_state"
	raw := pgStateOf(t, inst, ib)
	if raw.State != "on" && raw.State != "off" {
		t.Fatalf("policy rig: %s is %q, want on or off", ib, raw.State)
	}

	policy := `version: 1
active: pg_agent
profiles:
  pg_agent:
    deny:
      "entity:input_boolean.pg_policy_lock_state/state": [read]
`
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)

	r := pgReadsOK(t, bin, dir, "ent", "show", ib)
	stateLine := regexp.MustCompile(`(?m)^state:\s+(.*)$`)
	if m := stateLine.FindStringSubmatch(r.stdout); m == nil || strings.TrimSpace(m[1]) != "<denied>" {
		t.Errorf("`ent show %s`: state line %v, want `state: <denied>` (D-140):\n%s", ib, m, r)
	}
	assertProseTrailer(t, r, pgReadsTrailer(0, 1))

	jr := pgReadsOK(t, bin, dir, "ent", "show", ib, "--json")
	var doc map[string]any
	if err := json.Unmarshal([]byte(jr.stdout), &doc); err != nil {
		t.Fatalf("`ent show --json` is not one JSON object: %v\n%s", err, jr)
	}
	if v, present := doc["state"]; !present || v != "<denied>" {
		t.Errorf("`ent show --json`: state = %v (present %v), want \"<denied>\" (D-140)", v, present)
	}
	if attrs, _ := doc["attributes"].(map[string]any); !reflect.DeepEqual(attrs, raw.Attributes) {
		t.Errorf("`ent show --json`: attributes %v, Home Assistant %v — only the state is denied", attrs, raw.Attributes)
	}
	assertJSONTrailer(t, jr, pgReadsTrailer(0, 1))

	lr := pgReadsOK(t, bin, dir, "ent", "ls", "--pattern", ib, "--json")
	rows := pgReadsJSONArray(t, lr)
	if len(rows) != 1 {
		t.Fatalf("`ent ls --pattern %s --json`: %d rows, want 1 — a masked part keeps its row:\n%s", ib, len(rows), lr)
	}
	if v := rows[0]["state"]; v != "<denied>" {
		t.Errorf("`ent ls --json`: %s state = %v, want \"<denied>\"", ib, v)
	}
	assertJSONTrailer(t, lr, pgReadsTrailer(0, 1))
}

// ---------------------------------------------------------------------------
// Case 5 — no profile, no change.
// ---------------------------------------------------------------------------

// TestPolicyReadsSpecNoProfileChangesNothing — without a profile in effect (no
// policy.yaml, and a file that selects no profile) the reads are what they
// were: every lock listed, brightness a number, no trailer and no `<denied>`
// on either stream (D-58, D-74).
func TestPolicyReadsSpecNoProfileChangesNothing(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	bin := pgReadsBinary(t)
	rig := buildPGRig(t, inst)
	light := rig.Lights[0]
	pgCallService(t, inst, "light", "turn_on", map[string]any{"entity_id": light, "brightness": 77})
	locks := pgRawLockIDs(t, inst)

	// No `active:` and no `default` profile: implicit allow-all — a profile is
	// declared that would hide every lock, but none is in effect.
	implicit := `version: 1
profiles:
  other:
    deny:
      "entity:lock.*": [read]
      "entity:light.*/attr/brightness": [read]
`
	for name, dir := range map[string]string{
		"no policy file":     pgInstanceDir(t, inst.URL(), inst.Token(), ""),
		"implicit allow-all": pgInstanceDir(t, inst.URL(), inst.Token(), implicit),
	} {
		t.Run(name, func(t *testing.T) {
			ls := pgReadsOK(t, bin, dir, "ent", "ls", "--full")
			assertNoPolicyTraces(t, ls)
			for _, l := range locks {
				if !strings.Contains(ls.stdout, l) {
					t.Errorf("`ent ls --full` misses %s with no profile in effect:\n%s", l, ls)
				}
			}

			js := pgReadsOK(t, bin, dir, "ent", "ls", "--json")
			assertNoPolicyTraces(t, js)
			ids := map[string]bool{}
			for _, row := range pgReadsJSONArray(t, js) {
				id, _ := row["entity_id"].(string)
				ids[id] = true
			}
			for _, l := range locks {
				if !ids[l] {
					t.Errorf("`ent ls --json` misses %s with no profile in effect", l)
				}
			}

			show := pgReadsOK(t, bin, dir, "ent", "show", light, "--full")
			assertNoPolicyTraces(t, show)
			showJSON := pgReadsOK(t, bin, dir, "ent", "show", light, "--json")
			assertNoPolicyTraces(t, showJSON)
			var doc struct {
				Attributes map[string]any `json:"attributes"`
			}
			if err := json.Unmarshal([]byte(showJSON.stdout), &doc); err != nil {
				t.Fatalf("`ent show --json`: %v\n%s", err, showJSON)
			}
			if want := pgStateOf(t, inst, light).Attributes["brightness"]; !reflect.DeepEqual(doc.Attributes["brightness"], want) {
				t.Errorf("`ent show --json` brightness %v, Home Assistant %v", doc.Attributes["brightness"], want)
			}

			lockShow := pgReadsOK(t, bin, dir, "ent", "show", "lock.pg_policy_lock")
			assertNoPolicyTraces(t, lockShow)
		})
	}
}

// ---------------------------------------------------------------------------
// Case 6 — `tpl eval` needs `read system:template` while any read is denied.
// ---------------------------------------------------------------------------

// TestPolicyReadsSpecTplEvalNeedsSystemTemplate — a template can read any
// state, so while the profile denies some read `tpl eval` is refused unless
// `"system:template": [read]` is allowed (D-143, M-3); allowed, it runs.
func TestPolicyReadsSpecTplEvalNeedsSystemTemplate(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	bin := pgReadsBinary(t)
	const tpl = "{{ states('lock.pg_policy_lock') }}"
	args := []string{"tpl", "eval", tpl}

	t.Run("default deny", func(t *testing.T) {
		dir := pgInstanceDir(t, inst.URL(), inst.Token(), pgReadsEntityProfile)
		assertReadRefused(t, bin, dir, args, pgDenied{
			Resource: "system:template", Verb: "read", Rule: "default: deny",
		}, true)
	})

	t.Run("default allow", func(t *testing.T) {
		// Nothing denies system:template by name and the default allows: the
		// read deny elsewhere is what makes the template need its own allow.
		policy := `version: 1
active: pg_agent
profiles:
  pg_agent:
    deny:
      "entity:lock.*": [read]
`
		dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
		assertReadRefused(t, bin, dir, args, pgDenied{Resource: "system:template", Verb: "read"}, false)
	})

	t.Run("allowed", func(t *testing.T) {
		policy := strings.Replace(pgReadsEntityProfile, `      "label:*": [read]`+"\n",
			`      "label:*": [read]`+"\n"+`      "system:template": [read]`+"\n", 1)
		if !strings.Contains(policy, `"system:template"`) {
			t.Fatal("test bug: the system:template allow was not inserted")
		}
		dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
		r := pgReadsExec(t, bin, dir, args...)
		if r.code != 0 {
			t.Fatalf("`tpl eval` with \"system:template\": [read] allowed must run:\n%s", r)
		}
		want := pgStateOf(t, inst, "lock.pg_policy_lock").State
		if got := strings.TrimSpace(r.stdout); got != want {
			t.Errorf("`tpl eval %s` = %q, Home Assistant renders %q — the owner allowed the template", tpl, got, want)
		}
	})
}

// ---------------------------------------------------------------------------
// Case 7 — `log` needs `read system:logs`.
// ---------------------------------------------------------------------------

// TestPolicyReadsSpecLogNeedsSystemLogs — `log` under `deny "system:logs":
// [read]` is refused naming `read system:logs` and the rule (D-142).
func TestPolicyReadsSpecLogNeedsSystemLogs(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	bin := pgReadsBinary(t)
	policy := `version: 1
active: pg_agent
profiles:
  pg_agent:
    deny:
      "system:logs": [read]
`
	plain := pgInstanceDir(t, inst.URL(), inst.Token(), "")
	pgReadsOK(t, bin, plain, "log") // the command itself works on this instance

	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
	assertReadRefused(t, bin, dir, []string{"log"}, pgDenied{
		Resource: "system:logs", Verb: "read",
		Rule: `deny "system:logs" [read]`,
		Line: policyLineOf(t, policy, `"system:logs"`),
	}, true)
}

// ---------------------------------------------------------------------------
// Case 8 — registry listings.
// ---------------------------------------------------------------------------

// TestPolicyReadsSpecAreaListingHidesDeniedArea — `area ls` under `deny
// "area:<a>": [read]` drops that area, keeps the others, and prints the
// trailer (prose) or puts it on stderr (--json).
func TestPolicyReadsSpecAreaListingHidesDeniedArea(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	bin := pgReadsBinary(t)
	ws := dialRawWS(t, inst.URL(), inst.Token())
	hidden := pgCreateArea(t, ws, "PG Policy Read Hidden")
	shown := pgCreateArea(t, ws, "PG Policy Read Shown")
	t.Cleanup(func() {
		cleanup := dialRawWS(t, inst.URL(), inst.Token())
		for _, id := range []string{hidden, shown} {
			cleanup.send(t, map[string]any{"type": "config/area_registry/delete", "area_id": id})
		}
	})
	if _, ok := pgAreas(t, ws)[hidden]; !ok {
		t.Fatalf("setup: area %s is not in HA's registry", hidden)
	}

	policy := fmt.Sprintf(`version: 1
active: pg_agent
profiles:
  pg_agent:
    deny:
      "area:%s": [read]
`, hidden)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
	plain := pgInstanceDir(t, inst.URL(), inst.Token(), "")

	base := pgReadsOK(t, bin, plain, "area", "ls", "--full")
	if !strings.Contains(base.stdout, hidden) {
		t.Fatalf("without a policy `area ls --full` does not list %s — the case is inert:\n%s", hidden, base)
	}

	r := pgReadsOK(t, bin, dir, "area", "ls", "--full")
	if strings.Contains(r.stdout, hidden) || strings.Contains(r.stdout, "PG Policy Read Hidden") {
		t.Errorf("`area ls` still shows the denied area %s:\n%s", hidden, r)
	}
	if !strings.Contains(r.stdout, shown) {
		t.Errorf("`area ls` dropped the allowed area %s too:\n%s", shown, r)
	}
	assertProseTrailer(t, r, pgReadsTrailer(1, 0))

	jr := pgReadsOK(t, bin, dir, "area", "ls", "--json")
	rows := pgReadsJSONArray(t, jr)
	sawShown := false
	for _, row := range rows {
		switch row["area_id"] {
		case hidden:
			t.Errorf("`area ls --json` still holds the denied area %s", hidden)
		case shown:
			sawShown = true
		}
	}
	if !sawShown {
		t.Errorf("`area ls --json` dropped the allowed area %s:\n%s", shown, jr)
	}
	assertJSONTrailer(t, jr, pgReadsTrailer(1, 0))
}

// ---------------------------------------------------------------------------
// Case 9 — logbook rows are decided per entity.
// ---------------------------------------------------------------------------

// pgRawLogbook is HA's logbook between start and now, read outside hactl.
func pgRawLogbook(t *testing.T, inst *hatest.Instance, start time.Time) []map[string]any {
	t.Helper()
	path := "/api/logbook/" + url.PathEscape(start.UTC().Format(time.RFC3339)) +
		"?end_time=" + url.QueryEscape(time.Now().UTC().Add(time.Minute).Format(time.RFC3339))
	status, body := pgHTTP(t, inst, http.MethodGet, path, nil)
	if status != http.StatusOK {
		t.Fatalf("GET %s: %d %s", path, status, body)
	}
	var rows []map[string]any
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("logbook did not parse: %v", err)
	}
	return rows
}

func pgLogbookCount(rows []map[string]any, entityID string) int {
	n := 0
	for _, r := range rows {
		if id, _ := r["entity_id"].(string); id == entityID {
			n++
		}
	}
	return n
}

// TestPolicyReadsSpecLogbookHidesDeniedEntity — after toggling an
// input_boolean the profile may not read, `changes --since 1h` shows no row
// for it and the trailer counts at least those rows (D-142: a logbook row by
// `read entity:<e>`); a row with no entity hactl cannot address, so it is
// hidden too (D-141).
func TestPolicyReadsSpecLogbookHidesDeniedEntity(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	bin := pgReadsBinary(t)
	const ib = "input_boolean.pg_policy_lock_state"
	window := time.Now().Add(-time.Hour)

	before := pgLogbookCount(pgRawLogbook(t, inst, window), ib)
	pgCallService(t, inst, "input_boolean", "toggle", map[string]any{"entity_id": ib})
	pgCallService(t, inst, "input_boolean", "toggle", map[string]any{"entity_id": ib})
	var raw []map[string]any
	deadline := time.Now().Add(30 * time.Second)
	for {
		raw = pgRawLogbook(t, inst, window)
		if pgLogbookCount(raw, ib) >= before+2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("setup: HA's logbook shows %d rows for %s after two toggles (had %d)", pgLogbookCount(raw, ib), ib, before)
		}
		time.Sleep(time.Second)
	}
	deniedRows := pgLogbookCount(raw, ib)

	policy := fmt.Sprintf(`version: 1
active: pg_agent
profiles:
  pg_agent:
    deny:
      "entity:%s": [read]
`, ib)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
	plain := pgInstanceDir(t, inst.URL(), inst.Token(), "")

	base := pgReadsOK(t, bin, plain, "changes", "--since", "1h", "--full")
	if !strings.Contains(base.stdout, ib) {
		t.Fatalf("without a policy `changes` shows no row for %s — the case is inert:\n%s", ib, base)
	}

	r := pgReadsOK(t, bin, dir, "changes", "--since", "1h", "--full")
	if strings.Contains(r.stdout, ib) || strings.Contains(r.stderr, ib) {
		t.Errorf("`changes` under a profile denying read on %s still shows it:\n%s", ib, r)
	}
	lines := pgReadsLines(r.stdout)
	if len(lines) == 0 {
		t.Fatalf("`changes` printed nothing:\n%s", r)
	}
	m := pgReadsTrailerRe.FindStringSubmatch(strings.TrimSpace(lines[len(lines)-1]))
	if m == nil {
		t.Errorf("`changes` does not end with a `hidden by policy: N (profile %s)` line:\n%s", pgProfile, r)
	} else {
		n, _ := strconv.Atoi(m[1])
		if n < deniedRows {
			t.Errorf("`changes` trailer counts %d hidden, Home Assistant's logbook holds %d rows for %s in the window", n, deniedRows, ib)
		}
		if m[3] != pgProfile {
			t.Errorf("`changes` trailer names profile %q, want %q", m[3], pgProfile)
		}
	}

	jr := pgReadsOK(t, bin, dir, "changes", "--since", "1h", "--json")
	rows := pgReadsJSONArray(t, jr)
	for _, row := range rows {
		id, _ := row["entity_id"].(string)
		switch id {
		case ib:
			t.Errorf("`changes --json` still holds a row for %s: %v", ib, row)
		case "":
			t.Errorf("`changes --json` holds a row without an entity — a row hactl cannot address is hidden (D-141): %v", row)
		}
	}
	if pgLogbookCount(raw, "lock.pg_policy_lock") > 0 && pgLogbookCount(rows, "lock.pg_policy_lock") == 0 {
		t.Errorf("`changes --json` dropped the readable lock.pg_policy_lock rows too:\n%s", jr)
	}
	trailers := pgReadsTrailers(jr.stderr)
	if len(trailers) != 1 {
		t.Errorf("`changes --json`: stderr trailer lines %v, want one (D-139):\n%s", trailers, jr)
	} else if n, _ := strconv.Atoi(pgReadsTrailerRe.FindStringSubmatch(trailers[0])[1]); n < deniedRows {
		t.Errorf("`changes --json` trailer counts %d hidden, want at least the %d rows of %s", n, deniedRows, ib)
	}
	if strings.Contains(jr.stdout, "hidden by policy") {
		t.Errorf("`changes --json`: the trailer is on stdout:\n%s", jr)
	}
}

// ---------------------------------------------------------------------------
// Case 10 — `policy show` says reads are enforced.
// ---------------------------------------------------------------------------

// TestPolicyReadsSpecPolicyShowSaysReadsEnforced — `policy show` under a
// profile: `enforced: writes and reads`, and --json `enforced_verbs` is
// ["read","control","edit","create","delete"] (manual "Writes and reads are
// enforced.").
func TestPolicyReadsSpecPolicyShowSaysReadsEnforced(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), pgReadsEntityProfile)

	out, err := runHactlDirErr(t, dir, "policy", "show")
	if err != nil {
		t.Fatalf("policy show: %v\n%s", err, out)
	}
	if !slices.Contains(pgReadsLines(out), "enforced: writes and reads") {
		t.Errorf("`policy show` has no line `enforced: writes and reads`:\n%s", out)
	}
	if strings.Contains(out, "reads not yet") {
		t.Errorf("`policy show` still says reads are not enforced:\n%s", out)
	}

	jout, jerr := runHactlDirErr(t, dir, "policy", "show", "--json")
	if jerr != nil {
		t.Fatalf("policy show --json: %v\n%s", jerr, jout)
	}
	var doc struct {
		Enforced      bool     `json:"enforced"`
		EnforcedVerbs []string `json:"enforced_verbs"`
	}
	if err := json.Unmarshal([]byte(jout), &doc); err != nil {
		t.Fatalf("policy show --json: %v\n%s", err, jout)
	}
	if want := []string{"read", "control", "edit", "create", "delete"}; !doc.Enforced || !slices.Equal(doc.EnforcedVerbs, want) {
		t.Errorf("policy show --json: enforced %v, enforced_verbs %v — want true and %v", doc.Enforced, doc.EnforcedVerbs, want)
	}
}

// ---------------------------------------------------------------------------
// Case 11 — writes are unaffected by read denies.
// ---------------------------------------------------------------------------

// TestPolicyReadsSpecWritesUnaffected — under a profile that denies read on
// the locks and allows control on lights, `svc call light.turn_on` on a light
// is still an allowed preview: exit 0, the plan, the `policy: … allowed` line.
func TestPolicyReadsSpecWritesUnaffected(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	rig := buildPGRig(t, inst)
	light := rig.Lights[0]
	policy := `version: 1
active: pg_agent
profiles:
  pg_agent:
    allow:
      "service:light.*": [control]
      "entity:light.*": [control]
    deny:
      "entity:lock.*": [read]
`
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), policy)
	before := pgStateOf(t, inst, light).State
	assertPolicyAllowedPreview(t, dir, pgSvc("light.turn_on", pgEntityData(light))...)
	if after := pgStateOf(t, inst, light).State; after != before {
		t.Errorf("a preview changed %s (%s → %s)", light, before, after)
	}
}
