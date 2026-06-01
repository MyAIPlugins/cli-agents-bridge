package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

func TestValidateEmit(t *testing.T) {
	t.Parallel()
	require.NoError(t, validateEmit(emitJSON))
	require.NoError(t, validateEmit(emitContent))
	require.Error(t, validateEmit("xml"))
	require.Error(t, validateEmit(""))
}

func TestEmitMessage_Content_BodyOnly(t *testing.T) {
	// Not parallel: captureStdout swaps os.Stdout process-wide.
	m := &message.Message{ID: "msg-aaaaaaaaaaaa", Content: "just the body"}
	out := captureStdout(t, func() {
		enc := json.NewEncoder(os.Stdout)
		require.NoError(t, emitMessage(enc, emitContent, m))
	})
	assert.Equal(t, "just the body", strings.TrimSpace(out))
	assert.NotContains(t, out, "msg-aaaaaaaaaaaa", "content mode emits no id/envelope, only the body")
}

func TestEmitMessage_JSON_FullMessage(t *testing.T) {
	// Not parallel: captureStdout swaps os.Stdout process-wide.
	m := &message.Message{
		ID:            "msg-bbbbbbbbbbbb",
		SchemaVersion: message.SchemaVersionV2,
		Type:          message.TypeResponse,
		Content:       "body",
	}
	out := captureStdout(t, func() {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		require.NoError(t, emitMessage(enc, emitJSON, m))
	})
	var got message.Message
	require.NoError(t, json.Unmarshal([]byte(out), &got))
	assert.Equal(t, "msg-bbbbbbbbbbbb", got.ID)
	assert.Equal(t, "body", got.Content)
	assert.Equal(t, message.TypeResponse, got.Type)
}
