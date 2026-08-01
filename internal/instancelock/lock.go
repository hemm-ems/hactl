// Package instancelock serializes hactl's confirmed writes against one
// Home Assistant instance.
//
// It exists because a confirmed write is a read-modify-write and hactl held
// nothing across it. `auto apply --confirm` fetches the stored entry (that
// fetch is also the backup), validates the candidate against HA over the
// WebSocket, and only then asks the companion to splice the new text in. On
// the reference instance that span measures two to three seconds, and two
// hactl processes launched together both read the same starting state inside
// it: both printed their own diff, both printed `applied` and `reload: ok`,
// both exited 0, and one of the two edits was not in the file afterwards
// (finding #100). The companion's own read-modify-write is milliseconds wide
// and was never the interesting half — hactl's is a thousand times longer.
//
// The unit of exclusion is the instance directory, because that is the unit
// of sharing: it carries the .env naming the instance, the cache the manual
// state lives in, and the backups directory a rollback reads. Two callers
// that share it are the concurrency this lock is for, and they are exactly
// the callers findings #61, #100 and #101 were all reported from.
//
// What it does NOT cover is stated here rather than discovered later: a writer
// that is not this hactl — Home Assistant's own UI, a second machine, or a
// hactl pointed at a different directory carrying the same URL — is not
// excluded by a lock on a local file. The lock makes hactl consistent with
// itself; H-12's read-back is what catches everyone else.
package instancelock

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// LockFileName is the file the advisory lock is taken on. It lives beside the
// other per-instance state rather than in the config directory proper, so a
// caller who copies an instance directory copies no lock state with it.
const LockFileName = "write.lock"

// pollInterval is how often a waiting process retries. The lock is taken by
// confirmed writes only — seconds apart at LLM cadence, and never in a hot
// loop — so a poll is cheaper in code and in failure modes than a blocking
// flock parked in a goroutine that ctx cancellation cannot reach.
const pollInterval = 25 * time.Millisecond

// Lock is a held instance write lock. The zero value is not usable; Acquire
// returns one, Release gives it back.
type Lock struct {
	f    *os.File
	path string
}

// Acquire takes the exclusive write lock for the instance whose state lives in
// cacheDir, waiting until it is free, ctx is done, or wait elapses.
//
// A caller that cannot get the lock is told who has it rather than being left
// to guess: the message names the file, which names the instance. Waiting is
// bounded for the same reason every connection is (H-23) — an unbounded wait
// on a peer that may itself be blocked is a hang, and a hang is the one
// failure an agent cannot report.
//
// cacheDir == "" means the instance directory could not be resolved. That is
// the same fail-open condition manual delivery has: no directory, no shared
// state, nothing to serialize against. It returns a Lock whose Release is a
// no-op rather than an error, because refusing a write over an unresolvable
// cache directory would break every caller that legitimately has none.
func Acquire(ctx context.Context, cacheDir string, wait time.Duration) (*Lock, error) {
	if cacheDir == "" {
		return &Lock{}, nil
	}
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return nil, fmt.Errorf("creating %s: %w", cacheDir, err)
	}
	path := filepath.Join(cacheDir, LockFileName)
	f, err := os.OpenFile(filepath.Clean(path), os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", path, err)
	}

	deadline := time.Now().Add(wait)
	for {
		ok, lockErr := tryLock(f)
		if lockErr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("locking %s: %w", path, lockErr)
		}
		if ok {
			return &Lock{f: f, path: path}, nil
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("waiting for the write lock on %s: %w", path, ctxErr)
		}
		if time.Now().After(deadline) {
			_ = f.Close()
			return nil, fmt.Errorf(
				"another hactl is writing to this instance and did not finish within %s: the write lock %s is held. "+
					"Confirmed writes to one instance are serialized so that the backup a write takes is the state that "+
					"write replaces; retry when the other command has finished, or raise --timeout to wait longer",
				wait, path)
		}
		select {
		case <-ctx.Done():
		case <-time.After(pollInterval):
		}
	}
}

// Release gives the lock back. It is safe on a Lock from an unresolvable cache
// directory and safe to call twice, so a deferred release never needs a nil
// check at the call site.
func (l *Lock) Release() {
	if l == nil || l.f == nil {
		return
	}
	_ = unlock(l.f)
	_ = l.f.Close()
	l.f = nil
}

// Held reports whether this Lock actually holds anything — false for the
// fail-open Lock returned when no instance directory could be resolved. Tests
// assert on it so that "the lock was skipped" can never masquerade as "the
// lock was taken".
func (l *Lock) Held() bool { return l != nil && l.f != nil }

// fdFile is what the platform layer needs from an open file: its descriptor.
// Naming it keeps the two implementations honest about how little they use,
// and lets a test hand them something other than an *os.File.
type fdFile interface{ Fd() uintptr }
