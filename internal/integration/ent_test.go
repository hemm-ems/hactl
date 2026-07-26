//go:build integration

package integration

import (
	"context"
	"encoding/json"
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

// TestEntLsPattern proves that --pattern selects, which needs two facts and not
// one: every row it returned matches, and the unfiltered listing held rows it
// did not return. A filter that silently matched everything satisfies the first
// alone, and a filter that silently matched nothing satisfies it vacuously —
// so the empty case is a failure here, not a pass.
func TestEntLsPattern(t *testing.T) {
	const domain = "person." // HA creates person.* during onboarding

	all := runHactlJSON[[]map[string]string](t, "ent", "ls")
	var wantMatches, wantExcluded int
	for _, e := range all {
		if strings.HasPrefix(e["entity_id"], domain) {
			wantMatches++
		} else {
			wantExcluded++
		}
	}
	if wantMatches == 0 || wantExcluded == 0 {
		t.Fatalf("precondition: need both %s* and non-%s* entities to prove a filter, got %d and %d",
			domain, domain, wantMatches, wantExcluded)
	}

	got := runHactlJSON[[]map[string]string](t, "ent", "ls", "--pattern", domain+"*")
	if len(got) != wantMatches {
		t.Errorf("ent ls --pattern %s* returned %d rows, HA has %d matching entities",
			domain, len(got), wantMatches)
	}
	for _, e := range got {
		if !strings.HasPrefix(e["entity_id"], domain) {
			t.Errorf("ent ls --pattern %s* returned %q, which does not match", domain, e["entity_id"])
		}
	}
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

// TestWebSocketConnection proves the WS handshake carries an identity and that
// HA answers a command over it with content.
//
// "Connect returned nil" is not enough on its own: it is equally consistent
// with a handshake that never authenticated. The wrong-token half is what makes
// the right-token half mean something — if hactl ever stopped sending
// auth/access_token, or HA stopped checking it, only that half goes red.
func TestWebSocketConnection(t *testing.T) {
	cfg := loadConfig(t)
	ctx := context.Background()

	ws := haapi.NewWSClient(cfg.URL, cfg.Token)
	if err := ws.Connect(ctx); err != nil {
		t.Fatalf("WebSocket connect failed: %v", err)
	}
	defer func() { _ = ws.Close() }()

	entries, err := ws.EntityRegistryList(ctx)
	if err != nil {
		t.Fatalf("EntityRegistryList over the WS connection failed: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("entity registry came back empty over WS; a running HA always has registered entities")
	}
	// The oracle is HA's own REST state list, not a hard-coded entity: which
	// entities are *registry*-backed depends on the fixture and on how HA has
	// grown (sun.sun, for instance, is a state without a registry entry here),
	// so naming one would pin the fixture rather than the transport. Every
	// enabled registry entry is an entity HA has loaded, so it must also have a
	// state — a WS reply that came back stale, truncated or belonging to
	// another instance fails this, an empty one fails above.
	client := haapi.New(cfg.URL, cfg.Token)
	rawStates, err := client.GetStates(ctx)
	if err != nil {
		t.Fatalf("get states over REST (the oracle for the WS registry): %v", err)
	}
	var restStates []struct {
		EntityID string `json:"entity_id"`
	}
	if err := json.Unmarshal(rawStates, &restStates); err != nil {
		t.Fatalf("decode states: %v", err)
	}
	haveState := map[string]bool{}
	for _, st := range restStates {
		haveState[st.EntityID] = true
	}
	matched := 0
	for _, e := range entries {
		if e.EntityID == "" {
			t.Errorf("entity registry entry with no entity_id: %+v", e)
			continue
		}
		if e.DisabledBy != "" {
			continue
		}
		if !haveState[e.EntityID] {
			t.Errorf("WS registry lists enabled %s, but HA's own state list over REST does not hold it",
				e.EntityID)
			continue
		}
		matched++
	}
	if matched == 0 {
		t.Errorf("none of the %d WS registry entries has a state in HA's REST state list; "+
			"the two sources describe different instances", len(entries))
	}

	bad := haapi.NewWSClient(cfg.URL, cfg.Token+"-not-the-token")
	if err := bad.Connect(ctx); err == nil {
		_ = bad.Close()
		t.Fatal("WS handshake accepted a wrong token; the authenticated connection above proves nothing")
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
