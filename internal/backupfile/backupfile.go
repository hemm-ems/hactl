// Package backupfile writes the files hactl creates to preserve a state it is
// about to replace, under names that cannot collide.
//
// It exists because all three of them named the file from a clock at
// one-second resolution and then wrote it with os.WriteFile — which truncates
// whatever is already there. Two confirmed writes to one automation inside the
// same second both logged `backup created` pointing at the same path, both
// printed that path to their caller as a recovery point, and one file existed
// afterwards (finding #101).
//
// The collision was demonstrated on the reference instance without needing two
// writers at all, which is the sharper way to state it: a file was planted at
// the path the next backup would choose, the apply was run, and the backup
// overwrote it and reported success. The defect is not that a clock repeats —
// it is that nothing asked whether the name was free.
//
// So the name carries microseconds and the file is created with O_EXCL. A name
// that is taken is not overwritten and not silently reused: the stamp advances
// and the create is retried, and if that cannot find a free name the write
// FAILS. For `auto apply` that failure ends the command (H-5), which is the
// right answer — a write whose backup could not be taken is a write with no
// undo.
package backupfile

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Layout is the stamp every preserved file is named from.
//
// Microseconds are what makes the retry below terminate: a stamp that cannot
// express the advance would produce the same name forever. Lexicographic order
// over this layout is chronological order, which `writer.findLatestBackup`
// depends on — it reads the directory (sorted by name) and takes the last
// match as the most recent.
const Layout = "2006-01-02T15-04-05.000000"

// maxAttempts bounds the search for a free name. Each attempt advances the
// stamp by a microsecond, so this is a one-millisecond window — far wider than
// any burst of confirmed writes, and narrow enough that a directory somebody
// has filled with a million files fails loudly instead of spinning.
const maxAttempts = 1000

// Now is the clock, replaceable so a test can put two writes in one
// microsecond and watch the collision be handled rather than hope for it.
var Now = time.Now

// Write stores data in dir under the name that name() builds from a timestamp,
// and returns the path it actually used.
//
// name receives the stamp and returns the whole filename, because the three
// call sites put the stamp in three different places: `<stamp>_<id>.yaml` for
// an automation, `<stamp>_script_<id>.yaml` for a script, and
// `<slug>.<stamp>.json` for a dashboard snapshot. What they share is the rule
// this function enforces, not the shape of the name.
func Write(dir string, perm os.FileMode, data []byte, name func(stamp string) string) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", fmt.Errorf("creating backup dir: %w", err)
	}
	at := Now()
	for range maxAttempts {
		path := filepath.Join(dir, name(at.Format(Layout)))
		f, err := os.OpenFile(filepath.Clean(path), os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
		if errors.Is(err, os.ErrExist) {
			// Taken — by a concurrent writer, or by a file that was already
			// there. Either way it is somebody's only copy of something, and
			// the one thing this function may not do is write over it.
			at = at.Add(time.Microsecond)
			continue
		}
		if err != nil {
			return "", fmt.Errorf("creating backup file: %w", err)
		}
		if _, err = f.Write(data); err != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return "", fmt.Errorf("writing backup: %w", err)
		}
		if err = f.Close(); err != nil {
			return "", fmt.Errorf("closing backup: %w", err)
		}
		return path, nil
	}
	return "", fmt.Errorf(
		"no free backup name in %s after %d attempts around %s: refusing to overwrite an existing backup",
		dir, maxAttempts, at.Format(Layout))
}
