package enforce_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
	"github.com/hemm-ems/hactl/internal/policy/reqgate"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// The read half of the request layer (W-2), held to
// testdata/policy/requests/read_filters.json: one or more vectors per read row
// of dev/surfaces/policy.manifest, each a request under a named policy and
// either the refusal before it is sent or the filtered answer with its hidden
// and masked counts.

type readVector struct {
	ID      string            `json:"id"`
	Why     string            `json:"why"`
	Row     string            `json:"row"`
	Policy  string            `json:"policy"`
	Request vecReadRequest    `json:"request"`
	Answer  json.RawMessage   `json:"answer"`
	Files   map[string]string `json:"files"`
	Expect  struct {
		Refused string          `json:"refused"`
		Answer  json.RawMessage `json:"answer"`
		Hidden  int             `json:"hidden"`
		Masked  int             `json:"masked"`
	} `json:"expect"`
}

type vecReadRequest struct {
	Transport string         `json:"transport"`
	Message   map[string]any `json:"message"`
	Method    string         `json:"method"`
	Path      string         `json:"path"`
	Body      string         `json:"body"`
}

func (r vecReadRequest) build() reqmap.Request {
	req := reqmap.Request{Transport: reqmap.Transport(r.Transport), Method: r.Method, Path: r.Path}
	if r.Body != "" {
		req.Body = []byte(r.Body)
	}
	if r.Message != nil {
		req.Params = map[string]any{}
		for k, v := range r.Message {
			if k == "type" {
				req.Type, _ = v.(string)
				continue
			}
			req.Params[k] = v
		}
	}
	return req
}

type readVectorFile struct {
	About    string            `json:"about"`
	Policies map[string]string `json:"policies"`
	Vectors  []readVector      `json:"vectors"`
}

func loadReadVectors(t *testing.T) readVectorFile {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the working directory")
		}
		dir = parent
	}
	raw, err := os.ReadFile(filepath.Join(dir, "testdata", "policy", "requests", "read_filters.json")) //nolint:gosec // fixed path under the repository root
	if err != nil {
		t.Fatal(err)
	}
	var f readVectorFile
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		t.Fatalf("decoding the read vectors: %v", err)
	}
	return f
}

// fileFetcher is a fetcher that also reads config files (the typed owner of
// a wide read's hit, D-142).
type fileFetcher struct {
	fakeFetcher

	files map[string]string
	read  []string
}

func (f *fileFetcher) ConfigFile(ctx context.Context, loc string) ([]byte, error) {
	if !reqgate.IsOwn(ctx) {
		return nil, errors.New("a config file was read without the session's own mark")
	}
	f.read = append(f.read, loc)
	c, ok := f.files[loc]
	if !ok {
		return nil, errors.New("no such file")
	}
	return []byte(c), nil
}

func jsonEqual(a, b []byte) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	return reflect.DeepEqual(x, y)
}

// TestEveryReadRowIsEnforced — W-2's contract, one vector or more per read row:
// what a read names itself is refused before it is sent when denied (D-138);
// its answer is filtered before any command decodes it — rows dropped and
// counted, parts masked `<denied>` and counted (D-139, D-140), a row hactl
// cannot address hidden (D-141), a wide read's hit decided by its file and
// typed owner (D-142), `tpl eval` by system:template (D-143), files by
// file:<path> (D-144). And the table is closed: every read row of the
// classifier has a vector here, and every vector's request maps to its row.
func TestEveryReadRowIsEnforced(t *testing.T) {
	vf := loadReadVectors(t)
	covered := map[string]bool{}
	for _, v := range vf.Vectors {
		covered[v.Row] = true
		t.Run(v.ID, func(t *testing.T) {
			body, ok := vf.Policies[v.Policy]
			if !ok {
				t.Fatalf("policy %q is not in the file", v.Policy)
			}
			runReadVector(t, v, body)
		})
	}
	for _, r := range reqmap.Rows() {
		if r.Disposition == reqmap.Read && !covered[r.Key] {
			t.Errorf("read row %q has no vector in read_filters.json", r.Key)
		}
	}
}

// runReadVector runs one vector: the request is checked, its answer filtered,
// and the outcome held to the vector — then the same request as the session's
// own question, which is never refused or filtered.
func runReadVector(t *testing.T, v readVector, body string) {
	t.Helper()
	s, dir := newSession(t, body, &fileFetcher{files: v.Files})
	req := v.Request.build()
	if res := reqmap.Classify(req, nil); res.Row != v.Row || res.Disposition != reqmap.Read {
		t.Fatalf("%s: the request maps to %q (%s), want the read row %q", v.Why, res.Row, res.Disposition, v.Row)
	}
	ctx := context.Background()
	err := s.Check(ctx, req)
	var out []byte
	if err == nil {
		out, err = s.Filter(ctx, req, v.Answer)
	}
	if v.Expect.Refused != "" {
		assertReadRefused(t, s, v, out, err)
	} else {
		assertReadFiltered(t, s, v, out, err)
	}
	if l := auditLines(t, dir); len(l) != 0 {
		t.Errorf("%s: a read was audited (D-146): %v", v.Why, l)
	}
	own := reqgate.Own(ctx)
	if err := s.Check(own, req); err != nil {
		t.Errorf("%s: the session's own read was refused: %v", v.Why, err)
	}
	if v.Answer != nil {
		if got, err := s.Filter(own, req, v.Answer); err != nil || string(got) != string(v.Answer) {
			t.Errorf("%s: the session's own read was filtered: %s, %v", v.Why, got, err)
		}
	}
}

func assertReadRefused(t *testing.T, s *enforce.Session, v readVector, out []byte, err error) {
	t.Helper()
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) || !ref.Read {
		t.Fatalf("%s: got %v (answer %s), want a read refusal naming %q", v.Why, err, out, v.Expect.Refused)
	}
	if !strings.Contains(ref.Error(), v.Expect.Refused) || ref.PolicyClass() != enforce.ClassDenied || ref.ExitCode() != 1 {
		t.Errorf("%s: refusal %q (class %s), want policy_denied naming %q", v.Why, ref.Error(), ref.PolicyClass(), v.Expect.Refused)
	}
	if hidden, masked, refused := s.ReadCounts(); refused != 1 || hidden != 0 || masked != 0 {
		t.Errorf("%s: counts hidden %d masked %d refused %d, want one refused read", v.Why, hidden, masked, refused)
	}
	if s.RefusedMidRun() != nil {
		t.Errorf("%s: a refused read became the invocation's refusal", v.Why)
	}
}

func assertReadFiltered(t *testing.T, s *enforce.Session, v readVector, out []byte, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s: refused: %v", v.Why, err)
	}
	if !jsonEqual(out, v.Expect.Answer) {
		t.Errorf("%s:\n answer %s\n   want %s", v.Why, out, v.Expect.Answer)
	}
	if hidden, masked, _ := s.ReadCounts(); hidden != v.Expect.Hidden || masked != v.Expect.Masked {
		t.Errorf("%s: hidden %d masked %d, want %d and %d", v.Why, hidden, masked, v.Expect.Hidden, v.Expect.Masked)
	}
	if v.Expect.Hidden == 0 && v.Expect.Masked == 0 && string(out) != string(v.Answer) {
		t.Errorf("%s: an answer the filter changed nothing in was not returned byte for byte:\n %s\n %s", v.Why, out, v.Answer)
	}
}

// TestReadFilterKeepsNumbersAsSent — a filtered answer is re-encoded, but a
// number in it is the number Home Assistant sent (H-21's sibling rule): a
// masked row next to an unmasked one keeps `1.10`, `0.50` and a big integer
// byte for byte.
func TestReadFilterKeepsNumbersAsSent(t *testing.T) {
	s, _ := newSession(t, `version: 1
active: a
profiles:
  a:
    deny:
      "entity:lock.*": [read]
      "entity:sensor.x/attr/secret": [read]
`, nil)
	req := reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/states"}
	in := `[{"entity_id":"lock.a","state":"x"},{"entity_id":"sensor.x","state":"1.10","attributes":{"secret":1,"p":1.10,"q":0.50,"big":12345678901234567890}}]`
	out, err := s.Filter(context.Background(), req, []byte(in))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`1.10`, `0.50`, `12345678901234567890`, `"secret":"<denied>"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("filtered answer %s lost %s", out, want)
		}
	}
	if strings.Contains(string(out), "lock.a") {
		t.Errorf("a denied entity survived: %s", out)
	}
}

// TestReadFilterRefusesAnAnswerItCannotRead — an answer whose shape the filter
// cannot read is refused, never passed unchecked (D-141): a listing that is
// not a list, a row that is not an object, an object answer that is not one.
func TestReadFilterRefusesAnAnswerItCannotRead(t *testing.T) {
	s, _ := newSession(t, `version: 1
active: a
profiles:
  a:
    deny:
      "entity:lock.*": [read]
`, nil)
	for _, c := range []struct {
		req    reqmap.Request
		answer string
	}{
		{reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/states"}, `{"entity_id":"lock.a"}`},
		{reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/states"}, `["lock.a"]`},
		{reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/history/period/x"}, `[{"entity_id":"lock.a"}]`},
		{reqmap.Request{Transport: reqmap.WS, Type: "recorder/statistics_during_period", Params: map[string]any{}}, `[]`},
		{reqmap.Request{Transport: reqmap.Companion, Method: "GET", Path: "/v1/config/helpers"}, `[]`},
		{reqmap.Request{Transport: reqmap.Companion, Method: "GET", Path: "/v1/config/helpers"}, `{"helpers":{"a":1}}`},
		{reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/services"}, `[{"domain":"x","services":[]}]`},
		{reqmap.Request{Transport: reqmap.Companion, Method: "GET", Path: "/v1/ref/scan"}, `{"hits":[1]}`},
	} {
		_, err := s.Filter(context.Background(), c.req, []byte(c.answer))
		var ref *enforce.DeniedError
		if !errors.As(err, &ref) || !ref.Read || !strings.Contains(ref.Error(), "fail-closed: the answer cannot be filtered") {
			t.Errorf("%s %s with %s: %v, want a fail-closed read refusal", c.req.Transport, c.req.Path+c.req.Type, c.answer, err)
		}
	}
}

// TestTemplateReadNeedsAnAllowOnlyWhileAReadIsDenied — D-143 as a pure rule,
// shared by the request layer and `policy check`.
func TestTemplateReadNeedsAnAllowOnlyWhileAReadIsDenied(t *testing.T) {
	tpl := policy.Resource{Kind: policy.KindSystem, ID: "template"}
	for _, c := range []struct {
		name, body string
		deniesRead bool
		allowed    bool
	}{
		{"no read denied", "version: 1\nactive: a\nprofiles:\n  a:\n    deny:\n      \"entity:lock.*\": [control]\n", false, true},
		{"a read denied, the default allows", "version: 1\nactive: a\nprofiles:\n  a:\n    deny:\n      \"entity:lock.*\": [read]\n", true, false},
		{"a read denied, an explicit allow", "version: 1\nactive: a\nprofiles:\n  a:\n    deny:\n      \"entity:lock.*\": [read]\n    allow:\n      \"system:template\": [read]\n", true, true},
		{"default deny without an allow", "version: 1\nactive: a\nprofiles:\n  a:\n    default: deny\n    allow:\n      \"entity:*\": [read]\n", true, false},
		{"default deny reading everything", "version: 1\nactive: a\nprofiles:\n  a:\n    default: deny\n    allow:\n      \"*\": [read]\n", false, true},
	} {
		pol, _ := loadPolicy(t, c.body)
		if got := enforce.DeniesAnyRead(pol); got != c.deniesRead {
			t.Errorf("%s: DeniesAnyRead %v, want %v", c.name, got, c.deniesRead)
		}
		v := enforce.TemplateRead(pol, tpl, pol.Decide(tpl, policy.Read, nil))
		if v.Allowed != c.allowed {
			t.Errorf("%s: read system:template allowed %v (%s), want %v", c.name, v.Allowed, v.Rule, c.allowed)
		}
	}
}

// TestDeletingAnUnreadableViewIsRefused — D-153: a command saves the config
// its read had a denied view dropped from (D-140); the save would delete that
// view, which no allow may grant — at the plan and at the request layer.
func TestDeletingAnUnreadableViewIsRefused(t *testing.T) {
	body := `version: 1
active: a
profiles:
  a:
    deny:
      "dashboard:main/views/secret": [read]
`
	f := &fakeFetcher{dash: map[string]reqmap.StoredDashboard{"main": {Config: json.RawMessage(`{"views":[{"path":"home"},{"path":"secret"}]}`)}}}
	s, _ := newSession(t, body, f)
	ctx := context.Background()
	stored, found, err := s.StoredDashboard(ctx, "main")
	if err != nil || !found || !strings.Contains(string(stored), "secret") {
		t.Fatalf("the session's own stored config: %s %v %v, want it unfiltered", stored, found, err)
	}
	checks, err := reqmap.DiffDashboard("main", stored, json.RawMessage(`{"views":[{"path":"home"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	_, err = s.CheckPlan(enforce.Plan{Action: "save dashboard config", Checks: checks}, true, false)
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) || ref.Denied[0].Rule != enforce.RuleUnreadViewDelete || ref.Denied[0].Resource != "dashboard:main/views/secret" {
		t.Fatalf("plan: %v, want delete dashboard:main/views/secret refused by %q", err, enforce.RuleUnreadViewDelete)
	}
	// The request layer diffs the save against the same stored config.
	save := reqmap.Request{Transport: reqmap.WS, Type: "lovelace/config/save", Params: map[string]any{"url_path": "main", "config": map[string]any{"views": []any{map[string]any{"path": "home"}}}}}
	if err := s.Check(ctx, save); !errors.As(err, &ref) || ref.Denied[0].Rule != enforce.RuleUnreadViewDelete {
		t.Fatalf("request: %v, want the same refusal", err)
	}
	// A save that keeps the view deletes nothing and passes.
	keep := reqmap.Request{Transport: reqmap.WS, Type: "lovelace/config/save", Params: map[string]any{"url_path": "main", "config": map[string]any{"views": []any{map[string]any{"path": "home", "title": "x"}, map[string]any{"path": "secret"}}}}}
	if err := s.Check(ctx, keep); err != nil {
		t.Errorf("a save keeping the view: %v", err)
	}
}

// TestAMaskedValueIsNeverWrittenBack — D-154: once a read masked something,
// a write carrying `<denied>` back is refused, so a command merging what it
// read (a label list) never writes the mask over the real value.
func TestAMaskedValueIsNeverWrittenBack(t *testing.T) {
	s, _ := newSession(t, `version: 1
active: a
profiles:
  a:
    deny:
      "entity:light.x/registry/labels": [read]
`, nil)
	ctx := context.Background()
	update := reqmap.Request{Transport: reqmap.WS, Type: "config/entity_registry/update", Params: map[string]any{"entity_id": "light.x", "labels": []any{"<denied>", "night"}}}
	if err := s.Check(ctx, update); err != nil {
		t.Fatalf("before anything was masked the write is decided as any: %v", err)
	}
	list := reqmap.Request{Transport: reqmap.WS, Type: "config/entity_registry/list"}
	out, err := s.Filter(ctx, list, []byte(`[{"entity_id":"light.x","labels":["night"]}]`))
	if err != nil || !strings.Contains(string(out), `["<denied>"]`) {
		t.Fatalf("the labels were not masked: %s %v", out, err)
	}
	err = s.Check(ctx, update)
	var ref *enforce.DeniedError
	if !errors.As(err, &ref) || ref.Denied[0].Rule != enforce.RuleMaskedWriteBack {
		t.Fatalf("a write carrying <denied> back: %v, want %q", err, enforce.RuleMaskedWriteBack)
	}
	clean := reqmap.Request{Transport: reqmap.WS, Type: "config/entity_registry/update", Params: map[string]any{"entity_id": "light.x", "labels": []any{"night"}}}
	if err := s.Check(ctx, clean); err != nil {
		t.Errorf("a write without the mask: %v", err)
	}
}

// TestTheTrailerCountsAThingOnce — D-139's count is of things not shown: an
// entity two reads of one invocation hide (its state and its registry row)
// counts once, an attribute masked in two answers once, a row without an
// address on its own.
func TestTheTrailerCountsAThingOnce(t *testing.T) {
	s, _ := newSession(t, `version: 1
active: a
profiles:
  a:
    deny:
      "entity:lock.*": [read]
      "entity:sensor.t/attr/secret": [read]
`, nil)
	ctx := context.Background()
	states := reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/states"}
	reg := reqmap.Request{Transport: reqmap.WS, Type: "config/entity_registry/list"}
	logbook := reqmap.Request{Transport: reqmap.REST, Method: "GET", Path: "/api/logbook/x"}
	for _, c := range []struct {
		req    reqmap.Request
		answer string
	}{
		{states, `[{"entity_id":"lock.a","state":"x"},{"entity_id":"sensor.t","state":"1","attributes":{"secret":1}}]`},
		{states, `[{"entity_id":"lock.a","state":"x"},{"entity_id":"sensor.t","state":"1","attributes":{"secret":1}}]`},
		{reg, `[{"entity_id":"lock.a"},{"entity_id":"sensor.t"}]`},
		{logbook, `[{"message":"started"},{"message":"stopped"}]`},
	} {
		if _, err := s.Filter(ctx, c.req, []byte(c.answer)); err != nil {
			t.Fatal(err)
		}
	}
	if hidden, masked, _ := s.ReadCounts(); hidden != 3 || masked != 1 {
		t.Errorf("hidden %d masked %d, want 3 (lock.a once, two rows without an entity) and 1", hidden, masked)
	}
}

// TestFilteringAWriteAsksNothing — the answer to a write passes as it is, and
// filtering it asks Home Assistant nothing: a service call's reach is not
// asked a second time for its answer.
func TestFilteringAWriteAsksNothing(t *testing.T) {
	f := &fakeFetcher{}
	s, _ := newSession(t, "version: 1\nactive: a\nprofiles:\n  a:\n    deny:\n      \"entity:lock.*\": [read]\n", f)
	req := reqmap.Request{Transport: reqmap.REST, Method: "POST", Path: "/api/services/light/turn_on", Body: []byte(`{"entity_id":"light.a"}`)}
	answer := []byte(`[{"entity_id":"lock.a","state":"locked"}]`)
	out, err := s.Filter(context.Background(), req, answer)
	if err != nil || string(out) != string(answer) {
		t.Errorf("a write's answer was filtered: %s %v", out, err)
	}
	if len(f.asked) != 0 {
		t.Errorf("filtering a write's answer asked %v", f.asked)
	}
}
