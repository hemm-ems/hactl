//go:build p2a_stub

package cmd

import (
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
)

// policyCommandCensus — CONTRACT STUB (`-tags p2a_stub`) so the p2a_contract
// gate compiles and can be watched to fail. The integrator replaces it with
// the derived census: one Site per write command, keyed by its cobra command
// path ("hactl area create"), File "cobra tree" — every --confirm command, every
// entrypoint reaching a mapped write request or a system:cache write without
// one, plus mcp and setup (D-80, R-17). Never a hand-written list.
func policyCommandCensus(t *testing.T) []surfaceaudit.Site { //nolint:unparam // contract stub, replaced by the derived census
	t.Helper()
	return nil
}
