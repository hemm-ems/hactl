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

func TestParseFlowResult_InvalidJSON(t *testing.T) {
	_, err := ParseFlowResult([]byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}
