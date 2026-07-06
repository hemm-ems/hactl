package manual

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hemm-ems/hactl/docs"
)

// Mode selects what auto-delivery sends.
type Mode int

// Delivery modes, selected by HACTL_MANUAL_MODE.
const (
	ModeProgressive Mode = iota // core once, family how-tos on first use
	ModeFull                    // whole manual once per session
	ModeOff                     // no delivery
)

// DefaultTTL bounds a session: state idle longer than this is discarded, so
// the next command re-primes the manual (sliding window — every non-exempt
// invocation refreshes last_activity).
const DefaultTTL = 30 * time.Minute

const stateFileName = "manual-state.json"

// ModeFromEnv reads HACTL_MANUAL_MODE. Unset or unknown values mean
// progressive, matching the tools.py default.
func ModeFromEnv() Mode {
	switch os.Getenv("HACTL_MANUAL_MODE") {
	case "off":
		return ModeOff
	case "full":
		return ModeFull
	}
	return ModeProgressive
}

// SessionKey returns HACTL_SESSION, or "default" for the TTL-bounded shared
// session.
func SessionKey() string {
	if s := os.Getenv("HACTL_SESSION"); s != "" {
		return s
	}
	return "default"
}

type sessionState struct {
	LastActivity time.Time `json:"last_activity"`
	Core         bool      `json:"core,omitempty"`
	Full         bool      `json:"full,omitempty"`
	Delivered    []string  `json:"delivered,omitempty"`
}

func (s *sessionState) deliveredSet() map[string]bool {
	set := make(map[string]bool, len(s.Delivered))
	for _, h := range s.Delivered {
		set[h] = true
	}
	return set
}

type stateData struct {
	Version  int                      `json:"version"`
	Sessions map[string]*sessionState `json:"sessions"`
}

// Claim returns the manual text due with the current command ("" if nothing
// new), marks it delivered, refreshes the session's sliding TTL, prunes stale
// sessions, and persists. cacheDir=="" (instance dir unresolved) still
// delivers but skips persistence — fail-open: a duplicate injection costs a
// one-off ~1.4k tokens, a missed one defeats the feature.
func Claim(cacheDir, session string, mode Mode, family string, now time.Time) string {
	if mode == ModeOff {
		return ""
	}
	st, sess := loadSession(cacheDir, session, now)
	text := ""
	if !sess.Full {
		switch mode {
		case ModeFull:
			sess.Full = true
			text = FullNote + "\n\n" + docs.Manual
		case ModeProgressive:
			var blocks []string
			if !sess.Core {
				sess.Core = true
				blocks = append(blocks, CoreNote+"\n\n"+CoreText())
			}
			if family != "" {
				famText, headings := FamilyText(family, sess.deliveredSet())
				if famText != "" {
					sess.Delivered = append(sess.Delivered, headings...)
					blocks = append(blocks, FamilyNote(family)+"\n\n"+famText)
				}
			}
			text = strings.Join(blocks, "\n\n")
		}
	}
	saveState(cacheDir, st)
	return text
}

// MarkDelivered records delivery without emitting anything (rtfm printed the
// content itself). Scopes: "all" (full manual), "core", or a family name.
func MarkDelivered(cacheDir, session string, now time.Time, scopes ...string) {
	st, sess := loadSession(cacheDir, session, now)
	for _, scope := range scopes {
		switch scope {
		case "all":
			sess.Full = true
			sess.Core = true
		case "core":
			sess.Core = true
		default:
			set := sess.deliveredSet()
			for _, h := range FamilySections[scope] {
				if !set[h] {
					sess.Delivered = append(sess.Delivered, h)
				}
			}
		}
	}
	saveState(cacheDir, st)
}

// loadSession loads the state file (unreadable/corrupt ⇒ empty state), drops
// sessions idle beyond DefaultTTL, and returns the current session with its
// activity refreshed.
func loadSession(cacheDir, session string, now time.Time) (*stateData, *sessionState) {
	st := &stateData{Version: 1, Sessions: map[string]*sessionState{}}
	if cacheDir != "" {
		if raw, err := os.ReadFile(filepath.Clean(filepath.Join(cacheDir, stateFileName))); err == nil {
			var loaded stateData
			if json.Unmarshal(raw, &loaded) == nil && loaded.Sessions != nil {
				st = &loaded
				st.Version = 1
			}
		}
	}
	for key, s := range st.Sessions {
		if now.Sub(s.LastActivity) > DefaultTTL {
			delete(st.Sessions, key)
		}
	}
	sess, ok := st.Sessions[session]
	if !ok {
		sess = &sessionState{}
		st.Sessions[session] = sess
	}
	sess.LastActivity = now
	return st, sess
}

// saveState persists best-effort via tmp-file + rename. No flock: concurrent
// hactl calls at LLM cadence are rare, and last-write-wins worst-cases as one
// duplicate injection, which is self-correcting.
func saveState(cacheDir string, st *stateData) {
	if cacheDir == "" {
		return
	}
	if err := os.MkdirAll(cacheDir, 0o750); err != nil {
		return
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return
	}
	tmp := filepath.Join(cacheDir, fmt.Sprintf("%s.tmp.%d", stateFileName, os.Getpid()))
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return
	}
	if err := os.Rename(tmp, filepath.Join(cacheDir, stateFileName)); err != nil {
		_ = os.Remove(tmp)
	}
}
