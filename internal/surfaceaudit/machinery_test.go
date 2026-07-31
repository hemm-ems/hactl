package surfaceaudit_test

import (
	"os"
	"path/filepath"
	"slices"
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
//
// An extractor whose surface has several independently derived legs names one
// site per leg. A single canonical key would leave the other legs free to stop
// matching in silence, which is the same blind spot one level up.
func TestExtractorsFindTheirOwnPackage(t *testing.T) {
	root := repoRoot(t)
	for _, tc := range []struct {
		name     string
		derive   func(string) (surfaceaudit.Surface, error)
		wantKeys []string
	}{
		{"clock", surfaceaudit.ClockSurface, []string{"internal/clock/render.go:Short"}},
		{"target", surfaceaudit.TargetSurface, []string{"internal/cmd/cc.go:runCCShow"}},
		{"invariant", surfaceaudit.InvariantSurface, []string{"H-17"}},
		// The named site is the one the maprange surface exists for: the walk
		// that rendered one arbitrary entry of a map for a whole release.
		{"maprange", surfaceaudit.MapRangeSurface, []string{"internal/cmd/wireguard_cmd.go:writeWireguardMonitor"}},
		{"decode", surfaceaudit.DecodeSurface, []string{"internal/writer/writer.go:parseRemoteAutomationConfig"}},
		// One key per leg: the schema, the whole-payload read, and the join.
		// fetchAutomations is the site H-21 was written for — the listing that
		// died on an entity it does not list.
		{"domaindecode", surfaceaudit.DomainDecodeSurface, []string{
			"internal/cmd/auto.go:automationEntity.attributes cmd.automationAttributes",
			"internal/cmd/ent.go:runEntLs",
			"internal/cmd/auto.go:fetchAutomations",
		}},
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
			for _, want := range tc.wantKeys {
				if !slices.Contains(keys, want) {
					t.Errorf("the %s extractor no longer finds %q — it has stopped matching the code it audits.\nfound: %v",
						tc.name, want, keys)
				}
			}
		})
	}
}

// TestAutomationRefExtractorFlagsAnUnresolvedTarget guards the autoref
// violation surface the way TestPreviewExtractorFlagsAHandRolledPlan guards
// the preview one: the surface is legitimately empty, so a non-empty check
// cannot tell a healthy gate from an extractor that stopped matching. Feed it
// a known-bad function and require exactly that one flagged — including the
// helper-hop case, so a wrapper like resolveAutomationConfigID keeps counting
// as resolution.
func TestAutomationRefExtractorFlagsAnUnresolvedTarget(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "cmd")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	src := `package cmd

func resolveAutomation(ctx context.Context, c *int, ref string) (int, bool) { return 0, false }

func resolveAutomationConfigID(ctx context.Context, c *int, ref string) string {
	if _, ok := resolveAutomation(ctx, c, ref); ok {
		return ref
	}
	return ref
}

func runAutoDirect(ctx context.Context, w io.Writer, autoID string) error {
	_, _ = resolveAutomation(ctx, nil, autoID)
	return nil
}

func runAutoViaHelper(ctx context.Context, w io.Writer, automationID string) error {
	_ = resolveAutomationConfigID(ctx, nil, automationID)
	return nil
}

func runAutoBareLookup(ctx context.Context, w io.Writer, autoID string) error {
	return findBackup(autoID)
}

func runScriptShow(ctx context.Context, w io.Writer, scriptID string) error {
	return nil // not an automation reference; must not be swept in
}
`
	if err := os.WriteFile(filepath.Join(dir, "thing.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := surfaceaudit.AutomationRefSurface(root)
	if err != nil {
		t.Fatalf("deriving: %v", err)
	}
	keys := make([]string, 0, len(s.Sites))
	for _, site := range s.Sites {
		keys = append(keys, site.Key)
	}
	if len(keys) != 1 || !strings.HasSuffix(keys[0], ":runAutoBareLookup") {
		t.Errorf("want exactly the unresolved automation target flagged, got %v", keys)
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

// TestDecodeExtractorSeesEveryForm pins the decode surface's detections and —
// just as load-bearing — its one exclusion, against a synthetic tree.
//
// Every form is a way a decode has entered this module or could enter it
// without ever spelling `json.Unmarshal`: an aliased encoding/json import, a
// yaml unmarshal, a decoder construction, gorilla's ReadJSON, and a dot import
// that strips the qualifier. The exclusion boundary matters in both
// directions: a plain `json.Unmarshal(b, &v)` inside a wire package belongs to
// the H-14 sweep and must NOT be double-flagged here, while the same call with
// a target the sweep cannot name (and no degeneracy.Check in the function) is
// invisible to the sweep and MUST land here instead of nowhere.
func TestDecodeExtractorSeesEveryForm(t *testing.T) {
	root := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("internal/format/x.go", `package format

import (
	ej "encoding/json"
	"gopkg.in/yaml.v3"
)

func aliased(b []byte) { var v map[string]any; _ = ej.Unmarshal(b, &v) }
func viaYAML(b []byte) { var v map[string]any; _ = yaml.Unmarshal(b, &v) }
func viaDecoder(r reader) { _ = ej.NewDecoder(r) }
func viaWS(c conn) { var v map[string]any; _ = c.ReadJSON(&v) }
`)
	write("internal/cmd/x.go", `package cmd

import (
	"encoding/json"

	"gopkg.in/yaml.v3"
)

func sweptJSON(b []byte) { var v map[string]any; _ = json.Unmarshal(b, &v) }
func unsweepableTarget(b []byte) { _ = json.Unmarshal(b, mk()) }
func yamlInWirePackage(b []byte) { var v map[string]any; _ = yaml.Unmarshal(b, &v) }
`)
	write("internal/other/dot.go", `package other

import . "encoding/json"

func viaDot(b []byte) { var v map[string]any; _ = Unmarshal(b, &v) }
`)

	s, err := surfaceaudit.DecodeSurface(root)
	if err != nil {
		t.Fatalf("deriving: %v", err)
	}
	got := map[string]bool{}
	for _, site := range s.Sites {
		got[site.Key] = true
	}
	for _, want := range []string{
		"internal/format/x.go:aliased",
		"internal/format/x.go:viaYAML",
		"internal/format/x.go:viaDecoder",
		"internal/format/x.go:viaWS",
		"internal/cmd/x.go:unsweepableTarget",
		"internal/cmd/x.go:yamlInWirePackage",
		"internal/other/dot.go:import encoding/json",
	} {
		if !got[want] {
			t.Errorf("the decode extractor no longer finds %q — that form can now enter the module unexamined.\nfound: %v", want, got)
		}
	}
	if got["internal/cmd/x.go:sweptJSON"] {
		t.Error("a sweep-governed json.Unmarshal in a wire package was flagged here too — the surface would demand a second ledger entry for every site the H-14 sweep already derives")
	}
}

// TestResultExtractorFlagsProseOnConfirm guards the result violation surface,
// which is empty by design and therefore cannot be guarded by a non-empty
// check.
//
// The fixture pins both directions, because both were live in the tree when the
// gate was written and getting either wrong makes the gate useless:
//
//   - the two GUARD SPELLINGS must both be accepted. `if !flagJSON { print }`
//     is lexical; `if flagJSON { …; return }` followed by prose is an early
//     return, which is how `config delete` is written and is correct. An
//     extractor that only understood the first would report a correct command
//     and teach people to override it.
//   - a rendered result (`done(…).text(…).render(w)`) must be accepted, and
//     an unconditional Fprintf on the confirmed branch must be flagged. That
//     Fprintf is the defect itself: `svc call --confirm --json` printing
//     `called script.turn_on` in prose right after really firing the script.
func TestResultExtractorFlagsProseOnConfirm(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "internal", "cmd")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	src := `package cmd

func runThingRendered(ctx context.Context, w io.Writer, id string) error {
	if !flagThingConfirm {
		return dryRun("do the thing").render(w)
	}
	return done("do the thing").text("did %s", id).render(w)
}

func runThingLexicallyGuarded(ctx context.Context, w io.Writer, id string) error {
	if !flagThingConfirm {
		return dryRun("do the thing").render(w)
	}
	if !flagJSON {
		fmt.Fprintf(w, "did %s\n", id)
	}
	return nil
}

func runThingEarlyReturnGuarded(ctx context.Context, w io.Writer, id string) error {
	if !flagThingConfirm {
		return dryRun("do the thing").render(w)
	}
	if flagJSON {
		fmt.Fprintln(w, "{}")
		return nil
	}
	fmt.Fprintf(w, "did %s\n", id)
	return nil
}

func runThingAsProse(ctx context.Context, w io.Writer, id string) error {
	if !flagThingConfirm {
		return dryRun("do the thing").render(w)
	}
	fmt.Fprintf(w, "did %s\n", id)
	return nil
}

func runReadOnlyThing(ctx context.Context, w io.Writer) error {
	fmt.Fprintln(w, "not a write command; no --confirm gate")
	return nil
}
`
	if err := os.WriteFile(filepath.Join(dir, "thing.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := surfaceaudit.ResultSurface(root)
	if err != nil {
		t.Fatalf("deriving: %v", err)
	}
	keys := make([]string, 0, len(s.Sites))
	for _, site := range s.Sites {
		keys = append(keys, site.Key)
	}
	if len(keys) != 1 || !strings.Contains(keys[0], ":runThingAsProse:") {
		t.Errorf("want exactly the prose-on-confirm result flagged, got %v", keys)
	}
}
