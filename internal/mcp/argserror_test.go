package mcp

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// What the model reads when its arguments are wrong is a sentence about its own input.
// The decoder's own text names the server's Go types and struct fields, and an error's
// text is the one thing a ToolError must not carry.

type decodeTarget struct {
	N   int      `json:"n"`
	S   string   `json:"s"`
	IDs []string `json:"ids"`
	O   struct {
		B bool `json:"b"`
	} `json:"o"`
}

func decodeText(t *testing.T, raw string) string {
	t.Helper()
	var v decodeTarget
	err := DecodeArgs(json.RawMessage(raw), &v)
	require.Error(t, err, raw)
	var te *ToolError
	require.ErrorAs(t, err, &te, "it is a sentence written for the model: %s", raw)
	return err.Error()
}

func TestDecodeArgs_NamesTheKeyAndTheKindsNotTheServersTypes(t *testing.T) {
	for raw, want := range map[string]string{
		`{"n":"three"}`:    `invalid arguments: "n" must be an integer, got a string`,
		`{"s":5}`:          `invalid arguments: "s" must be a string, got a number`,
		`{"ids":"x"}`:      `invalid arguments: "ids" must be an array, got a string`,
		`{"ids":[1]}`:      `invalid arguments: "ids.0" must be a string, got a number`,
		`{"o":{"b":"y"}}`:  `invalid arguments: "o.b" must be a boolean, got a string`,
		`{"o":5}`:          `invalid arguments: "o" must be an object, got a number`,
		`{"n":1.5}`:        `invalid arguments: "n" must be an integer, got a number that does not fit`,
		`{"n":1e999}`:      `invalid arguments: "n" must be an integer, got a number that does not fit`,
		`[1]`:              `invalid arguments: the arguments must be a JSON object, not an array`,
		`"text"`:           `invalid arguments: the arguments must be a JSON object, not a string`,
		`{"zz":1}`:         `invalid arguments: unknown field "zz"`,
		`{"a\"b\nc":true}`: `invalid arguments: unknown field "a\"b\nc"`,
	} {
		got := decodeText(t, raw)
		assert.Equal(t, want, got, raw)
		for _, vocabulary := range []string{"Go struct field", "Go value", "json:", "decodeTarget", "struct", "main."} {
			assert.NotContains(t, got, vocabulary, "the server's vocabulary stays on the machine: %s", raw)
		}
	}
}

func TestDecodeArgs_AnErrorItDoesNotRecogniseStaysOnTheMachine(t *testing.T) {
	// A tool decoding into something it cannot, or a truncated line: not the caller's
	// doing, so no text of it may be written for the model. It is an ordinary error,
	// which the server logs and answers with the neutral text.
	var v decodeTarget
	for name, err := range map[string]error{
		"non-pointer target": DecodeArgs(json.RawMessage(`{}`), v),
		"truncated":          DecodeArgs(json.RawMessage(`{"n":`), &v),
	} {
		require.Error(t, err, name)
		var te *ToolError
		assert.False(t, errors.As(err, &te), "%s: %v", name, err)
	}
}
