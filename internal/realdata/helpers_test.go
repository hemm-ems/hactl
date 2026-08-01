package realdata_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/realdata"
)

// The generator's own TC-8: a capture missing a key the domain's entity reads
// must be REFUSED, not silently defaulted.
//
// This is the SPEC §6 Q1 probe turned into a gate. A `counter` item without
// `minimum`/`maximum` passed Home Assistant's storage schema, was registered,
// and then raised `KeyError: 'minimum'` on every state write — so the fixture
// would have carried a helper that exists and reads `unavailable`, which is
// worse than no helper at all: it is a shape that looks present and proves
// nothing.
func TestStorageCollectionsRefusesAnItemThatWouldBootBroken(t *testing.T) {
	var s realdata.Sanitizer

	_, _, err := realdata.StorageCollections([]realdata.HelperItem{{
		Domain: "counter", ID: "laundry_loads", Name: "Laundry Loads",
		Config: map[string]any{"step": 1, "initial": 0}, // no minimum/maximum
	}}, &s)
	if err == nil {
		t.Fatal("a counter with no minimum/maximum was accepted; it would come up unavailable")
	}
	if !strings.Contains(err.Error(), "minimum") {
		t.Errorf("the refusal does not name the missing key: %v", err)
	}

	// The control: the same item with the keys present is written.
	files, _, err := realdata.StorageCollections([]realdata.HelperItem{{
		Domain: "counter", ID: "laundry_loads", Name: "Laundry Loads",
		Config: map[string]any{"minimum": nil, "maximum": nil, "step": 1, "initial": 0},
	}}, &s)
	if err != nil {
		t.Fatalf("a complete counter was refused: %v", err)
	}
	if len(files["counter"]) == 0 {
		t.Error("no .storage/counter document was produced")
	}
}

// An empty capture may not produce an empty .storage. That would be the rig's
// original zero, re-created by the tool written to end it.
func TestStorageCollectionsRefusesAnEmptyCapture(t *testing.T) {
	var s realdata.Sanitizer
	if _, _, err := realdata.StorageCollections(nil, &s); err == nil {
		t.Fatal("an empty capture produced a fixture instead of an error")
	}
}

// The envelope has to be the one Home Assistant reads, and the runtime-only
// attributes have to stay out of it: a `.storage` file carrying `editable` or
// `friendly_name` is not a document HA would ever have written, so a command
// tested against it would be tested against a shape that does not occur.
func TestStorageCollectionsWritesTheEnvelopeHomeAssistantReads(t *testing.T) {
	var s realdata.Sanitizer
	files, renamed, err := realdata.StorageCollections([]realdata.HelperItem{
		{Domain: "input_number", ID: "target_temp", Name: "Zieltemperatur", Icon: "mdi:thermometer",
			Config: map[string]any{
				"min": 5.0, "max": 30.0, "step": 0.5, "mode": "slider",
				"unit_of_measurement": "°C",
				// Runtime noise that must not be stored:
				"editable": true, "friendly_name": "Zieltemperatur",
			}},
	}, &s)
	if err != nil {
		t.Fatalf("StorageCollections: %v", err)
	}

	var doc struct {
		Version      int    `json:"version"`
		MinorVersion int    `json:"minor_version"`
		Key          string `json:"key"`
		Data         struct {
			Items []map[string]any `json:"items"`
		} `json:"data"`
	}
	if err := json.Unmarshal(files["input_number"], &doc); err != nil {
		t.Fatalf("the generated document is not JSON: %v\n%s", err, files["input_number"])
	}
	if doc.Version != 1 || doc.Key != "input_number" {
		t.Errorf("envelope is version=%d key=%q, want 1/input_number", doc.Version, doc.Key)
	}
	if len(doc.Data.Items) != 1 {
		t.Fatalf("want one item, got %d", len(doc.Data.Items))
	}
	item := doc.Data.Items[0]

	for _, key := range []string{"id", "name", "icon", "min", "max", "step", "mode", "unit_of_measurement"} {
		if _, ok := item[key]; !ok {
			t.Errorf("the stored item lost %q, which HA's collection reads", key)
		}
	}
	for _, key := range []string{"editable", "friendly_name"} {
		if _, ok := item[key]; ok {
			t.Errorf("the stored item carries the runtime attribute %q — HA never writes that into .storage", key)
		}
	}
	if item["name"] == "Zieltemperatur" {
		t.Error("the display name survived sanitizing")
	}
	if item["icon"] != "mdi:thermometer" {
		t.Errorf("icon = %v; an mdi name is a public vocabulary and is carried verbatim", item["icon"])
	}
	if item["unit_of_measurement"] != "°C" {
		t.Errorf("unit = %v; a unit is structure, not content", item["unit_of_measurement"])
	}

	// The rename map is what keeps the fixture internally consistent: the
	// registry and every automation referencing the helper have to follow it.
	newID, mapped := renamed["input_number.target_temp"]
	if !mapped {
		t.Fatal("the id mapping does not record the rename, so nothing else in the fixture can follow it")
	}
	if newID == "input_number.target_temp" {
		t.Error("the identifier was not sanitized")
	}
	storedID, isText := doc.Data.Items[0]["id"].(string)
	if !isText {
		t.Fatalf("the stored id is %T, not a string", doc.Data.Items[0]["id"])
	}
	if "input_number."+storedID != newID {
		t.Errorf("the stored id %q disagrees with the mapping %q", storedID, newID)
	}
}

// A domain the generator does not know is refused rather than written blind:
// its required-key set is unknown, so it could only be emitted by guessing.
func TestStorageCollectionsRefusesAnUnknownDomain(t *testing.T) {
	var s realdata.Sanitizer
	_, _, err := realdata.StorageCollections([]realdata.HelperItem{
		{Domain: "input_hologram", ID: "x", Name: "X"},
	}, &s)
	if err == nil {
		t.Fatal("an unknown helper domain was written anyway")
	}
}
