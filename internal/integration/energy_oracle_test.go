//go:build integration

package integration

import (
	"context"
	"regexp"
	"sort"
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

// energyDirectionTable is the set of source types internal/cmd's
// energyDirectionByType classifies. Kept here as a literal rather than
// imported, because the two lists agreeing is the thing being asserted and a
// gate that reads its expectation from the code it checks proves nothing.
var energyDirectionTable = []string{"battery", "gas", "grid", "solar", "water"}

// TestOracleEnergySourceTypes asks Home Assistant which energy source types
// exist, and fails when hactl's direction table does not cover exactly those.
//
// A wire sample cannot answer this: energy/get_prefs returns the types the
// instance happens to have configured, which is as consistent with "HA defines
// five" as with "HA defines nine and this house uses three". The producing
// code answers it — data.py declares `type SourceType = GridSourceType |
// SolarSourceType | BatterySourceType | GasSourceType | WaterSourceType`, a
// closed union, and each member carries the `type: Literal["…"]` this reads.
//
// This is the closure gate for finding #26. The defect was a direction derived
// from the field name alone, and the fix is a table over the source types;
// a table is only a fix while it is complete, so the day HA adds a sixth type
// this test says so instead of `hactl energy show` quietly labelling it
// "unknown" in somebody's terminal.
func TestOracleEnergySourceTypes(t *testing.T) {
	const path = "/usr/src/homeassistant/homeassistant/components/energy/data.py"
	code, out, err := ha.Exec(context.Background(), "grep", "-oE", `type: Literal\["[a-z_]+"\]`, path)
	if err != nil {
		t.Fatalf("reading %s from the running container: %v", path, err)
	}
	if code != 0 {
		t.Fatalf("grep over %s exited %d: %s", path, code, out)
	}

	literal := regexp.MustCompile(`"([a-z_]+)"`)
	seen := map[string]bool{}
	for _, match := range literal.FindAllStringSubmatch(out, -1) {
		seen[match[1]] = true
	}
	got := make([]string, 0, len(seen))
	for kind := range seen {
		got = append(got, kind)
	}
	sort.Strings(got)

	if len(got) == 0 {
		t.Fatalf("no `type: Literal[…]` declarations found in %s — the extractor has stopped "+
			"matching and would pass forever while proving nothing:\n%s", path, out)
	}
	if strings.Join(got, ",") != strings.Join(energyDirectionTable, ",") {
		t.Errorf("Home Assistant defines energy source types %v; hactl's energyDirectionByType "+
			"covers %v.\nA type HA emits and the table does not classify renders as %q — add it to "+
			"internal/cmd/energy.go with the direction data.py's own docstring gives it, and to "+
			"energySourceTypes in internal/livefire/rigshapes_test.go.",
			got, energyDirectionTable, "unknown")
	}
}
