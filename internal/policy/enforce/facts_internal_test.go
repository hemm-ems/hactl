package enforce

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
)

// TestFactCacheIsTaggedWithThePolicyHash (A-8, R-7): every cached fact —
// fetched or observed — belongs to the policy it was established under; read
// under another hash, the cache is empty.
func TestFactCacheIsTaggedWithThePolicyHash(t *testing.T) {
	c := newFactCache("h1")
	c.verifiedAutomations["heating"] = true
	c.optionsHandlers["f1"] = "entry"
	c.dashboards[""] = reqmap.StoredDashboard{}
	c.live, c.liveRead = &reqmap.LiveStates{EntityIDs: []string{"light.a"}}, true
	c.observeRefReplace(reqmap.Request{Body: []byte(`{"old":"a","new":"b"}`)}, []byte(`{"changes":[{"location":"x.yaml","path":"p"}]}`))
	if len(c.refPlans) != 1 || c.refPlans[0].PolicyHash != "h1" {
		t.Fatalf("an observed ref/replace plan is not tagged with the hash: %+v", c.refPlans)
	}
	if same := c.forHash("h1"); same != c {
		t.Error("the same hash dropped the cache")
	}
	other := c.forHash("h2")
	f := other.facts(nil, false)
	if f.PolicyHash != "h2" || len(f.VerifiedAutomationIDs) != 0 || len(f.OptionsFlowHandlers) != 0 ||
		len(f.Dashboards) != 0 || f.Live != nil || len(f.RefReplacePlans) != 0 {
		t.Errorf("facts survived a policy change: %+v", f)
	}
	if got := c.facts(nil, false); got.PolicyHash != "h1" || !got.VerifiedAutomationIDs["heating"] || got.Live == nil {
		t.Errorf("facts under their own hash: %+v", got)
	}
}

// TestIncompleteRegistriesAreNoCascadeSource (D-76): a device list without
// config_entries cannot say which devices an entry owns, so a config entry's
// cascade is not enumerated from it (the mapping then checks kind-wide).
func TestIncompleteRegistriesAreNoCascadeSource(t *testing.T) {
	c := newFactCache("h")
	c.registries, c.regComplete, c.regRead = &reqmap.Registries{}, false, true
	if c.facts(nil, true).Registries != nil {
		t.Error("incomplete registries were handed to a cascade")
	}
	if c.facts(nil, false).Registries == nil {
		t.Error("incomplete registries were withheld from a mapping that does not read config entries")
	}
}

// TestAuditLogRotatesAtTheBound (D-71): a log that has reached the bound is
// renamed to .1 — replacing the previous generation — before the next line.
func TestAuditLogRotatesAtTheBound(t *testing.T) {
	defer func(n int64) { AuditRotateBytes = n }(AuditRotateBytes)
	AuditRotateBytes = 200
	dir := t.TempDir()
	a := NewAudit(dir)
	line := AuditLine{Time: "t", Command: "hactl label create x", Layer: LayerCommand, Resource: "label:*", Verb: "create", Rule: "default: allow", Profile: "a"}
	if err := os.MkdirAll(filepath.Dir(a.Path()), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.Path()+".1", []byte("old generation\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := a.Append([]AuditLine{line}); err != nil {
			t.Fatal(err)
		}
	}
	// Two lines take the log past the bound; the next append rotates first.
	if _, err := os.Stat(a.Path() + ".1"); err != nil {
		t.Fatal(err)
	}
	if err := a.Append([]AuditLine{line}); err != nil {
		t.Fatal(err)
	}
	rotated, err := os.ReadFile(a.Path() + ".1")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(rotated), "old generation") || strings.Count(string(rotated), "\n") != 2 {
		t.Errorf(".1 is not the rotated log (one generation kept):\n%s", rotated)
	}
	cur, _ := os.ReadFile(a.Path())
	if strings.Count(string(cur), "\n") != 1 {
		t.Errorf("the new log holds %q, want the one line written after the rotation", cur)
	}
}
