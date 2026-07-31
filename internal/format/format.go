package format

import (
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"strings"
	"unicode/utf8"

	"github.com/hemm-ems/hactl/internal/clock"
)

// Table holds tabular data for compact rendering.
//
// A cell is a string because a text table is made of strings. That made the
// JSON rendering below a re-use of the HUMAN rendering, which is how
// `ent ls --json` came to report `"last_changed": "06:31"`: the cell held
// clock.Short's output, and renderJSON copied the cell. Machine sets the
// machine's value for a cell whose text form is a human rendering — see
// SetMachine.
type Table struct {
	Rows    [][]string
	Headers []string
	// Machine[i][header] overrides the JSON value of row i's cell under
	// header. Text rendering never consults it. Nil for the common table
	// whose cells mean the same thing to both audiences.
	Machine []map[string]any
	// Widths[header] caps how wide that column renders AS TEXT. Nil for the
	// common table whose cells are short by nature. See SetWidth.
	Widths map[string]int
	// TimeColumns[header] says that column's cells are raw instants this type
	// renders as a clock. Nil for a table with no time column. See SetTimeColumn.
	TimeColumns map[string]bool
}

// SetWidth declares that a column is too wide to print and how wide it may be.
//
// The cap belongs here rather than at the call site, and that is the whole
// point. Every log-family renderer used to do `if len(msg) > 60 { msg =
// msg[:57] + "..." }` while ASSEMBLING the row, so the message reached this
// type already cut: `--json`, `--full` and `--tokensmax 0` could not undo it
// because by then there was nothing left to undo, and `log show <id>` was the
// only way to read a message hactl had itself received in full (finding #14).
// `ent ls --json` reported `"state": "2026-07-31T03:13:..."` for 76 entities
// on the reference instance while `ent show --json` reported the whole instant
// for the same field — the same defect one command over, which no report named.
//
// A width is a property of a COLUMN in a text table, so only renderText
// consults it: the machine contract carries the value the caller was handed
// (H-10), and `--full` lifts the cap for the reader too, which is what the
// flag's own documentation ("show full output") says it does.
func (t *Table) SetWidth(header string, runes int) {
	if t.Widths == nil {
		t.Widths = make(map[string]int, len(t.Headers))
	}
	t.Widths[header] = runes
}

// SetTimeColumn declares that a column holds raw Home Assistant timestamps,
// which this type renders — rather than the call site.
//
// It is SetWidth's argument about a different value. Every renderer used to turn
// the instant into a clock while ASSEMBLING the row, and the abbreviation
// "today means no date" is a decision that can only be made correctly once the
// whole column is known: `auto show`'s trace table printed `07-29 01:15` on four
// rows and a bare `01:15` on the fifth, because that run started today (#71).
// Nothing in the answer says so, and by the time the cells reach here the date
// is gone — there is nothing left to undo, exactly as with a message cut to 60
// characters before it arrived.
//
// The cell therefore carries the wire value and the column is rendered at
// display time, after --top has decided which rows are shown: a date that only
// appears because of a row nobody sees is a worse answer than the one it
// replaced. JSON is untouched — the cell's raw instant is already the machine
// contract (H-10), and SetMachine still wins where a site has a better one.
func (t *Table) SetTimeColumn(header string) {
	if t.TimeColumns == nil {
		t.TimeColumns = make(map[string]bool, 1)
	}
	t.TimeColumns[header] = true
}

// SetMachine records the value row i's `header` cell carries in JSON output,
// leaving its text form alone.
//
// This is the one place the two audiences are allowed to diverge, and it is
// deliberately explicit at the call site rather than inferred: a cell that
// renders "06:31" for a person and "2026-07-30T06:31:28.65+02:00" for a
// machine is a decision about that column, not a property a renderer can
// derive.
func (t *Table) SetMachine(i int, header string, v any) {
	if t.Machine == nil {
		t.Machine = make([]map[string]any, len(t.Rows))
	}
	for len(t.Machine) <= i {
		t.Machine = append(t.Machine, nil)
	}
	if t.Machine[i] == nil {
		t.Machine[i] = map[string]any{}
	}
	t.Machine[i][header] = v
}

// RenderOpts controls table output mode.
type RenderOpts struct {
	// Top caps the number of rows shown in text output. It has no effect on
	// JSON output — see JSON below — so --top never silently shortens a
	// machine-readable result.
	Top     int
	Full    bool
	JSON    bool
	Compact bool
	// MoreHint is appended to the "…+N more" line when rows are truncated.
	// Use to suggest a way to narrow output (e.g. "try --pattern foo.*").
	MoreHint string
}

// Render writes the table to w using the given options.
func (t *Table) Render(w io.Writer, opts RenderOpts) error {
	if opts.JSON {
		return t.renderJSON(w, opts)
	}
	return t.renderText(w, opts)
}

// renderTimeColumns replaces every declared time column's raw instants with a
// clock rendering that is uniform down the column.
func (t *Table) renderTimeColumns(rows [][]string) [][]string {
	if len(t.TimeColumns) == 0 {
		return rows
	}
	out := make([][]string, len(rows))
	for i, row := range rows {
		cells := make([]string, len(row))
		copy(cells, row)
		out[i] = cells
	}
	for j, h := range t.Headers {
		if !t.TimeColumns[h] {
			continue
		}
		raw := make([]string, 0, len(out))
		for _, row := range out {
			if j < len(row) {
				raw = append(raw, row[j])
			} else {
				raw = append(raw, "")
			}
		}
		rendered := clock.ShortColumn(raw)
		for i, row := range out {
			if j < len(row) {
				row[j] = rendered[i]
			}
		}
	}
	return out
}

// visibleRows returns the rows to render. Top only ever truncates text
// output: JSON is a machine contract that must never be silently short, so
// opts.JSON always yields every row regardless of Top (see H-10 in
// INVARIANTS.md — this was defect A, where `--top` silently truncated
// `--json` because this function had no JSON exemption).
func (t *Table) visibleRows(opts RenderOpts) [][]string {
	if opts.JSON || opts.Full || opts.Top <= 0 || opts.Top >= len(t.Rows) {
		return t.Rows
	}
	return t.Rows[:opts.Top]
}

func (t *Table) renderJSON(w io.Writer, opts RenderOpts) error {
	rows := t.visibleRows(opts)
	result := make([]map[string]any, len(rows))
	for i, row := range rows {
		m := make(map[string]any, len(t.Headers))
		for j, h := range t.Headers {
			if j < len(row) {
				m[h] = row[j]
			}
		}
		// A machine value replaces the cell's text form. visibleRows returns
		// every row under JSON (H-10), so i indexes t.Machine directly.
		if i < len(t.Machine) {
			maps.Copy(m, t.Machine[i])
		}
		result[i] = m
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func (t *Table) renderText(w io.Writer, opts RenderOpts) error {
	rows := t.displayRows(t.renderTimeColumns(t.visibleRows(opts)), opts.Full)
	remaining := len(t.Rows) - len(rows)

	// Runes, not bytes: fmt's `%-*s` pads to a rune count, so measuring a cell
	// in bytes over-pads every column holding a non-ASCII value. The reference
	// instance's log messages are German, and the fixture now carries one
	// (capability R6), so this is reachable on both profiles.
	widths := make([]int, len(t.Headers))
	for i, h := range t.Headers {
		widths[i] = utf8.RuneCountInString(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if n := utf8.RuneCountInString(cell); i < len(widths) && n > widths[i] {
				widths[i] = n
			}
		}
	}

	p := textPrinter{w: w, widths: widths, compact: opts.Compact}

	p.writeRow(t.Headers)
	for _, row := range rows {
		p.writeRow(row)
	}

	if remaining > 0 {
		if opts.MoreHint != "" {
			_, _ = fmt.Fprintf(w, "\u2026+%d more (%s)\n", remaining, opts.MoreHint)
		} else {
			_, _ = fmt.Fprintf(w, "\u2026+%d more\n", remaining)
		}
	}

	return nil
}

// TruncationMarker ends a cell a text table had to shorten.
//
// It is one rune, and one rune that nothing else in hactl's output produces —
// which is what lets the H-10 sweep say "this document carries a display
// truncation" as a statement about the SHAPE of a value rather than as a list
// of the columns known to truncate today. The old marker was "..." and Home
// Assistant's own messages are full of those.
const TruncationMarker = "…"

// displayRows applies the columns' text-only rendering: a cell is one line, and
// no wider than its column declared.
//
// Both halves are display rules and neither may reach renderJSON. The width is
// finding #14. The single line is the half of #14 the report did not name: the
// old cut was a LENGTH test, so a message whose first line was under 60 bytes
// passed through it untouched and put its newline in a table cell — the
// reference instance printed 58 lines for 54 rows plus a header, three of them
// split, with the continuation carrying no columns at all. A row per line is
// what makes a table a table.
func (t *Table) displayRows(rows [][]string, full bool) [][]string {
	if len(t.Widths) == 0 {
		return rows
	}
	out := make([][]string, len(rows))
	for i, row := range rows {
		cells := make([]string, len(row))
		copy(cells, row)
		for j, h := range t.Headers {
			width, capped := t.Widths[h]
			if !capped || j >= len(cells) {
				continue
			}
			cells[j] = displayCell(cells[j], width, full)
		}
		out[i] = cells
	}
	return out
}

// displayCell renders one cell of a width-capped column.
func displayCell(s string, width int, full bool) string {
	s = flattenLines(s)
	if full || width <= 1 {
		return s
	}
	return Clip(s, width)
}

// flattenLines folds a multi-line value onto the single line a table row is.
//
// The break is shown rather than swallowed: a message whose remaining lines
// have simply vanished reads as a complete short message, which is the same
// class of lie as a silent truncation. ⏎ says there was more, and `--json` and
// `log show <id>` both carry it.
func flattenLines(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return strings.TrimSpace(s)
	}
	fields := strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '\r' })
	parts := make([]string, 0, len(fields))
	for _, line := range fields {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, " ⏎ ")
}

// Clip shortens s to at most width runes, marker included.
//
// It counts RUNES. The call sites this replaced sliced bytes — `msg[:57]` —
// and the reference instance's messages are German: a two-byte character
// straddling offset 57 is cut in half, and the invalid UTF-8 that leaves
// survives all the way into `--json`, where an encoder writes it as U+FFFD.
// The rig reproduces it on demand (capability R6).
//
// Exported for the one shortening that is not a table cell: the condensed trace
// renders its own text, and a second implementation of this rule is how the
// clock surface came to hold five renderers that disagreed.
func Clip(s string, width int) string {
	if utf8.RuneCountInString(s) <= width {
		return s
	}
	runes := []rune(s)
	return strings.TrimRight(string(runes[:width-1]), " ") + TruncationMarker
}

// textPrinter handles column-aligned text output.
type textPrinter struct {
	w       io.Writer
	widths  []int
	compact bool
}

func (p *textPrinter) writeRow(cells []string) {
	sep := "  "
	if p.compact {
		sep = " "
	}
	lastCol := len(p.widths) - 1

	for i := range p.widths {
		cell := ""
		if i < len(cells) {
			cell = cells[i]
		}
		if p.compact && i == lastCol && cell == "" {
			break
		}
		if i > 0 {
			_, _ = fmt.Fprint(p.w, sep)
		}
		if p.compact && i == lastCol {
			_, _ = fmt.Fprint(p.w, cell)
		} else {
			_, _ = fmt.Fprintf(p.w, "%-*s", p.widths[i], cell)
		}
	}
	_, _ = fmt.Fprintln(p.w)
}
