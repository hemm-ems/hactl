package cmd

import (
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/companion"
)

func TestRootCmd_HasCompanionStatus(t *testing.T) {
	// "companion" group
	grp, _, err := rootCmd.Find([]string{"companion"})
	if err != nil || grp == nil || grp.Name() != "companion" {
		t.Fatalf("rootCmd missing 'companion' subcommand group: cmd=%v err=%v", grp, err)
	}
	// "companion status" sub-sub
	cmd, _, err2 := rootCmd.Find([]string{"companion", "status"})
	if err2 != nil || cmd == nil || cmd.Name() != "status" {
		t.Fatalf("'companion status' not registered: cmd=%v err=%v", cmd, err2)
	}
}

func TestCompanionStatusLine_AuthDenied(t *testing.T) {
	msg := formatCompanionStatusLine("not found", "auth_denied")
	if !strings.Contains(msg, "token lacks hassio_admin") {
		t.Errorf("auth_denied line should mention hassio_admin scope, got: %q", msg)
	}
}

func TestCompanionStatusLine_AddonMissing(t *testing.T) {
	msg := formatCompanionStatusLine("not found", "addon_missing")
	if !strings.Contains(msg, "not installed") {
		t.Errorf("addon_missing line should mention 'not installed', got: %q", msg)
	}
}

func TestCompanionStatusLine_Unreachable(t *testing.T) {
	msg := formatCompanionStatusLine("unreachable", "unreachable")
	if !strings.Contains(msg, "unreachable") {
		t.Errorf("unreachable line should contain 'unreachable', got: %q", msg)
	}
}

func TestCompanionStatusLine_OK(t *testing.T) {
	msg := formatCompanionStatusLine("ok", "")
	if !strings.Contains(msg, "ok") {
		t.Errorf("ok line should contain 'ok', got: %q", msg)
	}
}

// TestEveryDiscoveryReasonHasAStatusLine — H-24 over the set, on this side of
// the package boundary.
//
// companion.DiscoveryReasons() is the closed set of causes (held to its own
// const block by TestDiscoveryReasonsMatchTheConstBlock), and `health`'s one-line
// overview has to render each of them as a next step. The default branch prints
// the bare reason code, which is what the reader of a one-line summary can do
// least with — and `health` printed exactly that for every reason, while the
// function written to explain them was called by nothing but its own tests.
func TestEveryDiscoveryReasonHasAStatusLine(t *testing.T) {
	for _, r := range companion.DiscoveryReasons() {
		line := formatCompanionStatusLine("not found", string(r))
		if strings.Contains(line, "("+string(r)+")") {
			t.Errorf("%q renders as its own reason code — the reader is told a category, not a next step: %q", r, line)
		}
		if !strings.Contains(line, "—") {
			t.Errorf("%q has no remediation clause: %q", r, line)
		}
	}
}

func TestCompanionStatusLine_ProtocolMismatch(t *testing.T) {
	msg := formatCompanionStatusLine("not found", "protocol_mismatch")
	if !strings.Contains(msg, "HA Container") {
		t.Errorf("protocol_mismatch line should mention HA Container, got: %q", msg)
	}
	if !strings.Contains(msg, "COMPANION_URL") {
		t.Errorf("protocol_mismatch line should point at COMPANION_URL, got: %q", msg)
	}
}
