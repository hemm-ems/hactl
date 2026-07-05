package mcpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/google/shlex"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/hemm-ems/hactl/docs"
)

// Options wires the MCP server to the CLI without importing internal/cmd.
type Options struct {
	// Runner executes a hactl command line; args include the leading
	// binary name (cmd.RunWithOutputContext). The context comes from the
	// MCP request, so client cancellation reaches the command.
	Runner func(ctx context.Context, args []string, w io.Writer) error
	// ResolvePath maps raw args (without binary name) to a canonical
	// command path like "hactl ent set-label" (cmd.FindCommandPath).
	ResolvePath func(args []string) (string, error)
	// AllowWrites permits mutating commands.
	AllowWrites bool
	// NoManualInject disables prepending the manual to the first tool
	// result of the session (the default self-teaching mechanism; clients
	// that inject hactl://manual themselves can turn it off).
	NoManualInject bool
	// Dir pins the server to one instance directory; empty means normal
	// discovery (CWD walk, HACTL_DIR, ~/.hactl/default).
	Dir     string
	Version string
}

// runMu serializes tool calls: the CLI runner mutates package-global flag
// state in internal/cmd (see resetSubcommandFlags), so concurrent
// invocations would leak flags between commands.
var runMu sync.Mutex

// Run serves the MCP protocol on stdio until the client disconnects or ctx
// is cancelled. Nothing here may write to stdout except the transport.
func Run(ctx context.Context, opts Options) error {
	return NewServer(opts).Run(ctx, &mcp.StdioTransport{})
}

// NewServer builds the MCP server with the hactl tool and manual resource.
// Exposed separately from Run so tests can connect over in-memory transports.
func NewServer(opts Options) *mcp.Server {
	server := mcp.NewServer(&mcp.Implementation{Name: "hactl", Version: opts.Version}, nil)

	mcp.AddTool(server, &mcp.Tool{Name: "hactl", Description: toolDescription(opts)}, toolHandler(opts))

	server.AddResource(&mcp.Resource{
		URI:         "hactl://manual",
		Name:        "hactl-manual",
		Title:       "hactl manual",
		Description: "Full hactl CLI manual (same content as 'hactl rtfm')",
		MIMEType:    "text/markdown",
	}, func(_ context.Context, req *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI:      req.Params.URI,
			MIMEType: "text/markdown",
			Text:     docs.Manual,
		}}}, nil
	})

	return server
}

type toolInput struct {
	Command string `json:"command" jsonschema:"hactl command line without the binary name, e.g. 'ent ls --domain light'"`
}

func toolDescription(opts Options) string {
	var b strings.Builder
	b.WriteString("Run a hactl command. hactl is a Home Assistant analysis and management CLI " +
		"tuned for LLM use: output is plain text capped at ~500 tokens by default. " +
		"Use --tokens to add a compact [~N tok] size header.\n\n" +
		"Pass the command line without the binary name, e.g. 'ent ls --domain light' or " +
		"'auto show <id>'. Useful global flags: --json (structured output), --tokens " +
		"(compact token estimate), --tokensmax N " +
		"(raise/remove the output cap, 0 = uncapped), --since 7d, --top N, --full.\n\n")
	if opts.NoManualInject {
		b.WriteString("Start by running 'rtfm' once: it prints the full manual of all commands and is also " +
			"available as the MCP resource hactl://manual.\n\n")
	} else {
		b.WriteString("The full manual is delivered together with your first tool result — read it before " +
			"interpreting anything; it documents every command, flag, and workflow. " +
			"(Also available on demand via 'rtfm' or the MCP resource hactl://manual.)\n\n")
	}
	if opts.AllowWrites {
		b.WriteString("Writes are ENABLED: mutating commands (svc call, auto apply/create/delete, script apply, ...) " +
			"are permitted. Every write is still dry-run unless --confirm is given; only pass --confirm " +
			"after the user explicitly confirmed the exact action — the original request is not that confirmation.")
	} else {
		b.WriteString("READ-ONLY: mutating commands (svc call, auto apply/create/delete, script apply/run, " +
			"helper/dash/area/floor/label create/delete, ...) are blocked; do not attempt them. " +
			"The server must be restarted with 'hactl mcp --allow-writes' to enable writes.")
	}
	if opts.Dir != "" {
		fmt.Fprintf(&b, "\n\nThis server is pinned to the instance at %s.", opts.Dir)
	}
	return b.String()
}

func toolHandler(opts Options) mcp.ToolHandlerFor[toolInput, any] {
	// Per-server session state: the first tool result carries the manual so
	// the agent self-teaches without spending a round on rtfm (measured best
	// delivery mechanism, see docs/llm-tuning.md). Guarded by runMu.
	manualDelivered := opts.NoManualInject
	return func(ctx context.Context, _ *mcp.CallToolRequest, in toolInput) (*mcp.CallToolResult, any, error) {
		tokens, err := shlex.Split(in.Command)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot parse command %q: %w", in.Command, err)
		}
		if len(tokens) > 0 && tokens[0] == "hactl" {
			tokens = tokens[1:]
		}
		if len(tokens) == 0 {
			return nil, nil, errors.New("empty command; pass hactl CLI args, e.g. 'ent ls'")
		}
		// Re-inject the pinned --dir on every call: the runner resets all
		// global flags (including --dir) after each invocation.
		if opts.Dir != "" && !hasDirFlag(tokens) {
			tokens = append([]string{"--dir", opts.Dir}, tokens...)
		}

		path, err := opts.ResolvePath(tokens)
		if err != nil {
			return nil, nil, fmt.Errorf("unknown command %q: %w", in.Command, err)
		}
		if d, reason := Gate(path, opts.AllowWrites); d != Allowed {
			return nil, nil, fmt.Errorf("%s", reason)
		}

		runMu.Lock()
		defer runMu.Unlock()
		var buf bytes.Buffer
		runErr := opts.Runner(ctx, append([]string{"hactl"}, tokens...), &buf)
		out := buf.String()
		if !manualDelivered {
			// First result of the session: prepend the manual (unless the
			// call was rtfm — its output already is the manual). Injected
			// even on errors: that's when the agent needs the manual most.
			manualDelivered = true
			if path != "hactl rtfm" {
				out = "[hactl manual — delivered once with this first tool result. Use it for every " +
					"subsequent command, flag, and workflow decision.]\n\n" + docs.Manual +
					"\n\n=== RESULT of '" + in.Command + "' ===\n" + out
			}
		}
		if runErr != nil {
			// The root command silences errors; surface them here, keeping
			// any partial output the command produced.
			if out != "" && !strings.HasSuffix(out, "\n") {
				out += "\n"
			}
			return &mcp.CallToolResult{
				IsError: true,
				Content: []mcp.Content{&mcp.TextContent{Text: out + "error: " + runErr.Error()}},
			}, nil, nil
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.TextContent{Text: out}},
		}, nil, nil
	}
}

func hasDirFlag(tokens []string) bool {
	for _, t := range tokens {
		if t == "--dir" || strings.HasPrefix(t, "--dir=") {
			return true
		}
	}
	return false
}
