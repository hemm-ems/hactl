package cmd

import (
	"strings"
	"testing"
)

// The measured F4 shape (dev/tuning e08): --confirm fired as the session's
// first command of a write family must be refused before execution, with the
// family how-to delivered alongside so the retry is informed.
func TestExecute_ConfirmGuardRefusesFirstFamilyWrite(t *testing.T) {
	setupInjectEnv(t, "guard-first-write")

	_, errOut, execErr := executeCapture(t, "dash", "delete", "stray", "--confirm")
	if execErr == nil {
		t.Fatal("first-of-family --confirm should be refused")
	}
	if !strings.Contains(execErr.Error(), "--confirm refused") {
		t.Fatalf("expected guard refusal, got: %v", execErr)
	}
	for _, want := range []string{
		"[hactl manual core", "'dash' family how-to",
		"=== RESULT of hactl dash delete stray --confirm ===",
		"--confirm refused",
	} {
		if !strings.Contains(errOut, want) {
			t.Errorf("refusal stderr missing %q", want)
		}
	}

	// The refusal delivered the how-to; the retry passes the guard and only
	// fails on the offline instance (connection), not on the guard.
	_, errOut2, execErr2 := executeCapture(t, "dash", "delete", "stray", "--confirm")
	if execErr2 != nil && strings.Contains(execErr2.Error(), "--confirm refused") {
		t.Fatalf("retry must pass the guard, got: %v", execErr2)
	}
	if strings.Contains(errOut2, "--confirm refused") {
		t.Error("retry stderr must not carry a second refusal")
	}
}

// The correct protocol — dry-run first, then --confirm — never hits the
// guard: the dry-run call delivers the family how-to.
func TestExecute_ConfirmGuardPassesAfterDryRun(t *testing.T) {
	setupInjectEnv(t, "guard-dry-run-first")

	// Dry-run (fails offline, which still delivers the how-to on stderr).
	_, errOut, _ := executeCapture(t, "dash", "delete", "stray")
	if !strings.Contains(errOut, "'dash' family how-to") {
		t.Fatalf("dry-run should deliver the dash how-to, got %.120q", errOut)
	}

	_, _, execErr := executeCapture(t, "dash", "delete", "stray", "--confirm")
	if execErr != nil && strings.Contains(execErr.Error(), "--confirm refused") {
		t.Fatalf("post-dry-run --confirm must pass the guard, got: %v", execErr)
	}
}

func TestExecute_ConfirmGuardOffWithManualMode(t *testing.T) {
	setupInjectEnv(t, "guard-mode-off")
	t.Setenv("HACTL_MANUAL_MODE", "off")

	_, _, execErr := executeCapture(t, "dash", "delete", "stray", "--confirm")
	if execErr != nil && strings.Contains(execErr.Error(), "--confirm refused") {
		t.Fatalf("HACTL_MANUAL_MODE=off must disable the guard, got: %v", execErr)
	}
}

func TestExecute_ConfirmGuardIgnoresNonWriteCommands(t *testing.T) {
	setupInjectEnv(t, "guard-non-write")

	// cache status has no --confirm flag: the guard must not fire; cobra
	// reports the unknown flag as usual.
	_, errOut, execErr := executeCapture(t, "cache", "status", "--confirm")
	if execErr == nil {
		t.Fatal("unknown flag should error")
	}
	if strings.Contains(errOut, "--confirm refused") {
		t.Error("guard must not fire for commands without a confirm flag")
	}
}

func TestHasConfirmArg(t *testing.T) {
	cases := []struct {
		args []string
		want bool
	}{
		{[]string{"dash", "delete", "x", "--confirm"}, true},
		{[]string{"svc", "call", "light.turn_on", "--confirm=true"}, true},
		{[]string{"dash", "delete", "x"}, false},
		{[]string{"svc", "call", "-d", `{"note":"--confirm"}`, "--confirm=false"}, false},
	}
	for _, tc := range cases {
		if got := hasConfirmArg(tc.args); got != tc.want {
			t.Errorf("hasConfirmArg(%v) = %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestDirFromArgs(t *testing.T) {
	cases := []struct {
		args []string
		want string
	}{
		{[]string{"--dir", "/x/y", "dash", "ls"}, "/x/y"},
		{[]string{"dash", "ls", "--dir=/a"}, "/a"},
		{[]string{"dash", "ls"}, ""},
	}
	for _, tc := range cases {
		if got := dirFromArgs(tc.args); got != tc.want {
			t.Errorf("dirFromArgs(%v) = %q, want %q", tc.args, got, tc.want)
		}
	}
}
