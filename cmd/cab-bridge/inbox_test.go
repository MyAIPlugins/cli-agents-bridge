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

// plantMsg writes a valid v2 message JSON into dataDir/sessions/<sid>/<box>/ so
// the read-only listing can be driven without a full register/listen cycle.
func plantMsg(t *testing.T, dataDir, sid, box, id, from, agentName, msgType, content string) {
	t.Helper()
	dir := filepath.Join(dataDir, "sessions", sid, box)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	m := message.Message{
		ID:            id,
		SchemaVersion: message.SchemaVersionV2,
		From:          from,
		FromRole:      "val",
		FromAgentName: agentName,
		To:            sid,
		ToRole:        "esc",
		Type:          msgType,
		Timestamp:     "2026-05-31T09:00:00Z",
		Status:        message.StatusPending,
		Content:       content,
		Metadata:      message.Metadata{FromProject: "test", ProcessingState: message.StatusPending},
	}
	data, err := json.Marshal(&m)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, id+".json"), data, 0o600))
}

// TestCollectInbox_ListsBothBoxesDistinctAndDoesNotConsume is the F-22 core:
// inbox/ (pending) and processed/ (consumed) are both listed and distinguished,
// fields map correctly, and the read consumes nothing (files stay on disk).
func TestCollectInbox_ListsBothBoxesDistinctAndDoesNotConsume(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "inboxts1"
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "brief one")
	plantMsg(t, dataDir, sid, "inbox", "msg-bbbbbbbbbbbb", "val12345", "VAL-x", message.TypeQuery, "brief two")
	plantMsg(t, dataDir, sid, "processed", "msg-cccccccccccc", "esc99999", "ESC-y", message.TypeResponse, "done report")

	entries, err := collectInbox(filepath.Join(dataDir, "sessions", sid), 65536)
	require.NoError(t, err)
	require.Len(t, entries, 3)

	box := map[string]string{}
	var proc inboxEntry
	for _, e := range entries {
		box[e.MsgID] = e.Box
		if e.MsgID == "msg-cccccccccccc" {
			proc = e
		}
	}
	assert.Equal(t, "inbox", box["msg-aaaaaaaaaaaa"])
	assert.Equal(t, "inbox", box["msg-bbbbbbbbbbbb"])
	assert.Equal(t, "processed", box["msg-cccccccccccc"])

	assert.Equal(t, "esc99999", proc.From)
	assert.Equal(t, "ESC-y", proc.FromAgentName)
	assert.Equal(t, message.TypeResponse, proc.Type)
	assert.Equal(t, "done report", proc.Preview)

	// NON-consumo: --list must never move/delete; every file stays on disk.
	assert.FileExists(t, filepath.Join(dataDir, "sessions", sid, "inbox", "msg-aaaaaaaaaaaa.json"))
	assert.FileExists(t, filepath.Join(dataDir, "sessions", sid, "inbox", "msg-bbbbbbbbbbbb.json"))
	assert.FileExists(t, filepath.Join(dataDir, "sessions", sid, "processed", "msg-cccccccccccc.json"))
}

func TestRunInbox_JSONOutput(t *testing.T) {
	// Not parallel: t.Setenv (CAB_DATA_DIR).
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	sid := "inboxts2"
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "hello")
	plantMsg(t, dataDir, sid, "processed", "msg-bbbbbbbbbbbb", "esc99999", "ESC-y", message.TypeResponse, "world")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runInbox([]string{"--session-id=" + sid, "--list", "--json"})
	})
	require.NoError(t, runErr)

	var entries []inboxEntry
	require.NoError(t, json.Unmarshal([]byte(out), &entries))
	require.Len(t, entries, 2)
	assert.Equal(t, "inbox", entries[0].Box, "inbox entries listed before processed")
	assert.Equal(t, "processed", entries[1].Box)
}

func TestRunInbox_EmptyBoxes_EmptyJSONNoCrash(t *testing.T) {
	// Not parallel: t.Setenv.
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)

	var runErr error
	out := captureStdout(t, func() {
		runErr = runInbox([]string{"--session-id=inboxts3", "--list", "--json"})
	})
	require.NoError(t, runErr)
	assert.Equal(t, "[]", strings.TrimSpace(out), "no boxes -> empty JSON array (not null), no crash")
}

func TestRunInbox_RequiresListFlag(t *testing.T) {
	// Not parallel: t.Setenv.
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)

	err := runInbox([]string{"--session-id=inboxts4"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "--list", "without --list the command must explain it needs --list")
}

// TestTidyInbox_ArchivesWellFormedLeavesForensics is the F-22 --tidy core: every
// well-formed inbox message is moved to processed/, malformed/.tmp files stay in
// inbox (forensics), processed/ is untouched, and a second tidy is a no-op.
func TestTidyInbox_ArchivesWellFormedLeavesForensics(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	sid := "tidyts1"
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "one")
	plantMsg(t, dataDir, sid, "inbox", "msg-bbbbbbbbbbbb", "val12345", "VAL-x", message.TypeQuery, "two")
	plantMsg(t, dataDir, sid, "processed", "msg-cccccccccccc", "esc99999", "ESC-y", message.TypeResponse, "old")

	inbox := filepath.Join(dataDir, "sessions", sid, "inbox")
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "msg-dddddddddddd.json"), []byte("{not valid json"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(inbox, ".tmp.partial.json"), []byte("{}"), 0o600))

	sessionDir := filepath.Join(dataDir, "sessions", sid)
	n, err := tidyInbox(sessionDir, 65536)
	require.NoError(t, err)
	assert.Equal(t, 2, n, "two well-formed inbox messages archived")

	assert.NoFileExists(t, filepath.Join(inbox, "msg-aaaaaaaaaaaa.json"))
	assert.NoFileExists(t, filepath.Join(inbox, "msg-bbbbbbbbbbbb.json"))
	assert.FileExists(t, filepath.Join(inbox, "msg-dddddddddddd.json"), "malformed file stays for forensics")
	assert.FileExists(t, filepath.Join(inbox, ".tmp.partial.json"), ".tmp file stays")

	entries, err := collectInbox(sessionDir, 65536)
	require.NoError(t, err)
	processed := 0
	for _, e := range entries {
		if e.Box == "processed" {
			processed++
		}
	}
	assert.Equal(t, 3, processed, "processed holds the original plus the two tidied")

	n2, err := tidyInbox(sessionDir, 65536)
	require.NoError(t, err)
	assert.Equal(t, 0, n2, "second tidy moves nothing")
}

func TestTidyInbox_MissingInbox_ZeroNoCrash(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	n, err := tidyInbox(filepath.Join(dataDir, "sessions", "tidyts4"), 65536)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}

func TestRunInbox_ListAndTidy_MutuallyExclusive(t *testing.T) {
	t.Parallel()
	err := runInbox([]string{"--session-id=tidyts3", "--list", "--tidy"})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestRunInbox_Tidy_JSONCount(t *testing.T) {
	// Not parallel: t.Setenv.
	dataDir := t.TempDir()
	t.Setenv("CAB_DATA_DIR", dataDir)
	sid := "tidyts2"
	plantMsg(t, dataDir, sid, "inbox", "msg-aaaaaaaaaaaa", "val12345", "VAL-x", message.TypeQuery, "one")

	var runErr error
	out := captureStdout(t, func() {
		runErr = runInbox([]string{"--session-id=" + sid, "--tidy", "--json"})
	})
	require.NoError(t, runErr)
	var res map[string]int
	require.NoError(t, json.Unmarshal([]byte(out), &res))
	assert.Equal(t, 1, res["tidied"])
}

func TestPreviewContent(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "short one", previewContent("short one", 80))
	assert.Equal(t, "a b c", previewContent("a\n  b\t c", 80), "whitespace/newlines collapse to single spaces")

	long := strings.Repeat("x", 100)
	got := previewContent(long, 80)
	assert.Equal(t, 83, len(got), "80 runes + 3-char ellipsis")
	assert.True(t, strings.HasSuffix(got, "..."), "overflow gets an ellipsis marker")
}
