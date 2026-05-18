package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSvcCallCmd_InvalidFormat(t *testing.T) {
	rootCmd.SetArgs([]string{"svc", "call", "badformat"})
	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid service format")
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
