package cmd

import (
	"bytes"
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

// TestSvcCallAgreesWithItselfOnAMissingInstance — INVERTED, and merged from
// two tests that sat next to each other documenting a disagreement without
// naming it.
//
// TestSvcCallCmd_DryRunByDefault asserted that a preview "returns before any
// config load or network call, so it must succeed even without an instance
// .env", while TestSvcCallCmd_ConfirmRequiresInstance asserted that --confirm
// fails on exactly the same argument. Both passed. That gap is the whole of
// H-2: a preview must fail where the confirmed run would, and a preview that
// never contacts Home Assistant cannot check that the service it is describing
// exists — so "would call: light.turn_onn" was the artifact a human approved.
func TestSvcCallAgreesWithItselfOnAMissingInstance(t *testing.T) {
	for _, confirm := range []bool{false, true} {
		name := "dry-run"
		args := []string{"hactl", "svc", "call", "automation.turn_off", "--dir", t.TempDir()}
		if confirm {
			name = "confirm"
			args = append(args, "--confirm")
		}
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := RunWithOutput(args, &buf); err == nil {
				t.Errorf("%s succeeded with no instance configured; the other half of this command fails on the same input:\n%s", name, buf.String())
			}
		})
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
