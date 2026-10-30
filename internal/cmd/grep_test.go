package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"unicode"
)

// --- fixtures ---

// grepCompanion stubs GET /v1/ref/grep and records every query it was sent.
type grepCompanion struct {
	srv     *httptest.Server
	mu      sync.Mutex
	queries []url.Values
}

func (g *grepCompanion) calls() []url.Values {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]url.Values(nil), g.queries...)
}

// startGrepCompanion answers /v1/ref/grep with body (or fails it with status
// when body is empty) and fails the test on any other route: grep reads
// exactly one companion route (D-50).
func startGrepCompanion(t *testing.T, status int, body string) *grepCompanion {
	t.Helper()
	g := &grepCompanion{}
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/ref/grep" {
			t.Errorf("grep called an unexpected companion route: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		g.mu.Lock()
		g.queries = append(g.queries, r.URL.Query())
		g.mu.Unlock()
		if body == "" {
			w.WriteHeader(status)
			_, _ = fmt.Fprint(w, `{"error":"stub companion says no"}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

// grepEmptyConfig is a config half with nothing in it.
const grepEmptyConfig = `{"term":"x","regex":false,"case_sensitive":false,"hits":[],"total":0,"truncated":false,"sources":[]}`

// grepFixture wires a fake HA serving lovelace (the same document for every
// dashboard) and the given dashboards list, plus the companion stub. Any
// request for the entity registry or states fails the test (D-50).
func grepFixture(t *testing.T, cc *grepCompanion, dashboards []any, lovelace any) *cmdTestServer {
	t.Helper()
	failEntitySource := func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("grep read an entity source it must not read (D-50): %s", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, "[]")
	}
	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": dashboards,
		"lovelace/config":          lovelace,
	}, map[string]http.HandlerFunc{"/api/states": failEntitySource, "/api/states/": failEntitySource})
	writeRefEnv(t, ts.dir, ts.srv.URL, cc.srv.URL)
	withFlagDir(t, ts.dir)
	return ts
}

func withGrepFlags(t *testing.T, regex, caseSensitive bool) {
	t.Helper()
	oldR, oldC := flagGrepRegex, flagGrepCaseSensitive
	flagGrepRegex, flagGrepCaseSensitive = regex, caseSensitive
	t.Cleanup(func() { flagGrepRegex, flagGrepCaseSensitive = oldR, oldC })
}

func decodeGrepAnswer(t *testing.T, out string) grepAnswer {
	t.Helper()
	var ans grepAnswer
	if err := json.Unmarshal([]byte(out), &ans); err != nil {
		t.Fatalf("grep --json does not parse: %v\n%s", err, out)
	}
	return ans
}

// balkonyDashboard is AC-1's original report: the id sits in a card, the
// term is a fragment of it.
func balkonyDashboard() map[string]any {
	return map[string]any{
		"title": "Energie",
		"views": []any{map[string]any{
			"title": "Balkon",
			"cards": []any{
				map[string]any{"type": "entity", "entity": "sensor.balkony_p_ac"},
				map[string]any{"type": "markdown", "content": "Nothing here"},
			},
		}},
	}
}

// --- AC-1 ---

// TestGrepFindsATermInsideADashboardValue is the field report behind #115:
// `dash grep balkony` answered "not referenced" while sensor.balkony_p_ac sat in
// a dashboard, because every search hactl had matched a whole value. The term
// is a fragment, in another case than the value, and the hit carries the value
// and the key it sits under.
func TestGrepFindsATermInsideADashboardValue(t *testing.T) {
	cc := startGrepCompanion(t, 0, grepEmptyConfig)
	grepFixture(t, cc, []any{}, balkonyDashboard())

	var buf bytes.Buffer
	if err := runGrep(context.Background(), &buf, "BALKONY"); err != nil {
		t.Fatalf("grep: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"dashboard", "(default)", "views[0].cards[0].entity", "entity", "sensor.balkony_p_ac"} {
		if !strings.Contains(out, want) {
			t.Errorf("grep output missing %q\n%s", want, out)
		}
	}
	if strings.Contains(out, "Nothing here") {
		t.Errorf("a value without the term is not a hit\n%s", out)
	}

	withRefFlag(t, &flagJSON)
	buf.Reset()
	if err := runGrep(context.Background(), &buf, "balkony"); err != nil {
		t.Fatalf("grep --json: %v", err)
	}
	ans := decodeGrepAnswer(t, buf.String())
	want := grepRow{Source: "dashboard", Location: "(default)", Path: "views[0].cards[0].entity",
		Key: "entity", Kind: "value", Value: "sensor.balkony_p_ac"}
	if len(ans.Hits) != 1 || ans.Hits[0] != want {
		t.Fatalf("hits = %+v, want exactly %+v", ans.Hits, want)
	}
	if ans.Total != 1 || ans.Dashboards.Total != 1 || ans.Truncated || ans.Partial {
		t.Errorf("counts/flags wrong: %+v", ans)
	}
}

// TestGrepGroupsBySourceWithExactCounts pins the output policy (D-48): a
// complete per-source summary with key/value counts comes first, the hit rows
// follow under --top, and the two reconcile (H-11).
func TestGrepGroupsBySourceWithExactCounts(t *testing.T) {
	cc := startGrepCompanion(t, 0, `{"term":"licht","regex":false,"case_sensitive":false,
		"hits":[
			{"location":"configuration.yaml","path":"input_number.licht_level","key":"licht_level","kind":"key","matched_value":"licht_level"},
			{"location":"scripts.yaml","path":"licht_an","key":"licht_an","kind":"key","matched_value":"licht_an"},
			{"location":"scripts.yaml","path":"licht_an.alias","key":"alias","kind":"value","matched_value":"Licht an"}
		],
		"total":3,"truncated":false,
		"sources":[
			{"location":"configuration.yaml","key_hits":1,"value_hits":0},
			{"location":"scripts.yaml","key_hits":1,"value_hits":1}
		]}`)
	lovelace := map[string]any{"views": []any{map[string]any{"cards": []any{
		map[string]any{"entity": "light.licht_flur"}, map[string]any{"entity": "light.licht_bad"},
	}}}}
	grepFixture(t, cc, []any{map[string]any{"id": "wohnen", "url_path": "wohnen", "mode": "storage", "title": "Wohnen"}}, lovelace)
	withTop(t, 2)

	var buf bytes.Buffer
	if err := runGrep(context.Background(), &buf, "licht"); err != nil {
		t.Fatalf("grep: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `grep "licht" (substring, case-insensitive): 7 hit(s) in 4 source(s) — config files 3, dashboards 4`) {
		t.Errorf("headline does not reconcile the two halves\n%s", out)
	}
	// The summary is complete whatever --top says: every source, its counts.
	for _, want := range []string{
		"config     configuration.yaml  1     0",
		"config     scripts.yaml        1     1",
		"dashboard  (default)           -     2",
		"dashboard  wohnen              -     2",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing row %q\n%s", want, out)
		}
	}
	if !strings.Contains(out, "…+5 more") {
		t.Errorf("--top 2 must cut the hit rows and say how many it cut\n%s", out)
	}
	if strings.Index(out, "dashboard  wohnen") > strings.Index(out, "path") {
		t.Errorf("the summary must come before the hit rows\n%s", out)
	}
}

// TestGrepZeroHitsIsEmptyListsNotNull: the manual documents `sources` and
// `hits` as lists, and a miss is the commonest answer a search gives — a
// consumer that ranges over `hits` must not meet `null` there.
func TestGrepZeroHitsIsEmptyListsNotNull(t *testing.T) {
	cc := startGrepCompanion(t, 0, grepEmptyConfig)
	grepFixture(t, cc, []any{}, map[string]any{"views": []any{}})
	withRefFlag(t, &flagJSON)

	var buf bytes.Buffer
	if err := runGrep(context.Background(), &buf, "balkony"); err != nil {
		t.Fatalf("grep --json: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(buf.Bytes(), &doc); err != nil {
		t.Fatalf("grep --json does not parse: %v\n%s", err, buf.String())
	}
	for _, field := range []string{"sources", "hits", "unread"} {
		if _, isList := doc[field].([]any); !isList {
			t.Errorf("%q = %#v on a zero-hit answer, want an empty list", field, doc[field])
		}
	}
}

// TestGrepPartialRefusalGivesGrepsOwnReason: the D-31 refusal is shared with
// ref scan and dash grep, whose --json is a bare array with nowhere to carry
// the scope. grep's document has `partial`, so that reason would be false of
// it — the refusal must give the one that is true of grep's output.
func TestGrepPartialRefusalGivesGrepsOwnReason(t *testing.T) {
	cc := startGrepCompanion(t, http.StatusUnauthorized, "")
	grepFixture(t, cc, []any{}, balkonyDashboard())
	withRefFlag(t, &flagJSON)

	err := runGrep(context.Background(), &bytes.Buffer{}, "balkony")
	if err == nil {
		t.Fatal("a partial sweep must refuse under --json")
	}
	if strings.Contains(err.Error(), "bare array") {
		t.Errorf("the refusal states ref scan's medium, not grep's:\n%s", err.Error())
	}
	if !strings.Contains(err.Error(), "partial") {
		t.Errorf("the refusal does not name the field a consumer can read past:\n%s", err.Error())
	}
}

func withTop(t *testing.T, n int) {
	t.Helper()
	old := flagTop
	flagTop = n
	t.Cleanup(func() { flagTop = old })
}

// TestGrepStatesTruncationNeverSilently: each half carries at most 1,000 rows,
// and a half that holds more says so with its exact total, in the body and in
// --json (D-48, AC-3). The dashboard half's cap is hactl's; the config half's is
// the companion's, decoded rather than inferred.
func TestGrepStatesTruncationNeverSilently(t *testing.T) {
	cc := startGrepCompanion(t, 0, `{"term":"on","regex":false,"case_sensitive":false,
		"hits":[{"location":"automations.yaml","path":"[0].alias","key":"alias","kind":"value","matched_value":"Licht on"}],
		"total":3681,"truncated":true,
		"sources":[{"location":"automations.yaml","key_hits":2531,"value_hits":1150}]}`)
	cards := make([]any, 0, grepHalfCap+102)
	for i := range grepHalfCap + 102 {
		cards = append(cards, map[string]any{"entity": fmt.Sprintf("sensor.on_%d", i)})
	}
	grepFixture(t, cc, []any{}, map[string]any{"views": []any{map[string]any{"cards": cards}}})

	var buf bytes.Buffer
	if err := runGrep(context.Background(), &buf, "on"); err != nil {
		t.Fatalf("grep: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		"truncated: the config half holds 3681 hit(s) and returns the first 1",
		"truncated: the dashboard half holds 1102 hit(s) and returns the first 1000",
		"dashboard  (default)         -     1102",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}

	withRefFlag(t, &flagJSON)
	buf.Reset()
	if err := runGrep(context.Background(), &buf, "on"); err != nil {
		t.Fatalf("grep --json: %v", err)
	}
	ans := decodeGrepAnswer(t, buf.String())
	if !ans.Truncated || !ans.Config.Truncated || !ans.Dashboards.Truncated {
		t.Errorf("--json must state both truncations: %+v %+v", ans.Config, ans.Dashboards)
	}
	if ans.Dashboards.Total != grepHalfCap+102 || ans.Dashboards.Returned != grepHalfCap {
		t.Errorf("dashboard half = %+v, want total %d returned %d", ans.Dashboards, grepHalfCap+102, grepHalfCap)
	}
	if ans.Total != 3681+grepHalfCap+102 {
		t.Errorf("total = %d, want both halves' exact totals", ans.Total)
	}
	if got := len(ans.Hits); got != 1+grepHalfCap {
		t.Errorf("--json carries %d rows, want every returned row (%d)", got, 1+grepHalfCap)
	}
}

// --- AC-4 ---

// TestGrepPartialIsStatedAndRefusesUnderJSON: grep takes D-31's search
// posture exactly as ref scan does — the scope in the body in plain text, a
// refusal under --json, and --allow-partial as the acknowledgement, after which
// the document says so in `partial` and `unread`.
func TestGrepPartialIsStatedAndRefusesUnderJSON(t *testing.T) {
	t.Run("companion_down_plain_states_scope", func(t *testing.T) {
		cc := startGrepCompanion(t, http.StatusUnauthorized, "")
		grepFixture(t, cc, []any{}, balkonyDashboard())
		var buf bytes.Buffer
		if err := runGrep(context.Background(), &buf, "balkony"); err != nil {
			t.Fatalf("plain text still answers: %v", err)
		}
		for _, want := range []string{"partial sweep", "config files could not be scanned", "sensor.balkony_p_ac"} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("output missing %q\n%s", want, buf.String())
			}
		}
	})
	t.Run("json_refuses", func(t *testing.T) {
		cc := startGrepCompanion(t, http.StatusUnauthorized, "")
		grepFixture(t, cc, []any{}, balkonyDashboard())
		withRefFlag(t, &flagJSON)
		var buf bytes.Buffer
		err := runGrep(context.Background(), &buf, "balkony")
		if err == nil || !strings.Contains(err.Error(), "--allow-partial") {
			t.Fatalf("a partial sweep must refuse under --json, got err=%v\n%s", err, buf.String())
		}
		if buf.Len() != 0 {
			t.Errorf("a refusal writes no document, got:\n%s", buf.String())
		}
	})
	t.Run("json_allow_partial_says_partial", func(t *testing.T) {
		cc := startGrepCompanion(t, 0, `{"term":"balkony","regex":false,"case_sensitive":false,"hits":[],"total":0,
			"truncated":false,"sources":[],"skipped":[{"location":"packages/heating.yaml","reason":"unreadable"}]}`)
		grepFixture(t, cc, []any{}, balkonyDashboard())
		withRefFlag(t, &flagJSON)
		withRefFlag(t, &flagGrepAllowPartial)
		var buf bytes.Buffer
		if err := runGrep(context.Background(), &buf, "balkony"); err != nil {
			t.Fatalf("--allow-partial must answer: %v", err)
		}
		ans := decodeGrepAnswer(t, buf.String())
		if !ans.Partial || len(ans.Unread) != 1 || ans.Unread[0] != (grepUnread{Source: "config", Location: "packages/heating.yaml", Reason: "unreadable"}) {
			t.Errorf("the document must carry the scope: partial=%v unread=%+v", ans.Partial, ans.Unread)
		}
		if len(ans.Hits) != 1 {
			t.Errorf("what could be read is still answered: %+v", ans.Hits)
		}
	})
	t.Run("a_missing_include_is_a_complete_zero", func(t *testing.T) {
		cc := startGrepCompanion(t, 0, `{"term":"balkony","regex":false,"case_sensitive":false,"hits":[],"total":0,
			"truncated":false,"sources":[],"skipped":[{"location":"themes","reason":"missing"}]}`)
		grepFixture(t, cc, []any{}, map[string]any{"views": []any{}})
		withRefFlag(t, &flagJSON)
		var buf bytes.Buffer
		if err := runGrep(context.Background(), &buf, "balkony"); err != nil {
			t.Fatalf("a target that is not there is not a target that went unread: %v", err)
		}
		if ans := decodeGrepAnswer(t, buf.String()); ans.Partial || len(ans.Unread) != 0 {
			t.Errorf("missing include made the answer partial: %+v", ans)
		}
	})
	t.Run("a_partial_miss_never_claims_the_negative", func(t *testing.T) {
		cc := startGrepCompanion(t, http.StatusInternalServerError, "")
		grepFixture(t, cc, []any{}, map[string]any{"views": []any{}})
		var buf bytes.Buffer
		if err := runGrep(context.Background(), &buf, "balkony"); err != nil {
			t.Fatalf("plain text answers: %v", err)
		}
		out := buf.String()
		if strings.Contains(out, "in any config file or dashboard") {
			t.Errorf("a sweep that did not read the config half may not claim a verified negative:\n%s", out)
		}
		if !strings.Contains(out, "in what could be searched") {
			t.Errorf("the partial miss must say what it covered:\n%s", out)
		}
	})
	// Every case above makes the config half the unread one; the same posture
	// for an unread dashboard is its own set of cases.
	t.Run("an_unread_dashboard", grepPartialDashboardHalf)
}

// grepConfigBalkonyHit is a complete config half holding one hit.
const grepConfigBalkonyHit = `{"term":"balkony","regex":false,"case_sensitive":false,
	"hits":[{"location":"automations.yaml","path":"[0].triggers[0].entity_id","key":"entity_id","kind":"value",
		"matched_value":"sensor.balkony_p_ac"}],
	"total":1,"truncated":false,
	"sources":[{"location":"automations.yaml","key_hits":0,"value_hits":1}]}`

// unreadableDashboard is HA failing the dashboard fetch — not the
// auto-generated default's config_not_found, which is a complete zero.
var unreadableDashboard = wsErrorResponse{Code: "home_assistant_error", Message: "boom"}

// grepPartialDashboardHalf is AC-4's dashboard source, run as a subtest of
// TestGrepPartialIsStatedAndRefusesUnderJSON (the test partialscope.manifest
// names for runGrep): the config half is complete and the dashboard walk is the
// one that fell short. H-10 records search commands that shipped "one source short" —
// a dashboard that went unread must reach the body, the --json refusal, the
// `unread` rows and the miss wording exactly as an unread config half does.
func grepPartialDashboardHalf(t *testing.T) {
	t.Run("plain_states_the_unread_dashboard", func(t *testing.T) {
		cc := startGrepCompanion(t, 0, grepConfigBalkonyHit)
		grepFixture(t, cc, []any{}, unreadableDashboard)
		var buf bytes.Buffer
		if err := runGrep(context.Background(), &buf, "balkony"); err != nil {
			t.Fatalf("plain text still answers: %v", err)
		}
		for _, want := range []string{"partial sweep: 0 of 1 dashboard(s) scanned", "(default)", "boom", "sensor.balkony_p_ac"} {
			if !strings.Contains(buf.String(), want) {
				t.Errorf("output missing %q\n%s", want, buf.String())
			}
		}
	})
	t.Run("json_refuses", func(t *testing.T) {
		cc := startGrepCompanion(t, 0, grepConfigBalkonyHit)
		grepFixture(t, cc, []any{}, unreadableDashboard)
		withRefFlag(t, &flagJSON)
		var buf bytes.Buffer
		err := runGrep(context.Background(), &buf, "balkony")
		if err == nil || !strings.Contains(err.Error(), "--allow-partial") || !strings.Contains(err.Error(), "dashboard") {
			t.Fatalf("an unread dashboard must refuse under --json and name the dashboard half, got err=%v\n%s", err, buf.String())
		}
		if buf.Len() != 0 {
			t.Errorf("a refusal writes no document, got:\n%s", buf.String())
		}
	})
	t.Run("json_allow_partial_names_the_dashboard", func(t *testing.T) {
		cc := startGrepCompanion(t, 0, grepConfigBalkonyHit)
		grepFixture(t, cc, []any{}, unreadableDashboard)
		withRefFlag(t, &flagJSON)
		withRefFlag(t, &flagGrepAllowPartial)
		var buf bytes.Buffer
		if err := runGrep(context.Background(), &buf, "balkony"); err != nil {
			t.Fatalf("--allow-partial must answer: %v", err)
		}
		ans := decodeGrepAnswer(t, buf.String())
		if !ans.Partial || len(ans.Unread) != 1 || ans.Unread[0].Source != "dashboard" ||
			ans.Unread[0].Location != "(default)" || !strings.Contains(ans.Unread[0].Reason, "boom") {
			t.Errorf("the document must carry the dashboard scope: partial=%v unread=%+v", ans.Partial, ans.Unread)
		}
		if len(ans.Hits) != 1 || ans.Hits[0].Source != "config" {
			t.Errorf("the config half is still answered: %+v", ans.Hits)
		}
	})
	t.Run("a_partial_miss_never_claims_the_negative", func(t *testing.T) {
		cc := startGrepCompanion(t, 0, grepEmptyConfig)
		grepFixture(t, cc, []any{}, unreadableDashboard)
		var buf bytes.Buffer
		if err := runGrep(context.Background(), &buf, "balkony"); err != nil {
			t.Fatalf("plain text answers: %v", err)
		}
		out := buf.String()
		if strings.Contains(out, "in any config file or dashboard") {
			t.Errorf("a sweep that did not read a dashboard may not claim a verified negative:\n%s", out)
		}
		if !strings.Contains(out, "in what could be searched") {
			t.Errorf("the partial miss must say what it covered:\n%s", out)
		}
	})
}

// TestGrepCompanionRefusalEndsTheCommand: a 400 is the companion refusing the
// term (a regex past its deadline, a pattern Python warns about) — the caller's
// input, not an unread source. Reporting it as "config files could not be read"
// beside a dashboard answer would be the half-answer D-31 forbids, with the
// wrong cause attached.
func TestGrepCompanionRefusalEndsTheCommand(t *testing.T) {
	cc := startGrepCompanion(t, http.StatusBadRequest, "")
	grepFixture(t, cc, []any{}, balkonyDashboard())

	var buf bytes.Buffer
	err := runGrep(context.Background(), &buf, "balkony")
	if err == nil {
		t.Fatalf("a companion 400 must end the command, got:\n%s", buf.String())
	}
	if !strings.Contains(err.Error(), "refused this search") || !strings.Contains(err.Error(), "stub companion says no") {
		t.Errorf("the refusal must carry the companion's reason: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("a refusal prints no answer, got:\n%s", buf.String())
	}
	if n := len(cc.calls()); n != 1 {
		t.Errorf("a 400 is not retried (it would be refused again): %d calls", n)
	}
}

// TestGrepDoesNotSearchDashboardKeys: dashboard keys are card schema, never a
// user's words (D-48: 0 hits for every real word measured, 395 schema hits for
// `on`). A config key IS searched — that is the companion's half.
func TestGrepDoesNotSearchDashboardKeys(t *testing.T) {
	cc := startGrepCompanion(t, 0, grepEmptyConfig)
	grepFixture(t, cc, []any{}, map[string]any{"views": []any{map[string]any{
		"cards": []any{map[string]any{"show_state": true, "state_color": "follow the state"}},
	}}})
	withRefFlag(t, &flagJSON)

	var buf bytes.Buffer
	if err := runGrep(context.Background(), &buf, "state"); err != nil {
		t.Fatalf("grep: %v", err)
	}
	ans := decodeGrepAnswer(t, buf.String())
	if len(ans.Hits) != 1 || ans.Hits[0].Path != "views[0].cards[0].state_color" {
		t.Errorf("only the VALUE containing the term is a hit (the keys show_state/state_color are not): %+v", ans.Hits)
	}
}

// TestGrepReadsNoEntitySource is D-50: grep reads config files and dashboards
// and nothing else — no registry, no states. The fixture fails the test on a
// states request; this asserts the registry was not asked either.
func TestGrepReadsNoEntitySource(t *testing.T) {
	cc := startGrepCompanion(t, 0, grepEmptyConfig)
	ts := grepFixture(t, cc, []any{}, balkonyDashboard())

	var buf bytes.Buffer
	if err := runGrep(context.Background(), &buf, "balkony"); err != nil {
		t.Fatalf("grep: %v", err)
	}
	for _, cmd := range []string{"config/entity_registry/list", "config/device_registry/list", "config/area_registry/list"} {
		if n := ts.commandCount(cmd); n != 0 {
			t.Errorf("grep sent %s %d time(s); D-50 keeps entity data out of its scope", cmd, n)
		}
	}
	if n := len(cc.calls()); n != 1 {
		t.Errorf("grep asked the companion %d time(s), want exactly one /v1/ref/grep", n)
	}
}

// TestGrepSendsTheMatchingModeToTheCompanion: both halves must be asked the
// same question, so the flags that decide the question reach the config half.
func TestGrepSendsTheMatchingModeToTheCompanion(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		regex, caseSensitive bool
		term                 string
		want                 url.Values
	}{
		{name: "default", term: "Küche", want: url.Values{"term": {"Küche"}}},
		{name: "regex", regex: true, term: "wp_(vl|rl)", want: url.Values{"term": {"wp_(vl|rl)"}, "regex": {"true"}}},
		{name: "case_sensitive", caseSensitive: true, term: "Licht", want: url.Values{"term": {"Licht"}, "case_sensitive": {"true"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cc := startGrepCompanion(t, 0, grepEmptyConfig)
			grepFixture(t, cc, []any{}, map[string]any{"views": []any{}})
			withGrepFlags(t, tc.regex, tc.caseSensitive)
			var buf bytes.Buffer
			if err := runGrep(context.Background(), &buf, tc.term); err != nil {
				t.Fatalf("grep: %v", err)
			}
			calls := cc.calls()
			if len(calls) != 1 || calls[0].Encode() != tc.want.Encode() {
				t.Errorf("companion was asked %v, want %v", calls, tc.want)
			}
		})
	}
}

// --- AC-5 / AC-6 ---

// TestGrepRefusesADegenerateTermBeforeAnyWireCall: every refusal happens before
// anything is contacted. The instance directory here has no .env at all, so a
// refusal that came after config.Load would surface as a configuration error
// instead of the term refusal asserted below.
func TestGrepRefusesADegenerateTermBeforeAnyWireCall(t *testing.T) {
	withFlagDir(t, t.TempDir())
	for _, tc := range []struct {
		name  string
		term  string
		regex bool
		want  string
	}{
		{name: "empty", term: "", want: "needs a term"},
		{name: "whitespace", term: " \t ", want: "needs a term"},
		{name: "one_rune", term: "a", want: "at least 2"},
		{name: "one_non_ascii_rune", term: "ü", want: "at least 2"},
		{name: "regex_whitespace", term: "  ", regex: true, want: "needs a term"},
		{name: "regex_invalid", term: "wp_(vl", regex: true, want: "missing closing )"},
		{name: "regex_lookahead", term: "wp_(?=vl)", regex: true, want: "invalid --regex"},
		{name: "regex_backref", term: `(a)\1`, regex: true, want: "invalid --regex"},
		{name: "regex_divergent", term: `\w+`, regex: true, want: "ASCII-only in Go"},
		{name: "regex_empty_match", term: "x*", regex: true, want: "matches the empty string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withGrepFlags(t, tc.regex, false)
			var buf bytes.Buffer
			err := runGrep(context.Background(), &buf, tc.term)
			if !errors.Is(err, errGrepTermRefused) {
				t.Fatalf("want a term refusal before any connection, got %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("refusal %q does not say %q", err, tc.want)
			}
			if buf.Len() != 0 {
				t.Errorf("a refusal prints nothing, got %q", buf.String())
			}
		})
	}
	// A regex is not length-gated (oracle resolution 3): a short pattern gets
	// past the term checks and fails, here, only at the missing .env.
	withGrepFlags(t, true, false)
	if err := runGrep(context.Background(), &bytes.Buffer{}, "[0-9]{4}"); errors.Is(err, errGrepTermRefused) {
		t.Errorf("a short legitimate pattern was refused: %v", err)
	}
}

// --- D-49: the matcher ---

// TestGrepLowercaseRuleIsTheCompanions pins the dashboard half's folding to the
// simple Unicode lowercase the companion's simple_lower applies on the config
// half — not case folding, which maps ß to ss in Python and has no Go twin.
func TestGrepLowercaseRuleIsTheCompanions(t *testing.T) {
	for _, tc := range []struct {
		term, value string
		want        bool
	}{
		{"küche", "KÜCHE Licht", true},
		{"KÜCHE", "Küche", true},
		{"straße", "STRASSE", false}, // no ß→SS: casefold would say true
		{"strasse", "Straße", false}, // and no transliteration either
		{"straße", "STRAẞE", true},   // ẞ lowercases to ß by the simple mapping
		{"istanbul", "İstanbul", true},
		{"οδοσ", "ΟΔΟΣ", true}, // no final-sigma rule
		{"kueche", "Küche", false},
		{"balkony", "sensor.balkony_p_ac", true},
	} {
		match, err := validateGrepTerm(tc.term, false, false)
		if err != nil {
			t.Fatalf("%q: %v", tc.term, err)
		}
		if got := match(tc.value); got != tc.want {
			t.Errorf("term %q on %q = %v, want %v", tc.term, tc.value, got, tc.want)
		}
	}
	sensitive, err := validateGrepTerm("Licht", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if sensitive("licht an") || !sensitive("Licht an") {
		t.Error("--case-sensitive must compare as written")
	}
}

// TestGrepRegexContract is oracle resolution 4 as a table: the common subset
// compiles, everything the two engines read differently — or only one of them
// reads at all — is refused with a reason.
func TestGrepRegexContract(t *testing.T) {
	accepted := []string{
		`[a-z_]+\.[a-z0-9_]+`, `[^a-z ]+`, `wp_(vl|rl)`, `a{2,3}`, `a{2}`, `a{2,}`, `<.+?>`,
		`(?i)balkony`, `(?i)küche`, `a(?i:b)c`, `(?m)^b$`, `(?s)a.b`, `(?i)(?s)a.b`,
		`\Afoo`, `(?P<dom>[a-z]+)\.`, `a|ab`, `(|a)b`, `[äöüß]+`, `sensor\.wp`, `\{\{`, `a{`, `x{2`,
		"[\u0130\u0131]", "[\u00e0-\u00ff]+", "[\u0132-\u017e]", `[]a]`, `[^]a]`, `\x41`, `\t\n`, `}}$`, `[a-]`, `[0-9]{4}`,
	}
	for _, p := range accepted {
		if _, err := validateGrepRegex(p, false); err != nil {
			t.Errorf("%q is in the common subset and must compile: %v", p, err)
		}
	}
	refused := map[string]string{
		`\w+`: "ASCII-only", `\W`: "ASCII-only", `\d+`: "ASCII-only", `\D`: "ASCII-only",
		`a\sb`: "ASCII-only", `\S`: "ASCII-only", `\bkino\b`: "ASCII-only", `\Bx`: "ASCII-only",
		`[\d]`: "ASCII-only",
		`\12`:  "digit escape", `\123`: "digit escape",
		`a{,3}b`:      "{0,n}",
		`[[:alpha:]]`: "inside a class", `[a[b]`: "inside a class",
		`[--x]`: "set operations", `[a&&b]`: "set operations", `[a~~b]`: "set operations", `[a||b]`: "set operations",
		`foo\z`: "Go-only", `\p{L}+`: "Go-only", `\pL`: "Go-only", `\PL`: "Go-only", `\Qa.b\E`: "Go-only",
		`\x{fc}`:     "Go-only",
		`(?<dom>a)`:  "Go-only",
		`(?U)a+`:     "Go-only or unknown",
		`a(?i)b`:     "at the start",
		`(?i)a(?i)b`: "at the start",
		`(?-i)a`:     "turns a flag off",
		// Part of the pattern in the other case mode: the dashboard half's
		// Python-fold mapping is only right for a pattern in one mode.
		`(?i)a(?-i:b)`: "case-insensitive pattern case-sensitive",
		`a(?s-i:b)`:    "case-insensitive pattern case-sensitive",
		// RE2 cannot compile these; Python can. Refused by the parser itself.
		`wp_(?=vl)`: "invalid --regex", `wp_(?!rl)`: "invalid --regex", `(?<=sensor\.)wp`: "invalid --regex",
		`(a)\1`: "invalid --regex", `(?P<c>a)(?P=c)`: "invalid --regex", `a++b`: "invalid --regex",
		`(?>a+)b`: "invalid --regex", `a(?#c)b`: "invalid --regex", `foo\Z`: "invalid --regex",
		"\\u00fc": "invalid --regex", `a{1001}`: "invalid --regex", `(?a)\w`: "invalid --regex",
	}
	for p, want := range refused {
		_, err := validateGrepRegex(p, false)
		if err == nil {
			t.Errorf("%q must be refused (%s)", p, want)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%q refused as %q, want it to say %q", p, err, want)
		}
	}
}

// TestGrepRegexCaseModeIsOneModeUnderCaseSensitive: --case-sensitive turns the
// same scoped-mode rule around — a pattern that opens with (?i) is
// case-insensitive as a whole and folds like the default, and a scoped (?i:…)
// is refused.
func TestGrepRegexCaseModeIsOneModeUnderCaseSensitive(t *testing.T) {
	for _, p := range []string{`(?i)istanbul`, `(?is)a.b`, "[\u0125-\u0137]", `abc`} {
		if _, err := validateGrepRegex(p, true); err != nil {
			t.Errorf("--case-sensitive %q must compile: %v", p, err)
		}
	}
	for _, p := range []string{`a(?i:b)c`, `(?s)a(?i:b)`} {
		_, err := validateGrepRegex(p, true)
		if err == nil || !strings.Contains(err.Error(), "--case-sensitive pattern case-insensitive") {
			t.Errorf("--case-sensitive %q must be refused for switching mode in part, got %v", p, err)
		}
	}
}

// TestGrepRegexFoldsAsPythonDoes pins the dashboard half's case-insensitive
// --regex to Python's re.IGNORECASE, which the config half runs. Go's (?i) alone
// folds İ, ı, ΐ/ΐ, ΰ/ΰ and ﬅ/ﬆ narrower than Python, so `--regex istanbul`
// found İstanbul in config and missed it in a dashboard (grepPythonFold).
//
// want is Python's answer, recorded with re.search(pattern, value, flags) on
// Python 3.12.13 (flags re.IGNORECASE unless case-sensitive); the
// both-halves version of the same question is the E2E
// TestE2EGrepNonASCIITermThroughBothHalves.
func TestGrepRegexFoldsAsPythonDoes(t *testing.T) {
	for _, tc := range []struct {
		pattern, value string
		caseSensitive  bool
		want           bool
	}{
		{"istanbul", "\u0130stanbul", false, true},
		{"ISTANBUL", "\u0130stanbul", false, true},
		{"\u0130stanbul", "istanbul", false, true},
		{"\u0131stanbul", "ISTANBUL", false, true},
		{"ISTANBUL", "\u0131stanbul", false, true},
		{"\u0131", "\u0130", false, true},
		{"^[a-z]+$", "\u0130\u0131", false, true},
		{"^[a-z]+$", "\u0131i", false, true},
		{"^[^a-z]$", "\u0131", false, false},
		{"^[^a-z]$", "\u0130", false, false},
		{"^[\u0131]$", "I", false, true},
		{"^[\u0130]$", "i", false, true},
		{"^.stanbul$", "\u0130stanbul", false, true},
		{"\u0390", "\u1fd3", false, true},
		{"\u1fd3", "\u0390", false, true},
		{"\u03b0", "\u1fe3", false, true},
		{"\ufb06", "\ufb05", false, true},
		{"\ufb05", "\ufb06", false, true},
		{"^[\u03b1-\u03c9]$", "\u1fd3", false, false},
		{"^[\u0390-\u03ac]$", "\u1fd3", false, true},
		{"küche", "KÜCHE", false, true},
		{"straße", "STRASSE", false, false},
		{"straße", "STRA\u1e9eE", false, true},
		{"k", "\u212a", false, true},
		{"s", "\u017f", false, true},
		{"istanbul", "\u0130stanbul", true, false},
		{"\u0131", "i", true, false},
		{"(?i)istanbul", "\u0130stanbul", true, true},
		{"(?i)^[a-z]+$", "\u0130\u0131", true, true},
		{"\u0130stanbul", "\u0130stanbul", true, true},
		// A class range keeps its endpoints and holds what Python's does.
		{"^[\u0125-\u0137]$", "i", false, true}, // spans İ and ı, so holds i and I
		{"^[\u0125-\u0137]$", "I", false, true},
		{"^[^\u0125-\u0137]$", "i", false, false}, // and its negation leaves them out
		{"^[^\u0125-\u0137]$", "a", false, true},
		{"^[\\x41-\ufb06]+$", "\u0131\ufb05\u1fd3", false, true}, // an escape endpoint
		{"^[\u0391-\u03a9]$", "\u1fd3", false, false},
		{"^[\u1fd0-\u1fd5]$", "\u0390", false, true},
		{"^[\\]-\u0131]$", "I", false, true},
		{"^[\u0125-\u0137]$", "i", true, false},
		{"(?i)^[\u0125-\u0137]$", "I", true, true},
	} {
		g, err := validateGrepRegex(tc.pattern, tc.caseSensitive)
		if err != nil {
			t.Fatalf("%q: %v", tc.pattern, err)
		}
		if got := regexGrepMatch(g)(tc.value); got != tc.want {
			t.Errorf("%+q on %+q (case-sensitive=%v) = %v, Python says %v", tc.pattern, tc.value, tc.caseSensitive, got, tc.want)
		}
	}
}

// TestGrepPythonFoldClosesEveryOrbit is the exhaustive half of the same pin,
// over every cased code point: with grepPythonFold, a one-character
// case-insensitive pattern matches exactly the characters Python's
// re.IGNORECASE does. Python's relation was measured over every code point
// (Python 3.12, Unicode 15.0): it is Go's unicode.SimpleFold orbit, widened
// only for the four classes below. A Go whose Unicode tables change those
// orbits fails here rather than silently reading a pattern differently.
func TestGrepPythonFoldClosesEveryOrbit(t *testing.T) {
	pythonWide := [][]rune{{'I', 'i', 0x130, 0x131}, {0x390, 0x1fd3}, {0x3b0, 0x1fe3}, {0xfb05, 0xfb06}}
	classOf := map[rune][]rune{}
	for _, c := range pythonWide {
		for _, r := range c {
			classOf[r] = c
		}
	}
	orbit := func(r rune) []rune {
		out := []rune{r}
		for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
			out = append(out, f)
		}
		return out
	}
	pythonMatches := func(c, o rune) bool {
		if slices.Contains(orbit(c), o) {
			return true
		}
		return classOf[c] != nil && slices.Contains(classOf[c], o)
	}
	for c := rune(0); c <= unicode.MaxRune; c++ {
		if unicode.SimpleFold(c) == c && classOf[c] == nil {
			continue
		}
		neighbourhood := orbit(c)
		for _, m := range classOf[c] {
			neighbourhood = append(neighbourhood, orbit(m)...)
		}
		g, err := validateGrepRegex("^"+regexp.QuoteMeta(string(c))+"$", false)
		if err != nil {
			t.Fatalf("U+%04X: %v", c, err)
		}
		match := regexGrepMatch(g)
		for _, o := range neighbourhood {
			if got, want := match(string(o)), pythonMatches(c, o); got != want {
				t.Errorf("pattern U+%04X on U+%04X = %v, Python's IGNORECASE says %v", c, o, got, want)
			}
		}
	}
}

// TestGrepDollarReadsAsPythonDoes: Python's `$` matches before one final
// newline and Go's does not, and a block scalar keeps its newline (78 of 7,406
// leaves on the reference house). The dashboard half reads `$` the companion's
// way, so `}}$` answers the same on both halves.
func TestGrepDollarReadsAsPythonDoes(t *testing.T) {
	for _, tc := range []struct {
		pattern, value string
		want           bool
	}{
		{`}}$`, "{{ x }}\n", true},    // Python: match; bare Go: no match
		{`^foo$`, "foo\n", true},      // oracle row 19
		{`^foo$`, "foo\n\n", false},   // Python's $ allows ONE final newline
		{`foo$`, "foo bar", false},    // and nothing else
		{`\n$`, "foo\n", true},        // a $ at the true end still matches
		{`(?m)^b$`, "a\nb\nc", true},  // multiline is the same on both
		{`balkony$`, "balkony", true}, // no newline, nothing changes
	} {
		re, err := validateGrepRegex(tc.pattern, false)
		if err != nil {
			t.Fatalf("%q: %v", tc.pattern, err)
		}
		if got := regexGrepMatch(re)(tc.value); got != tc.want {
			t.Errorf("%q on %q = %v, want %v (Python's reading)", tc.pattern, tc.value, got, tc.want)
		}
	}
}

// TestScanDashboardsStillMatchesWholeValuesOnly is AC-7's structural half: the
// matcher grep threads through the shared walk must leave the exact-id search
// exactly as it was — a fragment, a case variant and a key are not hits.
func TestScanDashboardsStillMatchesWholeValuesOnly(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"lovelace/dashboards/list": []any{},
		"lovelace/config": map[string]any{"views": []any{map[string]any{"cards": []any{
			map[string]any{"entity": "sensor.balkony_p_ac"},
			map[string]any{"entity": "SENSOR.BALKONY_P_AC"},
			map[string]any{"sensor.balkony_p_ac": "a key, never a hit"},
			map[string]any{"content": "see sensor.balkony_p_ac"},
		}}}},
	}, nil)
	withFlagDir(t, ts.dir)
	ws, err := connectWS(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ws.Close() }()

	hits, scope := scanDashboards(context.Background(), ws, []dashScanTarget{{label: "(default)"}}, "sensor.balkony_p_ac")
	if scope.partial() {
		t.Fatalf("unexpected partial scope: %v", scope.reason())
	}
	if len(hits) != 1 || hits[0] != (dashHit{"(default)", "views[0].cards[0].entity"}) {
		t.Errorf("the exact-value walk changed: %+v", hits)
	}
}
