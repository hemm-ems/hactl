package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A witness is hactl's record that a caller has SEEN what a write would do —
// that the dry-run form of this exact command ran, against this exact target,
// recently, and succeeded.
//
// It replaces what confirmGuard used to ask, which was whether the family's
// how-to had reached the session. That question was answered out of
// `<instance>/cache/manual-state.json` under the session key `default`
// whenever HACTL_SESSION was unset — a key every process on the machine
// shares. So the guard could be switched off for one caller by another
// caller's unrelated command: measured on the reference instance, a process
// running `area ls` under HACTL_MANUAL_MODE=full set `full: true`, and a
// SECOND process, in another directory, in progressive mode, whose first-ever
// automation command it was, then wrote to the live instance with no refusal
// and nothing on stderr (finding #61).
//
// The delivery record was never evidence about the caller. It is evidence
// about the directory, and a directory is shared. A dry-run of the same target
// is evidence about the write: it is the thing the manual actually instructs
// ("run the dry-run form, present the plan to the user, and repeat with
// --confirm"), and it is what the guard's own doc comment always claimed to be
// checking. It is also strictly stronger — `auto ls` used to satisfy the guard
// for `auto apply --confirm`, so a caller could confirm a write it had never
// previewed.
//
// The record is keyed by SESSION as well as by target, so a caller that
// identifies itself with HACTL_SESSION has a private one: another caller's
// preview of the same object does not authorize its write. That is the
// supported, attributable configuration, and it is the one the manual asks for.
//
// What it does NOT close, said here rather than found later: callers that
// leave HACTL_SESSION unset all share the key `default`, so one of them
// previewing a target does authorize another's write of THAT target. The leak
// is scoped to a session and a target now instead of being instance-wide,
// which is the difference between "somebody was shown this exact plan" and
// "the guard is off". It was found by the sweep rather than reasoned about:
// two sibling cases previewing the same automation authorized a third case
// whose whole precondition was that nothing had.
const witnessFileName = "confirm-witness.json"

// witnessTTL is how long a preview authorizes its write.
//
// It matches the manual session's window because it bounds the same thing: how
// long ago a caller can have been shown something and still be acting on it.
// The plan a preview printed is a claim about the instance's state, and the
// instance is live — an hour-old diff is not a plan, it is a memory.
const witnessTTL = 30 * time.Minute

type witnessData struct {
	Version  int                  `json:"version"`
	Previews map[string]time.Time `json:"previews"`
}

// witnessKey names one previewable write by one caller: the session, the
// resolved command path, and the positional arguments that say what it acts on.
//
// All three matter. Without the command path, a dry-run of `auto diff X` would
// authorize `auto delete X`. Without the arguments, a dry-run of
// `auto apply pg_test` would authorize `auto apply` on anything — the
// instance-wide hole this file exists to close, rebuilt one level down.
// Without the session, one caller's preview authorizes another's write, which
// is #61 itself rebuilt two levels down.
func witnessKey(session, cmdPath string, args []string) string {
	return session + "|" + cmdPath + "|" + strings.Join(args, " ")
}

// recordWitness notes that the dry-run form of a write ran and succeeded, so
// the matching --confirm within witnessTTL is an informed one.
//
// Failure is silent and never blocks: this is a record kept for the NEXT
// invocation, and an unwritable cache directory must not turn a preview that
// worked into a command that failed. The cost of losing it is one refusal
// telling the caller to preview again.
func recordWitness(cacheDir, session, cmdPath string, args []string, now time.Time) {
	if cacheDir == "" {
		return
	}
	data := loadWitness(cacheDir, now)
	data.Previews[witnessKey(session, cmdPath, args)] = now
	saveWitness(cacheDir, data)
}

// hasWitness reports whether the dry-run form of this command and target ran
// within witnessTTL.
func hasWitness(cacheDir, session, cmdPath string, args []string, now time.Time) bool {
	if cacheDir == "" {
		return false
	}
	at, ok := loadWitness(cacheDir, now).Previews[witnessKey(session, cmdPath, args)]
	return ok && now.Sub(at) <= witnessTTL
}

// loadWitness reads the record, dropping previews older than witnessTTL. An
// unreadable or corrupt file reads as no previews at all — the safe direction,
// because the consequence is a refusal that names the dry-run to run, not a
// write nobody planned.
func loadWitness(cacheDir string, now time.Time) *witnessData {
	data := &witnessData{Version: 1, Previews: map[string]time.Time{}}
	raw, err := os.ReadFile(filepath.Clean(filepath.Join(cacheDir, witnessFileName)))
	if err != nil {
		return data
	}
	var loaded witnessData
	if json.Unmarshal(raw, &loaded) != nil || loaded.Previews == nil {
		return data
	}
	for key, at := range loaded.Previews {
		if now.Sub(at) <= witnessTTL {
			data.Previews[key] = at
		}
	}
	return data
}

// saveWitness persists best-effort via tmp-file + rename, the same shape
// manual state uses. Concurrent writers can lose a record here; the cost is a
// caller being asked to preview again, which is the direction this whole file
// is biased in.
func saveWitness(cacheDir string, data *witnessData) {
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return
	}
	encoded, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return
	}
	tmp := filepath.Join(cacheDir, fmt.Sprintf("%s.tmp.%d", witnessFileName, os.Getpid()))
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, filepath.Join(cacheDir, witnessFileName)); err != nil {
		_ = os.Remove(tmp)
	}
}
