//go:build livefire

package livefire

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Profile is which instance a case is running against.
type Profile string

const (
	// Rig is the Docker instance: writes are unrestricted because nothing on it
	// belongs to anybody.
	Rig Profile = "rig"
	// Live is the real Home Assistant: reads are unrestricted, writes are
	// confined to pg_* by guardLiveWrite and nothing else may be touched.
	Live Profile = "live"
)

// Target is an instance a case can drive hactl against.
type Target struct {
	Profile Profile
	Dir     string // HACTL_DIR — carries .env
	Bin     string // hactl binary under test
}

// Read runs a command that must not modify anything. It carries no guard
// because it needs none, which is also why the live profile can run the whole
// read corpus against a real house without ceremony.
func (t Target) Read(tb testing.TB, args ...string) (string, error) {
	tb.Helper()
	return t.exec(tb, args)
}

// MustRead fails the test if the command errors, and returns its output.
func (t Target) MustRead(tb testing.TB, args ...string) string {
	tb.Helper()
	out, err := t.Read(tb, args...)
	if err != nil {
		tb.Fatalf("%v: %v\n%s", args, err, out)
	}
	return out
}

// ReadDiagnostic runs a read command and returns what the process wrote to
// STDERR, where every error message goes.
//
// Read returns stdout alone, which is what a case asserting on an ANSWER
// wants. A case asserting on a MESSAGE needs the other stream, and using Read
// for one would be worse than not writing the case: the assertion would run
// against an empty string and pass. Findings #23 and #24 are both about the
// wording of a failure, so they read here.
//
// The manual is delivered to stderr on a session's first command and is
// therefore mixed in. That is harmless for both directions of assertion — a
// message that must not appear does not appear in the manual either — and it
// is the price of reading the stream errors actually use.
func (t Target) ReadDiagnostic(tb testing.TB, args ...string) (stderr string, err error) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	full := append([]string{"--dir", t.Dir}, args...)
	cmd := exec.CommandContext(ctx, t.Bin, full...) //nolint:gosec // G204: the binary is built by this tier and the args are test-owned
	var errBuf strings.Builder
	cmd.Stdout = &strings.Builder{}
	cmd.Stderr = &errBuf
	runErr := cmd.Run()
	return errBuf.String(), runErr
}

// Write runs a mutating command.
//
// targets are the objects the case intends to change; vocab is every other
// word the command legitimately contains (subcommand names, domains, service
// names). On the live profile both are checked by guardLiveWrite before the
// process starts — an argument the case did not account for refuses the run
// even when it is harmless, because the harness cannot tell that it is.
//
// On the rig the guard is skipped deliberately: confining rig writes to pg_*
// would mean the two profiles exercise different code, and the rig is where
// the destructive cases belong.
func (t Target) Write(tb testing.TB, targets, vocab, args []string) (string, error) {
	tb.Helper()
	if t.Profile == Live {
		if err := guardLiveWrite(args, targets, vocab); err != nil {
			tb.Fatalf("%v", err)
		}
	}
	return t.exec(tb, args)
}

func (t Target) exec(tb testing.TB, args []string) (string, error) {
	tb.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	full := append([]string{"--dir", t.Dir}, args...)
	cmd := exec.CommandContext(ctx, t.Bin, full...) //nolint:gosec // G204: the binary is built by this tier and the args are test-owned
	// The manual is delivered to stderr on a session's first command; keeping
	// the streams apart is what lets a case assert on stdout alone.
	var stdout strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &strings.Builder{}
	err := cmd.Run()
	return stdout.String(), err
}

// ExitCode extracts the process exit code from Read/Write's error, so a case
// can assert the code contract rather than merely "it failed".
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

// LiveTarget builds the real-instance target from HACTL_LIVEFIRE_DIR.
//
// It is opt-in by environment on purpose: the live profile must never be
// something a `go test ./...` stumbles into. Absent the variable the live
// cases skip, so the rig profile stays the default everywhere including CI.
func LiveTarget(tb testing.TB, bin string) (Target, bool) {
	tb.Helper()
	dir := os.Getenv("HACTL_LIVEFIRE_DIR")
	if dir == "" {
		return Target{}, false
	}
	//nolint:gosec // G703: the path comes from HACTL_LIVEFIRE_DIR, set by whoever runs the tier against their own instance
	if _, err := os.Stat(filepath.Join(dir, ".env")); err != nil {
		tb.Fatalf("HACTL_LIVEFIRE_DIR=%s carries no .env: %v", dir, err)
	}
	return Target{Profile: Live, Dir: dir, Bin: bin}, true
}

// BuildHactl compiles the binary under test into dir.
//
// It takes a directory rather than calling t.TempDir() because the binary has
// to outlive the first subtest that asks for it: a t.TempDir() is removed when
// that test ends, and every later case then fails with "no such file or
// directory" on a binary it never built. TestMain owns the directory.
func BuildHactl(dir string) (string, error) {
	root, err := repoRoot()
	if err != nil {
		return "", err
	}
	bin := filepath.Join(dir, "hactl")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/hactl") //nolint:gosec // G204: fixed go CLI, paths owned by this tier
	cmd.Dir = root
	if out, buildErr := cmd.CombinedOutput(); buildErr != nil {
		return "", fmt.Errorf("building hactl: %w\n%s", buildErr, out)
	}
	return bin, nil
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for range 6 {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir, nil
		}
		dir = filepath.Dir(dir)
	}
	return "", errors.New("go.mod not found above the working directory")
}

// Finding cites the entry in _archive/livefire-2026-07-30/findings_merged.json
// a case exists for, so the report stops being prose and a fixed defect can be
// traced back to the run that found it.
type Finding int

func (f Finding) String() string { return fmt.Sprintf("finding #%d", int(f)) }
