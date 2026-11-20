//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

// B-16 (D-131) end to end: `script run --confirm` on a script whose step targets
// an entity that does not exist passes Home Assistant's own warning through —
// the line TestOracleScriptRunMissingTarget found in the system log and nowhere
// else — in text and in --json `warnings`, and still exits 0. Written from the
// contract (D-131, manual "What `executed` means") without reading the
// `script run` implementation.
func TestScriptRunWarnsAboutAMissingTarget(t *testing.T) {
	inst, _ := getOracleHA(t)
	const want = "warning: Home Assistant logged during this run: " +
		"Referenced entities " + missingTargetEntity + " are missing or not currently available"

	out := runHactlDir(t, inst.Dir(), "script", "run", "oracle_script_missing_target", "--confirm")
	assertContains(t, out, "executed "+missingTargetScript)
	assertContains(t, out, want)

	raw := runHactlDir(t, inst.Dir(), "script", "run", "oracle_script_missing_target", "--confirm", "--json")
	var doc struct {
		Warnings []string `json:"warnings"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &doc); err != nil {
		t.Fatalf("script run --json is not one JSON object: %v\n%s", err, raw)
	}
	sentence := "Referenced entities " + missingTargetEntity + " are missing or not currently available"
	found := false
	for _, w := range doc.Warnings {
		if strings.Contains(w, sentence) {
			found = true
		}
	}
	if !found {
		t.Errorf("--json warnings %q do not carry HA's sentence %q", doc.Warnings, sentence)
	}
}
