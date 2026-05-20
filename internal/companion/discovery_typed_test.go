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

func TestClassifyDiscoveryError(t *testing.T) {
	cases := []struct {
		input  string
		reason DiscoveryReason
	}{
		{"hassio/addon/info failed: Forbidden", ReasonAuthDenied},
		{"hassio/addon/info failed: unauthorized", ReasonAuthDenied},
		{"hassio/addon/info failed: 401", ReasonAuthDenied},
		{"hassio/addon/info failed: addon not found", ReasonAddonMissing},
		{"hassio/addon/info failed: not found", ReasonAddonMissing},
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
	}
	for _, c := range cases {
		err := newDiscoveryError(c.reason)
		if !strings.Contains(err.Error(), c.wantMsg) {
			t.Errorf("newDiscoveryError(%q).Error() = %q, want it to contain %q", c.reason, err.Error(), c.wantMsg)
		}
	}
}
