package companion

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWireGuardStatusActive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/wireguard/status" {
			t.Errorf("path = %q, want /v1/wireguard/status", r.URL.Path)
		}
		if got := r.URL.Query().Get("tunnel"); got != "wg0" {
			t.Errorf("tunnel param = %q, want wg0", got)
		}
		_ = json.NewEncoder(w).Encode(WireGuardStatusResponse{
			Tunnel:    "wg0",
			State:     "active",
			Interface: &WireGuardIface{PublicKey: "PUB", ListeningPort: 36317},
			Peers:     []WireGuardPeer{{Endpoint: "1.2.3.4:51820", LatestHandshake: "42 seconds ago"}},
		})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	st, err := c.WireGuardStatus(context.Background(), "wg0")
	if err != nil {
		t.Fatalf("WireGuardStatus: %v", err)
	}
	if st.State != "active" {
		t.Errorf("got state=%q, want active", st.State)
	}
	if st.Interface == nil || st.Interface.ListeningPort != 36317 {
		t.Errorf("interface = %+v, want port 36317", st.Interface)
	}
	if len(st.Peers) != 1 {
		t.Fatalf("peers = %d, want 1", len(st.Peers))
	}
}

func TestWireGuardStatusInactive(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(WireGuardStatusResponse{Tunnel: "wg0", State: "inactive"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	st, err := c.WireGuardStatus(context.Background(), "wg0")
	if err != nil {
		t.Fatalf("WireGuardStatus: %v", err)
	}
	if st.State != "inactive" {
		t.Errorf("state = %q, want inactive", st.State)
	}
}

func TestWireGuardConfig(t *testing.T) {
	const conf = "[Interface]\nPrivateKey = X\n\n[Peer]\nPublicKey = Y\nAllowedIPs = 10.6.0.0/24\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/wireguard/config" {
			t.Errorf("got %s %s, want POST /v1/wireguard/config", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("tunnel"); got != "wg0" {
			t.Errorf("tunnel = %q, want wg0", got)
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != conf {
			t.Errorf("body = %q, want the raw conf", string(body))
		}
		_ = json.NewEncoder(w).Encode(WireGuardActionResponse{Status: "configured", Tunnel: "wg0"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	res, err := c.WireGuardConfig(context.Background(), "wg0", conf)
	if err != nil {
		t.Fatalf("WireGuardConfig: %v", err)
	}
	if res.Status != "configured" {
		t.Errorf("status = %q, want configured", res.Status)
	}
}

func TestWireGuardStart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/wireguard/start" {
			t.Errorf("path = %q, want /v1/wireguard/start", r.URL.Path)
		}
		if got := r.URL.Query().Get("tunnel"); got != "wg0" {
			t.Errorf("tunnel = %q, want wg0", got)
		}
		_ = json.NewEncoder(w).Encode(WireGuardActionResponse{Status: "started", Tunnel: "wg0"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	res, err := c.WireGuardStart(context.Background(), "wg0")
	if err != nil {
		t.Fatalf("WireGuardStart: %v", err)
	}
	if res.Status != "started" {
		t.Errorf("got status=%q, want started", res.Status)
	}
}

func TestWireGuardStop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/wireguard/stop" {
			t.Errorf("path = %q, want /v1/wireguard/stop", r.URL.Path)
		}
		if got := r.URL.Query().Get("tunnel"); got != "wg0" {
			t.Errorf("tunnel = %q, want wg0", got)
		}
		_ = json.NewEncoder(w).Encode(WireGuardActionResponse{Status: "stopped", Tunnel: "wg0"})
	}))
	defer srv.Close()

	c := New(srv.URL, "test-token")
	res, err := c.WireGuardStop(context.Background(), "wg0")
	if err != nil {
		t.Fatalf("WireGuardStop: %v", err)
	}
	if res.Status != "stopped" {
		t.Errorf("status = %q, want stopped", res.Status)
	}
}
