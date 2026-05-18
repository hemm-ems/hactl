package format

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
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

	if len(result) != 2 {
		t.Fatalf("expected 2 items (top=2), got %d", len(result))
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
