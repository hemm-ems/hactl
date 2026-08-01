//go:build livefire

package livefire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The rig's shape manifest.
//
// Every case in this file asserts a PROPERTY OF THE FIXTURE, not a property of
// hactl — and that is the point. The 2026-07-30 report's central finding was
// not any of its 90 defects; it was that a suite could be green because the
// condition it tested for could not occur. `helper show` 404ing on
// storage-backed helpers was untestable while no fixture had a `.storage`;
// `tpl create` poisoning a shared block was untestable while no fixture had a
// block worth poisoning. Each was a green rig by construction.
//
// A capability added to the rig is therefore worth exactly as long as it
// survives the next edit to the fixture. Delete two lines from template.yaml
// and every test that depends on a shared block starts passing for the wrong
// reason, silently and forever. These cases are what makes that edit fail
// instead: they name the property each shape exists to carry, so removing the
// shape breaks the manifest before it can quietly break the proofs.
//
// See FIXPLAN-livefire.md §4 for the capability backlog these work through.
const rigFixture = "realistic-instance"

func fixtureDir(tb testing.TB) string {
	tb.Helper()
	root, err := repoRoot()
	if err != nil {
		tb.Fatalf("locating repo root: %v", err)
	}
	return filepath.Join(root, "testdata", "fixtures", rigFixture)
}

func parseFixtureFile(tb testing.TB, rel string) *yaml.Node {
	tb.Helper()
	path := filepath.Join(fixtureDir(tb), rel)
	data, err := os.ReadFile(path) //nolint:gosec // G304: a path under this repo's testdata
	if err != nil {
		tb.Fatalf("reading %s: %v", rel, err)
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		tb.Fatalf("parsing %s: %v", rel, err)
	}
	return &doc
}

// tagValues collects the values carried by every node bearing the given custom
// tag. It walks the node tree rather than decoding into a Go value because
// decoding is precisely what destroys the information: `!include x.yaml` and
// the string "!include x.yaml" are indistinguishable afterwards, which is the
// defect these shapes exist to expose (finding #20).
func tagValues(node *yaml.Node, tag string) []string {
	var out []string
	var walk func(*yaml.Node)
	walk = func(n *yaml.Node) {
		if n == nil {
			return
		}
		if n.Tag == tag {
			out = append(out, n.Value)
		}
		for _, child := range n.Content {
			walk(child)
		}
	}
	walk(node)
	return out
}

// topLevelKeys returns the domain keys of a document's root mapping, paired
// with the node each maps to.
func topLevelKeys(tb testing.TB, doc *yaml.Node) map[string]*yaml.Node {
	tb.Helper()
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		tb.Fatalf("document root is not a mapping")
	}
	root := doc.Content[0]
	out := make(map[string]*yaml.Node, len(root.Content)/2)
	for i := 0; i+1 < len(root.Content); i += 2 {
		out[root.Content[i].Value] = root.Content[i+1]
	}
	return out
}

// TestRigFixtureCarriesConfigFilesBeyondTheThree is rig capability R2.
//
// Every other fixture in testdata/fixtures is exactly automations.yaml,
// configuration.yaml and scripts.yaml. A real instance is 155 files: domains
// pulled in from their own file, helpers arriving through a package, secrets
// in secrets.yaml. Three of the four ways a domain can reach HA were therefore
// unreachable on this tier, and a command that only ever met the fourth could
// not be caught here.
func TestRigFixtureCarriesConfigFilesBeyondTheThree(t *testing.T) {
	doc := parseFixtureFile(t, "configuration.yaml")
	keys := topLevelKeys(t, doc)

	// Both forms of the same job have to exist side by side. A fixture where
	// every domain is included proves as little as one where every domain is
	// inline: what a real config makes possible is the DISAGREEMENT between
	// two domains of the same kind, one of which is nowhere near
	// configuration.yaml.
	included := map[string]string{}
	for domain, value := range keys {
		if strings.HasPrefix(value.Tag, "!include") {
			included[domain] = value.Value
		}
	}
	for _, domain := range []string{"template", "input_boolean", "sensor"} {
		if _, ok := included[domain]; !ok {
			t.Errorf("configuration.yaml no longer pulls %q in from its own file — "+
				"a command that reads configuration.yaml and stops there is unfalsifiable again", domain)
		}
	}
	if inline := keys["input_number"]; inline == nil || inline.Kind != yaml.MappingNode {
		t.Error("configuration.yaml no longer writes any helper domain out inline — " +
			"the fixture has to carry both forms, or it cannot tell them apart")
	}

	// An include target that does not exist is a fixture that boots by luck.
	for domain, target := range included {
		if target == "" {
			continue // !include_dir_named names a directory below
		}
		if _, err := os.Stat(filepath.Join(fixtureDir(t), target)); err != nil {
			t.Errorf("%s: !include %s does not exist: %v", domain, target, err)
		}
	}

	// The package path: a domain that reaches HA without appearing in
	// configuration.yaml at all.
	home := keys["homeassistant"]
	if home == nil {
		t.Fatal("configuration.yaml has no homeassistant: block")
	}
	pkgDirs := tagValues(home, "!include_dir_named")
	if len(pkgDirs) == 0 {
		t.Fatal("configuration.yaml no longer loads a package directory — " +
			"the shape where a helper is defined in no file the tool knows about is gone")
	}
	entries, err := os.ReadDir(filepath.Join(fixtureDir(t), pkgDirs[0]))
	if err != nil || len(entries) == 0 {
		t.Fatalf("package directory %q is empty or missing: %v", pkgDirs[0], err)
	}
	pkg := parseFixtureFile(t, filepath.Join(pkgDirs[0], entries[0].Name()))
	for domain := range topLevelKeys(t, pkg) {
		if _, alsoInConfig := keys[domain]; alsoInConfig {
			t.Errorf("package %s defines %q, which configuration.yaml also defines — "+
				"the package then proves nothing a grep of configuration.yaml would not find",
				entries[0].Name(), domain)
		}
	}

	// A custom tag that is not an include. Resolved output that turns this into
	// a quoted string is output HA would reject (finding #20), so the fixture
	// has to contain one and it has to point at a key that really exists.
	secrets := tagValues(doc, "!secret")
	if len(secrets) == 0 {
		t.Fatal("configuration.yaml carries no !secret tag — the corruption in finding #20 " +
			"has nothing to corrupt")
	}
	defined := topLevelKeys(t, parseFixtureFile(t, "secrets.yaml"))
	for _, name := range secrets {
		if _, ok := defined[name]; !ok {
			t.Errorf("!secret %s is not defined in secrets.yaml", name)
		}
	}
}

// TestRigFixtureTemplateFileHasASharedBlock is the shape behind finding #89.
//
// A top-level item in template.yaml is a block, and every entity in it shares
// that block's fate. Two properties make the difference observable, and a
// fixture missing either one turns the proofs above it into tautologies:
//
//   - the same domain appears in more than one block — otherwise "append to
//     the first block" and "append to the right block" are the same
//     instruction and no placement bug can exist;
//   - some block holds more than one entity — otherwise poisoning a block
//     costs one entity, which is the entity being written, and the collateral
//     the finding is about cannot occur.
func TestRigFixtureTemplateFileHasASharedBlock(t *testing.T) {
	doc := parseFixtureFile(t, "template.yaml")
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.SequenceNode {
		t.Fatal("template.yaml is not a list of blocks")
	}

	blocksPerDomain := map[string]int{}
	widest := 0
	triggerKeyed := 0
	for _, block := range doc.Content[0].Content {
		if block.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(block.Content); i += 2 {
			key, value := block.Content[i].Value, block.Content[i+1]
			if key == "trigger" || key == "triggers" {
				triggerKeyed++
				continue
			}
			blocksPerDomain[key]++
			if value.Kind == yaml.SequenceNode && len(value.Content) > widest {
				widest = len(value.Content)
			}
		}
	}

	shared := ""
	for domain, n := range blocksPerDomain {
		if n > 1 {
			shared = domain
		}
	}
	if shared == "" {
		t.Errorf("no domain in template.yaml appears in two blocks: %v — "+
			"placement has become unobservable, so a writer that always appends to the "+
			"first block passes forever", blocksPerDomain)
	}
	if widest < 2 {
		t.Error("every block in template.yaml holds a single entity — a poisoned block can no " +
			"longer take an unrelated entity down with it, which is what finding #89 is about")
	}
	if triggerKeyed == 0 {
		t.Error("template.yaml has no trigger-keyed block — an entry appended to one inherits a " +
			"trigger it never asked for, the shape that corrupted template.yaml on 2026-07-13")
	}

	// SPEC-realdata-fixture.md S2: scale is itself a shape. Everything above is
	// satisfied by the five-block file this fixture used to hold, and five is
	// not the question `tpl create` has to answer on a real house — 91 is. With
	// five candidates, "append to the first block" and "append to the right
	// block" differ by four; with ninety, a writer that picks wrong is picking
	// wrong out of a field where being right is not luck.
	//
	// The bar is A2's fifty rather than the instance's ninety-one, because the
	// derivative tracks an instance that moves and a test pinned to today's
	// exact count would fail on the next capture for no reason.
	blocks := len(doc.Content[0].Content)
	if blocks < 50 {
		t.Errorf("template.yaml holds %d top-level blocks; the reference instance has 91 and A2 "+
			"asks for at least 50, because a placement decision among five candidates is not the "+
			"decision the command actually faces", blocks)
	}
	// The domain spread matters as much as the count: 65 sensor blocks against
	// 19 binary_sensor and 2 switch is what makes "the right block" a question
	// with a wrong answer available in the same domain AND in a different one.
	if len(blocksPerDomain) < 3 {
		t.Errorf("template.yaml uses %d entity domains: %v — with one domain, every wrong block "+
			"is at least the right KIND, and half the placement question disappears",
			len(blocksPerDomain), blocksPerDomain)
	}
	if triggerKeyed < 2 {
		t.Errorf("template.yaml has %d trigger-keyed blocks; more than one is what stops a writer "+
			"that finds \"the\" trigger block from being right by accident", triggerKeyed)
	}
}

// TestRigFixtureBlueprintCarriesInputTags is the other half of finding #20's
// shape: a custom YAML tag in a file that contains no !include whatsoever.
//
// Resolved mode documents itself as resolving !include directives. The
// corruption it actually performs hits every tag it does not know, in files
// that have no includes at all — so a fixture proving it needs a file where
// include resolution has nothing to do and damage happens anyway.
func TestRigFixtureBlueprintCarriesInputTags(t *testing.T) {
	root := filepath.Join(fixtureDir(t), "blueprints")
	var blueprint string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".yaml" {
			return err
		}
		if blueprint == "" {
			blueprint = path
		}
		return nil
	})
	if err != nil || blueprint == "" {
		t.Fatalf("no blueprint in the fixture: %v", err)
	}

	rel, err := filepath.Rel(fixtureDir(t), blueprint)
	if err != nil {
		t.Fatalf("relativising %s: %v", blueprint, err)
	}
	doc := parseFixtureFile(t, rel)
	inputs := tagValues(doc, "!input")
	if len(inputs) == 0 {
		t.Fatalf("%s carries no !input tag", rel)
	}
	if includes := tagValues(doc, "!include"); len(includes) > 0 {
		t.Errorf("%s contains !include %v — the file has to be free of includes, or the "+
			"corruption it demonstrates is indistinguishable from include resolution", rel, includes)
	}

	// The blueprint has to be instantiated, not merely present. An unused
	// blueprint is a file on disk; an automation defined by `use_blueprint` is
	// a fifth shape of automation, one with no trigger, condition or action of
	// its own for a reader to find.
	autos := parseFixtureFile(t, "automations.yaml")
	if len(autos.Content) == 0 || autos.Content[0].Kind != yaml.SequenceNode {
		t.Fatal("automations.yaml is not a list")
	}
	found := false
	for _, entry := range autos.Content[0].Content {
		if entry.Kind != yaml.MappingNode {
			continue
		}
		for i := 0; i+1 < len(entry.Content); i += 2 {
			if entry.Content[i].Value == "use_blueprint" {
				found = true
			}
		}
	}
	if !found {
		t.Error("no automation in the fixture is defined by use_blueprint — the blueprint's " +
			"!input tags are decoration and nothing depends on them surviving")
	}
}

// TestRigFixtureCarriesACustomComponentOwningForeignEntities is rig capability
// R3, and the gap it exists for is between two names that look like one.
//
// `shapewatch` is an INTEGRATION domain. `sensor.shape_watch_alpha` is an
// entity in the SENSOR domain that the integration happens to own. Nothing in
// the entity_id records the ownership; the entity registry's `platform` field
// is the only place it is written down. `cc show` matched entity_ids against
// the integration domain and reported `entities: 0` for all fourteen custom
// components on the reference instance, one of which owns 467 entities
// (finding #15).
//
// A component publishing `shapewatch.*` entities would be worse than no
// component at all: the prefix rule would count them correctly and the wrong
// rule would have a passing test.
func TestRigFixtureCarriesACustomComponentOwningForeignEntities(t *testing.T) {
	root := filepath.Join(fixtureDir(t), "custom_components")
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) == 0 {
		t.Fatalf("the fixture has no custom component: %v", err)
	}
	domain := entries[0].Name()

	manifest, err := os.ReadFile(filepath.Join(root, domain, "manifest.json")) //nolint:gosec // a path under this repo's testdata
	if err != nil {
		t.Fatalf("reading %s/manifest.json: %v", domain, err)
	}
	var declared struct {
		Domain string `json:"domain"`
	}
	if err := json.Unmarshal(manifest, &declared); err != nil {
		t.Fatalf("parsing %s/manifest.json: %v", domain, err)
	}
	if declared.Domain != domain {
		t.Errorf("%s/manifest.json declares domain %q — HA loads a custom component by its "+
			"directory name and the two have to agree", domain, declared.Domain)
	}

	// A component HA never sets up publishes nothing, and the shape is then a
	// directory rather than a capability.
	if _, ok := topLevelKeys(t, parseFixtureFile(t, "configuration.yaml"))[domain]; !ok {
		t.Errorf("configuration.yaml does not enable %q, so the component is never set up", domain)
	}
}

// TestRigFixtureCarriesEnergyPreferences is rig capability R4.
//
// The unit fixture for `energy show` carried one grid source in the
// flow_from/flow_to form plus a solar source, and its comment claimed to
// mirror HA's data.py. Both halves of that were the wrong way round: data.py
// calls flow_from/flow_to the LEGACY form and migrates it away on load, so the
// only shape the test exercised is the one a current instance never answers
// with — and solar is the single source type for which the code's rule
// ("stat_energy_from means production") is true by accident.
//
// So the fixture is written in the legacy form deliberately and HA migrates it
// while loading: the flat shape the rig serves is then Home Assistant's own
// output, not a fixture author's reconstruction of it. All five source types
// HA defines are present, because the direction a statistic represents is a
// property of (type, field) and a fixture holding two of five types cannot
// tell that rule from the one that shipped.
func TestRigFixtureCarriesEnergyPreferences(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(fixtureDir(t), ".storage", "energy"))
	if err != nil {
		t.Fatalf("the fixture has no energy preferences: %v", err)
	}
	var stored struct {
		MinorVersion int `json:"minor_version"`
		Data         struct {
			EnergySources []map[string]any `json:"energy_sources"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &stored); err != nil {
		t.Fatalf("parsing .storage/energy: %v", err)
	}

	// HA's _EnergyPreferencesStore migrates a grid source out of the legacy form
	// when the stored minor_version is below 3. Bumping this number would leave
	// the file as-is on load, and the rig would then serve a shape HA itself
	// would never store.
	if stored.MinorVersion >= 3 {
		t.Errorf("minor_version %d is at or past HA's migration threshold — the legacy grid form "+
			"is passed through unmigrated and the rig stops serving HA's own output",
			stored.MinorVersion)
	}

	types := map[string]bool{}
	legacyGrid := false
	for _, source := range stored.Data.EnergySources {
		kind, _ := source["type"].(string)
		types[kind] = true
		if kind == "grid" {
			if _, ok := source["flow_from"]; ok {
				legacyGrid = true
			}
		}
	}
	for _, kind := range energySourceTypes {
		if !types[kind] {
			t.Errorf("no %q source in the fixture — the direction rule is a property of "+
				"(type, field) and a type nobody configured cannot disagree with it", kind)
		}
	}
	if !legacyGrid {
		t.Error("the grid source is not in the legacy flow_from form — nothing then proves that " +
			"the flat shape the rig serves is HA's migration output rather than a fixture author's " +
			"reconstruction of it")
	}
}

// energySourceTypes is the closed set HA defines: data.py's
// `type SourceType = GridSourceType | SolarSourceType | BatterySourceType |
// GasSourceType | WaterSourceType`.
var energySourceTypes = []string{"grid", "solar", "battery", "gas", "water"}

// TestRigFixtureSeededDevicesAreAnchored is rig capability R1's other half,
// and it exists because the shape it protects was being deleted at runtime by
// Home Assistant itself.
//
// helpers/device_registry.async_cleanup removes every device referenced by
// neither a live config entry nor an entity registry entry. The seeded devices
// have no config entry — nothing in a YAML fixture can give them one — so they
// were orphans, and the debounced cleanup (CLEANUP_DELAY = 10s, and again on
// every restart) deleted them. The rename case passed or failed depending on
// how far the tier had got by then, which is worse than failing outright: it
// went green while the shape it tests was being removed underneath it, and it
// only surfaced at all because the recorder backfill added a restart.
//
// A seeded device therefore needs an anchor, and "remember to add an anchor"
// is not a mechanism.
func TestRigFixtureSeededDevicesAreAnchored(t *testing.T) {
	var devices struct {
		Data struct {
			Devices []struct {
				ID            string   `json:"id"`
				Name          string   `json:"name"`
				ConfigEntries []string `json:"config_entries"`
			} `json:"devices"`
		} `json:"data"`
	}
	raw, err := os.ReadFile(filepath.Join(fixtureDir(t), ".storage", "core.device_registry"))
	if err != nil {
		t.Fatalf("the fixture seeds no device registry: %v", err)
	}
	if jsonErr := json.Unmarshal(raw, &devices); jsonErr != nil {
		t.Fatalf("parsing .storage/core.device_registry: %v", jsonErr)
	}
	if len(devices.Data.Devices) == 0 {
		t.Fatal("the seeded device registry is empty")
	}

	var entities struct {
		Data struct {
			Entities []struct {
				EntityID string `json:"entity_id"`
				DeviceID string `json:"device_id"`
				Platform string `json:"platform"`
			} `json:"entities"`
		} `json:"data"`
	}
	raw, err = os.ReadFile(filepath.Join(fixtureDir(t), ".storage", "core.entity_registry"))
	if err != nil {
		t.Fatalf("the fixture seeds no entity registry, so no seeded device can be anchored: %v", err)
	}
	if jsonErr := json.Unmarshal(raw, &entities); jsonErr != nil {
		t.Fatalf("parsing .storage/core.entity_registry: %v", jsonErr)
	}

	anchoredBy := map[string]string{}
	for _, e := range entities.Data.Entities {
		if e.DeviceID != "" {
			anchoredBy[e.DeviceID] = e.EntityID
		}
	}
	for _, d := range devices.Data.Devices {
		if len(d.ConfigEntries) > 0 {
			continue
		}
		if _, ok := anchoredBy[d.ID]; !ok {
			t.Errorf("device %q (%s) has no config entry and no entity registry row pointing at "+
				"it — Home Assistant's own cleanup will delete it mid-run, and whichever case "+
				"depends on it will fail by the clock", d.Name, d.ID)
		}
	}
}

// TestRigBackfilledHistoryCanDisagreeWithAFlooredBucketCount is rig capability
// R5's fixture half, and it exists because the first version of it was wrong
// in a way that took a deliberate fail-check to notice.
//
// TestSweepResampleUsesTheBucketItWasGiven asks for ten-minute buckets. The
// backfilled series was two hours long, and against the DEFECT it was written
// for it passed: `int(span/bucket)` and `ceil(span/bucket)` agree exactly when
// the span divides evenly, and `span/count` then reproduces the requested
// width by arithmetic accident. The rig had a case for finding #39 that finding
// #39 could not fail.
//
// So the span has to be a non-multiple of the bucket, and that is not a
// property anyone will remember while editing an unrelated number.
func TestRigBackfilledHistoryCanDisagreeWithAFlooredBucketCount(t *testing.T) {
	if rigHistory.Span%sweepResampleBucket == 0 {
		t.Errorf("the backfilled span is %s, a whole number of %s buckets — the floored "+
			"bucket count and the correct one then agree, and the resample case passes against "+
			"the defect it exists for", rigHistory.Span, sweepResampleBucket)
	}
	if rigHistory.Span < 3*sweepResampleBucket {
		t.Errorf("the backfilled span is %s, which is fewer than three %s buckets — "+
			"\"every gap is one bucket\" needs three points to be a claim rather than an "+
			"accident of two endpoints", rigHistory.Span, sweepResampleBucket)
	}
	if rigHistory.Step >= sweepResampleBucket {
		t.Errorf("samples are %s apart and buckets are %s wide, so a bucket holds at most one "+
			"sample — nothing is being averaged and a resampler that mis-assigns samples to "+
			"buckets passes every value check there is", rigHistory.Step, sweepResampleBucket)
	}
}

// logShapeBudget is the width the log family's message column renders to. The
// shapes below are stated relative to it because that is what makes them
// shapes: a message is "long" only against the budget that cuts it.
const logShapeBudget = 60

// TestRigFixtureCarriesTheErrorLogShapes is rig capability R6's fixture half.
//
// A freshly booted container logs short, single-line, ASCII messages from
// loggers one segment deep, and every one of those words is a reason findings
// #14 and #16 could not fail here. The custom component's logshapes.py writes
// four records that are each the negation of one of them; this case is what
// keeps them written, because a fixture edit that softens a message is
// invisible to every proof standing on it.
func TestRigFixtureCarriesTheErrorLogShapes(t *testing.T) {
	path := filepath.Join(fixtureDir(t), "custom_components", "shapewatch", "logshapes.py")
	raw, err := os.ReadFile(path) //nolint:gosec // G304: a path under this repo's testdata
	if err != nil {
		t.Fatalf("the fixture emits no log records: %v", err)
	}
	src := string(raw)

	// The logger name is the whole of #16. `--component shapewatch` matches the
	// full dotted name and the table shows the last segment, so the shape needs
	// the filter term to be an INNER segment: a logger called `shapewatch`
	// would match and display the same string, and the defect would have had a
	// passing test.
	for _, logger := range []string{
		"custom_components.shapewatch.diagnostics.probe",
		"custom_components.shapewatch.helpers.loader",
	} {
		if !strings.Contains(src, logger) {
			t.Errorf("logshapes.py no longer registers %q", logger)
			continue
		}
		segments := strings.Split(logger, ".")
		if len(segments) < 3 {
			t.Errorf("%q is too shallow: the displayed segment has to be able to differ from the "+
				"matched one", logger)
		}
		if segments[len(segments)-1] == "shapewatch" {
			t.Errorf("%q ends in the filter term, so the displayed value contains it by accident "+
				"and finding #16 cannot fail here", logger)
		}
	}

	// Each constant carries one property. Reading them out of the Python is
	// what ties this manifest to the file that is actually mounted, rather than
	// to a Go copy of it that can drift.
	shapes := map[string]struct {
		want string
		why  string
	}{
		"LONG_SINGLE_LINE": {
			want: "longer than the display budget with no newline in it",
			why:  "the plain case of #14; without it nothing is ever cut",
		},
		"SHORT_FIRST_LINE": {
			want: "a first line under the budget and more lines after it",
			why: "the length test #14 is about never fires, so the newline reaches the cell and " +
				"breaks the table's column alignment — a merely long message cannot show that",
		},
		"RUNE_AT_THE_BOUNDARY": {
			want: "a two-byte character straddling the byte a slice would cut at",
			why:  "a byte slice there yields invalid UTF-8, and the reference instance is German",
		},
	}
	for name, shape := range shapes {
		if !strings.Contains(src, name+" = ") {
			t.Errorf("logshapes.py no longer defines %s (%s) — %s", name, shape.want, shape.why)
		}
	}
	if !strings.Contains(src, ".exception(") {
		t.Error("logshapes.py logs no exception, so no entry carries a traceback in Home " +
			"Assistant's separate `exception` field — the multi-kilobyte shape #14 is about")
	}
	// The byte offset is the shape, and a reworded sentence silently loses it.
	// The Python asserts it at import; this says the assertion is still there,
	// because a fixture that stops checking itself is a fixture that has
	// stopped carrying the property.
	if !strings.Contains(src, "[56:58]") {
		t.Errorf("logshapes.py no longer pins where the multi-byte character sits, so a reworded "+
			"message can move it off the %d-byte cut without failing anything", logShapeBudget)
	}
}

// rigCapabilityDebt records the capabilities in FIXPLAN-livefire.md §4 the rig
// has NOT been taught, each with the reason.
//
// It exists because of what the surfaces README calls the asymmetry: debt is
// legal, invisible debt is not. A shape nobody has built is indistinguishable
// from a shape nobody has thought of, and the second is how a suite acquires a
// blind spot it cannot report. Delete a row when the shape lands.
var rigCapabilityDebt = map[string]string{
	"R8": "two writers against one target — needs drivers, not a fixture, so it is harness work " +
		"rather than a shape; WP11 builds it",
	"R11": "a companion beside the rig's Home Assistant — hatest boots HA alone and writes a .env " +
		"with no COMPANION_URL, so every companion-routed command (config file/block, helper " +
		"ls/show/create, tpl cat, ref scan) can only be swept on the live profile. Found while " +
		"adding WP2's cases and widened by WP6, whose whole family routes through the companion; " +
		"the stack exists already in internal/companiontest/docker-compose.yaml, so this is " +
		"wiring rather than a shape",
}

// TestRigCapabilityDebtIsRecordedNotSilent keeps the ledger above honest: a row
// whose reason is a shrug is the idiom this whole mechanism exists to stop
// forming, and a row for a shape that has since been taught is a ledger that
// has stopped describing the rig.
func TestRigCapabilityDebtIsRecordedNotSilent(t *testing.T) {
	for capability, reason := range rigCapabilityDebt {
		if len(reason) < 25 {
			t.Errorf("%s: %q is not a reason", capability, reason)
		}
	}
	if len(rigCapabilityDebt) == 0 {
		return
	}
	t.Logf("rig capabilities not yet taught: %d (see FIXPLAN-livefire.md §4)", len(rigCapabilityDebt))
}

// TestRigServesTheShapesItCarries is the second half of a capability: the
// files above are a claim, and Home Assistant deciding to publish an entity
// for each of them is the evidence. A fixture that parses but whose package
// never loads teaches the rig nothing.
//
// It runs on both profiles because the real instance carries every one of
// these shapes too — that is where they were taken from — and a case that can
// only be asked of the rig is a case the rig can drift on alone.
func TestRigServesTheShapesItCarries(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		out := tgt.MustRead(t, "ent", "ls", "--top", "500", "--json")
		var rows []map[string]any
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("ent ls --json: %v\n%s", err, truncate(out))
		}

		var wallClocks, templateSensors, helpers int
		for _, row := range rows {
			id, _ := row["entity_id"].(string)
			state, _ := row["state"].(string)
			if looksLikeARenderedClock(state) {
				wallClocks++
			}
			switch {
			case strings.HasPrefix(id, "sensor."):
				templateSensors++
			case strings.HasPrefix(id, "input_"):
				helpers++
			}
		}

		// The clock case in corpus_test.go exempts `state`, because on a real
		// instance a sensor's honest state IS a wall clock. That exemption
		// passed on the rig for the wrong reason — no rig fixture had such a
		// sensor, so the exemption was never exercised and could have been
		// wrong without anything saying so. The fixture's sensor.yaml supplies
		// one; this is the assertion that keeps it there.
		if wallClocks == 0 {
			t.Error("no entity's state reads as a bare wall clock — the `state` exemption in " +
				"TestSweepNoRenderedClockReachesJSON is unexercised here and passes vacuously")
		}
		if templateSensors < 4 || helpers < 4 {
			t.Errorf("the instance serves %d sensors and %d helpers; the fixture's shapes are "+
				"not reaching Home Assistant", templateSensors, helpers)
		}

		// A pg_-namespaced helper on BOTH profiles. Nothing on the rig needs a
		// namespace, but the write cases discover their target by that prefix
		// (it is what guardLiveWrite allows), so a rig without one turns every
		// write case into a skip — the failure mode a corpus shared by two
		// profiles is most exposed to, because a skip reads like a pass.
		if pgInputBoolean(t, tgt) == "" {
			t.Error("no pg_ input_boolean is served here; the sweep's write cases would all skip " +
				"(the rig's is in testdata/fixtures/realistic-instance/input_boolean.yaml)")
		}
	})
}

// TestRigServesACustomComponentsForeignEntities is R3's served half: the
// ownership gap has to be visible through the tool, on both instances.
//
// The live profile names powercalc, which owns 218 entities and not one of
// them is called `powercalc.*` — the same shape as the rig's shapewatch, at a
// scale no fixture will reproduce.
func TestRigServesACustomComponentsForeignEntities(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		domain := "shapewatch"
		if tgt.Profile == Live {
			domain = "powercalc"
		}
		out := tgt.MustRead(t, "cc", "show", domain, "--json")
		var info struct {
			EntityIDs []string `json:"entity_ids"`
		}
		if err := json.Unmarshal([]byte(out), &info); err != nil {
			t.Fatalf("cc show %s --json: %v\n%s", domain, err, truncate(out))
		}
		if len(info.EntityIDs) == 0 {
			t.Fatalf("cc show %s reports no entities — the component owns none here, so nothing "+
				"can tell an entity_id prefix match from a registry join", domain)
		}
		for _, id := range info.EntityIDs {
			if strings.HasPrefix(id, domain+".") {
				t.Errorf("%s is named after its own integration — an entity a prefix match would "+
					"find is an entity that proves nothing about the join", id)
			}
		}
	})
}

// TestRigServesEveryEnergySourceTypeItConfigures is R4's served half.
//
// The two profiles expect different sets, and that asymmetry is the point of
// having both: the reference instance configures three of HA's five source
// types, so it can show that grid and gas exist in the wild and can say
// nothing at all about battery or water. The rig configures all five. Neither
// instance alone establishes the rule that finding #26 turns on.
func TestRigServesEveryEnergySourceTypeItConfigures(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		want := energySourceTypes
		if tgt.Profile == Live {
			want = []string{"grid", "solar", "gas"}
		}
		out := tgt.MustRead(t, "energy", "show", "--json")
		var prefs struct {
			Configured bool `json:"configured"`
			Sources    []struct {
				Type      string `json:"type"`
				Statistic string `json:"statistic"`
			} `json:"sources"`
		}
		if err := json.Unmarshal([]byte(out), &prefs); err != nil {
			t.Fatalf("energy show --json: %v\n%s", err, truncate(out))
		}
		if !prefs.Configured {
			t.Fatal("the instance reports no energy dashboard — R4's shape is absent")
		}
		got := map[string]bool{}
		for _, s := range prefs.Sources {
			got[s.Type] = true
			if s.Statistic == "" {
				t.Errorf("a %s row carries no statistic id", s.Type)
			}
		}
		for _, kind := range want {
			if !got[kind] {
				t.Errorf("no %q source reaches the output; got %v", kind, got)
			}
		}
	})
}

// TestRigFixtureCarriesTheDashboardShapes is R12's claim half: the three
// dashboard states a real instance has, and three flat YAML files could not.
//
// Every dash defect in WP5 needed one of them. `dash show --view` on a
// dashboard with no views was unreachable while every fixture dashboard had
// views; "Home Assistant holds no config for this dashboard" was unreachable
// while no fixture registered a dashboard without saving one; and the premise
// that a listed default is a YAML-mode default was unfalsifiable while the only
// fixture with a listed default was the YAML one.
func TestRigFixtureCarriesTheDashboardShapes(t *testing.T) {
	var collection struct {
		Data struct {
			Items []struct {
				ID      string `json:"id"`
				URLPath string `json:"url_path"`
				Mode    string `json:"mode"`
			} `json:"items"`
		} `json:"data"`
	}
	readFixtureJSON(t, filepath.Join(".storage", "lovelace_dashboards"), &collection)

	seeded := map[string]string{}
	for _, item := range collection.Data.Items {
		seeded[item.URLPath] = item.ID
	}
	for _, urlPath := range []string{"map", "rig-unsaved"} {
		if seeded[urlPath] == "" {
			t.Errorf("the dashboards collection registers no %q; R12's shapes start here", urlPath)
		}
	}
	if _, listed := seeded["lovelace"]; listed {
		t.Error("the fixture registers the default itself — the shape only proves anything when " +
			"Home Assistant's own migration produces it from .storage/lovelace")
	}

	// A strategy dashboard's config has no `views` key AT ALL. Giving it an
	// empty `views: []` would look equivalent and would not be: the document
	// hactl parses is what decides, and the reference instance's map dashboard
	// carries exactly {"strategy":{"type":"map"}}.
	var mapConfig struct {
		Data struct {
			Config map[string]any `json:"config"`
		} `json:"data"`
	}
	readFixtureJSON(t, filepath.Join(".storage", "lovelace."+seeded["map"]), &mapConfig)
	if _, hasViews := mapConfig.Data.Config["views"]; hasViews {
		t.Error("the map dashboard's stored config carries a `views` key; the shape is a document " +
			"with none, which is what makes len(cfg.Views) == 0 reachable")
	}
	if _, hasStrategy := mapConfig.Data.Config["strategy"]; !hasStrategy {
		t.Error("the map dashboard's stored config carries no `strategy`; then its zero views are " +
			"an empty dashboard rather than a generated one, and they are different answers")
	}

	// The registered-but-unsaved shape is an ABSENT file. Asserting it is the
	// only way the shape survives someone helpfully adding a config for it.
	if _, err := os.Stat(filepath.Join(fixtureDir(t), ".storage", "lovelace."+seeded["rig-unsaved"])); err == nil {
		t.Error("rig-unsaved has a stored config; the shape is a dashboard Home Assistant answers " +
			"config_not_found for, so this file must not exist")
	}

	// The default's config in its PRE-migration location, so HA performs the
	// migration and the rig carries whatever HA's migration currently produces.
	var defaultConfig struct {
		Data struct {
			Config map[string]any `json:"config"`
		} `json:"data"`
	}
	readFixtureJSON(t, filepath.Join(".storage", "lovelace"), &defaultConfig)
	if defaultConfig.Data.Config == nil {
		t.Error(".storage/lovelace holds no config, so _async_migrate_default_config returns at " +
			"its second step and the default is never listed")
	}
}

// TestRigServesTheDashboardShapes is R12's served half. The fixture is a claim;
// Home Assistant answering these four questions is the evidence.
//
// It runs on both profiles because the reference instance carries every one of
// them — a migrated storage-mode default under the reserved `lovelace` slug, a
// strategy-only `map` Home Assistant made during onboarding, and (WP5 keeps one
// there deliberately) a registered dashboard with nothing saved.
func TestRigServesTheDashboardShapes(t *testing.T) {
	eachProfile(t, func(t *testing.T, tgt Target) {
		t.Helper()
		var rows []struct {
			URLPath string `json:"url_path"`
			Mode    string `json:"mode"`
		}
		out := tgt.MustRead(t, "dash", "ls", "--json")
		if err := json.Unmarshal([]byte(out), &rows); err != nil {
			t.Fatalf("dash ls --json: %v\n%s", err, truncate(out))
		}
		mode := map[string]string{}
		for _, r := range rows {
			mode[r.URLPath] = r.Mode
		}

		// The shape the code believed impossible: the default IS listed, and it
		// is storage-mode. hactl read `listed` as `YAML-mode` and refused every
		// write to the default on an instance that accepts them.
		if got, listed := mode["lovelace"]; !listed || got != "storage" {
			t.Errorf("the default dashboard is listed=%v mode=%q; R12's shape is listed with "+
				"mode `storage` (Home Assistant's own migration produces it)", listed, got)
		}

		// A dashboard whose stored config has no views.
		strategy := "map"
		if mode[strategy] == "" {
			t.Fatalf("no %q dashboard is served; dash ls reports %v", strategy, mode)
		}
		raw := tgt.MustRead(t, "dash", "show", strategy, "--raw")
		var config map[string]any
		if err := json.Unmarshal([]byte(raw), &config); err != nil {
			t.Fatalf("dash show %s --raw is not JSON: %v\n%s", strategy, err, truncate(raw))
		}
		if _, hasViews := config["views"]; hasViews {
			t.Errorf("%s's served config carries `views`; the shape is a document with none", strategy)
		}

		// A dashboard Home Assistant answers config_not_found for. The live
		// profile's is pg-w5-fresh, created by WP5 and left in place on purpose:
		// it is the only way the reference instance can be asked this question
		// without saving a config over one of Jan's dashboards.
		unsaved := "rig-unsaved"
		if tgt.Profile == Live {
			unsaved = "pg-w5-fresh"
		}
		if _, listed := mode[unsaved]; !listed {
			t.Fatalf("no %q dashboard is registered; dash ls reports %v", unsaved, mode)
		}
		if _, err := tgt.Read(t, "dash", "show", unsaved, "--raw"); err == nil {
			t.Errorf("%s has a stored config; the shape is a registered dashboard with none", unsaved)
		}
	})
}

// readFixtureJSON decodes a fixture file into v.
func readFixtureJSON(tb testing.TB, rel string, v any) {
	tb.Helper()
	path := filepath.Join(fixtureDir(tb), rel)
	data, err := os.ReadFile(path) //nolint:gosec // G304: a path under this repo's testdata
	if err != nil {
		tb.Fatalf("reading %s: %v", rel, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		tb.Fatalf("parsing %s: %v", rel, err)
	}
}
