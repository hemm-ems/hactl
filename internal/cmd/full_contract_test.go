package cmd

import (
	"bytes"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Finding #21 — `--full` means "show full/raw output" in every command's help
// screen, and meant it in none of them consistently.
//
// It reached exactly one cap: format.Table's `--top` row limit. The 500-token
// prose cap behind it was untouched, which made the flag range from useless to
// actively harmful depending on which side of the table a command sat on.
// Measured against the real instance, 2026-07-31:
//
//	hactl config entries          ->  10 rows, "…+203 more"
//	hactl config entries --full   ->   7 rows, the last one cut mid-field,
//	                                   "…output capped at 500 tok"
//
// Asking for the full listing returned three rows FEWER than not asking. On
// the other side, `config show <entry> --full` on an entry big enough to
// truncate was byte-identical to `config show <entry>` — the flag had no rows
// to uncap and said nothing about having done nothing.
//
// The gate is a sweep over the live cobra tree, not three per-command tests,
// because "this flag means something different in each of sixty commands" is
// the defect. A command added tomorrow is covered the day it appears.
// ---------------------------------------------------------------------------

// TestFullNeverTruncates asserts the property `--full` has always claimed:
// with it, the answer is not cut short.
//
// It reuses TestJSONContract's fixture, classification and positional table —
// the same machinery, one clause further on — so the two sweeps cannot come to
// cover different sets of commands.
func TestFullNeverTruncates(t *testing.T) {
	rootCmd.InitDefaultHelpCmd()
	rootCmd.InitDefaultCompletionCmd()

	fixture := buildContractFixture(t)
	posArgs := contractPosArgs(fixture)

	leaves := leafCommands(rootCmd)
	sort.Slice(leaves, func(i, j int) bool { return leaves[i].CommandPath() < leaves[j].CommandPath() })

	var swept, withTeeth []string

	for _, leaf := range leaves {
		args := cmdArgsOf(leaf)
		path := strings.Join(args, " ")

		switch classifyCommand(leaf, path) {
		case catMutating, catMeta, catVerbatim, catActionOnly:
			continue
		}
		extra, ok := posArgs[path]
		if !ok {
			// TestJSONContract already fails on an unregistered command; not
			// failing twice for one gap keeps the message that names it single.
			continue
		}

		swept = append(swept, path)
		t.Run(strings.ReplaceAll(path, " ", "_"), func(t *testing.T) {
			run := func(extraFlags ...string) string {
				t.Helper()
				full := append([]string{"hactl"}, args...)
				full = append(full, extra...)
				full = append(full, "--dir", fixture.dir)
				full = append(full, extraFlags...)
				var buf bytes.Buffer
				if err := RunWithOutput(full, &buf); err != nil {
					t.Fatalf("command failed: %v\nargs: %v\noutput: %s", err, full[1:], buf.String())
				}
				return buf.String()
			}

			out := run("--full")
			if strings.Contains(out, truncationNotice) {
				t.Errorf("--full output is truncated — the flag that means \"show full output\" "+
					"delivered a capped one:\n%s", out)
			}
			// Whether this command could have failed at all: only an answer
			// longer than the default cap would have been cut by it. Recorded
			// rather than assumed, because a sweep whose teeth are invisible
			// reads as coverage it does not have.
			if estimateTokens(int64(len(out))) > 500 {
				withTeeth = append(withTeeth, path)
			}
		})
	}

	sort.Strings(swept)
	sort.Strings(withTeeth)
	t.Logf("--full sweep: asserted on %d read command(s): %s", len(swept), strings.Join(swept, ", "))
	t.Logf("--full sweep: %d of them answer more than the 500-token default cap against this fixture, so "+
		"those are the ones whose --full run could have failed: %s", len(withTeeth), strings.Join(withTeeth, ", "))
	if len(withTeeth) == 0 {
		t.Error("no swept command's --full answer exceeds the token cap against this fixture, so every " +
			"assertion above passed vacuously — the fixture has stopped being able to express a long answer")
	}
}

// truncationNotice is the fragment applyTokenPolicy appends when it cuts
// output short. Spelled once so a reworded notice fails the tests that depend
// on it rather than making them pass silently.
const truncationNotice = "output capped at"

// entryRow matches one data row of the fixture's `config entries` table.
var entryRow = regexp.MustCompile(`^entry\d+ `)

// TestFullLiftsTheTokenCap is finding #21's direct reproduction: a listing
// long enough to hit both caps, asked for three ways.
//
// It is separate from the sweep because the sweep proves a property over every
// command and this proves the arithmetic of one — including the count that
// went DOWN, which is the observation that made the finding more than a
// papercut.
func TestFullLiftsTheTokenCap(t *testing.T) {
	fixture := buildContractFixture(t)

	run := func(t *testing.T, extraFlags ...string) string {
		t.Helper()
		args := append([]string{"hactl", "config", "entries", "--dir", fixture.dir}, extraFlags...)
		var buf bytes.Buffer
		if err := RunWithOutput(args, &buf); err != nil {
			t.Fatalf("config entries %v failed: %v\n%s", extraFlags, err, buf.String())
		}
		return buf.String()
	}

	// Data rows only: the header cell is "entry_id", which starts the same way
	// every entry_id in this fixture does.
	countRows := func(out string) int {
		n := 0
		for line := range strings.SplitSeq(out, "\n") {
			if entryRow.MatchString(line) {
				n++
			}
		}
		return n
	}

	base := run(t)
	full := run(t, "--full")

	// The fixture has to be able to fail this, or the case proves nothing.
	if !strings.Contains(base, truncationNotice) && !strings.Contains(base, "more") {
		t.Fatalf("the fixture's config entries listing is short enough to fit both caps, so this "+
			"case cannot distinguish a working --full from a broken one:\n%s", base)
	}

	if strings.Contains(full, truncationNotice) {
		t.Errorf("--full was truncated by the token cap:\n%s", full)
	}
	if got, want := countRows(full), contractConfigEntries; got != want {
		t.Errorf("--full showed %d of %d entries; the flag exists to show all of them\n%s", got, want, full)
	}
	// The shape as reported: --full returned FEWER rows than no flag at all.
	if countRows(full) < countRows(base) {
		t.Errorf("--full returned %d rows where the default returned %d — asking for more gave less",
			countRows(full), countRows(base))
	}

	t.Run("an explicit --tokensmax still wins", func(t *testing.T) {
		out := run(t, "--full", "--tokensmax", strconv.Itoa(60))
		if !strings.Contains(out, truncationNotice) {
			t.Errorf("--full discarded the cap the caller typed:\n%s", out)
		}
		if !strings.Contains(out, "capped at 60 tok") {
			t.Errorf("the notice does not name the caller's number:\n%s", out)
		}
	})

	t.Run("--full is not sticky across in-process runs", func(t *testing.T) {
		// RunWithOutput resets flag VALUES; pflag's Changed lives on the flag
		// object and used to outlive the invocation, which would make one
		// `--tokensmax` look typed to every later run in the same process —
		// the MCP server runs every tool call that way.
		_ = run(t, "--full", "--tokensmax", "1")
		if out := run(t, "--full"); strings.Contains(out, truncationNotice) {
			t.Errorf("a previous run's --tokensmax leaked into this one:\n%s", out)
		}
	})
}
