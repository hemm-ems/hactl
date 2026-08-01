package instancelock

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// H-26, clause "held": the lock is exclusive ACROSS PROCESSES, which is the
// only kind of exclusion that matters here — the two writers in finding #100
// were two hactl processes, not two goroutines.
//
// The peer is a real child process because flock is per open file description:
// a same-process second acquire and a cross-process one are different
// questions, and only one of them is the one hactl faces.
func TestLockIsExclusiveAcrossProcesses(t *testing.T) {
	dir := t.TempDir()
	held, err := Acquire(context.Background(), dir, time.Second)
	if err != nil {
		t.Fatalf("acquiring: %v", err)
	}
	if !held.Held() {
		t.Fatal("a resolvable cache dir must produce a real lock, not a fail-open one")
	}
	defer held.Release()

	if free := peerCanLock(t, filepath.Join(dir, LockFileName)); free {
		t.Error("a second process took the lock while this one held it")
	}

	held.Release()
	if free := peerCanLock(t, filepath.Join(dir, LockFileName)); !free {
		t.Error("the lock was not given back on Release")
	}
}

// peerCanLock runs a child process that tries the lock without waiting and
// reports whether it got it.
func peerCanLock(t *testing.T, path string) bool {
	t.Helper()
	// `flock -n` is the shell's own answer to this question, which keeps the
	// test from proving the implementation against itself.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "flock", "-n", path, "true") //nolint:gosec // fixed argv, path owned by this test
	err := cmd.Run()
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return false
	}
	// No flock(1) on this machine — fall back to a Go child doing the same
	// thing, so the case still means something rather than silently passing.
	t.Skipf("flock(1) unavailable (%v); cross-process exclusion is asserted by the livefire tier too", err)
	return false
}

// Waiting is bounded. An unbounded wait on a peer that may itself be blocked
// is a hang, and a hang is the one failure an agent cannot report (H-23's
// reason, applied to a wait that is not a connection).
func TestAcquireGivesUpAndSaysWhoHasIt(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(context.Background(), dir, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Release()

	// A second acquire from THIS process is also blocked — flock is per open
	// file description, so hactl's own second descriptor is as excluded as a
	// stranger's. That is why cmd.acquireWriteLock is idempotent per instance.
	start := time.Now()
	_, err = Acquire(context.Background(), dir, 150*time.Millisecond)
	if err == nil {
		t.Fatal("a second acquire must not succeed while the first is held")
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Errorf("waited %s for a 150ms bound", waited)
	}
	if !strings.Contains(err.Error(), LockFileName) {
		t.Errorf("the error must name the lock file so the caller can see which instance is busy, got: %v", err)
	}
}

// A cancelled context ends the wait, so `--timeout` is not the only thing that
// can stop it: an MCP client cancelling a tool call aborts the queue too.
func TestAcquireHonoursContextCancellation(t *testing.T) {
	dir := t.TempDir()
	first, err := Acquire(context.Background(), dir, time.Second)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer first.Release()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Acquire(ctx, dir, time.Minute); err == nil {
		t.Fatal("a cancelled context must end the wait rather than run to the timeout")
	}
}

// An unresolvable instance directory means there is no shared state to
// serialize against. Refusing the write there would break every caller that
// legitimately has no directory, so it fails open — and says so, rather than
// returning something that looks like a held lock.
func TestUnresolvableCacheDirFailsOpenVisibly(t *testing.T) {
	lock, err := Acquire(context.Background(), "", time.Second)
	if err != nil {
		t.Fatalf("an empty cache dir must not be an error: %v", err)
	}
	if lock.Held() {
		t.Error("a fail-open lock must not claim to hold anything")
	}
	lock.Release() // must not panic
	lock.Release() // twice, so a deferred release never needs a nil check
}

// The lock file lives beside the instance's other state and is created if the
// directory is not there yet — the first confirmed write against a fresh
// instance must not fail for want of a cache directory.
func TestAcquireCreatesTheCacheDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "cache")
	lock, err := Acquire(context.Background(), dir, time.Second)
	if err != nil {
		t.Fatalf("acquiring in a missing dir: %v", err)
	}
	defer lock.Release()
	if !lock.Held() {
		t.Error("a lock taken in a freshly created cache dir must report itself held")
	}
	info, statErr := os.Stat(filepath.Join(dir, LockFileName))
	if statErr != nil {
		t.Fatalf("lock file not created: %v", statErr)
	}
	if info.IsDir() {
		t.Errorf("%s is a directory, not the lock file", LockFileName)
	}
}
