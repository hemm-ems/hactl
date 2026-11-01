//go:build integration

package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/hemm-ems/hactl/internal/hatest"
)

// ============================================================================
// Rig and assertions for the permission policy's write enforcement (phase 2,
// stage 2b). Written from the contract only — docs/manual.md "Enforcement
// (writes)" and its per-command table, docs/policy.md, INVARIANTS.md H-29,
// docs/decisions.md D-51..D-84 — before the enforcement existed.
//
// Two rules keep the expectation independent of the code under test (H-12):
//
//   - every read-back goes to Home Assistant over a raw websocket or plain
//     HTTP, never through haapi, so a policy hook that 2b installs in hactl's
//     own transports can neither hide a write nor refuse the test's own
//     setup;
//   - every rule a refusal must name is located in the policy text the test
//     wrote (policyLineOf), never read back from hactl.
// ============================================================================

var (
	policyEnforceOnce sync.Once
	policyEnforceHA   *hatest.Instance
)

// getPolicyEnforceHA starts the instance every enforcement test shares. The
// tests mutate registries and states, so they never touch the package's
// shared `ha`; each test builds its own areas, labels and dashboards with
// pg_-style names and reads its own "before" state.
func getPolicyEnforceHA(t *testing.T) *hatest.Instance {
	t.Helper()
	policyEnforceOnce.Do(func() {
		opts := []hatest.Option{hatest.WithFixture("policyenforce")}
		if img := os.Getenv("HACTL_HA_IMAGE"); img != "" {
			opts = append(opts, hatest.WithImage(img))
		}
		policyEnforceHA = hatest.StartShared(t, opts...)
		waitForRunning(t, policyEnforceHA)
	})
	if policyEnforceHA == nil {
		t.Fatal("policy enforcement HA instance unavailable")
	}
	return policyEnforceHA
}

// pgInstanceDir is an instance directory whose .env points at haURL, holding
// policy as policy.yaml — or no policy file at all when policy is "".
func pgInstanceDir(t *testing.T, haURL, token, policy string) string {
	t.Helper()
	dir := t.TempDir()
	env := "HA_URL=" + haURL + "\nHA_TOKEN=" + token + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	if policy != "" {
		if err := os.WriteFile(filepath.Join(dir, "policy.yaml"), []byte(policy), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// policyLineOf returns the 1-based line of policy holding needle — the line a
// refusal must name for the rule written there.
func policyLineOf(t *testing.T, policy, needle string) int {
	t.Helper()
	for i, l := range strings.Split(policy, "\n") {
		if strings.Contains(l, needle) {
			return i + 1
		}
	}
	t.Fatalf("policy text holds no line with %q:\n%s", needle, policy)
	return 0
}

// pgExitCode is the exit code the CLI entry point would derive from err.
func pgExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

// pgDenied is one entry of details.policy.denied (D-70, R-13).
type pgDenied struct {
	Resource string `json:"resource"`
	Verb     string `json:"verb"`
	Rule     string `json:"rule"`
	Line     int    `json:"line"`
	Partial  bool   `json:"partial"`
	KindWide bool   `json:"kind_wide"`
}

func (d pgDenied) String() string {
	return fmt.Sprintf("%s %s — %s (line %d, partial=%v, kind_wide=%v)", d.Verb, d.Resource, d.Rule, d.Line, d.Partial, d.KindWide)
}

// textLine is the report's line for this denial: `denied: <verb> <resource> —
// <rule>`, followed by ` (line <n>)` when a rule line decided.
func (d pgDenied) textLine() string {
	s := "denied: " + d.Verb + " " + d.Resource + " — " + d.Rule
	if d.Line > 0 {
		s += fmt.Sprintf(" (line %d)", d.Line)
	}
	return s
}

// pgRefusal is the --json refusal: the preview envelope with ok false (R-13).
type pgRefusal struct {
	DryRun  *bool  `json:"dry_run"`
	OK      *bool  `json:"ok"`
	Error   string `json:"error"`
	Action  string `json:"action"`
	Hint    string `json:"hint"`
	Details struct {
		Policy *struct {
			Denied     []pgDenied      `json:"denied"`
			Checked    int             `json:"checked"`
			Profile    string          `json:"profile"`
			SelectedBy string          `json:"selected_by"`
			Level      string          `json:"level"`
			Written    json.RawMessage `json:"written"`
		} `json:"policy"`
	} `json:"details"`
}

// refusalEnvelopeKeys is the top level of a --json refusal, exactly (R-13).
var refusalEnvelopeKeys = []string{"action", "details", "dry_run", "error", "hint", "ok"}

// refusalPolicyKeys is details.policy of a --json refusal, exactly (D-70).
var refusalPolicyKeys = []string{"checked", "denied", "level", "profile", "selected_by", "written"}

// decodeRefusal parses a --json refusal and checks the envelope's shape.
func decodeRefusal(t *testing.T, what, out string, wantDryRun bool) pgRefusal {
	t.Helper()
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		t.Fatalf("%s: --json refusal is not one JSON object: %v\n%s", what, err, out)
	}
	if got := sortedKeys(raw); !slices.Equal(got, refusalEnvelopeKeys) {
		t.Errorf("%s: refusal envelope keys = %v, want exactly %v (R-13)\n%s", what, got, refusalEnvelopeKeys, out)
	}
	var details map[string]json.RawMessage
	if err := json.Unmarshal(raw["details"], &details); err != nil {
		t.Fatalf("%s: details is not an object: %v\n%s", what, err, out)
	}
	var pol map[string]json.RawMessage
	if err := json.Unmarshal(details["policy"], &pol); err != nil {
		t.Fatalf("%s: details.policy is not an object: %v\n%s", what, err, out)
	}
	if got := sortedKeys(pol); !slices.Equal(got, refusalPolicyKeys) {
		t.Errorf("%s: details.policy keys = %v, want exactly %v (D-70)\n%s", what, got, refusalPolicyKeys, out)
	}

	var r pgRefusal
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		t.Fatalf("%s: refusal did not decode: %v\n%s", what, err, out)
	}
	switch {
	case r.DryRun == nil || *r.DryRun != wantDryRun:
		t.Errorf("%s: dry_run = %v, want %v (true for a refused preview, false for a refused --confirm)", what, r.DryRun, wantDryRun)
	case r.OK == nil || *r.OK:
		t.Errorf("%s: ok = %v, want false", what, r.OK)
	}
	if r.Error != "policy_denied" {
		t.Errorf("%s: error = %q, want policy_denied", what, r.Error)
	}
	if r.Action == "" {
		t.Errorf("%s: action is empty", what)
	}
	if strings.Contains(r.Hint, "--confirm") {
		t.Errorf("%s: hint suggests --confirm on a policy refusal: %q", what, r.Hint)
	}
	p := r.Details.Policy
	if p == nil {
		t.Fatalf("%s: no details.policy in the refusal:\n%s", what, out)
	}
	if p.Level != "A" {
		t.Errorf("%s: details.policy.level = %q, want A", what, p.Level)
	}
	if strings.TrimSpace(string(p.Written)) != "[]" {
		t.Errorf("%s: details.policy.written = %s, want [] — a refusal of the plan has written nothing", what, p.Written)
	}
	if len(p.Denied) == 0 {
		t.Errorf("%s: details.policy.denied is empty on a refusal", what)
	}
	if p.Checked < len(p.Denied) {
		t.Errorf("%s: checked = %d < %d denied — checked counts the whole plan", what, p.Checked, len(p.Denied))
	}
	if p.Profile == "" || p.SelectedBy == "" {
		t.Errorf("%s: profile %q selected_by %q — a refusal names the profile in effect and how", what, p.Profile, p.SelectedBy)
	}
	return r
}

func sortedKeys(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// containsDenied reports whether got holds an entry equal to want in every
// field the contract fixes.
func containsDenied(got []pgDenied, want pgDenied) bool {
	return slices.Contains(got, want)
}

// assertPolicyRefused is the core 2b assertion for one write command:
//
//  1. the text preview exits 1 with the class policy_denied, names every
//     wanted denial on stdout with its rule and line, prints no plan, and its
//     error (the stderr summary line, D-70) names the first wanted denial;
//  2. the --json preview is the refusal envelope (R-13) with dry_run true;
//  3. the --json --confirm run is refused the same way with dry_run false,
//     and its denied list is the preview's, entry for entry (preview parity,
//     H-2, D-80).
//
// The in-process tier runs no confirm guard (cliEntry is false), so the
// --confirm leg runs as if a witness were present — the condition D-80 asks
// parity tests to hold. The caller then reads HA back to prove nothing was
// written (H-12).
func assertPolicyRefused(t *testing.T, dir string, args []string, want ...pgDenied) {
	t.Helper()
	if len(want) == 0 {
		t.Fatal("assertPolicyRefused needs at least one wanted denial")
	}

	// --- 1. text preview ---
	out, err := runHactlDirErr(t, dir, args...)
	if pgExitCode(err) != 1 {
		t.Fatalf("hactl %v: exit %d (err %v), want 1 — a denied write is refused in its dry run\n%s",
			args, pgExitCode(err), err, out)
	}
	summary := strings.SplitN(err.Error(), "\n", 2)[0]
	if !strings.Contains(out, "policy_denied: would ") || !strings.Contains(out, "nothing was written") {
		t.Errorf("hactl %v: refusal report on stdout does not open with `policy_denied: would … nothing was written`:\n%s", args, out)
	}
	for _, w := range want {
		if !strings.Contains(out, w.textLine()) {
			t.Errorf("hactl %v: refusal report misses %q:\n%s", args, w.textLine(), out)
		}
	}
	if strings.Contains(out, "dry-run: would") {
		t.Errorf("hactl %v: a refused preview still printed its plan:\n%s", args, out)
	}

	// --- 2. --json preview ---
	jsonArgs := append(slices.Clone(args), "--json")
	jout, jerr := runHactlDirErr(t, dir, jsonArgs...)
	if pgExitCode(jerr) != 1 {
		t.Fatalf("hactl %v: exit %d, want 1\n%s", jsonArgs, pgExitCode(jerr), jout)
	}
	preview := decodeRefusal(t, fmt.Sprintf("hactl %v", jsonArgs), jout, true)
	for _, w := range want {
		if !containsDenied(preview.Details.Policy.Denied, w) {
			t.Errorf("hactl %v: denied = %v, want it to hold %v", jsonArgs, preview.Details.Policy.Denied, w)
		}
	}
	// The one stderr line (D-70) names a denial of the plan: the first, in
	// plan order — the order details.policy.denied keeps.
	if len(preview.Details.Policy.Denied) > 0 {
		assertSummaryLine(t, args, summary, preview.Details.Policy.Denied[0])
	}

	// --- 3. --json --confirm: same refusal, same rules, nothing sent ---
	confirmArgs := append(slices.Clone(args), "--confirm", "--json")
	cout, cerr := runHactlDirErr(t, dir, confirmArgs...)
	if pgExitCode(cerr) != 1 {
		t.Fatalf("hactl %v: exit %d, want 1 — --confirm refuses what the preview refused\n%s",
			confirmArgs, pgExitCode(cerr), cout)
	}
	confirmed := decodeRefusal(t, fmt.Sprintf("hactl %v", confirmArgs), cout, false)
	if !slices.Equal(preview.Details.Policy.Denied, confirmed.Details.Policy.Denied) {
		t.Errorf("preview parity broken for %v:\n preview denied: %v\n confirm denied: %v",
			args, preview.Details.Policy.Denied, confirmed.Details.Policy.Denied)
	}
	if preview.Details.Policy.Checked != confirmed.Details.Policy.Checked {
		t.Errorf("preview parity broken for %v: preview checked %d, confirm checked %d",
			args, preview.Details.Policy.Checked, confirmed.Details.Policy.Checked)
	}
}

// assertSummaryLine checks the refusal's one-line summary — on the CLI the
// stderr line, in process the error's first line (Execute prints exactly the
// error): `policy_denied: <verb> <resource> — <rule> (line <n>)` (D-70). A
// denial no rule line decided (default: deny, fail-closed) is checked up to
// its rule.
func assertSummaryLine(t *testing.T, args []string, summary string, d pgDenied) {
	t.Helper()
	want := "policy_denied: " + d.Verb + " " + d.Resource + " — " + d.Rule
	if d.Line > 0 {
		want += fmt.Sprintf(" (line %d)", d.Line)
		if summary != want {
			t.Errorf("hactl %v: summary line = %q, want %q (D-70)", args, summary, want)
		}
		return
	}
	if !strings.HasPrefix(summary, want) {
		t.Errorf("hactl %v: summary line = %q, want it to start %q (D-70)", args, summary, want)
	}
}

// allowedPreviewLine is the line an allowed preview gains while a profile is
// in effect (D-70).
func allowedPreviewLine(profile string) *regexp.Regexp {
	return regexp.MustCompile(`policy: \d+ checked, allowed \(profile ` + regexp.QuoteMeta(profile) + `\)`)
}

// assertPolicyAllowedPreview runs the preview of an allowed write under
// pgProfile: exit 0, the plan printed, and the `policy: … allowed` line; under
// --json, details.policy carries {checked, profile}.
func assertPolicyAllowedPreview(t *testing.T, dir string, args ...string) {
	t.Helper()
	profile := pgProfile
	out, err := runHactlDirErr(t, dir, args...)
	if err != nil {
		t.Fatalf("hactl %v: allowed preview failed: %v\n%s", args, err, out)
	}
	if !strings.Contains(out, "dry-run: would") {
		t.Errorf("hactl %v: allowed preview printed no plan:\n%s", args, out)
	}
	if !allowedPreviewLine(profile).MatchString(out) {
		t.Errorf("hactl %v: allowed preview lacks `policy: <n> checked, allowed (profile %s)`:\n%s", args, profile, out)
	}
	jout, jerr := runHactlDirErr(t, dir, append(slices.Clone(args), "--json")...)
	if jerr != nil {
		t.Fatalf("hactl %v --json: allowed preview failed: %v\n%s", args, jerr, jout)
	}
	var doc struct {
		DryRun  bool `json:"dry_run"`
		Details struct {
			Policy *struct {
				Checked int    `json:"checked"`
				Profile string `json:"profile"`
			} `json:"policy"`
		} `json:"details"`
	}
	if err := json.Unmarshal([]byte(jout), &doc); err != nil {
		t.Fatalf("hactl %v --json: %v\n%s", args, err, jout)
	}
	if !doc.DryRun || doc.Details.Policy == nil || doc.Details.Policy.Checked < 1 || doc.Details.Policy.Profile != profile {
		t.Errorf("hactl %v --json: allowed preview details.policy = %+v, want {checked ≥ 1, profile %q}",
			args, doc.Details.Policy, profile)
	}
}

// ---------------------------------------------------------------------------
// Raw reads and writes against HA — outside every hactl client path.
// ---------------------------------------------------------------------------

// pgHTTP sends one REST request straight to HA and returns status and body.
func pgHTTP(t *testing.T, inst *hatest.Instance, method, path string, body any) (int, []byte) {
	t.Helper()
	var rdr io.Reader = http.NoBody
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encoding %s %s: %v", method, path, err)
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(context.Background(), method, inst.URL()+path, rdr)
	if err != nil {
		t.Fatalf("building %s %s: %v", method, path, err)
	}
	req.Header.Set("Authorization", "Bearer "+inst.Token())
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading %s %s: %v", method, path, err)
	}
	return resp.StatusCode, out
}

// pgState is one entity's state as HA reports it.
type pgState struct {
	State      string         `json:"state"`
	Attributes map[string]any `json:"attributes"`
}

// pgStateOf reads one entity's state from HA's REST API.
func pgStateOf(t *testing.T, inst *hatest.Instance, entityID string) pgState {
	t.Helper()
	status, body := pgHTTP(t, inst, http.MethodGet, "/api/states/"+entityID, nil)
	if status != http.StatusOK {
		t.Fatalf("GET /api/states/%s: %d %s", entityID, status, body)
	}
	var s pgState
	if err := json.Unmarshal(body, &s); err != nil {
		t.Fatalf("state of %s did not parse: %v", entityID, err)
	}
	return s
}

// pgCallService calls a service as test setup, outside hactl.
func pgCallService(t *testing.T, inst *hatest.Instance, domain, service string, data map[string]any) {
	t.Helper()
	status, body := pgHTTP(t, inst, http.MethodPost, "/api/services/"+domain+"/"+service, data)
	if status != http.StatusOK {
		t.Fatalf("setup call %s.%s: %d %s", domain, service, status, body)
	}
}

// pgRegistryRow is the part of a registry row the tests compare.
type pgRegistryRow struct {
	EntityID string   `json:"entity_id"`
	DeviceID string   `json:"device_id"`
	AreaID   string   `json:"area_id"`
	Labels   []string `json:"labels"`
	Disabled string   `json:"disabled_by"`
	UniqueID string   `json:"unique_id"`
}

func pgEntityRegistry(t *testing.T, ws *rawWS) map[string]pgRegistryRow {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{"type": "config/entity_registry/list"})
	var rows []pgRegistryRow
	if err := json.Unmarshal(env.Result, &rows); err != nil {
		t.Fatalf("entity registry did not parse: %v", err)
	}
	out := make(map[string]pgRegistryRow, len(rows))
	for _, r := range rows {
		out[r.EntityID] = r
	}
	return out
}

type pgDeviceRow struct {
	ID     string   `json:"id"`
	AreaID string   `json:"area_id"`
	Labels []string `json:"labels"`
}

func pgDeviceRegistry(t *testing.T, ws *rawWS) map[string]pgDeviceRow {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{"type": "config/device_registry/list"})
	var rows []pgDeviceRow
	if err := json.Unmarshal(env.Result, &rows); err != nil {
		t.Fatalf("device registry did not parse: %v", err)
	}
	out := make(map[string]pgDeviceRow, len(rows))
	for _, r := range rows {
		out[r.ID] = r
	}
	return out
}

type pgNamedRow struct {
	ID     string   `json:"-"`
	Name   string   `json:"name"`
	Labels []string `json:"labels"`
}

// pgAreas maps area_id → row, read from HA.
func pgAreas(t *testing.T, ws *rawWS) map[string]pgNamedRow {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{"type": "config/area_registry/list"})
	var rows []struct {
		AreaID string   `json:"area_id"`
		Name   string   `json:"name"`
		Labels []string `json:"labels"`
	}
	if err := json.Unmarshal(env.Result, &rows); err != nil {
		t.Fatalf("area registry did not parse: %v", err)
	}
	out := make(map[string]pgNamedRow, len(rows))
	for _, r := range rows {
		out[r.AreaID] = pgNamedRow{ID: r.AreaID, Name: r.Name, Labels: r.Labels}
	}
	return out
}

// pgLabels maps label_id → row, read from HA.
func pgLabels(t *testing.T, ws *rawWS) map[string]pgNamedRow {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{"type": "config/label_registry/list"})
	var rows []struct {
		LabelID string `json:"label_id"`
		Name    string `json:"name"`
	}
	if err := json.Unmarshal(env.Result, &rows); err != nil {
		t.Fatalf("label registry did not parse: %v", err)
	}
	out := make(map[string]pgNamedRow, len(rows))
	for _, r := range rows {
		out[r.LabelID] = pgNamedRow{ID: r.LabelID, Name: r.Name}
	}
	return out
}

// pgCreateLabel creates a label as setup and returns its HA-minted id.
func pgCreateLabel(t *testing.T, ws *rawWS, name string) string {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{"type": "config/label_registry/create", "name": name})
	var row struct {
		LabelID string `json:"label_id"`
	}
	if err := json.Unmarshal(env.Result, &row); err != nil || row.LabelID == "" {
		t.Fatalf("label create answered no label_id: %v %s", err, env.Result)
	}
	return row.LabelID
}

// pgCreateArea creates an area as setup and returns its HA-minted id.
func pgCreateArea(t *testing.T, ws *rawWS, name string) string {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{"type": "config/area_registry/create", "name": name})
	var row struct {
		AreaID string `json:"area_id"`
	}
	if err := json.Unmarshal(env.Result, &row); err != nil || row.AreaID == "" {
		t.Fatalf("area create answered no area_id: %v %s", err, env.Result)
	}
	return row.AreaID
}

// pgUpdateEntity changes an entity registry row as setup. HA refuses an
// entity-level area on an entity without a name of its own (the targets rig
// recorded this), so a placement also names the entity.
func pgUpdateEntity(t *testing.T, ws *rawWS, entityID string, changes map[string]any) {
	t.Helper()
	if _, hasArea := changes["area_id"]; hasArea {
		if _, named := changes["name"]; !named {
			changes["name"] = "PG Policy " + entityID
		}
	}
	msg := map[string]any{"type": "config/entity_registry/update", "entity_id": entityID}
	maps.Copy(msg, changes)
	ws.mustSucceed(t, msg)
}

// pgE7Dashboard is the dashboard the all-or-nothing test owns.
const pgE7Dashboard = "pg-policy-e7"

// pgE7DashboardConfig reads pgE7Dashboard's stored config straight from HA.
func pgE7DashboardConfig(t *testing.T, ws *rawWS) map[string]any {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{"type": "lovelace/config", "url_path": pgE7Dashboard})
	var cfg map[string]any
	if err := json.Unmarshal(env.Result, &cfg); err != nil {
		t.Fatalf("dashboard %s config did not parse: %v", pgE7Dashboard, err)
	}
	return cfg
}

// pgDashboards maps url_path → dashboard id, read from HA.
func pgDashboards(t *testing.T, ws *rawWS) map[string]string {
	t.Helper()
	env := ws.mustSucceed(t, map[string]any{"type": "lovelace/dashboards/list"})
	var rows []struct {
		ID      string `json:"id"`
		URLPath string `json:"url_path"`
	}
	if err := json.Unmarshal(env.Result, &rows); err != nil {
		t.Fatalf("dashboard list did not parse: %v", err)
	}
	out := make(map[string]string, len(rows))
	for _, r := range rows {
		out[r.URLPath] = r.ID
	}
	return out
}

// pgCreateDashboard creates a storage dashboard holding config, as setup, and
// deletes it when the test ends.
func pgCreateDashboard(t *testing.T, inst *hatest.Instance, urlPath string, config map[string]any) {
	t.Helper()
	ws := dialRawWS(t, inst.URL(), inst.Token())
	env := ws.mustSucceed(t, map[string]any{
		"type": "lovelace/dashboards/create", "url_path": urlPath, "title": "PG " + urlPath,
		"require_admin": false, "show_in_sidebar": false,
	})
	var row struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Result, &row); err != nil || row.ID == "" {
		t.Fatalf("dashboard create answered no id: %v %s", err, env.Result)
	}
	t.Cleanup(func() {
		cleanup := dialRawWS(t, inst.URL(), inst.Token())
		cleanup.send(t, map[string]any{"type": "lovelace/dashboards/delete", "dashboard_id": row.ID})
	})
	ws.mustSucceed(t, map[string]any{"type": "lovelace/config/save", "url_path": urlPath, "config": config})
}

// pgRig is what the enforcement tests act on, chosen from HA's own registry
// so a fixture change fails loudly instead of skipping a case.
type pgRig struct {
	Lights  []string          // available demo lights with a device, sorted
	DevOf   map[string]string // light → its device
	// Lock is a registered lock in state locked or unlocked (it is placed in
	// an area, so it needs a registry entry). Observed on HA 2026.10 (stable,
	// 2026-11-01): the demo integration's locks have no registry entry (no
	// unique_id), so the fixture's template lock is the one found here.
	Lock    string
	LockDev string // its device, "" for the template lock
}

func buildPGRig(t *testing.T, inst *hatest.Instance) pgRig {
	t.Helper()
	ws := dialRawWS(t, inst.URL(), inst.Token())
	reg := pgEntityRegistry(t, ws)
	rig := pgRig{DevOf: map[string]string{}}
	ids := make([]string, 0, len(reg))
	for id := range reg {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	seenDev := map[string]bool{}
	for _, id := range ids {
		row := reg[id]
		if row.Disabled != "" || row.UniqueID == "" {
			continue
		}
		switch {
		case strings.HasPrefix(id, "light.") && row.DeviceID != "" && !seenDev[row.DeviceID]:
			if st := pgStateOf(t, inst, id).State; st == "on" || st == "off" {
				rig.Lights = append(rig.Lights, id)
				rig.DevOf[id] = row.DeviceID
				seenDev[row.DeviceID] = true
			}
		case strings.HasPrefix(id, "lock.") && rig.Lock == "":
			if st := pgStateOf(t, inst, id).State; st == "locked" || st == "unlocked" {
				rig.Lock, rig.LockDev = id, row.DeviceID
			}
		}
	}
	if len(rig.Lights) < 3 {
		t.Fatalf("policy rig: need 3 available demo lights on distinct devices, found %v — the rig is inert", rig.Lights)
	}
	if rig.Lock == "" {
		t.Fatal("policy rig: no registered lock in state locked/unlocked — the lock cases would prove nothing")
	}
	for _, l := range rig.Lights {
		if rig.DevOf[l] == rig.LockDev {
			t.Fatalf("policy rig: light %s shares the lock's device — a device target would reach both", l)
		}
	}
	return rig
}

// ---------------------------------------------------------------------------
// A recording proxy between hactl and HA: what hactl sends, as observed on the
// wire, so "nothing sent" (D-60) and "no extra request" (D-58) are
// measurements rather than claims.
// ---------------------------------------------------------------------------

type recordingProxy struct {
	srv  *httptest.Server
	mu   sync.Mutex
	seen []string
}

func newRecordingProxy(t *testing.T, target string) *recordingProxy {
	t.Helper()
	u, err := url.Parse(target)
	if err != nil {
		t.Fatalf("parsing %s: %v", target, err)
	}
	rp := httputil.NewSingleHostReverseProxy(u)
	p := &recordingProxy{}
	p.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		line := r.Method + " " + r.URL.Path
		if strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
			line = "WS " + r.URL.Path
		}
		p.mu.Lock()
		p.seen = append(p.seen, line)
		p.mu.Unlock()
		// Forward the request as hactl sent it. ReverseProxy would add an
		// X-Forwarded-For, and HA answers a forwarded request from a proxy
		// it does not trust with 400 (observed 2026-11-01: every request
		// through the proxy, the websocket handshake too); a nil entry tells
		// ReverseProxy to leave the header out.
		r.Header["X-Forwarded-For"] = nil
		rp.ServeHTTP(w, r)
	}))
	t.Cleanup(p.srv.Close)
	return p
}

func (p *recordingProxy) URL() string { return p.srv.URL }

// take returns what was recorded since the last take, and forgets it.
func (p *recordingProxy) take() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := p.seen
	p.seen = nil
	return out
}

// pgAuditLine is one line of <instance>/cache/policy-audit.log (D-71).
type pgAuditLine struct {
	Time     string `json:"time"`
	Command  string `json:"command"`
	DryRun   bool   `json:"dry_run"`
	Layer    string `json:"layer"`
	Resource string `json:"resource"`
	Verb     string `json:"verb"`
	Allowed  bool   `json:"allowed"`
	Rule     string `json:"rule"`
	Line     int    `json:"line"`
	Profile  string `json:"profile"`
}

// auditLogPath is where D-71 puts the audit log.
func auditLogPath(dir string) string {
	return filepath.Join(dir, "cache", "policy-audit.log")
}
