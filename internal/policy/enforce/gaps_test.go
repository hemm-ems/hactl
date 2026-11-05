package enforce_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/enforce"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// The session's refusal text, its bookkeeping of the first refusal, the audit
// of a refused redirect hop, what a refusal names of a request, and the live
// set read from Home Assistant — each pinned so that dropping it fails a test.

// labelCreate is a request the agentDenyLabelX profile refuses (it creates on
// every label id, and one label id is denied).
var labelCreate = reqmap.Request{Transport: reqmap.WS, Type: "config/label_registry/create", Params: map[string]any{"name": "x"}}

// TestRequestRefusalAfterAnAllowedPlanSaysItIsABug (D-89): a request the
// request layer refuses although the command's plan was checked and allowed
// means the plan was one resource short — the refusal says so, for the user to
// report. Without a checked plan it does not.
func TestRequestRefusalAfterAnAllowedPlanSaysItIsABug(t *testing.T) {
	const bug = "although the command's plan allowed it (a hactl bug to report)"
	ctx := context.Background()

	s, _ := newSession(t, agentDenyLabelX, nil)
	if _, err := s.CheckPlan(enforce.Plan{Action: "x"}, false, false); err != nil {
		t.Fatalf("an empty plan was refused: %v", err)
	}
	err := s.Check(ctx, labelCreate)
	if err == nil || !strings.Contains(err.Error(), bug) {
		t.Errorf("after an allowed plan: %v, want it to say %q", err, bug)
	}

	fresh, _ := newSession(t, agentDenyLabelX, nil)
	err = fresh.Check(ctx, labelCreate)
	if err == nil || strings.Contains(err.Error(), bug) || !strings.Contains(err.Error(), "— refused at the request layer; already written: nothing") {
		t.Errorf("with no plan checked: %v", err)
	}
}

// TestRefusedMidRunIsTheFirstRefusal: a best-effort command that carries on
// past two request-layer refusals ends with the first one, not the last
// (whose `written` would name more than the first refusal saw).
func TestRefusedMidRunIsTheFirstRefusal(t *testing.T) {
	s, _ := newSession(t, agentDenyLabelX, nil)
	ctx := context.Background()
	first := s.Check(ctx, labelCreate)
	if first == nil {
		t.Fatal("the denied request was allowed")
	}
	if err := s.Check(ctx, reqmap.Request{Transport: reqmap.Companion, Method: "POST", Path: "/v1/wireguard/stop", Body: []byte(`{}`)}); err != nil {
		t.Fatalf("an allowed write was refused: %v", err)
	}
	second := s.Check(ctx, labelCreate)
	if second == nil || errors.Is(second, first) {
		t.Fatal("the second refusal is not a refusal of its own")
	}
	if got := s.RefusedMidRun(); got == nil || !errors.Is(got, first) {
		t.Errorf("RefusedMidRun = %v, want the first refusal %v", got, first)
	}
}

// TestRefusedRedirectHopIsAudited (D-71): a redirect hop refused because it
// maps differently is a write decision like any other — one request-layer line
// in the audit log, with the hop's rule.
func TestRefusedRedirectHopIsAudited(t *testing.T) {
	s, dir := newSession(t, agentDenyLabelX, nil)
	orig := reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/states"}
	if err := s.CheckHop(context.Background(), orig, reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/config"}); err == nil {
		t.Fatal("a hop that maps differently was allowed")
	}
	lines := auditLines(t, dir)
	if len(lines) != 1 || lines[0]["layer"] != enforce.LayerRequest || lines[0]["rule"] != enforce.RuleHopDiffers ||
		lines[0]["allowed"] != false || lines[0]["resource"] != "rest GET /api/config" {
		t.Errorf("audit = %v, want one refused request-layer line for the hop", lines)
	}
}

// TestRefusalNeverCarriesTheQuery: a refused request is named by its
// transport, method and route — never its query, which may carry content —
// in the refusal and in the audit log alike.
func TestRefusalNeverCarriesTheQuery(t *testing.T) {
	s, dir := newSession(t, agentDenyLabelX, nil)
	err := s.Check(context.Background(), reqmap.Request{Transport: reqmap.Companion, Method: "DELETE", Path: "/v1/no_such_route?secret=hunter2&path=x.yaml"})
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) {
		t.Fatalf("an unmapped mutating request was allowed: %v", err)
	}
	if ref.Denied[0].Resource != "companion DELETE /v1/no_such_route" || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("refusal names %q (%v)", ref.Denied[0].Resource, err)
	}
	for _, l := range auditLines(t, dir) {
		if l["resource"] != "companion DELETE /v1/no_such_route" || strings.Contains(fmt.Sprint(l), "hunter2") {
			t.Errorf("audit line carries the query: %v", l)
		}
	}
}

// TestAuditLogRotatesExactlyAtTheBound (D-71): "at 10 MiB the file is
// renamed" — a log of exactly the bound is rotated by the next append.
func TestAuditLogRotatesExactlyAtTheBound(t *testing.T) {
	dir := t.TempDir()
	a := enforce.NewAudit(dir)
	if err := os.MkdirAll(filepath.Dir(a.Path()), 0o750); err != nil {
		t.Fatal(err)
	}
	full := strings.Repeat("x", int(enforce.AuditRotateBytes-1)) + "\n"
	if err := os.WriteFile(a.Path(), []byte(full), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Append([]enforce.AuditLine{{Time: "t", Command: "hactl label create x", Layer: enforce.LayerCommand}}); err != nil {
		t.Fatal(err)
	}
	rotated, err := os.Stat(a.Path() + ".1")
	if err != nil || rotated.Size() != enforce.AuditRotateBytes {
		t.Fatalf("a log of exactly the bound was not rotated: %v", err)
	}
	cur, err := os.ReadFile(a.Path())
	if err != nil || strings.Count(string(cur), "\n") != 1 {
		t.Errorf("the new log holds %d bytes, want the one line appended after the rotation (%v)", len(cur), err)
	}
}

// TestHAFetcherRefusesAStateWithoutAnEntityID (H-7): a /api/states answer
// holding a state without an entity_id is unusable — the live set stays
// unread rather than holding "" — so a service call that needs it is refused
// with `fail-closed: reach unknown`.
func TestHAFetcherRefusesAStateWithoutAnEntityID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/states" {
			_, _ = fmt.Fprint(w, `[{"entity_id":"light.a","state":"on","attributes":{}},{"state":"on","attributes":{}}]`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	f := enforce.NewHAFetcher(context.Background(), srv.URL, "t", nil)
	t.Cleanup(f.Close)
	if live, err := f.LiveStates(context.Background()); err == nil {
		t.Fatalf("a state without an entity_id was read as %v", live.EntityIDs)
	}

	s, _ := newSession(t, policyAllowAll, f)
	// Data without a target selector: the live set alone decides which of
	// its strings name an entity, and no reach question is asked.
	_, denial := s.ServiceCallChecks(context.Background(), "notify", "notify", map[string]any{"message": "light.a"})
	if denial == nil || denial.Rule != "fail-closed: reach unknown" {
		t.Errorf("a call needing the live set: denial %+v, want fail-closed: reach unknown", denial)
	}
}

// TestHAFetcherReadsMembersAndAutomationIDs (D-106, D-107): the live set
// carries each state's `entity_id` attribute — a list or one comma-separated
// string — and an automation's `id` attribute; no other domain's `id`.
func TestHAFetcherReadsMembersAndAutomationIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `[
		 {"entity_id":"light.group","attributes":{"entity_id":["light.a","light.b"]}},
		 {"entity_id":"scene.s","attributes":{"entity_id":"light.a, lock.x","id":"170"}},
		 {"entity_id":"automation.heat","attributes":{"id":"prod_heating"}},
		 {"entity_id":"automation.noid","attributes":{}},
		 {"entity_id":"light.a","attributes":{}}]`)
	}))
	t.Cleanup(srv.Close)
	f := enforce.NewHAFetcher(context.Background(), srv.URL, "t", nil)
	t.Cleanup(f.Close)
	live, err := f.LiveStates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(live.Members); got != "map[light.group:[light.a light.b] scene.s:[light.a lock.x]]" {
		t.Errorf("members = %s", got)
	}
	if got := fmt.Sprint(live.AutomationIDs); got != "map[automation.heat:prod_heating]" {
		t.Errorf("automation ids = %s", got)
	}
	if len(live.EntityIDs) != 5 {
		t.Errorf("entity ids = %v", live.EntityIDs)
	}
}
