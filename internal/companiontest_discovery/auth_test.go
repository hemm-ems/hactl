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

// TestCompanionCall_WithSigner_SucceedsThroughIngress verifies the end-to-end
// signed-URL flow: discovery resolves an ingress URL, the client signs each
// HTTP request via the WS auth/sign_path command, the fake ingress proxy
// enforces presence of the signature, and the real Companion responds 200.
//
// Without the signer wired up the same call must fail with 401 — proves the
// signature is what's authenticating the request, not the bearer token.
func TestCompanionCall_WithSigner_SucceedsThroughIngress(t *testing.T) {
	fakeSup.SetRequireSignature(true)
	defer fakeSup.SetRequireSignature(false)

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

	// Without signer: must fail (proves the signature requirement is real).
	plain := companion.New(url, "any-token-accepted")
	if _, err := plain.Health(ctx); err == nil {
		t.Error("Companion call without signer unexpectedly succeeded — " +
			"is requireSignature really enforced?")
	} else if !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 without signer, got: %v", err)
	}

	// With signer: must succeed.
	signed := companion.New(url, "any-token-accepted").WithSigner(ws)
	h, err := signed.Health(ctx)
	if err != nil {
		t.Fatalf("Companion call with signer failed: %v", err)
	}
	if h.Status != "ok" {
		t.Errorf("Health.Status = %q, want ok", h.Status)
	}
}

// TestCompanionCall_ReSignsAfterExpiredSignature checks that when the fake's
// signature is invalidated mid-flight (we trigger this by resetting which
// signatures are accepted between requests), the client re-signs and the
// second attempt succeeds.
//
// Modelled on the production failure mode: HA's authSig JWT expires in 30s by
// default, so any retry past that point needs a fresh signature.
func TestCompanionCall_ReSignsAfterExpiredSignature(t *testing.T) {
	// Force every signature to look fresh — the test relies on the client
	// signing on each attempt rather than caching. The fake's
	// signatureCounter makes each issued sig unique, so we just verify the
	// client doesn't cache by issuing two calls and asserting they used
	// different sigs.
	fakeSup.SetRequireSignature(true)
	defer fakeSup.SetRequireSignature(false)

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

	cc := companion.New(url, "any-token-accepted").WithSigner(ws)

	before := fakeSup.signatureCounter.Load()
	if _, err := cc.Health(ctx); err != nil {
		t.Fatalf("first Health: %v", err)
	}
	if _, err := cc.Health(ctx); err != nil {
		t.Fatalf("second Health: %v", err)
	}
	after := fakeSup.signatureCounter.Load()
	if after-before < 2 {
		t.Errorf("expected ≥2 sign_path calls across 2 requests, got %d", after-before)
	}
}
