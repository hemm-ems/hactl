// Stage-2a request-mapping tests for H-29, written from the contract only
// (dev/surfaces/policy.manifest, INVARIANTS.md H-29, docs/manual.md
// "#### Enforcement (writes)", D-59, D-66, D-72..D-84) before any classifier
// exists. The vectors are data: testdata/policy/requests/vectors.json (one or
// more per ws/rest/companion manifest row, plus negative vectors) and
// testdata/policy/requests/dashboard_diff.json.
//
// WIRING (integrated 2026-11-01). The build tag and the contract stub are gone;
// the ADAPTER section below translates the vectors to the real API
// (reqmap.Classify(Request, *Facts), reqmap.DiffDashboard) and nothing else.
// History: this file was built only with `-tags p2a_contract`. Until the
// classifier lands it compiles against the contract stub
// (internal/policy/reqmap/contract_stub_p2a.go, `-tags p2a_stub`):
//
//	go test -tags 'p2a_contract p2a_stub' ./internal/policy/reqmap/
//
// and fails — which is the point: watched to fail before it is cited. The
// integrator deletes the stub, removes the `//go:build p2a_contract` line, and
// — only if the real API differs from the stub's — edits the ADAPTER section
// below and nothing else.
package reqmap_test

import (
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// ===========================================================================
// ADAPTER — the only part that names the classifier's API.
// ===========================================================================

// got is the classifier's answer in the vectors' terms.
type got struct {
	Row         string
	Disposition string
	Checks      []string // "verb resource" (+ " kind_wide"), sorted, deduplicated
	Reason      string
}

// The five dispositions, in the vectors' spelling.
const (
	dWrite        = "write"
	dReadDeferred = "read-deferred"
	dExempt       = "exempt"
	dUnmapped     = "unmapped"
	dUnmappable   = "unmappable"
)

func dispositionName(d reqmap.Disposition) string {
	switch d {
	case reqmap.Write:
		return dWrite
	case reqmap.ReadDeferred:
		return dReadDeferred
	case reqmap.Exempt:
		return dExempt
	case reqmap.Unmapped:
		return dUnmapped
	case reqmap.Unmappable:
		return dUnmappable
	}
	return d.String()
}

func classify(t *testing.T, r vecRequest, f vecFacts) got {
	t.Helper()
	facts := f.build(t)
	res := reqmap.Classify(r.build(t), &facts)
	reason := res.Rule
	if res.Detail != "" {
		reason += ": " + res.Detail
	}
	return got{
		Row:         res.Row,
		Disposition: dispositionName(res.Disposition),
		Checks:      normChecks(res.Checks),
		Reason:      reason,
	}
}

func dashboardDiff(urlPath string, stored, updated json.RawMessage) ([]string, error) {
	cs, err := reqmap.DiffDashboard(urlPath, stored, updated)
	return normChecks(cs), err
}

func normChecks(cs []reqmap.Check) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		s := c.String()
		if c.KindWide {
			s += " kind_wide"
		}
		out = append(out, s)
	}
	sort.Strings(out)
	return slices.Compact(out)
}

func (r vecRequest) build(t *testing.T) reqmap.Request {
	t.Helper()
	req := reqmap.Request{Transport: reqmap.Transport(r.Transport), Method: r.Method, Path: r.Path}
	if r.Message != nil {
		req.Params = map[string]any{}
		for k, v := range r.Message {
			if k == "type" {
				typ, ok := v.(string)
				if !ok {
					t.Fatalf("vector request: message type %v is not a string", v)
				}
				req.Type = typ
				continue
			}
			req.Params[k] = v
		}
	}
	switch {
	case r.JSON != nil:
		req.Body = []byte(r.JSON)
	case r.Body != nil:
		req.Body = []byte(*r.Body)
	}
	return req
}

func (f vecFacts) build(t *testing.T) reqmap.Facts {
	t.Helper()
	out := reqmap.Facts{
		PolicyHash:          f.PolicyHash,
		DashboardIDs:        f.DashboardIDs,
		OptionsFlowHandlers: f.OptionsFlowHandlers,
	}
	if f.LiveEntities != nil {
		out.Live = &reqmap.LiveStates{EntityIDs: f.LiveEntities}
	}
	if f.DashboardConfigs != nil {
		out.Dashboards = map[string]reqmap.StoredDashboard{}
		for u, cfg := range f.DashboardConfigs {
			sd := reqmap.StoredDashboard{}
			if len(cfg) > 0 && string(cfg) != "null" {
				sd.Config = cfg
			}
			out.Dashboards[u] = sd
		}
	}
	if f.VerifiedAutomationIDs != nil {
		out.VerifiedAutomationIDs = map[string]bool{}
		for _, id := range f.VerifiedAutomationIDs {
			out.VerifiedAutomationIDs[id] = true
		}
	}
	reg := f.Registry
	if reg.Entities != nil || reg.Devices != nil || reg.Areas != nil {
		out.Registries = &reqmap.Registries{}
	}
	for _, e := range reg.Entities {
		out.Registries.Entities = append(out.Registries.Entities, reqmap.RegistryEntity{
			EntityID: e.EntityID, UniqueID: e.UniqueID,
			AreaID: deref(e.AreaID), Labels: e.Labels, ConfigEntryID: e.ConfigEntryID,
		})
	}
	for _, d := range reg.Devices {
		out.Registries.Devices = append(out.Registries.Devices, reqmap.RegistryDevice{
			ID: d.ID, AreaID: deref(d.AreaID), Labels: d.Labels, ConfigEntries: d.ConfigEntries,
		})
	}
	for _, a := range reg.Areas {
		out.Registries.Areas = append(out.Registries.Areas, reqmap.RegistryArea{
			ID: a.AreaID, FloorID: a.FloorID, Labels: a.Labels,
		})
	}
	if f.Reach != nil {
		out.Reach = &reqmap.Reach{EntityIDs: f.Reach.Entities}
		if f.Reach.Error != "" {
			out.Reach.Err = errors.New(f.Reach.Error)
		}
	}
	for _, p := range f.RefReplacePlans {
		out.RefReplacePlans = append(out.RefReplacePlans, reqmap.RefReplacePlan{
			Old: p.Old, New: p.New, PolicyHash: p.PolicyHash, Changes: planChanges(t, p.Checks),
		})
	}
	return out
}

// planChanges expresses a plan's checked (resource, verb) set as the changes
// the real API takes: each `edit file:<path>` is a change in that file; each
// `edit <automation|script|template>:<id>/config` is a change whose typed
// owner is that entry, placed in the plan's first file. Any other check is a
// vector this adapter cannot express.
func planChanges(t *testing.T, checks []vecCheck) []reqmap.PlanChange {
	t.Helper()
	var files []string
	var owners []policy.Resource
	for _, c := range checks {
		r, err := policy.ParseResource(c.Resource)
		if err != nil {
			t.Fatalf("vector fact: plan resource %q: %v", c.Resource, err)
		}
		if c.Verb != "edit" || c.KindWide {
			t.Fatalf("vector fact: plan check %v is not an edit the adapter can express", c)
		}
		switch {
		case r.Kind == policy.KindFile:
			files = append(files, r.ID)
		case (r.Kind == policy.KindAutomation || r.Kind == policy.KindScript || r.Kind == policy.KindTemplate) && r.Sub == "config":
			owners = append(owners, r)
		default:
			t.Fatalf("vector fact: plan check %v is not an edit the adapter can express", c)
		}
	}
	out := make([]reqmap.PlanChange, 0, len(files)+len(owners))
	for _, f := range files {
		out = append(out, reqmap.PlanChange{Location: f})
	}
	for _, o := range owners {
		if len(files) == 0 {
			t.Fatalf("vector fact: plan owner %v without a file", o)
		}
		out = append(out, reqmap.PlanChange{Location: files[0], Owner: &reqmap.TypedOwner{Kind: o.Kind, ID: o.ID}})
	}
	return out
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// ===========================================================================
// The vector files.
// ===========================================================================

type vecCheck struct {
	Verb     string `json:"verb"`
	Resource string `json:"resource"`
	KindWide bool   `json:"kind_wide"`
}

func (c vecCheck) String() string {
	s := c.Verb + " " + c.Resource
	if c.KindWide {
		s += " kind_wide"
	}
	return s
}

func wantChecks(cs []vecCheck) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.String())
	}
	sort.Strings(out)
	return slices.Compact(out)
}

type vecRequest struct {
	Transport string          `json:"transport"`
	Message   map[string]any  `json:"message"`
	Method    string          `json:"method"`
	Path      string          `json:"path"`
	JSON      json.RawMessage `json:"json"`
	Body      *string         `json:"body"`
}

type vecFacts struct {
	PolicyHash   string   `json:"policy_hash"`
	LiveEntities []string `json:"live_entities"`
	Registry     struct {
		Entities []struct {
			EntityID      string   `json:"entity_id"`
			UniqueID      string   `json:"unique_id"`
			Platform      string   `json:"platform"`
			DeviceID      string   `json:"device_id"`
			AreaID        *string  `json:"area_id"`
			Labels        []string `json:"labels"`
			ConfigEntryID string   `json:"config_entry_id"`
		} `json:"entities"`
		Devices []struct {
			ID            string   `json:"id"`
			AreaID        *string  `json:"area_id"`
			Labels        []string `json:"labels"`
			ConfigEntries []string `json:"config_entries"`
		} `json:"devices"`
		Areas []struct {
			AreaID  string   `json:"area_id"`
			FloorID string   `json:"floor_id"`
			Labels  []string `json:"labels"`
		} `json:"areas"`
	} `json:"registry"`
	Reach *struct {
		Entities []string `json:"entities"`
		Error    string   `json:"error"`
	} `json:"reach"`
	DashboardConfigs      map[string]json.RawMessage `json:"dashboard_configs"`
	DashboardIDs          map[string]string          `json:"dashboard_ids"`
	VerifiedAutomationIDs []string                   `json:"verified_automation_ids"`
	OptionsFlowHandlers   map[string]string          `json:"options_flow_handlers"`
	RefReplacePlans       []struct {
		Old        string     `json:"old"`
		New        string     `json:"new"`
		PolicyHash string     `json:"policy_hash"`
		Checks     []vecCheck `json:"checks"`
	} `json:"ref_replace_plans"`
}

type vecExpect struct {
	Disposition    string     `json:"disposition"`
	DispositionAny []string   `json:"disposition_any"`
	Row            *string    `json:"row"`
	Checks         []vecCheck `json:"checks"`
	ReasonContains string     `json:"reason_contains"`
	Refused        *bool      `json:"refused"`
}

type requestVector struct {
	ID       string                     `json:"id"`
	Why      string                     `json:"why"`
	FactsRef string                     `json:"facts_ref"`
	Facts    map[string]json.RawMessage `json:"facts"`
	Request  vecRequest                 `json:"request"`
	Expect   vecExpect                  `json:"expect"`
}

type redirectVector struct {
	ID       string                     `json:"id"`
	Why      string                     `json:"why"`
	FactsRef string                     `json:"facts_ref"`
	Facts    map[string]json.RawMessage `json:"facts"`
	From     vecRequest                 `json:"from"`
	To       vecRequest                 `json:"to"`
	Same     bool                       `json:"same"`
}

type requestVectorFile struct {
	Facts     map[string]map[string]json.RawMessage `json:"facts"`
	Vectors   []requestVector                       `json:"vectors"`
	Redirects []redirectVector                      `json:"redirects"`
}

func requestsDir(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return filepath.Join(dir, "testdata", "policy", "requests")
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
}

func loadRequestVectors(t *testing.T) requestVectorFile {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(requestsDir(t), "vectors.json"))
	if err != nil {
		t.Fatalf("reading the request vectors: %v", err)
	}
	var vf requestVectorFile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&struct {
		*requestVectorFile

		About string `json:"about"`
	}{requestVectorFile: &vf}); err != nil {
		t.Fatalf("decoding the request vectors: %v", err)
	}
	if len(vf.Vectors) == 0 {
		t.Fatal("the request vector table is empty")
	}
	return vf
}

// factsFor overlays a vector's own facts on the named shared set, key by key.
func (vf requestVectorFile) factsFor(t *testing.T, ref string, own map[string]json.RawMessage) vecFacts {
	t.Helper()
	merged := map[string]json.RawMessage{}
	if ref != "" {
		base, ok := vf.Facts[ref]
		if !ok {
			t.Fatalf("facts_ref %q names no shared fact set", ref)
		}
		maps.Copy(merged, base)
	}
	maps.Copy(merged, own)
	raw, err := json.Marshal(merged)
	if err != nil {
		t.Fatal(err)
	}
	var f vecFacts
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decoding the vector's facts: %v", err)
	}
	return f
}

// refusedInPhase2 is D-74's consequence of a mapping while a profile is in
// effect: an unmappable request is refused; an unmapped one is refused when it
// can change anything — every websocket type, every HTTP method but GET. An
// unmapped GET is still sent in phase 2.
func refusedInPhase2(g got, r vecRequest) bool {
	switch g.Disposition {
	case dUnmappable:
		return true
	case dUnmapped:
		return r.Transport == "ws" || r.Method != http.MethodGet
	}
	return false
}

// ===========================================================================
// Tests.
// ===========================================================================

// TestEveryWriteRequestHasAMapping — H-29's request layer, one case per
// mapped row of dev/surfaces/policy.manifest plus the negative vectors: a
// request in, the row it matches, its disposition and its (resource, verb) set
// out.
//
// Beyond each vector's own expectation it holds every answer to the shape the
// contract gives a disposition: an unmapped request names no row; a write,
// read or exempt request names the row it matched; a read row never carries a
// write-verb check (phase 2 must never refuse a read by a write check, D-74);
// an exempt row carries no check at all. And the mapping is a function of the
// request and the invocation's facts alone: mapping the same request twice —
// as a retry rebuilding it would — gives the same answer (one verdict per
// logical request, D-74).
func TestEveryWriteRequestHasAMapping(t *testing.T) {
	vf := loadRequestVectors(t)
	for _, v := range vf.Vectors {
		t.Run(v.ID, func(t *testing.T) {
			facts := vf.factsFor(t, v.FactsRef, v.Facts)
			g := classify(t, v.Request, facts)
			again := classify(t, v.Request, facts)
			if !reflect.DeepEqual(g, again) {
				t.Errorf("%s: mapping the same request twice gave two answers:\n first %+v\nsecond %+v", v.Why, g, again)
			}

			assertExpectation(t, v, g)
			assertShape(t, g)
		})
	}
}

// assertExpectation holds an answer to what its vector expects.
func assertExpectation(t *testing.T, v requestVector, g got) {
	t.Helper()
	e := v.Expect
	if e.Disposition != "" && g.Disposition != e.Disposition {
		t.Errorf("%s:\n disposition %q, want %q (row %q, reason %q)", v.Why, g.Disposition, e.Disposition, g.Row, g.Reason)
	}
	if len(e.DispositionAny) > 0 && !slices.Contains(e.DispositionAny, g.Disposition) {
		t.Errorf("%s:\n disposition %q, want one of %v", v.Why, g.Disposition, e.DispositionAny)
	}
	if e.Row != nil && g.Row != *e.Row {
		t.Errorf("%s:\n row %q, want %q", v.Why, g.Row, *e.Row)
	}
	if e.Disposition == dWrite {
		if want := wantChecks(e.Checks); !slices.Equal(g.Checks, want) {
			t.Errorf("%s:\n checks\n  %s\n want\n  %s\n missing %v\n extra   %v", v.Why,
				strings.Join(g.Checks, "\n  "), strings.Join(want, "\n  "),
				minus(want, g.Checks), minus(g.Checks, want))
		}
	}
	if e.ReasonContains != "" && !strings.Contains(g.Reason, e.ReasonContains) {
		t.Errorf("%s:\n reason %q does not name %q", v.Why, g.Reason, e.ReasonContains)
	}
	if e.Refused != nil {
		if r := refusedInPhase2(g, v.Request); r != *e.Refused {
			t.Errorf("%s:\n refused in phase 2 = %v, want %v (disposition %q)", v.Why, r, *e.Refused, g.Disposition)
		}
	}
}

// assertShape holds every answer, whatever its vector asserted, to the shape
// the contract gives a disposition.
func assertShape(t *testing.T, g got) {
	t.Helper()
	switch g.Disposition {
	case dUnmapped:
		if g.Row != "" {
			t.Errorf("an unmapped request names row %q", g.Row)
		}
	case dWrite, dReadDeferred, dExempt:
		if g.Row == "" {
			t.Errorf("a %s answer names no manifest row", g.Disposition)
		}
	case dUnmappable:
	default:
		t.Errorf("disposition %q is none of the five", g.Disposition)
	}
	if g.Disposition == dReadDeferred {
		for _, c := range g.Checks {
			if !strings.HasPrefix(c, "read ") {
				t.Errorf("read row %q carries a write check %q", g.Row, c)
			}
		}
	}
	if g.Disposition == dExempt && len(g.Checks) > 0 {
		t.Errorf("exempt row %q carries checks %v", g.Row, g.Checks)
	}
}

// TestRedirectHopIsMappedAgain — a same-origin redirect hop is mapped again,
// and a hop that maps differently is refused (D-74). The classifier's part is
// the second mapping: these pairs pin which hops map the same (followed) and
// which do not (refused).
func TestRedirectHopIsMappedAgain(t *testing.T) {
	vf := loadRequestVectors(t)
	if len(vf.Redirects) == 0 {
		t.Fatal("no redirect vector")
	}
	for _, v := range vf.Redirects {
		t.Run(v.ID, func(t *testing.T) {
			facts := vf.factsFor(t, v.FactsRef, v.Facts)
			from := classify(t, v.From, facts)
			to := classify(t, v.To, facts)
			same := from.Row == to.Row && from.Disposition == to.Disposition && slices.Equal(from.Checks, to.Checks)
			if same != v.Same {
				t.Errorf("%s:\n from %+v\n   to %+v\n maps the same = %v, want %v", v.Why, from, to, same, v.Same)
			}
			if v.Same && from.Disposition != dWrite {
				t.Errorf("%s: the pair is meant to be a mapped write, got %q", v.Why, from.Disposition)
			}
		})
	}
}

type diffVector struct {
	ID      string          `json:"id"`
	Why     string          `json:"why"`
	URLPath string          `json:"url_path"`
	Stored  json.RawMessage `json:"stored"`
	Updated json.RawMessage `json:"updated"`
	Expect  struct {
		Checks []vecCheck `json:"checks"`
		Error  bool       `json:"error"`
	} `json:"expect"`
}

// TestDashboardViewDiff — lovelace/config/save's mapping as a pure function
// over (stored config, new config): edit/create/delete per changed, added or
// removed view addressed by its path; the dashboard itself for non-view keys,
// for path-less views and when there is no stored config; an error — so the
// save is unmappable — for a view path that is not one grammar segment
// (dev/surfaces/policy.manifest, D-66 as refined, D-69, D-72).
func TestDashboardViewDiff(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(requestsDir(t), "dashboard_diff.json"))
	if err != nil {
		t.Fatalf("reading the dashboard diff vectors: %v", err)
	}
	var file struct {
		About   string       `json:"about"`
		Vectors []diffVector `json:"vectors"`
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		t.Fatalf("decoding the dashboard diff vectors: %v", err)
	}
	if len(file.Vectors) == 0 {
		t.Fatal("the dashboard diff table is empty")
	}
	for _, v := range file.Vectors {
		t.Run(v.ID, func(t *testing.T) {
			stored := v.Stored
			if string(stored) == "null" {
				stored = nil
			}
			checks, err := dashboardDiff(v.URLPath, stored, v.Updated)
			if v.Expect.Error {
				if err == nil {
					t.Errorf("%s: want an error, got checks %v", v.Why, checks)
				}
				return
			}
			if err != nil {
				t.Errorf("%s: unexpected error %v", v.Why, err)
				return
			}
			if want := wantChecks(v.Expect.Checks); !slices.Equal(checks, want) {
				t.Errorf("%s:\n checks %v\n want   %v", v.Why, checks, want)
			}
		})
	}
}

func minus(a, b []string) []string {
	var out []string
	for _, s := range a {
		if !slices.Contains(b, s) {
			out = append(out, s)
		}
	}
	return out
}
