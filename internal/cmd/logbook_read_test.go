package cmd

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// logbookRecorder is a fake HA that answers every logbook request with body
// (or with failStatus from request number failAt on, 1-based) and records
// what it was asked.
type logbookRecorder struct {
	mu         sync.Mutex
	requests   []*url.URL
	body       string
	failAt     int
	failStatus int
}

func (lr *logbookRecorder) handler(w http.ResponseWriter, r *http.Request) {
	lr.mu.Lock()
	lr.requests = append(lr.requests, r.URL)
	n := len(lr.requests)
	lr.mu.Unlock()
	if lr.failAt > 0 && n >= lr.failAt {
		http.Error(w, "upstream gone", lr.failStatus)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprint(w, lr.body)
}

func newLogbookRecorder(t *testing.T, lr *logbookRecorder) *haapi.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(lr.handler))
	t.Cleanup(srv.Close)
	return haapi.New(srv.URL, "tok")
}

func syntheticAutomationIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("automation.wohnzimmer_licht_abends_szene_%03d", i)
	}
	return ids
}

// requestWindow decodes the span one logbook request asked about.
func requestWindow(t *testing.T, u *url.URL) (time.Time, time.Time) {
	t.Helper()
	startRaw, err := url.PathUnescape(strings.TrimPrefix(u.EscapedPath(), "/api/logbook/"))
	if err != nil {
		t.Fatalf("path %q: %v", u.EscapedPath(), err)
	}
	start, err := time.Parse(time.RFC3339Nano, startRaw)
	if err != nil {
		t.Fatalf("start %q: %v", startRaw, err)
	}
	end, err := time.Parse(time.RFC3339Nano, u.Query().Get("end_time"))
	if err != nil {
		t.Fatalf("end_time %q: %v", u.Query().Get("end_time"), err)
	}
	return start, end
}

// TestLogbookWindowsCoverWithoutGap pins D-232's window arithmetic: every
// span is at most 24 h (plus the 1 µs overlap), the spans cover the whole
// window, and each one starts just before the previous one ended, because HA
// excludes both bounds.
func TestLogbookWindowsCoverWithoutGap(t *testing.T) {
	end := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	start := end.Add(-30 * 24 * time.Hour)
	ws := logbookWindows(start, end)
	if len(ws) != 30 {
		t.Fatalf("30 days in 24 h steps gave %d windows", len(ws))
	}
	if !ws[0].start.Equal(start) || !ws[len(ws)-1].end.Equal(end) {
		t.Fatalf("windows cover %s – %s, want %s – %s", ws[0].start, ws[len(ws)-1].end, start, end)
	}
	for i, w := range ws {
		if w.end.Sub(w.start) > logbookWindowStep+time.Microsecond {
			t.Errorf("window %d spans %s", i, w.end.Sub(w.start))
		}
		if i > 0 && !w.start.Equal(ws[i-1].end.Add(-time.Microsecond)) {
			t.Errorf("window %d starts %s, want 1 µs before %s", i, w.start, ws[i-1].end)
		}
	}

	if got := logbookWindows(start, start.Add(90*time.Minute)); len(got) != 1 {
		t.Errorf("a 90 min window gave %d requests, want 1", len(got))
	}
	if got := logbookWindows(start, start); len(got) != 0 {
		t.Errorf("an empty window gave %d requests, want 0", len(got))
	}
}

// TestEntityChunksStayWithinBudget: 390 automation ids (the instance in #195)
// fit no single request under an 8 KiB proxy buffer. Every chunk stays within
// the budget once escaped, and together the chunks are the ids, in order.
func TestEntityChunksStayWithinBudget(t *testing.T) {
	ids := syntheticAutomationIDs(390)
	chunks := entityChunks(ids)
	if len(chunks) < 2 {
		t.Fatalf("390 ids went into %d chunk(s)", len(chunks))
	}
	var back []string
	for i, c := range chunks {
		if n := len(url.QueryEscape(strings.Join(c, ","))); n > logbookEntityChunkBytes {
			t.Errorf("chunk %d encodes to %d bytes, budget %d", i, n, logbookEntityChunkBytes)
		}
		back = append(back, c...)
	}
	if strings.Join(back, ",") != strings.Join(ids, ",") {
		t.Error("the chunks are not the ids in order")
	}

	long := strings.Repeat("x", logbookEntityChunkBytes+10)
	if got := entityChunks([]string{"a", long, "b"}); len(got) != 3 {
		t.Errorf("an id longer than the budget should stand alone; got %d chunks", len(got))
	}
}

// TestFetchFireCountsChunksAndStopsAtFirstFailure is the #195 regression on
// the request side: every request names entities, covers at most 24 h, fits
// the budget, and the first failure ends the read with nothing more sent —
// not a retry, not the next chunk.
func TestFetchFireCountsChunksAndStopsAtFirstFailure(t *testing.T) {
	ids := syntheticAutomationIDs(390)

	t.Run("every request is bounded", func(t *testing.T) {
		lr := &logbookRecorder{body: `[{"when":"2026-11-27T08:00:00Z","domain":"automation","entity_id":"` + ids[7] + `"}]`}
		client := newLogbookRecorder(t, lr)
		got, err := fetchFireCounts(context.Background(), client, 48*time.Hour, "automation", ids, true)
		if err != nil {
			t.Fatal(err)
		}
		chunks := len(entityChunks(ids))
		if want := 2 * chunks; len(lr.requests) != want {
			t.Fatalf("48 h over %d id chunks sent %d requests, want %d", chunks, len(lr.requests), want)
		}
		for _, u := range lr.requests {
			if u.Query().Get("entity") == "" {
				t.Errorf("request without entity filter: %s", u)
			}
			if n := len(url.QueryEscape(u.Query().Get("entity"))); n > logbookEntityChunkBytes {
				t.Errorf("entity value encodes to %d bytes", n)
			}
			start, end := requestWindow(t, u)
			if end.Sub(start) > logbookWindowStep+time.Microsecond {
				t.Errorf("request spans %s", end.Sub(start))
			}
		}
		// The same entry answered every request, so it is counted once per request.
		if n, ok := got.runs(ids[7]); !ok || n != len(lr.requests) {
			t.Errorf("runs = (%d, %v), want (%d, true)", n, ok, len(lr.requests))
		}
	})

	for _, status := range []int{http.StatusBadGateway, http.StatusGatewayTimeout, http.StatusInternalServerError} {
		t.Run(fmt.Sprintf("a %d ends the read", status), func(t *testing.T) {
			lr := &logbookRecorder{body: `[]`, failAt: 2, failStatus: status}
			client := newLogbookRecorder(t, lr)
			got, err := fetchFireCounts(context.Background(), client, 48*time.Hour, "automation", ids, true)
			if err == nil {
				t.Fatal("a failed request was not reported")
			}
			if got.ok {
				t.Error("a failed read counted as an answer; the traces must stand in (H-18)")
			}
			if len(lr.requests) != 2 {
				t.Errorf("sent %d requests; the read must stop at the failing one (2), "+
					"without a retry and without the next chunk", len(lr.requests))
			}
		})
	}
}

// TestFilteredLogbookSilenceIsZeroWhenNothingRan and its sibling pin D-234:
// a filtered query about automations that did not run is empty, and that is
// a zero only when the rows' own last_triggered agrees.
func TestFilteredLogbookSilenceIsZeroWhenNothingRan(t *testing.T) {
	lr := &logbookRecorder{body: `[]`}
	got, err := fetchFireCounts(context.Background(), newLogbookRecorder(t, lr), 24*time.Hour,
		"automation", []string{"automation.blocked"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if n, ok := got.runs("automation.blocked"); !ok || n != 0 {
		t.Errorf("runs = (%d, %v), want (0, true): nothing ran by last_triggered and the logbook agrees", n, ok)
	}
}

func TestFilteredLogbookSilenceFallsBackWhenARowRan(t *testing.T) {
	lr := &logbookRecorder{body: `[]`}
	got, err := fetchFireCounts(context.Background(), newLogbookRecorder(t, lr), 24*time.Hour,
		"automation", []string{"automation.ran"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got.runs("automation.ran"); ok {
		t.Error("an empty logbook counted as an answer although last_triggered says the row ran: " +
			"the logbook is not recording it, and the traces must stand in (H-18)")
	}
}

func TestTriggeredSince(t *testing.T) {
	cutoff := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	cases := map[string]bool{
		"":                                 false, // HA's null: never ran
		"2026-11-26T11:59:59.000000+00:00": false,
		"2026-11-26T12:00:01.123456+00:00": true,
		"not a time":                       true, // proves nothing, so the weaker claim
	}
	for in, want := range cases {
		if got := triggeredSince(in, cutoff); got != want {
			t.Errorf("triggeredSince(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestLogbookWindowCapRefusesBeforeAnyRequest pins D-233 on all three
// logbook listings: a window above 7 d is refused, nothing reaches HA, and
// --allow-heavy lifts the refusal.
func TestLogbookWindowCapRefusesBeforeAnyRequest(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()
		if strings.HasPrefix(r.URL.Path, "/api/states") || strings.HasPrefix(r.URL.Path, "/api/logbook/") {
			_, _ = fmt.Fprint(w, `[]`)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("HA_URL="+srv.URL+"\nHA_TOKEN=tok\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	for _, c := range logbookListings() {
		path := strings.Fields(c.CommandPath())[1:]
		t.Run(strings.Join(path, " "), func(t *testing.T) {
			mu.Lock()
			seen = nil
			mu.Unlock()
			var out bytes.Buffer
			args := append(append([]string{"hactl", "--dir", dir}, path...), "--since", "8d")
			err := RunWithOutput(args, &out)
			if err == nil || !strings.Contains(err.Error(), "--allow-heavy") {
				t.Fatalf("--since 8d was not refused with the flag's name: err=%v out=%q", err, out.String())
			}
			mu.Lock()
			sent := append([]string(nil), seen...)
			mu.Unlock()
			if len(sent) != 0 {
				t.Errorf("a refused window still sent %v", sent)
			}

			out.Reset()
			if err := RunWithOutput(append(args, "--allow-heavy"), &out); err != nil &&
				strings.Contains(err.Error(), "--allow-heavy") {
				t.Errorf("--allow-heavy did not lift the cap: %v", err)
			}
			if err := RunWithOutput(append(append([]string{"hactl", "--dir", dir}, path...), "--since", "7d"), &out); err != nil &&
				strings.Contains(err.Error(), "--allow-heavy") {
				t.Errorf("7d is inside the cap and was refused: %v", err)
			}
		})
	}
}

// TestAllowHeavyIsDeclaredOnlyOnTheLogbookListings is the flagcontract proof
// for --allow-heavy (H-25): offered by exactly the commands that read it.
func TestAllowHeavyIsDeclaredOnlyOnTheLogbookListings(t *testing.T) {
	want := map[string]bool{}
	for _, c := range logbookListings() {
		want[c.CommandPath()] = true
	}
	var walk func(c *cobra.Command)
	got := map[string]bool{}
	walk = func(c *cobra.Command) {
		if c.Flags().Lookup("allow-heavy") != nil || c.PersistentFlags().Lookup("allow-heavy") != nil {
			got[c.CommandPath()] = true
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	for p := range want {
		if !got[p] {
			t.Errorf("%s reads --allow-heavy but does not declare it", p)
		}
	}
	for p := range got {
		if !want[p] {
			t.Errorf("%s declares --allow-heavy but does not read the logbook window", p)
		}
	}
}
