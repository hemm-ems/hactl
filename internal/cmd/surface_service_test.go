package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/hemm-ems/hactl/internal/policy/reqmap"
	"github.com/hemm-ems/hactl/internal/surfaceaudit"
	"github.com/hemm-ems/hactl/internal/testaudit"
)

// serviceInventoryPath is the service inventory read off Home Assistant
// (D-184): every service the test instances expose, with whether it takes a
// target and its fields. TestServiceInventoryMatchesHomeAssistant and
// TestE2EServiceInventoryMatchesHomeAssistant keep it true.
const serviceInventoryPath = "testdata/policy/services/inventory.json"

// TestServiceSurfaceIsClosed — D-184: every service Home Assistant exposes,
// and every service the code maps or treats as opaque, is classified in
// dev/surfaces/service.manifest: target-only (exempt, with what its fields
// are), mapped or opaque (proven by the test that asserts its mapping). The
// manifest's disposition and the code's class move in lockstep: a service
// the code maps cannot sit in the manifest as target-only, nor the reverse.
//
// The hunt of 2026-11-19 found three services whose fields reach entities by
// another name (F1, F3) or by none (F2); nothing in the repository could say
// which other services were like them. This gate is that list.
func TestServiceSurfaceIsClosed(t *testing.T) {
	root := surfaceRepoRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, serviceInventoryPath)) //nolint:gosec // G304: a fixed path under the repository root
	if err != nil {
		t.Fatalf("reading the service inventory: %v", err)
	}
	var inv map[string]struct {
		Target bool     `json:"target"`
		Fields []string `json:"fields"`
	}
	if err = json.Unmarshal(raw, &inv); err != nil {
		t.Fatalf("parsing the service inventory: %v", err)
	}
	keys := map[string]bool{}
	for k := range inv {
		keys[k] = true
	}
	for k := range reqmap.ServiceTable() {
		keys[k] = true
	}
	s := surfaceaudit.Surface{
		Name: "service",
		Rule: "every service is classified: target-only, mapped (its other-name fields mapped) or opaque (needs an exact allow while anything is denied)",
	}
	sorted := make([]string, 0, len(keys))
	for k := range keys {
		sorted = append(sorted, k)
	}
	sort.Strings(sorted)
	for _, k := range sorted {
		s.Sites = append(s.Sites, surfaceaudit.Site{Key: k, File: serviceInventoryPath})
	}
	if len(inv) < 200 {
		t.Fatalf("the inventory holds %d services, want the ~250 of the test instances — it has been truncated", len(inv))
	}
	m, err := surfaceaudit.LoadManifest(root, s.Name)
	if err != nil {
		t.Fatalf("loading the service manifest: %v", err)
	}
	tests, err := testaudit.ScanRepo(root)
	if err != nil {
		t.Fatalf("indexing the test corpus: %v", err)
	}
	byName := make(map[string]bool, len(tests))
	for _, tc := range tests {
		byName[tc.Name] = true
	}
	res := surfaceaudit.Check(s, m, func(name string) bool { return byName[name] })
	if res.Failed() {
		t.Error(res.Report())
	}
	// Lockstep: the manifest's disposition is the code's class.
	for _, k := range sorted {
		d, ok := m.Entries[k]
		if !ok {
			continue
		}
		class := reqmap.ServiceClassOf(k)
		switch {
		case class == reqmap.ServiceTargetOnly && d.Kind != surfaceaudit.Exempt:
			t.Errorf("%s (manifest line %d): the code reads it as target-only, the manifest says %s — map it in reqmap.mappedServices or opaqueServices, or mark it exempt", k, d.Line, d.Kind)
		case class != reqmap.ServiceTargetOnly && d.Kind != surfaceaudit.Proven:
			t.Errorf("%s (manifest line %d): the code reads it as %s, the manifest says %s — a mapped or opaque service is proven by the test of its mapping", k, d.Line, class, d.Kind)
		}
	}
	if !res.Failed() {
		t.Log(res.Report())
	}
}
