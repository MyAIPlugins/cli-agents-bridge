package main

import (
	"encoding/json"
	"fmt"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// Output formats for the --emit flag shared by listen and receive (F-48).
//
//	json    full message as indented JSON — the back-compatible scripting
//	        contract, and the ONLY mode that emits the {"status":"timeout"}
//	        wake envelope (an empty content stream cannot carry JSON).
//	content the message body only — zero-parsing for an agent that just wants
//	        the text, the same semantics as `read`.
const (
	emitJSON    = "json"
	emitContent = "content"
)

// validateEmit rejects an --emit value other than json|content. Callers wrap the
// returned error with their subcommand name.
func validateEmit(mode string) error {
	if mode != emitJSON && mode != emitContent {
		return fmt.Errorf("--emit must be %q or %q, got %q", emitJSON, emitContent, mode)
	}
	return nil
}

// emitMessage writes m to stdout per the --emit mode: content prints the body
// only (followed by a newline); json encodes the full message via enc (indented,
// one object per call — NDJSON across a batch). enc is reused across a batch so
// callers build it once. mode is assumed pre-validated by validateEmit.
func emitMessage(enc *json.Encoder, mode string, m *message.Message) error {
	if mode == emitContent {
		fmt.Println(m.Content)
		return nil
	}
	return enc.Encode(m)
}
