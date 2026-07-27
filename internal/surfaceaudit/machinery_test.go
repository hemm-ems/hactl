package surfaceaudit_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
)

// writeManifest lays out a throwaway repo root holding one manifest.
func writeManifest(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, surfaceaudit.ManifestDir)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo.manifest"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return root
}

func siteSurface(keys ...string) surfaceaudit.Surface {
	s := surfaceaudit.Surface{Name: "demo", Rule: "demo rule"}
	for _, k := range keys {
		s.Sites = append(s.Sites, surfaceaudit.Site{Key: k, File: "f.go", Line: 1, Note: "note"})
	}
	return s
}

func allProofsExist(string) bool { return true }
func noProofsExist(string) bool  { return false }
func longReason(kind string) string {
	return kind + ": this reason is deliberately long enough to satisfy the minimum"
}

// TestCheckFailsOnAnUndispositionedSite is the closure property itself: a site
// the manifest does not mention must fail, because that is the state every
// missed fix was in.
func TestCheckFailsOnAnUndispositionedSite(t *testing.T) {
	root := writeManifest(t, "#ceiling 0\nknown = "+longReason("exempt")+"\n")
	m, err := surfaceaudit.LoadManifest(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	res := surfaceaudit.Check(siteSurface("known", "forgotten"), m, allProofsExist)
	if !res.Failed() {
		t.Fatal("a site nobody dispositioned passed the gate")
	}
	if len(res.Unclassified) != 1 || res.Unclassified[0].Key != "forgotten" {
		t.Errorf("want the one unclassified site to be 'forgotten', got %+v", res.Unclassified)
	}
	if !strings.Contains(res.Report(), "forgotten = debt:") {
		t.Errorf("the report must print the manifest line to add:\n%s", res.Report())
	}
}

// TestCheckFailsOnAPhantomProof — a disposition naming a test that does not
// exist is the mechanism that turns an "Enforced by:" list into decoration.
func TestCheckFailsOnAPhantomProof(t *testing.T) {
	root := writeManifest(t, "#ceiling 0\nknown = proven: TestThatWasDeleted\n")
	m, err := surfaceaudit.LoadManifest(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	res := surfaceaudit.Check(siteSurface("known"), m, noProofsExist)
	if !res.Failed() || len(res.Phantom) != 1 {
		t.Fatalf("a proof that does not exist passed the gate: %+v", res)
	}
	if res.Proven != 0 {
		t.Errorf("a phantom must not be counted as proven, got %d", res.Proven)
	}
}

// TestCheckFailsOnAStaleEntry — a ledger that describes code which is gone has
// stopped describing the code.
func TestCheckFailsOnAStaleEntry(t *testing.T) {
	root := writeManifest(t, "#ceiling 0\ngone = "+longReason("exempt")+"\n")
	m, err := surfaceaudit.LoadManifest(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	res := surfaceaudit.Check(siteSurface(), m, allProofsExist)
	if !res.Failed() || len(res.Stale) != 1 {
		t.Fatalf("a stale disposition passed the gate: %+v", res)
	}
}

// TestCheckRejectsAThinReason — `exempt: n/a` is the idiom this is here to stop
// from forming, in both the exempt and the debt directions.
func TestCheckRejectsAThinReason(t *testing.T) {
	for _, kind := range []string{"exempt", "debt"} {
		t.Run(kind, func(t *testing.T) {
			root := writeManifest(t, "#ceiling 9\nknown = "+kind+": n/a\n")
			m, err := surfaceaudit.LoadManifest(root, "demo")
			if err != nil {
				t.Fatal(err)
			}
			res := surfaceaudit.Check(siteSurface("known"), m, allProofsExist)
			if !res.Failed() || len(res.ThinReason) != 1 {
				t.Fatalf("%q with a two-character reason passed the gate: %+v", kind, res)
			}
		})
	}
}

// TestDebtIsLegalUpToTheCeiling — recorded debt passes, and one more does not.
//
// The asymmetry is the design. Writing a reason into the ledger and raising a
// number is a visible act with an author; forgetting a site is not an act at
// all, and that is the only difference between debt and the thing this package
// exists to prevent.
func TestDebtIsLegalUpToTheCeiling(t *testing.T) {
	body := "#ceiling 1\na = " + longReason("debt") + "\n"
	root := writeManifest(t, body)
	m, err := surfaceaudit.LoadManifest(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	if res := surfaceaudit.Check(siteSurface("a"), m, allProofsExist); res.Failed() {
		t.Fatalf("debt within the ceiling failed the gate:\n%s", res.Report())
	}

	body += "b = " + longReason("debt") + "\n"
	root = writeManifest(t, body)
	m, err = surfaceaudit.LoadManifest(root, "demo")
	if err != nil {
		t.Fatal(err)
	}
	res := surfaceaudit.Check(siteSurface("a", "b"), m, allProofsExist)
	if !res.Failed() {
		t.Fatal("debt over the ceiling passed the gate")
	}
	if !strings.Contains(res.Report(), "exceeds the ceiling") {
		t.Errorf("the report must say the ceiling was exceeded:\n%s", res.Report())
	}
}

// TestManifestWithoutACeilingIsAnError — a surface that never states how much
// debt it tolerates would accept any amount silently.
func TestManifestWithoutACeilingIsAnError(t *testing.T) {
	root := writeManifest(t, "known = "+longReason("exempt")+"\n")
	if _, err := surfaceaudit.LoadManifest(root, "demo"); err == nil {
		t.Fatal("a manifest with no ceiling line loaded without complaint")
	}
}

// TestManifestRejectsADuplicateKey — two dispositions for one site means one of
// them is not being read, and which one is an accident of file order.
func TestManifestRejectsADuplicateKey(t *testing.T) {
	root := writeManifest(t, "#ceiling 0\nk = "+longReason("exempt")+"\nk = proven: TestX\n")
	_, err := surfaceaudit.LoadManifest(root, "demo")
	if err == nil || !strings.Contains(err.Error(), "twice") {
		t.Fatalf("want a duplicate-key error, got %v", err)
	}
}

// TestExtractorsFindTheirOwnPackage — every extractor is run against this
// repository and must return a non-empty surface.
//
// An extractor that has stopped matching passes its gate forever while proving
// nothing, which is the failure mode a closure gate is most exposed to: it
// looks green precisely because it sees nothing.
func TestExtractorsFindTheirOwnPackage(t *testing.T) {
	root := repoRoot(t)
	for _, tc := range []struct {
		name    string
		derive  func(string) (surfaceaudit.Surface, error)
		wantKey string
	}{
		{"clock", surfaceaudit.ClockSurface, "internal/clock/render.go:Short"},
		{"target", surfaceaudit.TargetSurface, "internal/cmd/cc.go:runCCShow"},
		{"invariant", surfaceaudit.InvariantSurface, "H-17"},
		// The named site is the one the maprange surface exists for: the walk
		// that rendered one arbitrary entry of a map for a whole release.
		{"maprange", surfaceaudit.MapRangeSurface, "internal/cmd/wireguard_cmd.go:writeWireguardMonitor"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := tc.derive(root)
			if err != nil {
				t.Fatalf("deriving: %v", err)
			}
			keys := make([]string, 0, len(s.Sites))
			for _, site := range s.Sites {
				keys = append(keys, site.Key)
			}
			found := false
			for _, k := range keys {
				if k == tc.wantKey {
					found = true
				}
			}
			if !found {
				t.Errorf("the %s extractor no longer finds %q — it has stopped matching the code it audits.\nfound: %v",
					tc.name, tc.wantKey, keys)
			}
		})
	}
}

// TestPreviewExtractorFlagsAHandRolledPlan guards the one violation surface,
// which is legitimately empty and therefore cannot be guarded by a non-empty
// check.
//
// Without this the extractor could stop matching — a renamed dryRun, a changed
// entrypoint convention — and the gate would pass forever while proving
// nothing, which is the failure mode a closure gate is most exposed to.
func TestPreviewExtractorFlagsAHandRolledPlan(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "cmd")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	src := `package cmd

func dryRun(string) *plan { return nil }

func runThingProperly(w int) error {
	if !flagThingConfirm {
		return dryRun("do the thing").render(w)
	}
	return nil
}

func runThingByHand(w int) error {
	if !flagThingConfirm {
		printf(w, "dry-run: would do the thing\n")
		return nil
	}
	return nil
}
`
	if err := os.WriteFile(filepath.Join(dir, "thing.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := surfaceaudit.PreviewSurface(root)
	if err != nil {
		t.Fatalf("deriving: %v", err)
	}
	keys := make([]string, 0, len(s.Sites))
	for _, site := range s.Sites {
		keys = append(keys, site.Key)
	}
	if len(keys) != 1 || !strings.HasSuffix(keys[0], ":runThingByHand") {
		t.Errorf("want exactly the hand-rolled preview flagged, got %v", keys)
	}
}
