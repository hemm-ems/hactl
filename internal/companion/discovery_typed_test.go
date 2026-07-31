package companion

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

func TestDiscoveryError_Reason(t *testing.T) {
	err := &DiscoveryError{Reason: ReasonAuthDenied, msg: "test"}
	if err.Reason != ReasonAuthDenied {
		t.Errorf("Reason = %q, want %q", err.Reason, ReasonAuthDenied)
	}
	if err.Error() != "test" {
		t.Errorf("Error() = %q, want %q", err.Error(), "test")
	}
}

func TestDiscoveryError_AsUnwrap(t *testing.T) {
	inner := &DiscoveryError{Reason: ReasonAddonMissing, msg: "addon not found"}
	wrapped := errors.New("outer: " + inner.Error())
	_ = wrapped // just confirm DiscoveryError is directly usable
	var de *DiscoveryError
	if !errors.As(inner, &de) {
		t.Fatal("errors.As should find DiscoveryError in itself")
	}
}

func TestClassifyWSError(t *testing.T) {
	cases := []struct {
		input  string
		reason DiscoveryReason
	}{
		// New Supervisor-WS-proxy error strings (post PR 2):
		{"supervisor/api /addons failed: Forbidden", ReasonAuthDenied},
		{"supervisor/api /addons failed: unauthorized", ReasonAuthDenied},
		{"supervisor/api /addons failed: 401", ReasonAuthDenied},
		{"supervisor/api /addons/foo/info failed: addon not found", ReasonAddonMissing},
		{"supervisor/api /addons failed: not found", ReasonAddonMissing},

		// HA Container / no Supervisor — HA Core returns unknown_command for supervisor/api.
		// Classifier must surface this as a distinct reason so the hint can tell the
		// user to set COMPANION_URL rather than chasing a network problem.
		{"supervisor/api failed: unknown_command", ReasonProtocolMismatch},
		{"supervisor/api failed: unknown_message_type", ReasonProtocolMismatch},
		{"sending supervisor/api: unknown command: supervisor/api", ReasonProtocolMismatch},

		// Network / transport failures.
		{"connecting to websocket: connection refused", ReasonUnreachable},
		{"some other error", ReasonUnreachable},
	}
	for _, c := range cases {
		got := classifyWSError(c.input)
		if got != c.reason {
			t.Errorf("classifyWSError(%q) = %q, want %q", c.input, got, c.reason)
		}
	}
}

// TestDiscoveryErrorMessages — every reason names its own fix.
//
// The two added here are the ones live-fire #75 was about: an authentication
// failure and a redirected origin both rendered as "unreachable", with a hint
// that told the reader to check their network and Ingress. Neither is a network
// problem and for neither of them does COMPANION_URL help, which is what the
// hint had recommended.
func TestDiscoveryErrorMessages(t *testing.T) {
	cases := []struct {
		reason  DiscoveryReason
		wantMsg string
	}{
		{ReasonAuthDenied, "hassio_admin"},
		{ReasonAuthInvalid, "HA_TOKEN"},
		{ReasonAddonMissing, "not installed"},
		{ReasonUnreachable, "unreachable"},
		{ReasonRedirected, "redirects"},
		{ReasonProtocolMismatch, "HA Container"},
	}
	for _, c := range cases {
		err := newDiscoveryError(c.reason, nil)
		if !strings.Contains(err.Error(), c.wantMsg) {
			t.Errorf("newDiscoveryError(%q).Error() = %q, want it to contain %q", c.reason, err.Error(), c.wantMsg)
		}
	}
	// A remediation that describes a category and never says what happened is
	// half the answer: the cause is quoted, and reachable through errors.As so a
	// caller can match on it rather than on the prose.
	cause := errors.New("dial tcp 10.255.255.1:80: i/o timeout")
	withCause := newDiscoveryError(ReasonUnreachable, cause)
	if !strings.Contains(withCause.Error(), cause.Error()) {
		t.Errorf("a discovery error must quote what failed:\n%s", withCause.Error())
	}
	if !errors.Is(withCause, cause) {
		t.Error("the cause must stay reachable through errors.Is")
	}
}

// TestClassifyConnectError — the reason comes from the typed error the
// transport produced, never from the absence of a connection.
//
// `companion status` handed companion.Discover a nil client whenever Connect
// failed, so a rejected token, a refused port and an origin that redirects
// elsewhere were one and the same fact: "we have no socket". All three printed
// `discovery_reason: "unreachable"` and the same "Check Ingress / network" hint
// (#75, #76).
func TestClassifyConnectError(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want DiscoveryReason
	}{
		{"rejected token", &haapi.AuthError{Message: "Invalid access token or password"}, ReasonAuthInvalid},
		{"redirected origin", &haapi.RedirectError{Configured: "http://ha", Redirect: "https://ha"}, ReasonRedirected},
		{"wrapped redirect", fmt.Errorf("connecting: %w", &haapi.RedirectError{Configured: "http://ha", Redirect: "https://ha"}), ReasonRedirected},
		{"refused port", errors.New("dial tcp 127.0.0.1:1: connect: connection refused"), ReasonUnreachable},
		{"nothing to classify", nil, ReasonUnreachable},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyConnectError(c.err); got != c.want {
				t.Errorf("classifyConnectError(%v) = %q, want %q", c.err, got, c.want)
			}
		})
	}
}

func TestMatchCompanion(t *testing.T) {
	cases := []struct {
		name     string
		addons   []addonEntry
		wantSlug string
	}{
		{
			name: "bare slug (dev / local install)",
			addons: []addonEntry{
				{Slug: "core_zwave_js", Name: "Z-Wave JS"},
				{Slug: "hactl_companion", Name: "hactl companion"},
			},
			wantSlug: "hactl_companion",
		},
		{
			name: "repo-prefixed slug (Supervisor install)",
			addons: []addonEntry{
				{Slug: "4f607318_hactl_companion", Name: "hactl companion"},
				{Slug: "core_zwave_js", Name: "Z-Wave JS"},
			},
			wantSlug: "4f607318_hactl_companion",
		},
		{
			name: "name-only fallback (slug differs unexpectedly)",
			addons: []addonEntry{
				{Slug: "weird_id_no_match", Name: "hactl companion"},
			},
			wantSlug: "weird_id_no_match",
		},
		{
			name: "case-insensitive name match",
			addons: []addonEntry{
				{Slug: "x", Name: "Hactl Companion"},
			},
			wantSlug: "x",
		},
		{
			name: "suffix-look-alike must not match (different add-on with similar suffix)",
			addons: []addonEntry{
				{Slug: "x_hactl_companion_test", Name: "Some Test Addon"},
			},
			wantSlug: "",
		},
		{
			name:     "empty list",
			addons:   nil,
			wantSlug: "",
		},
		{
			name: "bare slug takes priority over repo-prefixed (deterministic)",
			addons: []addonEntry{
				{Slug: "4f607318_hactl_companion", Name: "hactl companion"},
				{Slug: "hactl_companion", Name: "hactl companion (dev)"},
			},
			wantSlug: "hactl_companion",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := matchCompanion(c.addons)
			if got != c.wantSlug {
				t.Errorf("matchCompanion = %q, want %q", got, c.wantSlug)
			}
		})
	}
}
