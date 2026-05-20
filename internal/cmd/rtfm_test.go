package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/docs"
)

// TestRtfm_DefaultPrintsFullManual verifies that hactl rtfm without an explicit
// --tokensmax flag outputs the entire manual (not the 500-token default cap).
func TestRtfm_DefaultPrintsFullManual(t *testing.T) {
	var buf bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "rtfm"}, &buf); err != nil {
		t.Fatalf("rtfm failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "Command Reference") {
		t.Errorf("rtfm output missing 'Command Reference' — output may be truncated")
	}
	// Output includes the [~N tok] header so it will be slightly larger than the raw manual.
	// If the cap is still applied, output will be ~2000 bytes instead of ~18kB.
	if buf.Len() < len(docs.Manual) {
		t.Errorf("rtfm output (%d bytes) is smaller than manual (%d bytes) — token cap still applied",
			buf.Len(), len(docs.Manual))
	}
}

// TestRtfm_ExplicitCapTruncates verifies that an explicit --tokensmax still works.
func TestRtfm_ExplicitCapTruncates(t *testing.T) {
	var buf bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "rtfm", "--tokensmax=50"}, &buf); err != nil {
		t.Fatalf("rtfm --tokensmax=50 failed: %v", err)
	}
	if !strings.Contains(buf.String(), "capped at 50 tok") {
		t.Errorf("expected truncation hint at 50 tok, got: %q", buf.String()[:min(buf.Len(), 300)])
	}
}
