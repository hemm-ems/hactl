//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// ============================================================================
// D-232 / D-233 — every logbook request hactl sends is small, measured on the
// wire between hactl and a real Home Assistant (#195).
//
// HA builds a logbook answer in full inside a recorder executor job that a
// client disconnect does not cancel, so the size of each request is the only
// bound a client has on the work it causes. Before D-232 `auto ls --since 30d`
// sent one request for every entity over thirty days, and the instance in
// #195 ran out of memory after hactl had already given up.
// ============================================================================

// logbookRequests returns the logbook requests among urls.
func logbookRequests(urls []*url.URL) []*url.URL {
	var out []*url.URL
	for _, u := range urls {
		if strings.HasPrefix(u.Path, "/api/logbook/") {
			out = append(out, u)
		}
	}
	return out
}

// logbookSpan decodes the window one logbook request asked about.
func logbookSpan(t *testing.T, u *url.URL) time.Duration {
	t.Helper()
	start, err := time.Parse(time.RFC3339Nano, strings.TrimPrefix(u.Path, "/api/logbook/"))
	if err != nil {
		t.Fatalf("logbook request %s: start: %v", u, err)
	}
	end, err := time.Parse(time.RFC3339Nano, u.Query().Get("end_time"))
	if err != nil {
		t.Fatalf("logbook request %s: end_time: %v", u, err)
	}
	return end.Sub(start)
}

// requestedEntities is the union of the entity= values of reqs.
func requestedEntities(reqs []*url.URL) []string {
	set := map[string]bool{}
	for _, u := range reqs {
		for id := range strings.SplitSeq(u.Query().Get("entity"), ",") {
			if id != "" {
				set[id] = true
			}
		}
	}
	out := make([]string, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// listedIDs returns the "id" column of a JSON listing, prefixed with domain.
func listedIDs(t *testing.T, raw, domain string) []string {
	t.Helper()
	var rows []map[string]string
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("listing did not parse: %v\n%s", err, raw)
	}
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, domain+"."+r["id"])
	}
	sort.Strings(out)
	return out
}

// assertEveryRequestIsSmall checks D-232's two bounds on every request:
// at most a day (plus the 1 µs overlap), and, when filtered, an entity value
// within the chunk budget.
func assertEveryRequestIsSmall(t *testing.T, reqs []*url.URL, wantFiltered bool) {
	t.Helper()
	for _, u := range reqs {
		if span := logbookSpan(t, u); span > 24*time.Hour+time.Microsecond {
			t.Errorf("logbook request spans %s: %s", span, u)
		}
		entity := u.Query().Get("entity")
		if wantFiltered && entity == "" {
			t.Errorf("unfiltered logbook request: %s", u)
		}
		if n := len(url.QueryEscape(entity)); n > 4096 {
			t.Errorf("entity value encodes to %d bytes: %s", n, u)
		}
	}
}

// assertAsksAbout checks that reqs asked the logbook about exactly want.
func assertAsksAbout(t *testing.T, reqs []*url.URL, want []string) {
	t.Helper()
	if got := requestedEntities(reqs); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("logbook was asked about %v, the listing shows %v", got, want)
	}
}

func TestLogbookReadsAreBoundedOnTheWire(t *testing.T) {
	inst, _ := getOracleHA(t)
	proxy := newRecordingProxy(t, inst.URL())
	dir := pgInstanceDir(t, proxy.URL(), inst.Token(), "")

	t.Run("auto ls asks about the listed automations only, a day at a time", func(t *testing.T) {
		proxy.takeURLs()
		raw := runHactlDir(t, dir, "auto", "ls", "--json", "--top", "1000", "--since", "72h")
		reqs := logbookRequests(proxy.takeURLs())
		if len(reqs) < 3 {
			t.Fatalf("72h in 24 h pieces should be at least 3 logbook requests, saw %d", len(reqs))
		}
		assertEveryRequestIsSmall(t, reqs, true)
		assertAsksAbout(t, reqs, listedIDs(t, raw, "automation"))
	})

	t.Run("a filter narrows the logbook request too", func(t *testing.T) {
		proxy.takeURLs()
		runHactlDir(t, dir, "auto", "ls", "--json", "--pattern", "oracle_blocked_cond")
		reqs := logbookRequests(proxy.takeURLs())
		if len(reqs) == 0 {
			t.Fatal("auto ls --pattern sent no logbook request at all")
		}
		assertAsksAbout(t, reqs, []string{oracleBlockedEntityID})
	})

	t.Run("script ls asks about the listed scripts only", func(t *testing.T) {
		proxy.takeURLs()
		raw := runHactlDir(t, dir, "script", "ls", "--json", "--top", "1000")
		reqs := logbookRequests(proxy.takeURLs())
		assertEveryRequestIsSmall(t, reqs, true)
		assertAsksAbout(t, reqs, listedIDs(t, raw, "script"))
	})

	t.Run("changes reads a day at a time", func(t *testing.T) {
		proxy.takeURLs()
		runHactlDir(t, dir, "changes", "--since", "48h")
		reqs := logbookRequests(proxy.takeURLs())
		if len(reqs) < 2 {
			t.Fatalf("48h in 24 h pieces should be at least 2 logbook requests, saw %d", len(reqs))
		}
		assertEveryRequestIsSmall(t, reqs, false)
	})

	for _, args := range [][]string{{"auto", "ls"}, {"script", "ls"}, {"changes"}} {
		t.Run(strings.Join(args, " ")+" refuses 8d before sending anything", func(t *testing.T) {
			proxy.takeURLs()
			_, err := runHactlDirErr(t, dir, append(args, "--since", "8d")...)
			if err == nil || !strings.Contains(err.Error(), "--allow-heavy") {
				t.Fatalf("--since 8d was not refused with the flag's name: %v", err)
			}
			if sent := proxy.takeURLs(); len(sent) != 0 {
				t.Errorf("a refused window still sent %d request(s), first %s", len(sent), sent[0])
			}
		})
	}
}

// TestLogbookMultiEntityAnswerIsTheUnion is the oracle D-232 rests on: HA
// answers a comma-separated entity= with exactly the entries it gives for each
// id on its own (components/logbook/rest_api.py splits the value; the queries
// bind the ids into the SQL). If HA ever narrowed this to the first id, a
// chunked read would silently undercount every id after it.
func TestLogbookMultiEntityAnswerIsTheUnion(t *testing.T) {
	inst, _ := getOracleHA(t)
	client := haapi.New(inst.URL(), inst.Token())
	now := time.Now()
	start, end := now.Add(-runs24hWindow).Format(time.RFC3339), now.Format(time.RFC3339)

	count := func(raw []byte) map[string]int {
		t.Helper()
		var entries []struct {
			EntityID string `json:"entity_id"`
			Domain   string `json:"domain"`
		}
		if err := json.Unmarshal(raw, &entries); err != nil {
			t.Fatalf("decoding logbook: %v", err)
		}
		out := map[string]int{}
		for _, e := range entries {
			if e.Domain == "automation" {
				out[e.EntityID]++
			}
		}
		return out
	}

	configIDs := oracleAutomationConfigIDs(t, inst)
	ids := make([]string, 0, len(configIDs))
	for entityID := range configIDs {
		ids = append(ids, entityID)
	}
	sort.Strings(ids)

	want := map[string]int{}
	for _, id := range ids {
		raw, err := client.GetLogbookFiltered(context.Background(), start, end, id)
		if err != nil {
			t.Fatalf("logbook for %s: %v", id, err)
		}
		for k, v := range count(raw) {
			want[k] += v
		}
	}
	raw, err := client.GetLogbookFiltered(context.Background(), start, end, strings.Join(ids, ","))
	if err != nil {
		t.Fatalf("logbook for %d ids: %v", len(ids), err)
	}
	got := count(raw)

	ran := 0
	for _, n := range want {
		ran += n
	}
	if ran == 0 || len(want) < 2 {
		t.Fatalf("precondition: the rig should have at least two automations with logbook runs; per-id answers: %v", want)
	}
	for id, n := range want {
		if got[id] != n {
			t.Errorf("%s: %d entries when asked alone, %d when asked with %d other ids", id, n, got[id], len(ids)-1)
		}
	}
	for id, n := range got {
		if want[id] == 0 {
			t.Errorf("%s: %d entries in the multi-id answer, none when asked alone", id, n)
		}
	}
}

// TestRuns24hIsZeroWhenTheOnlyListedRowWasBlocked is the D65 regression under
// a filter (D-234). With the logbook asked only about the listed rows, an
// automation that never ran gets an empty answer, and that has to read as a
// zero: before D-232 the empty answer could not happen, because some other
// automation's entry was always in it.
func TestRuns24hIsZeroWhenTheOnlyListedRowWasBlocked(t *testing.T) {
	inst, _ := getOracleHA(t)
	_, blocked, total := haRunsAndBlocked(t, inst, oracleBlockedConfigID)
	if blocked == 0 {
		t.Fatalf("precondition: HA holds no condition-blocked trace for %s (%d traces total)",
			oracleBlockedConfigID, total)
	}
	raw := runHactlDir(t, inst.Dir(), "auto", "ls", "--json", "--pattern", "oracle_blocked_cond")
	var rows []map[string]string
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("auto ls --json did not parse: %v\n%s", err, raw)
	}
	if len(rows) != 1 {
		t.Fatalf("auto ls --pattern oracle_blocked_cond listed %d rows, want 1:\n%s", len(rows), raw)
	}
	if rows[0]["runs_24h"] != "0" {
		t.Errorf("runs_24h = %q for %s, want \"0\": HA stopped all %d triggers at the condition "+
			"and its logbook holds no run", rows[0]["runs_24h"], oracleBlockedEntityID, blocked)
	}
}
