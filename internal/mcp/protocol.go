// Package mcp is a minimal Model Context Protocol server over newline-delimited
// JSON-RPC on stdio (F-72), built on the standard library only.
//
// It knows nothing about the bridge: framing, dispatch, the tool registry and the
// size budget of every result live here, and the tools themselves are supplied by
// the caller. That split is deliberate, so the protocol can be tested over an
// io.Pipe without any session on disk.
//
// Protocol version: 2025-11-25, the one Claude Desktop 4.4212.0 was measured
// speaking. The 2026-07-28 revision removes `initialize`; a client that starts
// with `server/discover` gets an ordinary method-not-found, which is the
// documented way for a client to fall back to `initialize`.
package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
)

// ProtocolVersion is the only revision this server speaks.
const ProtocolVersion = "2025-11-25"

const jsonRPCVersion = "2.0"

// JSON-RPC 2.0 error codes used by the server.
const (
	codeParseError     = -32700
	codeInvalidRequest = -32600
	codeMethodNotFound = -32601
	codeInvalidParams  = -32602
	codeInternal       = -32603
)

// Block is one piece of a tool result. Only text exists: nothing the bridge
// returns is an image, a resource or a link.
type Block struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// Text builds a text block.
func Text(s string) Block { return Block{Type: "text", Text: s} }

// Result is what a tool returns. IsError marks a failure of the tool itself (bad
// arguments, a refused action): the model reads it and can react, which a
// JSON-RPC error does not allow.
type Result struct {
	Content []Block `json:"content"`
	IsError bool    `json:"isError"`
}

// ErrorResult is the failure shape for a tool that refuses.
func ErrorResult(msg string) Result { return Result{Content: []Block{Text(msg)}, IsError: true} }

// ToolError is an error whose text was written for the model that called the tool.
// A handler vouches for the text by building the error with Refuse or Refusef: it
// says what went wrong in words that name no path, no session and no message.
//
// It is the ONLY kind of error whose text reaches the model. Anything else a handler
// returns (a wrapped file-system error, a library error, a context error) is replaced
// by a fixed text and its detail goes to the server's log, so the default of a new
// error is to stay on the machine and the exception is to be said on purpose.
type ToolError struct{ text string }

func (e *ToolError) Error() string { return e.text }

// Refuse builds an error whose text is meant for the model.
func Refuse(text string) *ToolError { return &ToolError{text: text} }

// Refusef is Refuse with a format. It has no %w: the text it builds is a sentence of
// its own, and wrapping another error in it is exactly what it exists to prevent.
func Refusef(format string, args ...any) *ToolError {
	return &ToolError{text: fmt.Sprintf(format, args...)}
}

// neutralToolError is what the model is told when a handler fails with an error that
// was not written for it.
const neutralToolError = "the tool could not complete this call on the machine it runs on. The details are in the server's log, not here"

// Handler runs one tool call. ctx is cancelled when the client withdraws the
// request or the server shuts down. An error returned here becomes an IsError
// result, but only a *ToolError carries its text to the model; any other error
// becomes neutralToolError and is logged (see ToolError).
type Handler func(ctx context.Context, args json.RawMessage) (Result, error)

// Tool is one entry of the registry.
type Tool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Handler     Handler
}

// ClientInfo is what the client said about itself in `initialize`. It is the only
// thing in a request that tells one surface from another (chat from Cowork).
type ClientInfo struct {
	Name            string
	Version         string
	ProtocolVersion string
}

type rawMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

var nullID = json.RawMessage("null")

// hasID reports whether the message carries a usable request id. A null id is
// invalid in MCP, so it counts as absent here and the caller decides what that
// means for a message that also has a method.
func (m *rawMessage) hasID() bool {
	return len(m.ID) > 0 && !bytes.Equal(bytes.TrimSpace(m.ID), nullID)
}

// DecodeArgs decodes tool arguments STRICTLY: an unknown field is an error, not a
// field silently dropped. Absent or null arguments decode as an empty object, so
// a tool with no parameters accepts both spellings.
func DecodeArgs(raw json.RawMessage, dst any) error {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, nullID) {
		trimmed = []byte("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		// The decoder's words name the caller's own keys and Go types, nothing else.
		return Refusef("invalid arguments: %v", err)
	}
	if dec.More() {
		return Refuse("invalid arguments: trailing data after the object")
	}
	return nil
}
