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

// The order is inbox/ first. A message that is in both places answers "in the inbox" first: for the
// readers that is the whole answer, for the delivery it is the first of the copies and the one that
// sends it to the create-if-absent, which judges the bytes. This pins the order for the one caller
// that can be hurt by it: a message only moves from inbox/ to processed/, so inbox/ then processed/
// cannot see it in neither while a rename is in progress, and processed/ then inbox/ can.
func TestLocate_TheInboxIsLookedAtBeforeProcessed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "inbox"), 0o700))
	inboxPath := filepath.Join(dir, "inbox", locateID+".json")
	require.NoError(t, os.WriteFile(inboxPath, []byte("in the inbox"), 0o600))
	archived := plantProcessedFile(t, dir, "20260101T000000.000000000Z", locateID, "in processed")

	loc, err := locateMessage(dir, locateID)
	require.NoError(t, err)
	assert.False(t, loc.archived, "the readers: in the inbox wins")
	assert.Equal(t, inboxPath, loc.path)

	copies, err := locateCopiesStrict(dir, locateID)
	require.NoError(t, err)
	require.Len(t, copies, 2, "the delivery sees both")
	assert.Equal(t, located{path: inboxPath}, copies[0], "inbox/ first")
	assert.Equal(t, located{path: archived, archived: true}, copies[1])
}

// Two entries of processed/ under one id. The readers keep the choice they always had, the first by
// name (which starts with the instant of the archiving, so the oldest); the delivery gets both, in
// that order. What the delivery makes of them is the next test.
func TestLocate_TwoEntriesUnderOneIdReadersTakeTheFirstDeliveryGetsBoth(t *testing.T) {
	dir := t.TempDir()
	// planted newest first: the order of the names decides, not the order of creation
	later := plantProcessedFile(t, dir, "20260102T000000.000000000Z", locateID, "later")
	first := plantProcessedFile(t, dir, "20260101T000000.000000000Z", locateID, "first")

	loc, err := locateMessage(dir, locateID)
	require.NoError(t, err)
	assert.Equal(t, first, loc.path, "the readers: the oldest")

	copies, err := locateCopiesStrict(dir, locateID)
	require.NoError(t, err)
	require.Len(t, copies, 2)
	assert.Equal(t, first, copies[0].path)
	assert.Equal(t, later, copies[1].path)
}

// F-178, criterion 3 on copies that are SEVERAL: whether a response is "already delivered" does not
// depend on how its copies are named. One archived copy with other bytes, wherever it sorts, is a
// collision and an error; the same two contents with the names swapped answer the same. Found in
// the gate of the diff: with the identical copy first the delivery used to close the ask and say
// nothing about the other one.
func TestResponseArchivedByRecipient_EveryCopyIsJudgedWhateverTheOrderOfTheNames(t *testing.T) {
	const same, other = "the response", "other"
	for _, tc := range []struct {
		name         string
		first, later string
		delivered    bool
		wantErr      bool
	}{
		{"oldest is the same, later is the same", same, same, true, false},
		{"oldest is the same, later differs", same, other, false, true},
		{"oldest differs, later is the same", other, same, false, true},
		{"oldest differs, later differs", other, other, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			// planted newest first: the order of the names decides, not the order of creation
			plantProcessedFile(t, dir, "20260102T000000.000000000Z", locateID, tc.later)
			plantProcessedFile(t, dir, "20260101T000000.000000000Z", locateID, tc.first)

			archived, err := responseArchivedByRecipient(dir, locateID, []byte(same))
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

// The copy in inbox/ and the copy archived are the same pair of places: an archived copy with other
// bytes is an error whether the copy in inbox/ is the same or not, and an archived copy with the
// same bytes beside one in inbox/ leaves the judgement to the create-if-absent (false), which
// refuses an inbox/ copy with other bytes itself. Skipping that step when something is archived
// would let a different copy in inbox/ pass.
func TestResponseArchivedByRecipient_AnInboxCopyAndAnArchivedOne(t *testing.T) {
	const same, other = "the response", "other"
	for _, tc := range []struct {
		name            string
		inbox, archived string
		delivered       bool
		wantErr         bool
	}{
		{"inbox the same, archived the same: the create-if-absent decides", same, same, false, false},
		{"inbox the same, archived differs: an error", same, other, false, true},
		{"inbox differs, archived the same: false, the create-if-absent refuses it", other, same, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.MkdirAll(filepath.Join(dir, "inbox"), 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "inbox", locateID+".json"), []byte(tc.inbox), 0o600))
			plantProcessedFile(t, dir, "20260101T000000.000000000Z", locateID, tc.archived)

			archived, err := responseArchivedByRecipient(dir, locateID, []byte(same))
			if tc.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), "different content")
			} else {
				require.NoError(t, err)
			}
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
