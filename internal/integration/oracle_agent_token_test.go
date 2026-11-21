//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

// ============================================================================
// W-5 (SPEC-policy-usp.md, M-6, decision D-160): `hactl agent add` gives every
// agent a long-lived access token of its own, for the same Home Assistant user.
// hactl holds only a long-lived token (the one in the source instance's .env),
// so the question that decides the code is whether Home Assistant lets a
// WebSocket connection authenticated with a LONG-LIVED token create another
// one. If it does not, `agent add` can only take a pasted token (`--token -`).
//
// The rig's token is itself a long-lived token: hatest's onboarding mints it
// over a connection authenticated with the login access token, and every test
// authenticates with that minted one.
// ============================================================================

func TestOracleLongLivedTokenFromLongLivedConnection(t *testing.T) {
	ws := dialRawWS(t, ha.URL(), ha.Token())

	env := ws.send(t, map[string]any{
		"type":        "auth/long_lived_access_token",
		"client_name": "hactl agent oracle-probe",
		"lifespan":    1,
	})
	// Observed on HA 2026.11.0 (2026-11-05): success, a token string. The
	// token itself is never logged.
	raw, _ := json.Marshal(map[string]any{"success": env.Success, "error": env.Error})
	t.Logf("auth/long_lived_access_token over an LLAT-authenticated connection: %s", raw)
	if !env.Success {
		t.Fatalf("Home Assistant refused to mint a long-lived token over a long-lived-token connection: %s", raw)
	}
	var minted string
	if err := json.Unmarshal(env.Result, &minted); err != nil || minted == "" {
		t.Fatalf("the result is not a token string: %s", raw)
	}
	if minted == ha.Token() {
		t.Fatal("the minted token is the caller's own token")
	}

	// The minted token authenticates on its own, REST and WebSocket alike.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ha.URL()+"/api/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+minted)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /api/ with the minted token: status %d, want 200", resp.StatusCode)
	}
	_ = dialRawWS(t, ha.URL(), minted) // fails the test unless auth_ok

	// And it is listed under the client name it was given — what makes the
	// agents tell apart in Home Assistant's token list (M-6).
	refresh := ws.mustSucceed(t, map[string]any{"type": "auth/refresh_tokens"})
	var tokens []struct {
		ClientName string `json:"client_name"`
		Type       string `json:"type"`
	}
	if err := json.Unmarshal(refresh.Result, &tokens); err != nil {
		t.Fatalf("auth/refresh_tokens: %v", err)
	}
	found := false
	for _, tk := range tokens {
		if tk.ClientName == "hactl agent oracle-probe" {
			found = true
			if tk.Type != "long_lived_access_token" {
				t.Errorf("the minted token's type is %q, want long_lived_access_token", tk.Type)
			}
		}
	}
	if !found {
		t.Errorf("auth/refresh_tokens lists no token named %q", "hactl agent oracle-probe")
	}
}
