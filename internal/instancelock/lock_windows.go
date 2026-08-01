//go:build windows

package instancelock

import (
	"errors"

	"golang.org/x/sys/windows"
)

// tryLock takes the exclusive lock without blocking, reporting whether it got
// it. Windows releases a LockFileEx region when the handle closes, which the
// process exiting does — the same "no stale lock to break" property flock
// gives on every other target.
func tryLock(f fdFile) (bool, error) {
	var overlapped windows.Overlapped
	err := windows.LockFileEx(
		windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, &overlapped,
	)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, windows.ERROR_LOCK_VIOLATION), errors.Is(err, windows.ERROR_IO_PENDING):
		return false, nil
	default:
		return false, err
	}
}

func unlock(f fdFile) error {
	var overlapped windows.Overlapped
	return windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &overlapped)
}
