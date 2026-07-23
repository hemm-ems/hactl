//go:build integration

package integration

import (
	"context"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/haapi"
)

func TestEntLs(t *testing.T) {
	out := runHactl(t, "ent", "ls")

	// Should show entities — at minimum HA creates some built-in entities
	if !strings.Contains(out, "entity_id") {
		t.Errorf("ent ls output missing 'entity_id' header: %s", out)
	}
}

func TestEntLsPattern(t *testing.T) {
	// Filter by a pattern that matches something (person.* should exist after onboarding)
	out := runHactl(t, "ent", "ls", "--pattern", "person.*")
	// Should succeed without error; may or may not have results
	_ = out
}

func TestEntLsJSON(t *testing.T) {
	out := runHactl(t, "ent", "ls", "--json")
	trimmed := strings.TrimSpace(out)
	if trimmed != "" && !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
		t.Errorf("ent ls --json did not produce JSON: %s", out)
	}
}

func TestEntLsJSONSchema(t *testing.T) {
	entries := runHactlJSON[[]map[string]string](t, "ent", "ls")
	if len(entries) == 0 {
		t.Fatal("ent ls returned no entities")
	}
	first := entries[0]
	for _, key := range []string{"entity_id", "state", "last_changed"} {
		if _, ok := first[key]; !ok {
			t.Errorf("ent ls --json entry missing key %q", key)
		}
	}
}

func TestEntLsPatternSun(t *testing.T) {
	out := runHactl(t, "ent", "ls", "--pattern", "sun.*")
	assertContains(t, out, "sun.sun")
}

func TestEntLsPatternSubstring(t *testing.T) {
	// Bare substring (no glob chars) should match
	out := runHactl(t, "ent", "ls", "--pattern", "sun")
	assertContains(t, out, "sun.sun")
}

func TestEntShowSun(t *testing.T) {
	// sun.sun is always present in HA
	out := runHactl(t, "ent", "show", "sun.sun")
	if !strings.Contains(out, "sun.sun") {
		t.Errorf("ent show output missing 'sun.sun': %s", out)
	}
}

func TestEntShowJSON(t *testing.T) {
	out := runHactl(t, "ent", "show", "sun.sun", "--json")
	if !strings.Contains(out, "sun.sun") {
		t.Errorf("ent show --json missing 'sun.sun': %s", out)
	}
}

func TestEntShowUnknown(t *testing.T) {
	_, err := runHactlErr(t, "ent", "show", "sensor.nonexistent_abc_xyz")
	if err == nil {
		t.Error("ent show nonexistent entity expected error, got nil")
	}
}

func TestEntHist(t *testing.T) {
	// sun.sun always exists and has state changes; history should return something
	out := runHactl(t, "ent", "hist", "sun.sun", "--since", "1h")
	// Should show table with timestamp column or "no history" message
	if !strings.Contains(out, "time") && !strings.Contains(out, "no history") && !strings.Contains(out, "no numeric") {
		t.Errorf("ent hist unexpected output: %s", out)
	}
}

func TestEntHistResample(t *testing.T) {
	// Custom resample interval; should not error
	out, err := runHactlErr(t, "ent", "hist", "sun.sun", "--since", "1h", "--resample", "5m")
	if err != nil {
		// sun.sun may not be numeric, so "no numeric" is acceptable
		if !strings.Contains(out, "no numeric") && !strings.Contains(out, "no history") {
			t.Errorf("ent hist --resample failed unexpectedly: %v\noutput: %s", err, out)
		}
		return
	}
	_ = out
}

// Inverted 2026-07-23: this test used to accept exit 0 with "no history data"
// for an entity that does not exist, on the grounds that "HA may return empty
// history (no error) for nonexistent entities". HA's behaviour is not the
// question — hactl's is. An empty answer and a nonexistent entity_id must not
// look the same, or a typo reads as a verified negative. See
// unknown_entity_test.go for the family-wide gate.
func TestEntHistUnknown(t *testing.T) {
	out, err := runHactlErr(t, "ent", "hist", "sensor.nonexistent_abc_xyz", "--since", "1h")
	if err == nil {
		t.Errorf("ent hist on a nonexistent entity exited 0; output:\n%s", out)
	}
}

// A resample bucket the resampler cannot honour must be refused, not ignored.
func TestEntHistResampleRejectsNonPositive(t *testing.T) {
	for _, bad := range []string{"0m", "-5m"} {
		out, err := runHactlErr(t, "ent", "hist", "sun.sun", "--since", "1h", "--resample", bad)
		if err == nil {
			t.Errorf("ent hist --resample %s exited 0; the value was silently ignored.\noutput:\n%s", bad, out)
		}
	}
}

func TestEntAnomalies(t *testing.T) {
	// Run anomalies on sun.sun — likely "no anomalies" which is valid
	out, err := runHactlErr(t, "ent", "anomalies", "sun.sun", "--since", "1h")
	if err != nil {
		// May fail if no numeric history; that's acceptable
		if !strings.Contains(out, "no numeric") && !strings.Contains(out, "no history") {
			t.Errorf("ent anomalies failed unexpectedly: %v\noutput: %s", err, out)
		}
		return
	}
	// Output should be "no anomalies" or an anomalies table
	assertNotContains(t, out, "panic")
}

func TestEntAnomaliesUnknown(t *testing.T) {
	out, err := runHactlErr(t, "ent", "anomalies", "sensor.nonexistent_abc_xyz", "--since", "1h")
	if err != nil {
		// Error is acceptable
		return
	}
	// HA may return empty history for nonexistent entities
	if !strings.Contains(out, "no numeric") && !strings.Contains(out, "no anomalies") && !strings.Contains(out, "no history") {
		t.Errorf("ent anomalies nonexistent entity: expected error or 'no numeric'/'no history', got: %s", out)
	}
}

func TestWebSocketConnection(t *testing.T) {
	cfg := loadConfig(t)
	ctx := context.Background()

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("WebSocket connect failed: %v", err)
	}
	defer func() { _ = ws.Close() }()

	// TraceList should succeed (may be empty)
	_, err := ws.TraceList(ctx, "automation")
	if err != nil {
		t.Fatalf("TraceList failed: %v", err)
	}
}

func TestEntShowFull(t *testing.T) {
	// sun.sun always has attributes (elevation, azimuth, etc.)
	out := runHactl(t, "ent", "show", "sun.sun", "--full")
	assertContains(t, out, "sun.sun")
	// --full should show extra attributes beyond the default set
	// sun.sun typically has elevation, azimuth, rising, next_dawn etc.
	if !strings.Contains(out, "elevation") && !strings.Contains(out, "rising") && !strings.Contains(out, "next_") {
		t.Errorf("ent show --full should show attributes for sun.sun, got:\n%s", out)
	}
}

func TestEntShowFullMoreThanDefault(t *testing.T) {
	// Default output should be shorter than --full output
	defaultOut := runHactl(t, "ent", "show", "sun.sun")
	fullOut := runHactl(t, "ent", "show", "sun.sun", "--full")
	if len(fullOut) <= len(defaultOut) {
		t.Errorf("ent show --full (%d bytes) should be longer than default (%d bytes)",
			len(fullOut), len(defaultOut))
	}
}

func TestEntLsDomain(t *testing.T) {
	out := runHactl(t, "ent", "ls", "--domain", "sun")
	assertContains(t, out, "sun.sun")
}

func TestEntLsDomainPerson(t *testing.T) {
	out := runHactl(t, "ent", "ls", "--domain", "person")
	assertContains(t, out, "person.")
}

func TestEntLsDomainJSON(t *testing.T) {
	entries := runHactlJSON[[]map[string]string](t, "ent", "ls", "--domain", "sun")
	if len(entries) == 0 {
		t.Fatal("ent ls --domain sun returned no entities")
	}
	for _, e := range entries {
		if !strings.HasPrefix(e["entity_id"], "sun.") {
			t.Errorf("ent ls --domain sun returned non-sun entity: %s", e["entity_id"])
		}
	}
}

func TestEntLsDomainNoMatch(t *testing.T) {
	// A zero-match domain teaches instead of printing an empty table.
	out := runHactl(t, "ent", "ls", "--domain", "nonexistent_domain_xyz")
	assertContains(t, out, "verify the domain exists")

	// The classic trap gets a redirect to the right command.
	out = runHactl(t, "ent", "ls", "--domain", "helper")
	assertContains(t, out, "hactl helper ls")
}

func TestEntLsDomainCombinedWithPattern(t *testing.T) {
	// --domain and --pattern should stack
	out := runHactl(t, "ent", "ls", "--domain", "sun", "--pattern", "sun.sun")
	assertContains(t, out, "sun.sun")
}

func TestEntLsHasAreaColumn(t *testing.T) {
	out := runHactl(t, "ent", "ls")
	assertContains(t, out, "area")
}

func TestEntLsHasLabelsColumn(t *testing.T) {
	out := runHactl(t, "ent", "ls")
	assertContains(t, out, "labels")
}

func TestEntRelated(t *testing.T) {
	out := runHactl(t, "ent", "related", "sun.sun")
	// Should succeed; may show no relations for sun.sun
	assertNotContains(t, out, "panic")
}

func TestEntRelatedUnknown(t *testing.T) {
	out, err := runHactlErr(t, "ent", "related", "sensor.nonexistent_abc_xyz")
	// May succeed with empty output or error — both acceptable
	if err == nil {
		assertNotContains(t, out, "panic")
	}
}

func TestWebSocketRegistryList(t *testing.T) {
	cfg := loadConfig(t)
	ctx := context.Background()

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("WebSocket connect failed: %v", err)
	}
	defer func() { _ = ws.Close() }()

	// Entity registry should have entries
	entities, err := ws.EntityRegistryList(ctx)
	if err != nil {
		t.Fatalf("EntityRegistryList failed: %v", err)
	}
	if len(entities) == 0 {
		t.Error("EntityRegistryList returned 0 entities")
	}

	// Area, label, floor registries should succeed (may be empty)
	if _, err := ws.AreaRegistryList(ctx); err != nil {
		t.Errorf("AreaRegistryList failed: %v", err)
	}
	if _, err := ws.LabelRegistryList(ctx); err != nil {
		t.Errorf("LabelRegistryList failed: %v", err)
	}
	if _, err := ws.FloorRegistryList(ctx); err != nil {
		t.Errorf("FloorRegistryList failed: %v", err)
	}
}

func TestWebSocketLabelCreateAndList(t *testing.T) {
	cfg := loadConfig(t)
	ctx := context.Background()

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("WebSocket connect failed: %v", err)
	}
	defer func() { _ = ws.Close() }()

	// Create a label
	entry, err := ws.LabelRegistryCreate(ctx, "test-label", "red", "mdi:test-tube", "integration test label")
	if err != nil {
		t.Fatalf("LabelRegistryCreate failed: %v", err)
	}
	if entry.Name != "test-label" {
		t.Errorf("created label name = %q, want test-label", entry.Name)
	}

	// Verify it appears in list
	labels, err := ws.LabelRegistryList(ctx)
	if err != nil {
		t.Fatalf("LabelRegistryList failed: %v", err)
	}
	found := false
	for _, l := range labels {
		if l.Name == "test-label" {
			found = true
			break
		}
	}
	if !found {
		t.Error("created label not found in LabelRegistryList")
	}
}
