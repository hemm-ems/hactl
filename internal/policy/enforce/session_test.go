package enforce_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// --- fixtures ---------------------------------------------------------------------

func loadPolicy(t *testing.T, body string) (*policy.Policy, string) {
	t.Helper()
	dir := t.TempDir()
	if body != "" {
		if err := os.WriteFile(filepath.Join(dir, policy.FileName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p, err := policy.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return p, dir
}

// fakeFetcher answers from fixed data and counts the questions.
type fakeFetcher struct {
	live     []string
	members  map[string][]string
	autoIDs  map[string]string
	liveErr  error
	reach    map[string][]string // key: json of target
	reachErr error
	reg      *reqmap.Registries
	complete bool
	regErr   error
	dash     map[string]reqmap.StoredDashboard
	ids      map[string]string
	where    policy.Live
	asked    []string
	closed   bool
}

func (f *fakeFetcher) LiveStates(context.Context) (*reqmap.LiveStates, error) {
	f.asked = append(f.asked, "live")
	if f.liveErr != nil {
		return nil, f.liveErr
	}
	return &reqmap.LiveStates{EntityIDs: f.live, Members: f.members, AutomationIDs: f.autoIDs}, nil
}

func (f *fakeFetcher) Reach(_ context.Context, target map[string]any) ([]string, error) {
	b, _ := json.Marshal(target)
	f.asked = append(f.asked, "reach "+string(b))
	if f.reachErr != nil {
		return nil, f.reachErr
	}
	return f.reach[string(b)], nil
}

func (f *fakeFetcher) Registries(context.Context) (*reqmap.Registries, bool, error) {
	f.asked = append(f.asked, "registries")
	return f.reg, f.complete, f.regErr
}

func (f *fakeFetcher) Dashboard(_ context.Context, u string) (reqmap.StoredDashboard, error) {
	f.asked = append(f.asked, "dashboard "+u)
	d, ok := f.dash[u]
	if !ok {
		return reqmap.StoredDashboard{}, errors.New("no such dashboard")
	}
	return d, nil
}

func (f *fakeFetcher) DashboardIDs(context.Context) (map[string]string, error) {
	f.asked = append(f.asked, "dashboard ids")
	return f.ids, nil
}

func (f *fakeFetcher) Live() policy.Live { return f.where }
func (f *fakeFetcher) Close()            { f.closed = true }

func newSession(t *testing.T, body string, f enforce.Fetcher, opts ...func(*enforce.Options)) (*enforce.Session, string) {
	t.Helper()
	pol, dir := loadPolicy(t, body)
	o := enforce.Options{
		Policy: pol, InstanceDir: dir, Command: "hactl label create Garden", DryRun: true, Fetcher: f,
		Now: func() time.Time { return time.Date(2026, 11, 1, 9, 42, 7, 0, time.FixedZone("CET", 3600)) },
	}
	for _, fn := range opts {
		fn(&o)
	}
	s, err := enforce.New(o)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func auditLines(t *testing.T, dir string) []map[string]any {
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
			t.Fatalf("audit line is not JSON: %v\n%s", err, sc.Text())
		}
		out = append(out, m)
	}
	return out
}

func kindWide(v policy.Verb, k policy.Kind) reqmap.Check {
	return reqmap.Check{Verb: v, Resource: policy.Resource{Kind: k}, KindWide: true}
}

func concrete(t *testing.T, v policy.Verb, s string) reqmap.Check {
	t.Helper()
	r, err := policy.ParseResource(s)
	if err != nil {
		t.Fatal(err)
	}
	return reqmap.Check{Verb: v, Resource: r}
}

const agentDenyLabelX = `version: 1
active: agent
profiles:
  agent:
    deny:
      "label:secret": [create]
      "automation:prod_*": [delete]
`

// --- New ------------------------------------------------------------------------------

// TestNoSessionWithoutAProfile: with no file or the implicit allow-all there is
// nothing to enforce, and no session may exist (D-58, D-74).
func TestNoSessionWithoutAProfile(t *testing.T) {
	for name, body := range map[string]string{
		"no file":            "",
		"implicit allow-all": "version: 1\nprofiles:\n  agent:\n    default: deny\n",
	} {
		pol, dir := loadPolicy(t, body)
		if s, err := enforce.New(enforce.Options{Policy: pol, InstanceDir: dir}); err == nil || s != nil {
			t.Errorf("%s: New made a session (%v), want a refusal", name, err)
		}
	}
}

// --- the command layer --------------------------------------------------------------

// TestCheckPlanRefusesEveryDeniedPairInPlanOrder: E-7 — any denial refuses the
// whole plan, the refusal lists every denied pair in plan order with its rule
// and line, counts what was checked and names the profile (D-70).
func TestCheckPlanRefusesEveryDeniedPairInPlanOrder(t *testing.T) {
	s, dir := newSession(t, agentDenyLabelX, nil)
	plan := enforce.Plan{Action: "delete automation", Checks: []reqmap.Check{
		concrete(t, policy.Delete, "automation:prod_heating"),
		concrete(t, policy.Delete, "entity:automation.prod_heating"),
		kindWide(policy.Create, policy.KindLabel),
	}}
	_, err := s.CheckPlan(plan, true, false)
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) {
		t.Fatalf("CheckPlan = %v, want a *DeniedError", err)
	}
	if ref.Layer != enforce.LayerCommand || !ref.DryRun || ref.Action != "delete automation" || ref.Checked != 3 ||
		ref.Profile != "agent" || ref.SelectedBy != policy.SelectedByActive || len(ref.Written) != 0 {
		t.Errorf("refusal = %+v", ref)
	}
	want := []enforce.Denial{
		{Resource: "automation:prod_heating", Verb: "delete", Rule: `deny "automation:prod_*" [delete]`, Line: 7},
		{Resource: "label:*", Verb: "create", Rule: `deny "label:secret" [create]`, Line: 6, KindWide: true},
	}
	if !reflect.DeepEqual(ref.Denied, want) {
		t.Errorf("denied\n  got  %+v\n  want %+v", ref.Denied, want)
	}
	if got := ref.Error(); got != `policy_denied: 2 of 3 resources denied (profile agent)` {
		t.Errorf("stderr line = %q", got)
	}
	if got := ref.FirstDenial(); got != `policy_denied: delete automation:prod_heating — deny "automation:prod_*" [delete] (line 7)` {
		t.Errorf("first denial = %q", got)
	}
	if ref.ExitCode() != 1 || ref.PolicyClass() != enforce.ClassDenied {
		t.Errorf("exit %d class %q", ref.ExitCode(), ref.PolicyClass())
	}
	const hint = "the owner would change the rule on line 6, 7; allowed: 1 of 3 checked resources, but the command is refused as a whole — report it; do not edit policy.yaml"
	if got := ref.Hint(); got != hint {
		t.Errorf("hint = %q, want %q", got, hint)
	}
	lines := auditLines(t, dir)
	if len(lines) != 3 {
		t.Fatalf("audit lines = %d, want one per checked pair (3)", len(lines))
	}
	first := lines[0]
	for k, v := range map[string]any{
		"time": "2026-11-01T09:42:07+01:00", "command": "hactl label create Garden", "dry_run": true, "layer": "command",
		"resource": "automation:prod_heating", "verb": "delete", "allowed": false,
		"rule": `deny "automation:prod_*" [delete]`, "line": float64(7), "profile": "agent",
	} {
		if first[k] != v {
			t.Errorf("audit line %q = %v, want %v", k, first[k], v)
		}
	}
	if len(first) != 10 {
		t.Errorf("audit line has %d fields, want D-71's ten: %v", len(first), first)
	}
	if lines[1]["allowed"] != true || lines[1]["rule"] != policy.RuleDefaultAllow {
		t.Errorf("an allowed pair's audit line = %v", lines[1])
	}
}

// TestCheckPlanAllowsAndSummarises: an allowed plan returns the preview's
// `policy: N checked, allowed (profile p)` facts and audits each pair.
func TestCheckPlanAllowsAndSummarises(t *testing.T) {
	s, dir := newSession(t, agentDenyLabelX, nil)
	res, err := s.CheckPlan(enforce.Plan{Action: "delete automation", Checks: []reqmap.Check{
		concrete(t, policy.Delete, "automation:heating"),
	}}, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Checked != 1 || res.Profile != "agent" || res.SelectedBy != policy.SelectedByActive {
		t.Errorf("result = %+v", res)
	}
	if l := auditLines(t, dir); len(l) != 1 || l[0]["dry_run"] != false || l[0]["allowed"] != true {
		t.Errorf("audit = %v", l)
	}
}

// TestCheckPlanPartialDenies: D-66 — a write to a resource a deny covers a
// part of is refused, marked partial.
func TestCheckPlanPartialDenies(t *testing.T) {
	s, _ := newSession(t, "version: 1\nactive: a\nprofiles:\n  a:\n    deny:\n      \"entity:*/registry/entity_id\": [edit]\n", nil)
	_, err := s.CheckPlan(enforce.Plan{Action: "edit entity", Checks: []reqmap.Check{concrete(t, policy.Edit, "entity:light.x")}}, true, false)
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) || len(ref.Denied) != 1 || !ref.Denied[0].Partial {
		t.Fatalf("partial verdict not refused as partial: %v %+v", err, ref)
	}
}

// TestCheckPlanRefusesUnmappableAddresses: an address the grammar refuses is
// denied, never canonicalised (D-69).
func TestCheckPlanRefusesUnmappableAddresses(t *testing.T) {
	s, _ := newSession(t, agentDenyLabelX, nil)
	_, err := s.CheckPlan(enforce.Plan{Action: "x", FailClosed: []enforce.Denial{{Resource: "entity:light. x", Verb: "edit", Rule: "fail-closed: unmappable resource entity:light. x", Line: 9}}}, true, false)
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) || len(ref.Denied) != 1 || ref.Denied[0].Rule != "fail-closed: unmappable resource entity:light. x" || ref.Denied[0].Line != 0 {
		t.Fatalf("unmappable address: %v %+v", err, ref)
	}
}

// TestUnwritableAuditLogRefuses: D-71 — a line that cannot be written refuses
// the write it records, the preview included.
func TestUnwritableAuditLogRefuses(t *testing.T) {
	for _, dryRun := range []bool{true, false} {
		s, dir := newSession(t, agentDenyLabelX, nil)
		// A file where the cache directory should be: nothing can be created
		// below it, whatever the permissions of the test runner.
		if err := os.WriteFile(filepath.Join(dir, "cache"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := s.CheckPlan(enforce.Plan{Action: "x", Checks: []reqmap.Check{concrete(t, policy.Delete, "automation:heating")}}, dryRun, false)
		var ref *enforce.DeniedError
		if !errors.As(err, &ref) || len(ref.Denied) != 1 || ref.Denied[0].Rule != enforce.RuleAuditUnwritable {
			t.Errorf("dry_run=%v: plan with an unwritable audit log = %v %+v", dryRun, err, ref)
		}
		err = s.Check(context.Background(), reqmap.Request{Transport: reqmap.WS, Type: "config/label_registry/create", Params: map[string]any{"name": "x"}})
		if !errors.As(err, &ref) || ref.Denied[0].Rule != enforce.RuleAuditUnwritable {
			t.Errorf("dry_run=%v: request with an unwritable audit log = %v", dryRun, err)
		}
	}
}

// --- the request layer ----------------------------------------------------------------

// TestRequestLayerDecidesWritesOnly: a write is decided and audited; a read, an
// exempt request and an unmapped GET pass unaudited; an unmapped websocket
// type and an unmapped POST are refused (D-74, E-9).
func TestRequestLayerDecidesWritesOnly(t *testing.T) {
	s, dir := newSession(t, agentDenyLabelX, nil)
	ctx := context.Background()
	pass := []reqmap.Request{
		{Transport: reqmap.WS, Type: "config/area_registry/list"},
		{Transport: reqmap.WS, Type: "auth", Params: map[string]any{"access_token": "t"}},
		{Transport: reqmap.Companion, Method: "GET", Path: "/v1/health"},
	}
	for _, r := range pass {
		if err := s.Check(ctx, r); err != nil {
			t.Errorf("%+v refused: %v", r, err)
		}
	}
	// An unmapped read is refused from W-2 on (D-145) — not audited (D-146),
	// and not the invocation's refusal: a command that carried on past it
	// showed less, which the trailer counts.
	readErr := s.Check(ctx, reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/no_such_read"})
	var readRef *enforce.DeniedError
	if !errors.As(readErr, &readRef) || !readRef.Read || !strings.HasPrefix(readRef.Denied[0].Rule, "fail-closed: unmapped request") {
		t.Errorf("an unmapped GET: %v, want a fail-closed read refusal (D-145)", readErr)
	}
	if l := auditLines(t, dir); len(l) != 0 {
		t.Errorf("reads were audited: %v", l)
	}
	if s.RefusedMidRun() != nil {
		t.Fatal("a read refusal was recorded as the invocation's refusal")
	}
	for _, r := range []reqmap.Request{
		{Transport: reqmap.WS, Type: "no_such/command"},
		{Transport: reqmap.REST, Method: "POST", Path: "/api/no_such_write", Body: []byte(`{}`)},
		{Transport: reqmap.REST, Method: "POST", Path: "/api/services/../config", Body: []byte(`{}`)},
	} {
		err := s.Check(ctx, r)
		var ref *enforce.DeniedError
		if !errors.As(err, &ref) || !strings.HasPrefix(ref.Denied[0].Rule, "fail-closed: unmap") {
			t.Errorf("%+v: %v, want a fail-closed refusal", r, err)
		}
	}
	if s.RefusedMidRun() == nil {
		t.Error("the request-layer refusal was not kept for the end of the invocation")
	}
	// A denied write names what was already written.
	err := s.Check(ctx, reqmap.Request{Transport: reqmap.Companion, Method: "POST", Path: "/v1/wireguard/stop", Body: []byte(`{}`)})
	if err != nil {
		t.Fatalf("an allowed write was refused: %v", err)
	}
	err = s.Check(ctx, reqmap.Request{Transport: reqmap.WS, Type: "config/label_registry/create", Params: map[string]any{"name": "x"}})
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) || ref.Layer != enforce.LayerRequest || !slices.Equal(ref.Written, []string{"control system:wireguard"}) {
		t.Fatalf("denied write: %v %+v", err, ref)
	}
	if !strings.Contains(ref.Error(), "already written: control system:wireguard") {
		t.Errorf("request-layer refusal text %q does not name what was written", ref.Error())
	}
	lines := auditLines(t, dir)
	var reqLines int
	for _, l := range lines {
		if l["layer"] == enforce.LayerRequest {
			reqLines++
		}
	}
	if reqLines != len(lines) || len(lines) != 5 {
		t.Errorf("audit = %d lines (%d request), want 5 request lines (3 refusals, 1 allowed write, 1 denied write): %v", len(lines), reqLines, lines)
	}
}

// TestServiceCallReach: the request layer asks the reach of a targeted service
// call (D-59), checks control on every entity in it, and refuses when the
// question cannot be answered (D-73); a call with no target asks nothing.
func TestServiceCallReach(t *testing.T) {
	const body = `version: 1
active: a
profiles:
  a:
    deny:
      "entity:lock.*": [control]
`
	call := func(data string) reqmap.Request {
		return reqmap.Request{Transport: reqmap.REST, Method: "POST", Path: "/api/services/light/turn_on", Body: []byte(data)}
	}
	f := &fakeFetcher{
		live:  []string{"light.a", "light.b", "lock.door"},
		reach: map[string][]string{`{"area_id":["hall"]}`: {"light.a", "lock.door"}},
	}
	s, _ := newSession(t, body, f)
	if err := s.Check(context.Background(), call(`{"area_id":"hall"}`)); err != nil {
		t.Errorf("light.turn_on in the hall (an entity service: only lights count) refused: %v", err)
	}
	if !slices.Contains(f.asked, `reach {"area_id":["hall"]}`) {
		t.Errorf("the reach was not asked: %v", f.asked)
	}
	err := s.Check(context.Background(), call(`{"area_id":"hall","entities":{"lock.door":"x"}}`))
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) || ref.Denied[0].Resource != "entity:lock.door" {
		t.Errorf("an entity named in the data was not checked (D-73): %v", err)
	}

	f2 := &fakeFetcher{live: []string{"light.a"}, reachErr: errors.New("timeout")}
	s2, _ := newSession(t, body, f2)
	err = s2.Check(context.Background(), call(`{"area_id":"hall"}`))
	if !errors.As(err, &ref) || ref.Denied[0].Rule != "fail-closed: reach unknown" {
		t.Errorf("an unanswered reach was not refused: %v", err)
	}
	s3, _ := newSession(t, body, nil)
	err = s3.Check(context.Background(), call(`{"area_id":"hall"}`))
	if !errors.As(err, &ref) || ref.Denied[0].Rule != "fail-closed: reach unknown" {
		t.Errorf("a session that cannot ask was not refused: %v", err)
	}
	f4 := &fakeFetcher{}
	s4, _ := newSession(t, body, f4)
	if err := s4.Check(context.Background(), reqmap.Request{Transport: reqmap.REST, Method: "POST", Path: "/api/services/homeassistant/restart", Body: []byte(`{}`)}); err != nil {
		t.Errorf("a target-less service was refused: %v", err)
	}
	if len(f4.asked) != 0 {
		t.Errorf("a target-less service asked Home Assistant: %v", f4.asked)
	}
}

// TestFetchedFactsAreDroppedAfterAWrite: the registries a cascade is
// enumerated from describe Home Assistant now — a write this invocation sent
// may change them, so they are asked again after it.
func TestFetchedFactsAreDroppedAfterAWrite(t *testing.T) {
	f := &fakeFetcher{reg: &reqmap.Registries{}, complete: true}
	s, _ := newSession(t, "version: 1\nactive: a\nprofiles:\n  a: {}\n", f)
	del := reqmap.Request{Transport: reqmap.WS, Type: "config/label_registry/delete", Params: map[string]any{"label_id": "x"}}
	for range 2 {
		if err := s.Check(context.Background(), del); err != nil {
			t.Fatal(err)
		}
	}
	if n := strings.Count(strings.Join(f.asked, ","), "registries"); n != 2 {
		t.Errorf("registries asked %d times across two writes, want 2 (dropped after each write): %v", n, f.asked)
	}
}

// TestObservedFactsAuthorise: a verified automation id (D-78), an options
// flow's handler (D-83) and a ref/replace dry run (D-77) observed in this
// invocation map the later request that relies on them.
func TestObservedFactsAuthorise(t *testing.T) {
	s, _ := newSession(t, "version: 1\nactive: a\nprofiles:\n  a:\n    deny:\n      \"config_entry:other/options\": [edit]\n", nil)
	ctx := context.Background()
	del := reqmap.Request{Transport: reqmap.Companion, Method: "DELETE", Path: "/v1/config/automation?id=heating"}
	var ref *enforce.DeniedError
	if err := s.Check(ctx, del); !errors.As(err, &ref) || !strings.Contains(ref.Denied[0].Rule, "not verified") {
		t.Fatalf("an unverified automation delete: %v", err)
	}
	// An answer for an alias, not the id asked, verifies nothing.
	s.Observe(reqmap.Request{Transport: reqmap.Companion, Method: "GET", Path: "/v1/config/automation?id=heating"}, []byte(`{"id":"other","content":""}`))
	if err := s.Check(ctx, del); err == nil {
		t.Fatal("an answer naming another id verified the delete")
	}
	s.Observe(reqmap.Request{Transport: reqmap.Companion, Method: "GET", Path: "/v1/config/automation?id=heating"}, []byte(`{"id":"heating","content":""}`))
	if err := s.Check(ctx, del); err != nil {
		t.Errorf("a verified automation delete: %v", err)
	}

	step := reqmap.Request{Transport: reqmap.REST, Method: "POST", Path: "/api/config/config_entries/options/flow/f1", Body: []byte(`{}`)}
	if err := s.Check(ctx, step); err == nil {
		t.Fatal("an options step without its start answer was allowed although a deny covers one entry's options (kind-wide, A-5)")
	}
	s.Observe(reqmap.Request{Transport: reqmap.REST, Method: "POST", Path: "/api/config/config_entries/options/flow", Body: []byte(`{"handler":"mine"}`)},
		[]byte(`{"flow_id":"f1","handler":"mine"}`))
	if err := s.Check(ctx, step); err != nil {
		t.Errorf("an options step of an observed flow: %v", err)
	}

	confirmed := reqmap.Request{Transport: reqmap.Companion, Method: "POST", Path: "/v1/ref/replace", Body: []byte(`{"old":"light.a","new":"light.b","dry_run":false}`)}
	if err := s.Check(ctx, confirmed); err == nil {
		t.Fatal("a confirmed ref/replace without a checked dry run was allowed")
	}
	dry := reqmap.Request{Transport: reqmap.Companion, Method: "POST", Path: "/v1/ref/replace", Body: []byte(`{"old":"light.a","new":"light.b","dry_run":true}`)}
	if err := s.Check(ctx, dry); err != nil {
		t.Fatal(err)
	}
	s.Observe(dry, []byte(`{"status":"dry_run","changes":[{"location":"automations.yaml","path":"[0].x"}]}`))
	if err := s.Check(ctx, confirmed); err != nil {
		t.Errorf("a confirmed ref/replace after its dry run: %v", err)
	}
}

// TestRedirectHopThatMapsDifferentlyIsRefused (D-74): a hop that maps the
// same passes without a new verdict; one that maps elsewhere is refused.
func TestRedirectHopThatMapsDifferentlyIsRefused(t *testing.T) {
	s, dir := newSession(t, agentDenyLabelX, nil)
	orig := reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/states"}
	if err := s.CheckHop(context.Background(), orig, reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/states"}); err != nil {
		t.Errorf("a hop that maps the same: %v", err)
	}
	if l := auditLines(t, dir); len(l) != 0 {
		t.Errorf("a same-mapping hop was audited: %v", l)
	}
	err := s.CheckHop(context.Background(), orig, reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/config"})
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) || ref.Denied[0].Rule != enforce.RuleHopDiffers {
		t.Errorf("a hop that maps differently: %v", err)
	}
}

// TestRecorderSeesPlanAndRequests: R-12's harness — the recorder sees every
// plan and every decided write request.
func TestRecorderSeesPlanAndRequests(t *testing.T) {
	rec := &recorder{}
	s, _ := newSession(t, "version: 1\nactive: a\nprofiles:\n  a: {}\n", nil, func(o *enforce.Options) { o.Recorder = rec })
	if _, err := s.CheckPlan(enforce.Plan{Action: "create label", Checks: []reqmap.Check{kindWide(policy.Create, policy.KindLabel)}}, false, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(context.Background(), reqmap.Request{Transport: reqmap.WS, Type: "config/label_registry/create", Params: map[string]any{"name": "x"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(context.Background(), reqmap.Request{Transport: reqmap.WS, Type: "config/label_registry/list"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(rec.plan, []string{"create label:*"}) || !slices.Equal(rec.requests, []string{"create label:*"}) {
		t.Errorf("recorded plan %q requests %q", rec.plan, rec.requests)
	}
}

type recorder struct{ plan, requests []string }

func (r *recorder) PlanChecked(cs []reqmap.Check) {
	for _, c := range cs {
		r.plan = append(r.plan, c.String())
	}
}

func (r *recorder) RequestChecked(_ reqmap.Request, cs []reqmap.Check) {
	for _, c := range cs {
		r.requests = append(r.requests, c.String())
	}
}

// TestBrokenErrorNamesEveryProblem: D-60's refusal — one `policy_broken:
// <file>:<line>: <message>` per problem, then the owner line (D-117), exit 1.
func TestBrokenErrorNamesEveryProblem(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, policy.FileName), []byte("version: 2\nfoo: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := policy.Load(dir)
	var le *policy.LoadError
	if !errors.As(err, &le) {
		t.Fatal(err)
	}
	be := &enforce.BrokenError{Err: le}
	lines := strings.Split(be.Error(), "\n")
	if len(lines) != len(le.Problems)+1 || len(lines) < 3 {
		t.Fatalf("%d lines for %d problems and the owner line:\n%s", len(lines), len(le.Problems), be.Error())
	}
	if last := lines[len(lines)-1]; last != "policy_broken: "+enforce.BrokenHint {
		t.Errorf("last line %q, want the owner line", last)
	}
	for _, l := range lines[:len(lines)-1] {
		if !strings.HasPrefix(l, "policy_broken: "+filepath.Join(dir, policy.FileName)+":") {
			t.Errorf("line %q is not policy_broken: <file>:<line>: …", l)
		}
	}
	if be.ExitCode() != 1 || be.PolicyClass() != enforce.ClassBroken || !errors.As(be, &le) {
		t.Error("BrokenError: exit code, class or unwrap")
	}
}

// TestServiceCallChecksIsTheRequestLayersMapping (D-59 refined, R-1, R-2,
// D-73): the svc family's plan helper answers exactly what the request layer
// will check when the call is sent — here homeassistant.turn_on on an area
// holding a light and a script: the service, both entities, each domain's own
// service, and the script's run by its registry unique_id (D-84).
func TestServiceCallChecksIsTheRequestLayersMapping(t *testing.T) {
	f := &fakeFetcher{
		live:  []string{"light.a", "script.wake"},
		reach: map[string][]string{`{"area_id":["hall"]}`: {"light.a", "script.wake"}},
		reg:   &reqmap.Registries{Entities: []reqmap.RegistryEntity{{EntityID: "script.wake", UniqueID: "morning"}}}, complete: true,
	}
	s, _ := newSession(t, policyAllowAll, f)
	checks, denial := s.ServiceCallChecks(context.Background(), "homeassistant", "turn_on", map[string]any{"area_id": "hall"})
	if denial != nil {
		t.Fatalf("denial %+v", denial)
	}
	got := reqmap.Result{Checks: checks}.Strings()
	want := []string{
		"control entity:light.a", "control entity:script.wake", "control script:morning (deny only)", "control script:morning/run",
		"control service:homeassistant.turn_on", "control service:light.turn_on", "control service:script.turn_on",
	}
	if !slices.Equal(got, want) {
		t.Errorf("checks\n  got  %q\n  want %q", got, want)
	}
	req := reqmap.Request{Transport: reqmap.REST, Method: "POST", Path: "/api/services/homeassistant/turn_on", Body: []byte(`{"area_id":"hall"}`)}
	if res := reqmap.Classify(req, &reqmap.Facts{Live: &reqmap.LiveStates{EntityIDs: f.live}, Reach: &reqmap.Reach{EntityIDs: f.reach[`{"area_id":["hall"]}`]}, Registries: f.reg}); !slices.Equal(res.Strings(), want) {
		t.Errorf("the request layer maps the same call to %q", res.Strings())
	}

	f2 := &fakeFetcher{live: []string{"light.a"}, reachErr: errors.New("no answer")}
	s2, _ := newSession(t, policyAllowAll, f2)
	if checks, denial := s2.ServiceCallChecks(context.Background(), "light", "turn_on", map[string]any{"area_id": "hall"}); checks != nil || denial == nil || denial.Rule != "fail-closed: reach unknown" {
		t.Errorf("an unanswered reach: checks %v denial %+v", checks, denial)
	}
	f3 := &fakeFetcher{}
	s3, _ := newSession(t, policyAllowAll, f3)
	if checks, denial := s3.ServiceCallChecks(context.Background(), "homeassistant", "restart", nil); denial != nil || len(checks) != 1 || len(f3.asked) != 0 {
		t.Errorf("a target-less call: %v %+v, asked %v", checks, denial, f3.asked)
	}
}

const policyAllowAll = "version: 1\nactive: a\nprofiles:\n  a: {}\n"
