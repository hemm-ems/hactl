package cmd

import (
	"strings"
	"testing"
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
