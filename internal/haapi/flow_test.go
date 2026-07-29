package haapi

import (
	"encoding/json"
	"testing"
)

func TestParseFlowResult_Form(t *testing.T) {
	raw := []byte(`{
		"flow_id": "abc123",
		"type": "form",
		"step_id": "init",
		"handler": "mqtt",
		"title": "MQTT Setup",
		"data_schema": [
			{"name": "broker", "required": true, "type": "string"},
			{"name": "port", "required": true, "type": "integer", "default": 1883}
		],
		"errors": {}
	}`)

	result, err := ParseFlowResult(raw)
	if err != nil {
		t.Fatalf("ParseFlowResult error: %v", err)
	}
	if result.FlowID != "abc123" {
		t.Errorf("FlowID = %q, want 'abc123'", result.FlowID)
	}
	if result.Type != "form" {
		t.Errorf("Type = %q, want 'form'", result.Type)
	}
	if result.StepID != "init" {
		t.Errorf("StepID = %q, want 'init'", result.StepID)
	}
	if result.Handler != "mqtt" {
		t.Errorf("Handler = %q, want 'mqtt'", result.Handler)
	}
	if result.Title != "MQTT Setup" {
		t.Errorf("Title = %q, want 'MQTT Setup'", result.Title)
	}
	if len(result.DataSchema) != 2 {
		t.Fatalf("DataSchema len = %d, want 2", len(result.DataSchema))
	}
	if result.DataSchema[0].Name != "broker" {
		t.Errorf("DataSchema[0].Name = %q, want 'broker'", result.DataSchema[0].Name)
	}
	if !result.DataSchema[0].Required {
		t.Error("DataSchema[0].Required = false, want true")
	}
	if result.DataSchema[1].Name != "port" {
		t.Errorf("DataSchema[1].Name = %q, want 'port'", result.DataSchema[1].Name)
	}
	// Default value for port should be 1883
	if result.DataSchema[1].Default == nil {
		t.Error("DataSchema[1].Default = nil, want 1883")
	}
}

func TestParseFlowResult_CreateEntry(t *testing.T) {
	raw := []byte(`{
		"flow_id": "xyz789",
		"type": "create_entry",
		"step_id": "",
		"handler": "mqtt",
		"title": "MQTT",
		"result": {"entry_id": "new-entry-123"}
	}`)

	result, err := ParseFlowResult(raw)
	if err != nil {
		t.Fatalf("ParseFlowResult error: %v", err)
	}
	if result.Type != "create_entry" {
		t.Errorf("Type = %q, want 'create_entry'", result.Type)
	}
	if len(result.Result) == 0 {
		t.Error("Result is empty, want entry JSON")
	}
	var entry map[string]string
	if err := json.Unmarshal(result.Result, &entry); err != nil {
		t.Fatalf("unmarshal Result: %v", err)
	}
	if entry["entry_id"] != "new-entry-123" {
		t.Errorf("entry_id = %q, want 'new-entry-123'", entry["entry_id"])
	}
}

func TestParseFlowResult_Abort(t *testing.T) {
	raw := []byte(`{
		"flow_id": "abort1",
		"type": "abort",
		"step_id": "",
		"handler": "hue",
		"reason": "already_configured"
	}`)

	result, err := ParseFlowResult(raw)
	if err != nil {
		t.Fatalf("ParseFlowResult error: %v", err)
	}
	if result.Type != "abort" {
		t.Errorf("Type = %q, want 'abort'", result.Type)
	}
	if result.FlowID != "abort1" {
		t.Errorf("FlowID = %q, want 'abort1'", result.FlowID)
	}
}

func TestParseFlowResult_WithErrors(t *testing.T) {
	raw := []byte(`{
		"flow_id": "err1",
		"type": "form",
		"step_id": "user",
		"handler": "test",
		"data_schema": [{"name": "host", "required": true, "type": "string"}],
		"errors": {"host": "cannot_connect", "base": "unknown"}
	}`)

	result, err := ParseFlowResult(raw)
	if err != nil {
		t.Fatalf("ParseFlowResult error: %v", err)
	}
	if len(result.Errors) != 2 {
		t.Errorf("Errors len = %d, want 2", len(result.Errors))
	}
	if result.Errors["host"] != "cannot_connect" {
		t.Errorf("Errors[host] = %q, want 'cannot_connect'", result.Errors["host"])
	}
}

func TestParseFlowResult_EmptySchema(t *testing.T) {
	raw := []byte(`{
		"flow_id": "f1",
		"type": "form",
		"step_id": "step1",
		"handler": "test",
		"data_schema": []
	}`)

	result, err := ParseFlowResult(raw)
	if err != nil {
		t.Fatalf("ParseFlowResult error: %v", err)
	}
	if len(result.DataSchema) != 0 {
		t.Errorf("DataSchema len = %d, want 0", len(result.DataSchema))
	}
}

func TestParseFlowResult_Expandable(t *testing.T) {
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

	result, err := ParseFlowResult(raw)
	if err != nil {
		t.Fatalf("ParseFlowResult error: %v", err)
	}
	if len(result.DataSchema) != 2 {
		t.Fatalf("DataSchema len = %d, want 2", len(result.DataSchema))
	}
	adv := result.DataSchema[1]
	if adv.Name != "advanced" || adv.Type != "expandable" {
		t.Fatalf("expected advanced/expandable, got %q/%q", adv.Name, adv.Type)
	}
	if len(adv.Schema) != 2 {
		t.Fatalf("advanced.Schema len = %d, want 2", len(adv.Schema))
	}
	if adv.Schema[0].Name != "framerate" || adv.Schema[0].Type != "float" {
		t.Errorf("Schema[0] = %q/%q, want framerate/float", adv.Schema[0].Name, adv.Schema[0].Type)
	}
	if adv.Schema[1].Name != "verify_ssl" {
		t.Errorf("Schema[1].Name = %q, want verify_ssl", adv.Schema[1].Name)
	}
}

// TestParseFlowResult_MenuBothWireShapes — HA sends menu_options either as a
// list of step ids or as a {step_id: label} map; the parse accepts the union
// (no live probe can enumerate which shape a given integration picks — the
// tolerant superset asserts nothing false, cf. D-8). Map form comes out
// sorted by id (H-16), list form keeps its wire order.
func TestParseFlowResult_MenuBothWireShapes(t *testing.T) {
	listForm := []byte(`{"flow_id":"m1","type":"menu","step_id":"init","handler":"knx",
		"menu_options":["b_second","a_first"]}`)
	flow, err := parseFlowResult(listForm)
	if err != nil {
		t.Fatalf("parseFlowResult(list form): %v", err)
	}
	if len(flow.MenuOptions) != 2 || flow.MenuOptions[0].ID != "b_second" || flow.MenuOptions[1].ID != "a_first" {
		t.Errorf("list form = %+v, want wire order [b_second a_first]", flow.MenuOptions)
	}

	mapForm := []byte(`{"flow_id":"m2","type":"menu","step_id":"init","handler":"knx",
		"menu_options":{"z_last":"Z","a_first":"A"}}`)
	flow, err = parseFlowResult(mapForm)
	if err != nil {
		t.Fatalf("parseFlowResult(map form): %v", err)
	}
	if len(flow.MenuOptions) != 2 || flow.MenuOptions[0].ID != "a_first" || flow.MenuOptions[1].Label != "Z" {
		t.Errorf("map form = %+v, want sorted ids [a_first z_last] with labels", flow.MenuOptions)
	}
}

// TestParseFlowResult_SelectOptions — a select's options survive the parse in
// both wire forms (plain strings and [value, label] pairs), normalized to the
// submittable value.
func TestParseFlowResult_SelectOptions(t *testing.T) {
	raw := []byte(`{"flow_id":"s1","type":"form","step_id":"user","handler":"generic",
		"data_schema":[{"name":"mode","type":"select","required":true,
			"options":[["opt_a","Label A"],"opt_b"]}]}`)
	flow, err := parseFlowResult(raw)
	if err != nil {
		t.Fatalf("parseFlowResult: %v", err)
	}
	if len(flow.DataSchema) != 1 {
		t.Fatalf("DataSchema = %+v, want one field", flow.DataSchema)
	}
	got := flow.DataSchema[0].Options
	if len(got) != 2 || got[0] != "opt_a" || got[1] != "opt_b" {
		t.Errorf("Options = %v, want [opt_a opt_b]", got)
	}
}

func TestParseFlowResult_InvalidJSON(t *testing.T) {
	_, err := ParseFlowResult([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}
