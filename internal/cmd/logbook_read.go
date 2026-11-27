package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/degeneracy"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// A logbook read is small by construction, and it is sent once (D-232).
//
// Home Assistant builds a logbook answer in full inside a recorder executor
// job, and a client that gives up does not cancel that job: aiohttp cancels
// the handler coroutine, the run_in_executor future keeps running to the end
// (HA 2026.11.2 components/logbook/rest_api.py, recorder/core.py). The
// WebSocket `logbook/get_events` runs the same job. So hactl's timeout bounds
// how long hactl waits, never how much HA does: `auto ls --since 30d` asked
// for every state row of every entity in thirty days, gave up after 30 s, and
// the instance ran out of memory three minutes later (#195). The only bound a
// client has is the size of each request.
const (
	// logbookWindowStep is the longest span one logbook request covers.
	logbookWindowStep = 24 * time.Hour

	// logbookEntityChunkBytes bounds the encoded `entity=` value of one
	// request. HA itself accepts request lines up to 24570 bytes
	// (components/http/server.py MAX_LINE_SIZE), but a reverse proxy in front
	// of it commonly allows 8 KiB for the whole header block. 4 KiB is roughly
	// a hundred automation ids, and it keeps the SQL bind lists small.
	logbookEntityChunkBytes = 4096

	// logbookWindowCap is the longest --since a logbook listing accepts
	// without --allow-heavy (D-233).
	logbookWindowCap = 7 * 24 * time.Hour
)

// flagAllowHeavy lifts logbookWindowCap.
var flagAllowHeavy bool

// logbookListings is every command that reads a logbook window of more than
// one entity, which is also every command that declares --allow-heavy (H-25).
func logbookListings() []*cobra.Command {
	return []*cobra.Command{autoLsCmd, changesCmd, scriptLsCmd}
}

func init() {
	for _, c := range logbookListings() {
		c.Flags().BoolVar(&flagAllowHeavy, "allow-heavy", false,
			"accept a --since above 7d (every day is one more logbook query Home Assistant runs to completion)")
	}
}

// checkLogbookWindow refuses a window above the cap before anything is sent.
func checkLogbookWindow(since time.Duration, spelled string) error {
	if since <= logbookWindowCap || flagAllowHeavy {
		return nil
	}
	return fmt.Errorf("--since %s is longer than 7d: every day is one more logbook query that Home Assistant "+
		"runs to completion, even after hactl stops waiting (#195). Narrow --since, or pass --allow-heavy to "+
		"read it anyway", spelled)
}

// timeWindow is one logbook request's span. HA's bounds are strict on both
// ends (components/logbook/queries/common.py: `time_fired_ts > start_day`
// and `< end_day`).
type timeWindow struct {
	start, end time.Time
}

// logbookWindows splits (start, end) into spans of at most logbookWindowStep,
// in order.
//
// Every span after the first starts one microsecond before the previous one
// ended. HA excludes both bounds, so back-to-back spans would lose an event
// stamped exactly on a boundary. Recorder timestamps have microsecond
// resolution and the boundaries are whole seconds, so the overlap contains
// no instant HA can record, and no event is counted twice.
func logbookWindows(start, end time.Time) []timeWindow {
	var out []timeWindow
	for s := start; s.Before(end); s = s.Add(logbookWindowStep) {
		e := s.Add(logbookWindowStep)
		if e.After(end) {
			e = end
		}
		ws := s
		if len(out) > 0 {
			ws = s.Add(-time.Microsecond)
		}
		out = append(out, timeWindow{start: ws, end: e})
	}
	return out
}

// entityChunks groups ids, in order, so that each group's comma-joined and
// query-escaped value stays within logbookEntityChunkBytes. An id longer than
// that on
// its own still gets a group of its own: it has to be asked about somehow.
func entityChunks(ids []string) [][]string {
	var out [][]string
	cur := make([]string, 0, len(ids))
	size := 0
	for _, id := range ids {
		n := len(url.QueryEscape(id))
		if len(cur) > 0 {
			n += len(url.QueryEscape(","))
		}
		if len(cur) > 0 && size+n > logbookEntityChunkBytes {
			out = append(out, cur)
			cur, size = nil, 0
			n = len(url.QueryEscape(id))
		}
		cur = append(cur, id)
		size += n
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

// readLogbook sends the bounded requests for (now-since, now) about
// entityIDs (nil = every entity, for `changes`), one after another, and
// hands each request's entries to visit. The first failure ends the read:
// nothing more is sent to a server that may still be working on the request
// that failed.
func readLogbook(ctx context.Context, client *haapi.Client, since time.Duration, entityIDs []string, visit func([]logbookEntry)) error {
	now := time.Now().Truncate(time.Second)
	chunks := [][]string{nil}
	if entityIDs != nil {
		chunks = entityChunks(entityIDs)
	}
	for _, w := range logbookWindows(now.Add(-since), now) {
		for _, chunk := range chunks {
			data, err := client.GetLogbookOnce(ctx,
				w.start.Format(time.RFC3339Nano),
				w.end.Format(time.RFC3339Nano),
				strings.Join(chunk, ","))
			if err != nil {
				return fmt.Errorf("fetching logbook (%s – %s): %w",
					w.start.Format(time.RFC3339), w.end.Format(time.RFC3339), err)
			}
			var entries []logbookEntry
			if err := json.Unmarshal(data, &entries); err != nil {
				return fmt.Errorf("parsing logbook: %w", err)
			}
			if err := degeneracy.Check("/api/logbook", &entries); err != nil {
				return err
			}
			visit(entries)
		}
	}
	return nil
}

// fetchFireCounts counts the runs the logbook recorded for entityIDs, all of
// one domain HA logs one entry per run for, over the window.
//
// ranInWindow says whether any of those rows has a `last_triggered` inside the
// window. It decides what an empty answer means (D-234, H-18): when nothing
// ran by its own last_triggered, the logbook's silence and the attribute agree
// on zero; when something did run and the logbook still holds no entry of the
// domain, the logbook is not recording it, and the traces stand in.
func fetchFireCounts(ctx context.Context, client *haapi.Client, since time.Duration, domain string, entityIDs []string, ranInWindow bool) (fireCounts, error) {
	counts := make(map[string]int)
	if len(entityIDs) == 0 {
		return fireCounts{byEntityID: counts, ok: true}, nil
	}
	err := readLogbook(ctx, client, since, entityIDs, func(entries []logbookEntry) {
		for _, e := range entries {
			if e.Domain == domain && e.EntityID != "" {
				counts[e.EntityID]++
			}
		}
	})
	if err != nil {
		return fireCounts{}, err
	}
	return fireCounts{byEntityID: counts, ok: len(counts) > 0 || !ranInWindow}, nil
}

// triggeredSince reports whether a `last_triggered` attribute lies after
// cutoff. HA writes null for an item that never ran, so an empty value is
// "not inside the window". A value hactl cannot parse proves nothing either
// way, so it counts as "ran": the weaker claim, which leaves an empty logbook
// answer to the traces rather than turning it into a zero.
func triggeredSince(lastTriggered string, cutoff time.Time) bool {
	if lastTriggered == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339Nano, lastTriggered)
	if err != nil {
		return true
	}
	return t.After(cutoff)
}
