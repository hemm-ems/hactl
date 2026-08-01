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
	Dashboards  []string
}

// TakeIntegrity reads the census through hactl itself, which is deliberate: a
// sweep that verified with a second tool would prove nothing about the one
// under test, and any read broken enough to hide damage is itself a finding.
func TakeIntegrity(tb testing.TB, t Target) Integrity {
	tb.Helper()
	census, err := Census(t)
	if err != nil {
		tb.Fatalf("%v", err)
	}
	return census
}

// Census is TakeIntegrity without a testing.TB, so TestMain can bracket the
// whole run with it.
//
// The split exists because the census had no caller at all. It was written for
// the run-level collateral check — the half a per-case assertion cannot do,
// since every case can pass while the RUN as a whole reformats automations.yaml
// or leaves an empty-id area behind, both of which happened on 2026-07-30 and
// neither of which showed up in any single command's output. But bracketing a
// run means taking the census before the first case and after the last, and the
// only thing that spans that is TestMain, which has no *testing.T. So the
// reading is a plain function and the TB wrapper sits on top for the cases that
// want one.
func Census(t Target) (Integrity, error) {
	health, err := t.exec(discard{}, []string{"health"})
	if err != nil {
		return Integrity{}, fmt.Errorf("census: reading health: %w\n%s", err, health)
	}
	census := Integrity{
		ConfigValid: strings.Contains(health, "RUNNING"),
		HealthState: firstLine(health),
	}
	for _, read := range []struct {
		into  *[]string
		field string
		args  []string
	}{
		{&census.Areas, "name", []string{"area", "ls"}},
		{&census.Floors, "name", []string{"floor", "ls"}},
		{&census.Labels, "name", []string{"label", "ls"}},
		{&census.Entities, "entity_id", []string{"ent", "ls", "--tokensmax", "0"}},
		{&census.Automations, "id", []string{"auto", "ls", "--tokensmax", "0"}},
		{&census.Scripts, "id", []string{"script", "ls", "--tokensmax", "0"}},
		// Dashboards are in the census because the sweep WRITES them —
		// `dash create`/`dash delete` run on both profiles — and a leaked
		// dashboard is collateral by exactly the same argument as a leaked
		// label. Leaving them out would have made the bracket blind to the one
		// registry whose cleanup discarded its own result.
		{&census.Dashboards, "url_path", []string{"dash", "ls", "--tokensmax", "0"}},
	} {
		args := append(append([]string{}, read.args...), "--json")
		out, readErr := t.exec(discard{}, args)
		if readErr != nil {
			return Integrity{}, fmt.Errorf("census %v: %w\n%s", read.args, readErr, out)
		}
		rows, parseErr := parseCensusRows(read.field, out)
		if parseErr != nil {
			return Integrity{}, fmt.Errorf("census %v: %w\n%s", read.args, parseErr, out)
		}
		*read.into = rows
	}
	return census, nil
}

// CompareIntegrity is AssertUnchanged without a testing.TB: it returns what
// moved outside the pg_ namespace, one line per problem, empty when the
// instance is as it was found.
func CompareIntegrity(before, after Integrity) []string {
	var problems []string
	if before.ConfigValid && !after.ConfigValid {
		problems = append(problems,
			"the instance was valid before the sweep and is not after: "+after.HealthState)
	}
	for _, set := range integritySets(before, after) {
		gone, added := diffOutsidePlayground(set.before, set.after)
		if len(gone) > 0 {
			problems = append(problems,
				fmt.Sprintf("the sweep REMOVED %s that are not playground objects: %v", set.what, gone))
		}
		if len(added) > 0 {
			problems = append(problems,
				fmt.Sprintf("the sweep CREATED %s outside the playground: %v", set.what, added))
		}
	}
	return problems
}

type integritySet struct {
	what          string
	before, after []string
}

func integritySets(before, after Integrity) []integritySet {
	return []integritySet{
		{"areas", before.Areas, after.Areas},
		{"floors", before.Floors, after.Floors},
		{"labels", before.Labels, after.Labels},
		{"entities", before.Entities, after.Entities},
		{"automations", before.Automations, after.Automations},
		{"scripts", before.Scripts, after.Scripts},
		{"dashboards", before.Dashboards, after.Dashboards},
	}
}

// discard is the testing.TB a census read needs and does not use. Target.exec
// only ever calls Helper() on it.
type discard struct{ testing.TB }

func (discard) Helper() {}

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

	for _, set := range integritySets(before, after) {
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
