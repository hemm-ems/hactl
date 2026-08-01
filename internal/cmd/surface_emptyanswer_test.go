package cmd

import (
	"bytes"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// ---------------------------------------------------------------------------
// An empty listing states what narrowed it (live-fire #28).
//
// The surface is the live cobra tree: every runnable command carrying at least
// one narrowing flag (narrowsListing, listing.go). It needs no manifest because
// it needs no dispositions — every site on it is driven end to end against the
// shared contract fixture, so a listing command that grows a filter is covered
// the day it appears rather than the day somebody remembers to register it.
//
// That matters here more than usual. `helper ls`'s "no helpers" was a literal
// chosen once, and the four listings that were merely mute rather than wrong
// (`ent ls`, `auto ls`, `script ls`, `log`) had no shared code with it at all:
// the defect was in a place per command, which is exactly the shape a
// per-command test suite cannot see the size of.
// ---------------------------------------------------------------------------

// missNeedle matches nothing in the contract fixture, in any listing, under any
// filter. It is not a word so that no fixture record can start carrying it.
const missNeedle = "zzz_no_such_thing_xyz"

// narrowedListings returns every command in the live tree that can be narrowed,
// paired with the narrowing flags it declares.
func narrowedListings(root *cobra.Command) map[*cobra.Command][]*pflag.Flag {
	out := map[*cobra.Command][]*pflag.Flag{}
	for _, leaf := range leafCommands(root) {
		if flags := narrowingFlags(leaf); len(flags) > 0 {
			out[leaf] = flags
		}
	}
	return out
}

// TestEveryNarrowedListingSaysWhatNarrowedIt — for every narrowing flag in the
// tree, an answer emptied by that flag names the flag and the value the caller
// typed.
//
// The negative control is in the same case: the same command WITHOUT the filter
// must not mention it. Without that, a command that printed the flag name
// unconditionally would pass, and "no helpers match --pattern" on a listing
// nobody filtered is the same class of false statement as "no helpers" on an
// instance holding 220.
func TestEveryNarrowedListingSaysWhatNarrowedIt(t *testing.T) {
	fixture := buildContractFixture(t)

	listings := narrowedListings(rootCmd)
	if len(listings) == 0 {
		t.Fatal("no narrowable listing was found in the tree — the extractor has stopped matching " +
			"and this gate proves nothing")
	}

	var covered []string
	for leaf, flags := range listings {
		args := cmdArgsOf(leaf)
		path := strings.Join(args, " ")
		if isMutating(leaf) {
			t.Errorf("%s declares a narrowing flag and is a write command — this gate drives it, "+
				"so classify it before it runs against a real instance", leaf.CommandPath())
			continue
		}
		for _, f := range flags {
			covered = append(covered, path+" --"+f.Name)
			t.Run(strings.ReplaceAll(path+"_"+f.Name, " ", "_"), func(t *testing.T) {
				assertNamesItsNarrowing(t, fixture.dir, args, f, flags)
			})
		}
	}

	sort.Strings(covered)
	t.Logf("empty-answer sweep: %d narrowing flag(s) across %d listing(s): %s",
		len(covered), len(listings), strings.Join(covered, ", "))
}

// assertNamesItsNarrowing drives one command with one narrowing flag set so
// that nothing survives it, and holds the answer to the rule.
func assertNamesItsNarrowing(t *testing.T, dir string, cmdArgs []string, f *pflag.Flag, siblings []*pflag.Flag) {
	t.Helper()

	args, expect := narrowingMiss(t, cmdArgs, f, siblings)
	answer, err := runListing(t, dir, args)
	if err != nil {
		// A refusal is the other honest way to explain a narrowing that found
		// nothing, and for a selector it is the RIGHT one: `dash show --view x`
		// on a dashboard without that view exits 1 rather than reporting an
		// empty dashboard, which is what live-fire #8 decided. What it may not
		// do is fail without naming what the caller typed.
		answer = err.Error()
	}
	for _, want := range expect {
		if !strings.Contains(answer, want) {
			t.Errorf("`hactl %s` found nothing and did not name %q:\n%s\n"+
				"    A narrowing that yields nothing is explained — as an empty answer naming it, or\n"+
				"    as a refusal naming it. Otherwise a caller reads the answer as an inventory fact\n"+
				"    and stops (live-fire #28: \"no helpers\" on an instance holding 220).\n"+
				"    Route the empty branch through emptyListing (internal/cmd/listing.go).",
				strings.Join(args, " "), want, indent(answer))
		}
	}

	// The negative control: no narrowing, no claim about one. Without it a
	// command that printed the flag name unconditionally would pass, and
	// "no helpers match --pattern" on a listing nobody filtered is the same
	// class of false statement as "no helpers" on an instance holding 220.
	unfiltered, unfilteredErr := runListing(t, dir, cmdArgs)
	if unfilteredErr == nil && strings.Contains(unfiltered, "--"+f.Name) {
		t.Errorf("`hactl %s` mentions --%s although nothing narrowed by it:\n%s",
			strings.Join(cmdArgs, " "), f.Name, indent(unfiltered))
	}
}

// narrowingMiss builds an invocation that must find nothing, and the strings
// its answer owes the caller.
//
// A string flag misses on its own — nothing in the fixture is called
// missNeedle. A switch cannot: it narrows to a class the fixture may well
// contain (the log fixture holds two ERROR records, so `log --errors` is not
// empty), so it is paired with a sibling string narrowing that misses, and the
// answer then owes BOTH — which is also the only place the multi-narrowing
// message is exercised.
func narrowingMiss(t *testing.T, cmdArgs []string, f *pflag.Flag, siblings []*pflag.Flag) (args, expect []string) {
	t.Helper()
	args = append(args, cmdArgs...)
	if f.Value.Type() != "bool" {
		return append(args, "--"+f.Name+"="+missNeedle), []string{"--" + f.Name, missNeedle}
	}
	for _, s := range siblings {
		if s.Value.Type() == "string" {
			return append(args, "--"+f.Name+"=true", "--"+s.Name+"="+missNeedle),
				[]string{"--" + f.Name, "--" + s.Name, missNeedle}
		}
	}
	t.Fatalf("`hactl %s --%s` is a switch on a listing with no value narrowing beside it, so this "+
		"gate cannot force it to find nothing — give the command a string narrowing or teach this "+
		"case how to empty it, but do not let the site go unasserted",
		strings.Join(cmdArgs, " "), f.Name)
	return nil, nil
}

// runListing executes one command against the fixture and returns what it wrote
// plus the error it ended with. Both, because a command that refuses explains
// itself through the error and a command that answers explains itself through
// the output, and this gate accepts either.
func runListing(t *testing.T, dir string, args []string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	full := append([]string{"hactl", "--dir", dir}, args...)
	err := RunWithOutput(full, &buf)
	return buf.String(), err
}

// mustRunListing is runListing for the cases that assert on a successful
// answer's wording.
func mustRunListing(t *testing.T, dir string, args []string) string {
	t.Helper()
	out, err := runListing(t, dir, args)
	if err != nil {
		t.Fatalf("`hactl %s` failed against the contract fixture: %v\n%s",
			strings.Join(args, " "), err, indent(out))
	}
	return out
}

func indent(s string) string {
	if s == "" {
		return "        (no output)"
	}
	return "        " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n        ")
}

// TestEmptyListingCountsTheInventoryItSearched pins the half of the message a
// caller cannot get any other way: how much the filter was applied to.
//
// "no helpers match --pattern zzz" is already honest. It still leaves the
// reader to run the unfiltered listing to learn whether the instance holds 220
// helpers or none, which is the question the original "no helpers" answered
// wrongly and the reason this defect was a P2 rather than a wording nit.
func TestEmptyListingCountsTheInventoryItSearched(t *testing.T) {
	fixture := buildContractFixture(t)

	out := mustRunListing(t, fixture.dir, []string{"helper", "ls", "--pattern", missNeedle})
	for _, want := range []string{"no helpers match", "--pattern", missNeedle, "on this instance"} {
		if !strings.Contains(out, want) {
			t.Errorf("`helper ls --pattern %s` is missing %q:\n%s", missNeedle, want, indent(out))
		}
	}
	// The fixture's companion manages two helpers, so the count is a fact this
	// test can name rather than a shape it can only match.
	if !strings.Contains(out, "2 helpers") {
		t.Errorf("`helper ls --pattern %s` did not report how many helpers it searched:\n%s",
			missNeedle, indent(out))
	}

	// An instance that genuinely holds none says so, with no filter arithmetic
	// attached: naming a filter that removed nothing would be its own falsehood.
	empty := mustRunListing(t, emptyHelperFixture(t), []string{"helper", "ls", "--pattern", missNeedle})
	if strings.TrimSpace(empty) != "no helpers" {
		t.Errorf("an instance with no helpers at all must answer plainly, got:\n%s", indent(empty))
	}
}

// emptyHelperFixture is an instance that genuinely holds no helpers: the
// companion manages none and HA's states carry none either. It is the control
// for the count — without it every message here would read the same whether
// the number were a measurement or a decoration.
func emptyHelperFixture(t *testing.T) string {
	t.Helper()
	return helperEnv(t, helperCompanionHandler(`{"helpers":[]}`), helperStatesHandler(`[
		{"entity_id":"sensor.not_a_helper","state":"1","attributes":{}}
	]`))
}

// TestEmptyListingUnderJSONIsStillAnArray — H-10 does not bend for the empty
// case. The reason a listing is empty is prose for a reader; the machine
// contract is that the document parses as the same shape whatever the answer.
func TestEmptyListingUnderJSONIsStillAnArray(t *testing.T) {
	fixture := buildContractFixture(t)
	for _, args := range [][]string{
		{"helper", "ls", "--pattern", missNeedle},
		{"ent", "ls", "--pattern", missNeedle},
		{"device", "ls", "--pattern", missNeedle},
		{"auto", "ls", "--pattern", missNeedle},
		{"script", "ls", "--pattern", missNeedle},
		{"config", "entries", "--domain", missNeedle},
		{"log", "--component", missNeedle},
	} {
		out := mustRunListing(t, fixture.dir, append(args, "--json"))
		if strings.TrimSpace(out) != "[]" {
			t.Errorf("`hactl %s --json` on an empty answer is %q, want []", strings.Join(args, " "), out)
		}
	}
}
