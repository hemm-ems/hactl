package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// INVARIANTS.md H-24 — a connectivity answer names the cause the transport
// reported, and its exit code carries the verdict.
//
// Live-fire #74 #75 #76. `companion status` printed "WS connect: failed",
// "discovery: failed (unreachable)" and "companion not found (unreachable)" for
// a rejected token, a refused port and an i/o timeout alike, and returned exit
// 0 in all three — so `hactl companion status && proceed` proceeded.

// refusedPort returns a URL nothing is listening on. Binding and closing is how
// a port is known to be free; picking a number and hoping is how a test becomes
// flaky on somebody else's machine.
func refusedPort(t *testing.T) string {
	t.Helper()
	ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listening: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return "http://" + addr
}

// statusRun drives `companion status` against a directory whose .env names haURL
// and returns what it printed and what it returned.
func statusRun(t *testing.T, haURL string, asJSON bool) (string, error) {
	t.Helper()
	dir := t.TempDir()
	writeEnv(t, dir, haURL)

	prevDir, prevJSON, prevTimeout := flagDir, flagJSON, haapi.DefaultTimeout
	flagDir, flagJSON, haapi.DefaultTimeout = dir, asJSON, 3*time.Second
	t.Cleanup(func() { flagDir, flagJSON, haapi.DefaultTimeout = prevDir, prevJSON, prevTimeout })

	buf := new(bytes.Buffer)
	err := runCompanionStatus(context.Background(), buf)
	return buf.String(), err
}

// TestCompanionStatusExitsNonZeroWhenTheCompanionIsNotUsable — finding #74.
//
// The exit code is asserted through the sentinel the entry point reads
// (main.go's `interface{ ExitCode() int }`), because that is the contract: a
// command signals failure by returning an error that carries a code, and
// anything else — including a nil return beside a body that says "failed" — is
// exit 0 to every caller.
func TestCompanionStatusExitsNonZeroWhenTheCompanionIsNotUsable(t *testing.T) {
	out, err := statusRun(t, refusedPort(t), false)
	if err == nil {
		t.Fatalf("`companion status` against a refused port returned success; it printed:\n%s", out)
	}
	var ec interface{ ExitCode() int }
	if !errors.As(err, &ec) || ec.ExitCode() == 0 {
		t.Errorf("error %v does not carry a non-zero exit code", err)
	}
	// D-33: the verdict ends the command, it does not erase the report.
	if out == "" {
		t.Error("the diagnostic printed nothing — the answer was erased by its own verdict")
	}
}

// TestCompanionStatusExitsZeroWhenTheCompanionAnswers is the control. Without
// it the case above is satisfied by a command that always fails, which is the
// same defect with the sign flipped.
func TestCompanionStatusExitsZeroWhenTheCompanionAnswers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"2026.7.9"}`))
	}))
	defer srv.Close()

	dir := t.TempDir()
	if writeErr := os.WriteFile(filepath.Join(dir, ".env"),
		[]byte("HA_URL="+srv.URL+"\nHA_TOKEN=test\nCOMPANION_URL="+srv.URL+"\n"), 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}

	prevDir, prevTimeout := flagDir, haapi.DefaultTimeout
	flagDir, haapi.DefaultTimeout = dir, 3*time.Second
	t.Cleanup(func() { flagDir, haapi.DefaultTimeout = prevDir, prevTimeout })

	buf := new(bytes.Buffer)
	if err := runCompanionStatus(context.Background(), buf); err != nil {
		t.Fatalf("a healthy companion must exit 0: %v\n%s", err, buf.String())
	}
	// Exit 0 is only the control if the run really succeeded: a command that
	// discovered nothing and returned nil would satisfy the line above and be
	// the defect this file is about, with the sign flipped.
	out := buf.String()
	for _, want := range []string{"health:      ok", "version:     2026.7.9"} {
		if !strings.Contains(out, want) {
			t.Errorf("a successful status must report %q:\n%s", want, out)
		}
	}
}

// TestCompanionStatusNamesTheCauseTheTransportReported — findings #75 and #76.
//
// Each row is a different root cause reaching the same command. Before the fix
// every one of them produced `discovery_reason: "unreachable"` with a hint
// telling the reader to check Ingress and the network — actively wrong for two
// of the three, and self-contradicting for the first, whose own `ws_error`
// field said the token was invalid.
func TestCompanionStatusNamesTheCauseTheTransportReported(t *testing.T) {
	rejecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := wsTestUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = c.Close() }()
		_ = c.WriteJSON(map[string]string{"type": "auth_required", "ha_version": "2026.7"})
		var msg map[string]string
		_ = c.ReadJSON(&msg)
		_ = c.WriteJSON(map[string]string{"type": "auth_invalid", "message": "Invalid access token or password"})
	}))
	defer rejecting.Close()

	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1"+r.URL.Path, http.StatusMovedPermanently)
	}))
	defer elsewhere.Close()

	for _, tc := range []struct {
		name   string
		url    string
		reason companion.DiscoveryReason
		hint   string
	}{
		{"rejected token", rejecting.URL, companion.ReasonAuthInvalid, "HA_TOKEN"},
		{"redirected origin", elsewhere.URL, companion.ReasonRedirected, "HA_URL"},
		{"nothing listening", refusedPort(t), companion.ReasonUnreachable, "HA_URL"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, err := statusRun(t, tc.url, true)
			if err == nil {
				t.Fatalf("must fail; printed:\n%s", out)
			}
			var res struct {
				WSError         string `json:"ws_error"`
				DiscoveryReason string `json:"discovery_reason"`
				DiscoveryHint   string `json:"discovery_hint"`
			}
			if jsonErr := json.Unmarshal([]byte(out), &res); jsonErr != nil {
				t.Fatalf("--json output does not parse: %v\n%s", jsonErr, out)
			}
			if res.DiscoveryReason != string(tc.reason) {
				t.Errorf("discovery_reason = %q, want %q (ws_error was %q)",
					res.DiscoveryReason, tc.reason, res.WSError)
			}
			if !strings.Contains(res.DiscoveryHint, tc.hint) {
				t.Errorf("the hint does not name what to change (%q):\n%s", tc.hint, res.DiscoveryHint)
			}
			if res.WSError == "" {
				t.Error("ws_error is empty: the cause was not recorded at all")
			}
		})
	}
}
