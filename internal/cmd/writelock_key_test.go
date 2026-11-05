package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/instancelock"
)

// instanceAt writes an instance directory whose .env names haURL.
func instanceAt(t *testing.T, haURL string) string {
	t.Helper()
	dir := t.TempDir()
	env := "HA_URL=" + haURL + "\nHA_TOKEN=test-token\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(env), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// isolateUserCache points the user cache directory (where the D-152 write
// lock lives) at a temp dir for this test.
func isolateUserCache(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	return home
}

// D-152, D-168, H-26: the write lock is one per Home Assistant. Two agents'
// directories naming one Home Assistant — in two spellings — take one lock; a
// directory naming another takes another; a directory with no .env keeps the
// per-directory lock.
func TestWriteLockIsKeyedByTheHAURL(t *testing.T) {
	home := isolateUserCache(t)
	a := instanceAt(t, "http://127.0.0.1:8123")
	b := instanceAt(t, "HTTP://127.0.0.1:8123/")
	other := instanceAt(t, "http://127.0.0.1:8124")
	bare := t.TempDir()

	la, lb, lo := writeLockDir(a), writeLockDir(b), writeLockDir(other)
	if la != lb {
		t.Errorf("two directories on one Home Assistant take two locks: %s and %s", la, lb)
	}
	if la == lo {
		t.Errorf("two Home Assistants share the lock %s", la)
	}
	if strings.HasPrefix(la, a) || !strings.HasPrefix(la, home) {
		t.Errorf("the lock %s is not in the user's cache directory (%s)", la, home)
	}
	if got, want := writeLockDir(bare), filepath.Join(bare, "cache"); got != want {
		t.Errorf("a directory without .env locks %s, want its own cache dir %s", got, want)
	}

	// Behaviour, not just the path: while a writer from directory a holds the
	// lock, a confirmed write from directory b waits and gives up naming it.
	held, err := instancelock.Acquire(context.Background(), la, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	var out bytes.Buffer
	err = RunWithOutput([]string{"hactl", "--dir", b, "--timeout", "300ms", "label", "create", "pg_lockprobe", "--confirm"}, &out)
	if err == nil || !strings.Contains(err.Error(), filepath.Join(la, instancelock.LockFileName)) {
		t.Fatalf("a confirmed write from the second directory while the first holds the lock: err = %v, want the lock %s named", err, la)
	}
	held.Release()

	// And a directory on another Home Assistant is not held up by it.
	held2, err := instancelock.Acquire(context.Background(), la, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer held2.Release()
	err = RunWithOutput([]string{"hactl", "--dir", other, "--timeout", "300ms", "label", "create", "pg_lockprobe", "--confirm"}, &out)
	if err != nil && strings.Contains(err.Error(), "write lock") {
		t.Errorf("a write to another Home Assistant waited for this one's lock: %v", err)
	}
}
