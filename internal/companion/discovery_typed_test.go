package companion

import (
	"errors"
	"strings"
	"testing"
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

func TestDiscoveryErrorMessages(t *testing.T) {
	cases := []struct {
		reason  DiscoveryReason
		wantMsg string
	}{
		{ReasonAuthDenied, "hassio_admin"},
		{ReasonAddonMissing, "not installed"},
		{ReasonUnreachable, "unreachable"},
		{ReasonProtocolMismatch, "HA Container"},
	}
	for _, c := range cases {
		err := newDiscoveryError(c.reason)
		if !strings.Contains(err.Error(), c.wantMsg) {
			t.Errorf("newDiscoveryError(%q).Error() = %q, want it to contain %q", c.reason, err.Error(), c.wantMsg)
		}
	}
}

func TestMatchCompanion(t *testing.T) {
	cases := []struct {
		name    string
		addons  []addonEntry
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
			name:    "empty list",
			addons:  nil,
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
