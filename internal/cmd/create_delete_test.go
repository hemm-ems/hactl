package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeEnvWithCompanion writes a .env with COMPANION_URL so companion discovery
// succeeds in unit tests without needing a live companion server.
func writeEnvWithCompanion(t *testing.T, dir string) {
	t.Helper()
	content := "HA_URL=http://127.0.0.1:19999\nHA_TOKEN=test\nCOMPANION_URL=http://127.0.0.1:19998\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAutoCreateDryRun(t *testing.T) {
	dir := t.TempDir()
	writeEnvWithCompanion(t, dir)
	yamlFile := filepath.Join(dir, "test_auto.yaml")
	if err := os.WriteFile(yamlFile, []byte("id: test_auto\nalias: Test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"auto", "create", "-f", yamlFile, "--dir", dir})

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

// ---------------------------------------------------------------------------
// Dry-run honesty.
//
// Every test below used to assert the opposite: that `<family> delete
// <fabricated id>` prints a confident plan and exits 0 without ever asking HA
// whether the target exists. That is the defect, not the contract — the manual
// tells agents to stop at the first miss, so a typo read as a verified plan.
// The assertions are inverted deliberately: a preview must fail exactly where
// the confirmed run would.
// ---------------------------------------------------------------------------

// assertPreviewRefuses runs a command in dry-run mode and requires it to fail
// rather than print a plan.
func assertPreviewRefuses(t *testing.T, args ...string) {
	t.Helper()
	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetErr(buf)
	rootCmd.SetArgs(args)
	err := rootCmd.Execute()
	out := buf.String()
	if err == nil {
		t.Fatalf("dry-run planned a write it cannot perform; output:\n%s", out)
	}
	if strings.Contains(out, "use --confirm") {
		t.Errorf("dry-run offered --confirm for an unresolvable target:\n%s", out)
	}
}

func TestAutoDeleteDryRunRefusesUnresolvable(t *testing.T) {
	dir := t.TempDir()
	writeEnvWithCompanion(t, dir)
	assertPreviewRefuses(t, "auto", "delete", "test_automation_id", "--dir", dir)
}

func TestScriptDeleteDryRunRefusesUnresolvable(t *testing.T) {
	dir := t.TempDir()
	writeEnvWithCompanion(t, dir)
	assertPreviewRefuses(t, "script", "delete", "welcome_home", "--dir", dir)
}

func TestTplDeleteDryRunRefusesUnresolvable(t *testing.T) {
	dir := t.TempDir()
	writeEnvWithCompanion(t, dir)
	assertPreviewRefuses(t, "tpl", "delete", "test_uid", "--dir", dir)
}

func TestHelperDeleteDryRunRefusesUnresolvable(t *testing.T) {
	dir := t.TempDir()
	writeEnvWithCompanion(t, dir)
	assertPreviewRefuses(t, "helper", "delete", "guest_mode", "--dir", dir)
}

// TestRegistryDeleteDryRunResolvesTarget drives the three registry deletes
// against a fake HA that holds exactly one entry each: a known id previews and
// names what would go, an unknown one is refused.
func TestRegistryDeleteDryRunResolvesTarget(t *testing.T) {
	ts := startCmdServer(t, map[string]any{
		"config/area_registry/list":  []map[string]any{{"area_id": "kitchen", "name": "Kitchen"}},
		"config/floor_registry/list": []map[string]any{{"floor_id": "ground", "name": "Ground Floor"}},
		"config/label_registry/list": []map[string]any{{"label_id": "energy", "name": "Energy"}},
	}, nil)

	cases := []struct {
		family  string
		known   string
		byName  string
		unknown string
		shownAs string
	}{
		{"area", "kitchen", "Kitchen", "no_such_area", "Kitchen"},
		{"floor", "ground", "Ground Floor", "no_such_floor", "Ground Floor"},
		{"label", "energy", "Energy", "no_such_label", "Energy"},
	}
	for _, tc := range cases {
		t.Run(tc.family, func(t *testing.T) {
			buf := new(bytes.Buffer)
			rootCmd.SetOut(buf)
			rootCmd.SetErr(buf)
			rootCmd.SetArgs([]string{tc.family, "delete", tc.known, "--dir", ts.dir})
			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("%s delete dry-run failed for a known id: %v\n%s", tc.family, err, buf.String())
			}
			out := buf.String()
			if !strings.Contains(out, "dry-run") {
				t.Errorf("expected a dry-run preview, got: %q", out)
			}
			// The name is the witness that the preview resolved the id
			// against HA rather than echoing the argument back.
			if !strings.Contains(out, tc.shownAs) {
				t.Errorf("preview does not name the resolved entry %q, got: %q", tc.shownAs, out)
			}

			// The display name resolves too: a command must not refuse the
			// identifier its own `ls` prints.
			buf.Reset()
			rootCmd.SetArgs([]string{tc.family, "delete", tc.byName, "--dir", ts.dir})
			if err := rootCmd.Execute(); err != nil {
				t.Errorf("%s delete refused its own displayed name %q: %v", tc.family, tc.byName, err)
			}

			assertPreviewRefuses(t, tc.family, "delete", tc.unknown, "--dir", ts.dir)
		})
	}
}

// TestEntSetAreaDryRunPrefersIDOverName pins the resolution order: an id match
// wins over a name match anywhere in the list, so the same argument cannot mean
// different entries depending on registry order.
func TestRegistryTargetIDWinsOverName(t *testing.T) {
	type row struct{ id, name string }
	rows := []row{{id: "second", name: "first"}, {id: "first", name: "other"}}
	got, ok := resolveRegistryTarget("first", rows, func(r row) (string, string) { return r.id, r.name })
	if !ok {
		t.Fatal("expected a match")
	}
	if got.id != "first" {
		t.Errorf("resolved to %q by name; the id match must win", got.id)
	}
}

func TestScriptCreateDryRun(t *testing.T) {
	dir := t.TempDir()
	writeEnvWithCompanion(t, dir)
	yamlFile := filepath.Join(dir, "test_script.yaml")
	if err := os.WriteFile(yamlFile, []byte("test_script:\n  alias: Test\n  sequence: []\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"script", "create", "-f", yamlFile, "--dir", dir})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("script create dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
	if !strings.Contains(out, "test_script") {
		t.Errorf("preview should name the script id it parsed out of the file, got: %q", out)
	}
}

// TestCreateDryRunRejectsUnusableInput covers the other half of dry-run
// honesty: the preview reads its input file and refuses what the confirmed run
// would refuse. `helper create` is the case found by hand — the companion needs
// a mapping keyed by the helper id, a bare `name:`/`icon:` mapping is a 400,
// and nothing documented it while the preview reported only the file size.
func TestCreateDryRunRejectsUnusableInput(t *testing.T) {
	cases := []struct {
		name string
		body string
		args func(dir, file string) []string
	}{
		{
			name: "helper create with an unkeyed mapping",
			body: "name: Not Keyed\nicon: mdi:alert\n",
			args: func(dir, f string) []string {
				return []string{"helper", "create", "input_boolean", "-f", f, "--dir", dir}
			},
		},
		{
			name: "script create without a sequence",
			body: "bad_script:\n  alias: No Sequence\n",
			args: func(dir, f string) []string { return []string{"script", "create", "-f", f, "--dir", dir} },
		},
		{
			name: "script create with unparseable YAML",
			body: "bad_script:\n  alias: [unclosed\n",
			args: func(dir, f string) []string { return []string{"script", "create", "-f", f, "--dir", dir} },
		},
		{
			name: "script create with two top-level keys",
			body: "one:\n  sequence: []\ntwo:\n  sequence: []\n",
			args: func(dir, f string) []string { return []string{"script", "create", "-f", f, "--dir", dir} },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeEnvWithCompanion(t, dir)
			f := filepath.Join(dir, "input.yaml")
			if err := os.WriteFile(f, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			assertPreviewRefuses(t, tc.args(dir, f)...)
		})
	}
}

func TestTplCreateDryRun(t *testing.T) {
	dir := t.TempDir()
	writeEnvWithCompanion(t, dir)
	yamlFile := filepath.Join(dir, "test_tpl.yaml")
	if err := os.WriteFile(yamlFile, []byte("unique_id: test_tpl\nname: Test\nstate: '{{ 42 }}'\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"tpl", "create", "-f", yamlFile, "--dir", dir})

	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("tpl create dry-run failed: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "dry-run") {
		t.Errorf("expected dry-run output, got: %q", out)
	}
}

func TestHelperCreateDryRun(t *testing.T) {
	dir := t.TempDir()
	writeEnvWithCompanion(t, dir)
	yamlFile := filepath.Join(dir, "test_helper.yaml")
	if err := os.WriteFile(yamlFile, []byte("test_toggle:\n  name: Test Toggle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	buf := new(bytes.Buffer)
	rootCmd.SetOut(buf)
	rootCmd.SetArgs([]string{"helper", "create", "input_boolean", "-f", yamlFile, "--dir", dir})

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
	if !strings.Contains(out, "test_toggle") {
		t.Errorf("preview should name the helper id it parsed out of the file, got: %q", out)
	}
}

// TestPreviewJSONIsMachineReadable pins the other half of the Wave 2c
// decision: --json on a preview used to be a byte-for-byte no-op, so an agent
// asking for JSON got prose. Every preview now answers with the same content
// as an object that states it is a plan, not a result.
func TestPreviewJSONIsMachineReadable(t *testing.T) {
	dir := t.TempDir()
	writeEnvWithCompanion(t, dir)
	yamlFile := filepath.Join(dir, "test_helper.yaml")
	if err := os.WriteFile(yamlFile, []byte("test_toggle:\n  name: Test Toggle\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	oldJSON := flagJSON
	flagJSON = true
	defer func() { flagJSON = oldJSON }()

	var buf bytes.Buffer
	oldFile := flagHelperFile
	flagHelperFile = yamlFile
	defer func() { flagHelperFile = oldFile }()
	withFlagDir(t, dir)

	if err := runHelperCreate(context.Background(), &buf, "input_boolean"); err != nil {
		t.Fatalf("helper create dry-run failed: %v", err)
	}

	var got struct {
		Details map[string]any `json:"details"`
		Action  string         `json:"action"`
		Hint    string         `json:"hint"`
		DryRun  bool           `json:"dry_run"`
	}
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("preview --json did not parse: %v\noutput:\n%s", err, buf.String())
	}
	if !got.DryRun {
		t.Error("preview JSON must state dry_run: true — a caller has to tell a plan from a result")
	}
	if got.Action != "create helper" {
		t.Errorf("action = %q, want %q", got.Action, "create helper")
	}
	if got.Details["id"] != "test_toggle" || got.Details["domain"] != "input_boolean" {
		t.Errorf("details lost the plan's content: %v", got.Details)
	}
	// A number stays a number: the table-backed --json paths stringify their
	// values, and previews are new enough not to inherit that.
	if _, ok := got.Details["bytes"].(float64); !ok {
		t.Errorf("bytes = %#v, want a JSON number", got.Details["bytes"])
	}
}
