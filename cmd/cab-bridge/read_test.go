package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// plantProcessedTimestamped writes a valid v2 message into processed/ using the
// REAL MoveToProcessed naming (<timestamp>-<id>.json), so the tests prove
// findMessage matches on the decoded id rather than the filename — the S-2
// concern the brief flagged (processed/ files are NOT named <id>.json).
func plantProcessedTimestamped(t *testing.T, dataDir, sid, id, content string) {
	t.Helper()
	dir := filepath.Join(dataDir, "sessions", sid, "processed")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	m := message.Message{
		ID:            id,
		SchemaVersion: message.SchemaVersionV2,
		From:          "esc99999",
		FromRole:      "esc",
		FromAgentName: "ESC-y",
		To:            sid,
		ToRole:        "val",
		Type:          message.TypeResponse,
		Timestamp:     "2026-05-31T09:00:00Z",
		Status:        message.StatusPending,
		Content:       content,
		Metadata:      message.Metadata{FromProject: "test", ProcessingState: message.StatusPending},
	}
	data, err := json.Marshal(&m)
	require.NoError(t, err)
	name := "20260531T090000.000000000Z-" + id + ".json"
	require.NoError(t, os.WriteFile(filepath.Join(dir, name), data, 0o600))
}

func TestFindMessage_FoundInInbox(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "readfm01"
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "brief body")

	m, box, err := findMessage(filepath.Join(dataDir, "sessions", sid), "msg-aaaaaaaaaaaa", 65536)
	require.NoError(t, err)
	assert.Equal(t, "inbox", box)
	assert.Equal(t, "brief body", m.Content)
}

// The S-2 case: a processed/ file carries a timestamp prefix in its name, so a
// filename lookup would miss it. findMessage must match on the decoded m.ID.
func TestFindMessage_FoundInProcessedDespiteTimestampPrefix(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "readfm02"
	plantProcessedTimestamped(t, dataDir, sid, "msg-bbbbbbbbbbbb", "archived report")

	m, box, err := findMessage(filepath.Join(dataDir, "sessions", sid), "msg-bbbbbbbbbbbb", 65536)
	require.NoError(t, err)
	assert.Equal(t, "processed", box, "matched by decoded id, not by filename")
	assert.Equal(t, "archived report", m.Content)
}

func TestFindMessage_InboxPrecedenceOverProcessed(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "readfm03"
	id := "msg-cccccccccccc"
	plantMsg(t, dataDir, sid, "inbox", id, "val12345", "VAL-x", message.TypeQuery, "pending copy")
	plantProcessedTimestamped(t, dataDir, sid, id, "archived copy")

	m, box, err := findMessage(filepath.Join(dataDir, "sessions", sid), id, 65536)
	require.NoError(t, err)
	assert.Equal(t, "inbox", box, "inbox/ is scanned first: a still-pending copy wins")
	assert.Equal(t, "pending copy", m.Content)
}

func TestFindMessage_NotFound(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "readfm04"
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "x")

	_, _, err := findMessage(filepath.Join(dataDir, "sessions", sid), "msg-dddddddddddd", 65536)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMessageNotFound)
}

func TestRunRead_ContentDefault(t *testing.T) {
	// Not parallel: t.Setenv.
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	sid := "readrr01"
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "the full brief body")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runRead([]string{"--session-id=" + sid, "msg-aaaaaaaaaaaa"})
	})
	require.NoError(t, runErr)
	assert.Equal(t, "the full brief body", strings.TrimSpace(out))
	assert.NotContains(t, out, "schemaVersion", "default content mode emits only the body, no JSON envelope")
}

func TestRunRead_JSON(t *testing.T) {
	// Not parallel: t.Setenv.
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	sid := "readrr02"
	plantProcessedTimestamped(t, dataDir, sid, "msg-bbbbbbbbbbbb", "done report")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runRead([]string{"--session-id=" + sid, "--json", "msg-bbbbbbbbbbbb"})
	})
	require.NoError(t, runErr)
	var m message.Message
	require.NoError(t, json.Unmarshal([]byte(out), &m))
	assert.Equal(t, "msg-bbbbbbbbbbbb", m.ID)
	assert.Equal(t, "done report", m.Content)
	assert.Equal(t, message.TypeResponse, m.Type)
}

func TestRunRead_MalformedID(t *testing.T) {
	// Validation runs before any FS access, so no setup is needed.
	err := runRead([]string{"--session-id=readrr03", "not-a-msg-id"})
	require.Error(t, err)
	assert.ErrorIs(t, err, message.ErrInvalidMessageID)
}

func TestRunRead_NotFound(t *testing.T) {
	// Not parallel: t.Setenv.
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	sid := "readrr04"
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "x")

	err := runRead([]string{"--session-id=" + sid, "msg-ffffffffffff"})
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrMessageNotFound)
}

// read is a PURE read: the message stays exactly where it was, processed/ is not
// created as a side effect.
func TestRunRead_NoConsume(t *testing.T) {
	// Not parallel: t.Setenv.
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	sid := "readrr05"
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "stays put")
	inboxFile := filepath.Join(dataDir, "sessions", sid, "inbox", "msg-aaaaaaaaaaaa.json")

	var runErr error
	_ = captureStdout(t, func() {
		runErr = runRead([]string{"--session-id=" + sid, "msg-aaaaaaaaaaaa"})
	})
	require.NoError(t, runErr)
	assert.FileExists(t, inboxFile, "read must not move the message out of inbox/")
	assert.NoDirExists(t, filepath.Join(dataDir, "sessions", sid, "processed"), "read must not create processed/")
}

func TestRunRead_RequiresExactlyOneArg(t *testing.T) {
	// No positional arg: errors before any FS access.
	err := runRead([]string{"--session-id=readrr06"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "exactly one")
}
