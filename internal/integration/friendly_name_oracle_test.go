//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/haapi"
	"github.com/hemm-ems/hactl/internal/hatest"
)

// TestOracleFriendlyNameAfterARegistryRename asks Home Assistant what an
// automation's and a script's `friendly_name` is once the entity registry
// holds a name of its own (#195's alias column; R1 marker 2).
//
// hactl reads `friendly_name` as the config `alias:` (auto.go autoRow.alias,
// D-1). helpers/entity.py builds friendly_name from the registry name when one
// is set, so the two can differ — this pins which one HA reports, and the
// column hactl prints is named after the answer.
//
// It runs on an instance of its own: renaming an oracle automation would move
// what every alias-resolving test on the shared instance resolves.
func TestOracleFriendlyNameAfterARegistryRename(t *testing.T) {
	inst := hatest.Start(t, hatest.WithFixture("oracle"))
	waitForRunning(t, inst)
	ws := oracleWS(t, inst)
	client := haapi.New(inst.URL(), inst.Token())

	friendly := func(entityID string) string {
		t.Helper()
		raw, err := client.GetState(context.Background(), entityID)
		if err != nil {
			t.Fatalf("state of %s: %v", entityID, err)
		}
		var st struct {
			Attributes struct {
				FriendlyName string `json:"friendly_name"`
			} `json:"attributes"`
		}
		if err := json.Unmarshal(raw, &st); err != nil {
			t.Fatalf("decoding %s: %v", entityID, err)
		}
		return st.Attributes.FriendlyName
	}

	for _, tc := range []struct{ entityID, alias string }{
		{"automation.oracle_boost_charge", "Oracle Boost Charge"},
		{"script.oracle_script_ok", "Oracle Script OK"},
	} {
		if got := friendly(tc.entityID); got != tc.alias {
			t.Fatalf("precondition: %s friendly_name = %q before any rename, want its alias %q", tc.entityID, got, tc.alias)
		}
		const renamed = "Registry Display Name"
		if err := ws.EntityRegistryUpdate(context.Background(), tc.entityID, map[string]any{"name": renamed}); err != nil {
			t.Fatalf("registry rename of %s: %v", tc.entityID, err)
		}
		deadline := time.Now().Add(10 * time.Second)
		got := friendly(tc.entityID)
		for got == tc.alias && time.Now().Before(deadline) {
			time.Sleep(200 * time.Millisecond)
			got = friendly(tc.entityID)
		}
		t.Logf("ORACLE %s: alias %q, registry name %q → friendly_name %q", tc.entityID, tc.alias, renamed, got)
		if got != renamed {
			t.Errorf("%s: friendly_name = %q after a registry rename to %q; HA kept the config alias", tc.entityID, got, renamed)
		}
	}

	// PROBE (log only): does `auto show <config alias>` still resolve once
	// friendly_name no longer holds the alias?
	for _, ref := range []string{"Oracle Boost Charge", "Registry Display Name", "cfgid_boost_charge"} {
		out, err := runHactlDirErr(t, inst.Dir(), "auto", "show", ref)
		first := out
		if len(first) > 160 {
			first = first[:160]
		}
		t.Logf("PROBE auto show %q → err=%v out=%q", ref, err, first)
	}
}
