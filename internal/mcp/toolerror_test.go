package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const secretPath = "/Users/someone/.claude/cli-agents-bridge/sessions/28286c50/outbox"

func errorTools() []Tool {
	return []Tool{
		{Name: "fs", Description: "fails with a file-system error", Handler: func(context.Context, json.RawMessage) (Result, error) {
			_, err := os.ReadFile(filepath.Join(secretPath, "x"))
			return Result{}, fmt.Errorf("read the sessions directory: %w", err)
		}},
		{Name: "plain", Description: "fails with a plain error", Handler: func(context.Context, json.RawMessage) (Result, error) {
			return Result{}, fmt.Errorf("could not open %s", secretPath)
		}},
		{Name: "vouched", Description: "refuses in words written for the model", Handler: func(context.Context, json.RawMessage) (Result, error) {
			return Result{}, Refusef("project %q is not open", "demo")
		}},
		{Name: "wrapped", Description: "wraps a vouched error in a sentence with a path", Handler: func(context.Context, json.RawMessage) (Result, error) {
			return Result{}, fmt.Errorf("while reading %s: %w", secretPath, Refuse("the inbox could not be read in time"))
		}},
		{Name: "args", Description: "decodes strictly", Handler: func(_ context.Context, raw json.RawMessage) (Result, error) {
			var a struct {
				N int `json:"n"`
			}
			if err := DecodeArgs(raw, &a); err != nil {
				return Result{}, err
			}
			return Result{Content: []Block{Text("ok")}}, nil
		}},
	}
}

// The one place where the text of an error reaches the model: an error a handler
// wrote FOR the model. A file-system error, or any error wrapped around one, is
// replaced by a fixed text and its detail goes to the log, so a path on the machine
// never reaches the cloud through the way a tool failed.
func TestAnErrorNotWrittenForTheModelIsReplacedAndLogged(t *testing.T) {
	c := newTestClient(t, func(cfg *Config) { cfg.Tools = append(cfg.Tools, errorTools()...) })
	c.init()

	for i, name := range []string{"fs", "plain"} {
		c.send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q}}`, 60+i, name))
		text, isErr := resultText(t, c.recv())
		assert.True(t, isErr, name)
		assert.Equal(t, neutralToolError, text, name)
		assert.NotContains(t, text, "/Users/", "the path of the machine never reaches the model: %s", name)
		assert.NotContains(t, text, "28286c50", name)
	}
	assert.Contains(t, c.logs.all(), secretPath, "and the detail is where an operator looks for it")
}

func TestAnErrorWrittenForTheModelReachesItAsWritten(t *testing.T) {
	c := newTestClient(t, func(cfg *Config) { cfg.Tools = append(cfg.Tools, errorTools()...) })
	c.init()

	c.send(`{"jsonrpc":"2.0","id":70,"method":"tools/call","params":{"name":"vouched"}}`)
	text, isErr := resultText(t, c.recv())
	assert.True(t, isErr)
	assert.Equal(t, `project "demo" is not open`, text)

	// Wrapped inside a sentence of the handler's own, only the vouched text is shown:
	// the wrapper is the handler's words about the machine, not about the model's call.
	c.send(`{"jsonrpc":"2.0","id":71,"method":"tools/call","params":{"name":"wrapped"}}`)
	text, _ = resultText(t, c.recv())
	assert.Equal(t, "the inbox could not be read in time", text)
	assert.NotContains(t, text, "/Users/")

	// The strict decoder's complaint is about the caller's own arguments.
	c.send(`{"jsonrpc":"2.0","id":72,"method":"tools/call","params":{"name":"args","arguments":{"m":1}}}`)
	text, isErr = resultText(t, c.recv())
	assert.True(t, isErr)
	assert.Contains(t, text, "invalid arguments")
	assert.Contains(t, text, `unknown field "m"`)
}

func TestRefusefHasNoRoomForWrappingAnotherError(t *testing.T) {
	t.Parallel()
	inner := fmt.Errorf("open %s: no such file", secretPath)
	got := Refusef("could not read: %v", "fixed words").Error()
	assert.NotContains(t, got, secretPath)
	var te *ToolError
	require.ErrorAs(t, error(Refuse("x")), &te)
	assert.NotErrorIs(t, Refusef("wrapped: %v", inner), inner, "a ToolError carries text only, never the error it was made near")
}
