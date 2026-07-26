package ids

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGetOrCreate_Consistent(t *testing.T) {
	reg := NewRegistry(filepath.Join(t.TempDir(), "ids.json"))

	id1 := reg.GetOrCreate("trc", "automation.test/run1")
	id2 := reg.GetOrCreate("trc", "automation.test/run1")

	if id1 != id2 {
		t.Errorf("inconsistent IDs: %q vs %q", id1, id2)
	}
}

func TestGetOrCreate_DifferentKeys(t *testing.T) {
	reg := NewRegistry(filepath.Join(t.TempDir(), "ids.json"))

	id1 := reg.GetOrCreate("trc", "automation.test/run1")
	id2 := reg.GetOrCreate("trc", "automation.test/run2")

	if id1 == id2 {
		t.Errorf("different keys got same ID: %q", id1)
	}
}

func TestGetOrCreate_HasPrefix(t *testing.T) {
	reg := NewRegistry(filepath.Join(t.TempDir(), "ids.json"))

	id := reg.GetOrCreate("trc", "something")
	if len(id) < 5 { // "trc:" + at least 2 hex chars
		t.Errorf("ID too short: %q", id)
	}
	if id[:4] != "trc:" {
		t.Errorf("ID missing prefix: %q", id)
	}
}

func TestResolve_Found(t *testing.T) {
	reg := NewRegistry(filepath.Join(t.TempDir(), "ids.json"))

	id := reg.GetOrCreate("trc", "mykey")
	key, ok := reg.Resolve(id)
	if !ok {
		t.Fatalf("Resolve(%q) returned false", id)
	}
	if key != "mykey" {
		t.Errorf("Resolve(%q) = %q, want %q", id, key, "mykey")
	}
}

func TestResolve_NotFound(t *testing.T) {
	reg := NewRegistry(filepath.Join(t.TempDir(), "ids.json"))

	_, ok := reg.Resolve("trc:zz")
	if ok {
		t.Fatal("Resolve for unknown ID should return false")
	}
}

func TestSaveAndLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache", "ids.json")

	// Create and save
	reg1 := NewRegistry(path)
	id := reg1.GetOrCreate("trc", "test_key")
	if err := reg1.Save(); err != nil {
		t.Fatalf("Save failed: %v", err)
	}

	// Verify file exists
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("ids.json not created: %v", err)
	}

	// Load into new registry
	reg2 := NewRegistry(path)
	if err := reg2.Load(); err != nil {
		t.Fatalf("Load failed: %v", err)
	}

	// Same key should get same ID
	id2 := reg2.GetOrCreate("trc", "test_key")
	if id != id2 {
		t.Errorf("ID changed after reload: %q → %q", id, id2)
	}

	// Resolve should work
	key, ok := reg2.Resolve(id)
	if !ok {
		t.Fatalf("Resolve(%q) returned false after load", id)
	}
	if key != "test_key" {
		t.Errorf("Resolve(%q) = %q, want %q", id, key, "test_key")
	}
}

func TestLoad_NoFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent", "ids.json")
	reg := NewRegistry(path)
	if err := reg.Load(); err != nil {
		t.Fatalf("Load on missing file should not error: %v", err)
	}

	// A first run has no ids file. Load must swallow only ENOENT — and it must
	// leave a *usable empty* registry behind, not a half-initialised one: the
	// maps a later GetOrCreate writes into are allocated by NewRegistry and a
	// Load that replaced them with a nil map would panic on the first ID.
	if _, ok := reg.Resolve("trc:00"); ok {
		t.Error("Resolve on a registry loaded from no file claimed to know an ID")
	}
	id := reg.GetOrCreate("trc", "first_key")
	if !strings.HasPrefix(id, "trc:") {
		t.Errorf("GetOrCreate after an empty Load = %q, want a trc:-prefixed ID", id)
	}
	key, ok := reg.Resolve(id)
	if !ok || key != "first_key" {
		t.Errorf("Resolve(%q) = (%q, %v), want (%q, true)", id, key, ok, "first_key")
	}

	// Load reads; it does not create. A Load that wrote the file (or its parent
	// directory) would leave state on disk for a command that only looked.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("os.Stat(%q) after Load = %v, want a not-exist error", path, statErr)
	}
}

func TestGetOrCreate_DifferentPrefixes(t *testing.T) {
	reg := NewRegistry(filepath.Join(t.TempDir(), "ids.json"))

	id1 := reg.GetOrCreate("trc", "samekey")
	id2 := reg.GetOrCreate("iss", "samekey")

	if id1 == id2 {
		t.Errorf("same key with different prefixes should produce different IDs: %q vs %q", id1, id2)
	}
	if id1[:4] != "trc:" {
		t.Errorf("first ID has wrong prefix: %q", id1)
	}
	if id2[:4] != "iss:" {
		t.Errorf("second ID has wrong prefix: %q", id2)
	}
}
