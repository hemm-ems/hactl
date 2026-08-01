package format

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderText_Basic(t *testing.T) {
	tbl := &Table{
		Headers: []string{"id", "state", "count"},
		Rows: [][]string{
			{"foo", "on", "5"},
			{"bar_long", "off", "12"},
		},
	}

	var buf bytes.Buffer
	if err := tbl.Render(&buf, RenderOpts{Full: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines (header + 2 rows), got %d:\n%s", len(lines), out)
	}

	// Header should contain all column names
	if !strings.Contains(lines[0], "id") || !strings.Contains(lines[0], "state") || !strings.Contains(lines[0], "count") {
		t.Errorf("header missing columns: %q", lines[0])
	}

	// Rows should be aligned
	if !strings.Contains(lines[1], "foo") || !strings.Contains(lines[1], "on") {
		t.Errorf("row 1 unexpected: %q", lines[1])
	}
}

func TestRenderText_TopN(t *testing.T) {
	tbl := &Table{
		Headers: []string{"name"},
		Rows: [][]string{
			{"a"}, {"b"}, {"c"}, {"d"}, {"e"},
		},
	}

	var buf bytes.Buffer
	if err := tbl.Render(&buf, RenderOpts{Top: 2}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "\u2026+3 more") {
		t.Errorf("expected '…+3 more' in output, got:\n%s", out)
	}

	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// header + 2 rows + more line = 4
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines, got %d:\n%s", len(lines), out)
	}
}

func TestRenderText_TopN_WithMoreHint(t *testing.T) {
	tbl := &Table{
		Headers: []string{"name"},
		Rows:    [][]string{{"a"}, {"b"}, {"c"}},
	}

	var buf bytes.Buffer
	if err := tbl.Render(&buf, RenderOpts{Top: 1, MoreHint: "try --pattern foo.*"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "…+2 more") {
		t.Errorf("expected '…+2 more' in output, got:\n%s", out)
	}
	if !strings.Contains(out, "try --pattern foo.*") {
		t.Errorf("expected hint 'try --pattern foo.*' in output, got:\n%s", out)
	}
}

func TestRenderText_FullIgnoresTop(t *testing.T) {
	tbl := &Table{
		Headers: []string{"x"},
		Rows:    [][]string{{"1"}, {"2"}, {"3"}},
	}

	var buf bytes.Buffer
	if err := tbl.Render(&buf, RenderOpts{Top: 1, Full: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if strings.Contains(out, "more") {
		t.Errorf("full mode should not truncate, got:\n%s", out)
	}
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 lines (header + 3 rows), got %d", len(lines))
	}
}

func TestRenderText_EmptyTable(t *testing.T) {
	tbl := &Table{
		Headers: []string{"id", "name"},
		Rows:    nil,
	}

	var buf bytes.Buffer
	if err := tbl.Render(&buf, RenderOpts{Full: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected 1 line (header only), got %d:\n%s", len(lines), out)
	}
}

func TestRenderJSON(t *testing.T) {
	tbl := &Table{
		Headers: []string{"id", "state"},
		Rows: [][]string{
			{"foo", "on"},
			{"bar", "off"},
		},
	}

	var buf bytes.Buffer
	if err := tbl.Render(&buf, RenderOpts{JSON: true, Full: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result []map[string]string
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, buf.String())
	}

	if len(result) != 2 {
		t.Fatalf("expected 2 items, got %d", len(result))
	}
	if result[0]["id"] != "foo" || result[0]["state"] != "on" {
		t.Errorf("unexpected first item: %v", result[0])
	}
	if result[1]["id"] != "bar" || result[1]["state"] != "off" {
		t.Errorf("unexpected second item: %v", result[1])
	}
}

// TestRenderJSON_TopN was inverted for defect A (see INVARIANTS.md H-10):
// it used to assert that --top=2 truncated a 3-row JSON array down to 2
// elements, i.e. it asserted the silent-truncation bug as correct behavior.
// --json must never be truncated by --top, so the expectation is now the
// opposite: all 3 rows come back regardless of Top.
func TestRenderJSON_TopN(t *testing.T) {
	tbl := &Table{
		Headers: []string{"name"},
		Rows:    [][]string{{"a"}, {"b"}, {"c"}},
	}

	var buf bytes.Buffer
	if err := tbl.Render(&buf, RenderOpts{JSON: true, Top: 2}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result []map[string]string
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	// --top must never truncate --json output (defect A): a machine caller
	// reading --json has no signal that anything was cut, unlike text mode's
	// "…+N more" line.
	if len(result) != 3 {
		t.Fatalf("expected all 3 items (--top must not truncate --json), got %d", len(result))
	}
}

func TestRenderText_ColumnAlignment(t *testing.T) {
	tbl := &Table{
		Headers: []string{"short", "long_header"},
		Rows: [][]string{
			{"a", "b"},
			{"longer_value", "c"},
		},
	}

	var buf bytes.Buffer
	if err := tbl.Render(&buf, RenderOpts{Full: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	// All lines should have consistent column positions
	// The "short" column should be padded to width of "longer_value" (12)
	if !strings.HasPrefix(lines[2], "longer_value") {
		t.Errorf("expected row to start with 'longer_value', got: %q", lines[2])
	}
}

func TestRenderText_Compact(t *testing.T) {
	tbl := &Table{
		Headers: []string{"id", "state", "last_err"},
		Rows: [][]string{
			{"climate_schedule", "on", "none"},
			{"alarm_morning", "on", ""},
		},
	}

	// Normal rendering
	var normalBuf bytes.Buffer
	if err := tbl.Render(&normalBuf, RenderOpts{Full: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Compact rendering
	var compactBuf bytes.Buffer
	if err := tbl.Render(&compactBuf, RenderOpts{Full: true, Compact: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	normalOut := normalBuf.String()
	compactOut := compactBuf.String()

	// Compact should be shorter (less whitespace)
	if len(compactOut) >= len(normalOut) {
		t.Errorf("compact output (%d bytes) should be shorter than normal (%d bytes)", len(compactOut), len(normalOut))
	}

	// Compact should use 1-space separator instead of 2
	compactLines := strings.Split(strings.TrimRight(compactOut, "\n"), "\n")
	normalLines := strings.Split(strings.TrimRight(normalOut, "\n"), "\n")

	// Both should have same number of lines
	if len(compactLines) != len(normalLines) {
		t.Errorf("compact has %d lines, normal has %d", len(compactLines), len(normalLines))
	}

	// Compact last column should not have trailing spaces (when non-empty)
	for _, line := range compactLines {
		trimmed := strings.TrimRight(line, " ")
		// Only check rows with non-empty last column
		parts := strings.Fields(line)
		if len(parts) == 3 && strings.HasSuffix(line, " ") {
			t.Errorf("compact line has trailing spaces on last column: %q", line)
		}
		// But lines where last column is empty are trimmed by breaking early
		_ = trimmed
	}
}

func TestRenderText_CompactJSON(t *testing.T) {
	tbl := &Table{
		Headers: []string{"id", "state"},
		Rows:    [][]string{{"a", "on"}},
	}

	// Compact should not affect JSON output
	var buf bytes.Buffer
	if err := tbl.Render(&buf, RenderOpts{JSON: true, Full: true, Compact: true}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var result []map[string]string
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(result) != 1 || result[0]["id"] != "a" {
		t.Errorf("unexpected JSON result: %v", result)
	}
}

// --- SetWidth: the display cap belongs to the renderer, not to the caller ---
//
// Every log-family renderer used to cut its message to 60 bytes while
// ASSEMBLING the row, so the value reached this package already shortened and
// `--json`, `--full` and `--tokensmax 0` had nothing left to undo (finding
// #14). The cases below are the three properties that separates: the machine
// contract is untouched, `--full` lifts the cap for the reader too, and a cell
// is one line whatever it arrived as.

const longCell = "Shape watch probe could not reach the configured endpoint after three attempts"

func widthTable() *Table {
	tbl := &Table{
		Headers: []string{"id", "message"},
		Rows:    [][]string{{"log:a1", longCell}},
	}
	tbl.SetWidth("message", 30)
	return tbl
}

func TestSetWidth_JSONCarriesTheWholeValue(t *testing.T) {
	var buf bytes.Buffer
	if err := widthTable().Render(&buf, RenderOpts{JSON: true}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	var rows []map[string]string
	if err := json.Unmarshal(buf.Bytes(), &rows); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buf.String())
	}
	if got := rows[0]["message"]; got != longCell {
		t.Errorf("--json carries a display truncation:\n got %q\nwant %q", got, longCell)
	}
}

func TestSetWidth_TextCapsTheColumn(t *testing.T) {
	var buf bytes.Buffer
	if err := widthTable().Render(&buf, RenderOpts{}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, longCell) {
		t.Errorf("the text table printed the whole cell, so the width was not applied:\n%s", out)
	}
	if !strings.Contains(out, TruncationMarker) {
		t.Errorf("the text table shortened a cell without saying so:\n%s", out)
	}
}

// --full is documented as "show full output" and did nothing to these columns,
// which is half of what finding #14 reported.
func TestSetWidth_FullLiftsTheCap(t *testing.T) {
	var buf bytes.Buffer
	if err := widthTable().Render(&buf, RenderOpts{Full: true}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(buf.String(), longCell) {
		t.Errorf("--full did not lift the column's width cap:\n%s", buf.String())
	}
}

// The half of #14 the report did not name: the old cut was a LENGTH test, so a
// message whose first line was short passed through untouched and put its
// newline in a cell. The reference instance printed 58 lines for 54 rows plus a
// header. A row per line is what makes a table a table.
func TestSetWidth_ACellIsOneLine(t *testing.T) {
	tbl := &Table{
		Headers: []string{"id", "message"},
		Rows: [][]string{
			{"log:a1", "loader skipped 2 sources\n  alpha: unparseable\n  beta: no reader"},
			{"log:a2", "plain"},
		},
	}
	tbl.SetWidth("message", 200) // wide enough that only the newlines matter
	var buf bytes.Buffer
	if err := tbl.Render(&buf, RenderOpts{}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if want := len(tbl.Rows) + 1; len(lines) != want {
		t.Errorf("rendered %d lines for %d rows plus a header (want %d):\n%s",
			len(lines), len(tbl.Rows), want, buf.String())
	}
	if !strings.Contains(buf.String(), "⏎") {
		t.Errorf("the dropped lines vanished silently, which reads as a complete short "+
			"message:\n%s", buf.String())
	}
}

// --- Clip ---

// Clip counts runes. The call sites it replaced sliced BYTES — `msg[:57]` —
// and the reference instance's log messages are German: a two-byte character
// straddling the cut is left in half, and the invalid UTF-8 survives into
// `--json`, where the encoder writes U+FFFD. The rig reproduces it on demand
// (capability R6).
func TestClip_NeverCutsARuneInHalf(t *testing.T) {
	// 56 ASCII characters then "ü", whose two bytes sit at offsets 56 and 57 —
	// exactly where the old `[:57]` landed.
	s := "Shape watch probe rejected a reading from sensor number " + "über dem zulässigen Bereich"
	for width := 1; width <= len([]rune(s)); width++ {
		got := Clip(s, width)
		if !utf8.ValidString(got) {
			t.Fatalf("Clip(%d) produced invalid UTF-8: %q", width, got)
		}
		if n := utf8.RuneCountInString(got); n > width {
			t.Errorf("Clip(%d) returned %d runes: %q", width, n, got)
		}
	}
}

func TestClip_ShortValueIsUntouched(t *testing.T) {
	if got := Clip("on", 20); got != "on" {
		t.Errorf("Clip shortened a value that fits: %q", got)
	}
}
