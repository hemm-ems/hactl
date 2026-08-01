package realdata_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/hemm-ems/hactl/internal/realdata"
)

// SPEC-realdata-fixture.md A3: the fixture leaks nothing, proven on every run
// rather than by a person having checked once.
//
// Both gates run in the NORMAL unit tier, behind no build tag and needing no
// Docker. That placement is the point: this is the check that decides whether a
// tree may be pushed to a public repository, so it must be impossible to be
// green without having run it.

func fixtureRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", "realistic-instance"))
	if err != nil {
		t.Fatalf("resolving the fixture: %v", err)
	}
	if _, statErr := os.Stat(root); statErr != nil {
		t.Fatalf("the fixture is not where this gate looks (%s): %v", root, statErr)
	}
	return root
}

// TestFixtureCarriesNoRealWorldShapes is the gate that does not need the
// capture's source, and therefore the one that still works next year.
//
// The derived gate below can only compare against a snapshot somebody
// remembered to archive. This one reads the published tree alone and refuses
// anything shaped like real-world data — a routable IPv4, a real NIC's MAC, a
// decimal precise enough to be a coordinate — so a leak introduced by a FUTURE
// capture, out of a part of the instance no snapshot covers, is caught by a
// check that never saw either.
func TestFixtureCarriesNoRealWorldShapes(t *testing.T) {
	leaks, err := realdata.ShapeLeaks(fixtureRoot(t))
	if err != nil {
		t.Fatalf("scanning the fixture: %v", err)
	}
	for _, leak := range leaks {
		t.Errorf("the committed fixture carries %s", leak)
	}
}

// TestFixtureCarriesNothingFromTheArchivedSnapshot is the derived half: every
// value sitting in a sensitive position in the archived capture of the
// reference instance, asserted absent from the published tree.
//
// It skips when the archive is not present, because the archive lives outside
// this repository — and it says so loudly rather than passing quietly, since a
// gate that silently becomes a no-op is the failure this project keeps finding.
func TestFixtureCarriesNothingFromTheArchivedSnapshot(t *testing.T) {
	snapshot := os.Getenv("HACTL_SNAPSHOT_DIR")
	if snapshot == "" {
		// The workspace layout the capture was taken in. Falling back to it
		// rather than requiring the variable keeps the gate live for whoever
		// actually holds the snapshot.
		snapshot = filepath.Join("..", "..", "..", "_archive", "livefire-2026-07-30", "snapshot")
	}
	// filepath.Clean is what settles gosec's taint analysis here: the path comes
	// from the operator's environment, and this gate only ever READS under it.
	if _, err := os.Stat(filepath.Clean(snapshot)); err != nil {
		t.Skipf("no archived snapshot at %s — the DERIVED half of the leak gate did not run; "+
			"TestFixtureCarriesNoRealWorldShapes still did", snapshot)
	}

	literals, err := realdata.SensitiveLiterals(snapshot)
	if err != nil {
		t.Fatalf("extracting from the snapshot: %v", err)
	}
	if len(literals) < 50 {
		t.Fatalf("only %d sensitive literals came out of %s — the extractor has stopped matching, "+
			"and an empty comparison would pass against any tree at all", len(literals), snapshot)
	}
	t.Logf("comparing the fixture against %d literals extracted from the archived capture", len(literals))

	leaks, err := realdata.Contains(fixtureRoot(t), literals)
	if err != nil {
		t.Fatalf("scanning the fixture: %v", err)
	}

	unused := map[string]bool{}
	for value := range collisions {
		unused[value] = true
	}
	for _, leak := range leaks {
		if _, exempt := collisions[leak.Value]; exempt {
			delete(unused, leak.Value)
			continue
		}
		t.Errorf("the committed fixture carries a value from the real instance: %s", leak)
	}
	// A stale exemption is how a gate turns into a rubber stamp — the same rule
	// dev/surfaces/README.md states for a manifest, applied here because this
	// list is a manifest by another name.
	for value := range unused {
		t.Errorf("the exemption for %q no longer matches anything — delete the line", value)
	}
}

// collisions are values the derived gate reports that are not leaks, each with
// the reason, and each re-checked on every run.
//
// The gate compares a single run of letters as a WHOLE VALUE rather than as a
// substring (see realdata.distinctiveLiteral), which is what stops it firing on
// every English word in the tree. What survives that are values which really
// are identical to something in the snapshot — and for a single common word
// that is coincidence, not survival.
//
// Both entries below are values HOME ASSISTANT authored, which is exactly why
// the snapshot and the fixture agree on them. Neither can identify anybody, and
// changing the fixture to dodge the gate would cost real fidelity: the `Map`
// title is byte-for-byte what HA's own `_create_map_dashboard` writes, which is
// the property .storage/lovelace.map exists to carry.
//
// This list is short on purpose. If it grows, the rule above is wrong and
// should be fixed rather than papered over.
var collisions = map[string]string{
	"Map":    "Home Assistant's own map-dashboard title, written by _create_map_dashboard; the fixture carries it verbatim on purpose",
	"Button": "a generic blueprint input label in the hand-authored half of the fixture, and an equally generic name somewhere in the snapshot",
}

// TestFixtureStorageHelpersAreWholeItems — SPEC §6 Q1's second answer, kept.
//
// The probe's finding was that Home Assistant ACCEPTING an item is not the same
// as the entity working: a `counter` without `minimum`/`maximum` was registered
// and then came up `unavailable`, because the key was absent rather than
// invalid and nothing rejected it. A fixture full of `unavailable` helpers
// would look like 170 storage helpers and prove nothing, so the completeness of
// each item is asserted here — on the committed bytes, where a hand-edit would
// also be caught.
func TestFixtureStorageHelpersAreWholeItems(t *testing.T) {
	storage := filepath.Join(fixtureRoot(t), ".storage")
	entries, err := os.ReadDir(storage)
	if err != nil {
		t.Fatalf("reading %s: %v", storage, err)
	}

	required := map[string][]string{
		"input_number":   {"id", "name", "min", "max"},
		"input_text":     {"id", "name", "min", "max"},
		"input_select":   {"id", "name", "options"},
		"input_datetime": {"id", "name", "has_date", "has_time"},
		"timer":          {"id", "name", "duration"},
		"input_boolean":  {"id", "name"},
		"input_button":   {"id", "name"},
		"schedule": {"id", "name", "monday", "tuesday", "wednesday", "thursday",
			"friday", "saturday", "sunday"},
	}

	found := 0
	for _, entry := range entries {
		keys, wanted := required[entry.Name()]
		if !wanted {
			continue
		}
		found++
		data, readErr := os.ReadFile(filepath.Join(storage, entry.Name())) //nolint:gosec // G304: this repo's own testdata
		if readErr != nil {
			t.Fatalf("reading .storage/%s: %v", entry.Name(), readErr)
		}
		var doc struct {
			Key  string `json:"key"`
			Data struct {
				Items []map[string]any `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Errorf(".storage/%s is not valid JSON: %v", entry.Name(), err)
			continue
		}
		if doc.Key != entry.Name() {
			t.Errorf(".storage/%s declares key %q — Home Assistant reads the key, not the filename", entry.Name(), doc.Key)
		}
		if len(doc.Data.Items) == 0 {
			t.Errorf(".storage/%s holds no items, so it carries the same zero the rig already had", entry.Name())
		}
		for _, item := range doc.Data.Items {
			for _, key := range keys {
				if _, present := item[key]; !present {
					t.Errorf(".storage/%s item %v has no %q — it would register and come up `unavailable`",
						entry.Name(), item["id"], key)
				}
			}
			for _, runtime := range []string{"editable", "friendly_name", "next_event"} {
				if _, present := item[runtime]; present {
					t.Errorf(".storage/%s item %v carries the runtime attribute %q, which HA never writes there",
						entry.Name(), item["id"], runtime)
				}
			}
		}
	}
	if found < len(required) {
		t.Errorf("the fixture holds %d of the %d helper collections the reference instance uses — "+
			"a domain that quietly disappears takes its whole defect class with it", found, len(required))
	}
}

// TestFixtureCarriesTheHelperShapesItExistsFor — SPEC A2, for the half this
// change delivers.
//
// A fixture that quietly loses a shape is the green-by-construction failure
// returning, so each property §11 names is asserted PRESENT rather than assumed.
// helperCensus is what the committed .storage collections actually hold.
type helperCensus struct {
	total      int
	nonASCII   int
	longestID  int
	domains    map[string]int
	icons      map[string]bool
	withBlocks int
}

func takeHelperCensus(t *testing.T) helperCensus {
	t.Helper()
	storage := filepath.Join(fixtureRoot(t), ".storage")
	c := helperCensus{domains: map[string]int{}, icons: map[string]bool{}}

	for _, domain := range []string{"input_boolean", "input_button", "input_datetime",
		"input_number", "input_select", "input_text", "schedule", "timer"} {
		data, err := os.ReadFile(filepath.Join(storage, domain)) //nolint:gosec // G304: this repo's own testdata
		if err != nil {
			t.Fatalf("the fixture has no .storage/%s: %v", domain, err)
		}
		var doc struct {
			Data struct {
				Items []map[string]any `json:"items"`
			} `json:"data"`
		}
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf(".storage/%s: %v", domain, err)
		}
		c.domains[domain] = len(doc.Data.Items)
		c.total += len(doc.Data.Items)
		for _, item := range doc.Data.Items {
			c.observe(domain, item)
		}
	}
	return c
}

func (c *helperCensus) observe(domain string, item map[string]any) {
	if id, _ := item["id"].(string); len([]rune(id)) > c.longestID {
		c.longestID = len([]rune(id))
	}
	if name, _ := item["name"].(string); anyNonASCII(name) {
		c.nonASCII++
	}
	if icon, _ := item["icon"].(string); icon != "" {
		c.icons[icon] = true
	}
	if domain != "schedule" {
		return
	}
	for _, day := range []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"} {
		if blocks, _ := item[day].([]any); len(blocks) > 0 {
			c.withBlocks++
			return
		}
	}
}

// TestFixtureCarriesTheHelperShapesItExistsFor — SPEC A2, for the half this
// change delivers.
//
// A fixture that quietly loses a shape is the green-by-construction failure
// returning, so each property §11 names is asserted PRESENT rather than assumed.
// Every one of these was watched failing against the fixture as it stood before
// the capture, where the answer to all six was zero.
func TestFixtureCarriesTheHelperShapesItExistsFor(t *testing.T) {
	c := takeHelperCensus(t)

	// The number the whole spec turns on: the rig had zero.
	if c.total < 150 {
		t.Errorf("the fixture holds %d storage-backed helpers; the reference instance has ~170 and the point "+
			"of §11 is that magnitude is itself a shape", c.total)
	}
	if len(c.domains) != 8 {
		t.Errorf("storage helpers in %d domains, want the 8 the reference instance uses: %v", len(c.domains), c.domains)
	}
	if c.nonASCII == 0 {
		t.Error("no helper name carries non-ASCII — finding #14 is a byte-versus-rune cut and a " +
			"pure-ASCII fixture cannot express it")
	}
	if c.longestID < 40 {
		t.Errorf("the longest helper identifier is %d characters; the rig's old fixtures were ~20 and "+
			"every truncation finding (#9 #14 #51 #87) needs something worth truncating", c.longestID)
	}
	if len(c.icons) < 10 {
		t.Errorf("only %d distinct icons — the real instance's icon set includes a typo (`mdi:pressence`) "+
			"that no hand-authored fixture would ever contain", len(c.icons))
	}
	if c.withBlocks == 0 {
		t.Error("no schedule carries a weekday block — the capture projected state attributes only " +
			"and wrote thirteen empty schedules once already (SPEC S4)")
	}
}
