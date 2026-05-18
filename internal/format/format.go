package format

import (
	"encoding/json"
	"fmt"
	"io"
)

// Table holds tabular data for compact rendering.
type Table struct {
	Rows    [][]string
	Headers []string
}

// RenderOpts controls table output mode.
type RenderOpts struct {
	Top     int
	Full    bool
	JSON    bool
	Compact bool
}

// Render writes the table to w using the given options.
func (t *Table) Render(w io.Writer, opts RenderOpts) error {
	if opts.JSON {
		return t.renderJSON(w, opts)
	}
	return t.renderText(w, opts)
}

func (t *Table) visibleRows(opts RenderOpts) [][]string {
	if opts.Full || opts.Top <= 0 || opts.Top >= len(t.Rows) {
		return t.Rows
	}
	return t.Rows[:opts.Top]
}

func (t *Table) renderJSON(w io.Writer, opts RenderOpts) error {
	rows := t.visibleRows(opts)
	result := make([]map[string]string, len(rows))
	for i, row := range rows {
		m := make(map[string]string, len(t.Headers))
		for j, h := range t.Headers {
			if j < len(row) {
				m[h] = row[j]
			}
		}
		result[i] = m
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

func (t *Table) renderText(w io.Writer, opts RenderOpts) error {
	rows := t.visibleRows(opts)
	remaining := len(t.Rows) - len(rows)

	widths := make([]int, len(t.Headers))
	for i, h := range t.Headers {
		widths[i] = len(h)
	}
	for _, row := range rows {
		for i, cell := range row {
			if i < len(widths) && len(cell) > widths[i] {
				widths[i] = len(cell)
			}
		}
	}

	p := textPrinter{w: w, widths: widths, compact: opts.Compact}

	p.writeRow(t.Headers)
	for _, row := range rows {
		p.writeRow(row)
	}

	if remaining > 0 {
		_, _ = fmt.Fprintf(w, "\u2026+%d more\n", remaining)
	}

	return nil
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
