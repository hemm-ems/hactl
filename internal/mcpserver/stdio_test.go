package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// The hostile-sequence harness. It drives a real server over the real stdio
// transport with raw lines, because that is the only place the defect this
// file covers lived: `hactl mcp` used to exit 1 on the first message that did
// not decode, so one bad line from any client ended the session and every
// later write hit a dead pipe. Every test here asserts the same two things —
// what the bad message was answered with, and that the *next* request is
// still served.

const rigTimeout = 5 * time.Second

type safeBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type rig struct {
	t     *testing.T
	in    *io.PipeWriter
	lines chan string
	log   *safeBuf
	done  chan error
	ended bool
}

// newRig starts a server on a pipe pair and returns a handle for sending raw
// lines and reading raw responses.
func newRig(t *testing.T, opts Options) *rig {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	r := &rig{
		t:     t,
		in:    inW,
		lines: make(chan string, 64),
		log:   &safeBuf{},
		done:  make(chan error, 1),
	}

	go func() {
		br := bufio.NewReaderSize(outR, 1<<20)
		for {
			line, err := br.ReadString('\n')
			if line != "" {
				r.lines <- strings.TrimRight(line, "\n")
			}
			if err != nil {
				close(r.lines)
				return
			}
		}
	}()

	srv := NewServer(opts)
	go func() {
		r.done <- srv.Run(context.Background(), &resilientTransport{in: inR, out: outW, log: r.log})
		_ = outW.Close()
	}()

	t.Cleanup(func() {
		if !r.ended {
			if err := r.endStream(); err != nil {
				t.Errorf("server ended with %v, want a clean end of stream", err)
			}
		}
		_ = inR.Close()
		_ = outR.Close()
	})
	return r
}

// endStream closes the client's side of the connection and returns what the
// server exited with. A client hanging up is the one condition that may end
// the session, and it must end it cleanly.
func (r *rig) endStream() error {
	r.t.Helper()
	r.ended = true
	_ = r.in.Close()
	select {
	case err := <-r.done:
		return err
	case <-time.After(rigTimeout):
		r.t.Fatal("server did not return after the stream ended")
		return nil
	}
}

func (r *rig) send(raw string) {
	r.t.Helper()
	if _, err := io.WriteString(r.in, raw+"\n"); err != nil {
		r.t.Fatalf("write %.60q: %v", raw, err)
	}
}

// reply reads the next line the server wrote, decoded as a generic JSON-RPC
// message.
func (r *rig) reply() map[string]any {
	r.t.Helper()
	select {
	case line, ok := <-r.lines:
		if !ok {
			r.t.Fatal("server closed the stream instead of answering")
		}
		var msg map[string]any
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			r.t.Fatalf("server wrote a non-JSON line %.120q: %v", line, err)
		}
		if msg["jsonrpc"] != "2.0" {
			r.t.Fatalf("server wrote a line that is not JSON-RPC 2.0: %.120q", line)
		}
		return msg
	case <-time.After(rigTimeout):
		r.t.Fatal("timed out waiting for a response — the server stopped serving")
		return nil
	}
}

// silence asserts the server answered nothing, for messages that carry no id
// to answer.
func (r *rig) silence() {
	r.t.Helper()
	select {
	case line, ok := <-r.lines:
		if !ok {
			r.t.Fatal("server closed the stream")
		}
		r.t.Fatalf("expected no answer, got %.120q", line)
	case <-time.After(150 * time.Millisecond):
	}
}

// handshake performs initialize + initialized, the state every later message
// is judged from.
func (r *rig) handshake() {
	r.t.Helper()
	r.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"rig","version":"0"}}}`)
	res := r.reply()
	if _, ok := res["result"]; !ok {
		r.t.Fatalf("initialize was not answered with a result: %v", res)
	}
	r.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
}

// askToolsList sends a well-formed request and returns the answer.
func (r *rig) askToolsList(id int) map[string]any {
	r.t.Helper()
	r.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/list","params":{}}`, id))
	res := r.reply()
	if got := res["id"]; got != float64(id) {
		r.t.Fatalf("answer carries id %v, want %d: %v", got, id, res)
	}
	return res
}

// assertServing fails unless res is a tools/list answer carrying the hactl
// tool — the assertion that separates "survived" from "died quietly".
func assertServing(t *testing.T, res map[string]any) {
	t.Helper()
	result, ok := res["result"].(map[string]any)
	if !ok {
		t.Fatalf("tools/list was not answered with a result: %v", res)
	}
	tools, ok := result["tools"].([]any)
	if !ok || len(tools) == 0 {
		t.Fatalf("tools/list answered without tools: %v", result)
	}
}

func rigOptions() Options {
	return Options{
		Runner: func(_ context.Context, _ []string, w io.Writer) error {
			_, _ = io.WriteString(w, "id state\nlight.x on\n")
			return nil
		},
		ResolvePath:    fakeResolver,
		NoManualInject: true,
	}
}

func errorOf(t *testing.T, msg map[string]any) map[string]any {
	t.Helper()
	e, ok := msg["error"].(map[string]any)
	if !ok {
		t.Fatalf("message carries no error object: %v", msg)
	}
	return e
}

// TestServeAnswersMissingVersionTagAndKeepsServing is the reproduction: a
// well-formed JSON request without the "jsonrpc" field. It used to end the
// process with exit 1 and the message "invalid message version tag".
func TestServeAnswersMissingVersionTagAndKeepsServing(t *testing.T) {
	r := newRig(t, rigOptions())
	r.handshake()

	r.send(`{"id":99,"method":"tools/list","params":{}}`)
	res := r.reply()
	if res["id"] != float64(99) {
		t.Errorf("error answer must carry the sender's id, got %v", res["id"])
	}
	if code := errorOf(t, res)["code"]; code != float64(-32600) {
		t.Errorf("error code = %v, want -32600 (invalid request)", code)
	}

	assertServing(t, r.askToolsList(2))
	if !strings.Contains(r.log.String(), "rejected a malformed") {
		t.Errorf("rejection must be visible on stderr, log = %q", r.log.String())
	}
}

// TestServeSurvivesUnanswerableGarbage covers every malformed shape that
// carries no id to answer: the session must drop it and keep serving.
func TestServeSurvivesUnanswerableGarbage(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"invalid json", `{"jsonrpc":"2.0","id":5,"method":`},
		{"not json at all", `hello world`},
		{"blank line", ``},
		{"json but not an object", `42`},
		{"batch array", `[{"jsonrpc":"2.0","id":6,"method":"tools/list"}]`},
		{"wrong version, no id", `{"jsonrpc":"1.0","method":"tools/list"}`},
		{"malformed response, not a request", `{"id":7,"result":{}}`},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, rigOptions())
			r.handshake()
			r.send(tc.line)
			r.silence()
			assertServing(t, r.askToolsList(100+i))
		})
	}
}

// TestServeSurvivesOversizedLine pins the bound: a line larger than the cap
// is discarded to its newline, so it costs one message rather than the
// process's memory, and the stream stays framed at the next line.
func TestServeSurvivesOversizedLine(t *testing.T) {
	r := newRig(t, rigOptions())
	r.handshake()

	huge := `{"jsonrpc":"2.0","id":8,"method":"tools/list","params":{"pad":"` +
		strings.Repeat("x", maxMessageBytes+1) + `"}}`
	r.send(huge)
	r.silence()
	assertServing(t, r.askToolsList(9))
	if !strings.Contains(r.log.String(), "larger than") {
		t.Errorf("oversized drop must be visible on stderr, log = %q", r.log.String())
	}
}

// TestServeAnswersUnknownMethod: an unknown method is answered by the SDK,
// and the session continues. Pinned here so a transport change cannot turn it
// into a fatal one without a red test.
func TestServeAnswersUnknownMethod(t *testing.T) {
	r := newRig(t, rigOptions())
	r.handshake()

	r.send(`{"jsonrpc":"2.0","id":11,"method":"no/such/method","params":{}}`)
	res := r.reply()
	if res["id"] != float64(11) {
		t.Errorf("answer carries id %v, want 11", res["id"])
	}
	if _, ok := res["error"]; !ok {
		t.Errorf("unknown method must be answered with an error: %v", res)
	}
	assertServing(t, r.askToolsList(12))
}

// TestServeSurvivesHandlerPanic covers the other direction: a panic inside a
// command took the process down with the session, because nothing in the SDK
// recovers. The caller is a model, so the answer is an error *result*.
func TestServeSurvivesHandlerPanic(t *testing.T) {
	opts := rigOptions()
	opts.Runner = func(_ context.Context, _ []string, _ io.Writer) error {
		panic("boom inside a command")
	}
	r := newRig(t, opts)
	r.handshake()

	r.send(`{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"hactl","arguments":{"command":"ent ls"}}}`)
	res := r.reply()
	result, ok := res["result"].(map[string]any)
	if !ok {
		t.Fatalf("panicking call was not answered with a result: %v", res)
	}
	if result["isError"] != true {
		t.Errorf("panic must be reported as an error result, got %v", result)
	}
	if body := fmt.Sprint(result["content"]); !strings.Contains(body, "panicked") {
		t.Errorf("error result should name the panic, got %v", body)
	}
	assertServing(t, r.askToolsList(14))
}

// TestServeEndsOnlyWithTheStream: the one condition that legitimately ends
// the session is the client hanging up, and it ends it cleanly — `hactl mcp`
// exits 0. Every other test here inherits the same assertion from newRig's
// cleanup; it is stated once on its own so the rule is not implicit.
func TestServeEndsOnlyWithTheStream(t *testing.T) {
	r := newRig(t, rigOptions())
	r.handshake()
	assertServing(t, r.askToolsList(15))
	if err := r.endStream(); err != nil {
		t.Errorf("a client hanging up must end the session cleanly, got %v", err)
	}
}

func TestProbeRequestRecoversTheSender(t *testing.T) {
	cases := []struct {
		name      string
		line      string
		wantID    any
		wantOK    bool
		wantValid bool
	}{
		{"numeric id", `{"id":99,"method":"tools/list"}`, int64(99), true, true},
		{"string id", `{"id":"abc","method":"tools/list"}`, "abc", true, true},
		{"notification", `{"method":"tools/list"}`, nil, true, false},
		{"null id", `{"id":null,"method":"tools/list"}`, nil, true, false},
		{"response, not a request", `{"id":3,"result":{}}`, nil, false, false},
		{"invalid json", `{"id":3,`, nil, false, false},
		{"batch", `[{"id":3,"method":"x"}]`, nil, false, false},
		{"unusable id type", `{"id":{"a":1},"method":"tools/list"}`, nil, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, _, ok := probeRequest([]byte(tc.line))
			if ok != tc.wantOK {
				t.Fatalf("isRequest = %v, want %v", ok, tc.wantOK)
			}
			if id.IsValid() != tc.wantValid {
				t.Fatalf("id valid = %v, want %v", id.IsValid(), tc.wantValid)
			}
			if tc.wantValid && id.Raw() != tc.wantID {
				t.Errorf("id = %v, want %v", id.Raw(), tc.wantID)
			}
		})
	}
}

func TestReadLineFramesAndBounds(t *testing.T) {
	const limit = 16
	input := "one\r\ntwo\n" + strings.Repeat("z", limit+5) + "\nthree\n"
	br := bufio.NewReaderSize(strings.NewReader(input), 8) // smaller than a line: forces reassembly

	type got struct {
		line     string
		oversize bool
	}
	var out []got
	for {
		line, oversize, err := readLine(br, limit)
		if len(line) > 0 || oversize {
			out = append(out, got{string(line), oversize})
		}
		if err != nil {
			break
		}
	}
	want := []got{{"one", false}, {"two", false}, {"", true}, {"three", false}}
	if len(out) != len(want) {
		t.Fatalf("readLine produced %v, want %v", out, want)
	}
	for i := range want {
		if out[i] != want[i] {
			t.Errorf("line %d = %v, want %v", i, out[i], want[i])
		}
	}
}
