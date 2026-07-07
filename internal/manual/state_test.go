package manual

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 7, 6, 12, 0, 0, 0, time.UTC)

func TestClaimProgressiveOncePerSession(t *testing.T) {
	dir := t.TempDir()

	first := Claim(dir, "s1", ModeProgressive, "health", t0)
	if !strings.HasPrefix(first, CoreNote) {
		t.Fatalf("first claim missing core note: %.60q", first)
	}
	for _, want := range []string{"## Quick routing", "'health' family how-to", "### Setup & health"} {
		if !strings.Contains(first, want) {
			t.Errorf("first claim missing %q", want)
		}
	}

	if again := Claim(dir, "s1", ModeProgressive, "health", t0.Add(time.Minute)); again != "" {
		t.Errorf("second health claim should be empty, got %.60q", again)
	}

	auto := Claim(dir, "s1", ModeProgressive, "auto", t0.Add(2*time.Minute))
	if strings.Contains(auto, CoreNote) {
		t.Error("core delivered twice")
	}
	if !strings.Contains(auto, "'auto' family how-to") || !strings.Contains(auto, "### Write path (automations)") {
		t.Errorf("auto claim missing family section: %.60q", auto)
	}

	// Overlapping heading (label/ent share "Organize entities with labels")
	// must not be delivered twice.
	label := Claim(dir, "s1", ModeProgressive, "ent", t0.Add(3*time.Minute))
	_ = Claim(dir, "s1", ModeProgressive, "label", t0.Add(4*time.Minute))
	relabel := Claim(dir, "s1", ModeProgressive, "label", t0.Add(5*time.Minute))
	if relabel != "" {
		t.Errorf("label already delivered, got %.60q", relabel)
	}
	if !strings.Contains(label, `### "Organize entities with labels"`) {
		t.Error("ent should carry the shared labels workflow")
	}
}

func TestClaimSessionIsolationAndTTL(t *testing.T) {
	dir := t.TempDir()

	_ = Claim(dir, "s1", ModeProgressive, "health", t0)
	other := Claim(dir, "s2", ModeProgressive, "health", t0.Add(time.Minute))
	if !strings.HasPrefix(other, CoreNote) {
		t.Error("distinct session should get its own core delivery")
	}

	// Sliding TTL: activity at +20m keeps the session alive at +40m…
	if txt := Claim(dir, "s1", ModeProgressive, "", t0.Add(20*time.Minute)); txt != "" {
		t.Errorf("expected silent refresh, got %.60q", txt)
	}
	if txt := Claim(dir, "s1", ModeProgressive, "", t0.Add(40*time.Minute)); txt != "" {
		t.Errorf("session should still be alive at +40m, got %.60q", txt)
	}
	// …but 31 idle minutes expire it and the core is re-primed.
	reprimed := Claim(dir, "s1", ModeProgressive, "", t0.Add(71*time.Minute+time.Second))
	if !strings.HasPrefix(reprimed, CoreNote) {
		t.Error("expired session should re-prime the core")
	}
}

func TestClaimFullMode(t *testing.T) {
	dir := t.TempDir()

	full := Claim(dir, "s1", ModeFull, "health", t0)
	if !strings.HasPrefix(full, FullNote) || !strings.Contains(full, "## MCP server") {
		t.Fatalf("full mode should deliver the whole manual: %.60q", full)
	}
	if again := Claim(dir, "s1", ModeFull, "auto", t0.Add(time.Minute)); again != "" {
		t.Errorf("full already delivered, got %.60q", again)
	}
	// full short-circuits progressive too
	if prog := Claim(dir, "s1", ModeProgressive, "auto", t0.Add(2*time.Minute)); prog != "" {
		t.Errorf("full session must not re-deliver progressively, got %.60q", prog)
	}
}

func TestClaimOffMode(t *testing.T) {
	dir := t.TempDir()
	if txt := Claim(dir, "s1", ModeOff, "health", t0); txt != "" {
		t.Errorf("off mode must deliver nothing, got %.60q", txt)
	}
	if _, err := os.Stat(filepath.Join(dir, stateFileName)); !os.IsNotExist(err) {
		t.Error("off mode must not touch state")
	}
}

func TestMarkDelivered(t *testing.T) {
	dir := t.TempDir()

	MarkDelivered(dir, "s1", t0, "all") // full rtfm
	if txt := Claim(dir, "s1", ModeProgressive, "auto", t0.Add(time.Minute)); txt != "" {
		t.Errorf("rtfm marked all; claim should be empty, got %.60q", txt)
	}

	MarkDelivered(dir, "s2", t0, "core", "auto") // rtfm --core --family auto
	txt := Claim(dir, "s2", ModeProgressive, "auto", t0.Add(time.Minute))
	if txt != "" {
		t.Errorf("core+auto marked; claim should be empty, got %.60q", txt)
	}
	ent := Claim(dir, "s2", ModeProgressive, "ent", t0.Add(2*time.Minute))
	if strings.Contains(ent, CoreNote) || !strings.Contains(ent, "'ent' family how-to") {
		t.Errorf("ent should deliver family only: %.60q", ent)
	}
}

func TestClaimFailOpen(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, stateFileName)
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if txt := Claim(dir, "s1", ModeProgressive, "health", t0); !strings.HasPrefix(txt, CoreNote) {
		t.Error("corrupt state must fail open and inject")
	}
	// …and the rewrite healed the file.
	if again := Claim(dir, "s1", ModeProgressive, "health", t0.Add(time.Minute)); again != "" {
		t.Errorf("state not healed after corrupt read, got %.60q", again)
	}

	// No cache dir at all: inject every time, never persist.
	if txt := Claim("", "s1", ModeProgressive, "health", t0); !strings.HasPrefix(txt, CoreNote) {
		t.Error("empty cacheDir must still inject")
	}
	if txt := Claim("", "s1", ModeProgressive, "health", t0); !strings.HasPrefix(txt, CoreNote) {
		t.Error("empty cacheDir cannot remember state; must inject again")
	}
}

func TestSaveLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	_ = Claim(dir, "s1", ModeProgressive, "health", t0)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != stateFileName {
			t.Errorf("unexpected file in cache dir: %s", e.Name())
		}
	}
}

func TestPruneStaleSessions(t *testing.T) {
	dir := t.TempDir()
	_ = Claim(dir, "old", ModeProgressive, "health", t0)
	_ = Claim(dir, "new", ModeProgressive, "health", t0.Add(31*time.Minute))

	raw, err := os.ReadFile(filepath.Clean(filepath.Join(dir, stateFileName)))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"old"`) {
		t.Error("stale session not pruned from state file")
	}
	if !strings.Contains(string(raw), `"new"`) {
		t.Error("active session missing from state file")
	}
}

func TestHowToPending(t *testing.T) {
	dir := t.TempDir()

	if !HowToPending(dir, "s1", ModeProgressive, "dash", t0) {
		t.Error("fresh session: dash how-to must be pending")
	}
	// Read-only: the check itself must not mark anything delivered.
	if !HowToPending(dir, "s1", ModeProgressive, "dash", t0) {
		t.Error("HowToPending must not consume the pending state")
	}

	_ = Claim(dir, "s1", ModeProgressive, "dash", t0)
	if HowToPending(dir, "s1", ModeProgressive, "dash", t0.Add(time.Minute)) {
		t.Error("after Claim the dash how-to is delivered, nothing pending")
	}
	if !HowToPending(dir, "s1", ModeProgressive, "svc", t0.Add(time.Minute)) {
		t.Error("svc how-to still pending after only dash was delivered")
	}

	// Full mode pends until the full manual went out.
	if !HowToPending(dir, "s2", ModeFull, "dash", t0) {
		t.Error("full mode, nothing delivered: pending")
	}
	_ = Claim(dir, "s2", ModeFull, "dash", t0)
	if HowToPending(dir, "s2", ModeFull, "dash", t0.Add(time.Minute)) {
		t.Error("full manual delivered: nothing pending")
	}

	// Fail-open cases.
	if HowToPending("", "s1", ModeProgressive, "dash", t0) {
		t.Error("stateless delivery must report nothing pending")
	}
	if HowToPending(dir, "s3", ModeOff, "dash", t0) {
		t.Error("mode off must report nothing pending")
	}
}
