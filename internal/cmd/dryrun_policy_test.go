package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

// An allowed plan's note is the manual's literal line — `policy: <n> checked,
// allowed (profile <p>)` — not a detail padded to the preview's column, and it
// comes last whatever the details' widths (D-70). Under --json it stays
// `details.policy` = {checked, profile}.
func TestAllowedPlanNoteIsTheManualsLine(t *testing.T) {
	plan := func() *dryRunPlan {
		return dryRun("create dashboard").
			with("url_path", "x").
			withPolicy(&planPolicyNote{Checked: 2, Profile: "agent"}).
			with("sidebar", true)
	}
	flagJSON = false
	var text strings.Builder
	if err := plan().render(&text); err != nil {
		t.Fatal(err)
	}
	want := "dry-run: would create dashboard\n  url_path: x\n  sidebar:  true\n  policy: 2 checked, allowed (profile agent)\n"
	if !strings.HasPrefix(text.String(), want) {
		t.Errorf("text preview\n got: %q\nwant prefix: %q", text.String(), want)
	}

	flagJSON = true
	defer func() { flagJSON = false }()
	var raw strings.Builder
	if err := plan().render(&raw); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Details map[string]json.RawMessage `json:"details"`
	}
	if err := json.Unmarshal([]byte(raw.String()), &doc); err != nil {
		t.Fatal(err)
	}
	if got := string(doc.Details["policy"]); strings.Join(strings.Fields(got), "") != `{"checked":2,"profile":"agent"}` {
		t.Errorf("details.policy = %s", got)
	}
}
