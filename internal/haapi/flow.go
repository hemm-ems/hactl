package haapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"

	"github.com/hemm-ems/hactl/internal/degeneracy"
)

// FlowResult represents a response from the config entries flow API.
type FlowResult struct {
	DataSchema  []SchemaField     `json:"-"`
	Errors      map[string]string `json:"errors,omitempty"`
	Result      json.RawMessage   `json:"result,omitempty"`
	FlowID      string            `json:"flow_id"`
	Type        string            `json:"type"` // "form", "create_entry", "abort", "external", "menu"
	StepID      string            `json:"step_id"`
	Handler     string            `json:"handler"`
	Title       string            `json:"title"`
	Description string            `json:"description_placeholders,omitempty"`
}

// SchemaField describes one field in a flow step's data schema.
type SchemaField struct {
	Default  any    `json:"default,omitempty"`
	Name     string `json:"name"`
	Type     string `json:"type,omitempty"` // "string", "integer", "boolean", "float", "select", "expandable", etc.
	Required bool   `json:"required"`
	// Schema holds the nested fields of an "expandable" section. When set, the
	// field's values must be submitted nested under Name, e.g. {"advanced": {...}}.
	Schema []SchemaField `json:"schema,omitempty"`
}

// flowRawResponse is the raw shape of the HA flow API response, used for parsing.
type flowRawResponse struct {
	DataSchema []json.RawMessage `json:"data_schema"`
	Errors     map[string]string `json:"errors"`
	Result     json.RawMessage   `json:"result"`
	FlowID     string            `json:"flow_id"`
	Type       string            `json:"type"`
	StepID     string            `json:"step_id"`
	Handler    string            `json:"handler"`
	Title      string            `json:"title"`
}

// parseFlowResult converts raw JSON into a FlowResult with parsed schema.
func parseFlowResult(data []byte) (*FlowResult, error) {
	var raw flowRawResponse
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parsing flow response: %w", err)
	}

	result := &FlowResult{
		FlowID:  raw.FlowID,
		Type:    raw.Type,
		StepID:  raw.StepID,
		Handler: raw.Handler,
		Title:   raw.Title,
		Errors:  raw.Errors,
		Result:  raw.Result,
	}

	result.DataSchema = parseSchemaFields(raw.DataSchema)

	// A flow step that decoded to nothing must not read as a step hactl simply
	// does not handle: without a type there is no form, no create_entry and no
	// abort, and `config flow-start` would print an empty form as progress.
	if err := degeneracy.Check("config_entries flow response", result); err != nil {
		return nil, err
	}

	return result, nil
}

// parseSchemaFields parses a list of raw schema-field JSON objects into
// SchemaFields, recursing into the nested "schema" of expandable sections so
// callers can see (and hint at) the fields that must be nested.
func parseSchemaFields(rawFields []json.RawMessage) []SchemaField {
	var fields []SchemaField
	for _, fieldRaw := range rawFields {
		var field struct {
			Default  any               `json:"default"`
			Name     string            `json:"name"`
			Type     string            `json:"type"`
			Required bool              `json:"required"`
			Schema   []json.RawMessage `json:"schema"`
		}
		if err := json.Unmarshal(fieldRaw, &field); err != nil {
			continue
		}
		sf := SchemaField{
			Name:     field.Name,
			Required: field.Required,
			Type:     field.Type,
			Default:  field.Default,
		}
		if len(field.Schema) > 0 {
			sf.Schema = parseSchemaFields(field.Schema)
		}
		fields = append(fields, sf)
	}
	return fields
}

// DeleteConfigEntry deletes a config entry by ID.
// DELETE /api/config/config_entries/entry/<entryID>
func (c *Client) DeleteConfigEntry(ctx context.Context, entryID string) ([]byte, error) {
	return c.doDelete(ctx, "/api/config/config_entries/entry/"+url.PathEscape(entryID))
}

// StartOptionsFlow starts an options flow for an existing config entry.
// POST /api/config/config_entries/options/flow with {"handler": entryID}
func (c *Client) StartOptionsFlow(ctx context.Context, entryID string) ([]byte, error) {
	body := map[string]string{"handler": entryID}
	return c.doPost(ctx, "/api/config/config_entries/options/flow", body)
}

// StartOptionsFlowOnce is like StartOptionsFlow but does not retry on 5xx.
// Retrying a POST that starts a flow can leave several flows dangling (one per
// attempt) while only one gets aborted, so the read-only probe in `config show`
// uses this single-shot variant.
// POST /api/config/config_entries/options/flow with {"handler": entryID}
func (c *Client) StartOptionsFlowOnce(ctx context.Context, entryID string) ([]byte, error) {
	body := map[string]string{"handler": entryID}
	return c.doPostOnce(ctx, "/api/config/config_entries/options/flow", body)
}

// StartConfigFlow starts a new config flow for a domain/integration.
// POST /api/config/config_entries/flow with {"handler": domain}
func (c *Client) StartConfigFlow(ctx context.Context, domain string) ([]byte, error) {
	body := map[string]string{"handler": domain}
	return c.doPost(ctx, "/api/config/config_entries/flow", body)
}

// StartConfigFlowOnce is like StartConfigFlow but does not retry on 5xx.
// When an integration fails to load (e.g. missing dependency), HA returns 500
// and retrying just wastes time. Use this for interactive/CLI flows.
func (c *Client) StartConfigFlowOnce(ctx context.Context, domain string) ([]byte, error) {
	body := map[string]string{"handler": domain}
	return c.doPostOnce(ctx, "/api/config/config_entries/flow", body)
}

// ConfigFlowHandlers lists the integration domains that expose a config flow —
// HA's own authority on what StartConfigFlow will accept. Unlike manifest/list
// (which reports only the integrations HA has currently *loaded*), this includes
// every installable integration with a config flow, loaded or not, so a dry run
// resolving a not-yet-configured domain accepts exactly what the confirmed run
// would.
// GET /api/config/config_entries/flow_handlers
func (c *Client) ConfigFlowHandlers(ctx context.Context) ([]string, error) {
	data, err := c.doGet(ctx, "/api/config/config_entries/flow_handlers")
	if err != nil {
		return nil, err
	}
	var handlers []string
	if err := json.Unmarshal(data, &handlers); err != nil {
		return nil, fmt.Errorf("parsing config flow handlers: %w", err)
	}
	return handlers, nil
}

// StepFlow submits data to advance a config/options flow.
// If options is true: POST /api/config/config_entries/options/flow/<flow_id>
// If options is false: POST /api/config/config_entries/flow/<flow_id>
func (c *Client) StepFlow(ctx context.Context, flowID string, options bool, data json.RawMessage) ([]byte, error) {
	if data == nil {
		data = json.RawMessage("{}")
	}
	path := "/api/config/config_entries/flow/" + flowID
	if options {
		path = "/api/config/config_entries/options/flow/" + flowID
	}
	return c.doPost(ctx, path, data)
}

// InspectFlow retrieves the current state of a flow.
// If options is true: GET /api/config/config_entries/options/flow/<flow_id>
// If options is false: GET /api/config/config_entries/flow/<flow_id>
func (c *Client) InspectFlow(ctx context.Context, flowID string, options bool) ([]byte, error) {
	path := "/api/config/config_entries/flow/" + flowID
	if options {
		path = "/api/config/config_entries/options/flow/" + flowID
	}
	return c.doGet(ctx, path)
}

// ParseFlowResult parses raw flow API response bytes into a structured FlowResult.
func ParseFlowResult(data []byte) (*FlowResult, error) {
	return parseFlowResult(data)
}

// AbortOptionsFlow deletes (aborts) an in-progress options flow. A read-only
// options-flow inspection starts a flow to read its schema; aborting it keeps
// the inspection from leaving a dangling flow behind in HA.
// DELETE /api/config/config_entries/options/flow/<flowID>
func (c *Client) AbortOptionsFlow(ctx context.Context, flowID string) error {
	_, err := c.doDelete(ctx, "/api/config/config_entries/options/flow/"+url.PathEscape(flowID))
	return err
}

// GetConfigEntryDiagnostics downloads the diagnostics dump for a config entry.
// GET /api/diagnostics/config_entry/<entryID>
//
// The dump is produced by the integration's diagnostics platform, which is
// responsible for redacting secrets, so it is the HA-blessed way to read an
// entry's data/options (the config_entries API deliberately omits them).
// Returns a 404 error when the integration ships no diagnostics platform (many
// custom integrations don't), and requires an admin token.
// Source: https://github.com/home-assistant/core/blob/dev/homeassistant/components/diagnostics/__init__.py
func (c *Client) GetConfigEntryDiagnostics(ctx context.Context, entryID string) ([]byte, error) {
	return c.doGet(ctx, "/api/diagnostics/config_entry/"+url.PathEscape(entryID))
}
