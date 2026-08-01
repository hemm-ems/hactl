//go:build !windows

package instancelock

import (
	"errors"
	"syscall"
)

// tryLock takes the exclusive advisory lock without blocking, reporting
// whether it got it. flock is released by the kernel when the process exits,
// which is why it is used here rather than an O_EXCL sentinel file: a crashed
// writer leaves no lock behind, so there is no stale-lock heuristic to get
// wrong and no --force flag to invite.
func tryLock(f fdFile) (bool, error) {
	//nolint:gosec // G115: a file descriptor from an open *os.File is a small non-negative int; the uintptr is os's own return type
	err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, syscall.EWOULDBLOCK):
		return false, nil
	default:
		return false, err
	}
}

func unlock(f fdFile) error {
	//nolint:gosec // G115: same descriptor this package opened, see tryLock
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
