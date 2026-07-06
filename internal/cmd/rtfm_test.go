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
	// If the cap is still applied, output will be ~2000 bytes instead of ~18kB.
	if buf.Len() < len(docs.Manual) {
		t.Errorf("rtfm output (%d bytes) is smaller than manual (%d bytes) — token cap still applied",
			buf.Len(), len(docs.Manual))
	}
}

// TestRtfm_CorePrintsCoreOnly verifies --core prints routing/conventions but no
// per-family reference sections.
func TestRtfm_CorePrintsCoreOnly(t *testing.T) {
	var buf bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "rtfm", "--core"}, &buf); err != nil {
		t.Fatalf("rtfm --core failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "## Quick routing") {
		t.Error("--core output missing '## Quick routing'")
	}
	if strings.Contains(out, "### Automations") {
		t.Error("--core output leaks family section '### Automations'")
	}
}

// TestRtfm_FamilyPrintsFamilySections verifies --family is alias-aware and
// prints only that family's sections.
func TestRtfm_FamilyPrintsFamilySections(t *testing.T) {
	var buf bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "rtfm", "--family", "trace"}, &buf); err != nil {
		t.Fatalf("rtfm --family trace failed: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "### Write path (automations)") {
		t.Error("--family trace (alias of auto) missing automation write path")
	}
	if strings.Contains(out, "## Quick routing") {
		t.Error("--family output leaks core section")
	}

	var errBuf bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "rtfm", "--family", "nosuch"}, &errBuf); err == nil {
		t.Error("unknown family should error")
	} else if !strings.Contains(err.Error(), "auto") {
		t.Errorf("unknown-family error should list valid families: %v", err)
	}
}

// TestRtfm_FamiliesListsSplit verifies the --families overview.
func TestRtfm_FamiliesListsSplit(t *testing.T) {
	var buf bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "rtfm", "--families"}, &buf); err != nil {
		t.Fatalf("rtfm --families failed: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"core", "auto", "aliases: rollback, trace", "tok"} {
		if !strings.Contains(out, want) {
			t.Errorf("--families output missing %q:\n%s", want, out)
		}
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
