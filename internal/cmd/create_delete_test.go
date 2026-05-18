package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAutoCreateDryRun(t *testing.T) {
	// Create a temp YAML file
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "test_auto.yaml")
	if err := os.WriteFile(yamlFile, []byte("id: test_auto\nalias: Test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"auto", "create", "-f", yamlFile})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("auto create dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
	if !strings.Contains(out, "test_auto.yaml") {
		t.Errorf("expected file name in output, got: %q", out)
	}
}

func TestAutoCreateMissingFile(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs([]string{"auto", "create"})

	err := rootCmd.Execute()
	if err == nil {
		t.Fatal("expected error for missing -f flag")
	}
}

func TestAutoDeleteDryRun(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"auto", "delete", "test_automation_id"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("auto delete dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
	if !strings.Contains(out, "test_automation_id") {
		t.Errorf("expected automation ID in output, got: %q", out)
	}
}

func TestScriptCreateDryRun(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "test_script.yaml")
	if err := os.WriteFile(yamlFile, []byte("test_script:\n  alias: Test\n  sequence: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"script", "create", "-f", yamlFile})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("script create dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
}

func TestScriptDeleteDryRun(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"script", "delete", "welcome_home"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("script delete dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
}

func TestTplCreateDryRun(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "test_tpl.yaml")
	if err := os.WriteFile(yamlFile, []byte("unique_id: test_tpl\nname: Test\nstate: '{{ 42 }}'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"tpl", "create", "-f", yamlFile})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("tpl create dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
}

func TestTplDeleteDryRun(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"tpl", "delete", "test_uid"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("tpl delete dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
}

func TestHelperCreateDryRun(t *testing.T) {
	dir := t.TempDir()
	yamlFile := filepath.Join(dir, "test_helper.yaml")
	if err := os.WriteFile(yamlFile, []byte("test_toggle:\n  name: Test Toggle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"helper", "create", "input_boolean", "-f", yamlFile})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("helper create dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
	if !strings.Contains(out, "input_boolean") {
		t.Errorf("expected domain in output, got: %q", out)
	}
}

func TestHelperDeleteDryRun(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"helper", "delete", "guest_mode"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("helper delete dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
}

func TestLabelDeleteDryRun(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"label", "delete", "test_label"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("label delete dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
}

func TestAreaDeleteDryRun(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"area", "delete", "test_area"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("area delete dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
}

func TestFloorDeleteDryRun(t *testing.T) {
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"floor", "delete", "test_floor"})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("floor delete dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
}
