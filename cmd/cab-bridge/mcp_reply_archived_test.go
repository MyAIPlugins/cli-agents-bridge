//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// F-178 on the Claude Desktop door. `toolReply` drives the very code the command line drives, so
// the check is there by construction; these tests run the real tool, the real server and the real
// disk, and fix what is its own: the text it hands back.

// valProcessedCopies counts the copies of a response in the processed/ of the val, by the name
// MoveToProcessed gives them (<instant>-<id>.json).
func valProcessedCopies(t *testing.T, j *joinedDemo, responseID string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(j.e.dataDir, "sessions", j.valSID, "processed"))
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), "-"+responseID+".json") {
			n++
		}
	}
	return n
}

// The scene of the defect through Desktop: a reply delivered its response and stopped before
// recording SENT, and the val archived the response (its `ack`). The next call to `reply` resumes
// the journal and must deliver nothing.
func TestMCPReply_ARecoveryAfterTheValArchivedTheResponseDeliversNothing(t *testing.T) {
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "ask A")

	selected := false
	j1, err := beginReply(j.e.manager(), j.arcSID,
		j.app.replySelector(j.app.lookup(projectKey("/repo/demo", surfaceCowork)), newAuthorizer(j.e.manager(), "/repo/demo", j.e.clock.now()), "msg-aaaaaaaaaaaa", "the answer to A", &selected), nil)
	require.NoError(t, err)
	delivered, derr := deliverResponse(j.app.cfg, j.e.manager(), j.arcSID, j1)
	require.NoError(t, derr)
	require.True(t, delivered, "premise: the first attempt delivered, and the journal is still pending")
	require.NoError(t, transportfs.MoveToProcessed(
		filepath.Join(j.e.dataDir, "sessions", j.valSID, "inbox", j1.ResponseID+".json"),
		filepath.Join(j.e.dataDir, "sessions", j.valSID, "processed")))
	require.Empty(t, j.valInboxFiles(), "premise: the val archived it")

	res := j.reply("msg-aaaaaaaaaaaa", "typed again after the crash")

	require.False(t, res.IsError, res.Text())
	assert.Contains(t, res.Text(), "has been completed")
	assert.Contains(t, res.Text(), "It went as it was first written", "the frozen text went, not this call's")
	assert.Empty(t, j.valInboxFiles(), "the val's inbox is not given the response again")
	assert.Equal(t, 1, valProcessedCopies(t, j, j1.ResponseID), "one copy: the archived one")
	assert.True(t, j.archived("msg-aaaaaaaaaaaa"), "the ask was closed")
	_, found := j.journal()
	assert.False(t, found, "the journal is complete and gone")
}

// The generic failure of the driver. It used to say "the next `reply` finishes it without sending
// it twice", which is false for every cause that repeats: a processed/ that cannot be read, or a
// message archived under the same id with other bytes, fails the same way at every attempt. The
// text says what the next call does, and when to stop asking it.
func TestMCPReply_AFailureThatRepeatsIsNotPromisedAsSolvedByTheNextCall(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root reads a folder with no permission")
	}
	j := newJoinedDemo(t)
	j.put("msg-aaaaaaaaaaaa", j.valSID, message.TypeQuery, "ask A")
	processed := filepath.Join(j.e.dataDir, "sessions", j.valSID, "processed")
	require.NoError(t, os.MkdirAll(processed, 0o700))
	require.NoError(t, os.Chmod(processed, 0o000))
	t.Cleanup(func() { _ = os.Chmod(processed, 0o700) })

	for attempt := 1; attempt <= 2; attempt++ {
		res := j.reply("msg-aaaaaaaaaaaa", "the answer to A")
		require.True(t, res.IsError, "attempt %d: %s", attempt, res.Text())
		text := res.Text()
		assert.Contains(t, text, "resumes it from where it stopped", "attempt %d", attempt)
		assert.Contains(t, text, "checking whether the recipient already has the response", "attempt %d", attempt)
		assert.Contains(t, text, "will not clear", "attempt %d: it says when to stop asking", attempt)
		assert.NotContains(t, text, "finishes it without sending it twice", "attempt %d: that promise is false here", attempt)
		assert.NotContains(t, res.Raw, "the answer to A", "what the journal holds is never shown")
		assert.Empty(t, j.valInboxFiles(), "attempt %d: nothing was delivered", attempt)
		_, found := j.journal()
		assert.True(t, found, "attempt %d: the journal is on record", attempt)
	}
}
