package policy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// D-161: a draft is read like policy.yaml; a draft that is not there is an
// error, never "no policy file".
func TestLoadDraft(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "policy.draft.yaml")
	text := "version: 1\nactive: agent\nprofiles:\n  agent:\n    default: deny\n    allow:\n      \"entity:light.*\": [control]\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	pol, data, err := LoadDraft(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != text || pol.Source() != path || !pol.Exists() || pol.Active() != "agent" {
		t.Errorf("draft read as source=%q exists=%v active=%q, %d bytes", pol.Source(), pol.Exists(), pol.Active(), len(data))
	}

	var missing *DraftMissingError
	if _, _, err := LoadDraft(filepath.Join(dir, "nope.yaml")); !errors.As(err, &missing) {
		t.Errorf("a missing draft: err = %v, want *DraftMissingError", err)
	}

	broken := filepath.Join(dir, "broken.yaml")
	if err := os.WriteFile(broken, []byte("version: 1\nactive: nobody\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var le *LoadError
	if _, _, err := LoadDraft(broken); !errors.As(err, &le) || le.File != broken {
		t.Errorf("a broken draft: err = %v, want a *LoadError naming %s", err, broken)
	}
	// A directory where the draft should be is unreadable, not missing.
	if _, _, err := LoadDraft(dir); !errors.As(err, &le) {
		t.Errorf("a directory as the draft: err = %v, want a *LoadError", err)
	}
}

func TestRuleReach(t *testing.T) {
	text := "version: 1\nactive: a\nprofiles:\n  a:\n    deny:\n      \"*\": [delete]\n      \"entity:lock.*\": [write]\n      \"entity:*/registry/entity_id\": [edit]\n"
	pol, problems := parse([]byte(text))
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	rules := pol.Rules()
	byPattern := map[string]Rule{}
	for _, r := range rules {
		byPattern[r.Pattern] = r
	}
	if r := byPattern["*"]; r.Kind() != "" || !r.MatchesID(KindAutomation, "x") || r.OnPart() {
		t.Errorf("* : kind %q, reaches automation:x %v, on part %v", r.Kind(), r.MatchesID(KindAutomation, "x"), r.OnPart())
	}
	if r := byPattern["entity:lock.*"]; r.Kind() != KindEntity || !r.MatchesID(KindEntity, "lock.front") || r.MatchesID(KindEntity, "light.x") || r.MatchesID(KindScript, "lock.front") {
		t.Error("entity:lock.* reaches the wrong ids")
	}
	if r := byPattern["entity:*/registry/entity_id"]; !r.OnPart() || !r.MatchesID(KindEntity, "light.x") {
		t.Error("entity:*/registry/entity_id is not a part rule reaching every entity")
	}
}
