package cmd

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// mcpStdoutTimeout bounds every wait in this test; it is generous because the
// first tool result carries the whole manual.
const mcpStdoutTimeout = 30 * time.Second

// TestMCPStdoutCarriesOnlyProtocol runs `hactl mcp` through the real
// Execute() path — os.Stdin and os.Stdout swapped for pipes, the real runner,
// the real command tree — and asserts that every byte the process writes to
// stdout is a JSON-RPC message.
//
// stdout is the transport: one stray line corrupts the stream for every
// client. Two mechanisms in this binary write to stdout on their own —
// Execute() flushes the captured command output there after the command
// returns, and the manual injection hook writes when a caller looks like an
// agent. Both are supposed to stay off this path (the manual is delivered
// *inside* the first tool result, which this test also pins), and nothing
// asserted it until a live-fire session asked the question.
func TestMCPStdoutCarriesOnlyProtocol(t *testing.T) {
	p := startMCPOverPipes(t)

	send := func(raw string) {
		t.Helper()
		if _, err := io.WriteString(p.in, raw+"\n"); err != nil {
			t.Errorf("write to server stdin: %v", err)
		}
	}

	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"purity","version":"0"}}}`)
	lines := []string{receiveLine(t, p.stdout)}
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	// `version` needs no Home Assistant, and it is not rtfm — so this is the
	// first result, the one the manual is injected into.
	send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"hactl","arguments":{"command":"version"}}}`)
	lines = append(lines, receiveLine(t, p.stdout))

	_ = p.in.Close()
	select {
	case err := <-p.done:
		if err != nil {
			t.Fatalf("hactl mcp ended with %v, want a clean exit when stdin closes", err)
		}
	case <-time.After(mcpStdoutTimeout):
		t.Fatal("hactl mcp did not return after stdin closed")
	}
	// The run is over, so whatever else it wrote is already in the pipe:
	// close the write end and collect it. Anything that arrives here is
	// exactly the pollution this test exists to catch.
	p.closeOut()
	for line := range p.stdout {
		lines = append(lines, line)
	}

	if len(lines) != 2 {
		t.Fatalf("stdout carries %d lines, want exactly the 2 answers: %.200q", len(lines), lines)
	}
	body := renderText(t, toolCallResult(t, lines))
	if !strings.Contains(body, "[hactl manual") {
		t.Errorf("the manual must be delivered inside the first tool result, got %.200q", body)
	}
	if !strings.Contains(body, "=== RESULT of 'version' ===") {
		t.Errorf("the tool result must still carry the command output, got %.200q", body)
	}
}

// mcpPipes is a running `hactl mcp` and the ends of its stdio.
type mcpPipes struct {
	in       *os.File    // the client's end of the server's stdin
	stdout   chan string // the server's stdout, one line per message
	done     chan error  // what Execute() returned
	closeOut func()      // close the server's stdout, ending the line stream
}

// startMCPOverPipes runs Execute() as `hactl mcp` with the process's own
// stdin and stdout replaced by pipes.
//
// stdout is drained by a goroutine rather than read at the end: the first
// tool result is far larger than a pipe buffer, so a server writing into an
// unread pipe would deadlock instead of answering.
func startMCPOverPipes(t *testing.T) *mcpPipes {
	t.Helper()
	inR, inW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	outR, outW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}

	resetSubcommandFlags()
	flagDir = ""
	oldIn, oldOut, oldArgs := os.Stdin, os.Stdout, os.Args
	os.Stdin, os.Stdout, os.Args = inR, outW, []string{"hactl", "mcp"}
	rootCmd.SetArgs([]string{"mcp"})

	stdout := make(chan string, 16)
	go func() {
		br := bufio.NewReaderSize(outR, 1<<20)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				stdout <- strings.TrimRight(line, "\n")
			}
			if err != nil {
				close(stdout)
				return
			}
		}
	}()

	done := make(chan error, 1)
	go func() { done <- Execute() }()

	var closeOutOnce sync.Once
	closeOut := func() { closeOutOnce.Do(func() { _ = outW.Close() }) }
	t.Cleanup(func() {
		os.Stdin, os.Stdout, os.Args = oldIn, oldOut, oldArgs
		rootCmd.SetArgs(nil)
		resetSubcommandFlags()
		_ = inW.Close()
		closeOut()
		_ = inR.Close()
		_ = outR.Close()
	})
	return &mcpPipes{in: inW, stdout: stdout, done: done, closeOut: closeOut}
}

func receiveLine(t *testing.T, stdout chan string) string {
	t.Helper()
	select {
	case line, ok := <-stdout:
		if !ok {
			t.Fatal("the server closed stdout instead of answering")
		}
		return line
	case <-time.After(mcpStdoutTimeout):
		t.Fatal("timed out waiting for an answer")
		return ""
	}
}

// toolCallResult checks that every line is a JSON-RPC message — the purity
// assertion — and returns the result of the tools/call.
func toolCallResult(t *testing.T, lines []string) map[string]any {
	t.Helper()
	var result map[string]any
	for i, line := range lines {
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("stdout line %d is not JSON — the protocol stream is polluted: %.200q", i, line)
		}
		if msg["jsonrpc"] != "2.0" {
			t.Fatalf("stdout line %d is not a JSON-RPC message: %.200q", i, line)
		}
		if msg["id"] == float64(2) {
			result, _ = msg["result"].(map[string]any)
		}
	}
	if result == nil {
		t.Fatal("the tool call was not answered")
	}
	return result
}

func renderText(t *testing.T, result map[string]any) string {
	t.Helper()
	content, ok := result["content"].([]any)
	if !ok {
		t.Fatalf("tool result carries no content: %v", result)
	}
	var sb strings.Builder
	for _, c := range content {
		block, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if text, ok := block["text"].(string); ok {
			sb.WriteString(text)
		}
	}
	return sb.String()
}
