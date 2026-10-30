package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/message"
	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
)

const inboxSID = "arcinbox1"

var inboxT0 = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

func sessionDirOf(dataDir string) string { return filepath.Join(dataDir, "sessions", inboxSID) }

func allowAll(*message.Message, int) error { return nil }

func TestScanInboxWithin_ReadsDeclaresCorruptAndDropsReceipts(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantInboxAt(t, dataDir, inboxSID, "msg-aaaaaaaaaaaa", "valinbox1", message.TypeQuery, "brief", inboxT0)
	plantInboxAt(t, dataDir, inboxSID, "msg-bbbbbbbbbbbb", "valinbox1", message.TypeNotify, "note", inboxT0.Add(time.Minute))
	plantInboxAt(t, dataDir, inboxSID, "msg-cccccccccccc", "valinbox1", message.TypeAck, "legacy receipt", inboxT0)
	inbox := filepath.Join(sessionDirOf(dataDir), "inbox")
	require.NoError(t, os.WriteFile(filepath.Join(inbox, "msg-broken000000.json"), []byte("{nope"), 0o600))

	scan, err := scanInboxWithin(context.Background(), &scanGuard{}, inbox, 65536)
	require.NoError(t, err)
	assert.Len(t, scan.entries, 2, "the legacy receipt is dropped by the same filter `next` has")
	assert.Equal(t, []string{"msg-broken000000.json"}, scan.corrupt, "an unreadable file is declared, not skipped in silence")
}

func TestScanInboxWithin_AMissingInboxIsEmptyNotAnError(t *testing.T) {
	t.Parallel()
	scan, err := scanInboxWithin(context.Background(), &scanGuard{}, filepath.Join(t.TempDir(), "nowhere"), 65536)
	require.NoError(t, err)
	assert.Empty(t, scan.entries)
}

func TestScanInboxWithin_AFileThatIsNotOursFailsTheWholeScan(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantInboxAt(t, dataDir, inboxSID, "msg-aaaaaaaaaaaa", "valinbox1", message.TypeQuery, "brief", inboxT0)
	inbox := filepath.Join(sessionDirOf(dataDir), "inbox")
	outside := filepath.Join(t.TempDir(), "elsewhere.json")
	require.NoError(t, os.WriteFile(outside, []byte("{}"), 0o600))
	mustSymlink(t, outside, filepath.Join(inbox, "msg-bbbbbbbbbbbb.json")) // a link is never ours

	_, err := scanInboxWithin(context.Background(), &scanGuard{}, inbox, 65536)
	require.ErrorIs(t, err, security.ErrOwnershipMismatch)
}

// The budget is a deadline, and missing it is an error and never a shorter list.
func TestScanInboxWithin_AnExpiredBudgetIsAnErrorAndTheGuardIsFreedWhenTheScanEnds(t *testing.T) {
	dataDir := t.TempDir()
	for i := 0; i < 1500; i++ {
		plantInboxAt(t, dataDir, inboxSID, fmt.Sprintf("msg-%012d", i), "valinbox1", message.TypeNotify, "x", inboxT0)
	}
	inbox := filepath.Join(sessionDirOf(dataDir), "inbox")
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)

	guard := &scanGuard{}
	_, err := scanInboxWithin(ctx, guard, inbox, 65536)
	require.ErrorIs(t, err, errInboxScanTimeout)

	require.Eventually(t, func() bool { return !guard.busy.Load() }, 10*time.Second, 5*time.Millisecond,
		"the abandoned scan finishes and frees the guard: a timeout does not wedge the project")
	scan, err := scanInboxWithin(context.Background(), guard, inbox, 65536)
	require.NoError(t, err)
	assert.Len(t, scan.entries, 1500)
}

func TestScanInboxWithin_ASecondScanWhileOneIsRunningIsRefusedNotStacked(t *testing.T) {
	t.Parallel()
	guard := &scanGuard{}
	guard.busy.Store(true) // a slow scan is in flight
	_, err := scanInboxWithin(context.Background(), guard, t.TempDir(), 65536)
	require.ErrorIs(t, err, errInboxScanTimeout)
}

func TestLocateMessage(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantInboxAt(t, dataDir, inboxSID, "msg-aaaaaaaaaaaa", "valinbox1", message.TypeQuery, "in the inbox", inboxT0)
	plantProcessedAt(t, dataDir, inboxSID, "msg-bbbbbbbbbbbb", "valinbox1", message.TypeNotify, "archived", inboxT0)
	dir := sessionDirOf(dataDir)

	loc, err := locateMessage(dir, "msg-aaaaaaaaaaaa")
	require.NoError(t, err)
	assert.False(t, loc.archived)

	loc, err = locateMessage(dir, "msg-bbbbbbbbbbbb")
	require.NoError(t, err, "archived files carry a timestamp prefix: found by suffix, not by decoding everything")
	assert.True(t, loc.archived)

	_, err = locateMessage(dir, "msg-ffffffffffff")
	require.ErrorIs(t, err, errMessageNotFound)

	for _, bad := range []string{"", "../../etc/passwd", "msg-short", "msg-AAAAAAAAAAAA", "msg-aaaaaaaaaaaa/../x"} {
		_, err = locateMessage(dir, bad)
		require.Error(t, err, "%q", bad)
		assert.NotErrorIs(t, err, errMessageNotFound, "%q is refused as an id, it never reached the filesystem", bad)
	}
}

func queryRefused(m *message.Message, _ int) error {
	if m.Type == message.TypeQuery {
		return errors.New("an ask is closed with reply, not with ack")
	}
	return nil
}

func inboxHas(dataDir, id string) bool {
	_, err := os.Stat(filepath.Join(sessionDirOf(dataDir), "inbox", id+".json"))
	return err == nil
}

func processedCount(t *testing.T, dataDir string) int {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(sessionDirOf(dataDir), "processed"))
	if os.IsNotExist(err) {
		return 0
	}
	require.NoError(t, err)
	return len(entries)
}

func TestArchiveBatch_ArchivesExactlyTheNamedMessagesAndOnlyThose(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	for i, id := range []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb", "msg-cccccccccccc"} {
		plantInboxAt(t, dataDir, inboxSID, id, "valinbox1", message.TypeNotify, "n", inboxT0.Add(time.Duration(i)*time.Minute))
	}
	res, ok := archiveBatch(sessionDirOf(dataDir), []string{"msg-aaaaaaaaaaaa", "msg-cccccccccccc"}, 65536, allowAll, moveToProcessed)
	require.True(t, ok)
	assert.Equal(t, []ackResult{{ID: "msg-aaaaaaaaaaaa", Outcome: ackArchived}, {ID: "msg-cccccccccccc", Outcome: ackArchived}}, res)
	assert.False(t, inboxHas(dataDir, "msg-aaaaaaaaaaaa"))
	assert.False(t, inboxHas(dataDir, "msg-cccccccccccc"))
	assert.True(t, inboxHas(dataDir, "msg-bbbbbbbbbbbb"), "a message that was not named stays: an ack never sweeps")
}

// A delayed or repeated ack: the first one's response was lost and the agent asks
// again. The second finds the ids in processed/ and says so; it touches nothing, in
// particular not a message that arrived in between.
func TestArchiveBatch_ARepeatedAckIsIdempotentAndLeavesNewerMessagesAlone(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantInboxAt(t, dataDir, inboxSID, "msg-aaaaaaaaaaaa", "valinbox1", message.TypeNotify, "n", inboxT0)
	_, ok := archiveBatch(sessionDirOf(dataDir), []string{"msg-aaaaaaaaaaaa"}, 65536, allowAll, moveToProcessed)
	require.True(t, ok)
	plantInboxAt(t, dataDir, inboxSID, "msg-bbbbbbbbbbbb", "valinbox1", message.TypeNotify, "arrived later", inboxT0.Add(time.Hour))

	res, ok := archiveBatch(sessionDirOf(dataDir), []string{"msg-aaaaaaaaaaaa"}, 65536, allowAll, moveToProcessed)
	require.True(t, ok)
	assert.Equal(t, []ackResult{{ID: "msg-aaaaaaaaaaaa", Outcome: ackAlreadyArchived}}, res)
	assert.True(t, inboxHas(dataDir, "msg-bbbbbbbbbbbb"), "the late ack cannot confirm a message it does not name")
	assert.Equal(t, 1, processedCount(t, dataDir))
}

// Phase one validates everything before anything moves: one bad id and nothing is archived.
func TestArchiveBatch_OneRefusedIdRefusesTheWholeBatchAndMovesNothing(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		second string
		plant  func(t *testing.T, dataDir string)
	}{
		{"an ask in the middle", "msg-bbbbbbbbbbbb", func(t *testing.T, dd string) {
			plantInboxAt(t, dd, inboxSID, "msg-bbbbbbbbbbbb", "valinbox1", message.TypeQuery, "an ask", inboxT0)
		}},
		{"an id that does not exist", "msg-dddddddddddd", func(t *testing.T, dd string) {}},
		{"an id that is not an id", "../../x", func(t *testing.T, dd string) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dataDir := t.TempDir()
			plantInboxAt(t, dataDir, inboxSID, "msg-aaaaaaaaaaaa", "valinbox1", message.TypeNotify, "fine", inboxT0)
			plantInboxAt(t, dataDir, inboxSID, "msg-cccccccccccc", "valinbox1", message.TypeNotify, "also fine", inboxT0)
			tc.plant(t, dataDir)

			res, ok := archiveBatch(sessionDirOf(dataDir), []string{"msg-aaaaaaaaaaaa", tc.second, "msg-cccccccccccc"}, 65536, queryRefused, moveToProcessed)
			require.False(t, ok)
			require.Len(t, res, 3)
			assert.Equal(t, ackNotAttempted, res[0].Outcome, "the good ids are reported as not attempted, never as done")
			assert.Equal(t, ackRefused, res[1].Outcome)
			assert.Equal(t, ackNotAttempted, res[2].Outcome)
			assert.True(t, inboxHas(dataDir, "msg-aaaaaaaaaaaa") && inboxHas(dataDir, "msg-cccccccccccc"), "nothing moved")
			assert.Zero(t, processedCount(t, dataDir))
		})
	}
}

func TestArchiveBatch_ACallerCheckThatRefusesStopsTheBatch(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantInboxAt(t, dataDir, inboxSID, "msg-aaaaaaaaaaaa", "valinbox1", message.TypeNotify, "held back", inboxT0)
	res, ok := archiveBatch(sessionDirOf(dataDir), []string{"msg-aaaaaaaaaaaa"}, 65536,
		func(*message.Message, int) error { return errors.New("not visible to Desktop") }, moveToProcessed)
	require.False(t, ok)
	assert.Equal(t, ackRefused, res[0].Outcome)
	assert.True(t, inboxHas(dataDir, "msg-aaaaaaaaaaaa"), "Desktop cannot archive what it was never shown")
}

func TestArchiveBatch_DuplicatesAreCollapsedAndTheSizeIsBounded(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	plantInboxAt(t, dataDir, inboxSID, "msg-aaaaaaaaaaaa", "valinbox1", message.TypeNotify, "n", inboxT0)
	res, ok := archiveBatch(sessionDirOf(dataDir), []string{"msg-aaaaaaaaaaaa", "msg-aaaaaaaaaaaa"}, 65536, allowAll, moveToProcessed)
	require.True(t, ok)
	assert.Len(t, res, 1)

	var many []string
	for i := 0; i <= maxAckBatch; i++ {
		many = append(many, fmt.Sprintf("msg-%012d", i))
	}
	res, ok = archiveBatch(sessionDirOf(dataDir), many, 65536, allowAll, moveToProcessed)
	require.False(t, ok)
	assert.Contains(t, res[0].Detail, "at most")

	_, ok = archiveBatch(sessionDirOf(dataDir), nil, 65536, allowAll, moveToProcessed)
	assert.False(t, ok, "an empty ack is refused, not a success")
}

// Phase two: an error between two moves leaves a completed prefix, says so, stops,
// and the retry finishes the job without redoing the first.
func TestArchiveBatch_AFailureBetweenTwoMovesLeavesAPrefixAndTheRetryFinishes(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	ids := []string{"msg-aaaaaaaaaaaa", "msg-bbbbbbbbbbbb", "msg-cccccccccccc"}
	for _, id := range ids {
		plantInboxAt(t, dataDir, inboxSID, id, "valinbox1", message.TypeNotify, "n", inboxT0)
	}
	moves := 0
	flaky := func(src, dir string) error {
		moves++
		if moves == 2 {
			return errors.New("disk said no")
		}
		return moveToProcessed(src, dir)
	}
	res, ok := archiveBatch(sessionDirOf(dataDir), ids, 65536, allowAll, flaky)
	require.False(t, ok)
	assert.Equal(t, ackArchived, res[0].Outcome)
	assert.Equal(t, ackFailed, res[1].Outcome)
	assert.Equal(t, ackNotAttempted, res[2].Outcome, "it stops at the first failure and says what it did not try")
	assert.NotContains(t, res[1].Detail, "disk said no", "the reason of an I/O error is not echoed to the model")

	res, ok = archiveBatch(sessionDirOf(dataDir), ids, 65536, allowAll, moveToProcessed)
	require.True(t, ok)
	assert.Equal(t, ackAlreadyArchived, res[0].Outcome, "the retry does not redo the prefix")
	assert.Equal(t, ackArchived, res[1].Outcome)
	assert.Equal(t, ackArchived, res[2].Outcome)
	assert.Equal(t, 3, processedCount(t, dataDir))
}
