//go:build livefire

package livefire

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
)

// Integrity is a census of everything the sweep must leave exactly as it found
// it. Taken before the run and again after; a difference outside the pg_
// namespace fails the run.
//
// This is the half of verification that a per-case assertion cannot do. Every
// case can pass while the run as a whole has reformatted automations.yaml or
// left an empty-id area behind — both happened on 2026-07-30, and neither
// showed up in any individual command's output. Collateral is a property of
// the instance, so it is measured on the instance.
type Integrity struct {
	ConfigValid bool
	HealthState string
	Areas       []string
	Floors      []string
	Labels      []string
	Entities    []string
	Automations []string
	Scripts     []string
}

// TakeIntegrity reads the census through hactl itself, which is deliberate: a
// sweep that verified with a second tool would prove nothing about the one
// under test, and any read broken enough to hide damage is itself a finding.
func TakeIntegrity(tb testing.TB, t Target) Integrity {
	tb.Helper()
	return Integrity{
		ConfigValid: strings.Contains(t.MustRead(tb, "health"), "RUNNING"),
		HealthState: firstLine(t.MustRead(tb, "health")),
		Areas:       namesFromJSON(tb, t, "name", "area", "ls"),
		Floors:      namesFromJSON(tb, t, "name", "floor", "ls"),
		Labels:      namesFromJSON(tb, t, "name", "label", "ls"),
		Entities:    namesFromJSON(tb, t, "entity_id", "ent", "ls", "--tokensmax", "0"),
		Automations: namesFromJSON(tb, t, "id", "auto", "ls", "--tokensmax", "0"),
		Scripts:     namesFromJSON(tb, t, "id", "script", "ls", "--tokensmax", "0"),
	}
}

// AssertUnchanged fails when anything outside the pg_ namespace differs.
//
// pg_ objects are excluded from the comparison rather than pinned, because the
// playground is what the write cases are for — requiring it to be identical
// would make every write case fail its own run.
func AssertUnchanged(tb testing.TB, before, after Integrity) {
	tb.Helper()

	if before.ConfigValid && !after.ConfigValid {
		tb.Errorf("the instance was valid before the sweep and is not after: %s", after.HealthState)
	}

	for _, set := range []struct {
		what          string
		before, after []string
	}{
		{"areas", before.Areas, after.Areas},
		{"floors", before.Floors, after.Floors},
		{"labels", before.Labels, after.Labels},
		{"entities", before.Entities, after.Entities},
		{"automations", before.Automations, after.Automations},
		{"scripts", before.Scripts, after.Scripts},
	} {
		gone, added := diffOutsidePlayground(set.before, set.after)
		if len(gone) > 0 {
			tb.Errorf("the sweep REMOVED %s that are not playground objects: %v", set.what, gone)
		}
		if len(added) > 0 {
			tb.Errorf("the sweep CREATED %s outside the playground: %v", set.what, added)
		}
	}
}

// diffOutsidePlayground returns what vanished and what appeared, ignoring pg_*.
func diffOutsidePlayground(before, after []string) (gone, added []string) {
	inAfter := map[string]bool{}
	for _, a := range after {
		inAfter[a] = true
	}
	inBefore := map[string]bool{}
	for _, b := range before {
		inBefore[b] = true
	}
	for _, b := range before {
		if !pgPrefix.MatchString(b) && !inAfter[b] {
			gone = append(gone, b)
		}
	}
	for _, a := range after {
		if !pgPrefix.MatchString(a) && !inBefore[a] {
			added = append(added, a)
		}
	}
	sort.Strings(gone)
	sort.Strings(added)
	return gone, added
}

// namesFromJSON pulls one field out of a listing's --json form.
func namesFromJSON(tb testing.TB, t Target, field string, args ...string) []string {
	tb.Helper()
	out, err := t.Read(tb, append(args, "--json")...)
	if err != nil {
		tb.Fatalf("census %v failed: %v\n%s", args, err, out)
	}
	names, parseErr := parseCensusRows(field, out)
	if parseErr != nil {
		tb.Fatalf("census %v: %v\n%s", args, parseErr, out)
	}
	return names
}

// parseCensusRows extracts one field from a listing document.
//
// An empty result is never accepted. A census that reads zero rows — because
// the listing broke, or because --json returned prose — would make every later
// "nothing outside pg_* changed" comparison pass vacuously, so the sweep would
// report a clean instance precisely when it had lost the ability to look. That
// is the shape H-14 exists to refuse, and this is the rule at work rather than
// an exception to it.
func parseCensusRows(field string, doc string) ([]string, error) {
	var rows []map[string]any
	if err := json.Unmarshal([]byte(doc), &rows); err != nil {
		return nil, fmt.Errorf("did not return a JSON array: %w", err)
	}
	if len(rows) == 0 {
		return nil, errors.New("read zero rows — a census that reads nothing cannot detect damage")
	}
	names := make([]string, 0, len(rows))
	for _, r := range rows {
		if v, ok := r[field]; ok {
			names = append(names, fmt.Sprint(v))
		}
	}
	sort.Strings(names)
	return names, nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}
