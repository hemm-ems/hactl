package cmd

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hemm-ems/hactl/internal/mcpserver"
	"github.com/hemm-ems/hactl/internal/policy"
)

// mcpCall runs one tool call through the real MCP server, resolver and
// runner, pinned to dir with --allow-writes — the agent's own setup.
func mcpCall(t *testing.T, dir, command string) (string, bool) {
	t.Helper()
	// RunWithOutputContext leaves the tool call's (cancelled) context on
	// rootCmd; a later Execute() in this test binary would inherit it and end
	// at once with "context canceled". In a real process nothing runs after
	// the server, so only the test needs the reset.
	t.Cleanup(func() { rootCmd.SetContext(context.Background()) })
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()
	srv := mcpserver.NewServer(mcpserver.Options{
		Runner: RunWithOutputContext, ResolvePath: FindCommandPath,
		AllowWrites: true, NoManualInject: true, Dir: dir, Version: "test",
	})
	ss, err := srv.Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ss.Wait() }()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cs.Close() }()
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "hactl", Arguments: map[string]any{"command": command}})
	if err != nil {
		t.Fatalf("CallTool(%q): %v", command, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	return sb.String(), res.IsError
}

// D-149: over MCP — even on a server with --allow-writes, even if the process
// had a terminal — `policy activate` and `agent add` are never run; the
// refusal says they are the owner's and ends with the owner hint.
func TestMCPRefusesPolicyActivate(t *testing.T) {
	withOwnerTerminal(t) // the gate must not be what refuses it here
	ts := policyRig(t, starterPolicy)
	if err := os.WriteFile(filepath.Join(ts.dir, "d.yaml"), []byte(activateDraft), 0o600); err != nil {
		t.Fatal(err)
	}
	// Even an answer waiting on stdin changes nothing.
	r, w, _ := os.Pipe()
	_, _ = w.WriteString("agent\n")
	_ = w.Close()
	oldIn := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = oldIn; _ = r.Close() }()

	for _, command := range []string{"policy activate d.yaml", "agent add helper"} {
		text, isErr := mcpCall(t, ts.dir, command)
		if !isErr || !strings.Contains(text, "is not available over MCP: only the owner runs it") ||
			!strings.Contains(text, "ask the owner; a change goes through a draft and `hactl policy activate`") {
			t.Errorf("MCP %q: isError=%v\n%s", command, isErr, text)
		}
	}
	if got := readFile(t, filepath.Join(ts.dir, policy.FileName)); got != starterPolicy {
		t.Error("policy.yaml changed through MCP")
	}
	// The agent can still read and review its draft over MCP.
	text, isErr := mcpCall(t, ts.dir, "policy review --file d.yaml")
	if isErr || !strings.Contains(text, "switch: 2 of 8") {
		t.Errorf("policy review over MCP: isError=%v\n%s", isErr, text)
	}
}
