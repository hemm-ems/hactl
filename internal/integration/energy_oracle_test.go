//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// TestOracleEnergyGetPrefsUnconfigured — the fork `energy show` (issue #114)
// rests on: what does energy/get_prefs answer on an instance whose Energy
// dashboard was never set up? The rig loads default_config (so the energy
// integration and its WS command exist) and nothing has saved prefs. A WS
// error here means the command maps it to an honest "no energy configuration"
// message; empty prefs would instead mean rendering an empty document, and
// the two must not be conflated (H-7: an unavailable answer must not read as
// a real, empty one).
func TestOracleEnergyGetPrefsUnconfigured(t *testing.T) {
	ws := haapi.NewWSClient(ha.URL(), ha.Token())
	if err := ws.Connect(context.Background()); err != nil {
		t.Fatalf("ws connect: %v", err)
	}
	defer func() { _ = ws.Close() }()

	prefs, err := ws.EnergyGetPrefs(context.Background())
	if err == nil {
		t.Fatalf("energy/get_prefs on an unconfigured instance answered %+v — "+
			"the command must render empty prefs honestly instead of mapping an error", prefs)
	}
	t.Logf("unconfigured error shape: %v", err)
	// Observed 2026-07-30 (HA stable): `energy/get_prefs failed: No prefs`.
	// The command keys its friendly message on this marker; if HA rewords it,
	// this oracle names the drift before a user sees a raw WS error.
	if !strings.Contains(strings.ToLower(err.Error()), "no prefs") {
		t.Errorf("unconfigured error %q no longer contains \"no prefs\" — "+
			"update isEnergyUnconfigured's mapping in internal/cmd/energy.go", err)
	}
}
