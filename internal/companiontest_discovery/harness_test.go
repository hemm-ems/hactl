//go:build companion_discovery

package companiontest_discovery

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// TestFakeSupervisorBoots is a smoke test: the in-process fake must respond
// on its declared base URL. Catches harness regressions independent of
// hactl's code under test.
func TestFakeSupervisorBoots(t *testing.T) {
	resp, err := httpGet(t, fakeSup.BaseURL()+"/api/websocket")
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
	resp, err := httpGet(t, url)
	if err != nil {
		t.Fatalf("GET via fake ingress proxy: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// TestWSClientCanConnectToFake verifies that hactl's real WSClient can complete
// the HA WS handshake against the Fake *and then use the connection*. Without
// this the downstream Discover tests are meaningless.
//
// "Connect returned nil" was the whole of it before, which is the weakest
// possible claim: it holds for a connection that completed the handshake and
// then answered nothing. So a command is issued over the same connection and
// its answer is checked against what the Fake was built to report — the same
// round trip Discover depends on, isolated from Discover's own logic.
func TestWSClientCanConnectToFake(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws := haapi.NewWSClient(fakeSup.BaseURL(), "any-token-accepted")
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("WSClient.Connect: %v", err)
	}
	defer ws.Close() //nolint:errcheck

	raw, err := ws.SupervisorAPI(ctx, "/addons", "get", nil)
	if err != nil {
		t.Fatalf("supervisor/api /addons over the established connection: %v", err)
	}
	var got struct {
		Addons []struct {
			Slug string `json:"slug"`
		} `json:"addons"`
	}
	if unmarshalErr := json.Unmarshal(raw, &got); unmarshalErr != nil {
		t.Fatalf("decoding the Fake's /addons answer: %v\nraw: %s", unmarshalErr, raw)
	}
	var slugs []string
	for _, a := range got.Addons {
		slugs = append(slugs, a.Slug)
	}
	if !slices.Contains(slugs, addonSlug) {
		t.Errorf("supervisor/api /addons returned %v, want it to include %q", slugs, addonSlug)
	}
}

// TestDiscover_ResolvesViaSupervisorWSProxy verifies that the discovery flow
// enumerates /addons via the Supervisor WS proxy, matches the companion by
// (repo-prefixed) slug, fetches its ingress URL, and composes the final
// companion URL by joining the HA base URL with the ingress path.
func TestDiscover_ResolvesViaSupervisorWSProxy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	ws := haapi.NewWSClient(fakeSup.BaseURL(), "any-token-accepted")
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("WSClient.Connect: %v", err)
	}
	defer ws.Close() //nolint:errcheck

	// Use a base URL with trailing slash to verify the join does not produce a
	// double slash where host meets the ingress path.
	cfg := minimalConfig(fakeSup.BaseURL() + "/")
	got, err := companion.Discover(ctx, cfg, ws)
	if err != nil {
		t.Fatalf("Discover failed: %v", err)
	}
	want := fakeSup.BaseURL() + ingressPrefix
	if got != want {
		t.Errorf("Discover URL = %q, want %q", got, want)
	}
	if strings.Contains(strings.TrimPrefix(got, "http://"), "//") {
		t.Errorf("Discover URL contains '//' after the host: %q", got)
	}

	// Wire-format pin: the two WS messages must be the new supervisor/api proxy
	// calls (not the legacy hassio/addon/info command). If hactl ever
	// regresses to the legacy command, this test catches it immediately.
	requests := fakeSup.WSRequests()
	if len(requests) < 2 {
		t.Fatalf("expected 2 WS requests, got %d: %+v", len(requests), requests)
	}
	if requests[0].Type != "supervisor/api" || requests[0].Endpoint != "/addons" {
		t.Errorf("first WS = %+v, want supervisor/api /addons", requests[0])
	}
	if requests[1].Type != "supervisor/api" || !strings.HasSuffix(requests[1].Endpoint, "_hactl_companion/info") {
		t.Errorf("second WS = %+v, want supervisor/api /addons/<slug>/info", requests[1])
	}
}

// TestDiscover_ResolvedURLServesCompanionHealth proves the end-to-end glue:
// the URL Discover returns is actually serviceable. Hits /v1/health on the
// resolved URL and expects a 200. Catches regressions in URL composition
// (extra/missing slashes), in the ingress proxy header injection, and in the
// Companion's auth middleware.
func TestDiscover_ResolvedURLServesCompanionHealth(t *testing.T) {
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
		t.Fatalf("Discover failed: %v", err)
	}

	resp, err := httpGet(t, url+"v1/health")
	if err != nil {
		t.Fatalf("GET discovered URL: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		t.Errorf("discovered URL /v1/health = %d, want 200", resp.StatusCode)
	}
}

// httpGet issues a GET bound to the test's context, so a hung fake supervisor
// fails the test at its deadline instead of blocking until the tier timeout.
// The URL is always one the harness itself constructed from a listener it
// started, which is why gosec's variable-URL warning does not apply.
func httpGet(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return http.DefaultClient.Do(req)
}
