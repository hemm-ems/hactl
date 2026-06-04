package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestRenderFlowResult_ExpandableHint verifies that expandable sections are
// rendered with their nested sub-fields and a hint showing how to nest them
// in --data.
func TestRenderFlowResult_ExpandableHint(t *testing.T) {
	old := flagJSON
	flagJSON = false
	defer func() { flagJSON = old }()

	raw := []byte(`{
		"flow_id": "cam1",
		"type": "form",
		"step_id": "user",
		"handler": "generic",
		"data_schema": [
			{"name": "stream_source", "required": true, "type": "string"},
			{"name": "advanced", "required": true, "type": "expandable", "schema": [
				{"name": "framerate", "type": "float", "required": false},
				{"name": "verify_ssl", "type": "boolean", "required": false}
			]}
		]
	}`)

	buf := new(bytes.Buffer)
	if err := renderFlowResult(buf, raw); err != nil {
		t.Fatalf("renderFlowResult error: %v", err)
	}
	out := buf.String()

	// Nested sub-fields shown with dotted path.
	if !strings.Contains(out, "advanced.framerate") {
		t.Errorf("expected nested field 'advanced.framerate' in output:\n%s", out)
	}
	// Nesting hint present.
	if !strings.Contains(out, "expandable section") {
		t.Errorf("expected expandable-section hint in output:\n%s", out)
	}
	if !strings.Contains(out, `{"advanced":`) {
		t.Errorf("expected nesting example in output:\n%s", out)
	}
}
