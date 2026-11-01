package cmd

import (
	"bufio"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/hemm-ems/hactl/internal/companion"
	"github.com/hemm-ems/hactl/internal/policy/reqmap"
	"github.com/hemm-ems/hactl/internal/surfaceaudit"
)

// policyCommands walks the live cobra tree: every command, whether it carries
// --confirm, and where the function its Run/RunE holds is declared.
func policyCommands(t *testing.T) []surfaceaudit.PolicyCommand {
	t.Helper()
	var out []surfaceaudit.PolicyCommand
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		pc := surfaceaudit.PolicyCommand{Path: c.CommandPath(), Confirm: c.Flags().Lookup("confirm") != nil}
		var run any
		switch {
		case c.RunE != nil:
			run = c.RunE
		case c.Run != nil:
			run = c.Run
		}
		if run != nil {
			entry := reflect.ValueOf(run).Pointer()
			if f := runtime.FuncForPC(entry); f != nil {
				pc.RunFile, pc.RunLine = f.FileLine(entry)
			}
			if pc.RunFile == "" {
				t.Fatalf("%s: the runtime cannot locate its Run function", pc.Path)
			}
		}
		if run != nil || pc.Confirm {
			out = append(out, pc)
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	return out
}

// policyCommandCensus is the command half of H-29's surface, derived — never
// hand-listed (D-80, R-17): every --confirm command of the live tree plus every
// command whose Run reaches a write sink over the product's call graph
// (surfaceaudit.PolicyCommandSites). One Site per write command, keyed by its
// cobra command path (`hactl area create`); surfaceaudit.PolicySurface adds the
// `cmd ` prefix.
func policyCommandCensus(t *testing.T) []surfaceaudit.Site {
	t.Helper()
	sites, err := surfaceaudit.PolicyCommandSites(surfaceRepoRoot(t), policyCommands(t))
	if err != nil {
		t.Fatalf("deriving the policy command census: %v", err)
	}
	for i := range sites {
		sites[i].Key = strings.TrimPrefix(sites[i].Key, "cmd ")
	}
	return sites
}

// manifestMaps reads the `# map:` comment above each manifest line: the
// contract the line's proof is a proof of.
func manifestMaps(t *testing.T, path string) map[string]string {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // a fixed manifest path under the repository root
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	out := map[string]string{}
	var last string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case strings.HasPrefix(line, "# map:"):
			last = strings.TrimSpace(strings.TrimPrefix(line, "# map:"))
		case line == "" || strings.HasPrefix(line, "#"):
		default:
			key, _, _ := strings.Cut(line, "=")
			out[strings.TrimSpace(key)] = last
			last = ""
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// TestPolicyManifestIsInLockstepWithTheClassifier — the request half of
// dev/surfaces/policy.manifest is held in lockstep twice beyond the closure
// gate (TestPolicySurfaceIsClosed): with the classifier's row table (a manifest
// row the classifier does not know is a request refused at runtime as
// unmapped; a classifier row the manifest does not list is a mapping nobody
// reviews), including what kind of row it is; and with companion.Endpoints,
// the client's own table of the routes it calls.
func TestPolicyManifestIsInLockstepWithTheClassifier(t *testing.T) {
	root := surfaceRepoRoot(t)
	reqs, err := surfaceaudit.PolicyRequestSites(root)
	if err != nil {
		t.Fatalf("deriving the request half: %v", err)
	}
	if len(reqs) == 0 {
		t.Fatal("the policy surface's request half is empty — the extractor has stopped matching the clients")
	}
	m, err := surfaceaudit.LoadManifest(root, "policy")
	if err != nil {
		t.Fatalf("loading the policy manifest: %v", err)
	}
	maps := manifestMaps(t, filepath.Join(root, m.Path))

	rows := map[string]reqmap.Disposition{}
	for _, r := range reqmap.Rows() {
		rows[r.Key] = r.Disposition
		if _, ok := m.Entries[r.Key]; !ok {
			t.Errorf("classifier row %q has no line in %s", r.Key, m.Path)
		}
	}
	for key := range m.Entries {
		if strings.HasPrefix(key, "cmd ") {
			continue
		}
		d, ok := rows[key]
		if !ok {
			t.Errorf("%s names %q, which the classifier (internal/policy/reqmap) has no row for — at runtime it would be refused as unmapped", m.Path, key)
			continue
		}
		want := reqmap.Write
		switch {
		case strings.HasPrefix(maps[key], "exempt"):
			want = reqmap.Exempt
		case strings.HasPrefix(maps[key], "read-deferred-phase3"):
			want = reqmap.ReadDeferred
		}
		if d != want {
			t.Errorf("%q: the manifest's `map: %s` says %s, the classifier's row says %s", key, maps[key], want, d)
		}
	}

	// The companion half agrees with the client's own route table, both ways.
	derived := map[string]bool{}
	for _, site := range reqs {
		if strings.HasPrefix(site.Key, "companion ") {
			derived[site.Key] = true
		}
	}
	for _, ep := range companion.Endpoints {
		key := "companion " + ep.Method + " " + ep.Path
		if !derived[key] {
			t.Errorf("companion.Endpoints lists %s, which no companion.Client method sends", key)
		}
		delete(derived, key)
	}
	for key := range derived {
		t.Errorf("companion.Client sends %s, which companion.Endpoints does not list", key)
	}
}
