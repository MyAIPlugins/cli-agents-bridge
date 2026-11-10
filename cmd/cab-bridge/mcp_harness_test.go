//go:build !windows

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// The harness runs the REAL server (newServer, the production tool registry) over
// pipes, against a real data directory, with a fake clock the manager and the
// authorizer both read. A test speaks to it the way Claude Desktop does: initialize,
// then tools/call. Several apps can share one data dir, which is how two processes
// are modelled; the identity lock is a real flock, and it does conflict between them.

type syncLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *syncLog) add(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *syncLog) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type mcpEnv struct {
	t *testing.T
	// baseDir is CAB_DATA_DIR (what a child is handed and where config.json lives);
	// dataDir is the state directory under it, <baseDir>/layout-2, where the sessions
	// are. They are different directories and both are explicit: a harness that
	// derived one from the other by assignment would let a test pass on the wrong one.
	baseDir string
	dataDir string
	cfg     config.Config
	clock   *testClock
	logs    *syncLog
	home    string
	exe     string // the binary the sending verbs run as a child; empty until a test sets one
}

func newMCPEnv(t *testing.T) *mcpEnv {
	t.Helper()
	baseDir := t.TempDir()
	dataDir := filepath.Join(baseDir, config.LayoutDir)
	cfg := nextTestConfig(t, dataDir)
	cfg.BaseDir = baseDir
	cfg.MCPNextMaxWaitSeconds = 5
	cfg.MCPNextDefaultWaitSeconds = 2
	cfg.MCPScanBudgetSeconds = 2
	return &mcpEnv{t: t, baseDir: baseDir, dataDir: dataDir, cfg: cfg, clock: &testClock{t: time.Now().UTC().Truncate(time.Second)}, logs: &syncLog{}, home: t.TempDir()}
}

// manager is a manager over the env's data dir that reads the env's clock.
func (e *mcpEnv) manager() *session.Manager {
	mgr := newSessionManager(e.cfg)
	mgr.Now = e.clock.now
	return mgr
}

// val plants a val and, when ttl is positive, opens its bridge.
func (e *mcpEnv) val(id, name, scope string, ttl time.Duration) {
	e.t.Helper()
	plantOverviewSession(e.t, e.dataDir, id, session.RoleVal, name, scope, "", session.StateOrchestrating)
	if ttl > 0 {
		_, err := e.manager().OpenDesktopGrant(id, ttl)
		require.NoError(e.t, err)
	}
}

// start builds an app and a connected client that calls itself clientName.
func (e *mcpEnv) start(clientName string) (*mcpApp, *mcpClient) {
	e.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	app := newMCPApp(ctx, e.cfg, e.manager(), e.exe, e.home, e.logs.add)
	srv, err := app.newServer()
	require.NoError(e.t, err)

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ctx, inR, outW); _ = outW.Close() }()

	c := &mcpClient{t: e.t, stdin: inW, pending: map[int]chan rpcReply{}}
	go c.readLoop(outR)
	e.t.Cleanup(func() {
		_ = inW.Close()
		select {
		case <-served:
		case <-time.After(10 * time.Second):
			e.t.Errorf("the server did not stop after its input ended")
		}
		app.close()
		cancel()
	})

	c.initialize(clientName)
	return app, c
}

type rpcReply struct {
	Result json.RawMessage
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	Raw string
}

type mcpClient struct {
	t     *testing.T
	stdin io.WriteCloser

	mu      sync.Mutex
	nextID  int
	pending map[int]chan rpcReply
	wire    []string // every line the server wrote, as it was written
}

func (c *mcpClient) readLoop(r io.Reader) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	for sc.Scan() {
		line := sc.Text()
		var env struct {
			ID     *int            `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Code    int    `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		c.mu.Lock()
		c.wire = append(c.wire, line)
		c.mu.Unlock()
		if err := json.Unmarshal([]byte(line), &env); err != nil || env.ID == nil {
			continue
		}
		c.mu.Lock()
		ch := c.pending[*env.ID]
		delete(c.pending, *env.ID)
		c.mu.Unlock()
		if ch != nil {
			ch <- rpcReply{Result: env.Result, Error: env.Error, Raw: line}
		}
	}
}

func (c *mcpClient) send(method string, params any) chan rpcReply {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	ch := make(chan rpcReply, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	require.NoError(c.t, err)
	_, err = c.stdin.Write(append(line, '\n'))
	require.NoError(c.t, err)
	return ch
}

func (c *mcpClient) initialize(name string) {
	c.t.Helper()
	reply := <-c.send("initialize", map[string]any{
		"protocolVersion": "2025-11-25",
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": name, "version": "0.0.0"},
	})
	require.Nil(c.t, reply.Error, reply.Raw)
	line, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	require.NoError(c.t, err)
	_, err = c.stdin.Write(append(line, '\n'))
	require.NoError(c.t, err)
}

// toolResult is what a tool call returned, flattened for assertions.
type toolResult struct {
	Blocks  []string
	IsError bool
	Raw     string
}

func (r toolResult) Text() string { return strings.Join(r.Blocks, "\n") }

func (c *mcpClient) callAsync(tool string, args map[string]any) func() toolResult {
	if args == nil {
		args = map[string]any{}
	}
	ch := c.send("tools/call", map[string]any{"name": tool, "arguments": args})
	return func() toolResult {
		select {
		case reply := <-ch:
			require.Nil(c.t, reply.Error, reply.Raw)
			var res struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			}
			require.NoError(c.t, json.Unmarshal(reply.Result, &res), reply.Raw)
			out := toolResult{IsError: res.IsError, Raw: reply.Raw}
			for _, b := range res.Content {
				out.Blocks = append(out.Blocks, b.Text)
			}
			return out
		case <-time.After(30 * time.Second):
			c.t.Fatalf("tool %s did not answer within 30s", tool)
			return toolResult{}
		}
	}
}

func (c *mcpClient) call(tool string, args map[string]any) toolResult {
	c.t.Helper()
	return c.callAsync(tool, args)()
}

// strayLines are the lines the server wrote that are not JSON-RPC 2.0 messages. The
// protocol owns stdout: one such line breaks the framing for the client.
func (c *mcpClient) strayLines() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var stray []string
	for _, line := range c.wire {
		var env struct {
			JSONRPC string `json:"jsonrpc"`
		}
		if err := json.Unmarshal([]byte(line), &env); err != nil || env.JSONRPC != "2.0" {
			stray = append(stray, line)
		}
	}
	return stray
}
