package cmd

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

// WP9 — what a rendered schema owes its reader.
//
// Live-fire #82 #83 #84. The data was on the wire in every case and the
// renderer read none of it: a modern Home Assistant types a form field with a
// SELECTOR and leaves `type` empty, so a number, a 28-value enum, an entity
// picker and a device picker all reached the table as the fallback "string",
// and `description.suggested_value` — the current configuration, which is the
// whole difference between an options flow and a fresh one — never reached the
// Default column at all.

// templateOptionsStep is the reference instance's own answer, trimmed: the
// options flow of a template binary_sensor helper. Every shape #82 and #83 name
// is in it — a template selector, a select with its choices, a device picker,
// an expandable section, and a suggested value.
const templateOptionsStep = `{
  "type": "form",
  "flow_id": "809f274e2c89559179dd2b9545c63347",
  "handler": "01KYRT9D6E97D39XN9KC6B9GFK",
  "step_id": "binary_sensor",
  "data_schema": [
    {"name":"state","required":true,"selector":{"template":{}},
     "description":{"suggested_value":"{{ true }}"}},
    {"name":"device_class","required":false,
     "selector":{"select":{"options":[
        {"value":"battery","label":"Battery"},
        {"value":"motion","label":"Motion"}],
        "mode":"dropdown","sort":true}}},
    {"name":"device_id","required":false,"selector":{"device":{"multiple":false}}},
    {"name":"advanced_options","required":false,"type":"expandable",
     "schema":[{"name":"availability","required":false,"selector":{"template":{}}}]}
  ]
}`

func parseStep(t *testing.T, body string) *haapi.FlowResult {
	t.Helper()
	res, err := haapi.ParseFlowResult(json.RawMessage(body))
	if err != nil {
		t.Fatalf("parsing the flow step: %v", err)
	}
	return res
}

// TestSchemaTableNamesTheSelectorKind — finding #82.
//
// The contrast the report drew is the one that makes this a defect rather than
// a preference: a MENU step renders its full choice list, while the
// structurally identical concept — a field with a finite set of valid values —
// rendered as an unconstrained "string" with the 28 allowed values nowhere in
// the output, and both facts came from the same response.
func TestSchemaTableNamesTheSelectorKind(t *testing.T) {
	flow := parseStep(t, templateOptionsStep)
	var buf strings.Builder
	if err := renderSchemaTable(&buf, flow); err != nil {
		t.Fatalf("rendering: %v", err)
	}
	out := buf.String()

	for _, want := range []string{
		"state                          template",
		"device_class                   select",
		"device_id                      device",
		"advanced_options               expandable",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the table does not type the field (%q):\n%s", want, out)
		}
	}
	if strings.Contains(out, "string") {
		t.Errorf("a selector-backed field still renders as an unconstrained string:\n%s", out)
	}
	// A finite set of values is only useful if the values are printed, which is
	// what the menu step already did and this did not.
	if !strings.Contains(out, "battery") || !strings.Contains(out, "motion") {
		t.Errorf("a select's submittable values are not shown:\n%s", out)
	}
}

// TestSchemaTableShowsTheValueHASuggests — finding #83.
//
// `config show --probe-options-flow` surfaced `{"state": "{{ true }}"}` from the
// same endpoint through a different code path, which is what proved the data
// was available and simply unread.
func TestSchemaTableShowsTheValueHASuggests(t *testing.T) {
	flow := parseStep(t, templateOptionsStep)
	var buf strings.Builder
	if err := renderSchemaTable(&buf, flow); err != nil {
		t.Fatalf("rendering: %v", err)
	}
	if !strings.Contains(buf.String(), "{{ true }}") {
		t.Errorf("the current value HA sent never reached the Default column:\n%s", buf.String())
	}
}

// TestSchemaFieldDefaultPrefersTheSuggestion pins the precedence, because the
// two wire fields mean different things: `default` is the fallback, and
// `description.suggested_value` is what HA proposes for THIS entry. A caller
// re-submitting an options form needs the second.
func TestSchemaFieldDefaultPrefersTheSuggestion(t *testing.T) {
	both := haapi.SchemaField{Default: "fallback", Suggested: "current"}
	if got := schemaFieldDefault(both); got != "current" {
		t.Errorf("Default column = %q, want the suggested value", got)
	}
	if got := schemaFieldDefault(haapi.SchemaField{Default: 5}); got != "5" {
		t.Errorf("a field with only a default renders %q", got)
	}
	if got := schemaFieldDefault(haapi.SchemaField{}); got != "" {
		t.Errorf("a field with neither renders %q, want empty", got)
	}
}

// TestSchemaFieldTypePrefersTheFieldsOwnDeclaration — a field that declares a
// `type` says what it is, and the selector is the answer only when it does not.
// Asserted rather than assumed: `expandable` arrives as a type beside no
// selector, and reversing the precedence would rename it after whatever
// selector a future HA wraps sections in.
func TestSchemaFieldTypePrefersTheFieldsOwnDeclaration(t *testing.T) {
	for _, tc := range []struct {
		field haapi.SchemaField
		want  string
	}{
		{haapi.SchemaField{Type: "expandable"}, "expandable"},
		{haapi.SchemaField{Type: "integer", Selector: "number"}, "integer"},
		{haapi.SchemaField{Selector: "entity"}, "entity"},
		{haapi.SchemaField{}, "string"},
	} {
		if got := schemaFieldType(tc.field); got != tc.want {
			t.Errorf("schemaFieldType(%+v) = %q, want %q", tc.field, got, tc.want)
		}
	}
}

// TestFlowLookupErrorExplainsThe404Everywhere — finding #84.
//
// One condition, three commands, and the explanation attached to whichever one
// somebody was looking at: `flow-step` without --confirm said flows expire,
// `flow-inspect` and `flow-step --confirm` handed back HA's bare 404. The
// endpoint-mismatch cause is the one a caller cannot guess, so the message
// names the flag and the direction to move it.
func TestFlowLookupErrorExplainsThe404Everywhere(t *testing.T) {
	notFound := haapi.NewHTTPStatusError(http.MethodGet, "/api/config/config_entries/flow/x",
		http.StatusNotFound, []byte(`{"message":"Invalid flow specified"}`))

	config := flowLookupError("x", false, notFound)
	for _, want := range []string{"never existed", "expired", "options flow", "add --options"} {
		if !strings.Contains(config.Error(), want) {
			t.Errorf("the config-flow message does not name %q: %s", want, config)
		}
	}
	options := flowLookupError("x", true, notFound)
	if !strings.Contains(options.Error(), "drop --options") {
		t.Errorf("the options-flow message points the wrong way: %s", options)
	}

	// The boundary: a failure that is not "no such flow" is not a caller
	// mistake, and telling them to check their flag would be a guess.
	server := haapi.NewHTTPStatusError(http.MethodGet, "/x", http.StatusInternalServerError, []byte("boom"))
	if got := flowLookupError("x", false, server); !errors.Is(got, server) || strings.Contains(got.Error(), "--options") {
		t.Errorf("a 500 was rewritten as a caller mistake: %s", got)
	}
	transport := errors.New("dial tcp: connection refused")
	if got := flowLookupError("x", false, transport); !errors.Is(got, transport) {
		t.Errorf("a transport failure was rewritten: %s", got)
	}
}
