//go:build livefire

package livefire

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/hemm-ems/hactl/internal/realdata"
	"gopkg.in/yaml.v3"
)

// The capture. It is a test rather than a command because everything it needs
// already exists here — a configured live target, the profile guard, the build
// of hactl under test — and because a `cmd/` binary that talks to somebody's
// house is a thing this repository should not ship.
//
// It does not run unless HACTL_CAPTURE_FIXTURE is set. Regenerating the fixture
// is a deliberate act by a maintainer sitting in front of a real instance, not
// something a sweep does on the way past.
//
// Read-only, and narrower than read-only needs to be: the routes below are the
// ones SPEC-realdata-fixture.md §5 names, and nothing here goes near
// `core.config_entries` (credentials for 38 integrations), `core.restore_state`
// or `auth*`. That is not an allowlist somebody has to remember — the capture
// is RECONSTRUCTIVE, so those files are not merely excluded, they are never
// opened. `config/auth/list` exists in haapi and is not called from here.

// TestCaptureRealDataFixture rebuilds testdata/fixtures/realistic-instance's
// storage-backed helper collections from the reference instance.
//
// SPEC-realdata-fixture.md's sharpest number: 220 storage helpers on the
// instance against zero on the rig, with no `.storage/<domain>` file at all —
// which is why the whole `helper show` 404 family was fixed and verified
// against a real house only, and why finding #104 was turned up by a capture
// rather than by a test.
func TestCaptureRealDataFixture(t *testing.T) {
	if os.Getenv("HACTL_CAPTURE_FIXTURE") == "" {
		t.Skip("set HACTL_CAPTURE_FIXTURE=1 (with HACTL_LIVEFIRE_DIR) to regenerate the fixture from a real instance")
	}
	tgt, ok := LiveTarget(t, hactlBin)
	if !ok {
		t.Fatal("HACTL_LIVEFIRE_DIR must name a configured instance to capture from")
	}

	items := captureHelpers(t, tgt)
	t.Logf("captured %d storage-backed helpers across %d domains", len(items), countDomains(items))

	var s realdata.Sanitizer
	files, renamed, err := realdata.StorageCollections(items, &s)
	if err != nil {
		t.Fatalf("generating the storage collections: %v", err)
	}

	dir := filepath.Join(fixtureDir(t), ".storage")
	for domain, body := range files {
		path := filepath.Join(dir, domain)
		if writeErr := os.WriteFile(path, body, 0o600); writeErr != nil {
			t.Fatalf("writing %s: %v", path, writeErr)
		}
		t.Logf("wrote .storage/%s", domain)
	}

	// The id mapping goes to a path the CAPTURER names, never into the fixture.
	//
	// It was written into `.storage/` first, and the leak gate caught it on the
	// next run: the mapping is keyed by the REAL identifiers, so committing it
	// publishes exactly the material the sanitizer had just removed —
	// `garten`, `strebergarten`, `timetester` — with the synthetic replacement
	// helpfully written next to each. A file that undoes the sanitizer is worse
	// than no sanitizer, because the tree looks clean.
	//
	// It is still produced, because a later stage (the registry, the
	// automations referencing these entities) has to follow the same renames
	// rather than sanitizing an identifier twice and disagreeing with itself.
	// It just belongs outside the repository.
	if out := os.Getenv("HACTL_CAPTURE_IDMAP"); out != "" {
		mapping, encodeErr := json.MarshalIndent(renamed, "", "  ")
		if encodeErr != nil {
			t.Fatalf("encoding the id mapping: %v", encodeErr)
		}
		if err := os.WriteFile(out, append(mapping, '\n'), 0o600); err != nil { //nolint:gosec // the capturer names the path
			t.Fatalf("writing the id mapping to %s: %v", out, err)
		}
		t.Logf("wrote the id mapping to %s — it carries the REAL identifiers; keep it out of the repository", out)
	}
}

// TestCaptureConfigTreeFixture derives the fixture's hand-written YAML from the
// archived snapshot rather than from a fresh read.
//
// SPEC-realdata-fixture.md §5: the config tree is already captured
// (`_archive/livefire-2026-07-30/snapshot/`), so no live read is needed and
// none is made. That is why it is a test of its own rather than a step inside
// the live capture: this half is reproducible by anybody holding the archive,
// with no house involved, and the half above is not — the snapshot is frozen
// and the instance is not.
//
// The two share no state and do not need to. The Sanitizer's answers are a pure
// function of the source value (a hash, never a counter — see the package doc),
// so a fresh one here maps `sensor.kwl_abluft` to exactly what the helper
// capture mapped it to, and the two halves agree without being run together.
func TestCaptureConfigTreeFixture(t *testing.T) {
	if os.Getenv("HACTL_CAPTURE_FIXTURE") == "" {
		t.Skip("set HACTL_CAPTURE_FIXTURE=1 to regenerate the config tree from the archived snapshot")
	}
	var s realdata.Sanitizer
	captureConfigTree(t, &s)
}

// configFile is one hand-written YAML file of the reference instance, and what
// has to happen to it on the way into the fixture.
type configFile struct {
	name string
	// exclusions are the entries that cannot be carried, each with its reason
	// (realdata.Exclusion). SPEC §5's bootability rule: what is dropped is named.
	exclusions []realdata.Exclusion
	// inert stops every entry from running here — see realdata.Inert.
	inert bool
	// keepHead preserves everything above the generated marker in the file that
	// is already in the fixture, because other cases name those entries.
	keepHead bool
}

var configFiles = []configFile{
	{name: "template.yaml"},
	{
		name:     "automations.yaml",
		inert:    true,
		keepHead: true,
		exclusions: []realdata.Exclusion{
			{
				Marker: "use_blueprint:",
				Why: "defined by a community-authored blueprint the fixture does not carry — " +
					"FIXPLAN §7's licensing question, still Jan's to settle",
			},
			{
				Marker: "platform: mqtt",
				Why:    "triggered by MQTT, and the rig boots Home Assistant with no broker",
			},
			{
				Marker: "platform: device",
				Why:    "triggered by a device that exists in one house and no fixture can seed",
			},
		},
	},
}

// generatedHeader separates a fixture file's hand-authored half from its
// captured one. Its first line is what the next capture cuts at.
var generatedHeader = []string{
	"# === EVERYTHING BELOW THIS LINE IS GENERATED ===============================",
	"#",
	"# TestCaptureConfigTreeFixture writes it, from a sanitized derivative of the",
	"# reference instance's own automations.yaml. The next capture discards and",
	"# rewrites all of it; an entry another case names belongs ABOVE this line.",
	"#",
	"# The entries carry `initial_state: false` because they cannot do here what",
	"# they were written to do — their services are not installed and their entities",
	"# mostly do not exist, so running them produces error log entries and nothing",
	"# else. They are carried for their SHAPE. See realdata.Inert.",
	"# ---------------------------------------------------------------------------",
}

// captureConfigTree writes each configFile into the fixture.
//
// The generator REFUSES on drift instead of writing a smaller file. That is S4,
// and it is not theoretical: one run dropped eight `unique_id` values carrying
// an umlaut, because they were being run through the entity-slug sanitizer and
// a slug has no room for one; another left the second line of a wrapped alias
// dangling and produced 9,600 lines of YAML that Home Assistant refused to
// parse. The first was caught by ShapeDrift and the second was not caught by
// anything, which is why StructureDrift exists.
func captureConfigTree(t *testing.T, s *realdata.Sanitizer) {
	t.Helper()

	root, err := repoRoot()
	if err != nil {
		t.Fatalf("locating the repo root: %v", err)
	}
	snapshot := os.Getenv("HACTL_SNAPSHOT_DIR")
	if snapshot == "" {
		snapshot = filepath.Join(root, "..", "_archive", "livefire-2026-07-30", "snapshot")
	}

	for _, file := range configFiles {
		raw, readErr := os.ReadFile(filepath.Clean(filepath.Join(snapshot, file.name)))
		if readErr != nil {
			t.Fatalf("reading %s from the snapshot at %s: %v", file.name, snapshot, readErr)
		}
		src := string(raw)

		if len(file.exclusions) > 0 {
			var removed map[string]int
			src, removed = realdata.DropTopLevelItems(src, file.exclusions)
			for _, line := range realdata.ExclusionReport(removed) {
				t.Logf("%s: %s", file.name, line)
			}
		}
		if file.inert {
			var changed int
			src, changed = realdata.Inert(src)
			t.Logf("%s: %d entries given `initial_state: false`", file.name, changed)
		}

		// Measured AFTER the declared edits, so drift reports what the
		// SANITIZER did. What the edits above did is reported by them.
		before := realdata.MeasureConfig(src)
		out := realdata.SanitizeConfigText(src, s)
		if drift := realdata.ShapeDrift(before, realdata.MeasureConfig(out)); len(drift) > 0 {
			t.Fatalf("sanitizing %s changed its shape, so the fixture would carry less than the "+
				"instance does: %v", file.name, drift)
		}
		if drift := realdata.StructureDrift(src, out); len(drift) > 0 {
			t.Fatalf("sanitizing %s produced a different document: %v", file.name, drift)
		}

		dst := filepath.Join(fixtureDir(t), file.name)
		if file.keepHead {
			out = handAuthoredHead(t, dst) + strings.Join(generatedHeader, "\n") + "\n" + out
		}
		//nolint:gosec // G703: `file.name` is a constant from the table above and
		// the directory is this repository's own testdata; the taint gosec
		// follows is HACTL_SNAPSHOT_DIR, which selects what is READ, never where
		// the result is written.
		if writeErr := os.WriteFile(dst, []byte(out), 0o600); writeErr != nil {
			t.Fatalf("writing %s: %v", dst, writeErr)
		}
		t.Logf("wrote %s — %d lines, %d top-level items, %d unique_ids, %d lines with non-ASCII",
			file.name, before.Lines, before.TopLevelDash, before.UniqueIDs, before.NonASCII)
	}
}

// handAuthoredHead returns everything in the fixture file above the generated
// marker, and refuses a file that has lost it.
//
// A capture that silently overwrote the hand-authored half would take
// `climate_schedule` with it — the automation the concurrency cases rewrite —
// and the failure would be four families away from its cause.
func handAuthoredHead(t *testing.T, path string) string {
	t.Helper()
	existing, err := os.ReadFile(path) //nolint:gosec // G304: a path under this repo's testdata
	if err != nil {
		t.Fatalf("reading the fixture's %s to keep its hand-authored half: %v", path, err)
	}
	head, _, found := strings.Cut(string(existing), generatedHeader[0])
	if !found {
		t.Fatalf("%s carries no generated marker, so this capture cannot tell the hand-authored "+
			"entries from the captured ones; the marker is %q", path, generatedHeader[0])
	}
	if strings.TrimSpace(head) == "" {
		t.Fatalf("%s has nothing above the generated marker — the hand-authored entries other "+
			"cases name are gone", path)
	}
	return head
}

// captureHelpers projects the instance's storage-backed helpers out of two
// reads.
//
// `/api/states` is the source for everything except which helpers are
// storage-backed: it carries the domain-specific configuration as attributes
// (min/max/step, options, has_date/has_time, the weekday blocks), which is the
// same information the `.storage` item holds. `editable` is what separates a
// storage collection from a YAML one — the attribute finding #104 was about,
// used here for the job it exists for.
//
// A helper whose `editable` is absent is skipped and counted. Those are
// restored ghosts: HA serves the registry entry with almost no attributes, so
// there is nothing to reconstruct from, and inventing one would put a helper in
// the fixture that the instance does not actually have.
func captureHelpers(t *testing.T, tgt Target) []realdata.HelperItem {
	t.Helper()

	baseURL, token := instanceCredentials(t, tgt)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/api/states", nil)
	if err != nil {
		t.Fatalf("building the states request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/states: %v", err)
	}
	defer resp.Body.Close() //nolint:errcheck // drained below
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading /api/states: %v", err)
	}

	var states []struct {
		EntityID   string         `json:"entity_id"`
		Attributes map[string]any `json:"attributes"`
	}
	if err := json.Unmarshal(body, &states); err != nil {
		t.Fatalf("parsing /api/states: %v", err)
	}
	if len(states) == 0 {
		t.Fatal("/api/states returned nothing — a capture that reads zero entities would write an empty fixture")
	}

	var items []realdata.HelperItem
	ghosts := 0
	for _, s := range states {
		domain := realdata.EntityDomain(s.EntityID)
		if !realdata.IsHelperDomain(domain) {
			continue
		}
		editable, stated := s.Attributes["editable"].(bool)
		if !stated {
			ghosts++
			continue
		}
		if !editable {
			continue // YAML-defined; it belongs in the config tree, not in .storage
		}

		config := map[string]any{}
		for _, key := range realdata.CarriedAttributes(domain) {
			if v, present := s.Attributes[key]; present {
				config[key] = v
			}
		}
		name, _ := s.Attributes["friendly_name"].(string)
		icon, _ := s.Attributes["icon"].(string)
		_, objectID, _ := cutEntityID(s.EntityID)
		if domain == "schedule" {
			// The weekday blocks are not in the state. HA publishes
			// `next_event` there and keeps the blocks in the collection, so a
			// capture built on attributes alone wrote thirteen empty schedules
			// — the shape lost quietly, which is what S4 exists to prevent.
			// The generator now refuses that, and this is where the blocks come
			// from instead.
			maps.Copy(config, scheduleBlocks(t, tgt, s.EntityID))
		}
		items = append(items, realdata.HelperItem{
			Domain: domain, ID: objectID, Name: name, Icon: icon, Config: config,
		})
	}
	if ghosts > 0 {
		t.Logf("skipped %d helper entities with no `editable` — restored ghosts, nothing to reconstruct from", ghosts)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].Domain != items[j].Domain {
			return items[i].Domain < items[j].Domain
		}
		return items[i].ID < items[j].ID
	})
	return items
}

// scheduleBlocks reads one schedule's weekday blocks through `helper show`,
// which is the only route that renders a storage collection's own document.
//
// The companion returns the definition as YAML keyed by the collection id, so
// the outer mapping is unwrapped and the seven weekday keys are taken from
// inside it. Every weekday is returned whether or not the schedule uses it: an
// absent key and an empty one mean different things to Home Assistant, and the
// generator requires presence.
func scheduleBlocks(t *testing.T, tgt Target, entityID string) map[string]any {
	t.Helper()

	out, err := tgt.Read(t, "helper", "show", entityID, "--json")
	if err != nil {
		t.Fatalf("reading %s: %v\n%s", entityID, err, truncate(out))
	}
	var shown struct {
		Content string `json:"content"`
	}
	if err := json.Unmarshal([]byte(out), &shown); err != nil {
		t.Fatalf("parsing `helper show %s --json`: %v\n%s", entityID, err, truncate(out))
	}

	var wrapper map[string]map[string]any
	if err := yaml.Unmarshal([]byte(shown.Content), &wrapper); err != nil {
		t.Fatalf("parsing the definition of %s: %v\n%s", entityID, err, truncate(shown.Content))
	}
	blocks := map[string]any{}
	for _, definition := range wrapper {
		for _, day := range []string{"monday", "tuesday", "wednesday", "thursday", "friday", "saturday", "sunday"} {
			if value, present := definition[day]; present {
				blocks[day] = value
			} else {
				blocks[day] = []any{}
			}
		}
	}
	if len(blocks) == 0 {
		t.Fatalf("`helper show %s` returned a definition with no weekday keys:\n%s", entityID, truncate(shown.Content))
	}
	return blocks
}

func cutEntityID(entityID string) (domain, object string, found bool) {
	for i := range len(entityID) {
		if entityID[i] == '.' {
			return entityID[:i], entityID[i+1:], true
		}
	}
	return entityID, "", false
}

func countDomains(items []realdata.HelperItem) int {
	seen := map[string]bool{}
	for _, i := range items {
		seen[i.Domain] = true
	}
	return len(seen)
}

func init() {
	// A capture writes into the repository's testdata. Saying so once, here,
	// beats discovering it from a dirty worktree.
	if os.Getenv("HACTL_CAPTURE_FIXTURE") != "" {
		fmt.Fprintln(os.Stderr, "HACTL_CAPTURE_FIXTURE is set: the capture tests will REWRITE "+
			"testdata/fixtures/realistic-instance/.storage and its captured YAML")
	}
}
