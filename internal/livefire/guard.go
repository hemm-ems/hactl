//go:build livefire

// Package livefire is the two-profile sweep: one corpus of assertions, run
// against the Docker rig and against a real Home Assistant.
//
// It exists because the rig was green while a real instance carried 90
// findings (live-fire 2026-07-30). The rig's whole fixture corpus is three
// YAML files per fixture, so the shapes those findings live in — a
// storage-backed helper, a domain written inline in configuration.yaml, a
// template block shared with production entities, a device carrying
// name_by_user — could not exist there. Tests over a corpus that cannot
// express the failure are green by construction.
//
// The split this package enforces: **the real instance is the oracle, the rig
// is the regression.** A case proves its claim against HA, and the same case
// runs on the rig so the claim keeps being true without anyone's real house
// being involved.
package livefire

import (
	"fmt"
	"regexp"
	"strings"
)

// pgPrefix is the playground namespace. Everything the live profile is allowed
// to write is named this way; the objects survive between runs deliberately
// (REPORT.md §Instance state), so a case reuses them rather than minting more.
var pgPrefix = regexp.MustCompile(`(^|\.)pg[_-]`)

// identifierish matches an argument that could name an object on the instance:
// an entity_id, a slug, a url_path. Flags and their values are excluded before
// this is consulted, so what is left is either a subcommand word or a target.
var identifierish = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]*$`)

// UnguardedError is returned when a live write names something outside pg_*.
type UnguardedError struct {
	Arg    string
	Reason string
}

func (e *UnguardedError) Error() string {
	return fmt.Sprintf("live write refused: %q %s — the live profile may only write pg_* objects", e.Arg, e.Reason)
}

// guardLiveWrite decides whether a command may run as a write against the real
// instance.
//
// The rule is deliberately not "does any argument look dangerous". It is the
// inverse, because an allowlist is the only form that fails safe on an
// argument nobody anticipated: every argument must be *accounted for* — a word
// of the command path, a flag, a flag's value, or one of the pg_* targets the
// case declared. An argument that is none of those refuses the run, even if it
// is harmless, because the harness cannot tell that it is.
//
// vocab is the set of non-target words the command legitimately contains:
// subcommand names ("auto", "create"), domains ("input_boolean"), and any
// literal a case needs. Keeping it explicit is what makes a stray entity_id
// stand out instead of blending in.
func guardLiveWrite(args, targets, vocab []string) error {
	for _, t := range targets {
		if !pgPrefix.MatchString(t) {
			return &UnguardedError{Arg: t, Reason: "is a declared target but is not in the pg_ namespace"}
		}
	}

	known := map[string]bool{}
	for _, t := range targets {
		known[t] = true
	}
	for _, v := range vocab {
		known[v] = true
	}

	scanNext := false
	for i, a := range args {
		if scanNext {
			scanNext = false
			// A flag's value is not an object name, so it is not required to be
			// a declared target — but it may not smuggle one either. `svc call
			// light.turn_off -d '{"entity_id":"light.wohnzimmer"}'` reaches a
			// real entity without ever passing it as an argument, which is the
			// one way past a guard that only reads positionals.
			if mentionsAnIdentifier(a) {
				return &UnguardedError{Arg: a, Reason: "is a flag value naming an object outside pg_*"}
			}
			continue
		}
		if strings.HasPrefix(a, "-") {
			consumes, err := inspectFlag(a, i, len(args))
			if err != nil {
				return err
			}
			scanNext = consumes
			continue
		}
		if err := inspectPositional(a, known); err != nil {
			return err
		}
	}
	return nil
}

// inspectFlag checks a flag written --name=value for a smuggled identifier and
// reports whether the flag consumes the argument after it.
func inspectFlag(flag string, i, n int) (consumes bool, err error) {
	if value, ok := strings.CutPrefix(flag, "--"); ok {
		if _, v, found := strings.Cut(value, "="); found && mentionsAnIdentifier(v) {
			return false, &UnguardedError{Arg: flag, Reason: "is a flag value naming an object outside pg_*"}
		}
	}
	return !strings.Contains(flag, "=") && !isBoolFlag(flag) && i+1 < n, nil
}

// inspectPositional requires every non-flag argument to be accounted for.
func inspectPositional(a string, known map[string]bool) error {
	if a == "" {
		// HA accepts a blank name, mints a blank id for it, and the area family
		// then fails on every read until the record is deleted over raw
		// WebSocket (P1 #1). Never on a real instance.
		return &UnguardedError{Arg: a, Reason: "is blank, which HA accepts and which can brick a registry family"}
	}
	if known[a] || pgPrefix.MatchString(a) {
		return nil
	}
	if !identifierish.MatchString(a) {
		// Payloads (JSON bodies, template strings) are not object names. They
		// still may not smuggle one in.
		if mentionsAnIdentifier(a) {
			return &UnguardedError{Arg: a, Reason: "is a payload naming an object outside pg_*"}
		}
		return nil
	}
	return &UnguardedError{Arg: a, Reason: "is neither a declared pg_* target nor part of the command's vocabulary"}
}

// isBoolFlag names the valueless flags the sweep passes, so the argument after
// one is not mistaken for its value and skipped past the guard.
func isBoolFlag(flag string) bool {
	switch strings.TrimLeft(flag, "-") {
	case "confirm", "json", "full", "raw", "yaml", "stats", "tokens", "unique", "failing", "errors", "warnings", "restored", "exit-code", "allow-partial":
		return true
	}
	return false
}

// mentionsAnIdentifier reports whether a free-form payload contains something
// shaped like an entity_id. A `-d '{"entity_id":"light.kitchen"}'` is the one
// way a write reaches a real object without ever passing it as an argument.
var entityIDInPayload = regexp.MustCompile(`\b[a-z_]+\.[a-z0-9_]+\b`)

func mentionsAnIdentifier(payload string) bool {
	for _, m := range entityIDInPayload.FindAllString(payload, -1) {
		if pgPrefix.MatchString(m) || looksLikeAPath(m) {
			continue
		}
		return true
	}
	return false
}

// fileExtensions are the suffixes that make `new.yaml` a path rather than the
// entity `new.yaml` — the two are indistinguishable by shape alone, and a
// guard that cannot tell them apart either refuses every `--file` or lets a
// real entity_id through in a payload.
var fileExtensions = map[string]bool{
	"yaml": true, "yml": true, "json": true, "txt": true, "md": true,
	"conf": true, "cfg": true, "ini": true, "log": true, "bak": true,
	"py": true, "go": true, "sh": true, "env": true, "db": true, "sqlite": true,
}

func looksLikeAPath(token string) bool {
	if strings.ContainsAny(token, "/\\") {
		return true
	}
	_, ext, found := strings.Cut(token, ".")
	return found && fileExtensions[ext]
}
