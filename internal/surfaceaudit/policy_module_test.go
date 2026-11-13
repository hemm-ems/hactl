package surfaceaudit_test

// Fixture modules for the two halves of H-29's surface that only run on a
// whole module: the request half's binary import closure, build-constraint
// sweep and typed check (PolicyRequestSites), and the command census's
// reachability over SSA (PolicyCommandSites). Every other policy fixture goes
// through PolicySurface's fallback walk, which never exercises either.

import (
	"bufio"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/hemm-ems/hactl/internal/surfaceaudit"
)

// writeModule lays out a throwaway Go module (no dependencies) and returns its
// root with symlinks resolved, so the paths go/packages reports and the paths
// the test computes agree.
func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	files["go.mod"] = "module example.com/fx\n\ngo 1.26\n"
	for rel, src := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const moduleWS = `package haapi

import (
	"context"
	"encoding/json"
)

type conn interface{ WriteJSON(v any) error }

type WSClient struct{ c conn }

func (ws *WSClient) sendCommand(ctx context.Context, cmdType string, params map[string]any) (json.RawMessage, error) {
	_, _ = ctx, params
	msg := map[string]any{"id": 1, "type": cmdType}
	return nil, ws.c.WriteJSON(msg)
}

func (ws *WSClient) AreaRegistryDelete(ctx context.Context, id string) error {
	_, err := ws.sendCommand(ctx, "config/area_registry/delete", map[string]any{"area_id": id})
	return err
}

func (ws *WSClient) AreaRegistryList(ctx context.Context) error {
	_, err := ws.sendCommand(ctx, "config/area_registry/list", nil)
	return err
}
`

const moduleClient = `package haapi

import (
	"context"
	"net/http"
)

type Client struct {
	base string
	hc   *http.Client
}

func (c *Client) doPost(ctx context.Context, path string, body any) ([]byte, error) {
	_ = body
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base+path, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	return nil, resp.Body.Close()
}

func (c *Client) FireEvent(ctx context.Context) error {
	_, err := c.doPost(ctx, "/api/events/hactl_fired", nil)
	return err
}
`

const moduleMain = `package main

import "example.com/fx/internal/cmd"

func main() { cmd.Main() }
`

// requestModule is a product whose sends all go through the builders.
func requestModule(extra map[string]string) map[string]string {
	files := map[string]string{
		"cmd/hactl/main.go":        moduleMain,
		"internal/haapi/ws.go":     moduleWS,
		"internal/haapi/client.go": moduleClient,
		"internal/cmd/cmd.go":      "package cmd\n\nimport _ \"example.com/fx/internal/haapi\"\n\nfunc Main() {}\n",
	}
	maps.Copy(files, extra)
	return files
}

func requestSiteKeys(t *testing.T, root string) []string {
	t.Helper()
	sites, err := surfaceaudit.PolicyRequestSites(root)
	if err != nil {
		t.Fatalf("deriving the request half: %v", err)
	}
	keys := make([]string, 0, len(sites))
	for _, s := range sites {
		keys = append(keys, s.Key)
	}
	return keys
}

// TestPolicyRequestSitesOnAModule — the binary's import closure is the
// product: the baseline derives exactly its sites, a send in a file no host
// build compiles is a site all the same, and a request built in any product
// package — not only the two clients — is refused.
func TestPolicyRequestSitesOnAModule(t *testing.T) {
	want := []string{"rest POST /api/events/hactl_fired", "ws config/area_registry/delete", "ws config/area_registry/list"}
	if got := requestSiteKeys(t, writeModule(t, requestModule(nil))); !slices.Equal(got, want) {
		t.Errorf("baseline sites %q, want %q", got, want)
	}

	t.Run("a send behind a build constraint", func(t *testing.T) {
		root := writeModule(t, requestModule(map[string]string{"internal/haapi/probe_never.go": `//go:build hactl_never_built

package haapi

import "context"

func (ws *WSClient) NeverBuilt(ctx context.Context) error {
	_, err := ws.sendCommand(ctx, "probe/never_built", nil)
	return err
}
`}))
		if got := requestSiteKeys(t, root); !slices.Contains(got, "ws probe/never_built") {
			t.Errorf("a send in a build-constrained file is no site: %q", got)
		}
	})

	t.Run("a request built in a third package", func(t *testing.T) {
		files := requestModule(map[string]string{
			"internal/other/other.go": `package other

import (
	"context"
	"net/http"
)

func Stop(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://ha/api/services/homeassistant/stop", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
`,
			"internal/cmd/cmd.go": "package cmd\n\nimport (\n\t_ \"example.com/fx/internal/haapi\"\n\t_ \"example.com/fx/internal/other\"\n)\n\nfunc Main() {}\n",
		})
		if sites, err := surfaceaudit.PolicyRequestSites(writeModule(t, files)); err == nil {
			t.Errorf("a request built outside the clients was not refused; sites %v", sites)
		}
	})
}

// TestPolicyTypedCheckSeesWhatSpellingCannot — the typed half of the request
// extractor: a *http.Client's Do of a request cloned from elsewhere, and a
// literal of a type that aliases http.Request, are no constructor call and no
// `http.Request{` to spell — only their types say they send.
func TestPolicyTypedCheckSeesWhatSpellingCannot(t *testing.T) {
	for name, src := range map[string]string{
		"a Do of a cloned request": `package haapi

import (
	"context"
	"net/http"
)

func (c *Client) Replay(ctx context.Context, r *http.Request) error {
	resp, err := c.hc.Do(r.Clone(ctx))
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
`,
		"a literal of an alias of http.Request": `package haapi

import "net/http"

type rq = http.Request

func blank() *rq { return &rq{Method: http.MethodPost} }

var _ = blank
`,
		"a RoundTrip outside a transport": `package haapi

import "net/http"

func (c *Client) Raw(r *http.Request) error {
	resp, err := http.DefaultTransport.RoundTrip(r)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
`,
	} {
		t.Run(name, func(t *testing.T) {
			root := writeModule(t, requestModule(map[string]string{"internal/haapi/probe.go": src}))
			_, err := surfaceaudit.PolicyRequestSites(root)
			if err == nil || !strings.Contains(err.Error(), "requests no site can name") {
				t.Errorf("not refused: %v", err)
			}
		})
	}
	// A transport's RoundTrip forwarding to its base is no new request.
	root := writeModule(t, requestModule(map[string]string{"internal/haapi/transport.go": `package haapi

import "net/http"

type hook struct{ base http.RoundTripper }

func (h *hook) RoundTrip(r *http.Request) (*http.Response, error) { return h.base.RoundTrip(r) }

var _ http.RoundTripper = (*hook)(nil)
`}))
	if _, err := surfaceaudit.PolicyRequestSites(root); err != nil {
		t.Errorf("a RoundTrip forwarding inside a RoundTrip method was refused: %v", err)
	}
}

// ---------------------------------------------------------------------------
// The command census.
// ---------------------------------------------------------------------------

// censusStubs are the declared sinks, stubbed: the census refuses to run when
// one is missing, so every fixture needs them.
var censusStubs = map[string]string{
	"internal/cache/store.go": `package cache

type Store struct{}

func (s *Store) Clear() error         { return nil }
func (s *Store) RefreshTraces() error { return nil }
func (s *Store) RefreshLogs() error   { return nil }
`,
	"internal/cache/timeseries.go": `package cache

type TSStore struct{}

func (s *TSStore) Clear() error { return nil }
`,
	"internal/cmd/setup.go": "package cmd\n\nfunc writeInstanceEnv() error { return nil }\n\nvar _ = writeInstanceEnv\n",
	"internal/cmd/root.go":  "package cmd\n\nfunc RunWithOutputContext() error { return nil }\n\nfunc Main() {}\n",
}

const censusCommands = `package cmd

import (
	"context"

	"example.com/fx/internal/cache"
	"example.com/fx/internal/haapi"
)

func connect() *haapi.WSClient { return &haapi.WSClient{} }

func runConfirmNoSink(ctx context.Context) error { _ = ctx; return nil }

func runFire(ctx context.Context) error { return (&haapi.Client{}).FireEvent(ctx) }

type areaDeleter interface {
	AreaRegistryDelete(ctx context.Context, id string) error
}

func runIface(ctx context.Context) error {
	var d areaDeleter = connect()
	return d.AreaRegistryDelete(ctx, "x")
}

var probeHook = func(ctx context.Context) error { return connect().AreaRegistryDelete(ctx, "x") }

func runSeam(ctx context.Context) error { return probeHook(ctx) }

func runRead(ctx context.Context) error { return connect().AreaRegistryList(ctx) }

func runPlain(ctx context.Context) error { _ = ctx; return nil }

func preHook(ctx context.Context) error { return connect().AreaRegistryDelete(ctx, "x") }

func runCache() error { return (&cache.Store{}).Clear() }

func runPkgVar(ctx context.Context) error { return haapi.PkgSend(ctx, connect()) }

var _ = []any{runConfirmNoSink, runFire, runIface, runSeam, runRead, runPlain, preHook, runCache, runPkgVar}
`

func censusModule(t *testing.T, extra map[string]string) string {
	t.Helper()
	files := requestModule(nil)
	delete(files, "internal/cmd/cmd.go")
	maps.Copy(files, censusStubs)
	files["internal/cmd/commands.go"] = censusCommands
	files["internal/haapi/pkgvar.go"] = `package haapi

import "context"

// PkgSend sends from a package-level func literal: the request half keys it
// by the variable's name, and so must the census's sink.
var PkgSend = func(ctx context.Context, ws *WSClient) error {
	_, err := ws.sendCommand(ctx, "config/area_registry/delete", nil)
	return err
}
`
	maps.Copy(files, extra)
	return writeModule(t, files)
}

// lineOf returns the census fixture's commands file and the line of it that
// starts with prefix.
func lineOf(t *testing.T, root, prefix string) (string, int) {
	t.Helper()
	const rel = "internal/cmd/commands.go"
	path := filepath.Join(root, filepath.FromSlash(rel))
	f, err := os.Open(path) //nolint:gosec // a fixture file under t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		if strings.HasPrefix(sc.Text(), prefix) {
			return path, n
		}
	}
	t.Fatalf("%s has no line starting %q", rel, prefix)
	return "", 0
}

func fixtureCommand(t *testing.T, root, path, run string, confirm bool, hooks ...string) surfaceaudit.PolicyCommand {
	t.Helper()
	file, line := lineOf(t, root, "func "+run+"(")
	c := surfaceaudit.PolicyCommand{Path: path, Confirm: confirm, RunFile: file, RunLine: line}
	for _, h := range hooks {
		hf, hl := lineOf(t, root, "func "+h+"(")
		c.Hooks = append(c.Hooks, surfaceaudit.FuncLoc{File: hf, Line: hl})
	}
	return c
}

func censusFixtureCommands(t *testing.T, root string) []surfaceaudit.PolicyCommand {
	t.Helper()
	return []surfaceaudit.PolicyCommand{
		fixtureCommand(t, root, "hactl confirm", "runConfirmNoSink", true),
		fixtureCommand(t, root, "hactl fire", "runFire", false),
		fixtureCommand(t, root, "hactl iface", "runIface", false),
		fixtureCommand(t, root, "hactl seam", "runSeam", false),
		fixtureCommand(t, root, "hactl read", "runRead", false),
		fixtureCommand(t, root, "hactl hooked", "runPlain", false, "preHook"),
		fixtureCommand(t, root, "hactl plain", "runPlain", false),
		fixtureCommand(t, root, "hactl cache", "runCache", false),
		fixtureCommand(t, root, "hactl pkgvar", "runPkgVar", false),
	}
}

// TestPolicyCommandSitesOnAModule — the census's derivation, watched on a
// fixture: a --confirm command is a site even when its Run reaches nothing
// (D-80); a Run reaching an unmapped POST, a write reached only through an
// interface, through a package-level func variable, from a send inside a
// package-level func literal, or only from a PreRun hook, and a declared
// sink, all make a site; a read command and a command
// whose Run reaches nothing do not.
func TestPolicyCommandSitesOnAModule(t *testing.T) {
	root := censusModule(t, nil)
	sites, err := surfaceaudit.PolicyCommandSites(root, censusFixtureCommands(t, root))
	if err != nil {
		t.Fatalf("deriving the census: %v", err)
	}
	got := make([]string, 0, len(sites))
	for _, s := range sites {
		got = append(got, s.Key)
	}
	want := []string{"cmd hactl cache", "cmd hactl confirm", "cmd hactl fire", "cmd hactl hooked", "cmd hactl iface", "cmd hactl pkgvar", "cmd hactl seam"}
	if !slices.Equal(got, want) {
		t.Errorf("census\n  got  %q\n  want %q", got, want)
	}
}

// TestPolicyCommandSitesRefusesWhatItCannotLocate — a declared sink that no
// longer exists, and a Run or hook the runtime places where no function is,
// are errors: a census built past either would prove nothing.
func TestPolicyCommandSitesRefusesWhatItCannotLocate(t *testing.T) {
	t.Run("a declared sink is gone", func(t *testing.T) {
		root := censusModule(t, map[string]string{"internal/cache/store.go": `package cache

type Store struct{}

func (s *Store) Clear() error         { return nil }
func (s *Store) RefreshTraces() error { return nil }
`})
		_, err := surfaceaudit.PolicyCommandSites(root, censusFixtureCommands(t, root))
		if err == nil || !strings.Contains(err.Error(), "RefreshLogs") {
			t.Errorf("a missing declared sink was not refused by name: %v", err)
		}
	})
	t.Run("a Run at no function", func(t *testing.T) {
		root := censusModule(t, nil)
		file, line := lineOf(t, root, "import (")
		cmds := []surfaceaudit.PolicyCommand{{Path: "hactl nowhere", RunFile: file, RunLine: line}}
		if _, err := surfaceaudit.PolicyCommandSites(root, cmds); err == nil {
			t.Error("a Run the runtime places at no function was skipped silently")
		}
	})
	t.Run("a hook at no function", func(t *testing.T) {
		root := censusModule(t, nil)
		c := fixtureCommand(t, root, "hactl plain", "runPlain", false)
		file, line := lineOf(t, root, "import (")
		c.Hooks = []surfaceaudit.FuncLoc{{File: file, Line: line}}
		if _, err := surfaceaudit.PolicyCommandSites(root, []surfaceaudit.PolicyCommand{c}); err == nil {
			t.Error("a hook the runtime places at no function was skipped silently")
		}
	})
}

// TestPolicyRequestSitesCoverEveryReleaseTarget (A-9) — the request half is
// derived for every GOOS/GOARCH .goreleaser.yaml builds, not for the host
// that runs the gate: a package only the windows build imports is product,
// and a request it builds outside the clients is refused. Without the
// release file the same module is read for the host alone and the package is
// not seen — which is what makes the release-file case able to fail.
func TestPolicyRequestSitesCoverEveryReleaseTarget(t *testing.T) {
	winOnly := map[string]string{
		"internal/cmd/win.go": "//go:build windows\n\npackage cmd\n\nimport _ \"example.com/fx/internal/winonly\"\n",
		"internal/winonly/winonly.go": `package winonly

import (
	"context"
	"net/http"
)

func Stop(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://ha/api/services/homeassistant/stop", nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	return resp.Body.Close()
}
`,
	}
	if runtimeIsWindows() {
		t.Skip("the host is windows: the host-only build already compiles the windows-only package")
	}
	if _, err := surfaceaudit.PolicyRequestSites(writeModule(t, requestModule(maps.Clone(winOnly)))); err != nil {
		t.Fatalf("host-only derivation (no .goreleaser.yaml) refused a package only windows imports: %v", err)
	}

	withRelease := maps.Clone(winOnly)
	withRelease[".goreleaser.yaml"] = "version: 2\nbuilds:\n  - main: ./cmd/hactl\n    goos: [linux, windows]\n    goarch: [amd64]\n"
	_, err := surfaceaudit.PolicyRequestSites(writeModule(t, requestModule(withRelease)))
	if err == nil || !strings.Contains(err.Error(), "internal/winonly/winonly.go") {
		t.Errorf("a request built in a package only the windows release imports was not refused: %v", err)
	}

	empty := maps.Clone(winOnly)
	empty[".goreleaser.yaml"] = "version: 2\nbuilds:\n  - main: ./cmd/hactl\n"
	if _, err := surfaceaudit.PolicyRequestSites(writeModule(t, requestModule(empty))); err == nil ||
		!strings.Contains(err.Error(), "no goos/goarch") {
		t.Errorf("a release file listing no target was read as the host: %v", err)
	}
}

func runtimeIsWindows() bool { return filepath.Separator == '\\' }
