package haapi_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/gorilla/websocket"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/policy"
	"github.com/hemm-ems/hactl/internal/policy/enforce"
	"github.com/hemm-ems/hactl/internal/policy/reqgate"
)

// TestUnmappedMutatingRequestIsRefused (D-74, E-9): under a named profile —
// one that allows everything a rule could name — a websocket type and a POST
// no row names are refused at the hook, and nothing reaches Home Assistant;
// with no profile in effect the same requests are sent unchanged (D-58).
func TestUnmappedMutatingRequestIsRefused(t *testing.T) {
	var wsSeen, restSeen atomic.Int64
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/websocket", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
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
			wsSeen.Add(1)
			_ = c.WriteJSON(map[string]any{"id": cmd["id"], "type": "result", "success": true, "result": nil})
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) {
		restSeen.Add(1)
		_, _ = w.Write([]byte(`{}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, policy.FileName), []byte("version: 1\nactive: open\nprofiles:\n  open: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, err := policy.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	sess, err := enforce.New(enforce.Options{Policy: pol, InstanceDir: dir})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	send := func() (wsErr, restErr error) {
		ws := haapi.NewWSClient(srv.URL, "tok")
		if err := ws.Connect(ctx); err != nil {
			t.Fatal(err)
		}
		defer func() { _ = ws.Close() }()
		_, wsErr = ws.SendCommandForTest(ctx, "hactl/no_such_command", map[string]any{"x": 1})
		_, restErr = haapi.New(srv.URL, "tok").PostForTest(ctx, "/api/no_such_write", map[string]any{"x": 1})
		return wsErr, restErr
	}

	uninstall := reqgate.Install(sess)
	wsErr, restErr := send()
	uninstall()
	var ref *enforce.DeniedError
	if !errors.As(wsErr, &ref) || ref.Denied[0].Rule != "fail-closed: unmapped request ws hactl/no_such_command" {
		t.Errorf("unmapped websocket type: %v", wsErr)
	}
	if !errors.As(restErr, &ref) || ref.Denied[0].Rule != "fail-closed: unmapped request rest POST /api/no_such_write" {
		t.Errorf("unmapped POST: %v", restErr)
	}
	if wsSeen.Load() != 0 || restSeen.Load() != 0 {
		t.Errorf("refused requests reached Home Assistant: ws %d, rest %d", wsSeen.Load(), restSeen.Load())
	}

	if wsErr, restErr = send(); wsErr != nil || restErr != nil {
		t.Errorf("without a profile: ws %v, rest %v", wsErr, restErr)
	}
	if wsSeen.Load() != 1 || restSeen.Load() != 1 {
		t.Errorf("without a profile the requests were not sent: ws %d, rest %d", wsSeen.Load(), restSeen.Load())
	}
}
