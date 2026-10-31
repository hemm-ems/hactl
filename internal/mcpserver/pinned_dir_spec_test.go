package mcpserver_test

// D-64 spec test, authored from docs/decisions.md D-62/D-64 and the manual's
// "Which profile applies" paragraph: a server pinned with `hactl mcp --dir`
// refuses a tool call whose --dir names another directory (the directory
// chooses the policy file, so choosing it per call is choosing the policy);
// a call naming the pinned directory runs; a server without --dir is not
// pinned. The CLI end-to-end half is TestPinnedMCPServerRefusesAnotherInstanceDir
// in internal/cmd.

import (
	"context"
	"io"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hemm-ems/hactl/internal/mcpserver"
)

type recordingRunner struct {
	calls [][]string
}

func (r *recordingRunner) run(_ context.Context, args []string, _ io.Writer) error {
	r.calls = append(r.calls, slices.Clone(args))
	return nil
}

func resolveEntLs(args []string) (string, error) {
	if slices.Contains(args, "ls") {
		return "hactl ent ls", nil
	}
	return "hactl ent", nil
}

func callPinned(t *testing.T, opts mcpserver.Options, command string) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := mcpserver.NewServer(opts).Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Wait() }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "hactl", Arguments: map[string]any{"command": command}})
	if err != nil {
		t.Fatalf("CallTool(%q): %v", command, err)
	}
	return res
}

// TestPinnedServerRefusesACallTimeDir — D-64 (finding 2). Red today: the
// caller's --dir wins (`hasDirFlag` skips the pinned injection).
func TestPinnedServerRefusesACallTimeDir(t *testing.T) {
	pinned := t.TempDir()
	other := t.TempDir()

	for _, command := range []string{
		"ent ls --dir " + other,
		"ent ls --dir=" + other,
		"--dir " + other + " ent ls",
	} {
		rr := &recordingRunner{}
		res := callPinned(t, mcpserver.Options{Runner: rr.run, ResolvePath: resolveEntLs, Dir: pinned, NoManualInject: true}, command)
		if !res.IsError {
			t.Errorf("%q on a server pinned to %s: not an error result, want a refusal (D-64)", command, pinned)
		}
		if len(rr.calls) != 0 {
			t.Errorf("%q: runner called with %v; a refused call runs nothing", command, rr.calls)
		}
	}

	// The pinned directory itself — as given, and with a trailing separator,
	// which names the same directory — runs, against the pinned directory.
	for _, command := range []string{
		"ent ls --dir " + pinned,
		"ent ls --dir " + pinned + string(filepath.Separator),
	} {
		rr := &recordingRunner{}
		res := callPinned(t, mcpserver.Options{Runner: rr.run, ResolvePath: resolveEntLs, Dir: pinned, NoManualInject: true}, command)
		if res.IsError || len(rr.calls) != 1 {
			t.Errorf("%q: IsError=%v calls=%v, want it to run once", command, res.IsError, rr.calls)
			continue
		}
		if strings.Contains(strings.Join(rr.calls[0], " "), other) {
			t.Errorf("%q ran with %v", command, rr.calls[0])
		}
	}

	// HACTL_DIR in the server's environment cannot re-point a pinned server:
	// the pinned --dir is what the command receives.
	t.Setenv("HACTL_DIR", other)
	rr := &recordingRunner{}
	res := callPinned(t, mcpserver.Options{Runner: rr.run, ResolvePath: resolveEntLs, Dir: pinned, NoManualInject: true}, "ent ls")
	if res.IsError || len(rr.calls) != 1 {
		t.Fatalf("plain call: IsError=%v calls=%v, want one run", res.IsError, rr.calls)
	}
	args := rr.calls[0]
	i := slices.Index(args, "--dir")
	if i < 0 || i+1 >= len(args) || args[i+1] != pinned {
		t.Errorf("runner args %v, want --dir %s (the pinned instance answers, never HACTL_DIR)", args, pinned)
	}

	// A server started without --dir is not pinned: the caller's --dir runs.
	rr = &recordingRunner{}
	res = callPinned(t, mcpserver.Options{Runner: rr.run, ResolvePath: resolveEntLs, NoManualInject: true}, "ent ls --dir "+other)
	if res.IsError || len(rr.calls) != 1 {
		t.Errorf("unpinned server: IsError=%v calls=%v, want the call to run", res.IsError, rr.calls)
	}
}
