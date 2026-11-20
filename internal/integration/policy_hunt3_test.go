//go:build integration

package integration

// Policy hunt 3 (2026-11-20): homeassistant.reload_config_entry re-creates
// every entity of the entry it reloads in its start-up state, so a denied
// demo light switched off by the owner came back on. Since D-200 the reload
// needs control on every entity of the entries it reaches.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/hatest"
)

const h3ReloadProfile = `    default: deny
    allow:
      "entity:*": [read, control]
      "service:homeassistant.*": [control]
      "service:light.*": [control]
    deny:
      "entity:light.ceiling_lights": [control]
      "entity:light.kitchen_lights": [control]
      "entity:lock.*": [control]
`

// demoEntryID is the demo integration's config entry on inst.
func demoEntryID(t *testing.T, inst *hatest.Instance) string {
	t.Helper()
	_, b := pgHTTP(t, inst, http.MethodGet, "/api/config/config_entries/entry", nil)
	var entries []struct {
		EntryID string `json:"entry_id"`
		Domain  string `json:"domain"`
	}
	if err := json.Unmarshal(b, &entries); err != nil {
		t.Fatalf("config entries: %v %s", err, huntOneLine(string(b), 200))
	}
	for _, e := range entries {
		if e.Domain == "demo" {
			return e.EntryID
		}
	}
	t.Fatalf("no demo config entry among %d", len(entries))
	return ""
}

func TestPolicyHunt3ReloadConfigEntry(t *testing.T) {
	inst := getPolicyEnforceHA(t)
	dir := pgInstanceDir(t, inst.URL(), inst.Token(), huntPolicy(h3ReloadProfile))
	demo := demoEntryID(t, inst)

	// By entity target: Home Assistant 2026.11 still reloads the entries of
	// a target's entities, but no longer declares the target fields, so
	// hactl refuses the call as an undeclared field before any policy check;
	// held either way.
	cases := []struct {
		name, data string
		policy     bool
	}{
		{"by entry_id", fmt.Sprintf(`{"entry_id":%q}`, demo), true},
		{"by allowed entity target", `{"entity_id":"light.bed_light"}`, false},
	}
	for _, k := range cases {
		pgCallService(t, inst, "light", "turn_off", map[string]any{"entity_id": "light.ceiling_lights"})
		pgCallService(t, inst, "lock", "unlock", map[string]any{"entity_id": "lock.front_door"})
		time.Sleep(time.Second)
		before := pgStateOf(t, inst, "light.ceiling_lights").State
		lockBefore := pgStateOf(t, inst, "lock.front_door").State
		pc, oc, rep := rhBoth(t, dir, "svc", "call", "homeassistant.reload_config_entry", "--data", k.data)
		var after, lockAfter string
		for range 20 {
			time.Sleep(time.Second)
			after = pgStateOf(t, inst, "light.ceiling_lights").State
			lockAfter = pgStateOf(t, inst, "lock.front_door").State
			if after != before || lockAfter != lockBefore {
				break
			}
		}
		rhVerdict(t, fmt.Sprintf("reload_config_entry %s (preview=%d confirm=%d) → light.ceiling_lights %s→%s, lock.front_door %s→%s",
			k.name, pc, oc, before, after, lockBefore, lockAfter),
			oc == 0 && (after != before || lockAfter != lockBefore), rep)
		if pc == 0 || oc == 0 || (k.policy && !strings.Contains(rep, "entity:light.ceiling_lights")) {
			t.Errorf("reload_config_entry %s: want refused naming entity:light.ceiling_lights (D-200)\n      %s", k.name, rep)
		}
	}

	// Allowed control on every entity of the entry: the reload is allowed
	// (preview only — the refusal above is what this test is about).
	open := pgInstanceDir(t, inst.URL(), inst.Token(), huntPolicy(`    default: deny
    allow:
      "entity:*": [read, control]
      "service:homeassistant.*": [control]
`))
	if out, c := huntRun(t, open, "svc", "call", "homeassistant.reload_config_entry", "--data", fmt.Sprintf(`{"entry_id":%q}`, demo)); c != 0 {
		t.Errorf("over-refusal: reload_config_entry with every entity controllable: exit %d %s", c, huntOneLine(out, 400))
	}
}
