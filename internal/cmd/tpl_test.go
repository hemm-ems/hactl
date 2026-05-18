package cmd

import (
	"os"
	"testing"
)

func TestResolveTemplate_InlineArg(t *testing.T) {
	flagTplFile = ""
	tpl, err := resolveTemplate([]string{"{{ states('sensor.x') }}"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tpl != "{{ states('sensor.x') }}" {
		t.Errorf("tpl = %q, want inline template", tpl)
	}
}

func TestResolveTemplate_NoArgNoFile(t *testing.T) {
	flagTplFile = ""
	_, err := resolveTemplate(nil)
	if err == nil {
		t.Fatal("expected error when no args and no file")
	}
}

func TestResolveTemplate_FromFile(t *testing.T) {
	// Create temp file
	tmpFile := t.TempDir() + "/test.j2"
	content := "{{ states('sensor.foo') | float * 2 }}"
	if err := writeTestFile(tmpFile, content); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	flagTplFile = tmpFile
	defer func() { flagTplFile = "" }()

	tpl, err := resolveTemplate(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tpl != content {
		t.Errorf("tpl = %q, want %q", tpl, content)
	}
}

func TestResolveTemplate_FilePriority(t *testing.T) {
	tmpFile := t.TempDir() + "/test.j2"
	content := "from_file"
	if err := writeTestFile(tmpFile, content); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	flagTplFile = tmpFile
	defer func() { flagTplFile = "" }()

	// Even with inline arg, file takes priority
	tpl, err := resolveTemplate([]string{"from_arg"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tpl != "from_file" {
		t.Errorf("tpl = %q, want 'from_file' (file takes priority)", tpl)
	}
}

func TestResolveTemplate_MissingFile(t *testing.T) {
	flagTplFile = "/nonexistent/file.j2"
	defer func() { flagTplFile = "" }()

	_, err := resolveTemplate(nil)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o600)
}
