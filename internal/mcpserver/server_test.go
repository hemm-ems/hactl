package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// fakeResolver resolves the leading non-flag tokens against a static tree,
// mimicking cmd.FindCommandPath closely enough for handler tests.
func fakeResolver(args []string) (string, error) {
	known := map[string]bool{
		"hactl ent ls":   true,
		"hactl ent show": true,
		"hactl svc call": true,
		"hactl setup":    true,
		"hactl version":  true,
		"hactl rtfm":     true,
	}
	path := "hactl"
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if a == "--dir" {
			continue
		}
		if known[path+" "+a] || a == "ent" || a == "svc" {
			path += " " + a
		}
	}
	return path, nil
}

func callTool(t *testing.T, opts Options, command string) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	srv := NewServer(opts)
	srvSession, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = srvSession.Wait() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.CallTool(ctx, &mcp.CallToolParams{
		Name:      "hactl",
		Arguments: map[string]any{"command": command},
	})
	if err != nil {
		t.Fatalf("CallTool(%q): %v", command, err)
	}
	return res
}

func textOf(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String()
}

func TestToolHandlerRunsCommand(t *testing.T) {
	var gotArgs []string
	opts := Options{
		Runner: func(_ context.Context, args []string, w io.Writer) error {
			gotArgs = args
			_, _ = fmt.Fprint(w, "id state\nlight.x on\n")
			return nil
		},
		ResolvePath: fakeResolver,
	}
	res := callTool(t, opts, "ent ls --domain light")
	if res.IsError {
		t.Fatalf("unexpected error result: %s", textOf(t, res))
	}
	want := []string{"hactl", "ent", "ls", "--domain", "light"}
	if strings.Join(gotArgs, " ") != strings.Join(want, " ") {
		t.Errorf("runner args = %v, want %v", gotArgs, want)
	}
	if got := textOf(t, res); !strings.Contains(got, "light.x") {
		t.Errorf("result content = %q, want command output", got)
	}
}

func TestToolHandlerStripsLeadingHactl(t *testing.T) {
	var gotArgs []string
	opts := Options{
		Runner: func(_ context.Context, args []string, w io.Writer) error {
			gotArgs = args
			return nil
		},
		ResolvePath: fakeResolver,
	}
	_ = callTool(t, opts, "hactl ent ls")
	if strings.Join(gotArgs, " ") != "hactl ent ls" {
		t.Errorf("runner args = %v, want [hactl ent ls]", gotArgs)
	}
}

func TestToolHandlerPreservesQuotedArgs(t *testing.T) {
	var gotArgs []string
	opts := Options{
		Runner: func(_ context.Context, args []string, w io.Writer) error {
			gotArgs = args
			return nil
		},
		ResolvePath: fakeResolver,
		AllowWrites: true,
	}
	_ = callTool(t, opts, `svc call light.turn_on --data '{"entity_id": "light.x"}'`)
	want := `{"entity_id": "light.x"}`
	if len(gotArgs) == 0 || gotArgs[len(gotArgs)-1] != want {
		t.Errorf("runner args = %#v, want last arg %q", gotArgs, want)
	}
}

func TestToolHandlerInjectsPinnedDir(t *testing.T) {
	var gotArgs []string
	runner := func(_ context.Context, args []string, w io.Writer) error {
		gotArgs = args
		return nil
	}

	opts := Options{Runner: runner, ResolvePath: fakeResolver, Dir: "/tmp/instance"}
	_ = callTool(t, opts, "ent ls")
	if strings.Join(gotArgs, " ") != "hactl --dir /tmp/instance ent ls" {
		t.Errorf("pinned dir not injected: %v", gotArgs)
	}

	// An explicit --dir from the caller wins over the pinned one.
	_ = callTool(t, opts, "--dir /other ent ls")
	if strings.Join(gotArgs, " ") != "hactl --dir /other ent ls" {
		t.Errorf("caller --dir overridden: %v", gotArgs)
	}
}

func TestToolHandlerBlocksWritesReadOnly(t *testing.T) {
	called := false
	opts := Options{
		Runner: func(_ context.Context, args []string, w io.Writer) error {
			called = true
			return nil
		},
		ResolvePath: fakeResolver,
	}
	res := callTool(t, opts, "svc call light.turn_on")
	if !res.IsError {
		t.Fatal("expected IsError result for blocked write")
	}
	if called {
		t.Error("runner must not be called for a blocked command")
	}
	if got := textOf(t, res); !strings.Contains(got, "--allow-writes") {
		t.Errorf("block message should teach the fix, got %q", got)
	}
}

func TestToolHandlerSurfacesRunnerError(t *testing.T) {
	opts := Options{
		Runner: func(_ context.Context, args []string, w io.Writer) error {
			_, _ = fmt.Fprint(w, "partial output")
			return errors.New("no .env found")
		},
		ResolvePath: fakeResolver,
	}
	res := callTool(t, opts, "ent ls")
	if !res.IsError {
		t.Fatal("expected IsError result for runner error")
	}
	got := textOf(t, res)
	if !strings.Contains(got, "partial output") || !strings.Contains(got, "no .env found") {
		t.Errorf("result should keep partial output and the error, got %q", got)
	}
}

func TestToolHandlerRejectsBadQuoting(t *testing.T) {
	opts := Options{Runner: nil, ResolvePath: fakeResolver}
	res := callTool(t, opts, `ent ls --pattern 'unterminated`)
	if !res.IsError {
		t.Fatal("expected IsError result for unparseable command")
	}
}

func TestToolDescriptionReflectsMode(t *testing.T) {
	ro := toolDescription(Options{})
	if !strings.Contains(ro, "READ-ONLY") || !strings.Contains(ro, "--allow-writes") {
		t.Errorf("read-only description should state the mode and the flag, got %q", ro)
	}
	rw := toolDescription(Options{AllowWrites: true})
	if !strings.Contains(rw, "ENABLED") {
		t.Errorf("writes-enabled description should say so, got %q", rw)
	}
	pinned := toolDescription(Options{Dir: "/tmp/x"})
	if !strings.Contains(pinned, "/tmp/x") {
		t.Errorf("pinned description should name the instance dir, got %q", pinned)
	}
}

// newSession connects a client to a fresh server and returns a CallTool
// helper bound to that single session, for tests that need call sequencing.
func newSession(t *testing.T, opts Options) func(command string) *mcp.CallToolResult {
	t.Helper()
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	srv := NewServer(opts)
	srvSession, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = srvSession.Wait() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return func(command string) *mcp.CallToolResult {
		res, err := session.CallTool(ctx, &mcp.CallToolParams{
			Name:      "hactl",
			Arguments: map[string]any{"command": command},
		})
		if err != nil {
			t.Fatalf("CallTool(%q): %v", command, err)
		}
		return res
	}
}

const injectMarker = "[hactl manual — delivered once"

func TestManualInjectedOnFirstResultOnly(t *testing.T) {
	call := newSession(t, Options{
		Runner: func(_ context.Context, _ []string, w io.Writer) error {
			_, _ = fmt.Fprint(w, "id state\nlight.x on\n")
			return nil
		},
		ResolvePath: fakeResolver,
	})

	first := textOf(t, call("ent ls"))
	if !strings.Contains(first, injectMarker) || !strings.Contains(first, "=== RESULT of 'ent ls' ===") {
		t.Errorf("first result should carry the manual and the result separator, got %.200q", first)
	}
	if !strings.Contains(first, "light.x") {
		t.Errorf("first result should still contain the command output, got %.200q", first)
	}

	second := textOf(t, call("ent ls"))
	if strings.Contains(second, injectMarker) {
		t.Errorf("second result must not re-deliver the manual, got %.200q", second)
	}
}

func TestManualNotDoubledWhenRtfmIsFirst(t *testing.T) {
	call := newSession(t, Options{
		Runner: func(_ context.Context, args []string, w io.Writer) error {
			_, _ = fmt.Fprint(w, "manual body from rtfm")
			return nil
		},
		ResolvePath: fakeResolver,
	})

	first := textOf(t, call("rtfm"))
	if strings.Contains(first, injectMarker) {
		t.Errorf("rtfm output must not get the manual prepended again, got %.200q", first)
	}
	second := textOf(t, call("ent ls"))
	if strings.Contains(second, injectMarker) {
		t.Errorf("rtfm counts as delivery; second call must be clean, got %.200q", second)
	}
}

func TestManualInjectionDisabled(t *testing.T) {
	call := newSession(t, Options{
		Runner: func(_ context.Context, _ []string, w io.Writer) error {
			_, _ = fmt.Fprint(w, "plain output")
			return nil
		},
		ResolvePath:    fakeResolver,
		NoManualInject: true,
	})
	if got := textOf(t, call("ent ls")); strings.Contains(got, injectMarker) {
		t.Errorf("--no-manual-inject must suppress delivery, got %.200q", got)
	}
}

func TestManualInjectedEvenOnFirstError(t *testing.T) {
	call := newSession(t, Options{
		Runner: func(_ context.Context, _ []string, w io.Writer) error {
			return errors.New("no .env found")
		},
		ResolvePath: fakeResolver,
	})
	res := call("ent ls")
	if !res.IsError {
		t.Fatal("expected IsError result")
	}
	got := textOf(t, res)
	if !strings.Contains(got, injectMarker) || !strings.Contains(got, "no .env found") {
		t.Errorf("error result should carry manual and error, got %.200q", got)
	}
}

func TestToolDescriptionReflectsInjection(t *testing.T) {
	def := toolDescription(Options{})
	if !strings.Contains(def, "delivered together with your first tool result") {
		t.Errorf("default description should announce manual delivery, got %q", def)
	}
	off := toolDescription(Options{NoManualInject: true})
	if !strings.Contains(off, "Start by running 'rtfm' once") {
		t.Errorf("no-inject description should point at rtfm, got %q", off)
	}
}

func TestManualResource(t *testing.T) {
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	srv := NewServer(Options{ResolvePath: fakeResolver})
	if _, err := srv.Connect(ctx, serverT, nil); err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	session, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.ReadResource(ctx, &mcp.ReadResourceParams{URI: "hactl://manual"})
	if err != nil {
		t.Fatalf("ReadResource: %v", err)
	}
	if len(res.Contents) != 1 || len(res.Contents[0].Text) < 1000 {
		t.Errorf("manual resource looks empty: %+v", res.Contents)
	}
}
