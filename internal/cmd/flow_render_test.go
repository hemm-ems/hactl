package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestRenderFlowResult_ErrorsAreDeterministic — a form step that fails several
// fields at once renders them in field order, byte-identically on every run.
//
// flow.Errors is a map, and the render loop used to range it directly, so two
// invocations of the same failed flow-step disagreed about which error came
// first (H-16). Forty renders make an unsorted walk vanishingly unlikely to
// pass by luck: (1/4!)^39 for the four keys below.
func TestRenderFlowResult_ErrorsAreDeterministic(t *testing.T) {
	old := flagJSON
	flagJSON = false
	defer func() { flagJSON = old }()

	raw := []byte(`{
		"flow_id": "f1",
		"type": "form",
		"step_id": "user",
		"handler": "generic",
		"errors": {
			"username": "invalid_auth",
			"password": "invalid_auth",
			"host": "cannot_connect",
			"port": "invalid_port"
		}
	}`)

	want := "  host: cannot_connect\n" +
		"  password: invalid_auth\n" +
		"  port: invalid_port\n" +
		"  username: invalid_auth\n"

	for run := range 40 {
		buf := new(bytes.Buffer)
		if err := renderFlowResult(buf, raw); err != nil {
			t.Fatalf("renderFlowResult error: %v", err)
		}
		if out := buf.String(); !strings.Contains(out, want) {
			t.Fatalf("run %d: errors are not rendered in sorted field order:\n%s", run, out)
		}
	}
}

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
