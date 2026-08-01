//go:build livefire

package livefire

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
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

// TestSweepHelperSourceIsReadNotInvented — finding #104, found by WP13's own
// capture while reading the instance to build the fixture.
//
// `helper ls` builds its listing from two reads: the companion's per-domain
// YAML files, and everything else in a helper domain that appears in
// /api/states. The second read labelled its rows `source: storage`
// unconditionally, so the column did not report where a helper comes from — it
// reported which of hactl's two code paths had produced the row.
//
// Those differ exactly where a real config is not tidy. A domain written inline
// in configuration.yaml is in no `<domain>.yaml` the companion reads, so its
// helpers arrive through the second path and were announced as created in the
// Home Assistant UI. On the reference instance that is five helpers, and the
// consequence is not cosmetic: `helper set` and `helper delete` refuse them
// with a reason that is false, and `helper show` 404s with a message naming the
// files it searched — all of them, none of them the one the helper is in.
//
// Home Assistant states the answer on the wire. Every helper entity carries
// `editable`, true only for a storage collection, and hactl had it in the same
// payload it was reading the entity out of. So the assertion is against HA's
// attribute rather than against a list of ids: an instance that grows a sixth
// inline helper is covered without anyone remembering to add it.
func TestSweepHelperSourceIsReadNotInvented(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		// `helper ls` opens with connectCompanion and returns its error, so the
		// rig cannot run this case at all — rig capability debt R11 again, and
		// worth stating rather than leaving as a skip nobody reads: the
		// classification itself needs no companion (`editable` rides on the
		// state payload) and is proven on the rig one tier down, by
		// TestRunHelperLs_SourceFollowsEditableNotTheCodePath. What is
		// live-profile-only here is the end-to-end command, not the rule.
		requireCompanion(t, tgt)

		editable := helperEditability(t, tgt)
		if len(editable) == 0 {
			t.Skip("this profile serves no helper-domain entities, so a source cannot be wrong about one")
		}

		var rows []struct {
			ID     string `json:"id"`
			Source string `json:"source"`
		}
		out := tgt.MustRead(t, "helper", "ls", "--json", "--full", "--tokensmax", "0")
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("`helper ls --json` is not a JSON array: %v\n%s", err, truncate(out))
		}
		if len(rows) == 0 {
			t.Fatal("`helper ls --json` read zero rows — a listing that reads nothing cannot be wrong about one")
		}

		checked := 0
		for _, row := range rows {
			// A YAML row's id is a bare slug, so it names no entity and HA has
			// no editable for it. The invented value only ever reached the
			// entity-derived rows, which are the ones addressed by entity_id.
			isEditable, known := editable[row.ID]
			if !known {
				continue
			}
			checked++
			want := helperSourceYAML
			if isEditable {
				want = helperSourceStorage
			}
			if row.Source != want {
				t.Errorf("`helper ls` reports %s as source=%q, but Home Assistant reports editable=%v, so it is %s-defined",
					row.ID, row.Source, isEditable, want)
			}
		}
		if checked == 0 {
			t.Error("no `helper ls` row could be matched to a helper entity — the case proved nothing")
		}
	})
}

const (
	helperSourceStorage = "storage"
	helperSourceYAML    = "yaml"
)

// helperEditability asks Home Assistant itself which helper entities belong to
// a storage collection, keyed by entity_id.
//
// It reads /api/states directly rather than through hactl, and that is the
// point: the claim under test is hactl's, so a case that sourced its truth from
// hactl would agree with the defect. This is the tier's oracle rule (TC-1)
// applied to a case about a classification — the instance decides.
func helperEditability(t *testing.T, tgt Target) map[string]bool {
	t.Helper()

	baseURL, token := instanceCredentials(t, tgt)
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/states", nil)
	if err != nil {
		t.Fatalf("building the states request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/states: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // the body is drained below
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading /api/states: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /api/states: HTTP %d: %s", resp.StatusCode, truncate(string(body)))
	}

	var states []struct {
		EntityID   string         `json:"entity_id"`
		Attributes map[string]any `json:"attributes"`
	}
	if err := json.Unmarshal(body, &states); err != nil {
		t.Fatalf("parsing /api/states: %v\n%s", err, truncate(string(body)))
	}

	out := map[string]bool{}
	for _, s := range states {
		domain, _, found := strings.Cut(s.EntityID, ".")
		if !found || !helperDomain(domain) {
			continue
		}
		// Absent is not false. A helper domain whose entity carries no
		// `editable` at all cannot answer the question, and recording it as
		// "not editable" would be the invented value this case exists about,
		// re-introduced by the case itself.
		if v, ok := s.Attributes["editable"].(bool); ok {
			out[s.EntityID] = v
		}
	}
	return out
}

// helperDomain reports whether a domain is one Home Assistant serves helpers
// in. It mirrors hactl's own list rather than importing it, so a domain
// silently dropped from the product does not silently narrow this case too.
func helperDomain(domain string) bool {
	switch domain {
	case "input_boolean", "input_number", "input_text", "input_select",
		"input_datetime", "input_button", "counter", "timer", "schedule":
		return true
	}
	return false
}

// instanceCredentials reads the URL and token a profile's .env points at.
func instanceCredentials(t *testing.T, tgt Target) (baseURL, token string) {
	t.Helper()
	env, err := os.ReadFile(filepath.Join(tgt.Dir, ".env"))
	if err != nil {
		t.Fatalf("reading %s/.env: %v", tgt.Dir, err)
	}
	for line := range strings.SplitSeq(string(env), "\n") {
		key, value, found := strings.Cut(strings.TrimSpace(line), "=")
		if !found {
			continue
		}
		switch key {
		case "HA_URL":
			baseURL = strings.TrimRight(strings.Trim(value, `"'`), "/")
		case "HA_TOKEN":
			token = strings.Trim(value, `"'`)
		}
	}
	if baseURL == "" || token == "" {
		t.Fatalf("%s/.env carries no HA_URL/HA_TOKEN pair", tgt.Dir)
	}
	return baseURL, token
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
