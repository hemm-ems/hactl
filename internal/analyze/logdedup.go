package analyze

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"sort"
	"strings"
	"time"
)

// LogEntry is a single parsed log line from HA error_log.
//
// Count is the number of occurrences this entry represents. HA's
// system_log/list WS command pre-aggregates identical messages into one
// record with a count field (haapi.SystemLogEntry.Count); callers that build
// LogEntry from that source must carry it through here. The REST
// /api/error_log fallback (ParseLogLines) has no such aggregation — every
// parsed line is exactly 1 occurrence.
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Component string `json:"component"`
	Message   string `json:"message"`
	Raw       string `json:"raw,omitempty"`
	Count     int    `json:"count"`
}

// DedupedLog is a group of identical log messages.
type DedupedLog struct {
	Hash      string     `json:"hash"`
	Level     string     `json:"level"`
	Component string     `json:"component"`
	Message   string     `json:"message"`
	FirstSeen string     `json:"first_seen"`
	LastSeen  string     `json:"last_seen"`
	Entries   []LogEntry `json:"-"`
	Count     int        `json:"count"`
}

// linePattern matches typical HA log lines: "2026-04-16 09:42:00.123 ERROR (MainThread) [component.name] message"
var linePattern = regexp.MustCompile(
	`^(\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}(?:\.\d+)?)\s+(ERROR|WARNING|INFO|DEBUG)\s+\((\w+)\)\s+\[([^\]]+)\]\s+(.*)$`,
)

// ParseLogLines parses the HA error_log text into structured entries.
func ParseLogLines(logText string) []LogEntry {
	lines := strings.Split(logText, "\n")
	entries := make([]LogEntry, 0, len(lines))

	var current *LogEntry
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}

		matches := linePattern.FindStringSubmatch(line)
		if matches != nil {
			if current != nil {
				entries = append(entries, *current)
			}
			current = &LogEntry{
				Timestamp: matches[1],
				Level:     matches[2],
				Component: matches[4],
				Message:   matches[5],
				Raw:       line,
				Count:     1,
			}
		} else if current != nil {
			// Continuation line (e.g. stack trace)
			current.Message += "\n" + line
			current.Raw += "\n" + line
		}
	}
	if current != nil {
		entries = append(entries, *current)
	}

	return entries
}

// DeduplicateLogs groups identical log messages by hash, summing occurrences.
//
// A LogEntry's own Count (defect #2) is summed into the group, not the number
// of LogEntry records merged — HA's system_log/list already pre-aggregates,
// so one LogEntry can itself represent many occurrences. Treating every
// LogEntry as exactly one occurrence silently discarded HA's own counts and
// made the manual's "sorted by count" promise wrong: genuinely-repeating
// failures sorted to the bottom instead of the top.
func DeduplicateLogs(entries []LogEntry) []DedupedLog {
	groups := make(map[string]*DedupedLog)
	order := make([]string, 0)

	for _, e := range entries {
		c := e.Count
		if c <= 0 {
			c = 1
		}
		h := hashLogMessage(e)
		if g, ok := groups[h]; ok {
			g.Count += c
			g.Entries = append(g.Entries, e)
			if e.Timestamp > g.LastSeen {
				g.LastSeen = e.Timestamp
			}
			if e.Timestamp < g.FirstSeen {
				g.FirstSeen = e.Timestamp
			}
		} else {
			groups[h] = &DedupedLog{
				Hash:      h,
				Level:     e.Level,
				Component: e.Component,
				Message:   e.Message,
				FirstSeen: e.Timestamp,
				LastSeen:  e.Timestamp,
				Count:     c,
				Entries:   []LogEntry{e},
			}
			order = append(order, h)
		}
	}

	result := make([]DedupedLog, 0, len(groups))
	for _, h := range order {
		result = append(result, *groups[h])
	}

	// Sort by count descending
	sort.Slice(result, func(i, j int) bool {
		return result[i].Count > result[j].Count
	})

	return result
}

// FilterByLevel filters entries to only include the given level (e.g. "ERROR").
func FilterByLevel(entries []LogEntry, level string) []LogEntry {
	result := make([]LogEntry, 0, len(entries))
	upper := strings.ToUpper(level)
	for _, e := range entries {
		if e.Level == upper {
			result = append(result, e)
		}
	}
	return result
}

// FilterByLevels filters entries to those matching any of the given levels
// (e.g. "ERROR", "WARNING"). With no levels it returns entries unchanged.
func FilterByLevels(entries []LogEntry, levels ...string) []LogEntry {
	if len(levels) == 0 {
		return entries
	}
	want := make(map[string]bool, len(levels))
	for _, l := range levels {
		want[strings.ToUpper(l)] = true
	}
	result := make([]LogEntry, 0, len(entries))
	for _, e := range entries {
		if want[e.Level] {
			result = append(result, e)
		}
	}
	return result
}

// FilterByComponent filters entries to only include the given component prefix.
func FilterByComponent(entries []LogEntry, component string) []LogEntry {
	result := make([]LogEntry, 0, len(entries))
	lower := strings.ToLower(component)
	for _, e := range entries {
		if strings.Contains(strings.ToLower(e.Component), lower) {
			result = append(result, e)
		}
	}
	return result
}

// hashLogMessage creates a stable hash from the message template (without timestamps and variable data).
func hashLogMessage(e LogEntry) string {
	// Normalize: strip numbers/timestamps from message, keep structure
	normalized := normalizeMessage(e.Component + "|" + e.Level + "|" + e.Message)
	h := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(h[:8])
}

// numberPattern matches timestamps, IDs, and other variable numbers in log messages.
var numberPattern = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}[.\d]*|\b\d+\.\d+\.\d+\.\d+\b|\b[0-9a-f]{8,}\b|\b\d+\b`)

func normalizeMessage(msg string) string {
	return numberPattern.ReplaceAllString(msg, "<N>")
}

// FormatShortTimestamp formats a log timestamp to short form.
func FormatShortTimestamp(ts string) string {
	if ts == "" {
		return "-"
	}
	// Parse "2026-04-16 09:42:00.123" format
	layouts := []string{
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
		time.RFC3339Nano,
		time.RFC3339,
	}
	for _, layout := range layouts {
		t, err := time.Parse(layout, ts)
		if err != nil {
			continue
		}
		now := time.Now()
		if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
			return t.Format("15:04")
		}
		return t.Format("01-02 15:04")
	}
	return ts
}

// Log timestamps come in two shapes. HA's system_log entries and the REST
// error_log carry no zone at all ("2026-07-23 15:04:05.123"); anything already
// RFC3339 carries its own.
var (
	naiveLogLayouts = []string{
		"2006-01-02 15:04:05.999999",
		"2006-01-02 15:04:05",
	}
	zonedLogLayouts = []string{
		time.RFC3339Nano,
		time.RFC3339,
	}
)

// naiveLogLayout is the layout used to render a cutoff back into the zone-less
// wall-clock form the zone-less entries are compared in.
const naiveLogLayout = "2006-01-02 15:04:05.999999"

// FilterSince keeps entries at or after cutoff, preserving order.
//
// HA's system log is a fixed-size in-memory buffer with no server-side time
// window, which is why `--since` used to be accepted and then ignored. Every
// entry carries a timestamp, so the window is answerable here instead.
//
// A zone-less entry is compared as a wall-clock reading, not as an instant:
// those strings were written by this process from a local time, so rendering
// the cutoff through the same layout puts both sides in the same frame without
// either needing to name a zone.
//
// An entry whose timestamp matches no layout is KEPT. Dropping records we
// cannot place in time would trade one silent loss for another, and the whole
// point of this command is that nothing goes missing quietly.
func FilterSince(entries []LogEntry, cutoff time.Time) []LogEntry {
	wallCutoff, wallErr := time.Parse(naiveLogLayout, cutoff.Format(naiveLogLayout))

	result := make([]LogEntry, 0, len(entries))
	for _, e := range entries {
		keep := true
		if t, ok := parseNaiveLogTimestamp(e.Timestamp); ok {
			keep = wallErr != nil || !t.Before(wallCutoff)
		} else if t, ok := parseZonedLogTimestamp(e.Timestamp); ok {
			keep = !t.Before(cutoff)
		}
		if keep {
			result = append(result, e)
		}
	}
	return result
}

func parseNaiveLogTimestamp(ts string) (time.Time, bool) {
	for _, layout := range naiveLogLayouts {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func parseZonedLogTimestamp(ts string) (time.Time, bool) {
	for _, layout := range zonedLogLayouts {
		if t, err := time.Parse(layout, ts); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}
