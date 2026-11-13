package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F-178: how the delivery of a response looks for a message among the recipient's, and the two
// properties of that look that no caller states and every caller needs: the ORDER (inbox/ first)
// and what happens with TWO entries under one id.

const locateID = "msg-aaaaaaaaaaaa"

func plantProcessedFile(t *testing.T, dir, stamp, id, content string) string {
	t.Helper()
	processed := filepath.Join(dir, "processed")
	require.NoError(t, os.MkdirAll(processed, 0o700))
	path := filepath.Join(processed, stamp+"-"+id+".json")
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600))
	return path
}

// The order is inbox/ first. A message that is in both places answers "in the inbox": that is the
// answer that sends the delivery to the create-if-absent, which judges the bytes. The reverse order
// would answer "archived" and skip it. This pins the order for the one caller that can be hurt by
// it: a message only moves from inbox/ to processed/, so inbox/ then processed/ cannot see it in
// neither while a rename is in progress, and processed/ then inbox/ can.
func TestLocateMessage_TheInboxIsLookedAtBeforeProcessed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "inbox"), 0o700))
	inboxPath := filepath.Join(dir, "inbox", locateID+".json")
	require.NoError(t, os.WriteFile(inboxPath, []byte("in the inbox"), 0o600))
	plantProcessedFile(t, dir, "20260101T000000.000000000Z", locateID, "in processed")

	for name, locate := range map[string]func(string, string) (located, error){"locateMessage": locateMessage, "locateMessageStrict": locateMessageStrict} {
		loc, err := locate(dir, locateID)
		require.NoError(t, err, name)
		assert.False(t, loc.archived, "%s: in the inbox wins", name)
		assert.Equal(t, inboxPath, loc.path, name)
	}
}

// Two entries of processed/ under one id: the first by name, which starts with the instant of the
// archiving, so the oldest. What the delivery makes of it is in responseArchivedByRecipient: the
// oldest decides. Same bytes first: already delivered. Other bytes first: an error, even if a
// later copy is the same, because the first one is a different message under this id and that is a
// collision, severe on purpose.
func TestResponseArchivedByRecipient_TwoEntriesUnderOneIdAreJudgedByTheOldest(t *testing.T) {
	data := []byte("the response")
	for _, tc := range []struct {
		name         string
		first, later string
		delivered    bool
		wantErr      bool
	}{
		{"oldest is the same, later is the same", "the response", "the response", true, false},
		{"oldest is the same, later differs", "the response", "other", true, false},
		{"oldest differs, later is the same", "other", "the response", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// planted newest first: the order of the names decides, not the order of creation
			plantProcessedFile(t, dir, "20260102T000000.000000000Z", locateID, tc.later)
			plantProcessedFile(t, dir, "20260101T000000.000000000Z", locateID, tc.first)

			archived, err := responseArchivedByRecipient(dir, locateID, data)
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "different content")
				assert.False(t, archived)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.delivered, archived)
		})
	}
}

// Nothing there is not an error: a recipient that never archived anything has no processed/ at all.
func TestResponseArchivedByRecipient_NothingArchivedIsNotAnError(t *testing.T) {
	for name, setup := range map[string]func(dir string){
		"no processed/ folder": func(string) {},
		"an empty one":         func(dir string) { require.NoError(t, os.MkdirAll(filepath.Join(dir, "processed"), 0o700)) },
		"other messages only": func(dir string) {
			plantProcessedFile(t, dir, "20260101T000000.000000000Z", "msg-bbbbbbbbbbbb", "another message")
		},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			setup(dir)
			archived, err := responseArchivedByRecipient(dir, locateID, []byte("the response"))
			require.NoError(t, err)
			assert.False(t, archived)
		})
	}
}
