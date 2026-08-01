package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/manual"
)

// setupGuardEnv is setupInjectEnv against an instance whose previews actually
// SUCCEED.
//
// That distinction is the whole subject of these tests now. The guard used to
// ask whether the family how-to had been delivered, and a how-to is delivered
// by any command of the family — including one that failed. It now asks
// whether a dry-run of this exact write ran and worked, so a harness pointed
// at a dead address can no longer produce the state under test.
func setupGuardEnv(t *testing.T, session string) string {
	t.Helper()
	dir := t.TempDir()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/config/wiring" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"domain":"input_boolean","wired":true,"file":"input_boolean.yaml"}`)
			return
		}
		http.Error(w, "not found", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)

	env := fmt.Sprintf("HA_URL=http://127.0.0.1:1\nHA_TOKEN=test-token\nCOMPANION_URL=%s\n", srv.URL)
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cand.yaml"), []byte("id: guard_auto\nalias: Guard\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HACTL_DIR", dir)
	t.Setenv("HACTL_SESSION", session)
	t.Setenv("HACTL_MANUAL_MODE", "progressive")

	old := isTerminal
	isTerminal = func(*os.File) bool { return false }
	t.Cleanup(func() { isTerminal = old })
	return dir
}

// H-26, clause "witnessed": a --confirm write no dry-run preceded is refused,
// and the refusal names the dry-run to run.
//
// The command under test is deliberately one whose PREVIEW works: an agent
// that fires --confirm blind is the shape being caught, and it must be caught
// on a healthy instance rather than only where everything fails anyway.
func TestExecute_ConfirmGuardRefusesUnpreviewedWrite(t *testing.T) {
	dir := setupGuardEnv(t, "guard-no-preview")

	_, errOut, execErr := executeCapture(t, "auto", "create", "-f", filepath.Join(dir, "cand.yaml"), "--confirm")
	if execErr == nil {
		t.Fatal("a --confirm with no dry-run behind it must be refused")
	}
	if !strings.Contains(execErr.Error(), "--confirm refused") {
		t.Fatalf("expected guard refusal, got: %v", execErr)
	}
	for _, want := range []string{
		"no dry-run of \"hactl auto create\" was recorded",
		"run `hactl auto create` without --confirm",
		"HACTL_MANUAL_MODE=off",
	} {
		if !strings.Contains(execErr.Error(), want) {
			t.Errorf("refusal missing %q\ngot: %v", want, execErr)
		}
	}
	// The how-to still rides along, in the same layout: a refusal an agent
	// cannot act on is worse than the silence it replaced. The note is taken
	// from the manual package rather than typed out, so a family that gains a
	// command renames its own note here too.
	for _, want := range []string{manual.CoreNote, manual.FamilyNote("auto")} {
		if !strings.Contains(errOut, want) {
			t.Errorf("refusal stderr missing %q", want)
		}
	}
}

// A refusal is not itself a preview. The old guard delivered the how-to as it
// refused, so the immediate retry passed — the caller was let through by the
// very message telling it to go and look first.
func TestExecute_ConfirmGuardRefusalDoesNotAuthorizeTheRetry(t *testing.T) {
	dir := setupGuardEnv(t, "guard-retry")
	file := filepath.Join(dir, "cand.yaml")

	if _, _, err := executeCapture(t, "auto", "create", "-f", file, "--confirm"); err == nil {
		t.Fatal("first --confirm must be refused")
	}
	_, _, execErr := executeCapture(t, "auto", "create", "-f", file, "--confirm")
	if execErr == nil || !strings.Contains(execErr.Error(), "--confirm refused") {
		t.Fatalf("the retry must still be refused — nothing previewed the write; got: %v", execErr)
	}
}

// The documented protocol passes: dry-run, then --confirm.
func TestExecute_ConfirmGuardPassesAfterDryRun(t *testing.T) {
	dir := setupGuardEnv(t, "guard-dry-run-first")
	file := filepath.Join(dir, "cand.yaml")

	if _, _, err := executeCapture(t, "auto", "create", "-f", file); err != nil {
		t.Fatalf("dry-run should succeed against the stub: %v", err)
	}
	_, _, execErr := executeCapture(t, "auto", "create", "-f", file, "--confirm")
	if execErr != nil && strings.Contains(execErr.Error(), "--confirm refused") {
		t.Fatalf("post-dry-run --confirm must pass the guard, got: %v", execErr)
	}
}

// The hole the old guard had, stated as a test: a READ of the family delivers
// the family how-to, so `auto ls` used to authorize `auto create --confirm`.
// A listing is not a plan.
func TestExecute_ConfirmGuardIsNotSatisfiedByAFamilyRead(t *testing.T) {
	dir := setupGuardEnv(t, "guard-family-read")

	// `auto ls` fails against the dead HA address, but it delivers the 'auto'
	// how-to on stderr either way — which is exactly what used to be enough.
	_, errOut, _ := executeCapture(t, "auto", "ls")
	if !strings.Contains(errOut, manual.FamilyNote("auto")) {
		t.Fatalf("precondition: auto ls must deliver the auto how-to, got %.160q", errOut)
	}

	_, _, execErr := executeCapture(t, "auto", "create", "-f", filepath.Join(dir, "cand.yaml"), "--confirm")
	if execErr == nil || !strings.Contains(execErr.Error(), "--confirm refused") {
		t.Fatalf("a family read must not authorize a write; got: %v", execErr)
	}
}

func TestExecute_ConfirmGuardOffWithManualMode(t *testing.T) {
	dir := setupGuardEnv(t, "guard-mode-off")
	t.Setenv("HACTL_MANUAL_MODE", "off")

	_, _, execErr := executeCapture(t, "auto", "create", "-f", filepath.Join(dir, "cand.yaml"), "--confirm")
	if execErr != nil && strings.Contains(execErr.Error(), "--confirm refused") {
		t.Fatalf("HACTL_MANUAL_MODE=off must disable the guard, got: %v", execErr)
	}
}

func TestExecute_ConfirmGuardIgnoresNonWriteCommands(t *testing.T) {
	setupGuardEnv(t, "guard-non-write")

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

// `--confirm=false` is not a confirm. The old guard scanned unparsed argv for
// the literal strings "--confirm" and "--confirm=true"; this one reads the
// value cobra parsed, so every spelling pflag accepts is handled by pflag.
func TestExecute_ConfirmGuardReadsTheParsedValue(t *testing.T) {
	dir := setupGuardEnv(t, "guard-parsed-value")

	_, _, execErr := executeCapture(t, "auto", "create", "-f", filepath.Join(dir, "cand.yaml"), "--confirm=false")
	if execErr != nil && strings.Contains(execErr.Error(), "--confirm refused") {
		t.Fatalf("--confirm=false is a dry-run and must not be guarded, got: %v", execErr)
	}
}

// witnessKey is what makes the guard target-scoped rather than instance-wide,
// which is the whole of finding #61: a preview authorizes the write it
// previewed, not every write on the machine.
func TestWitnessKeyDistinguishesCommandAndTarget(t *testing.T) {
	cases := []struct {
		name     string
		aCmd     string
		aArgs    []string
		bCmd     string
		bArgs    []string
		wantSame bool
	}{
		{"same command and target", "hactl auto apply", []string{"x"}, "hactl auto apply", []string{"x"}, true},
		{"different target", "hactl auto apply", []string{"x"}, "hactl auto apply", []string{"y"}, false},
		{"different command", "hactl auto apply", []string{"x"}, "hactl auto delete", []string{"x"}, false},
		{"targetless commands", "hactl auto create", nil, "hactl auto create", nil, true},
		{"one target vs none", "hactl auto apply", []string{"x"}, "hactl auto apply", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			same := witnessKey("s", tc.aCmd, tc.aArgs) == witnessKey("s", tc.bCmd, tc.bArgs)
			if same != tc.wantSame {
				t.Errorf("witnessKey(%q,%v) vs (%q,%v): same=%v want %v",
					tc.aCmd, tc.aArgs, tc.bCmd, tc.bArgs, same, tc.wantSame)
			}
		})
	}
}

// A preview goes stale. The plan it printed is a claim about a live instance,
// and an hour-old diff is a memory rather than a plan.
func TestWitnessExpires(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	recordWitness(dir, "session-a", "hactl auto apply", []string{"x"}, at)

	if !hasWitness(dir, "session-a", "hactl auto apply", []string{"x"}, at.Add(witnessTTL-time.Second)) {
		t.Error("a preview inside the window must authorize its write")
	}
	if hasWitness(dir, "session-a", "hactl auto apply", []string{"x"}, at.Add(witnessTTL+time.Second)) {
		t.Error("a preview older than the window must not authorize anything")
	}
	if hasWitness(dir, "session-a", "hactl auto apply", []string{"other"}, at) {
		t.Error("a preview of one target must not authorize another")
	}
	// #61 itself, two levels down: a caller that names its session does not
	// inherit another caller's preview of the same object. The sweep found this
	// one — two sibling cases previewing pg_f3_auto_1 authorized a third whose
	// entire precondition was that nothing had.
	if hasWitness(dir, "session-b", "hactl auto apply", []string{"x"}, at) {
		t.Error("one session's preview must not authorize another session's write")
	}
}

// No instance directory means no record, and no record must not mean "yes".
// hasWitness answering true for an unresolvable cache dir would make the guard
// vacuous exactly where it cannot see anything.
func TestWitnessWithoutCacheDirIsNotAWitness(t *testing.T) {
	if hasWitness("", "session-a", "hactl auto apply", []string{"x"}, time.Now()) {
		t.Error("an unresolvable cache dir must not report a preview it cannot have read")
	}
}
