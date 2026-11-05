package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const draftDenyLocks = `version: 1
active: agent
profiles:
  agent:
    default: deny
    allow:
      "entity:light.*": [control]
`

// draftInstance is an instance (no policy.yaml: allow-all) with a draft beside
// it; HA is unreachable, which show and check never need here.
func draftInstance(t *testing.T, draft string) (dir, path string) {
	t.Helper()
	dir = instanceAt(t, "http://127.0.0.1:9")
	path = filepath.Join(dir, "policy.draft.yaml")
	if draft != "" {
		if err := os.WriteFile(path, []byte(draft), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir, path
}

// D-161: show reads the draft, says it is one, and that none of it is enforced.
func TestPolicyFileFlagShow(t *testing.T) {
	dir, path := draftInstance(t, draftDenyLocks)
	var out bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "--dir", dir, "policy", "show", "--file", path}, &out); err != nil {
		t.Fatalf("policy show --file: %v\n%s", err, out.String())
	}
	for _, want := range []string{
		"file: " + path + " (draft, not in effect)\n",
		"enforced: nothing — a draft is not in effect; hactl policy activate puts it in effect\n",
		"profile: agent (selected by active)\n",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("policy show --file lacks %q:\n%s", want, out.String())
		}
	}
	out.Reset()
	if err := RunWithOutput([]string{"hactl", "--dir", dir, "policy", "show", "--file", path, "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		File          string   `json:"file"`
		Draft         *bool    `json:"draft"`
		Enforced      bool     `json:"enforced"`
		EnforcedVerbs []string `json:"enforced_verbs"`
		Profile       string   `json:"profile"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Draft == nil || !*doc.Draft || doc.Enforced || doc.EnforcedVerbs == nil || len(doc.EnforcedVerbs) != 0 || doc.Profile != "agent" || doc.File != path {
		t.Errorf("show --file --json = %s", out.String())
	}

	// Without --file, the same instance answers from policy.yaml: none, draft false.
	out.Reset()
	if err := RunWithOutput([]string{"hactl", "--dir", dir, "policy", "show", "--json"}, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"draft":false`) && !strings.Contains(out.String(), `"draft": false`) {
		t.Errorf("show --json without --file does not say draft false: %s", out.String())
	}
}

// D-161: check answers the draft's verdict, exits on it, and says the draft is
// not enforced.
func TestPolicyFileFlagCheck(t *testing.T) {
	dir, path := draftInstance(t, draftDenyLocks)
	var out bytes.Buffer
	err := RunWithOutput([]string{"hactl", "--dir", dir, "policy", "check", "--file", path, "entity:lock.front_door", "control"}, &out)
	if pgExit(err) != 1 || !strings.HasPrefix(out.String(), "denied: entity:lock.front_door control\n") ||
		!strings.Contains(out.String(), "not enforced: a draft is not in effect\n") {
		t.Errorf("check --file of a draft denial: exit %d\n%s", pgExit(err), out.String())
	}
	out.Reset()
	if err := RunWithOutput([]string{"hactl", "--dir", dir, "policy", "check", "--file", path, "entity:light.sofa", "control", "--json"}, &out); err != nil {
		t.Fatalf("check --file of a draft allow: %v", err)
	}
	if !strings.Contains(out.String(), `"draft":true`) && !strings.Contains(out.String(), `"draft": true`) {
		t.Errorf("check --file --json lacks draft true: %s", out.String())
	}
	// policy.yaml (absent) still allows it: --file never changed the instance.
	out.Reset()
	if err := RunWithOutput([]string{"hactl", "--dir", dir, "policy", "check", "entity:lock.front_door", "control"}, &out); err != nil {
		t.Errorf("check without --file after a draft check: %v\n%s", err, out.String())
	}
}

// D-161: a draft that is not there is an error, never "no policy file".
func TestPolicyFileFlagMissingDraftIsAnError(t *testing.T) {
	dir, path := draftInstance(t, "")
	for _, args := range [][]string{
		{"policy", "show", "--file", path},
		{"policy", "lint", "--file", path},
		{"policy", "check", "--file", path, "entity:lock.x", "control"},
	} {
		var out bytes.Buffer
		err := RunWithOutput(append([]string{"hactl", "--dir", dir}, args...), &out)
		if err == nil || !strings.Contains(err.Error(), "no draft at "+path) || pgExit(err) != 1 {
			t.Errorf("%v with no draft: err = %v", args, err)
		}
		if strings.Contains(out.String(), "no policy file") || strings.Contains(out.String(), "allowed") {
			t.Errorf("%v with no draft answered as if there were no policy: %q", args, out.String())
		}
	}
}

// D-161: a broken draft is reported as a draft, exit 1.
func TestPolicyFileFlagLintBrokenDraft(t *testing.T) {
	dir, path := draftInstance(t, "version: 1\nactive: nobody\n")
	var out bytes.Buffer
	err := RunWithOutput([]string{"hactl", "--dir", dir, "policy", "lint", "--file", path}, &out)
	if pgExit(err) != 1 || !strings.Contains(out.String(), "file: "+path+" (draft, not in effect)\n") || !strings.Contains(out.String(), "policy_broken: "+path) {
		t.Errorf("lint --file of a broken draft: exit %d\n%s", pgExit(err), out.String())
	}
}

// pgExit is the exit code the CLI entry point derives from err.
func pgExit(err error) int {
	if err == nil {
		return 0
	}
	var ec interface{ ExitCode() int }
	if errors.As(err, &ec) {
		return ec.ExitCode()
	}
	return 1
}

// D-161 (orchestrator 2026-11-05): a relative --file resolves against the
// instance directory, never the working directory.
func TestPolicyFileFlagRelativePathIsTheInstanceDirectory(t *testing.T) {
	dir, path := draftInstance(t, draftDenyLocks)
	elsewhere := t.TempDir()
	t.Chdir(elsewhere)
	if err := os.WriteFile(filepath.Join(elsewhere, "policy.draft.yaml"), []byte("version: 1\nactive: other\nprofiles:\n  other: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := RunWithOutput([]string{"hactl", "--dir", dir, "policy", "show", "--file", "policy.draft.yaml"}, &out); err != nil {
		t.Fatalf("show --file relative: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "file: "+path+" (draft, not in effect)\n") || !strings.Contains(out.String(), "profile: agent") {
		t.Errorf("a relative --file did not read the instance's draft:\n%s", out.String())
	}
}
