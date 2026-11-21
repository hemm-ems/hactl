//go:build integration && !windows

package integration

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// TestPolicyAgentsSpecWriteLockIsPerHomeAssistant — D-152: "one lock per Home
// Assistant, keyed by its URL, in the user's cache directory — the
// per-directory lock let two agents' writes interleave"; manual: "Writes from
// several agents to one Home Assistant never interleave (D-152): the write
// lock is held per Home Assistant (keyed by its URL), not per directory"; W-5
// acceptance: "two concurrent writes serialize".
//
// Black-box: HOME is the test's own, so "the user's cache directory" is too.
// Agent A's confirmed write must leave its lock there (and nothing in the
// test's HOME existed before). The test then holds every file A left there —
// an advisory flock, the mechanism instancelock already uses (ASSUMPTION: the
// per-URL lock is still a flock, D-152 calls it "a fix to instancelock") —
// and agent B, another directory naming the same Home Assistant, must not
// write until it is released.
func TestPolicyAgentsSpecWriteLockIsPerHomeAssistant(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	home := setupHome(t)
	a := setupInstanceDir(t, inst, w5KitchenPolicy, true)
	b := setupInstanceDir(t, inst, w5KitchenPolicy, true)
	t.Cleanup(func() { deleteAreasNamed(t, inst, "PG W5 Lock") })

	before := treeSnapshot(t, home, nil)
	if r := runSetupBin(t, home, a, nil, "--dir", a, "area", "create", "PG W5 Lock A", "--confirm"); r.Code != 0 {
		t.Fatalf("agent A's allowed area create --confirm failed: %s", r)
	}
	if len(haAreasNamed(t, inst, "PG W5 Lock A")) != 1 {
		t.Fatal("agent A's area create --confirm reported success but Home Assistant has no such area")
	}
	release := holdNewFiles(t, home, before)
	defer release()

	cmd := execBin(t, home, b, "--dir", b, "area", "create", "PG W5 Lock B", "--confirm")
	var out, errOut lockedBuffer
	cmd.Stdout, cmd.Stderr = &out, &errOut
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()

	waiting := true
	select {
	case err := <-done:
		waiting = false
		if code := exitCodeOf(err); code == 0 {
			t.Errorf("D-152: agent B (another directory, same Home Assistant) wrote while this HA's write lock was held — the lock is still per directory\nstdout:\n%s\nstderr:\n%s", out.String(), errOut.String())
			return
		} else if s := strings.ToLower(out.String() + errOut.String()); !strings.Contains(s, "lock") && !strings.Contains(s, "another hactl") {
			t.Errorf("agent B's write failed for another reason than the held lock (exit %d)\nstdout:\n%s\nstderr:\n%s", code, out.String(), errOut.String())
		}
	case <-time.After(4 * time.Second):
		// Still waiting for the lock: what serializing looks like.
	}
	if ids := haAreasNamed(t, inst, "PG W5 Lock B"); len(ids) > 0 {
		t.Errorf("D-152: agent B's area exists while the lock was held: %v", ids)
	}

	release()
	finishAfterRelease(t, waiting, done, cmd, &out, &errOut, func() procResult {
		return runSetupBin(t, home, b, nil, "--dir", b, "area", "create", "PG W5 Lock B", "--confirm")
	})
	if len(haAreasNamed(t, inst, "PG W5 Lock B")) != 1 {
		t.Error("agent B's write succeeded but Home Assistant has no area PG W5 Lock B")
	}
}

// holdNewFiles flocks every file that appeared under home since before — the
// write lock a confirmed write left in the user's cache directory — and
// returns the release.
func holdNewFiles(t *testing.T, home string, before map[string]string) func() {
	t.Helper()
	diff := diffSnapshots(before, treeSnapshot(t, home, nil))
	held := make([]*os.File, 0, len(diff))
	release := func() {
		for _, f := range held {
			_ = unix.Flock(int(f.Fd()), unix.LOCK_UN) //nolint:gosec // G115: a file descriptor fits an int
			_ = f.Close()
		}
		held = nil
	}
	for _, d := range diff {
		rel, ok := strings.CutPrefix(d, "added ")
		if !ok {
			continue
		}
		p := filepath.Join(home, rel)
		f, err := os.OpenFile(filepath.Clean(p), os.O_RDWR, 0)
		if err != nil {
			release()
			t.Fatalf("opening %s: %v", p, err)
		}
		if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil { //nolint:gosec // G115: a file descriptor fits an int
			_ = f.Close()
			release()
			t.Fatalf("locking %s: %v — something still holds it after agent A exited", p, err)
		}
		held = append(held, f)
	}
	if len(held) == 0 {
		t.Fatalf("D-152: a confirmed write left nothing in the user's cache directory (HOME=%s) — the write lock is not held per Home Assistant there", home)
	}
	return release
}

// finishAfterRelease: a B still waiting must now finish with exit 0; a B that
// gave up on the held lock must succeed when run again.
func finishAfterRelease(t *testing.T, waiting bool, done chan error, cmd *exec.Cmd, out, errOut *lockedBuffer, rerun func() procResult) {
	t.Helper()
	if !waiting {
		if r := rerun(); r.Code != 0 {
			t.Fatalf("agent B's write after the lock was released: %s", r)
		}
		return
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("agent B's write, once the lock was released: %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), errOut.String())
		}
	case <-time.After(60 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("agent B's write did not finish within 60s of the lock being released")
	}
}

func exitCodeOf(err error) int {
	var ee *exec.ExitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.ExitCode()
	default:
		return -1
	}
}
