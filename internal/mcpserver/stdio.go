package mcpserver

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// maxMessageBytes caps a single inbound line. A line longer than this is
// discarded up to its newline and the stream stays framed, so a client that
// forgets a newline (or sends a hostile blob) costs one message, not the
// process and not the machine's memory.
const maxMessageBytes = 4 << 20

// readBufferSize is the initial read buffer; longer lines are assembled by
// [readLine] up to maxMessageBytes.
const readBufferSize = 64 << 10

// resilientTransport is the stdio transport hactl serves MCP on. It exists
// because the SDK's own [mcp.StdioTransport] treats every malformed inbound
// message as the end of the session: its reader returns the decode error, the
// jsonrpc2 read loop breaks on any read error, the connection unwinds, and
// `hactl mcp` exits 1. A single line without a "jsonrpc" tag — or one line of
// invalid JSON from a client that hiccuped — therefore killed a whole agent
// session, and every later write hit a dead pipe.
//
// The rule this type implements: a malformed *message* is a message-level
// fault. It is answered (JSON-RPC error response) when the sender can be
// identified, dropped otherwise, and the session keeps serving either way.
// Only the stream itself failing — EOF, or a read error on the pipe — ends
// the session, which is the one condition that genuinely cannot be served.
type resilientTransport struct {
	in  io.Reader
	out io.Writer
	// log receives one line per rejected message; nil means os.Stderr.
	// Never stdout: that belongs to the protocol.
	log io.Writer
}

// Connect implements [mcp.Transport].
func (t *resilientTransport) Connect(context.Context) (mcp.Connection, error) {
	c := &resilientConn{
		out:      t.out,
		log:      t.log,
		incoming: make(chan jsonrpc.Message),
		closed:   make(chan struct{}),
	}
	go c.readLoop(t.in)
	return c, nil
}

// resilientConn is the [mcp.Connection] returned by [resilientTransport].
type resilientConn struct {
	out io.Writer
	log io.Writer

	writeMu sync.Mutex

	// incoming carries decoded messages from readLoop; it is closed when the
	// stream ends. readErr is written before that close and read only after
	// observing it, so the handoff needs no lock.
	incoming chan jsonrpc.Message
	readErr  error

	closeOnce sync.Once
	closed    chan struct{}
}

// Read implements [mcp.Connection]. It only ever returns an error the session
// cannot survive: a cancelled context, or the end of the stream.
func (c *resilientConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	// As a matter of principle, reads on a closed context return an error even
	// when a message is already queued (mirrors the SDK's own transports).
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.closed:
		return nil, io.EOF
	case msg, ok := <-c.incoming:
		if !ok {
			if c.readErr != nil && !errors.Is(c.readErr, io.EOF) {
				return nil, c.readErr
			}
			return nil, io.EOF
		}
		return msg, nil
	}
}

// Write implements [mcp.Connection], emitting one newline-delimited JSON
// message. json.Marshal escapes newlines inside strings, so an encoded
// message never contains the delimiter.
func (c *resilientConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	data, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return err
	}
	return c.writeLine(data)
}

// Close implements [mcp.Connection]. It unblocks a Read waiting for input.
// The underlying streams are not closed: they are the process's stdin and
// stdout, owned by whoever started it.
func (c *resilientConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}

// SessionID implements [mcp.Connection]; stdio has exactly one session.
func (c *resilientConn) SessionID() string { return "" }

func (c *resilientConn) writeLine(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_, err := c.out.Write(append(data, '\n'))
	return err
}

func (c *resilientConn) logf(format string, args ...any) {
	w := c.log
	if w == nil {
		w = os.Stderr
	}
	_, _ = fmt.Fprintf(w, "hactl mcp: "+format+"\n", args...)
}

// readLoop frames stdin into lines and decodes each one on its own. A line
// that does not decode never reaches the session; it is rejected here and the
// loop continues with the next line.
func (c *resilientConn) readLoop(r io.Reader) {
	defer close(c.incoming)

	br := bufio.NewReaderSize(r, readBufferSize)
	for {
		line, oversize, err := readLine(br, maxMessageBytes)
		switch {
		case oversize:
			// No id can be trusted from a truncated line, so there is
			// nobody to answer; say so on stderr and stay framed.
			c.logf("dropped a message larger than %d bytes", maxMessageBytes)
		case len(bytes.TrimSpace(line)) == 0:
			// Blank line between messages: not a message at all.
		default:
			msg, decErr := jsonrpc.DecodeMessage(line)
			if decErr != nil {
				c.rejectMalformed(line, decErr)
				break
			}
			select {
			case c.incoming <- msg:
			case <-c.closed:
				return
			}
		}
		if err != nil {
			c.readErr = err
			return
		}
	}
}

// rejectMalformed answers a message that did not decode. JSON-RPC requires an
// error response to carry the id it answers; when no id can be recovered
// (invalid JSON, a notification, a batch) the message is dropped, because a
// response with a null id is itself a message some clients cannot decode —
// and killing the client's read loop is the defect being fixed here, not a
// behaviour worth propagating.
func (c *resilientConn) rejectMalformed(line []byte, cause error) {
	id, method, isRequest := probeRequest(line)
	if !isRequest || !id.IsValid() {
		c.logf("dropped a malformed message: %v", cause)
		return
	}
	resp := &jsonrpc.Response{ID: id, Error: &jsonrpc.Error{
		Code:    jsonrpc.CodeInvalidRequest,
		Message: fmt.Sprintf("invalid JSON-RPC message: %v", cause),
	}}
	data, err := jsonrpc.EncodeMessage(resp)
	if err != nil {
		c.logf("dropped a malformed message (%v); encoding the error response failed too: %v", cause, err)
		return
	}
	if werr := c.writeLine(data); werr != nil {
		c.logf("rejecting a malformed message failed to write: %v", werr)
		return
	}
	c.logf("rejected a malformed %q request: %v", method, cause)
}

// probeRequest re-reads a line that failed to decode, looking for the two
// fields that decide whether it can be answered: a method (only a request is
// answered — a malformed *response* with an id must not be replied to, that
// would be a response to a response) and an id.
func probeRequest(line []byte) (jsonrpc.ID, string, bool) {
	var probe struct {
		ID     json.RawMessage `json:"id"`
		Method *string         `json:"method"`
	}
	if err := json.Unmarshal(line, &probe); err != nil || probe.Method == nil {
		return jsonrpc.ID{}, "", false
	}
	if len(probe.ID) == 0 {
		return jsonrpc.ID{}, *probe.Method, true // a notification: nothing to answer
	}
	var raw any
	if err := json.Unmarshal(probe.ID, &raw); err != nil {
		return jsonrpc.ID{}, *probe.Method, true
	}
	id, err := jsonrpc.MakeID(raw)
	if err != nil {
		return jsonrpc.ID{}, *probe.Method, true
	}
	return id, *probe.Method, true
}

// readLine returns the next newline-delimited line without its line ending.
// A line exceeding limit is consumed to its end and reported as oversize rather
// than buffered, so an unterminated stream cannot grow the process without
// bound and the next line is still read at the right offset.
func readLine(r *bufio.Reader, limit int) ([]byte, bool, error) {
	var (
		buf      []byte
		oversize bool
	)
	for {
		chunk, isPrefix, err := r.ReadLine()
		if !oversize && len(chunk) > 0 {
			if len(buf)+len(chunk) > limit {
				oversize, buf = true, nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		if err != nil {
			return buf, oversize, err
		}
		if !isPrefix {
			return buf, oversize, nil
		}
	}
}
