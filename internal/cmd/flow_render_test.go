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

// TestRenderFlowResult_MenuStep — a menu step renders its options and the
// flow-step submit hint. Regression for #112: menu_options were dropped at
// decode and the renderer had no menu branch, so a menu step printed the five
// header lines and nothing else. The map form must render sorted (H-16);
// forty runs make an unsorted walk vanishingly unlikely to pass by luck.
func TestRenderFlowResult_MenuStep(t *testing.T) {
	old := flagJSON
	flagJSON = false
	defer func() { flagJSON = old }()

	mapForm := []byte(`{
		"flow_id": "m1",
		"type": "menu",
		"step_id": "init",
		"handler": "knx",
		"menu_options": {"secure_knxkeys": "Keyfile", "secure_manual": "Manual", "plain": "Plain"}
	}`)
	want := "  plain  (Plain)\n" +
		"  secure_knxkeys  (Keyfile)\n" +
		"  secure_manual  (Manual)\n"
	for run := range 40 {
		buf := new(bytes.Buffer)
		if err := renderFlowResult(buf, mapForm); err != nil {
			t.Fatalf("renderFlowResult error: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, want) {
			t.Fatalf("run %d: map-form menu options not rendered in sorted id order:\n%s", run, out)
		}
		if !strings.Contains(out, `config flow-step m1 --data '{"next_step_id": "<option>"}'`) {
			t.Fatalf("run %d: missing flow-step submit hint:\n%s", run, out)
		}
	}

	listForm := []byte(`{
		"flow_id": "m2",
		"type": "menu",
		"step_id": "init",
		"handler": "knx",
		"menu_options": ["b_second", "a_first"]
	}`)
	buf := new(bytes.Buffer)
	if err := renderFlowResult(buf, listForm); err != nil {
		t.Fatalf("renderFlowResult error: %v", err)
	}
	out := buf.String()
	// List form keeps HA's order — it is not a map walk, so H-16 does not
	// reorder it.
	if !strings.Contains(out, "  b_second\n  a_first\n") {
		t.Errorf("list-form menu options not rendered in wire order:\n%s", out)
	}
}

// TestRenderFlowResult_SelectOptions — a select field's submittable values
// are visible without --json. They used to be dropped at decode, leaving a
// next_step_id select with no visible choices (#112's reported symptom).
func TestRenderFlowResult_SelectOptions(t *testing.T) {
	old := flagJSON
	flagJSON = false
	defer func() { flagJSON = old }()

	raw := []byte(`{
		"flow_id": "s1",
		"type": "form",
		"step_id": "user",
		"handler": "generic",
		"data_schema": [
			{"name": "next_step_id", "required": true, "type": "select",
			 "options": [["add_device", "Add device"], "remove_device"]}
		]
	}`)
	buf := new(bytes.Buffer)
	if err := renderFlowResult(buf, raw); err != nil {
		t.Fatalf("renderFlowResult error: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"next_step_id" options: add_device, remove_device`) {
		t.Errorf("select options line missing (pair and string forms must both normalize):\n%s", out)
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
