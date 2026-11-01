package haapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/hemm-ems/hactl/internal/policy/reqgate"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// recordingGate records every decision the clients ask of it and refuses what
// refuse says.
type recordingGate struct {
	mu       sync.Mutex
	checks   []reqmap.Request
	hops     []reqmap.Request
	observed []string
	refuse   func(reqmap.Request) error
	hopErr   error
}

func (g *recordingGate) Check(_ context.Context, r reqmap.Request) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.checks = append(g.checks, r)
	if g.refuse != nil {
		return g.refuse(r)
	}
	return nil
}

func (g *recordingGate) CheckHop(_ context.Context, _, hop reqmap.Request) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.hops = append(g.hops, hop)
	return g.hopErr
}

func (g *recordingGate) Observe(r reqmap.Request, resp []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observed = append(g.observed, describeReq(r)+" => "+string(resp))
}

func describeReq(r reqmap.Request) string {
	if r.Transport == reqmap.WS {
		return "ws " + r.Type
	}
	return string(r.Transport) + " " + r.Method + " " + r.Path
}

func installGate(t *testing.T, g reqgate.Gate) {
	t.Helper()
	uninstall := reqgate.Install(g)
	t.Cleanup(uninstall)
}

var errRefused = errors.New("refused by the test gate")

// TestRESTRequestGetsOneVerdictBeforeItsRetries (D-74): a GET that 5xx's twice
// and then answers is decided once, before the retry loop — the transport hook
// lets the marked retries through — and its answer is observed.
func TestRESTRequestGetsOneVerdictBeforeItsRetries(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if hits.Add(1) < 3 {
			http.Error(w, "busy", http.StatusBadGateway)
			return
		}
		_, _ = io.WriteString(w, `[]`)
	}))
	defer srv.Close()
	g := &recordingGate{}
	installGate(t, g)

	if _, err := New(srv.URL, "tok").GetStates(context.Background()); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 3 {
		t.Fatalf("server saw %d attempts, want 3 (two retries)", hits.Load())
	}
	if len(g.checks) != 1 || g.checks[0].Transport != reqmap.REST || g.checks[0].Method != http.MethodGet || g.checks[0].Path != "/api/states" {
		t.Errorf("verdicts = %+v, want exactly one for GET /api/states", g.checks)
	}
	if len(g.observed) != 1 || g.observed[0] != "rest GET /api/states => []" {
		t.Errorf("observed = %q", g.observed)
	}
}

// TestRefusedRESTRequestIsNeverSent: a refusal is returned as is and the
// request never leaves the process — retrying, once-only and body-carrying
// builders alike; the gate sees the body as sent.
func TestRefusedRESTRequestIsNeverSent(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	g := &recordingGate{refuse: func(reqmap.Request) error { return errRefused }}
	installGate(t, g)
	c := New(srv.URL, "tok")
	ctx := context.Background()

	if _, err := c.CallService(ctx, "light", "turn_on", map[string]any{"entity_id": "light.a"}); !errors.Is(err, errRefused) {
		t.Errorf("CallService = %v, want the gate's refusal", err)
	}
	if _, err := c.doPostOnce(ctx, "/api/config/config_entries/flow", map[string]any{"handler": "met"}); !errors.Is(err, errRefused) {
		t.Errorf("doPostOnce = %v, want the gate's refusal", err)
	}
	if hits.Load() != 0 {
		t.Errorf("a refused request reached the server %d times", hits.Load())
	}
	if len(g.checks) != 2 || string(g.checks[0].Body) != `{"entity_id":"light.a"}` || g.checks[0].Path != "/api/services/light/turn_on" {
		t.Errorf("checks = %+v", g.checks)
	}
}

// TestRESTGateStripsTheBasePath: a Home Assistant behind a path prefix is
// mapped by its API path.
func TestRESTGateStripsTheBasePath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, `[]`) }))
	defer srv.Close()
	g := &recordingGate{}
	installGate(t, g)
	if _, err := New(srv.URL+"/ha/", "tok").GetStates(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(g.checks) != 1 || g.checks[0].Path != "/api/states" {
		t.Errorf("checks = %+v, want /api/states", g.checks)
	}
}

// TestRedirectHopPassesTheGateAgain (D-74): a same-origin redirect carries the
// decided request's mark to another path; the transport asks CheckHop, and a
// refused hop never reaches its target.
func TestRedirectHopPassesTheGateAgain(t *testing.T) {
	var reachedTarget atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/states", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/api/config", http.StatusFound)
	})
	mux.HandleFunc("/api/config", func(w http.ResponseWriter, _ *http.Request) {
		reachedTarget.Add(1)
		_, _ = io.WriteString(w, `{}`)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	g := &recordingGate{hopErr: errRefused}
	installGate(t, g)
	_, err := New(srv.URL, "tok").GetStates(context.Background())
	if !errors.Is(err, errRefused) {
		t.Errorf("GetStates through a refused hop = %v", err)
	}
	if reachedTarget.Load() != 0 {
		t.Error("the refused hop reached its target")
	}
	if len(g.checks) != 1 || len(g.hops) != 1 || g.hops[0].Path != "/api/config" || g.hops[0].Transport != reqmap.REST {
		t.Errorf("checks %+v hops %+v", g.checks, g.hops)
	}

	g2 := &recordingGate{}
	installGate(t, g2)
	if _, err := New(srv.URL, "tok").GetStates(context.Background()); err != nil {
		t.Fatalf("an allowed hop: %v", err)
	}
	if reachedTarget.Load() != 1 || len(g2.checks) != 1 || len(g2.hops) != 1 {
		t.Errorf("allowed hop: target %d checks %d hops %d", reachedTarget.Load(), len(g2.checks), len(g2.hops))
	}
}

// TestUnmarkedRequestIsDecidedAtTheTransport: a request on the shared HTTP
// client that no client decided is decided by the transport hook itself.
func TestUnmarkedRequestIsDecidedAtTheTransport(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	g := &recordingGate{refuse: func(reqmap.Request) error { return errRefused }}
	installGate(t, g)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/api/x", strings.NewReader(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := HTTPClient(srv.URL).Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, errRefused) || hits.Load() != 0 {
		t.Errorf("unmarked request: err %v, hits %d", err, hits.Load())
	}
	if len(g.checks) != 1 || string(g.checks[0].Body) != `{"a":1}` || g.checks[0].Path != "/api/x" {
		t.Errorf("checks = %+v", g.checks)
	}
}

// TestNoGateNoChange (D-58): with no gate installed every request is sent as
// before and nothing is asked.
func TestNoGateNoChange(t *testing.T) {
	if reqgate.Current() != nil {
		t.Fatal("a gate is installed")
	}
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = io.WriteString(w, `[]`)
	}))
	defer srv.Close()
	if _, err := New(srv.URL, "tok").CallService(context.Background(), "light", "turn_on", nil); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d", hits.Load())
	}
}

// TestEveryWebsocketSendPassesTheGate (H-29): sendCommand, a direct sender and
// the auth handshake all go through the one gated connection; a refused
// message is never written; the id never reaches the gate.
func TestEveryWebsocketSendPassesTheGate(t *testing.T) {
	var serverSaw []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.WriteJSON(map[string]string{"type": "auth_required"})
		var auth map[string]any
		_ = c.ReadJSON(&auth)
		_ = c.WriteJSON(map[string]string{"type": "auth_ok"})
		for {
			var cmd map[string]any
			if c.ReadJSON(&cmd) != nil {
				return
			}
			mu.Lock()
			typ, _ := cmd["type"].(string)
			serverSaw = append(serverSaw, typ)
			mu.Unlock()
			_ = c.WriteJSON(map[string]any{"id": cmd["id"], "type": "result", "success": true, "result": []any{}})
		}
	}))
	defer srv.Close()

	g := &recordingGate{refuse: func(r reqmap.Request) error {
		if r.Type == "config/label_registry/create" {
			return errRefused
		}
		return nil
	}}
	installGate(t, g)
	ws := NewWSClient(srv.URL, "tok")
	if err := ws.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()
	ctx := context.Background()
	if _, err := ws.AreaRegistryList(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.SystemLogList(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.LabelRegistryCreate(ctx, "x", "", "", ""); !errors.Is(err, errRefused) {
		t.Errorf("LabelRegistryCreate = %v, want the refusal", err)
	}
	types := make([]string, 0, len(g.checks))
	for _, c := range g.checks {
		types = append(types, c.Type)
		if _, hasID := c.Params["id"]; hasID {
			t.Errorf("%s: the message id reached the gate", c.Type)
		}
	}
	want := []string{"auth", "config/area_registry/list", "system_log/list", "config/label_registry/create"}
	if strings.Join(types, ",") != strings.Join(want, ",") {
		t.Errorf("gate saw %q, want %q", types, want)
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(serverSaw, ",") != "config/area_registry/list,system_log/list" {
		t.Errorf("server saw %q — a refused message was written", serverSaw)
	}
	if len(g.observed) != 1 || !strings.HasPrefix(g.observed[0], "ws config/area_registry/list") {
		t.Errorf("observed = %q, want sendCommand's answer", g.observed)
	}
}

// TestExtractFromTargetAsksWithGroupsExpanded (D-59): the reach question is
// the target with expand_group true, and an answer without
// referenced_entities is an error, never an empty reach.
func TestExtractFromTargetAsksWithGroupsExpanded(t *testing.T) {
	answer := map[string]any{"referenced_entities": []string{"light.a"}}
	var got map[string]any
	srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) {
		got = cmd
		sendWSResult(t, c, cmd, answer)
	})
	defer srv.Close()
	ws := connectWSTest(t, srv)
	ids, err := ws.ExtractFromTarget(context.Background(), map[string]any{"area_id": []any{"hall"}})
	if err != nil || len(ids) != 1 || ids[0] != "light.a" {
		t.Fatalf("ExtractFromTarget = %v, %v", ids, err)
	}
	if got["type"] != "extract_from_target" || got["expand_group"] != true {
		t.Errorf("sent %v", got)
	}
	if tgt, ok := got["target"].(map[string]any); !ok || tgt["area_id"] == nil {
		t.Errorf("target not sent under target: %v", got)
	}

	srv2 := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) { sendWSResult(t, c, cmd, map[string]any{}) })
	defer srv2.Close()
	if _, err := connectWSTest(t, srv2).ExtractFromTarget(context.Background(), map[string]any{"area_id": []any{"x"}}); err == nil {
		t.Error("an answer without referenced_entities was read as a reach")
	}
}

// TestDeviceConfigEntriesSaysWhenItCannotAnswer (D-76): a device row without
// config_entries makes the answer incomplete.
func TestDeviceConfigEntriesSaysWhenItCannotAnswer(t *testing.T) {
	for _, tc := range []struct {
		rows     []map[string]any
		complete bool
	}{
		{[]map[string]any{{"id": "d1", "config_entries": []string{"e1"}}, {"id": "d2", "config_entries": []string{}}}, true},
		{[]map[string]any{{"id": "d1", "config_entries": []string{"e1"}}, {"id": "d2"}}, false},
	} {
		srv := startWSTestServer(t, func(c *websocket.Conn, cmd map[string]any) { sendWSResult(t, c, cmd, tc.rows) })
		got, complete, err := connectWSTest(t, srv).DeviceConfigEntries(context.Background())
		srv.Close()
		if err != nil || complete != tc.complete || len(got["d1"]) != 1 {
			t.Errorf("rows %v: entries %v complete %v err %v", tc.rows, got, complete, err)
		}
	}
}
