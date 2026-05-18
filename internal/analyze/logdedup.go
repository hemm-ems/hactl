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
type LogEntry struct {
	Timestamp string `json:"timestamp"`
	Level     string `json:"level"`
	Component string `json:"component"`
	Message   string `json:"message"`
	Raw       string `json:"raw,omitempty"`
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

// DeduplicateLogs groups identical log messages by hash, counting occurrences.
func DeduplicateLogs(entries []LogEntry) []DedupedLog {
	groups := make(map[string]*DedupedLog)
	order := make([]string, 0)

	for _, e := range entries {
		h := hashLogMessage(e)
		if g, ok := groups[h]; ok {
			g.Count++
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
				Count:     1,
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
