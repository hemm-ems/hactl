package backupfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// frozen pins the clock so the collision under test happens by construction
// rather than by luck. On the reference instance two confirmed writes landed
// 2.1 seconds apart however hard they were launched together — hactl's own
// round trip is wider than the window — so a test that waits for two writes to
// share a wall-clock second is a test that mostly proves nothing.
func frozen(t *testing.T, at time.Time) {
	t.Helper()
	old := Now
	Now = func() time.Time { return at }
	t.Cleanup(func() { Now = old })
}

// H-26, clause "unique": a second backup written in the same instant gets its
// own file, and the first one's bytes are still there.
//
// This is finding #101 stated as an assertion. Before, the name was
// `<second>_<id>.yaml` and the write was os.WriteFile, so the second call
// truncated the first file and returned the same path — and both callers were
// told that path held their recovery point.
func TestWriteNeverOverwritesAnExistingBackup(t *testing.T) {
	dir := t.TempDir()
	frozen(t, time.Date(2026, 8, 1, 10, 44, 48, 0, time.UTC))
	name := func(stamp string) string { return stamp + "_pg_auto.yaml" }

	first, err := Write(dir, 0o600, []byte("STATE ONE"), name)
	if err != nil {
		t.Fatalf("first write: %v", err)
	}
	second, err := Write(dir, 0o600, []byte("STATE TWO"), name)
	if err != nil {
		t.Fatalf("second write: %v", err)
	}

	if first == second {
		t.Fatalf("both writes chose %s: one caller's only recovery point was destroyed", first)
	}
	for path, want := range map[string]string{first: "STATE ONE", second: "STATE TWO"} {
		got, readErr := os.ReadFile(path) //nolint:gosec // G304: paths this test just created
		if readErr != nil {
			t.Fatalf("reading %s: %v", path, readErr)
		}
		if string(got) != want {
			t.Errorf("%s holds %q, want %q", filepath.Base(path), got, want)
		}
	}
	// Chronological order has to survive the disambiguation, because
	// writer.findLatestBackup reads the directory sorted by name and takes the
	// last match as the most recent — `auto rollback` restores whatever that
	// picks.
	if filepath.Base(first) >= filepath.Base(second) {
		t.Errorf("the later backup %s does not sort after the earlier %s; a rollback would restore the wrong one",
			filepath.Base(second), filepath.Base(first))
	}
}

// A file that is already at the chosen path is somebody's only copy of
// something, whoever put it there. This is the live reproduction: a sentinel
// planted at the path the next backup would take, and the backup wrote over it
// and reported success.
func TestWriteDoesNotClobberAFileItDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	at := time.Date(2026, 8, 1, 10, 44, 48, 0, time.UTC)
	frozen(t, at)
	name := func(stamp string) string { return stamp + "_pg_auto.yaml" }

	planted := filepath.Join(dir, name(at.Format(Layout)))
	if err := os.WriteFile(planted, []byte("SENTINEL"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := Write(dir, 0o600, []byte("BACKUP"), name)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if got == planted {
		t.Fatal("the backup took the planted path")
	}
	if raw, _ := os.ReadFile(planted); string(raw) != "SENTINEL" { //nolint:gosec // G304: a path this test created
		t.Errorf("the planted file now holds %q — it was overwritten", raw)
	}
}

// The stamp must be able to express the advance, or the retry loop is a spin.
// Microseconds are not decoration: with a whole-second layout every attempt
// would rebuild the same name and the write would fail after 1000 tries
// instead of succeeding on the second.
func TestLayoutCarriesSubSecondPrecision(t *testing.T) {
	at := time.Date(2026, 8, 1, 10, 44, 48, 0, time.UTC)
	if at.Format(Layout) == at.Add(time.Microsecond).Format(Layout) {
		t.Fatalf("Layout %q cannot distinguish two instants a microsecond apart", Layout)
	}
	if !strings.Contains(at.Format(Layout), ".") {
		t.Errorf("Layout %q carries no fractional part", Layout)
	}
}

// A backup that cannot be written must FAIL rather than return a path nobody
// wrote — H-5 makes that failure end an `auto apply`, which is the answer a
// write with no undo deserves.
func TestWriteFailsRatherThanReturningAPathItDidNotWrite(t *testing.T) {
	dir := t.TempDir()
	frozen(t, time.Date(2026, 8, 1, 10, 44, 48, 0, time.UTC))
	// A name function that ignores the stamp cannot produce a free name, which
	// is the shape of "the retry cannot terminate".
	fixed := func(string) string { return "always-the-same.yaml" }

	if _, err := Write(dir, 0o600, []byte("one"), fixed); err != nil {
		t.Fatalf("first write should succeed: %v", err)
	}
	path, err := Write(dir, 0o600, []byte("two"), fixed)
	if err == nil {
		t.Fatalf("a name that can never be free must fail, got path %s", path)
	}
	if !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Errorf("the error must say what it refused to do, got: %v", err)
	}
	if raw, _ := os.ReadFile(filepath.Join(dir, "always-the-same.yaml")); string(raw) != "one" { //nolint:gosec // G304: a path this test created
		t.Errorf("the existing file was modified: %q", raw)
	}
}
