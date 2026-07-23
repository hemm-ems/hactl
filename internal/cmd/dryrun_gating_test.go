package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeGatingEnv writes a .env that points to a port that refuses connections,
// so connectCompanion will always fail with a "companion not found" error.
func writeGatingEnv(t *testing.T, dir string) {
	t.Helper()
	content := "HA_URL=http://127.0.0.1:19999\nHA_TOKEN=test\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeYAML writes a minimal valid YAML file for create tests.
func writeYAML(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestHelperCreate_DryRun_CompanionUnreachable verifies that a dry-run helper
// create fails with the companion-not-found error when the companion is unreachable,
// rather than falsely reporting "would create helper".
func TestHelperCreate_DryRun_CompanionUnreachable(t *testing.T) {
	dir := t.TempDir()
	writeGatingEnv(t, dir)
	// A valid keyed mapping: the point of this test is the companion gate, and
	// an unusable file would now be refused before we ever get there.
	yamlFile := writeYAML(t, dir, "toggle.yaml", "test_toggle:\n  name: Test Toggle\n")

	oldDir := flagDir
	flagDir = dir
	defer func() { flagDir = oldDir }()

	oldFile := flagHelperFile
	flagHelperFile = yamlFile
	defer func() { flagHelperFile = oldFile }()

	oldConfirm := flagHelperConfirm
	flagHelperConfirm = false
	defer func() { flagHelperConfirm = oldConfirm }()

	var out bytes.Buffer
	err := runHelperCreate(context.Background(), &out, "input_boolean")
	if err == nil {
		t.Fatalf("expected companion error on dry-run with unreachable companion, got nil; output: %s", out.String())
	}
	if !strings.Contains(err.Error(), "companion") {
		t.Errorf("error should mention 'companion', got: %v", err)
	}
	// Dry-run summary must NOT be printed when companion is unreachable.
	if strings.Contains(out.String(), "would create helper") {
		t.Errorf("dry-run should not print 'would create helper' when companion unreachable")
	}
}

// TestAutoCreate_DryRun_CompanionUnreachable verifies the same for automation create.
func TestAutoCreate_DryRun_CompanionUnreachable(t *testing.T) {
	dir := t.TempDir()
	writeGatingEnv(t, dir)
	yamlFile := writeYAML(t, dir, "auto.yaml", "alias: test\ntrigger: []\naction: []\n")

	oldDir := flagDir
	flagDir = dir
	defer func() { flagDir = oldDir }()

	oldFile := flagAutoFile
	flagAutoFile = yamlFile
	defer func() { flagAutoFile = oldFile }()

	oldConfirm := flagAutoConfirm
	flagAutoConfirm = false
	defer func() { flagAutoConfirm = oldConfirm }()

	var out bytes.Buffer
	err := runAutoCreate(context.Background(), &out)
	if err == nil {
		t.Fatalf("expected companion error on dry-run with unreachable companion, got nil; output: %s", out.String())
	}
	if !strings.Contains(err.Error(), "companion") {
		t.Errorf("error should mention 'companion', got: %v", err)
	}
}

// TestTplCreate_DryRun_CompanionUnreachable verifies the same for template create.
func TestTplCreate_DryRun_CompanionUnreachable(t *testing.T) {
	dir := t.TempDir()
	writeGatingEnv(t, dir)
	yamlFile := writeYAML(t, dir, "tpl.yaml", "name: test_sensor\nstate: '{{ 42 }}'\n")

	oldDir := flagDir
	flagDir = dir
	defer func() { flagDir = oldDir }()

	oldFile := flagTplFile
	flagTplFile = yamlFile
	defer func() { flagTplFile = oldFile }()

	oldConfirm := flagTplConfirm
	flagTplConfirm = false
	defer func() { flagTplConfirm = oldConfirm }()

	var out bytes.Buffer
	err := runTplCreate(context.Background(), &out)
	if err == nil {
		t.Fatalf("expected companion error on dry-run with unreachable companion, got nil; output: %s", out.String())
	}
	if !strings.Contains(err.Error(), "companion") {
		t.Errorf("error should mention 'companion', got: %v", err)
	}
}

// TestScriptCreate_DryRun_CompanionUnreachable verifies the same for script create.
func TestScriptCreate_DryRun_CompanionUnreachable(t *testing.T) {
	dir := t.TempDir()
	writeGatingEnv(t, dir)
	yamlFile := writeYAML(t, dir, "script.yaml", "test_script:\n  sequence: []\n")

	oldDir := flagDir
	flagDir = dir
	defer func() { flagDir = oldDir }()

	oldFile := flagScriptFile
	flagScriptFile = yamlFile
	defer func() { flagScriptFile = oldFile }()

	oldConfirm := flagScriptConfirm
	flagScriptConfirm = false
	defer func() { flagScriptConfirm = oldConfirm }()

	var out bytes.Buffer
	err := runScriptCreate(context.Background(), &out)
	if err == nil {
		t.Fatalf("expected companion error on dry-run with unreachable companion, got nil; output: %s", out.String())
	}
	if !strings.Contains(err.Error(), "companion") {
		t.Errorf("error should mention 'companion', got: %v", err)
	}
}

// TestHelperCreate_DryRun_LocalValidation verifies that a missing file is rejected
// before the companion is contacted (local validation > companion ping order).
func TestHelperCreate_DryRun_LocalValidation(t *testing.T) {
	dir := t.TempDir()
	writeGatingEnv(t, dir)

	oldDir := flagDir
	flagDir = dir
	defer func() { flagDir = oldDir }()

	oldFile := flagHelperFile
	flagHelperFile = filepath.Join(dir, "does_not_exist.yaml")
	defer func() { flagHelperFile = oldFile }()

	oldConfirm := flagHelperConfirm
	flagHelperConfirm = false
	defer func() { flagHelperConfirm = oldConfirm }()

	var out bytes.Buffer
	err := runHelperCreate(context.Background(), &out, "input_boolean")
	if err == nil {
		t.Fatal("expected error for missing YAML file, got nil")
	}
	// The error should be about the file, not the companion.
	if strings.Contains(err.Error(), "companion") {
		t.Errorf("local file error should not mention 'companion': %v", err)
	}
}
