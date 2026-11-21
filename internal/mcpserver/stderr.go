package mcpserver

import (
	"context"
	"io"
	"os"
)

type stderrKey struct{}

// WithStderr carries the writer a command run in-process writes its stderr
// lines to — the `hidden by policy` trailer under --json (D-139) — instead
// of the process's own stderr, which over MCP is the server's log.
func WithStderr(ctx context.Context, w io.Writer) context.Context {
	return context.WithValue(ctx, stderrKey{}, w)
}

// Stderr is the writer WithStderr put on ctx, or os.Stderr.
func Stderr(ctx context.Context) io.Writer {
	if ctx != nil {
		if w, ok := ctx.Value(stderrKey{}).(io.Writer); ok && w != nil {
			return w
		}
	}
	return os.Stderr
}
