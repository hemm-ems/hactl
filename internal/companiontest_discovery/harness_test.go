//go:build companion_discovery

package companiontest_discovery

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// TestFakeSupervisorBoots is a smoke test: the in-process fake must respond
// on its declared base URL. Catches harness regressions independent of
// hactl's code under test.
func TestFakeSupervisorBoots(t *testing.T) {
	resp, err := http.Get(fakeSup.BaseURL() + "/api/websocket")
	if err != nil {
		t.Fatalf("GET fake /api/websocket: %v", err)
	}
	_ = resp.Body.Close()
	// /api/websocket returns 400 to a non-upgrade GET — that's the gorilla
	// upgrader rejecting the handshake, which is the right signal it's wired.
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400 (upgrade required)", resp.StatusCode)
	}
}

// TestIngressProxyReachesCompanion verifies that the Fake's HTTP reverse
// proxy on /api/hassio_ingress/<id>/* correctly forwards to the real
// Companion HTTP service, including adding the X-Ingress-Path header that
// the Companion's auth middleware exempts.
func TestIngressProxyReachesCompanion(t *testing.T) {
	url := fakeSup.BaseURL() + ingressPrefix + "v1/health"
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET via fake ingress proxy: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestWSClientCanConnectToFake verifies that hactl's real WSClient can
// complete the HA WS handshake against the Fake. Without this the
// downstream Discover tests are meaningless.
func TestWSClientCanConnectToFake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws := haapi.NewWSClient(fakeSup.BaseURL(), "any-token-accepted")
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("WSClient.Connect: %v", err)
	}
	defer ws.Close() //nolint:errcheck
}

// TestDiscover_DocumentsCurrentBug pins the CURRENT (broken) behavior of
// hactl's discovery layer against a Supervisor: it sends `hassio/addon/info`,
// which HA Core does not implement, so the Fake replies `unknown_command` and
// classifyWSError mis-buckets that as ReasonUnreachable. This test passes in
// PR 1 by asserting the failure mode. PR 2 inverts it: after the discovery
// fix, the test must assert SUCCESS and resolve to fakeSup.BaseURL()+ingressPrefix.
//
// Inverting this test is part of PR 2's acceptance.
func TestDiscover_DocumentsCurrentBug(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws := haapi.NewWSClient(fakeSup.BaseURL(), "any-token-accepted")
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("WSClient.Connect: %v", err)
	}
	defer ws.Close() //nolint:errcheck

	cfg := minimalConfig(fakeSup.BaseURL())
	_, err := companion.Discover(ctx, cfg, ws)
	if err == nil {
		t.Fatal("Discover unexpectedly succeeded — has PR 2 landed? " +
			"Update this test to assert success and the resolved ingress URL.")
	}
	var de *companion.DiscoveryError
	if !asDiscoveryError(err, &de) {
		t.Fatalf("error is not *DiscoveryError: %T %v", err, err)
	}
	// Today's broken classifier returns ReasonUnreachable for unknown_command.
	// PR 2 introduces ReasonProtocolMismatch and routes unknown_command there.
	if de.Reason != companion.ReasonUnreachable {
		t.Errorf("Reason = %q, want %q (current bug — PR 2 will change this)",
			de.Reason, companion.ReasonUnreachable)
	}

	// Verify that the bug-trigger really did fire: hactl sent the legacy
	// command, which the Fake records and rejects.
	requests := fakeSup.WSRequests()
	if len(requests) == 0 {
		t.Fatal("Fake recorded no WS requests — handshake or routing broken")
	}
	last := requests[len(requests)-1]
	if last.Type != "hassio/addon/info" {
		t.Errorf("last WS type = %q, want %q (the broken legacy call PR 2 will replace)",
			last.Type, "hassio/addon/info")
	}
}
