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

	captureConfigTree(t, &s)

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

// captureConfigTree derives the fixture's hand-written YAML from the archived
// snapshot rather than from a fresh read.
//
// SPEC-realdata-fixture.md §5: the config tree is already captured
// (`_archive/livefire-2026-07-30/snapshot/`), so no live read is needed and
// none is made. That is also what makes this half reproducible in a way the
// live half is not — the snapshot is frozen, the instance is not.
//
// The generator REFUSES on shape drift instead of writing a smaller file. That
// is S4, and it is not theoretical: the first run of this dropped eight
// `unique_id` values carrying an umlaut, because they were being run through
// the entity-slug sanitizer and a slug has no room for one. Nothing would have
// reported it — the file would simply have been tidier than the house.
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

	for _, name := range []string{"template.yaml"} {
		src, readErr := os.ReadFile(filepath.Clean(filepath.Join(snapshot, name)))
		if readErr != nil {
			t.Fatalf("reading %s from the snapshot at %s: %v", name, snapshot, readErr)
		}
		before := realdata.MeasureConfig(string(src))
		out := realdata.SanitizeConfigText(string(src), s)
		if drift := realdata.ShapeDrift(before, realdata.MeasureConfig(out)); len(drift) > 0 {
			t.Fatalf("sanitizing %s changed its shape, so the fixture would carry less than the "+
				"instance does: %v", name, drift)
		}
		dst := filepath.Join(fixtureDir(t), name)
		//nolint:gosec // G703: `name` is a constant from the loop above and the
		// directory is this repository's own testdata; the taint gosec follows
		// is HACTL_SNAPSHOT_DIR, which selects what is READ, never where the
		// result is written.
		if writeErr := os.WriteFile(dst, []byte(out), 0o600); writeErr != nil {
			t.Fatalf("writing %s: %v", dst, writeErr)
		}
		t.Logf("wrote %s — %d lines, %d top-level blocks, %d unique_ids, %d lines with non-ASCII",
			name, before.Lines, before.TopLevelDash, before.UniqueIDs, before.NonASCII)
	}
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
		fmt.Fprintln(os.Stderr, "HACTL_CAPTURE_FIXTURE is set: TestCaptureRealDataFixture will REWRITE testdata/fixtures/realistic-instance/.storage")
	}
}
