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
	})
}
