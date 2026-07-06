package cmd

import (
	"testing"

	"github.com/hemm-ems/hactl/internal/manual"
)

// Every visible top-level command must be covered by the manual taxonomy —
// mapped as a family, aliased to one, or explicitly exempt — so a new command
// cannot ship without a progressive-manual decision. Fix a failure in
// internal/manual/families.go.
func TestTopLevelCommandsHaveManualCoverage(t *testing.T) {
	rootCmd.InitDefaultHelpCmd()
	rootCmd.InitDefaultCompletionCmd()
	for _, c := range rootCmd.Commands() {
		if c.Hidden {
			continue // hidden commands (cobra internals) are auto-exempt
		}
		name := c.Name()
		if _, ok := manual.FamilyFor(name); ok {
			continue
		}
		if manual.Exempt[name] {
			continue
		}
		t.Errorf("top-level command %q has no manual coverage: add it to FamilySections, Aliases, or Exempt in internal/manual/families.go", name)
	}
}
