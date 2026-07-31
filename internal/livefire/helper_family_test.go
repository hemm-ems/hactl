//go:build livefire

package livefire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// WP6 — the helper family remnants, plus the two classes reproducing them
// turned out to belong to: what an empty listing may claim, and what a glob is
// anchored to.
//
// Findings #28 #29 #63 #64. Read-only on both profiles: the two `helper create`
// cases are dry runs, which reach the companion but write nothing.

// TestSweepEmptyListingNamesItsFilter — finding #28.
//
// `helper ls --pattern zzz` answered "no helpers" on an instance holding 220,
// and `device ls` the same on one holding 307. The case runs over the listings
// that answered wrongly AND the ones that answered mutely, because the fix made
// them one answer: a listing that narrowed says what narrowed it, and how many
// records it searched (D-29).
//
// The count is asserted as a number the profile itself produces rather than as
// a shape, so a message that carried a plausible zero would fail here.
func TestSweepEmptyListingNamesItsFilter(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		for _, c := range []struct {
			args    []string
			subject string
			needsCC bool
		}{
			{args: []string{"ent", "ls"}, subject: "entities"},
			{args: []string{"auto", "ls"}, subject: "automations"},
			{args: []string{"script", "ls"}, subject: "scripts"},
			{args: []string{"device", "ls"}, subject: "devices"},
			{args: []string{"helper", "ls"}, subject: "helpers", needsCC: true},
		} {
			t.Run(strings.Join(c.args, "_"), func(t *testing.T) {
				if c.needsCC {
					requireCompanion(t, tgt)
				}
				// How many the instance really holds, read the way a caller
				// would: the unfiltered listing's own machine contract.
				total := countJSONRows(t, tgt, append(c.args, "--json", "--full"))
				if total == 0 {
					t.Skipf("this profile holds no %s, so a filter miss cannot be told from an empty inventory", c.subject)
				}

				out := tgt.MustRead(t, append(c.args, "--pattern", missNeedle)...)
				if !strings.Contains(out, "--pattern") || !strings.Contains(out, missNeedle) {
					t.Errorf("`%s --pattern %s` does not name the filter that emptied it:\n%s",
						strings.Join(c.args, " "), missNeedle, truncate(out))
				}
				if !strings.Contains(out, strconv.Itoa(total)+" "+c.subject) {
					t.Errorf("`%s --pattern %s` did not report the %d %s it searched:\n%s",
						strings.Join(c.args, " "), missNeedle, total, c.subject, truncate(out))
				}
				// The machine contract does not change with the reason.
				if empty := tgt.MustRead(t, append(c.args, "--pattern", missNeedle, "--json")...); strings.TrimSpace(empty) != "[]" {
					t.Errorf("`%s --pattern %s --json` = %q, want []", strings.Join(c.args, " "), missNeedle, empty)
				}
			})
		}
	})
}

// missNeedle matches no identifier on either profile.
const missNeedle = "zzz_no_such_thing_xyz"

// TestSweepGlobMatchesTheNameNotOnlyTheQualifiedID — finding #29, D-28.
//
// The property is stated against the instance rather than against a fixture:
// take an id the listing actually printed, split off its domain, and require
// the glob anchored at the name to find it. On the reference instance that is
// `input_boolean.anwesenheit_flur` and `anwesen*`; on the rig it is whatever
// `ent ls` prints first. A profile whose ids carry no domain cannot express the
// defect and says so.
func TestSweepGlobMatchesTheNameNotOnlyTheQualifiedID(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		var rows []struct {
			EntityID string `json:"entity_id"`
		}
		out := tgt.MustRead(t, "ent", "ls", "--json", "--top", "1")
		if err := json.Unmarshal([]byte(out), &rows); err != nil || len(rows) == 0 {
			t.Fatalf("`ent ls --json --top 1` gave no entity to anchor against: %v\n%s", err, truncate(out))
		}
		_, unqualified, found := strings.Cut(rows[0].EntityID, ".")
		if !found || len(unqualified) < 4 {
			t.Skipf("%q has no domain prefix to anchor past", rows[0].EntityID)
		}
		glob := unqualified[:4] + "*"

		matched := countJSONRows(t, tgt, []string{"ent", "ls", "--pattern", glob, "--json", "--full"})
		if matched == 0 {
			t.Errorf("`ent ls --pattern %s` matched nothing, although %q is on this instance — "+
				"the glob is anchored at the domain rather than at the name (D-28)", glob, rows[0].EntityID)
		}
		// The control: a glob that names the domain still selects by domain,
		// so the fix widened the anchor without dissolving it.
		domain, _, _ := strings.Cut(rows[0].EntityID, ".")
		if n := countJSONRows(t, tgt, []string{"ent", "ls", "--pattern", domain + ".*", "--json", "--full"}); n == 0 {
			t.Errorf("`ent ls --pattern %s.*` matched nothing — the domain anchor is gone", domain)
		}
	})
}

// TestSweepFilterCaseNeverDecidesTheAnswer — finding #28's second half, D-2.
//
// `--domain` compared with `==` while `--pattern` and `--name` beside it folded
// case, so `ent ls --domain SENSOR` answered for 2 551 sensors on the reference
// instance — through a message telling the caller to verify the domain exists.
func TestSweepFilterCaseNeverDecidesTheAnswer(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		lower := countJSONRows(t, tgt, []string{"ent", "ls", "--domain", "sensor", "--json", "--full"})
		if lower == 0 {
			t.Skip("this profile has no sensor entities")
		}
		for _, spelling := range []string{"SENSOR", "Sensor"} {
			if got := countJSONRows(t, tgt, []string{"ent", "ls", "--domain", spelling, "--json", "--full"}); got != lower {
				t.Errorf("`ent ls --domain %s` matched %d and `--domain sensor` matched %d — "+
					"case decided the answer (D-2)", spelling, got, lower)
			}
		}
	})
}

// TestSweepHelperCreateRefusesWhatConfirmRefuses — findings #63 and #64, D-30.
//
// Both are dry runs and neither writes: the point is that the preview ends the
// command rather than planning a create the confirmed run cannot perform.
//
// #63 as REPORTED (a typo domain) is closed by WP4's wiring probe; what is
// asserted here is the half the probe cannot answer — `script` is a domain the
// companion has a create route for and `helper create` may not write. #64's id
// check has to come first to be observable at all: on an instance whose helper
// domains are configured inline, every wired-ness probe fails before an id
// would ever be reached, which is why the original report could only call it
// speculative.
func TestSweepHelperCreateRefusesWhatConfirmRefuses(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		requireCompanion(t, tgt)

		dir := t.TempDir()
		valid := filepath.Join(dir, "valid.yaml")
		if err := os.WriteFile(valid, []byte("pg_w6_sweep_flag:\n  name: PG W6 Sweep\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		badID := filepath.Join(dir, "badid.yaml")
		if err := os.WriteFile(badID, []byte("\"pg w6 sweep space\":\n  name: PG W6 Sweep\n"), 0o600); err != nil {
			t.Fatal(err)
		}

		for _, c := range []struct {
			name, domain, file, wants string
		}{
			{name: "a domain no create route writes", domain: "pg_w6_not_a_domain", file: valid, wants: "invalid helper domain"},
			{name: "a domain another route writes", domain: "script", file: valid, wants: "invalid helper domain"},
			{name: "a storage-only helper domain", domain: "input_button", file: valid, wants: "invalid helper domain"},
			{name: "an id HA cannot use", domain: "input_boolean", file: badID, wants: "pg w6 sweep space"},
		} {
			t.Run(c.name, func(t *testing.T) {
				out, err := tgt.Read(t, "helper", "create", c.domain, "-f", c.file)
				if err == nil {
					t.Fatalf("`helper create %s` planned a create the confirmed run refuses:\n%s", c.domain, out)
				}
				if code := ExitCode(err); code != 1 {
					t.Errorf("exit code %d, want 1", code)
				}
				if strings.Contains(out, "would create") {
					t.Errorf("a plan reached stdout for a create that cannot happen:\n%s", out)
				}
				stderr, _ := tgt.ReadDiagnostic(t, "helper", "create", c.domain, "-f", c.file)
				if !strings.Contains(strings.ToLower(stderr), c.wants) {
					t.Errorf("the refusal does not name its reason (%q):\n%s", c.wants, truncate(stderr))
				}
			})
		}

		// The control: the same command with a usable domain and a usable id
		// gets as far as the wiring probe, so the two new checks refuse
		// something rather than everything. What the probe then answers is the
		// instance's business — WP4's case asserts that half.
		stderr, err := tgt.ReadDiagnostic(t, "helper", "create", "input_boolean", "-f", valid)
		if err != nil && strings.Contains(strings.ToLower(stderr), "invalid helper domain") {
			t.Errorf("a domain `helper create` writes was refused as invalid:\n%s", truncate(stderr))
		}
		if err != nil && strings.Contains(stderr, "pg_w6_sweep_flag\" is not usable") {
			t.Errorf("a valid entity object id was refused:\n%s", truncate(stderr))
		}
	})
}

// countJSONRows runs a listing under --json and returns how many rows it
// answered with. --full is the caller's job to pass: --top caps text tables
// only, but a case that means "everything" says so.
func countJSONRows(t *testing.T, tgt Target, args []string) int {
	t.Helper()
	out := tgt.MustRead(t, args...)
	var rows []json.RawMessage
	if err := json.Unmarshal([]byte(out), &rows); err != nil {
		t.Fatalf("`%s` is not a JSON array: %v\n%s", strings.Join(args, " "), err, truncate(out))
	}
	return len(rows)
}
