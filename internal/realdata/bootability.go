package realdata

import (
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// Bootability
// ---------------------------------------------------------------------------
//
// SPEC-realdata-fixture.md §5 already says the derivative has to boot, and
// lists what has to go for it to: ssl paths that exist in nobody's container,
// integrations pointed at a network that is not there. This is the same rule
// applied to a list rather than to a file.
//
// The reference instance's automations.yaml holds fourteen entries that name
// something the fixture cannot carry — a community blueprint, an MQTT broker, a
// device that exists in one house. Home Assistant does not refuse the file for
// them; it logs an error per entry, disables that automation and carries on, so
// the failure is invisible to anything except a person reading the boot log.
// Fourteen of those errors also push the fixture's own deliberate log shapes out
// of a `system_log` capped at fifty entries, which is how a fixture defect turns
// into an unrelated case failing.
//
// So they are removed, and the removal is DECLARED: each exclusion carries the
// reason, and the capture reports how many entries each one took. A shape lost
// on purpose is debt; a shape lost quietly is the thing this whole package
// exists to stop.

// Exclusion is one reason an entry cannot be carried into the fixture.
//
// Marker is the text that identifies an unusable entry. A substring is enough
// and is deliberately blunt: the alternative is a matcher clever enough to be
// wrong in a way nobody notices, and DropTopLevelItems reports its counts so a
// marker that stops matching is visible rather than silent.
type Exclusion struct {
	Marker string
	Why    string
}

// DropTopLevelItems removes every item of a column-zero YAML list carrying an
// exclusion's marker, and reports how many items each exclusion removed.
//
// Every declared exclusion appears in the result, zero included: an exclusion
// that has stopped matching is the interesting case, because it means the
// source moved and the reason no longer describes it.
func DropTopLevelItems(src string, exclusions []Exclusion) (string, map[string]int) {
	removed := make(map[string]int, len(exclusions))
	for _, e := range exclusions {
		removed[e.Why] = 0
	}
	return rewriteTopLevelItems(src, func(item []string) []string {
		text := strings.Join(item, "\n")
		for _, e := range exclusions {
			if strings.Contains(text, e.Marker) {
				removed[e.Why]++
				return nil
			}
		}
		return item
	}), removed
}

// Inert gives every top-level item an `initial_state: false` it does not
// already have, and reports how many it changed.
//
// An automation transplanted out of the house it was written for cannot do what
// it was written to do: its actions call services that are not installed here
// and its entities mostly do not exist. Left running, the ones with a time
// trigger fire on the rig's clock and log an error every few seconds — 119
// distinct entries against a `system_log` capped at 50, which evicts the four
// deliberate log shapes rig capability R6 exists for and fails a case in an
// unrelated family. The ones whose targets DO exist are worse: they write to
// the fixture's own helpers while a case is reading them.
//
// So the captured automations are carried for their SHAPE — the file's size,
// the entry count, the trigger and action forms, the wrapped scalars — and not
// for their behaviour, which is the reference instance's and does not travel.
// The rig's own hand-authored automations still run.
func Inert(src string) (string, int) {
	changed := 0
	return rewriteTopLevelItems(src, func(item []string) []string {
		for _, line := range item {
			if strings.HasPrefix(line, "  initial_state:") {
				return item
			}
		}
		changed++
		// Before any trailing blank lines: they separate this item from the
		// next one and are not part of it.
		at := len(item)
		for at > 0 && strings.TrimSpace(item[at-1]) == "" {
			at--
		}
		out := make([]string, 0, len(item)+1)
		out = append(out, item[:at]...)
		out = append(out, "  initial_state: false")
		return append(out, item[at:]...)
	}), changed
}

// rewriteTopLevelItems applies rewrite to each item of a column-zero YAML list.
//
// An item is a `- ` at column zero and everything under it, which is the whole
// of the grammar here: automations.yaml is a list of mappings and nothing else.
// Returning nil from rewrite drops the item.
func rewriteTopLevelItems(src string, rewrite func(item []string) []string) string {
	// The final newline is put back at the end rather than carried through as an
	// empty last line, which would otherwise belong to the last item and vanish
	// with it.
	body, newline := strings.CutSuffix(src, "\n")

	var out, item []string
	started := false
	flush := func() {
		if len(item) > 0 {
			out = append(out, rewrite(item)...)
			item = nil
		}
	}
	for line := range strings.SplitSeq(body, "\n") {
		if strings.HasPrefix(line, "- ") {
			flush()
			started = true
		}
		if !started {
			out = append(out, line) // the preamble, before the first item
			continue
		}
		item = append(item, line)
	}
	flush()

	kept := strings.Join(out, "\n")
	if newline {
		kept += "\n"
	}
	return kept
}

// ExclusionReport renders the counts in a stable order, so a capture's log is a
// function of what it did rather than of Go's map iteration (H-16).
func ExclusionReport(removed map[string]int) []string {
	reasons := make([]string, 0, len(removed))
	for why := range removed {
		reasons = append(reasons, why)
	}
	sort.Strings(reasons)
	out := make([]string, 0, len(reasons))
	for _, why := range reasons {
		out = append(out, itoa(removed[why])+" dropped: "+why)
	}
	return out
}
