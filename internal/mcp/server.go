package mcp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// Config builds a Server. Nothing here has a silent default: a zero limit is an
// error at construction, because a server that quietly picks its own cap is a
// server whose behaviour nobody wrote down.
type Config struct {
	Name    string
	Version string
	Tools   []Tool

	// MaxLineBytes caps ONE inbound message. A longer line is answered with an
	// error and skipped; the connection survives.
	MaxLineBytes int
	// MaxResultBytes caps the serialized response line of EVERY tools/call, the
	// whole envelope included (see fitResponse).
	MaxResultBytes int
	// DrainTimeout is how long a shutdown waits for calls still running.
	DrainTimeout time.Duration
	// Logf receives metadata only: method, id, duration, outcome. Never params and
	// never a result: they carry message bodies that must not reach a log.
	Logf func(format string, args ...any)
}

// Server is one MCP session over a single stream pair.
type Server struct {
	cfg   Config
	tools map[string]Tool

	outMu sync.Mutex
	out   io.Writer

	mu          sync.Mutex // guards the fields below
	initialized bool
	client      ClientInfo
	inflight    map[string]*call
}

type call struct {
	cancel    context.CancelFunc
	withdrawn bool // the client cancelled it: no response is owed
}

// New validates cfg and builds the server.
func New(cfg Config) (*Server, error) {
	switch {
	case cfg.Name == "":
		return nil, errors.New("mcp: Name is required")
	case cfg.MaxLineBytes <= 0:
		return nil, errors.New("mcp: MaxLineBytes must be positive")
	case cfg.MaxResultBytes < minResultBytes:
		return nil, fmt.Errorf("mcp: MaxResultBytes must be at least %d", minResultBytes)
	case cfg.DrainTimeout <= 0:
		return nil, errors.New("mcp: DrainTimeout must be positive")
	case cfg.Logf == nil:
		return nil, errors.New("mcp: Logf is required (use a no-op function to silence it)")
	}
	tools := make(map[string]Tool, len(cfg.Tools))
	for _, t := range cfg.Tools {
		switch {
		case t.Name == "":
			return nil, errors.New("mcp: a tool has no name")
		case t.Handler == nil:
			return nil, fmt.Errorf("mcp: tool %q has no handler", t.Name)
		}
		if _, dup := tools[t.Name]; dup {
			return nil, fmt.Errorf("mcp: tool %q registered twice", t.Name)
		}
		tools[t.Name] = t
	}
	return &Server{cfg: cfg, tools: tools, inflight: map[string]*call{}}, nil
}

type clientKey struct{}

// ClientFrom returns what the client said about itself in `initialize`. It is
// available to a handler through its context.
func ClientFrom(ctx context.Context) (ClientInfo, bool) {
	c, ok := ctx.Value(clientKey{}).(ClientInfo)
	return c, ok
}

type lineEvent struct {
	data    []byte
	tooLong bool
}

// Serve reads messages from in and writes responses to out until in reaches EOF
// (a clean end: it returns nil), ctx is cancelled, or a write fails (the client
// is gone). It never returns while a handler is still running past DrainTimeout.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	s.out = out
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	lines := make(chan lineEvent)
	readErr := make(chan error, 1)
	go s.readLines(runCtx, in, lines, readErr)

	var wg sync.WaitGroup
	writeFailed := make(chan error, 1)
	for {
		select {
		case <-runCtx.Done():
			s.drain(cancel, &wg)
			return ctx.Err()
		case werr := <-writeFailed:
			s.drain(cancel, &wg)
			return fmt.Errorf("mcp: the client is gone: %w", werr)
		case ev, ok := <-lines:
			if !ok {
				s.drain(cancel, &wg)
				return <-readErr
			}
			if err := s.handleEvent(runCtx, ev, &wg, writeFailed); err != nil {
				s.drain(cancel, &wg)
				return err
			}
		}
	}
}

// readLines splits the stream into lines of at most MaxLineBytes. A longer line is
// reported as tooLong and discarded up to its newline, so one oversized message
// costs an error response and not the connection.
func (s *Server) readLines(ctx context.Context, in io.Reader, lines chan<- lineEvent, readErr chan<- error) {
	defer close(lines)
	br := bufio.NewReaderSize(in, 64*1024)
	var buf []byte
	tooLong := false
	for {
		chunk, err := br.ReadSlice('\n')
		if len(chunk) > 0 && !tooLong {
			if len(buf)+len(chunk) > s.cfg.MaxLineBytes {
				tooLong = true
				buf = nil
			} else {
				buf = append(buf, chunk...)
			}
		}
		switch {
		case err == nil:
			// a complete line: deliver it
			if !deliver(ctx, lines, lineEvent{data: buf, tooLong: tooLong}) {
				return
			}
			buf, tooLong = nil, false
		case errors.Is(err, bufio.ErrBufferFull):
			// the line is longer than the buffer: keep accumulating
		case errors.Is(err, io.EOF):
			if len(buf) > 0 || tooLong {
				if !deliver(ctx, lines, lineEvent{data: buf, tooLong: tooLong}) {
					return
				}
			}
			readErr <- nil
			return
		default:
			readErr <- fmt.Errorf("mcp: read: %w", err)
			return
		}
	}
}

// deliver hands a line to the dispatcher, or gives up if the server is stopping:
// a blocked send must not keep the reader alive after Serve has returned.
func deliver(ctx context.Context, lines chan<- lineEvent, ev lineEvent) bool {
	select {
	case lines <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *Server) handleEvent(ctx context.Context, ev lineEvent, wg *sync.WaitGroup, writeFailed chan<- error) error {
	if ev.tooLong {
		return s.sendError(nullID, codeInvalidRequest, fmt.Sprintf("message longer than %d bytes was skipped", s.cfg.MaxLineBytes))
	}
	data := bytes.TrimSpace(ev.data)
	if len(data) == 0 {
		return nil
	}
	if data[0] == '[' {
		return s.sendError(nullID, codeInvalidRequest, "batches are not supported")
	}
	var msg rawMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return s.sendError(nullID, codeParseError, "parse error: "+err.Error())
	}
	if msg.Method == "" {
		// A response to a request this server never sent. Nothing to do with it.
		s.cfg.Logf("mcp: ignored a message without a method")
		return nil
	}
	if !msg.hasID() {
		if len(msg.ID) > 0 {
			return s.sendError(nullID, codeInvalidRequest, "a null request id is not valid")
		}
		s.handleNotification(msg)
		return nil
	}
	return s.handleRequest(ctx, msg, wg, writeFailed)
}

func (s *Server) handleNotification(msg rawMessage) {
	switch msg.Method {
	case "notifications/initialized":
		s.cfg.Logf("mcp: client initialized")
	case "notifications/cancelled":
		s.withdraw(msg.Params)
	default:
		s.cfg.Logf("mcp: ignored notification %s", msg.Method)
	}
}

func (s *Server) withdraw(params json.RawMessage) {
	var p struct {
		RequestID json.RawMessage `json:"requestId"`
	}
	if err := json.Unmarshal(params, &p); err != nil || len(p.RequestID) == 0 {
		s.cfg.Logf("mcp: ignored a malformed cancellation")
		return
	}
	s.mu.Lock()
	c, ok := s.inflight[string(bytes.TrimSpace(p.RequestID))]
	if ok {
		c.withdrawn = true
	}
	s.mu.Unlock()
	if ok {
		c.cancel()
		s.cfg.Logf("mcp: request %s withdrawn by the client", string(p.RequestID))
		return
	}
	s.cfg.Logf("mcp: cancellation of an unknown or finished request, ignored")
}

func (s *Server) handleRequest(ctx context.Context, msg rawMessage, wg *sync.WaitGroup, writeFailed chan<- error) error {
	s.mu.Lock()
	ready := s.initialized
	s.mu.Unlock()
	if !ready && msg.Method != "initialize" && msg.Method != "ping" {
		return s.sendError(msg.ID, codeInvalidRequest, "server not initialized: send `initialize` first")
	}
	switch msg.Method {
	case "initialize":
		return s.initialize(msg)
	case "ping":
		return s.sendResult(msg.ID, map[string]any{})
	case "tools/list":
		return s.sendResult(msg.ID, map[string]any{"tools": s.toolList()})
	case "tools/call":
		return s.startCall(ctx, msg, wg, writeFailed)
	default:
		// Includes `server/discover` of the 2026-07-28 revision: the answer that
		// lets a newer client fall back to `initialize`.
		return s.sendError(msg.ID, codeMethodNotFound, "method not found: "+msg.Method)
	}
}

func (s *Server) initialize(msg rawMessage) error {
	var p struct {
		ProtocolVersion string `json:"protocolVersion"`
		ClientInfo      struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"clientInfo"`
	}
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			return s.sendError(msg.ID, codeInvalidParams, "invalid initialize params: "+err.Error())
		}
	}
	s.mu.Lock()
	if s.initialized {
		s.mu.Unlock()
		return s.sendError(msg.ID, codeInvalidRequest, "already initialized")
	}
	s.initialized = true
	s.client = ClientInfo{Name: p.ClientInfo.Name, Version: p.ClientInfo.Version, ProtocolVersion: p.ProtocolVersion}
	s.mu.Unlock()

	s.cfg.Logf("mcp: initialize from %q %q, client protocol %q, answering %s", p.ClientInfo.Name, p.ClientInfo.Version, p.ProtocolVersion, ProtocolVersion)
	return s.sendResult(msg.ID, map[string]any{
		// Always our own revision: the spec leaves it to the client to disconnect
		// if it cannot use it. Echoing the client's would claim a protocol this
		// server was never tested against.
		"protocolVersion": ProtocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
		"serverInfo":      map[string]any{"name": s.cfg.Name, "version": s.cfg.Version},
	})
}

func (s *Server) toolList() []map[string]any {
	out := make([]map[string]any, 0, len(s.cfg.Tools))
	for _, t := range s.cfg.Tools { // registration order: deterministic, cacheable
		schema := t.InputSchema
		if schema == nil {
			schema = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		out = append(out, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema})
	}
	return out
}

func (s *Server) startCall(ctx context.Context, msg rawMessage, wg *sync.WaitGroup, writeFailed chan<- error) error {
	var p struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(msg.Params, &p); err != nil || p.Name == "" {
		return s.sendError(msg.ID, codeInvalidParams, "invalid tools/call params: a tool name is required")
	}
	tool, ok := s.tools[p.Name]
	if !ok {
		return s.sendError(msg.ID, codeInvalidParams, "unknown tool: "+p.Name)
	}

	key := string(bytes.TrimSpace(msg.ID))
	callCtx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if _, busy := s.inflight[key]; busy {
		s.mu.Unlock()
		cancel()
		return s.sendError(msg.ID, codeInvalidRequest, "a request with this id is already in flight")
	}
	c := &call{cancel: cancel}
	s.inflight[key] = c
	client := s.client
	s.mu.Unlock()

	callCtx = context.WithValue(callCtx, clientKey{}, client)
	wg.Add(1)
	go func() {
		defer wg.Done()
		s.runCall(ctx, callCtx, msg.ID, key, tool, p.Arguments, c, writeFailed)
	}()
	return nil
}

func (s *Server) runCall(parent, ctx context.Context, id json.RawMessage, key string, tool Tool, args json.RawMessage, c *call, writeFailed chan<- error) {
	start := time.Now()
	defer c.cancel() // release the call's context whatever happens below
	res := s.invoke(ctx, tool, args)

	s.mu.Lock()
	withdrawn := c.withdrawn
	delete(s.inflight, key)
	s.mu.Unlock()

	// No response is owed to a request the client withdrew, and none can be
	// delivered once the server itself is stopping.
	if withdrawn || parent.Err() != nil {
		s.cfg.Logf("mcp: tool %s id=%s finished after withdrawal or shutdown, no response sent (%s)", tool.Name, key, time.Since(start).Round(time.Millisecond))
		return
	}
	line, cut, err := fitResponse(id, res, s.cfg.MaxResultBytes)
	if err != nil {
		s.cfg.Logf("mcp: tool %s id=%s: %v", tool.Name, key, err)
		line, err = marshalErrorLine(id, codeInternal, "internal error")
		if err != nil {
			s.cfg.Logf("mcp: tool %s id=%s: cannot even encode an error: %v", tool.Name, key, err)
			return
		}
	}
	s.cfg.Logf("mcp: tool %s id=%s isError=%v cut=%v %s", tool.Name, key, res.IsError, cut, time.Since(start).Round(time.Millisecond))
	if werr := s.writeLine(line); werr != nil {
		select {
		case writeFailed <- werr:
		default:
		}
	}
}

// invoke runs the handler and turns every outcome into a Result. A panic does not
// reach the process: it becomes a generic error, and the panic value, which may
// hold arguments, is logged as a type and never as text.
func (s *Server) invoke(ctx context.Context, tool Tool, args json.RawMessage) (res Result) {
	defer func() {
		if r := recover(); r != nil {
			s.cfg.Logf("mcp: tool %s panicked (%T)", tool.Name, r)
			res = ErrorResult("internal error: the tool failed unexpectedly")
		}
	}()
	out, err := tool.Handler(ctx, args)
	if err != nil {
		return ErrorResult(err.Error())
	}
	return out
}

func (s *Server) drain(cancel context.CancelFunc, wg *sync.WaitGroup) {
	cancel()
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(s.cfg.DrainTimeout):
		s.cfg.Logf("mcp: %s passed with handlers still running, giving up on them", s.cfg.DrainTimeout)
	}
}

func (s *Server) sendResult(id json.RawMessage, result any) error {
	line, err := json.Marshal(response{JSONRPC: jsonRPCVersion, ID: id, Result: result})
	if err != nil {
		return fmt.Errorf("mcp: marshal response: %w", err)
	}
	return s.writeLine(line)
}

func (s *Server) sendError(id json.RawMessage, code int, msg string) error {
	line, err := marshalErrorLine(id, code, msg)
	if err != nil {
		return err
	}
	return s.writeLine(line)
}

func marshalErrorLine(id json.RawMessage, code int, msg string) ([]byte, error) {
	line, err := json.Marshal(response{JSONRPC: jsonRPCVersion, ID: id, Error: &rpcError{Code: code, Message: msg}})
	if err != nil {
		return nil, fmt.Errorf("mcp: marshal error response: %w", err)
	}
	return line, nil
}

// writeLine writes one message and its newline atomically with respect to other
// writers. A compact JSON document has no raw newline in it, so the framing holds.
func (s *Server) writeLine(line []byte) error {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if _, err := s.out.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("mcp: write: %w", err)
	}
	return nil
}
