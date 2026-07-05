package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSvcCallCmd_InvalidFormat(t *testing.T) {
	rootCmd.SetArgs([]string{"svc", "call", "badformat"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid service format")
	}
}

func TestSvcCallCmd_DryRunByDefault(t *testing.T) {
	// No --confirm: prints the plan and returns before any config load or
	// network call, so it must succeed even without an instance .env.
	var buf bytes.Buffer
	err := RunWithOutput([]string{"hactl", "svc", "call", "automation.turn_off",
		"--dir", t.TempDir(), "--data", `{"entity_id":"automation.x"}`}, &buf)
	if err != nil {
		t.Fatalf("dry-run must not error: %v", err)
	}
	out := buf.String()
	for _, want := range []string{"dry-run", "automation.turn_off", `"automation.x"`, "--confirm"} {
		if !strings.Contains(out, want) {
			t.Errorf("dry-run output missing %q, got %q", want, out)
		}
	}
}

func TestSvcCallCmd_ConfirmRequiresInstance(t *testing.T) {
	// With --confirm the call proceeds past the dry-run gate and fails on
	// the missing instance config — proving the gate no longer short-circuits.
	var buf bytes.Buffer
	err := RunWithOutput([]string{"hactl", "svc", "call", "automation.turn_off",
		"--dir", t.TempDir(), "--confirm"}, &buf)
	if err == nil {
		t.Fatal("expected config error for --confirm without instance")
	}
}

func TestSvcCallCmd_InvalidJSON(t *testing.T) {
	flagSvcData = "not json"
	rootCmd.SetArgs([]string{"svc", "call", "test.service", "--dir", t.TempDir(), "--data", "not json"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid JSON data")
	}
	flagSvcData = "{}"
}

func TestResolveData_Inline(t *testing.T) {
	data, err := resolveData(`{"key":"value"}`)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != `{"key":"value"}` {
		t.Errorf("resolveData inline = %q, want %q", string(data), `{"key":"value"}`)
	}
}

func TestResolveData_File(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "data.json")
	if err := os.WriteFile(p, []byte(`{"from":"file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := resolveData("@" + p)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != `{"from":"file"}` {
		t.Errorf("resolveData @file = %q, want %q", string(data), `{"from":"file"}`)
	}
}

func TestResolveData_FileMissing(t *testing.T) {
	_, err := resolveData("@/nonexistent/path.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestResolveData_EmptyDefault(t *testing.T) {
	data, err := resolveData("{}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(data) != "{}" {
		t.Errorf("resolveData empty = %q, want %q", string(data), "{}")
	}
}
