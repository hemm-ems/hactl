//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// readAutomationConfig fetches an automation's config straight from HA, so the
// assertion does not depend on any hactl rendering path.
func readAutomationConfig(t *testing.T, automationID string) map[string]any {
	t.Helper()
	cfg := loadConfig(t)
	client := haapi.New(cfg.URL, cfg.Token)
	raw, err := client.GetAutomationConfig(context.Background(), automationID)
	if err != nil {
		t.Fatalf("reading automation config %s: %v", automationID, err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("parsing automation config %s: %v", automationID, err)
	}
	return out
}

// TestAutoApplyRollbackRoundTrip verifies that a write actually reaches Home
// Assistant and that a rollback actually restores what was there before.
//
// This exists because the pre-existing write test asserted only that the
// automation still existed after each step (`assertContains(out, autoID)`),
// which is true whether or not anything was written. Stubbing
// haapi.Client.UpdateAutomationConfig to `return nil` — i.e. discarding every
// automation write — left both the unit tier and this whole integration package
// green. The one code path that mutates a user's Home Assistant had no
// end-to-end verification anywhere.
//
// Assert on config read back from HA, never on hactl's own echo: "applied: <id>"
// is printed unconditionally once the write call returns nil.
func TestAutoApplyRollbackRoundTrip(t *testing.T) {
	autoID := getFirstAutoID(t)

	before := readAutomationConfig(t, autoID)
	beforeAlias, _ := before["alias"].(string)

	const newAlias = "RoundTrip Applied Alias"
	if beforeAlias == newAlias {
		t.Fatalf("precondition failed: automation %s already has the test alias", autoID)
	}

	yamlPath := writeTestAutoYAML(t, `alias: `+newAlias+`
description: byte-level round-trip test
trigger:
  - platform: time
    at: "03:21:00"
condition: []
action:
  - service: light.turn_on
    target:
      entity_id: light.bedroom
mode: single
`)

	// --- apply must change what HA stores ---
	applyOut := runHactl(t, "auto", "apply", autoID, "-f", yamlPath, "--confirm")
	assertContains(t, applyOut, "applied:")

	afterApply := readAutomationConfig(t, autoID)
	gotAlias, _ := afterApply["alias"].(string)
	if gotAlias != newAlias {
		t.Fatalf("apply did not reach HA: alias in HA is %q, want %q\napply output:\n%s",
			gotAlias, newAlias, applyOut)
	}

	// The trigger is a second, independent witness that the whole document was
	// written rather than just the field the renderer happens to show.
	if !strings.Contains(mustJSON(t, afterApply), "03:21:00") {
		t.Errorf("apply reached HA but did not write the new trigger; config is:\n%s",
			mustJSON(t, afterApply))
	}

	// --- rollback must restore what was there before ---
	rollbackOut := runHactl(t, "auto", "rollback", autoID, "--confirm")
	assertContains(t, rollbackOut, "rolled back:")

	afterRollback := readAutomationConfig(t, autoID)
	if restored, _ := afterRollback["alias"].(string); restored != beforeAlias {
		t.Fatalf("rollback did not restore the original config: alias is %q, want %q\nrollback output:\n%s",
			restored, beforeAlias, rollbackOut)
	}
	if got, want := mustJSON(t, canonicalize(afterRollback)), mustJSON(t, canonicalize(before)); got != want {
		t.Errorf("rollback restored a different config than was there before:\n before: %s\n after:  %s",
			want, got)
	}
}

// canonicalize folds Home Assistant's legacy singular automation keys onto the
// modern plural ones so a round-trip can be compared exactly.
//
// Writing an automation through the Config API migrates its schema: HA rewrites
// trigger/condition/action to triggers/conditions/actions, and service to action
// within each step. So a config read back after a write never matches the
// pre-write bytes even when the round-trip is perfectly faithful. Comparing raw
// bytes here would make this gate permanently red; comparing nothing would make
// it worthless. Fold the aliases, then demand exact equality of the rest.
func canonicalize(cfg map[string]any) map[string]any {
	out := make(map[string]any, len(cfg))
	for k, v := range cfg {
		switch k {
		case "trigger":
			k = "triggers"
		case "condition":
			k = "conditions"
		case "action":
			k = "actions"
		}
		if k == "actions" {
			if steps, ok := v.([]any); ok {
				folded := make([]any, 0, len(steps))
				for _, step := range steps {
					m, ok := step.(map[string]any)
					if !ok {
						folded = append(folded, step)
						continue
					}
					c := make(map[string]any, len(m))
					for sk, sv := range m {
						if sk == "service" {
							sk = "action"
						}
						c[sk] = sv
					}
					folded = append(folded, c)
				}
				v = folded
			}
		}
		out[k] = v
	}
	return out
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshaling config: %v", err)
	}
	return string(b)
}
