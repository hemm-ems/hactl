package cmd

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"github.com/hemm-ems/hactl/internal/config"
	"github.com/hemm-ems/hactl/internal/haapi"
)

// What `executed` cannot say (D-131, D-132; live test F-14).
//
// Home Assistant runs a script step aimed at an entity that does not exist,
// does nothing, answers the call 200 and stores a trace without an error. The
// one place it says so is its system log, as a WARNING that does not name the
// script. `script run --confirm` used to answer `executed` and stop there, so a
// script whose every target was gone read as one that had worked.
//
// The log is read once, after the call. Two things about it decide what the
// run can claim, both observed rather than assumed:
//
//   - A line is "this run's" only by time, and the time is HA's: the earliest
//     last_updated on the script's own states in the call's answer. The local
//     clock would compare two machines' clocks; the trace is stored when the
//     run ends, so the newest trace can belong to an earlier run.
//   - system_log keeps ONE entry per call site, and every missing reference is
//     logged from one, so all such lines share one time — the newest — and up
//     to five sentences, with a repeat moving nothing. An entry holding one
//     sentence names the line; an entry holding several says only that one of
//     them was logged, and the warning says exactly that much.

// missingTargetLogger is the logger HA's missing-reference warning comes from.
const missingTargetLogger = "homeassistant.helpers.service"

// missingTargetSentence is the observed frame of that warning. The kind
// ("entities") is left open: other kinds (devices, areas) were not observed,
// and a line in the same frame is passed through as HA wrote it rather than
// claimed to be understood.
var missingTargetSentence = regexp.MustCompile(`^Referenced .+ are missing or not currently available$`)

// scriptRunLogWarnings reads HA's system log once and returns the warnings a
// confirmed run of entityID adds, without the "warning: " prefix. changed is
// HA's answer to the call. A log that cannot be read is a warning too, never
// an empty answer (H-7).
func scriptRunLogWarnings(ctx context.Context, cfg *config.Config, entityID string, changed []haapi.ServiceStateChange) []string {
	start, ok := scriptRunStart(changed, entityID)
	if !ok {
		return []string{fmt.Sprintf("could not check Home Assistant's log for missing targets: "+
			"its answer to the call carries no state of %s to time the run by", entityID)}
	}

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		return []string{"could not read Home Assistant's log to check for missing targets: " + err.Error()}
	}
	defer func() { _ = ws.Close() }()
	entries, err := ws.SystemLogList(ctx)
	if err != nil {
		return []string{"could not read Home Assistant's log to check for missing targets: " + err.Error()}
	}
	return missingTargetWarnings(entries, start)
}

// scriptRunStart is when the run began on HA's clock: the earliest
// last_updated among the script's own states in the call's answer. An answer
// without one has no start, and saying so beats measuring from a guess.
func scriptRunStart(changed []haapi.ServiceStateChange, entityID string) (time.Time, bool) {
	var start time.Time
	for _, c := range changed {
		if c.EntityID != entityID {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, c.LastUpdated)
		if err != nil {
			continue
		}
		if start.IsZero() || at.Before(start) {
			start = at
		}
	}
	return start, !start.IsZero()
}

// missingTargetWarnings picks the missing-reference lines HA logged at or
// after start, in log order.
func missingTargetWarnings(entries []haapi.SystemLogEntry, start time.Time) []string {
	var warnings []string
	for _, e := range entries {
		if e.Name != missingTargetLogger || e.Level != "WARNING" {
			continue
		}
		// system_log stamps an entry with its newest record, as float seconds
		// since the epoch on HA's clock. Rounded to the microsecond — the
		// precision of HA's ISO stamps the start comes from — so a float that
		// lands a hair under the same instant is not read as earlier.
		logged := time.UnixMicro(int64(math.Round(e.Timestamp * 1e6)))
		if logged.Before(start) {
			continue
		}
		var lines []string
		for _, m := range e.Message {
			if missingTargetSentence.MatchString(m) {
				lines = append(lines, m)
			}
		}
		switch {
		case len(lines) == 0:
		case len(e.Message) == 1:
			warnings = append(warnings, "Home Assistant logged during this run: "+lines[0])
		default:
			// The entry's time belongs to one of its sentences, and nothing in
			// the entry says which (D-132).
			warnings = append(warnings, "Home Assistant logged during this run one of these lines, "+
				"and its log does not say which: "+strings.Join(lines, "; "))
		}
	}
	return warnings
}
