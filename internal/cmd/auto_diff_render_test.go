package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
)

// diffMarkers counts the +/- change lines in a rendered diff.
func diffMarkers(s string) (plus, minus int) {
	for l := range strings.SplitSeq(s, "\n") {
		switch {
		case strings.HasPrefix(l, "+"):
			plus++
		case strings.HasPrefix(l, "-"):
			minus++
		}
	}
	return plus, minus
}

// TestRenderAutoDiff_ChangeVisibleUnderDefaultCap is the regression for #69: a
// one-line change in a document large enough to overflow the default token cap
// must still render its +/- lines. Against a full-document echo the change
// lands past the truncation point and this test fails; compact hunk rendering
// keeps it near the top.
func TestRenderAutoDiff_ChangeVisibleUnderDefaultCap(t *testing.T) {
	flagTokensMax = 500
	flagJSON = false
	flagTokens = false
	defer func() { flagTokensMax = 500; flagJSON = false; flagTokens = false }()

	// Go's YAML marshal sorts map keys alphabetically, so a changed trigger
	// time lands near the end of the normalized document. Simulate that: a long
	// run of unchanged lines, then a single -/+ change at the very end.
	lines := make([]string, 0, 402)
	for i := range 400 {
		lines = append(lines, fmt.Sprintf(" some_unchanged_config_line_%03d: a fairly long value to eat tokens", i))
	}
	lines = append(lines,
		"-    - at: input_datetime.aufstehzeit_wochentag",
		"+    - at: input_datetime.aufstehzeit_wochenende",
	)

	var rendered bytes.Buffer
	renderAutoDiff(&rendered, lines)

	// Apply the same default token policy the CLI applies to captured output.
	var out bytes.Buffer
	applyTokenPolicy(&out, rendered.Bytes(), "hactl auto diff")
	got := out.String()

	if !strings.Contains(got, "+    - at: input_datetime.aufstehzeit_wochenende") {
		t.Errorf("changed + line truncated away under default token cap:\n%s", got)
	}
	if !strings.Contains(got, "-    - at: input_datetime.aufstehzeit_wochentag") {
		t.Errorf("changed - line truncated away under default token cap:\n%s", got)
	}
	if !strings.Contains(got, "unchanged lines") {
		t.Errorf("expected a collapsed-run marker in compact output, got:\n%s", got)
	}
}

func TestCompactDiff_CollapsesUnchangedRunsWithBoundedContext(t *testing.T) {
	var lines []string
	for i := range 10 {
		lines = append(lines, fmt.Sprintf(" line%02d", i))
	}
	lines = append(lines, "-line10", "+line10new")
	for i := 11; i < 20; i++ {
		lines = append(lines, fmt.Sprintf(" line%02d", i))
	}

	out := compactDiff(lines)

	// The change must survive.
	plus, minus := diffMarkers(strings.Join(out, "\n"))
	if plus != 1 || minus != 1 {
		t.Fatalf("want one +/- pair, got +%d -%d in %v", plus, minus, out)
	}

	// At most compactDiffContext (3) unchanged context lines flank the hunk.
	var leading, trailing int
	for _, l := range out {
		if strings.HasPrefix(l, "-") || strings.HasPrefix(l, "+") {
			break
		}
		if strings.HasPrefix(l, " ") {
			leading++
		}
	}
	for i := len(out) - 1; i >= 0; i-- {
		if strings.HasPrefix(out[i], "-") || strings.HasPrefix(out[i], "+") {
			break
		}
		if strings.HasPrefix(out[i], " ") {
			trailing++
		}
	}
	if leading > compactDiffContext {
		t.Errorf("want at most %d leading context lines, got %d in %v", compactDiffContext, leading, out)
	}
	if trailing > compactDiffContext {
		t.Errorf("want at most %d trailing context lines, got %d in %v", compactDiffContext, trailing, out)
	}

	// The long unchanged runs collapse to markers naming the collapsed count.
	joined := strings.Join(out, "\n")
	if !strings.Contains(joined, "unchanged lines") {
		t.Fatalf("expected collapse markers, got %v", out)
	}
	// 21 lines total (20 context + 1 inserted). Leading run: lines 0..9 minus 3
	// kept = 7 collapsed. Trailing run: lines 11..19 minus 3 kept = 6 collapsed.
	if !strings.Contains(joined, "… 7 unchanged lines …") {
		t.Errorf("expected '… 7 unchanged lines …' leading marker, got %v", out)
	}
	if !strings.Contains(joined, "… 6 unchanged lines …") {
		t.Errorf("expected '… 6 unchanged lines …' trailing marker, got %v", out)
	}
}

func TestCompactDiff_MergesNearbyHunks(t *testing.T) {
	// Two changes only 4 unchanged lines apart: their ±3 context windows overlap,
	// so nothing between them collapses — one merged hunk, no interior marker.
	lines := []string{
		" a0", " a1", " a2", " a3", " a4", " a5",
		"-changed_first",
		" b0", " b1", " b2", " b3",
		"-changed_second",
		" c0", " c1", " c2", " c3", " c4", " c5",
	}
	out := compactDiff(lines)
	joined := strings.Join(out, "\n")

	// Both changes present.
	if !strings.Contains(joined, "-changed_first") || !strings.Contains(joined, "-changed_second") {
		t.Fatalf("both changes must survive, got %v", out)
	}
	// No collapse marker between the two hunks (the b-lines stay as context).
	betweenIdx0 := strings.Index(joined, "changed_first")
	betweenIdx1 := strings.Index(joined, "changed_second")
	between := joined[betweenIdx0:betweenIdx1]
	if strings.Contains(between, "unchanged lines") {
		t.Errorf("adjacent hunks should merge without an interior marker, got between:\n%s", between)
	}
	for _, want := range []string{" b0", " b1", " b2", " b3"} {
		if !strings.Contains(between, want) {
			t.Errorf("expected merged context %q between hunks, got:\n%s", want, between)
		}
	}
}

func TestCompactDiff_NoChangesReturnedUnchanged(t *testing.T) {
	lines := []string{" a", " b", " c", " d", " e"}
	out := compactDiff(lines)
	if len(out) != len(lines) {
		t.Fatalf("no-change diff must pass through unchanged, got %v", out)
	}
	for i := range lines {
		if out[i] != lines[i] {
			t.Errorf("line %d changed: got %q want %q", i, out[i], lines[i])
		}
	}
	if strings.Contains(strings.Join(out, "\n"), "unchanged lines") {
		t.Errorf("no-change diff must not emit a collapse marker, got %v", out)
	}
}
