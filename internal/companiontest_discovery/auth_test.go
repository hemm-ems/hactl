//go:build companion_discovery

package companiontest_discovery

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// TestCompanionCall_WithIngressAuth_SucceedsThroughIngress verifies the
// end-to-end Ingress flow: discovery resolves an ingress URL, the client
// asks Supervisor for an ingress_session token via WS, sets it as a cookie
// on the HTTP request, and the real Companion (via the fake's ingress
// reverse proxy) responds 200. Without the IngressAuth source wired up the
// same call must 401 — proves the session cookie is what's authenticating
// the request.
func TestCompanionCall_WithIngressAuth_SucceedsThroughIngress(t *testing.T) {
	fakeSup.SetRequireSession(true)
	defer fakeSup.SetRequireSession(false)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws := haapi.NewWSClient(fakeSup.BaseURL(), "any-token-accepted")
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("WSClient.Connect: %v", err)
	}
	defer ws.Close() //nolint:errcheck

	cfg := minimalConfig(fakeSup.BaseURL())
	url, err := companion.Discover(ctx, cfg, ws)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	// Without IngressAuth: must fail (proves the session requirement is real).
	plain := companion.New(url, "any-token-accepted")
	if _, err := plain.Health(ctx); err == nil {
		t.Error("Companion call without IngressAuth unexpectedly succeeded — " +
			"is requireSession really enforced?")
	} else if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 without IngressAuth, got: %v", err)
	}

	// With IngressAuth: must succeed.
	authed := companion.New(url, "any-token-accepted").WithIngressAuth(ws)
	h, err := authed.Health(ctx)
	if err != nil {
		t.Fatalf("Companion call with IngressAuth failed: %v", err)
	}
	if h.Status != "ok" {
		t.Errorf("Health.Status = %q, want ok", h.Status)
	}
}

// TestCompanionCall_RefreshesSessionOnInvalidation simulates a Supervisor
// expiring the session mid-flight: the client's cached token is wiped at
// the fake, the next call lands 401, and the client must fetch a fresh
// /ingress/session and succeed on retry.
func TestCompanionCall_RefreshesSessionOnInvalidation(t *testing.T) {
	fakeSup.SetRequireSession(true)
	defer fakeSup.SetRequireSession(false)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	ws := haapi.NewWSClient(fakeSup.BaseURL(), "any-token-accepted")
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("WSClient.Connect: %v", err)
	}
	defer ws.Close() //nolint:errcheck

	cfg := minimalConfig(fakeSup.BaseURL())
	url, err := companion.Discover(ctx, cfg, ws)
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	cc := companion.New(url, "any-token-accepted").WithIngressAuth(ws)

	// First call seeds the cached session.
	if _, err := cc.Health(ctx); err != nil {
		t.Fatalf("initial Health: %v", err)
	}
	before := fakeSup.sessionCounter.Load()

	// Wipe sessions server-side; the cached cookie is now stale.
	fakeSup.InvalidateSessions()

	// Next call must transparently fetch a fresh session and succeed.
	if _, err := cc.Health(ctx); err != nil {
		t.Fatalf("Health after session invalidation: %v", err)
	}
	after := fakeSup.sessionCounter.Load()
	if after-before < 1 {
		t.Errorf("expected ≥1 /ingress/session call after invalidation, got %d", after-before)
	}
}
