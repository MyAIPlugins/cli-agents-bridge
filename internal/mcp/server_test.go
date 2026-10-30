package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const wait = 3 * time.Second

type logSink struct {
	mu    sync.Mutex
	lines []string
}

func (l *logSink) logf(format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, args...))
}

func (l *logSink) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

type testClient struct {
	t       *testing.T
	in      *io.PipeWriter
	out     *io.PipeReader
	resp    chan map[string]any
	serveCh chan error
	cancel  context.CancelFunc
	logs    *logSink
}

func testTools(release chan struct{}) []Tool {
	return []Tool{
		{Name: "echo", Description: "echoes message", Handler: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a struct {
				Message string `json:"message"`
			}
			if err := DecodeArgs(raw, &a); err != nil {
				return Result{}, err
			}
			return Result{Content: []Block{Text(a.Message)}}, nil
		}},
		{Name: "slow", Description: "waits", Handler: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			select {
			case <-release:
				return Result{Content: []Block{Text("released")}}, nil
			case <-ctx.Done():
				return Result{}, ctx.Err()
			}
		}},
		{Name: "boom", Description: "panics", Handler: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			panic("secret-sentinel-in-panic")
		}},
		{Name: "fail", Description: "refuses", Handler: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			return Result{}, errors.New("refused: for a reason")
		}},
		{Name: "big", Description: "returns n bytes", Handler: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			var a struct {
				N int `json:"n"`
			}
			if err := DecodeArgs(raw, &a); err != nil {
				return Result{}, err
			}
			return Result{Content: []Block{Text(strings.Repeat("é", a.N/2))}}, nil
		}},
		{Name: "who", Description: "client info", Handler: func(ctx context.Context, raw json.RawMessage) (Result, error) {
			c, ok := ClientFrom(ctx)
			return Result{Content: []Block{Text(fmt.Sprintf("%v|%s|%s", ok, c.Name, c.ProtocolVersion))}}, nil
		}},
	}
}

func newTestClient(t *testing.T, mutate func(*Config)) *testClient {
	t.Helper()
	logs := &logSink{}
	release := make(chan struct{})
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
	})
	cfg := Config{
		Name: "test-server", Version: "0.0.1", Tools: testTools(release),
		MaxLineBytes: 4096, MaxResultBytes: 2048, DrainTimeout: 300 * time.Millisecond, Logf: logs.logf,
	}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := New(cfg)
	require.NoError(t, err)

	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	c := &testClient{t: t, in: inW, out: outR, resp: make(chan map[string]any, 256), serveCh: make(chan error, 1), cancel: cancel, logs: logs}

	go func() {
		err := srv.Serve(ctx, inR, outW)
		_ = outW.Close()
		c.serveCh <- err
	}()
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 1<<20), 8<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				t.Errorf("server wrote a line that is not JSON: %q", sc.Text())
				continue
			}
			m["_rawlen"] = float64(len(sc.Bytes()))
			c.resp <- m
		}
		close(c.resp)
	}()
	t.Cleanup(func() { cancel(); _ = inW.Close() })
	return c
}

func (c *testClient) send(line string) {
	c.t.Helper()
	_, err := io.WriteString(c.in, line+"\n")
	require.NoError(c.t, err)
}

func (c *testClient) sendRaw(s string) {
	c.t.Helper()
	_, err := io.WriteString(c.in, s)
	require.NoError(c.t, err)
}

func (c *testClient) recv() map[string]any {
	c.t.Helper()
	select {
	case m, ok := <-c.resp:
		require.True(c.t, ok, "the server closed its output while a response was expected")
		return m
	case <-time.After(wait):
		c.t.Fatal("no response within the deadline")
		return nil
	}
}

func (c *testClient) expectSilence(d time.Duration) {
	c.t.Helper()
	select {
	case m, ok := <-c.resp:
		if ok {
			c.t.Fatalf("expected no response, got %v", m)
		}
	case <-time.After(d):
	}
}

func (c *testClient) init() {
	c.t.Helper()
	c.send(`{"jsonrpc":"2.0","id":0,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"test-client","version":"9"}}}`)
	r := c.recv()
	require.NotNil(c.t, r["result"], "initialize must succeed: %v", r)
	c.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
}

func errCode(m map[string]any) float64 {
	e, _ := m["error"].(map[string]any)
	code, _ := e["code"].(float64)
	return code
}

func resultText(t *testing.T, m map[string]any) (text string, isError bool) {
	t.Helper()
	res, ok := m["result"].(map[string]any)
	require.True(t, ok, "no result in %v", m)
	content, _ := res["content"].([]any)
	var parts []string
	for _, b := range content {
		parts = append(parts, b.(map[string]any)["text"].(string))
	}
	isErr, _ := res["isError"].(bool)
	return strings.Join(parts, "\n"), isErr
}

func TestInitialize_AnswersWithOurOwnProtocolAndServerInfo(t *testing.T) {
	c := newTestClient(t, nil)
	c.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2099-01-01","clientInfo":{"name":"future-client","version":"1"}}}`)
	r := c.recv()
	res := r["result"].(map[string]any)
	assert.Equal(t, ProtocolVersion, res["protocolVersion"], "never echo a revision this server was not tested against")
	assert.Equal(t, "test-server", res["serverInfo"].(map[string]any)["name"])
	caps := res["capabilities"].(map[string]any)
	assert.Contains(t, caps, "tools")
	assert.Equal(t, float64(1), r["id"])
}

func TestRequestsBeforeInitializeAreRefusedExceptPing(t *testing.T) {
	c := newTestClient(t, nil)
	c.send(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	assert.Equal(t, float64(codeInvalidRequest), errCode(c.recv()))
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"echo","arguments":{"message":"x"}}}`)
	assert.Equal(t, float64(codeInvalidRequest), errCode(c.recv()))
	c.send(`{"jsonrpc":"2.0","id":3,"method":"ping"}`)
	assert.NotNil(t, c.recv()["result"], "ping is allowed before initialize")
}

func TestInitializeTwiceIsRefused(t *testing.T) {
	c := newTestClient(t, nil)
	c.init()
	c.send(`{"jsonrpc":"2.0","id":5,"method":"initialize","params":{}}`)
	assert.Equal(t, float64(codeInvalidRequest), errCode(c.recv()))
}

func TestToolsListIsInRegistrationOrderWithASchema(t *testing.T) {
	c := newTestClient(t, nil)
	c.init()
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := c.recv()["result"].(map[string]any)["tools"].([]any)
	var names []string
	for _, tl := range tools {
		m := tl.(map[string]any)
		names = append(names, m["name"].(string))
		assert.NotNil(t, m["inputSchema"], "every tool advertises a schema, even an empty one")
	}
	assert.Equal(t, []string{"echo", "slow", "boom", "fail", "big", "who"}, names)
}

func TestToolsCall_HappyPathAndRefusals(t *testing.T) {
	c := newTestClient(t, nil)
	c.init()

	c.send(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"echo","arguments":{"message":"` + "`rm -rf $HOME` \\\"q\\\" \\n line2 ☃" + `"}}}`)
	text, isErr := resultText(t, c.recv())
	assert.False(t, isErr)
	assert.Equal(t, "`rm -rf $HOME` \"q\" \n line2 ☃", text, "content crosses the transport byte-identical")

	c.send(`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"nope"}}`)
	assert.Equal(t, float64(codeInvalidParams), errCode(c.recv()))

	c.send(`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"echo","arguments":{"message":"x","surprise":1}}}`)
	text, isErr = resultText(t, c.recv())
	assert.True(t, isErr, "an unknown argument is an error, not a field silently dropped")
	assert.Contains(t, text, "invalid arguments")

	c.send(`{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{}}`)
	assert.Equal(t, float64(codeInvalidParams), errCode(c.recv()))
}

func TestUnknownMethodsIncludingDiscoverAreMethodNotFound(t *testing.T) {
	c := newTestClient(t, nil)
	c.init()
	for i, m := range []string{"server/discover", "resources/list", "prompts/list", "logging/setLevel"} {
		c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":%q}`, 20+i, m))
		assert.Equal(t, float64(codeMethodNotFound), errCode(c.recv()), m)
	}
}

func TestDiscoverBeforeInitializeFallsBackToInitialize(t *testing.T) {
	c := newTestClient(t, nil)
	c.send(`{"jsonrpc":"2.0","id":1,"method":"server/discover"}`)
	first := errCode(c.recv())
	assert.Contains(t, []float64{float64(codeMethodNotFound), float64(codeInvalidRequest)}, first, "any error lets the client fall back")
	c.init() // the fallback the 2026-07-28 client performs
	c.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	assert.NotNil(t, c.recv()["result"])
}

func TestMalformedMessagesAreAnsweredAndTheConnectionSurvives(t *testing.T) {
	c := newTestClient(t, nil)
	c.init()
	c.send(`{not json`)
	r := c.recv()
	assert.Equal(t, float64(codeParseError), errCode(r))
	assert.Nil(t, r["id"], "a message that cannot be parsed has no id to answer to")

	c.send(`[{"jsonrpc":"2.0","id":1,"method":"ping"}]`)
	assert.Equal(t, float64(codeInvalidRequest), errCode(c.recv()), "batches are refused, not half-handled")

	c.send(`{"jsonrpc":"2.0","id":null,"method":"ping"}`)
	assert.Equal(t, float64(codeInvalidRequest), errCode(c.recv()), "a null id is not valid in MCP")

	c.send(`{"jsonrpc":"2.0","id":30,"method":"ping"}`)
	assert.NotNil(t, c.recv()["result"], "and the server is still there")
}

func TestToolErrorsAndPanicsBecomeResultsNotCrashes(t *testing.T) {
	c := newTestClient(t, nil)
	c.init()

	c.send(`{"jsonrpc":"2.0","id":40,"method":"tools/call","params":{"name":"fail"}}`)
	text, isErr := resultText(t, c.recv())
	assert.True(t, isErr)
	assert.Equal(t, "refused: for a reason", text)

	c.send(`{"jsonrpc":"2.0","id":41,"method":"tools/call","params":{"name":"boom"}}`)
	text, isErr = resultText(t, c.recv())
	assert.True(t, isErr)
	assert.NotContains(t, text, "secret-sentinel", "the panic value never reaches the model")
	assert.NotContains(t, c.logs.all(), "secret-sentinel", "nor the log: only the type is recorded")

	c.send(`{"jsonrpc":"2.0","id":42,"method":"ping"}`)
	assert.NotNil(t, c.recv()["result"], "the server survived the panic")
}

func TestFraming(t *testing.T) {
	t.Run("a message split across two writes", func(t *testing.T) {
		c := newTestClient(t, nil)
		c.sendRaw(`{"jsonrpc":"2.0","id":1,"met`)
		time.Sleep(20 * time.Millisecond)
		c.sendRaw(`hod":"ping"}` + "\n")
		assert.NotNil(t, c.recv()["result"])
	})
	t.Run("two messages in one write", func(t *testing.T) {
		c := newTestClient(t, nil)
		c.sendRaw(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n" + `{"jsonrpc":"2.0","id":2,"method":"ping"}` + "\n")
		a, b := c.recv(), c.recv()
		assert.ElementsMatch(t, []any{float64(1), float64(2)}, []any{a["id"], b["id"]})
	})
	t.Run("CRLF line endings", func(t *testing.T) {
		c := newTestClient(t, nil)
		c.sendRaw(`{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\r\n")
		assert.NotNil(t, c.recv()["result"])
	})
	t.Run("blank lines are ignored", func(t *testing.T) {
		c := newTestClient(t, nil)
		c.sendRaw("\n\n" + `{"jsonrpc":"2.0","id":1,"method":"ping"}` + "\n")
		assert.NotNil(t, c.recv()["result"])
	})
	t.Run("a last message with no trailing newline is still handled at EOF", func(t *testing.T) {
		c := newTestClient(t, nil)
		c.sendRaw(`{"jsonrpc":"2.0","id":1,"method":"ping"}`)
		require.NoError(t, c.in.Close())
		assert.NotNil(t, c.recv()["result"])
		select {
		case err := <-c.serveCh:
			assert.NoError(t, err, "EOF is a clean end")
		case <-time.After(wait):
			t.Fatal("Serve did not return at EOF")
		}
	})
	t.Run("an oversized line costs an error and not the connection", func(t *testing.T) {
		c := newTestClient(t, nil)
		c.send(`{"jsonrpc":"2.0","id":1,"method":"ping","params":"` + strings.Repeat("x", 10000) + `"}`)
		assert.Equal(t, float64(codeInvalidRequest), errCode(c.recv()))
		c.send(`{"jsonrpc":"2.0","id":2,"method":"ping"}`)
		r := c.recv()
		assert.Equal(t, float64(2), r["id"])
		assert.NotNil(t, r["result"], "the next message after the oversized one is handled")
	})
}

func TestACallThatWaitsDoesNotBlockTheOthers(t *testing.T) {
	c := newTestClient(t, nil)
	c.init()
	c.send(`{"jsonrpc":"2.0","id":50,"method":"tools/call","params":{"name":"slow"}}`)
	c.send(`{"jsonrpc":"2.0","id":51,"method":"ping"}`)
	c.send(`{"jsonrpc":"2.0","id":52,"method":"tools/call","params":{"name":"echo","arguments":{"message":"quick"}}}`)
	got := map[float64]bool{}
	for i := 0; i < 2; i++ {
		got[c.recv()["id"].(float64)] = true
	}
	assert.True(t, got[51] && got[52], "ping and echo answered while slow is still waiting: %v", got)
}

func TestWithdrawnRequestGetsNoResponseAndItsContextIsCancelled(t *testing.T) {
	c := newTestClient(t, nil)
	c.init()
	c.send(`{"jsonrpc":"2.0","id":60,"method":"tools/call","params":{"name":"slow"}}`)
	time.Sleep(50 * time.Millisecond)
	c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":60,"reason":"user stopped"}}`)
	c.expectSilence(300 * time.Millisecond)

	c.send(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":999}}`)
	c.send(`{"jsonrpc":"2.0","id":61,"method":"ping"}`)
	assert.Equal(t, float64(61), c.recv()["id"], "an unknown cancellation is ignored and nothing else changes")
}

func TestADuplicateInFlightIdIsRefused(t *testing.T) {
	c := newTestClient(t, nil)
	c.init()
	c.send(`{"jsonrpc":"2.0","id":70,"method":"tools/call","params":{"name":"slow"}}`)
	time.Sleep(50 * time.Millisecond)
	c.send(`{"jsonrpc":"2.0","id":70,"method":"tools/call","params":{"name":"echo","arguments":{"message":"x"}}}`)
	assert.Equal(t, float64(codeInvalidRequest), errCode(c.recv()))
}

func TestHandlerSeesWhoTheClientIs(t *testing.T) {
	c := newTestClient(t, nil)
	c.init()
	c.send(`{"jsonrpc":"2.0","id":80,"method":"tools/call","params":{"name":"who"}}`)
	text, _ := resultText(t, c.recv())
	assert.Equal(t, "true|test-client|2025-11-25", text)
}

func TestShutdown(t *testing.T) {
	t.Run("EOF with a call still running waits for the drain timeout and returns", func(t *testing.T) {
		c := newTestClient(t, nil)
		c.init()
		c.send(`{"jsonrpc":"2.0","id":90,"method":"tools/call","params":{"name":"slow"}}`)
		time.Sleep(50 * time.Millisecond)
		require.NoError(t, c.in.Close())
		select {
		case err := <-c.serveCh:
			assert.NoError(t, err)
		case <-time.After(wait):
			t.Fatal("Serve hung with a call in flight")
		}
	})
	t.Run("context cancellation stops Serve and cancels the calls", func(t *testing.T) {
		c := newTestClient(t, nil)
		c.init()
		c.send(`{"jsonrpc":"2.0","id":91,"method":"tools/call","params":{"name":"slow"}}`)
		time.Sleep(50 * time.Millisecond)
		c.cancel()
		select {
		case err := <-c.serveCh:
			assert.ErrorIs(t, err, context.Canceled)
		case <-time.After(wait):
			t.Fatal("Serve ignored the cancellation")
		}
	})
	t.Run("a client that went away ends Serve with an error", func(t *testing.T) {
		c := newTestClient(t, nil)
		c.init()
		require.NoError(t, c.out.Close()) // nobody reads the responses any more
		c.send(`{"jsonrpc":"2.0","id":92,"method":"ping"}`)
		select {
		case err := <-c.serveCh:
			assert.Error(t, err)
		case <-time.After(wait):
			t.Fatal("Serve kept running against a closed output")
		}
	})
}

func TestLogsCarryMetadataNeverContent(t *testing.T) {
	c := newTestClient(t, nil)
	c.init()
	c.send(`{"jsonrpc":"2.0","id":100,"method":"tools/call","params":{"name":"echo","arguments":{"message":"body-sentinel-1234"}}}`)
	text, _ := resultText(t, c.recv())
	require.Equal(t, "body-sentinel-1234", text)
	assert.NotContains(t, c.logs.all(), "body-sentinel", "a message body must never reach a log")
	assert.Contains(t, c.logs.all(), "tool echo", "but the call is recorded")
}

func TestNew_RefusesAConfigurationThatCannotBeHonoured(t *testing.T) {
	base := Config{Name: "n", MaxLineBytes: 10, MaxResultBytes: MinResultBytes, DrainTimeout: time.Second, Logf: func(string, ...any) {}}
	ok := func(c Config) { _, err := New(c); require.NoError(t, err) }
	bad := func(name string, mut func(*Config)) {
		c := base
		mut(&c)
		_, err := New(c)
		require.Error(t, err, name)
	}
	ok(base)
	bad("no name", func(c *Config) { c.Name = "" })
	bad("no line cap", func(c *Config) { c.MaxLineBytes = 0 })
	bad("a result budget below the floor", func(c *Config) { c.MaxResultBytes = MinResultBytes - 1 })
	bad("no drain timeout", func(c *Config) { c.DrainTimeout = 0 })
	bad("no logger", func(c *Config) { c.Logf = nil })
	bad("a tool without a handler", func(c *Config) { c.Tools = []Tool{{Name: "x"}} })
	bad("a nameless tool", func(c *Config) {
		c.Tools = []Tool{{Handler: func(context.Context, json.RawMessage) (Result, error) { return Result{}, nil }}}
	})
	bad("the same tool twice", func(c *Config) {
		h := func(context.Context, json.RawMessage) (Result, error) { return Result{}, nil }
		c.Tools = []Tool{{Name: "x", Handler: h}, {Name: "x", Handler: h}}
	})
}

func TestDecodeArgs(t *testing.T) {
	type a struct {
		N int `json:"n"`
	}
	for _, raw := range []string{"", "null", " {} "} {
		var v a
		require.NoError(t, DecodeArgs(json.RawMessage(raw), &v), "%q", raw)
	}
	var v a
	require.NoError(t, DecodeArgs(json.RawMessage(`{"n":3}`), &v))
	assert.Equal(t, 3, v.N)
	require.Error(t, DecodeArgs(json.RawMessage(`{"n":3,"x":1}`), &v), "unknown field")
	require.Error(t, DecodeArgs(json.RawMessage(`{"n":"three"}`), &v), "wrong type")
	require.Error(t, DecodeArgs(json.RawMessage(`{"n":3} {"n":4}`), &v), "trailing data")
}
