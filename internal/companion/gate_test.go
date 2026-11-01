package companion

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/reqgate"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

type countingGate struct {
	mu       sync.Mutex
	checks   []reqmap.Request
	observed int
	refuse   bool
}

func (g *countingGate) Check(_ context.Context, r reqmap.Request) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.checks = append(g.checks, r)
	if g.refuse {
		return errors.New("refused by the test gate")
	}
	return nil
}

func (g *countingGate) CheckHop(context.Context, reqmap.Request, reqmap.Request) error {
	return errors.New("no redirect expected")
}

func (g *countingGate) Observe(reqmap.Request, []byte) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.observed++
}

// TestCompanionRequestGetsOneVerdictOnEveryTransport (D-74, R-15): a companion
// write is decided once, by its logical route, on the direct URL and under
// Ingress alike — never a second time by the shared transport as HA REST,
// and never again for a retry.
func TestCompanionRequestGetsOneVerdictOnEveryTransport(t *testing.T) {
	for _, tc := range []struct{ name, prefix string }{
		{"direct", ""},
		{"ingress", "/api/hassio_ingress/" + testIngressToken},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var hits atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != tc.prefix+"/v1/config/script" {
					t.Errorf("request to %s", r.URL.Path)
				}
				if hits.Add(1) == 1 {
					http.Error(w, "busy", http.StatusServiceUnavailable) // PUT is idempotent: retried
					return
				}
				_, _ = io.WriteString(w, `{"status":"ok"}`)
			}))
			defer srv.Close()
			g := &countingGate{}
			defer reqgate.Install(g)()

			if _, err := New(srv.URL+tc.prefix, "tok").WriteScriptDef(context.Background(), "morning", "alias: x\n", false); err != nil {
				t.Fatal(err)
			}
			if hits.Load() != 2 {
				t.Fatalf("server saw %d attempts, want 2 (one retry)", hits.Load())
			}
			if len(g.checks) != 1 {
				t.Fatalf("%d verdicts, want exactly one: %+v", len(g.checks), g.checks)
			}
			c := g.checks[0]
			if c.Transport != reqmap.Companion || c.Method != http.MethodPut || c.Path != "/v1/config/script?dry_run=false&id=morning" {
				t.Errorf("verdict asked for %+v, want the logical route", c)
			}
			if g.observed != 1 {
				t.Errorf("observed %d answers, want 1", g.observed)
			}
		})
	}
}

// TestRefusedCompanionRequestIsNeverSent: the refusal comes back as is and
// nothing reaches the companion.
func TestRefusedCompanionRequestIsNeverSent(t *testing.T) {
	var hits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
	defer srv.Close()
	g := &countingGate{refuse: true}
	defer reqgate.Install(g)()
	if _, err := New(srv.URL, "tok").DeleteScriptDef(context.Background(), "morning"); err == nil {
		t.Error("a refused delete succeeded")
	}
	if hits.Load() != 0 {
		t.Errorf("the companion saw %d requests", hits.Load())
	}
}
