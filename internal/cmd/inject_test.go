package cmd

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/manual"
)

func TestShouldInject(t *testing.T) {
	cases := []struct {
		name                 string
		mode                 manual.Mode
		stdoutTTY, stderrTTY bool
		top                  string
		bare                 bool
		want                 bool
	}{
		{"agent run, family cmd", manual.ModeProgressive, false, false, "health", false, true},
		{"agent run, full mode", manual.ModeFull, false, false, "health", false, true},
		{"agent run, unknown cmd (core only)", manual.ModeProgressive, false, false, "", false, true},
		{"mode off", manual.ModeOff, false, false, "health", false, false},
		{"human at terminal", manual.ModeProgressive, true, true, "health", false, false},
		{"human piping stdout, stderr on TTY", manual.ModeProgressive, false, true, "ent", false, false},
		{"stdout TTY, stderr redirected", manual.ModeProgressive, true, false, "ent", false, false},
		{"bare hactl (help screen)", manual.ModeProgressive, false, false, "", true, false},
		{"rtfm exempt from injection", manual.ModeProgressive, false, false, "rtfm", false, false},
		{"mcp exempt", manual.ModeProgressive, false, false, "mcp", false, false},
		{"setup exempt", manual.ModeProgressive, false, false, "setup", false, false},
		{"version exempt", manual.ModeProgressive, false, false, "version", false, false},
		{"completion machinery exempt", manual.ModeProgressive, false, false, "__complete", false, false},
	}
	for _, tc := range cases {
		if got := shouldInject(tc.mode, tc.stdoutTTY, tc.stderrTTY, tc.top, tc.bare); got != tc.want {
			t.Errorf("%s: shouldInject = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestTopCommandName(t *testing.T) {
	cases := map[string]string{
		"ent ls":     "ent",
		"auto show":  "auto",
		"trace show": "trace",
		"health":     "health",
		"cache":      "cache",
	}
	for args, want := range cases {
		c, _, err := rootCmd.Find(strings.Fields(args))
		if err != nil {
			t.Fatalf("Find(%s): %v", args, err)
		}
		if got := topCommandName(c); got != want {
			t.Errorf("topCommandName(%s) = %q, want %q", args, got, want)
		}
	}
	if got := topCommandName(rootCmd); got != "" {
		t.Errorf("topCommandName(root) = %q, want empty", got)
	}
	if got := topCommandName(nil); got != "" {
		t.Errorf("topCommandName(nil) = %q, want empty", got)
	}
}

// executeCapture runs Execute() the way the real CLI does (args from
// os.Args), with both streams swapped for pipes so the injection hook sees an
// agent-shaped invocation.
func executeCapture(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	// Other tests leak cobra state (SetArgs, --dir flag values); start clean.
	resetSubcommandFlags()
	flagDir = ""
	oldArgs := os.Args
	os.Args = append([]string{"hactl"}, args...) // read by maybeInjectManual
	rootCmd.SetArgs(args)
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr
	defer func() {
		os.Args = oldArgs
		rootCmd.SetArgs(nil)
		os.Stdout, os.Stderr = oldOut, oldErr
		resetSubcommandFlags()
	}()

	err = Execute()

	_ = wOut.Close()
	_ = wErr.Close()
	outB, _ := io.ReadAll(rOut)
	errB, _ := io.ReadAll(rErr)
	return string(outB), string(errB), err
}

func setupInjectEnv(t *testing.T, session string) {
	t.Helper()
	dir := t.TempDir()
	// Offline instance: cache status never talks to HA, auto ls fails fast.
	env := "HA_URL=http://127.0.0.1:1\nHA_TOKEN=test-token\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HACTL_DIR", dir)
	t.Setenv("HACTL_SESSION", session)
	t.Setenv("HACTL_MANUAL_MODE", "progressive")

	old := isTerminal
	isTerminal = func(*os.File) bool { return false }
	t.Cleanup(func() { isTerminal = old })
}

func TestExecute_InjectsOnStderrOnceThenSilent(t *testing.T) {
	setupInjectEnv(t, "e2e-once")

	out1, err1, execErr := executeCapture(t, "cache", "status")
	if execErr != nil {
		t.Fatalf("cache status: %v", execErr)
	}
	for _, want := range []string{
		"[hactl manual core", "## Quick routing",
		"'cache' family how-to", "### Cache & version",
		"=== RESULT of hactl cache status ===",
	} {
		if !strings.Contains(err1, want) {
			t.Errorf("first stderr missing %q", want)
		}
	}
	if !strings.Contains(out1, "traces:") {
		t.Errorf("stdout should carry normal output, got %.80q", out1)
	}
	if strings.Contains(out1, "[hactl manual") {
		t.Error("manual text leaked into stdout")
	}

	out2, err2, execErr := executeCapture(t, "cache", "status")
	if execErr != nil {
		t.Fatalf("second cache status: %v", execErr)
	}
	if err2 != "" {
		t.Errorf("second run stderr should be empty, got %.120q", err2)
	}
	// stdout is untouched by injection on both runs (cache db sizes may
	// drift between runs, so no byte comparison).
	if !strings.Contains(out2, "traces:") || strings.Contains(out2, "[hactl manual") {
		t.Errorf("second stdout should be normal output, got %.80q", out2)
	}
}

func TestExecute_InjectsFamilyOnErrorToo(t *testing.T) {
	setupInjectEnv(t, "e2e-error")

	_, errOut, execErr := executeCapture(t, "auto", "ls")
	if execErr == nil {
		t.Fatal("auto ls against a closed port should error")
	}
	// Cold-start help matters most on failures: manual precedes the error.
	for _, want := range []string{"[hactl manual core", "'auto' family how-to", "=== RESULT of hactl auto ls ==="} {
		if !strings.Contains(errOut, want) {
			t.Errorf("error-path stderr missing %q", want)
		}
	}
	if idx := strings.Index(errOut, "=== RESULT"); idx >= 0 {
		if !strings.Contains(errOut[idx:], "connect") && !strings.Contains(errOut[idx:], "refused") && !strings.Contains(errOut[idx:], "error") {
			t.Logf("note: error text after marker: %.120q", errOut[idx:])
		}
	}
}

func TestExecute_RtfmMarksState(t *testing.T) {
	setupInjectEnv(t, "e2e-rtfm")

	// Full rtfm marks everything delivered for the session…
	if _, _, err := executeCapture(t, "rtfm"); err != nil {
		t.Fatalf("rtfm: %v", err)
	}
	_, errOut, err := executeCapture(t, "cache", "status")
	if err != nil {
		t.Fatalf("cache status: %v", err)
	}
	if errOut != "" {
		t.Errorf("after full rtfm nothing should inject, got %.120q", errOut)
	}
}

func TestExecute_RtfmCoreMarksCoreOnly(t *testing.T) {
	setupInjectEnv(t, "e2e-rtfm-core")

	if _, _, err := executeCapture(t, "rtfm", "--core"); err != nil {
		t.Fatalf("rtfm --core: %v", err)
	}
	_, errOut, err := executeCapture(t, "cache", "status")
	if err != nil {
		t.Fatalf("cache status: %v", err)
	}
	if strings.Contains(errOut, "[hactl manual core") {
		t.Error("core was shown by rtfm --core; must not re-deliver")
	}
	if !strings.Contains(errOut, "'cache' family how-to") {
		t.Errorf("family how-to still due after rtfm --core, got %.120q", errOut)
	}
}

func TestExecute_ModeOffSilent(t *testing.T) {
	setupInjectEnv(t, "e2e-off")
	t.Setenv("HACTL_MANUAL_MODE", "off")

	_, errOut, err := executeCapture(t, "cache", "status")
	if err != nil {
		t.Fatalf("cache status: %v", err)
	}
	if errOut != "" {
		t.Errorf("mode off must not inject, got %.120q", errOut)
	}
}

func TestExecute_FullModeWholeManualOnce(t *testing.T) {
	setupInjectEnv(t, "e2e-full")
	t.Setenv("HACTL_MANUAL_MODE", "full")

	_, err1, execErr := executeCapture(t, "cache", "status")
	if execErr != nil {
		t.Fatalf("cache status: %v", execErr)
	}
	if !strings.Contains(err1, "## MCP server") || !strings.HasPrefix(err1, manual.FullNote) {
		t.Errorf("full mode should deliver the whole manual, got %.80q", err1)
	}
	_, err2, execErr := executeCapture(t, "health")
	if execErr == nil {
		t.Log("health unexpectedly succeeded offline (fine for this test)")
	}
	if strings.Contains(err2, "[hactl manual") {
		t.Errorf("full manual must deliver once, got %.120q", err2)
	}
}
