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
	"github.com/hemm-ems/hactl/internal/testaudit"
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

// policySurface is H-29's whole site set: the requests the product's source
// can send and the write commands of the live tree.
func policySurface(t *testing.T, root string) surfaceaudit.Surface {
	t.Helper()
	reqs, err := surfaceaudit.PolicyRequestSites(root)
	if err != nil {
		t.Fatalf("deriving the request half: %v", err)
	}
	cmds, err := surfaceaudit.PolicyCommandSites(root, policyCommands(t))
	if err != nil {
		t.Fatalf("deriving the command half: %v", err)
	}
	// Both halves are censuses: in any tree that still contains the product
	// neither can be empty, so an empty one means an extractor stopped
	// matching and the gate would pass forever proving nothing.
	if len(reqs) == 0 {
		t.Fatal("the policy surface's request half is empty — the extractor has stopped matching the clients")
	}
	if len(cmds) == 0 {
		t.Fatal("the policy surface's command half is empty — the cobra walk or the call graph has stopped matching")
	}
	return surfaceaudit.Surface{
		Name:  "policy",
		Rule:  "every request hactl sends carries a policy verdict, and every write command checks its whole plan before its first write (H-29)",
		Sites: append(reqs, cmds...),
	}
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

// TestPolicySurfaceIsClosed — H-29's closure gate over
// dev/surfaces/policy.manifest.
//
// Every request the product can send — each sendCommand type and direct
// conn.WriteJSON, each Supervisor (method, endpoint), each haapi request
// builder call, each companion.Client route — and every write command — each
// --confirm command and each command whose Run reaches a write without one —
// has a manifest line, and the manifest names nothing that is gone. On top of
// the shared closure check the request half is held in lockstep twice more:
// with the classifier's row table (a manifest row the classifier does not know
// is a request refused at runtime as unmapped; a classifier row the manifest
// does not list is a mapping nobody reviews), including what kind of row it
// is; and with companion.Endpoints, the client's own table of the routes it
// calls.
func TestPolicySurfaceIsClosed(t *testing.T) {
	root := surfaceRepoRoot(t)
	s := policySurface(t, root)

	m, err := surfaceaudit.LoadManifest(root, s.Name)
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
	for _, site := range s.Sites {
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
		return
	}
	t.Log(res.Report())
}
