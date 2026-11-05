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

// funcLoc locates a function value as the runtime reports it.
func funcLoc(t *testing.T, path string, fn any) surfaceaudit.FuncLoc {
	t.Helper()
	entry := reflect.ValueOf(fn).Pointer()
	var loc surfaceaudit.FuncLoc
	if f := runtime.FuncForPC(entry); f != nil {
		loc.File, loc.Line = f.FileLine(entry)
	}
	if loc.File == "" {
		t.Fatalf("%s: the runtime cannot locate a function the command runs", path)
	}
	return loc
}

// commandHooks returns the hooks cobra can run around a command's Run: its own
// PreRun(E)/PostRun(E) and every PersistentPreRun(E)/PersistentPostRun(E) of
// it and its ancestors — all of them, though cobra runs only the nearest
// persistent one unless traverse is on.
func commandHooks(c *cobra.Command) []any {
	var hooks []any
	add := func(fns ...any) {
		for _, fn := range fns {
			if !reflect.ValueOf(fn).IsNil() {
				hooks = append(hooks, fn)
			}
		}
	}
	add(c.PreRunE, c.PreRun, c.PostRunE, c.PostRun)
	for p := c; p != nil; p = p.Parent() {
		add(p.PersistentPreRunE, p.PersistentPreRun, p.PersistentPostRunE, p.PersistentPostRun)
	}
	return hooks
}

// policyCommands walks the live cobra tree: every command, whether it carries
// --confirm, where the function its Run/RunE holds is declared, and where each
// hook cobra runs around it is.
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
			loc := funcLoc(t, pc.Path, run)
			pc.RunFile, pc.RunLine = loc.File, loc.Line
			for _, h := range commandHooks(c) {
				pc.Hooks = append(pc.Hooks, funcLoc(t, pc.Path, h))
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
		case maps[key] == "read" || strings.HasPrefix(maps[key], "read ") || strings.HasPrefix(maps[key], "read,"):
			want = reqmap.Read
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

// TestPolicyCommandCensusSeesEveryHook — the census's roots are every function
// cobra can run for a command, not its Run alone: a write in a PreRun, a
// PostRun or an ancestor's persistent hook makes a read command a writer.
func TestPolicyCommandCensusSeesEveryHook(t *testing.T) {
	noop := func(*cobra.Command, []string) error { return nil }
	noopRun := func(*cobra.Command, []string) {}
	root := &cobra.Command{Use: "root", PersistentPreRunE: noop}
	mid := &cobra.Command{Use: "mid", PersistentPostRun: noopRun}
	leaf := &cobra.Command{Use: "leaf", RunE: noop, PreRun: noopRun, PostRunE: noop}
	root.AddCommand(mid)
	mid.AddCommand(leaf)
	if got := len(commandHooks(leaf)); got != 4 {
		t.Errorf("leaf has %d hooks, want 4: its PreRun and PostRunE, mid's PersistentPostRun, root's PersistentPreRunE", got)
	}
	if got := len(commandHooks(root)); got != 1 {
		t.Errorf("root has %d hooks, want its own PersistentPreRunE", got)
	}
	// The live tree's hooks reach the census: rootCmd has a persistent hook,
	// so every runnable command lists at least one.
	for _, c := range policyCommands(t) {
		if c.RunFile != "" && len(c.Hooks) == 0 && rootCmd.PersistentPreRunE != nil {
			t.Errorf("%s lists no hook, but rootCmd's PersistentPreRunE runs before it", c.Path)
			break
		}
	}
}

// TestWriteCommandsAreThePolicyWriteSurface — D-82's predicate (which
// invocations' own reads are exempt) is the policy write surface, not a guess:
// isWriteCommand holds for exactly the manifest's `cmd` rows, less `config
// show` (a read command whose probe its plan checks), `setup` and `mcp` (no
// session covers them).
func TestWriteCommandsAreThePolicyWriteSurface(t *testing.T) {
	root := surfaceRepoRoot(t)
	m, err := surfaceaudit.LoadManifest(root, "policy")
	if err != nil {
		t.Fatalf("loading the policy manifest: %v", err)
	}
	notWrites := map[string]bool{"hactl config show": true, "hactl setup": true, "hactl mcp": true}
	want := map[string]bool{}
	for key := range m.Entries {
		path, ok := strings.CutPrefix(key, "cmd ")
		if ok && !notWrites[path] {
			want[path] = true
		}
	}
	if len(want) < 30 {
		t.Fatalf("the manifest names %d write commands — the cmd rows have stopped matching", len(want))
	}
	got := map[string]bool{}
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if c.Runnable() && isWriteCommand(c) {
			got[c.CommandPath()] = true
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	for p := range want {
		if !got[p] {
			t.Errorf("%s is a write command in policy.manifest, but isWriteCommand says no — its own reads would be enforced (D-82)", p)
		}
	}
	for p := range got {
		if !want[p] {
			t.Errorf("isWriteCommand says %s is a write command, but policy.manifest has no cmd row for it — its reads would go unenforced", p)
		}
	}
	for p := range notWrites {
		if got[p] {
			t.Errorf("%s is exempted from read enforcement, but it is no write command", p)
		}
	}
}
