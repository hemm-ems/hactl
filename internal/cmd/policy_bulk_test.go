package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/enforce"
)

// Enforcement of the multi-target writes (manual "Enforcement (writes)"):
// `ref replace` and `ent rename` (E-7, D-77, R-3, E-5, A-7) and the dash family
// (D-66 as refined). Each command is proven by its refusal naming the rule in
// the preview and under --confirm with nothing sent (H-2's parity), by its
// --confirm run registered in TestEveryConfirmedRequestIsInThePlan (R-12), and
// by D-58: with no profile it sends and prints exactly what it did before.

const bulkAutomations = `- id: prod_heating
  alias: Heating
  triggers:
    - trigger: state
      entity_id: sensor.old
  actions:
    - action: light.turn_on
- id: kitchen
  triggers: [{trigger: state, entity_id: sensor.other}]
  actions: []
`

// bulkChange is the companion's answer for one rewritten leaf.
func bulkChange(location, path string) map[string]any {
	return map[string]any{"location": location, "path": path, "before": "sensor.old", "after": "sensor.new"}
}

// bulkRig is Home Assistant and the companion on one server: the websocket
// answers ws, /v1/ref/replace answers dryChanges to a dry run and
// writeChanges to a write, /v1/config/file serves files, /v1/ref/scan finds
// nothing. It records every companion request in order.
type bulkRig struct {
	ts *cmdTestServer

	mu       sync.Mutex
	requests []string // "ref/replace dry_run=true", "config/file automations.yaml", …
}

func newBulkRig(t *testing.T, ws map[string]any, files map[string]string, dryChanges, writeChanges []map[string]any) *bulkRig {
	t.Helper()
	r := &bulkRig{}
	record := func(s string) {
		r.mu.Lock()
		r.requests = append(r.requests, s)
		r.mu.Unlock()
	}
	handlers := map[string]http.HandlerFunc{
		"/v1/ref/replace": func(w http.ResponseWriter, req *http.Request) {
			var body struct {
				DryRun *bool `json:"dry_run"`
			}
			raw, _ := io.ReadAll(req.Body)
			_ = json.Unmarshal(raw, &body)
			dry := body.DryRun == nil || *body.DryRun
			record(fmt.Sprintf("ref/replace dry_run=%v", dry))
			changes, status := dryChanges, "dry_run"
			if !dry {
				changes, status = writeChanges, "applied"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "changes": changes})
		},
		"/v1/config/file": func(w http.ResponseWriter, req *http.Request) {
			p := req.URL.Query().Get("path")
			record("config/file " + p)
			content, ok := files[p]
			if !ok {
				http.Error(w, "File not found: "+p, http.StatusNotFound)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"path": p, "content": content})
		},
		"/v1/config/files": func(w http.ResponseWriter, _ *http.Request) {
			record("config/files")
			names := slices.Sorted(maps.Keys(files))
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"files": names})
		},
		"/v1/ref/scan": func(w http.ResponseWriter, req *http.Request) {
			record("ref/scan")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"target": req.URL.Query().Get("target"), "hits": []any{}})
		},
	}
	r.ts = startCmdServer(t, ws, handlers)
	pointAtCompanion(t, r.ts)
	return r
}

func (r *bulkRig) companionRequests() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.requests)
}

func (r *bulkRig) reset() {
	r.mu.Lock()
	r.requests = nil
	r.mu.Unlock()
	r.ts.mu.Lock()
	r.ts.cmdCounts = map[string]int{}
	r.ts.mu.Unlock()
}

// refWS is the websocket half of a ref replace rig: no listed dashboard, and a
// default dashboard whose card references sensor.old.
func refWS() map[string]any {
	return map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/config":          dashboardConfigWith("sensor.old"),
		"lovelace/config/save":     map[string]any{},
		"config/entity_registry/list": []any{
			map[string]any{"entity_id": "sensor.old", "unique_id": "u-old", "platform": "template"},
		},
		"config/entity_registry/update": map[string]any{"entity_entry": map[string]any{"entity_id": "sensor.new"}},
	}
}

func refRig(t *testing.T, policyBody string) *bulkRig {
	t.Helper()
	ch := []map[string]any{bulkChange("automations.yaml", "[0].triggers[0].entity_id")}
	r := newBulkRig(t, refWS(), map[string]string{"automations.yaml": bulkAutomations}, ch, ch)
	if policyBody != "" {
		writePolicyFile(t, r.ts.dir, policyBody)
	}
	return r
}

func agentPolicy(rules string) string {
	return "version: 1\nactive: agent\nprofiles:\n  agent:\n" + rules
}

// assertRefused checks a refusal: exit 1, class policy_denied, the stderr line
// naming want (the first denial), and the report naming every denial in
// alsoDenied.
func assertRefused(t *testing.T, what, out string, err error, want string, alsoDenied ...string) {
	t.Helper()
	if cliExitCode(err) != 1 || policyErrorClass(err) != enforce.ClassDenied {
		t.Fatalf("%s: exit %d class %q (%v), want 1 policy_denied\n%s", what, cliExitCode(err), policyErrorClass(err), err, out)
	}
	line, _, _ := strings.Cut(want, " — a part of it is denied")
	if !strings.HasPrefix(err.Error(), "policy_denied: "+line) {
		t.Errorf("%s: stderr line %q, want it to start %q", what, err.Error(), "policy_denied: "+line)
	}
	for _, d := range append([]string{want}, alsoDenied...) {
		if !strings.Contains(out, "  denied: "+d) {
			t.Errorf("%s: the report does not name %q:\n%s", what, d, out)
		}
	}
}

// TestRefReplaceRefusedByItsPlan (D-77, R-3, E-7): a change inside an
// automation is checked as that automation's config as well as its file, so a
// deny on the automation refuses the rewrite — in the preview and under
// --confirm alike, before the companion writes anything and before any
// dashboard is saved, even though the file and the dashboard are allowed.
func TestRefReplaceRefusedByItsPlan(t *testing.T) {
	r := refRig(t, agentPolicy("    deny:\n      \"automation:prod_*\": [edit]\n"))
	const want = `edit automation:prod_heating/config — deny "automation:prod_*" [edit] (line 6)`

	out, err := runPolicyCLI(t, r.ts.dir, "ref", "replace", "sensor.old", "sensor.new")
	assertRefused(t, "preview", out, err, want)
	if !strings.HasPrefix(out, `policy_denied: would rename "sensor.old" to "sensor.new" — refused by policy, nothing was written`) {
		t.Errorf("preview report:\n%s", out)
	}
	if !strings.Contains(out, "checked: 3 resources, 1 denied") {
		t.Errorf("the plan is not file + automation + dashboard:\n%s", out)
	}

	out, err = runPolicyCLI(t, r.ts.dir, "ref", "replace", "sensor.old", "sensor.new", "--confirm")
	assertRefused(t, "--confirm", out, err, want)
	got := r.companionRequests()
	if slices.Contains(got, "ref/replace dry_run=false") {
		t.Errorf("a refused plan reached the companion's write: %q", got)
	}
	if n := r.ts.commandCount("lovelace/config/save"); n != 0 {
		t.Errorf("a refused plan saved %d dashboard(s)", n)
	}

	out, err = runPolicyCLI(t, r.ts.dir, "ref", "replace", "sensor.old", "sensor.new", "--json")
	if cliExitCode(err) != 1 {
		t.Fatalf("--json exit %d", cliExitCode(err))
	}
	m := decodeObject(t, out)
	wantField(t, "refusal", m, "error", "policy_denied")
	wantField(t, "refusal", m, "action", `rename "sensor.old" to "sensor.new"`)
}

// TestRefReplaceChecksFileAndDashboardParts (E-7): a deny on the rewritten
// file refuses the file edit, and a deny on a view of a rewritten dashboard
// refuses through the per-view diff. Every denial is listed, not only the
// first. A deny on a block of the file no longer refuses it (D-110, refining
// D-66): no command edits a block on its own, so edit does not apply where
// that deny sits — deny the file or the typed entry instead.
func TestRefReplaceChecksFileAndDashboardParts(t *testing.T) {
	r := refRig(t, agentPolicy("    deny:\n      \"file:automations.yaml\": [edit]\n      \"dashboard:\": [edit]\n"))
	out, err := runPolicyCLI(t, r.ts.dir, "ref", "replace", "sensor.old", "sensor.new")
	assertRefused(t, "preview", out, err,
		`edit file:automations.yaml — deny "file:automations.yaml" [edit] (line 6)`,
		`edit dashboard: — deny "dashboard:" [edit] (line 7)`)
	if !strings.Contains(out, "checked: 3 resources, 2 denied") {
		t.Errorf("both denials are reported:\n%s", out)
	}

	r = refRig(t, agentPolicy("    deny:\n      \"file:automations.yaml/block/other\": [edit]\n"))
	if out, err := runPolicyCLI(t, r.ts.dir, "ref", "replace", "sensor.old", "sensor.new"); err != nil {
		t.Errorf("a block deny refused the file edit (D-110: edit does not apply at a block): %v\n%s", err, out)
	}
}

// TestRefReplaceIDRewriteIsARename (E-5, D-77): a change that rewrites an
// automation's own id moves it, so it needs create on the new id; a deny on
// the new id refuses the rewrite.
func TestRefReplaceIDRewriteIsARename(t *testing.T) {
	ch := []map[string]any{{"location": "automations.yaml", "path": "[0].id", "before": "prod_heating", "after": "test_heating"}}
	r := newBulkRig(t, map[string]any{"lovelace/dashboards/list": []any{}, "lovelace/config": map[string]any{"views": []any{}}},
		map[string]string{"automations.yaml": bulkAutomations}, ch, ch)
	writePolicyFile(t, r.ts.dir, agentPolicy("    deny:\n      \"automation:test_*\": [create]\n"))
	out, err := runPolicyCLI(t, r.ts.dir, "ref", "replace", "prod_heating", "test_heating")
	assertRefused(t, "preview", out, err, `create automation:test_heating — deny "automation:test_*" [create] (line 6)`)
	if !strings.Contains(out, "checked: 3 resources, 1 denied") {
		t.Errorf("a rename is file + edit old + create new:\n%s", out)
	}
}

// TestRefReplaceUnplaceableChangeFailsClosed (D-77): when hactl cannot read
// the file a change lies in, it cannot tell which typed entries the change
// touches, and refuses rather than check the file alone.
func TestRefReplaceUnplaceableChangeFailsClosed(t *testing.T) {
	ch := []map[string]any{bulkChange("automations.yaml", "[0].triggers[0].entity_id")}
	r := newBulkRig(t, refWS(), map[string]string{}, ch, ch)
	writePolicyFile(t, r.ts.dir, policyAllowEverything)
	out, err := runPolicyCLI(t, r.ts.dir, "ref", "replace", "sensor.old", "sensor.new")
	assertRefused(t, "preview", out, err, "edit file:automations.yaml — fail-closed: typed owner unknown")
}

// TestRefReplaceChecksEveryEntryThatSharesTheChange (D-104): the live-fire
// reproduction. An anchor in an allowed automation, an alias of it in a denied
// one; the companion reports one change, at the anchor. The companion
// rewrites the anchor once and the denied automation reads the new value, so
// it is checked too — the preview and --confirm refuse, and the write is
// never sent.
func TestRefReplaceChecksEveryEntryThatSharesTheChange(t *testing.T) {
	const autos = `- id: pg_test
  alias: PG test
  trigger: &temp_trigger
    - platform: state
      entity_id: sensor.old
  action: []
- id: prod_heating
  alias: Prod heating
  trigger: *temp_trigger
  action: []
`
	ch := []map[string]any{bulkChange("automations.yaml", "[0].trigger[0].entity_id")}
	r := newBulkRig(t, map[string]any{"lovelace/dashboards/list": []any{}, "lovelace/config": map[string]any{"views": []any{}}},
		map[string]string{"automations.yaml": autos}, ch, ch)
	writePolicyFile(t, r.ts.dir, agentPolicy("    deny:\n      \"automation:prod_*\": [write]\n"))
	const want = `edit automation:prod_heating/config — deny "automation:prod_*" [write] (line 6)`
	for _, args := range [][]string{{}, {"--confirm"}} {
		out, err := runPolicyCLI(t, r.ts.dir, append([]string{"ref", "replace", "sensor.old", "sensor.new"}, args...)...)
		assertRefused(t, "anchor shared by a denied automation "+strings.Join(args, ""), out, err, want)
		if !strings.Contains(out, "checked: 3 resources, 1 denied") {
			t.Errorf("the plan is file + both sharing automations:\n%s", out)
		}
	}
	if got := r.companionRequests(); slices.Contains(got, "ref/replace dry_run=false") {
		t.Errorf("a refused plan reached the companion's write: %q", got)
	}
}

// TestRefReplaceChecksTheEntryIncludingAFragment (D-105): a change in a file
// an automation pulls in with `!include` lies in that automation, although
// the fragment has no automation's shape; a deny on the automation refuses
// the rewrite, preview and --confirm alike. A fragment included where no
// typed entry is stays checked as its file only.
func TestRefReplaceChecksTheEntryIncludingAFragment(t *testing.T) {
	files := map[string]string{
		"configuration.yaml":       "automation: !include automations.yaml\nsensor: !include sensors.yaml\n",
		"automations.yaml":         "- id: prod_heating\n  trigger: !include common/temp_trigger.yaml\n  action: []\n",
		"common/temp_trigger.yaml": "- platform: state\n  entity_id: sensor.old\n",
		"sensors.yaml":             "- platform: template\n  sensors: !include common/body.yaml\n",
		"common/body.yaml":         "x:\n  value_template: \"{{ states('sensor.old') }}\"\n",
	}
	ws := map[string]any{"lovelace/dashboards/list": []any{}, "lovelace/config": map[string]any{"views": []any{}}}
	policy := agentPolicy("    deny:\n      \"automation:prod_*\": [write]\n")

	frag := []map[string]any{bulkChange("common/temp_trigger.yaml", "[0].entity_id")}
	r := newBulkRig(t, ws, files, frag, frag)
	writePolicyFile(t, r.ts.dir, policy)
	for _, args := range [][]string{{}, {"--confirm"}} {
		out, err := runPolicyCLI(t, r.ts.dir, append([]string{"ref", "replace", "sensor.old", "sensor.new"}, args...)...)
		assertRefused(t, "fragment in a denied automation "+strings.Join(args, ""), out, err,
			`edit automation:prod_heating/config — deny "automation:prod_*" [write] (line 6)`)
	}
	if got := r.companionRequests(); slices.Contains(got, "ref/replace dry_run=false") {
		t.Errorf("a refused plan reached the companion's write: %q", got)
	}

	body := []map[string]any{bulkChange("common/body.yaml", "x.value_template")}
	r = newBulkRig(t, ws, files, body, body)
	writePolicyFile(t, r.ts.dir, policy)
	out, err := runPolicyCLI(t, r.ts.dir, "ref", "replace", "sensor.old", "sensor.new")
	if err != nil || !strings.Contains(out, "\n  policy: 1 checked, allowed (profile agent)\n") {
		t.Errorf("a fragment below no typed entry is its file only: %v\n%s", err, out)
	}
}

// TestRefReplaceCountsEachCheckOnce: two changes in one automation of one file
// are one edit of the file and one of the automation — `checked` counts the
// plan's (resource, verb) pairs, and a denial is listed once.
func TestRefReplaceCountsEachCheckOnce(t *testing.T) {
	const autos = `- id: prod_heating
  triggers:
    - trigger: state
      entity_id: sensor.old
    - trigger: state
      entity_id: sensor.old
  actions: []
`
	ch := []map[string]any{
		bulkChange("automations.yaml", "[0].triggers[0].entity_id"),
		bulkChange("automations.yaml", "[0].triggers[1].entity_id"),
	}
	r := newBulkRig(t, map[string]any{"lovelace/dashboards/list": []any{}, "lovelace/config": map[string]any{"views": []any{}}},
		map[string]string{"automations.yaml": autos}, ch, ch)
	writePolicyFile(t, r.ts.dir, agentPolicy("    deny:\n      \"automation:prod_*\": [edit]\n"))
	const want = `edit automation:prod_heating/config — deny "automation:prod_*" [edit] (line 6)`
	out, err := runPolicyCLI(t, r.ts.dir, "ref", "replace", "sensor.old", "sensor.new")
	assertRefused(t, "two changes, one owner", out, err, want)
	if !strings.Contains(out, "  checked: 2 resources, 1 denied\n") {
		t.Errorf("checked counts a pair twice:\n%s", out)
	}
	if n := strings.Count(out, "  denied: "+want); n != 1 {
		t.Errorf("the denial is listed %d times, want once:\n%s", n, out)
	}
}

// TestRefReplaceConfirmChecksADryRunFirst (D-77): under a profile the
// confirmed run asks the companion for the rewrite as a dry run, places and
// checks it, and only then sends the write; the allowed preview says what it
// checked; and the write is checked at the request layer through the placed
// plan (its typed owner included).
func TestRefReplaceConfirmChecksADryRunFirst(t *testing.T) {
	r := refRig(t, policyAllowEverything)
	out, err := runPolicyCLI(t, r.ts.dir, "ref", "replace", "sensor.old", "sensor.new")
	if err != nil {
		t.Fatalf("preview: %v\n%s", err, out)
	}
	if !strings.Contains(out, "\n  policy: 3 checked, allowed (profile agent)\n") {
		t.Errorf("the allowed preview does not say what it checked:\n%s", out)
	}
	out, err = runPolicyCLI(t, r.ts.dir, "ref", "replace", "sensor.old", "sensor.new", "--json")
	if err != nil {
		t.Fatal(err)
	}
	details, _ := decodeObject(t, out)["details"].(map[string]any)
	wantField(t, "preview details", details, "policy", map[string]any{"checked": float64(3), "profile": "agent"})

	r.reset()
	if out, err = runPolicyCLI(t, r.ts.dir, "ref", "replace", "sensor.old", "sensor.new", "--confirm"); err != nil {
		t.Fatalf("confirm: %v\n%s", err, out)
	}
	want := []string{"ref/replace dry_run=true", "config/file automations.yaml", "config/files", "ref/replace dry_run=false"}
	if got := r.companionRequests(); !slices.Equal(got, want) {
		t.Errorf("companion requests = %q, want %q", got, want)
	}
	var request []string
	for _, l := range readAudit(t, r.ts.dir) {
		if l["layer"] == "request" && l["dry_run"] == false {
			request = append(request, fmt.Sprint(l["verb"], " ", l["resource"]))
		}
	}
	for _, w := range []string{"edit automation:prod_heating/config", "edit file:automations.yaml", "edit dashboard:"} {
		if !slices.Contains(request, w) {
			t.Errorf("the confirmed write was not checked as %q at the request layer: %q", w, request)
		}
	}
}

// TestRefReplaceReportsAFileWrittenOutsideThePlan (D-77, A-7): a file the
// confirmed write changed that its checked dry run did not list is reported
// after the fact — exit 1, policy_denied, the file denied with `fail-closed:
// written outside the checked plan` and listed in `written`; under --json the
// refusal is the run's one document.
func TestRefReplaceReportsAFileWrittenOutsideThePlan(t *testing.T) {
	dry := []map[string]any{bulkChange("automations.yaml", "[0].triggers[0].entity_id")}
	wrote := append(slices.Clone(dry), bulkChange("packages/late.yaml", "sensor[0].entity_id"))
	r := newBulkRig(t, refWS(), map[string]string{"automations.yaml": bulkAutomations}, dry, wrote)
	writePolicyFile(t, r.ts.dir, policyAllowEverything)

	out, err := runPolicyCLI(t, r.ts.dir, "ref", "replace", "sensor.old", "sensor.new", "--confirm")
	assertRefused(t, "confirm", out, err, "edit file:packages/late.yaml — fail-closed: written outside the checked plan")
	if !strings.Contains(out, "renamed \"sensor.old\" → \"sensor.new\"") || !strings.Contains(out, "already written: edit file:packages/late.yaml") {
		t.Errorf("the text report must show the rename and what was written outside the plan:\n%s", out)
	}

	out, err = runPolicyCLI(t, r.ts.dir, "ref", "replace", "sensor.old", "sensor.new", "--confirm", "--json")
	if cliExitCode(err) != 1 {
		t.Fatalf("--json exit %d (%v)", cliExitCode(err), err)
	}
	m := decodeObject(t, out)
	wantField(t, "refusal", m, "dry_run", false)
	pol, _ := m["details"].(map[string]any)["policy"].(map[string]any)
	wantField(t, "details.policy", pol, "written", []any{"edit file:packages/late.yaml"})

	var outside int
	for _, l := range readAudit(t, r.ts.dir) {
		if l["rule"] == enforce.RuleWrittenOutsidePlan {
			outside++
		}
	}
	if outside != 2 {
		t.Errorf("audit lines for the file written outside the plan = %d, want 2 (one per run)", outside)
	}
}

// TestEntRenameRefusedByItsPlan (E-5 + E-7, D-77): the rename is the registry
// edit, the create of the new id and everything the reference rewrite after it
// touches — checked before the registry update, so a deny on an automation the
// rewrite reaches refuses the whole rename with nothing written; and a deny on
// the new id refuses it too.
func TestEntRenameRefusedByItsPlan(t *testing.T) {
	r := refRig(t, agentPolicy("    deny:\n      \"automation:prod_*\": [write]\n"))
	const want = `edit automation:prod_heating/config — deny "automation:prod_*" [write] (line 6)`
	out, err := runPolicyCLI(t, r.ts.dir, "ent", "rename", "sensor.old", "sensor.new")
	assertRefused(t, "preview", out, err, want)
	if !strings.Contains(out, "checked: 5 resources, 1 denied") {
		t.Errorf("the plan is registry edit + create + file + automation + dashboard:\n%s", out)
	}
	out, err = runPolicyCLI(t, r.ts.dir, "ent", "rename", "sensor.old", "sensor.new", "--confirm")
	assertRefused(t, "--confirm", out, err, want)
	if n := r.ts.commandCount("config/entity_registry/update"); n != 0 {
		t.Errorf("a refused rename updated the registry %d time(s)", n)
	}
	if got := r.companionRequests(); slices.Contains(got, "ref/replace dry_run=false") {
		t.Errorf("a refused rename reached the companion's write: %q", got)
	}

	writePolicyFile(t, r.ts.dir, agentPolicy("    deny:\n      \"entity:sensor.new\": [create]\n"))
	out, err = runPolicyCLI(t, r.ts.dir, "ent", "rename", "sensor.old", "sensor.new")
	assertRefused(t, "deny on the new id", out, err, `create entity:sensor.new — deny "entity:sensor.new" [create] (line 6)`)
}

// TestEntRenameConfirmChecksOncePerInvocation (D-77): the confirmed rename
// checks the reference rewrite's dry run before the registry update and sends
// the rewrite through that checked plan — one dry run, one write, in that
// order, and the allowed preview says what it checked.
func TestEntRenameConfirmChecksOncePerInvocation(t *testing.T) {
	r := refRig(t, policyAllowEverything)
	out, err := runPolicyCLI(t, r.ts.dir, "ent", "rename", "sensor.old", "sensor.new")
	if err != nil || !strings.Contains(out, "policy:") || !strings.Contains(out, "5 checked, allowed (profile agent)") {
		t.Fatalf("preview: %v\n%s", err, out)
	}
	r.reset()
	if out, err = runPolicyCLI(t, r.ts.dir, "ent", "rename", "sensor.old", "sensor.new", "--confirm"); err != nil {
		t.Fatalf("confirm: %v\n%s", err, out)
	}
	want := []string{"ref/scan", "ref/replace dry_run=true", "config/file automations.yaml", "config/files", "ref/replace dry_run=false"}
	if got := r.companionRequests(); !slices.Equal(got, want) {
		t.Errorf("companion requests = %q, want %q", got, want)
	}
	if n := r.ts.commandCount("config/entity_registry/update"); n != 1 {
		t.Errorf("registry updates = %d, want 1", n)
	}
}

// --- the dash family -----------------------------------------------------------

func dashView(path, entity string) map[string]any {
	return map[string]any{"path": path, "cards": []any{map[string]any{"entity": entity}}}
}

func dashRig(t *testing.T, stored any) *bulkRig {
	t.Helper()
	return newBulkRig(t, map[string]any{
		"lovelace/dashboards/list": []any{
			map[string]any{"id": "wall_panel", "url_path": "wall-panel", "title": "Wall", "mode": "storage"},
			map[string]any{"id": "lovelace", "url_path": "lovelace", "title": "Overview", "mode": "storage"},
		},
		"lovelace/config":            stored,
		"lovelace/config/save":       map[string]any{},
		"lovelace/dashboards/create": map[string]any{"id": "new_one", "url_path": "new-one", "title": "New", "mode": "storage"},
		"lovelace/dashboards/delete": map[string]any{},
	}, nil, nil, nil)
}

func writeDashFile(t *testing.T, cfg any) string {
	t.Helper()
	b, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "dash.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDashSaveRefusedByItsPlan (D-66 as refined): `dash save` is checked per
// view against the stored config — a change to one view needs edit on that
// view only, so a deny on another view does not refuse it; a change to the
// denied view does; a change outside views (the title) is addressable only at
// the dashboard, which the view deny makes partial, hence denied; and with no
// stored config the whole save is checked at the dashboard. Preview and
// --confirm refuse alike, and nothing is saved.
func TestDashSaveRefusedByItsPlan(t *testing.T) {
	stored := map[string]any{"title": "Wall", "views": []any{dashView("home", "light.a"), dashView("secret", "lock.door")}}
	r := dashRig(t, stored)
	writePolicyFile(t, r.ts.dir, agentPolicy("    deny:\n      \"dashboard:wall-panel/views/secret\": [edit]\n"))
	const viewDeny = `deny "dashboard:wall-panel/views/secret" [edit] (line 6)`

	homeOnly := writeDashFile(t, map[string]any{"title": "Wall", "views": []any{dashView("home", "light.b"), dashView("secret", "lock.door")}})
	out, err := runPolicyCLI(t, r.ts.dir, "dash", "save", "wall-panel", "-f", homeOnly)
	if err != nil || !strings.Contains(out, " 1 checked, allowed (profile agent)\n") {
		t.Errorf("a change to an allowed view: %v\n%s", err, out)
	}

	secret := writeDashFile(t, map[string]any{"title": "Wall", "views": []any{dashView("home", "light.a"), dashView("secret", "lock.other")}})
	for _, args := range [][]string{{}, {"--confirm"}} {
		out, err = runPolicyCLI(t, r.ts.dir, append([]string{"dash", "save", "wall-panel", "-f", secret}, args...)...)
		assertRefused(t, "the denied view "+strings.Join(args, ""), out, err, "edit dashboard:wall-panel/views/secret — "+viewDeny)
	}

	title := writeDashFile(t, map[string]any{"title": "Renamed", "views": stored["views"]})
	out, err = runPolicyCLI(t, r.ts.dir, "dash", "save", "wall-panel", "-f", title)
	assertRefused(t, "a non-view key", out, err, "edit dashboard:wall-panel — "+viewDeny+" — a part of it is denied, so all of it is (D-66)")
	if n := r.ts.commandCount("lovelace/config/save"); n != 0 {
		t.Errorf("refused saves sent %d lovelace/config/save", n)
	}

	none := dashRig(t, wsErrorResponse{Code: "config_not_found", Message: "No config found."})
	writePolicyFile(t, none.ts.dir, agentPolicy("    deny:\n      \"dashboard:wall-panel/views/secret\": [edit]\n"))
	out, err = runPolicyCLI(t, none.ts.dir, "dash", "save", "wall-panel", "-f", homeOnly)
	assertRefused(t, "no stored config", out, err, "edit dashboard:wall-panel — "+viewDeny+" — a part of it is denied")
}

// TestDashSaveUnmappableViewFailsClosed (D-68, D-69, D-101): a view whose
// path the policy grammar refuses (a zero-width character, a trailing space)
// cannot be addressed, so the save is refused — never dropped from the plan,
// which would let the preview pass with nothing checked and the request layer
// refuse the real save mid-run (H-2). Preview and --confirm alike, for dash
// save and for dash replace, and nothing is saved.
func TestDashSaveUnmappableViewFailsClosed(t *testing.T) {
	const want = "edit dashboard:wall-panel — fail-closed: unmappable resource dashboard:wall-panel"
	for _, bad := range []string{"bad\u200b", "bad "} {
		stored := map[string]any{"title": "Wall", "views": []any{dashView("home", "light.a")}}
		r := dashRig(t, stored)
		writePolicyFile(t, r.ts.dir, policyAllowEverything)
		added := writeDashFile(t, map[string]any{"title": "Wall", "views": []any{dashView("home", "light.a"), dashView(bad, "light.a")}})
		for _, args := range [][]string{{}, {"--confirm"}} {
			out, err := runPolicyCLI(t, r.ts.dir, append([]string{"dash", "save", "wall-panel", "-f", added}, args...)...)
			assertRefused(t, fmt.Sprintf("dash save, view %q %s", bad, strings.Join(args, "")), out, err, want)
			if strings.Contains(out, "dry-run:") {
				t.Errorf("a save with an unmappable view previewed:\n%s", out)
			}
		}

		withBad := map[string]any{"title": "Wall", "views": []any{dashView("home", "light.a"), dashView(bad, "light.a")}}
		rr := dashRig(t, withBad)
		writePolicyFile(t, rr.ts.dir, policyAllowEverything)
		for _, args := range [][]string{{}, {"--confirm"}} {
			out, err := runPolicyCLI(t, rr.ts.dir, append([]string{"dash", "replace", "light.a", "light.b", "wall-panel"}, args...)...)
			assertRefused(t, fmt.Sprintf("dash replace, view %q %s", bad, strings.Join(args, "")), out, err, want)
		}
		for _, rig := range []*bulkRig{r, rr} {
			if n := rig.ts.commandCount("lovelace/config/save"); n != 0 {
				t.Errorf("view %q: a refused save sent %d lovelace/config/save", bad, n)
			}
		}
	}
}

// TestDashDefaultDashboardIsOneAddress (D-66 as refined): the default
// dashboard is `dashboard:` however the command names it — no url_path, or
// the `lovelace` HA lists it under.
func TestDashDefaultDashboardIsOneAddress(t *testing.T) {
	r := dashRig(t, map[string]any{"views": []any{dashView("home", "light.a")}})
	writePolicyFile(t, r.ts.dir, agentPolicy("    deny:\n      \"dashboard:\": [write]\n"))
	cfg := writeDashFile(t, map[string]any{"views": []any{dashView("home", "light.b")}})
	const want = `edit dashboard:/views/home — deny "dashboard:" [write] (line 6)`
	for _, args := range [][]string{
		{"dash", "save", "-f", cfg},
		{"dash", "save", "lovelace", "-f", cfg},
		{"dash", "replace", "light.a", "light.b"},
		{"dash", "replace", "light.a", "light.b", "lovelace"},
	} {
		out, err := runPolicyCLI(t, r.ts.dir, args...)
		assertRefused(t, strings.Join(args, " "), out, err, want)
	}
	out, err := runPolicyCLI(t, r.ts.dir, "dash", "delete", "lovelace")
	assertRefused(t, "dash delete lovelace", out, err, `delete dashboard: — deny "dashboard:" [write] (line 6)`)
}

// TestDashReplaceCreateDeleteRefusedByTheirPlans: dash replace is checked per
// view like a save; dash create and dash delete on the dashboard itself —
// preview and --confirm alike, nothing sent.
func TestDashReplaceCreateDeleteRefusedByTheirPlans(t *testing.T) {
	r := dashRig(t, map[string]any{"views": []any{dashView("home", "light.a"), dashView("secret", "light.a")}})
	writePolicyFile(t, r.ts.dir, agentPolicy("    deny:\n      \"dashboard:wall-panel/views/secret\": [edit]\n      \"dashboard:new-*\": [create]\n      \"dashboard:wall-*\": [delete]\n"))
	for _, confirm := range [][]string{{}, {"--confirm"}} {
		out, err := runPolicyCLI(t, r.ts.dir, append([]string{"dash", "replace", "light.a", "light.b", "wall-panel"}, confirm...)...)
		assertRefused(t, "dash replace", out, err, `edit dashboard:wall-panel/views/secret — deny "dashboard:wall-panel/views/secret" [edit] (line 6)`)
		out, err = runPolicyCLI(t, r.ts.dir, append([]string{"dash", "create", "--url-path", "new-one", "--title", "New"}, confirm...)...)
		assertRefused(t, "dash create", out, err, `create dashboard:new-one — deny "dashboard:new-*" [create] (line 7)`)
		out, err = runPolicyCLI(t, r.ts.dir, append([]string{"dash", "delete", "wall-panel"}, confirm...)...)
		assertRefused(t, "dash delete", out, err, `delete dashboard:wall-panel — deny "dashboard:wall-*" [delete] (line 8)`)
	}
	for _, c := range []string{"lovelace/config/save", "lovelace/dashboards/create", "lovelace/dashboards/delete"} {
		if n := r.ts.commandCount(c); n != 0 {
			t.Errorf("refused plans sent %d %s", n, c)
		}
	}
}

// --- D-58: no profile, no change -------------------------------------------------

// bulkNoProfileGolden is the transcript of the commands of this family as they
// were before stage 2b wired their plans (recorded from 60a8501): each run's
// stdout, error, companion requests in order and websocket command counts.
const bulkNoProfileGolden = "testdata/policy/bulk_no_profile.golden"

// bulkNoProfileRuns are the invocations the transcript holds; "@cfg" is a
// dashboard config file.
var bulkNoProfileRuns = [][]string{
	{"ref", "replace", "sensor.old", "sensor.new"},
	{"ref", "replace", "sensor.old", "sensor.new", "--json"},
	{"ref", "replace", "sensor.old", "sensor.new", "--confirm"},
	{"ref", "replace", "sensor.old", "sensor.new", "--confirm", "--json"},
	{"ent", "rename", "sensor.old", "sensor.new"},
	{"ent", "rename", "sensor.old", "sensor.new", "--json"},
	{"ent", "rename", "sensor.old", "sensor.new", "--confirm"},
	{"dash", "replace", "sensor.old", "sensor.new", "wall-panel"},
	{"dash", "replace", "sensor.old", "sensor.new", "wall-panel", "--confirm", "--json"},
	{"dash", "save", "wall-panel", "-f", "@cfg"},
	{"dash", "save", "wall-panel", "-f", "@cfg", "--confirm"},
	{"dash", "create", "--url-path", "new-one", "--title", "New"},
	{"dash", "create", "--url-path", "new-one", "--title", "New", "--confirm"},
	{"dash", "delete", "wall-panel"},
	{"dash", "delete", "wall-panel", "--confirm", "--json"},
}

// bulkTranscriptWS are the websocket commands whose counts the transcript
// records.
var bulkTranscriptWS = []string{
	"config/entity_registry/update", "lovelace/config", "lovelace/config/save",
	"lovelace/dashboards/create", "lovelace/dashboards/delete", "lovelace/dashboards/list",
}

// bulkNoProfileRun runs one invocation against a fresh rig under policy body
// (no file when empty) and returns its transcript entry. It fails the test if
// anything was audited.
func bulkNoProfileRun(t *testing.T, args []string, body string) string {
	t.Helper()
	cfg := map[string]any{"views": []any{dashView("home", "sensor.old")}}
	ws := refWS()
	ws["lovelace/dashboards/list"] = []any{map[string]any{"id": "wall_panel", "url_path": "wall-panel", "title": "Wall", "mode": "storage"}}
	ws["lovelace/config"] = cfg
	ws["lovelace/dashboards/create"] = map[string]any{"id": "new_one", "url_path": "new-one", "title": "New", "mode": "storage"}
	ws["lovelace/dashboards/delete"] = map[string]any{}
	ch := []map[string]any{bulkChange("automations.yaml", "[0].triggers[0].entity_id")}
	r := newBulkRig(t, ws, map[string]string{"automations.yaml": bulkAutomations}, ch, ch)
	if body != "" {
		writePolicyFile(t, r.ts.dir, body)
	}
	file := writeDashFile(t, cfg)
	run := slices.Clone(args)
	for i, a := range run {
		if a == "@cfg" {
			run[i] = file
		}
	}
	out, err := runPolicyCLI(t, r.ts.dir, run...)
	var b strings.Builder
	b.WriteString("$ hactl " + strings.Join(args, " ") + "\n")
	b.WriteString(strings.ReplaceAll(out, r.ts.dir, "<dir>"))
	if err != nil {
		b.WriteString("error: " + err.Error() + "\n")
	}
	b.WriteString("companion: " + strings.Join(r.companionRequests(), ", ") + "\n")
	for _, c := range bulkTranscriptWS {
		fmt.Fprintf(&b, "ws %s: %d\n", c, r.ts.commandCount(c))
	}
	if _, statErr := os.Stat(filepath.Join(r.ts.dir, "cache", enforce.AuditFileName)); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("policy %q, %v: an audit log was written", body, args)
	}
	return b.String() + "----\n"
}

// TestBulkWritesWithoutAProfileAreUnchanged (D-58): with no policy file, and
// with a file that puts no profile in effect, each command of this family
// prints the same bytes and sends the same requests — no extra dry run, no file
// read, no extra dashboard read — as it did before the policy was enforced (the
// golden transcript, recorded from 60a8501), and writes no audit log.
func TestBulkWritesWithoutAProfileAreUnchanged(t *testing.T) {
	want, err := os.ReadFile(bulkNoProfileGolden)
	if err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"", policyImplicitAllow} {
		var got strings.Builder
		for _, args := range bulkNoProfileRuns {
			got.WriteString(bulkNoProfileRun(t, args, body))
		}
		if got.String() != string(want) {
			t.Errorf("policy %q: the transcript differs from %s:\n%s", body, bulkNoProfileGolden, got.String())
		}
	}
}

// --- R-12 -----------------------------------------------------------------------

// asCompanion points the rig's .env at itself as the companion too.
func asCompanion(t *testing.T, ts *cmdTestServer, args []string) []string {
	t.Helper()
	pointAtCompanion(t, ts)
	return args
}

// withDashFile writes the dash save input and puts its path where args say
// "@cfg": one view changed, one added, one removed and the title changed —
// every kind of diff.
func withDashFile(t *testing.T, _ *cmdTestServer, args []string) []string {
	t.Helper()
	p := writeDashFile(t, map[string]any{"title": "Changed", "views": []any{dashView("home", "light.b"), dashView("new", "light.c")}})
	for i, a := range args {
		if a == "@cfg" {
			args[i] = p
		}
	}
	return args
}

func init() {
	refCase := func(name string, args ...string) confirmedCoverageCase {
		return confirmedCoverageCase{name: name, args: args, ws: refWS(), http: refCoverageCompanion(), setup: asCompanion}
	}
	dashWS := func() map[string]any {
		return map[string]any{
			"lovelace/dashboards/list": []any{
				map[string]any{"id": "wall_panel", "url_path": "wall-panel", "title": "Wall", "mode": "storage"},
			},
			"lovelace/config":            map[string]any{"title": "Wall", "views": []any{dashView("home", "light.a"), dashView("secret", "light.a")}},
			"lovelace/config/save":       map[string]any{},
			"lovelace/dashboards/create": map[string]any{"id": "new_one", "url_path": "new-one", "title": "New", "mode": "storage"},
			"lovelace/dashboards/delete": map[string]any{},
		}
	}
	confirmedCoverageCases = append(confirmedCoverageCases,
		refCase("ref replace", "ref", "replace", "sensor.old", "sensor.new", "--confirm"),
		refCase("ent rename", "ent", "rename", "sensor.old", "sensor.new", "--confirm"),
		confirmedCoverageCase{name: "dash replace", args: []string{"dash", "replace", "light.a", "light.b", "wall-panel", "--confirm"}, ws: dashWS()},
		confirmedCoverageCase{name: "dash save", args: []string{"dash", "save", "wall-panel", "-f", "@cfg", "--confirm"}, ws: dashWS(), setup: withDashFile},
		confirmedCoverageCase{name: "dash create", args: []string{"dash", "create", "--url-path", "new-one", "--title", "New", "--confirm"}, ws: dashWS()},
		confirmedCoverageCase{name: "dash delete", args: []string{"dash", "delete", "wall-panel", "--confirm"}, ws: dashWS()},
	)
}

// refCoverageCompanion is the companion half of the ref coverage cases: a
// change inside an automation (so the plan holds a typed owner) and the file
// it lies in.
func refCoverageCompanion() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"/v1/ref/replace": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "dry_run", "changes": []any{bulkChange("automations.yaml", "[0].triggers[0].entity_id")}})
		},
		"/v1/config/file": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"path": r.URL.Query().Get("path"), "content": bulkAutomations})
		},
		"/v1/config/files": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"files":["automations.yaml"]}`)
		},
		"/v1/ref/scan": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"target":"sensor.old","hits":[]}`)
		},
	}
}
