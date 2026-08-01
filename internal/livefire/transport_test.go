//go:build livefire

package livefire

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// livefireUpgrader turns rejectingHA's handler into a Home Assistant WebSocket
// endpoint. CheckOrigin is open because the client under test is hactl, which
// sends no Origin header.
var livefireUpgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// WP8 — companion status and connectivity, and rig capability R9: hostile
// transports.
//
// Findings #73 #74 #75 #76. Read-only on both profiles, and more than that:
// every case here replaces Home Assistant with a socket this file controls, so
// nothing reaches either instance at all. The profile contributes the caller's
// configuration — its token, its COMPANION_URL — and the transport is the
// subject. That is what makes R9 a rig capability rather than a fixture: the
// shapes the rig could not express were not entities, they were *networks*.
//
// The four shapes, all deterministic and hermetic:
//
//   - a host that accepts the connection and never answers (hangingListener)
//   - a port nothing is listening on (closedPort)
//   - an origin that 301s every request somewhere else (redirectingOrigin)
//   - a Home Assistant that rejects the token on the WebSocket auth handshake
//     (rejectingHA)
//
// An unroutable address was the reported reproduction and is deliberately NOT
// used: 10.255.255.1 answers with an i/o timeout on one network and
// EHOSTUNREACH on the next, and the rule under test is about the bound, not
// about which syscall reports the stall.

// hangingListener accepts TCP connections and answers nothing, ever.
func hangingListener(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, acceptErr := ln.Accept()
			if acceptErr != nil {
				return
			}
			// Held, never written to. Closing it would make the peer fail fast,
			// which is the opposite of the condition under test.
			t.Cleanup(func() { _ = conn.Close() })
		}
	}()
	return "http://" + ln.Addr().String()
}

// closedPort returns a URL nothing is listening on. Binding and releasing is
// how a port is known to be free; picking a number and hoping is how a case
// becomes flaky on somebody else's machine.
func closedPort(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "http://" + addr
}

// redirectingOrigin 301s every request to another origin, which is what a
// reverse proxy in front of Home Assistant does when HA_URL names the http
// port. `to` is a whole origin, so the redirect crosses one.
func redirectingOrigin(t *testing.T, to string) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, to+r.URL.Path, http.StatusMovedPermanently)
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}

// withHostileHA is withStubCompanion's counterpart: the .env is copied and its
// HA_URL replaced, so the case drives hactl at a transport it owns.
//
// COMPANION_URL is dropped along with HA_URL. Leaving the live profile's
// companion in place would let discovery succeed against a real add-on while
// Home Assistant is a hanging socket, which is not a shape any instance has.
func withHostileHA(t *testing.T, tgt Target, haURL string) Target {
	t.Helper()
	env, err := os.ReadFile(filepath.Join(tgt.Dir, ".env"))
	if err != nil {
		t.Fatalf("reading %s/.env: %v", tgt.Dir, err)
	}
	kept := []string{"HA_URL=" + haURL}
	for line := range strings.SplitSeq(string(env), "\n") {
		key, _, _ := strings.Cut(line, "=")
		switch strings.TrimSpace(key) {
		case "HA_URL", "COMPANION_URL", "COMPANION_TOKEN", "":
			continue
		}
		kept = append(kept, line)
	}
	kept = append(kept, "")

	dir := t.TempDir()
	if writeErr := os.WriteFile(filepath.Join(dir, ".env"), []byte(strings.Join(kept, "\n")), 0o600); writeErr != nil {
		t.Fatalf("writing hostile .env: %v", writeErr)
	}
	return Target{Profile: tgt.Profile, Dir: dir, Bin: tgt.Bin}
}

// TestSweepEveryTransportHonoursTheCallerTimeout — finding #73.
//
// `companion status --timeout 1s` took 10.02s and exited 0 on the reference
// instance; 3s and 20s produced the same 10.02s, so the flag was not merely
// wrong, it was unread. `health` and `ent ls` against the identical host
// aborted at 1.01s, which is why nothing pointed at a third transport.
//
// Both commands run here, in one case, because the defect was the DIFFERENCE
// between them: a bound that half the product honours is the shape that hides.
func TestSweepEveryTransportHonoursTheCallerTimeout(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		hostile := withHostileHA(t, tgt, hangingListener(t))

		// Generous relative to the 2s asked for, and far below the 10s and 20s
		// the defect produced: this must go red for the reported reason and
		// never for a loaded machine.
		const asked, ceiling = "2s", 6 * time.Second
		for _, args := range [][]string{
			{"companion", "status", "--timeout", asked},
			{"health", "--timeout", asked},
			{"ent", "ls", "--timeout", asked},
		} {
			t.Run(strings.Join(args[:len(args)-2], "_"), func(t *testing.T) {
				start := time.Now()
				out, err := hostile.Read(t, args...)
				elapsed := time.Since(start)

				if err == nil {
					t.Errorf("%v against a host that never answers reported success:\n%s", args, truncate(out))
				}
				if elapsed > ceiling {
					t.Errorf("%v took %v with --timeout %s — the caller's bound never reached the transport",
						args, elapsed, asked)
				}
			})
		}
	})
}

// TestSweepCompanionStatusVerdictReachesTheExitCode — finding #74.
//
// Every failure mode reported produced exit 0 while the body said "failed", so
// `hactl companion status && proceed` proceeded. Both a refused port and a
// hanging host are checked because they fail at different syscalls and the
// verdict must not depend on which.
func TestSweepCompanionStatusVerdictReachesTheExitCode(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		// A slice, not a map: the case's subtests are named in the output and a
		// map walk would order them differently on every run (H-16's habit,
		// applied where it costs nothing).
		for _, tc := range []struct{ name, url string }{
			{"refused", closedPort(t)},
			{"hanging", hangingListener(t)},
		} {
			name, url := tc.name, tc.url
			t.Run(name, func(t *testing.T) {
				hostile := withHostileHA(t, tgt, url)
				out, err := hostile.Read(t, "companion", "status", "--timeout", "2s", "--tokensmax", "0")
				if code := ExitCode(err); code == 0 {
					t.Errorf("`companion status` exited 0 while answering:\n%s", truncate(out))
				}
				// D-33: the verdict ends the command, it does not erase the
				// diagnostic — which is the whole value of this command.
				if strings.TrimSpace(out) == "" {
					t.Error("the diagnostic printed nothing at all")
				}
				if !strings.Contains(out, "failed") {
					t.Errorf("the body does not say what failed:\n%s", truncate(out))
				}
			})
		}

		// The control: on an instance where the companion answers, the same
		// command exits 0. Without it the case above is satisfied by a command
		// that always fails, which is the same defect with the sign flipped.
		if _, err := tgt.Read(t, "companion", "status", "--tokensmax", "0"); err != nil {
			if tgt.Profile == Rig {
				t.Skip("R11: the rig boots Home Assistant with no companion, so there is nothing to succeed against")
			}
			t.Errorf("`companion status` against a healthy instance must exit 0: %v", err)
		}
	})
}

// TestSweepCompanionStatusNamesTheCause — finding #75.
//
// Three root causes, one label. The reproduction's sharpest form is the first
// row: the command printed `ws_error: "authentication failed: Invalid access
// token or password"` and, in the same document, `discovery_reason:
// "unreachable"` with a hint telling the reader to check Ingress and the
// network — contradicting its own evidence and recommending a fix that cannot
// work.
func TestSweepCompanionStatusNamesTheCause(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, tc := range []struct {
			name   string
			url    string
			reason string
			hint   string
		}{
			{"rejected_token", rejectingHA(t), "auth_invalid", "HA_TOKEN"},
			{"redirected_origin", redirectingOrigin(t, closedPort(t)), "redirected", "HA_URL"},
			{"nothing_answers", closedPort(t), "unreachable", "HA_URL"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				hostile := withHostileHA(t, tgt, tc.url)
				out, _ := hostile.Read(t, "companion", "status", "--json", "--timeout", "2s", "--tokensmax", "0")

				var res struct {
					WSError         string `json:"ws_error"`
					DiscoveryReason string `json:"discovery_reason"`
					DiscoveryHint   string `json:"discovery_hint"`
				}
				if err := json.Unmarshal([]byte(out), &res); err != nil {
					t.Fatalf("--json does not parse: %v\n%s", err, truncate(out))
				}
				if res.DiscoveryReason != tc.reason {
					t.Errorf("discovery_reason = %q, want %q (ws_error said %q)",
						res.DiscoveryReason, tc.reason, res.WSError)
				}
				if !strings.Contains(res.DiscoveryHint, tc.hint) {
					t.Errorf("the hint does not name what to change (%q):\n%s", tc.hint, res.DiscoveryHint)
				}
				// The self-contradiction, asserted directly: whatever the hint
				// recommends, it may not be the thing the evidence rules out.
				if tc.reason != "unreachable" && strings.Contains(res.DiscoveryHint, "Check Ingress / network") {
					t.Errorf("a %s failure is answered with the network hint:\n%s", tc.reason, res.DiscoveryHint)
				}
			})
		}
	})
}

// TestSweepARedirectedOriginIsNamedNotFollowed — finding #76.
//
// The reported shape: `health --json` returned a real version, state, location
// and timezone beside `"errors": -1` and `"companion_status": "not found
// (unreachable)"` at exit 0, and `ent ls` returned all 4 486 entities — because
// REST followed the 301 with the credentials intact and the WebSocket could
// not. The upstream here answers like a real HA so the case can prove the
// REFUSAL rather than a failure to serve.
func TestSweepARedirectedOriginIsNamedNotFollowed(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprint(w, `{"version":"2026.7.4","state":"RUNNING","location_name":"Home",
				"time_zone":"Europe/Berlin","components":["recorder"],"unit_system":{"length":"km"}}`)
		}))
		defer upstream.Close()

		hostile := withHostileHA(t, tgt, redirectingOrigin(t, upstream.URL))

		out, err := hostile.Read(t, "health", "--timeout", "2s", "--tokensmax", "0")
		if err == nil {
			t.Fatalf("`health` answered from an origin it was not configured with:\n%s", truncate(out))
		}
		if strings.Contains(out, "2026.7.4") {
			t.Errorf("the redirected answer was rendered as this instance's own:\n%s", truncate(out))
		}
		stderr, _ := hostile.ReadDiagnostic(t, "health", "--timeout", "2s")
		for _, want := range []string{"redirects to", "HA_URL="} {
			if !strings.Contains(stderr, want) {
				t.Errorf("the refusal does not name the fix (%q):\n%s", want, truncate(stderr))
			}
		}
	})
}

// rejectingHA is a Home Assistant that completes the WebSocket upgrade and then
// refuses the token — HA's own `auth_invalid` — and answers 401 to REST. It is
// the shape a stale or mistyped HA_TOKEN produces, and the one #75 was reported
// against.
func rejectingHA(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/websocket" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = fmt.Fprint(w, `{"message":"401: Unauthorized"}`)
			return
		}
		c, err := livefireUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.WriteJSON(map[string]string{"type": "auth_required", "ha_version": "2026.7.4"})
		var msg map[string]string
		_ = c.ReadJSON(&msg)
		_ = c.WriteJSON(map[string]string{"type": "auth_invalid", "message": "Invalid access token or password"})
	}))
	t.Cleanup(srv.Close)
	return srv.URL
}
