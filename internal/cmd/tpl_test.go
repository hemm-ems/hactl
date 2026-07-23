package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

func TestClassifyTemplate_BareItem(t *testing.T) {
	k, err := classifyTemplate("unique_id: x\nname: X\nstate: \"{{ 1 }}\"\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if k.isBlock || k.triggerBased || k.strayKey != "" {
		t.Errorf("bare item misclassified: %+v", k)
	}
}

func TestClassifyTemplate_TriggerBlock(t *testing.T) {
	content := "triggers:\n  - trigger: state\n    entity_id: sensor.s\nsensor:\n  - unique_id: x\n    state: \"{{ 1 }}\"\n"
	k, err := classifyTemplate(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !k.isBlock || !k.triggerBased {
		t.Errorf("trigger block misclassified: %+v", k)
	}
	if len(k.domains) != 1 || k.domains[0] != "sensor" {
		t.Errorf("domains = %v, want [sensor]", k.domains)
	}
	if k.strayKey != "" {
		t.Errorf("strayKey = %q, want empty (trigger is at block level)", k.strayKey)
	}
}

func TestClassifyTemplate_StrayTriggerInItem(t *testing.T) {
	// A trigger nested inside a bare entity item — the corruption trap.
	content := "unique_id: x\nstate: \"{{ 1 }}\"\ntrigger:\n  - platform: state\n    entity_id: sensor.s\n"
	k, err := classifyTemplate(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if k.isBlock {
		t.Errorf("item with stray trigger should not be a block: %+v", k)
	}
	if k.strayKey != "trigger" {
		t.Errorf("strayKey = %q, want \"trigger\"", k.strayKey)
	}
}

func TestClassifyTemplate_MultiDomainBlock(t *testing.T) {
	content := "sensor:\n  - unique_id: a\n    state: x\nbinary_sensor:\n  - unique_id: b\n    state: y\n"
	k, err := classifyTemplate(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !k.isBlock || k.triggerBased {
		t.Errorf("state-based multi-domain block misclassified: %+v", k)
	}
	if len(k.domains) != 2 {
		t.Errorf("domains = %v, want 2 entries", k.domains)
	}
}

func TestClassifyTemplate_NumberBlock(t *testing.T) {
	// A non-sensor domain block is still classified as a full block.
	content := "number:\n  - unique_id: n\n    state: \"{{ 1 }}\"\n    set_value: []\n"
	k, err := classifyTemplate(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !k.isBlock || k.triggerBased {
		t.Errorf("state-based number block misclassified: %+v", k)
	}
	if len(k.domains) != 1 || k.domains[0] != "number" {
		t.Errorf("domains = %v, want [number]", k.domains)
	}
}

func TestClassifyTemplate_NonSensorMultiDomainBlock(t *testing.T) {
	content := "select:\n  - unique_id: s\n    state: x\nbutton:\n  - unique_id: b\n"
	k, err := classifyTemplate(content)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !k.isBlock || k.triggerBased {
		t.Errorf("non-sensor multi-domain block misclassified: %+v", k)
	}
	if len(k.domains) != 2 {
		t.Errorf("domains = %v, want 2 entries (select, button)", k.domains)
	}
}

func TestClassifyTemplate_InvalidYAML(t *testing.T) {
	if _, err := classifyTemplate("- this is\n  a: list\n"); err == nil {
		t.Fatal("expected error for a top-level list (not a single mapping)")
	}
}

// TestRunTplEval_JSONIsAlwaysValidJSON pins a round-2 finding (H-10).
//
// HA renders a template to a STRING, and for non-scalars that string is
// Python's repr — ['a', 'b'], None, True. Echoing it under --json produced
// output that was not JSON at all. hactl cannot faithfully reparse Python
// repr, so the rendered text ships verbatim inside a JSON envelope.
func TestRunTplEval_JSONIsAlwaysValidJSON(t *testing.T) {
	for _, rendered := range []string{"['a', 'b']", "None", "True", "42", "", "he said \"hi\""} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(rendered))
		}))
		t.Cleanup(srv.Close)

		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, ".env"),
			[]byte("HA_URL="+srv.URL+"\nHA_TOKEN=tok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		withFlagDir(t, dir)

		oldJSON := flagJSON
		flagJSON = true
		t.Cleanup(func() { flagJSON = oldJSON })

		var buf bytes.Buffer
		if err := runTplEval(context.Background(), &buf, []string{"{{ x }}"}); err != nil {
			t.Fatalf("runTplEval(%q): %v", rendered, err)
		}
		var out struct {
			Template string `json:"template"`
			Result   string `json:"result"`
		}
		if err := json.Unmarshal(buf.Bytes(), &out); err != nil {
			t.Errorf("tpl eval --json did not parse for rendered %q: %v\noutput: %s",
				rendered, err, buf.String())
			continue
		}
		if out.Result != rendered {
			t.Errorf("result = %q, want %q (the rendered text must survive verbatim)", out.Result, rendered)
		}
	}
}
