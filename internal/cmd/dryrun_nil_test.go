package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

// A plan detail that is nil means "this becomes nothing" — `ent set-area
// --clear` and `device set-area --clear` set new_area that way so `--json`
// carries the null a machine can act on.
//
// The two audiences need different words for it (H-10). Rendering the human
// line with %v printed `new_area: <nil>`, which is Go's spelling of nothing
// leaking into output a caller reads, while the JSON null was already right.
// Both halves are asserted here because fixing either one alone breaks the
// other.
func TestADryRunRendersNothingInWordsAndNullInJSON(t *testing.T) {
	plan := func() *dryRunPlan {
		return dryRun("clear entity area").
			with("entity_id", "sensor.x").
			with("new_area", nil)
	}

	var text strings.Builder
	flagJSON = false
	if err := plan().render(&text); err != nil {
		t.Fatalf("render: %v", err)
	}
	if strings.Contains(text.String(), "<nil>") {
		t.Errorf("the human plan spells nothing the way Go does:\n%s", text.String())
	}
	if !strings.Contains(text.String(), "(none)") {
		t.Errorf("the human plan does not say what new_area becomes:\n%s", text.String())
	}

	var raw strings.Builder
	flagJSON = true
	defer func() { flagJSON = false }()
	if err := plan().render(&raw); err != nil {
		t.Fatalf("render --json: %v", err)
	}
	var doc struct {
		Details map[string]any `json:"details"`
	}
	if err := json.Unmarshal([]byte(raw.String()), &doc); err != nil {
		t.Fatalf("--json is not JSON: %v\n%s", err, raw.String())
	}
	if v, ok := doc.Details["new_area"]; !ok || v != nil {
		t.Errorf("--json must carry new_area as null, got %#v (present=%v)", v, ok)
	}
}
