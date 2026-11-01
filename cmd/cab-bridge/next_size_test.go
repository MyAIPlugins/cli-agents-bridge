package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
)

// `bytes` in the JSON of `next` is the size of the TEXT, the number the CLI shows as
// `[83 B]` when a message is sent and the MCP header shows too. It used to be the size
// of the file in the inbox, envelope included, so one text said two numbers.

func inboxFileSize(t *testing.T, dataDir, sid, id string) int {
	t.Helper()
	st, err := os.Stat(filepath.Join(dataDir, "sessions", sid, "inbox", id+".json"))
	require.NoError(t, err)
	return int(st.Size())
}

func TestNext_BytesIsTheSizeOfTheTextNotOfTheFile(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	base := time.Now().UTC()
	// 13 characters, 14 bytes: counting characters is not counting bytes.
	bodies := map[string]string{
		"msg-aaaaaaaaaaaa": "caffè e latte",
		"msg-bbbbbbbbbbbb": strings.Repeat("x", 300),
	}
	require.NotEqual(t, len([]rune(bodies["msg-aaaaaaaaaaaa"])), len(bodies["msg-aaaaaaaaaaaa"]), "the premise: the text has a character of two bytes")
	for i, id := range []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb"} {
		plantInboxAt(t, dataDir, sid, id, "valxxx01", message.TypeNotify, bodies[id], base.Add(time.Duration(i)*time.Second))
		require.Greater(t, inboxFileSize(t, dataDir, sid, id), len(bodies[id]), "the premise: the file is bigger than its text")
	}

	p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
	require.Len(t, p.Messages, 2)
	for _, m := range p.Messages {
		assert.Equal(t, len(bodies[m.ID]), m.Bytes, "message %s", m.ID)
		assert.NotEqual(t, inboxFileSize(t, dataDir, sid, m.ID), m.Bytes, "message %s: not the file's size", m.ID)
		assert.Contains(t, formatSendEcho("ESC-y", "VAL-x", m.ID, bodies[m.ID], "tell"), "["+humanSize(m.Bytes)+"]",
			"message %s: the sender's echo and the reader's JSON count in the same unit", m.ID)
	}
}

func TestNext_AnOversizeMessageStillSaysHowBigItsTextIs(t *testing.T) {
	mgr, cfg, sid, dataDir := newNextSession(t)
	cfg.MaxPageBytes = 500
	body := strings.Repeat("y", 4000) + "é"
	plantInboxAt(t, dataDir, sid, "msg-aaaaaaaaaaaa", "valxxx01", message.TypeNotify, body, time.Now().UTC())
	require.Greater(t, inboxFileSize(t, dataDir, sid, "msg-aaaaaaaaaaaa"), len(body), "the premise: the file is bigger than its text")

	p := runNextOnce(t, mgr, cfg, sid, 2*time.Second)
	require.Len(t, p.Messages, 1)
	got := p.Messages[0]
	require.True(t, got.Oversize, "it took the pointer branch")
	require.Empty(t, got.Content)
	assert.Equal(t, len(body), got.Bytes)
}
