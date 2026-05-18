//go:build integration

package integration

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestScriptLs(t *testing.T) {
	out := runHactl(t, "script", "ls")
	assertContains(t, out, "id")
	assertContains(t, out, "state")
}

func TestScriptLsJSON(t *testing.T) {
	out := runHactl(t, "script", "ls", "--json")
	trimmed := strings.TrimSpace(out)
	if trimmed != "" && !strings.HasPrefix(trimmed, "[") && !strings.HasPrefix(trimmed, "{") {
		t.Errorf("script ls --json did not produce JSON-like output: %s", out)
	}
}

func TestScriptLsJSONSchema(t *testing.T) {
	entries := runHactlJSON[[]map[string]string](t, "script", "ls")
	if len(entries) == 0 {
		t.Skip("no scripts loaded in HA")
	}
	first := entries[0]
	for _, key := range []string{"id", "state"} {
		if _, ok := first[key]; !ok {
			t.Errorf("script ls --json entry missing key %q", key)
		}
	}
}

func TestScriptLsHasFixtureScripts(t *testing.T) {
	entries := runHactlJSON[[]map[string]string](t, "script", "ls")
	ids := make(map[string]bool)
	for _, e := range entries {
		ids[e["id"]] = true
	}
	for _, want := range []string{"kino_start", "kino_ende", "standby_activate"} {
		if !ids[want] {
			t.Errorf("script ls missing fixture script %q, got: %v", want, ids)
		}
	}
}

func TestScriptShow(t *testing.T) {
	entries := runHactlJSON[[]map[string]string](t, "script", "ls")
	if len(entries) == 0 {
		t.Skip("no scripts available for script show test")
	}
	scriptID := entries[0]["id"]

	out := runHactl(t, "script", "show", scriptID)
	assertContains(t, out, scriptID)
	assertContains(t, out, "state=")
	if !strings.Contains(out, "traces") {
		t.Errorf("script show missing traces section: %s", out)
	}
}

func TestScriptShowKinoStart(t *testing.T) {
	out, err := runHactlErr(t, "script", "show", "kino_start")
	if err != nil {
		t.Skip("kino_start not available")
	}
	assertContains(t, out, "kino_start")
	assertContains(t, out, "state=")
	assertContains(t, out, "mode=")
}

func TestScriptShowUnknown(t *testing.T) {
	_, err := runHactlErr(t, "script", "show", "nonexistent_script_xyz")
	if err == nil {
		t.Error("script show nonexistent_script_xyz expected error, got nil")
	}
}

func TestScriptLsPattern(t *testing.T) {
	out := runHactl(t, "script", "ls", "--pattern", "nonexistent_xyz")
	assertContains(t, out, "id") // header should still appear
}

func TestScriptLsPatternSubstring(t *testing.T) {
	// Verify substring matching works (the bug we fixed)
	entries := runHactlJSON[[]map[string]string](t, "script", "ls")
	if len(entries) == 0 {
		t.Skip("no scripts loaded in HA")
	}

	// "kino" should match both kino_start and kino_ende via substring
	out := runHactl(t, "script", "ls", "--pattern", "kino")
	assertContains(t, out, "kino_start")
	assertContains(t, out, "kino_ende")
}

func TestScriptLsPatternGlob(t *testing.T) {
	out := runHactl(t, "script", "ls", "--pattern", "kino_*")
	assertContains(t, out, "kino_start")
	assertContains(t, out, "kino_ende")
}

func TestScriptLsPatternJSON(t *testing.T) {
	var entries []map[string]string
	out := runHactl(t, "script", "ls", "--pattern", "kino", "--json")
	if err := json.Unmarshal([]byte(out), &entries); err != nil {
		t.Fatalf("script ls --pattern kino --json invalid: %v", err)
	}
	if len(entries) < 2 {
		t.Errorf("expected at least 2 kino scripts, got %d", len(entries))
	}
}

func TestScriptRun(t *testing.T) {
	// kino_start is a safe script (turns off a light that may not exist)
	out := runHactl(t, "script", "run", "kino_start")
	assertContains(t, out, "executed script.kino_start")
}

func TestScriptRunWithPrefix(t *testing.T) {
	out := runHactl(t, "script", "run", "script.kino_ende")
	assertContains(t, out, "executed script.kino_ende")
}

func TestScriptRunUnknown(t *testing.T) {
	_, err := runHactlErr(t, "script", "run", "nonexistent_script_xyz")
	if err == nil {
		t.Error("script run nonexistent should fail")
	}
}

func TestScriptRunNoArgs(t *testing.T) {
	_, err := runHactlErr(t, "script", "run")
	if err == nil {
		t.Error("script run without args should fail")
	}
}
