package fs

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDrainInboxOnce_EmptyDir_ReturnsNil(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	msgs, err := DrainInboxOnce(inbox, 65536)
	require.NoError(t, err)
	assert.Empty(t, msgs, "empty inbox must yield no messages")
}

func TestDrainInboxOnce_AbsentDir_ReturnsNilNoError(t *testing.T) {
	t.Parallel()

	// inbox not yet created — manager creates it lazily; not an error.
	msgs, err := DrainInboxOnce(filepath.Join(t.TempDir(), "nonexistent-inbox"), 65536)
	require.NoError(t, err)
	assert.Empty(t, msgs)
}

func TestDrainInboxOnce_Single_MovesToProcessed(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	want := validQueryMessage(t, "msg-aaaaaaaaaaaa", "abc123ef", "def456ab")
	writeMessage(t, inbox, want)

	msgs, err := DrainInboxOnce(inbox, 65536)
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	assert.Equal(t, want.ID, msgs[0].ID)
	assert.Equal(t, want.Content, msgs[0].Content)

	// Moved out of inbox/ into sibling processed/.
	_, statErr := os.Stat(filepath.Join(inbox, want.ID+".json"))
	assert.True(t, os.IsNotExist(statErr), "consumed message must leave inbox/")

	processed := filepath.Join(filepath.Dir(inbox), "processed")
	entries, err := os.ReadDir(processed)
	require.NoError(t, err)
	require.Len(t, entries, 1)
	assert.Contains(t, entries[0].Name(), want.ID)
}

// TestDrainInboxOnce_Multiple_NoMessageLost is the unit-level guard for the
// F-10 core invariant: when several messages are present in a single sweep,
// DrainInboxOnce must return ALL of them — none may be moved to processed/
// without being handed back to the caller. This is precisely the loss the
// channel-based PollInbox would risk if --wait-one exited mid-stream.
func TestDrainInboxOnce_Multiple_NoMessageLost(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	a := validQueryMessage(t, "msg-aaaaaaaaaaaa", "abc123ef", "def456ab")
	b := validQueryMessage(t, "msg-bbbbbbbbbbbb", "abc123ef", "def456ab")
	writeMessage(t, inbox, a)
	writeMessage(t, inbox, b)

	msgs, err := DrainInboxOnce(inbox, 65536)
	require.NoError(t, err)
	require.Len(t, msgs, 2, "both messages of the batch must be returned — no loss")

	got := map[string]bool{}
	for _, m := range msgs {
		got[m.ID] = true
	}
	assert.True(t, got[a.ID], "first message must be present")
	assert.True(t, got[b.ID], "second message must be present")

	// Both moved to processed/, inbox drained.
	inboxLeft, err := os.ReadDir(inbox)
	require.NoError(t, err)
	assert.Empty(t, inboxLeft, "inbox must be fully drained")

	processed := filepath.Join(filepath.Dir(inbox), "processed")
	processedEntries, err := os.ReadDir(processed)
	require.NoError(t, err)
	assert.Len(t, processedEntries, 2, "both messages must be in processed/")
}

func TestDrainInboxOnce_SkipsMalformedAndTempFiles(t *testing.T) {
	t.Parallel()

	inbox := filepath.Join(t.TempDir(), "inbox")
	require.NoError(t, os.MkdirAll(inbox, 0o700))

	require.NoError(t, os.WriteFile(filepath.Join(inbox, "garbage.json"), []byte("{not valid"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(inbox, ".tmp.abcd"), []byte("{}"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "readme.txt"), []byte("hi"), 0o600))
	want := validQueryMessage(t, "msg-cccccccccccc", "abc123ef", "def456ab")
	writeMessage(t, inbox, want)

	msgs, err := DrainInboxOnce(inbox, 65536)
	require.NoError(t, err)
	require.Len(t, msgs, 1, "only the valid message must be returned")
	assert.Equal(t, want.ID, msgs[0].ID)

	// Malformed/non-message files MUST remain on disk for forensics.
	_, statErr := os.Stat(filepath.Join(inbox, "garbage.json"))
	assert.NoError(t, statErr, "malformed message must be left on disk")
	_, statErr = os.Stat(filepath.Join(inbox, ".tmp.abcd"))
	assert.NoError(t, statErr, "temp leftover must be left on disk")
	_, statErr = os.Stat(filepath.Join(inbox, "readme.txt"))
	assert.NoError(t, statErr, "non-json file must be left on disk")
}
