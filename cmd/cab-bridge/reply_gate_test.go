package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/myAIPlugins/cli-agents-bridge/internal/routing"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
)

// --- the gate in beginReply: it depends on the state of the journal (F-72 C2) ---

type gateProbe struct {
	calls int
	err   error
}

func (g *gateProbe) gate() replyGate {
	return func(*session.ReplyTxn) error {
		g.calls++
		return g.err
	}
}

var errTestRefused = errors.New("test gate refuses")

// A refused reply never becomes a journal: a pending journal outranks every
// argument and retries its delivery forever, so persisting a refused reply would
// block every later reply of the session behind it.
func TestBeginReply_GateRefusesBeforeAnyJournalExists(t *testing.T) {
	mgr, _, _ := newReplyPair(t)
	probe := &gateProbe{err: errTestRefused}
	var called bool

	got, err := beginReply(mgr, replySelf, staticSelector(&called, newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "x")), probe.gate())
	require.Error(t, err)
	assert.ErrorIs(t, err, errReplyBlocked)
	assert.ErrorIs(t, err, errTestRefused, "the reason survives the wrapping")
	assert.Nil(t, got)
	assert.True(t, called, "the selector ran: the gate judges what it built")
	assert.Equal(t, 1, probe.calls)

	_, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	assert.False(t, found, "a refused reply leaves NO journal behind")
}

func TestBeginReply_AllowingGateLetsTheTransactionThrough(t *testing.T) {
	mgr, _, _ := newReplyPair(t)
	probe := &gateProbe{}
	var called bool

	got, err := beginReply(mgr, replySelf, staticSelector(&called, newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "x")), probe.gate())
	require.NoError(t, err)
	assert.Equal(t, "x", got.Content)
	assert.Equal(t, 1, probe.calls)
	_, found, _ := mgr.ReadReplyTxn(replySelf)
	assert.True(t, found)
}

// A pending journal still has a delivery to make, so it is gated; if refused it
// stays exactly as it was and the retry's own arguments do not replace it.
func TestBeginReply_PendingJournalIsGatedAndStaysIntact(t *testing.T) {
	mgr, _, _ := newReplyPair(t)
	first := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "the first answer")
	require.NoError(t, mgr.WriteReplyTxn(replySelf, first))
	probe := &gateProbe{err: errTestRefused}
	var called bool

	got, err := beginReply(mgr, replySelf, staticSelector(&called, newTxn(replySelf, []string{"msg-bbbbbbbbbbbb"}, "other")), probe.gate())
	require.ErrorIs(t, err, errReplyBlocked)
	assert.Nil(t, got)
	assert.False(t, called, "the retry's selector never runs over an in-flight journal")
	assert.Equal(t, 1, probe.calls, "the gate saw the EFFECTIVE transaction")

	onDisk, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	require.True(t, found, "blocking is not discarding")
	assert.Equal(t, first.ResponseID, onDisk.ResponseID)
	assert.Equal(t, "the first answer", onDisk.Content)
	assert.Equal(t, session.ReplyTxnPending, onDisk.State)
}

// A SENT journal has nothing left to deliver, only archiving to finish. A gate
// that refused it would leave the ask half-closed and jam the session, so it is
// not consulted at all.
func TestBeginReply_SentJournalIsNotGated(t *testing.T) {
	mgr, _, _ := newReplyPair(t)
	sent := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "already delivered")
	sent.State = session.ReplyTxnSent
	require.NoError(t, mgr.WriteReplyTxn(replySelf, sent))
	probe := &gateProbe{err: errTestRefused}
	var called bool

	got, err := beginReply(mgr, replySelf, staticSelector(&called, newTxn(replySelf, []string{"msg-bbbbbbbbbbbb"}, "other")), probe.gate())
	require.NoError(t, err, "finishing a delivered reply must always be possible")
	assert.Equal(t, sent.ResponseID, got.ResponseID)
	assert.Equal(t, session.ReplyTxnSent, got.State)
	assert.Equal(t, 0, probe.calls, "the delivery gate has no business with archive-only work")
	assert.False(t, called)
}

// A state this version does not know is an error. It is never read as "sent":
// that would skip a delivery that may not have happened, and report it done.
func TestBeginReply_UnknownJournalStateIsAnErrorNotSent(t *testing.T) {
	for _, state := range []string{"", "weird", "SENT"} {
		t.Run("state="+state, func(t *testing.T) {
			mgr, _, _ := newReplyPair(t)
			odd := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "odd")
			odd.State = state
			require.NoError(t, mgr.WriteReplyTxn(replySelf, odd))
			probe := &gateProbe{}
			var called bool

			got, err := beginReply(mgr, replySelf, staticSelector(&called, newTxn(replySelf, []string{"msg-bbbbbbbbbbbb"}, "other")), probe.gate())
			require.Error(t, err)
			assert.Contains(t, err.Error(), "does not know")
			assert.Nil(t, got)
			assert.False(t, called)

			onDisk, found, _ := mgr.ReadReplyTxn(replySelf)
			require.True(t, found, "left for a human to inspect")
			assert.Equal(t, "odd", onDisk.Content)
		})
	}
}

// --- the routing gate and the delivery gateway (F-147) -----------------------

// plantRolePair plants a responder and an asker with the given roles in one scope.
func plantRolePair(t *testing.T, selfRole, peerRole string) (*session.Manager, string) {
	t.Helper()
	dataDir := t.TempDir()
	cfg := nextTestConfig(t, dataDir)
	plantOverviewSession(t, dataDir, replySelf, selfRole, "SELF-gate", "/repo/g", "", session.StateIdle)
	plantOverviewSession(t, dataDir, replyPeer, peerRole, "PEER-gate", "/repo/g", "", session.StateIdle)
	return newSessionManager(cfg), dataDir
}

func gateInboxFiles(t *testing.T, dataDir, sid string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "sessions", sid, "inbox"))
	if os.IsNotExist(err) {
		return nil
	}
	require.NoError(t, err)
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// The routing policy binds an answer exactly as it binds an ask. The first row
// of the refusals is the one CRI executed: an observer's reply was DELIVERED,
// although an observer may never send.
func TestReplyRouting_AnswersObeyTheSamePolicyAsAsks(t *testing.T) {
	cases := []struct {
		name         string
		self, peer   string
		wantRefusal  error
		refusalLabel string
	}{
		{"esc answers val", session.RoleEsc, session.RoleVal, nil, ""},
		{"val answers esc", session.RoleVal, session.RoleEsc, nil, ""},
		{"val answers architect", session.RoleVal, session.RoleArchitect, nil, ""},
		{"architect answers val", session.RoleArchitect, session.RoleVal, nil, ""},
		{"critic answers val", session.RoleCritic, session.RoleVal, nil, ""},
		{"observer answers val", session.RoleObserver, session.RoleVal, routing.ErrObserverCannotSend, "observer"},
		{"critic answers esc", session.RoleCritic, session.RoleEsc, routing.ErrCriticMustGoThroughVal, "critic"},
		{"architect answers esc", session.RoleArchitect, session.RoleEsc, routing.ErrArchitectMustGoThroughVal, "architect"},
		{"esc answers architect", session.RoleEsc, session.RoleArchitect, routing.ErrOnlyValMayAddressArchitect, "esc"},
		{"esc answers esc", session.RoleEsc, session.RoleEsc, routing.ErrEscToEscForbidden, "esc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mgr, dataDir := plantRolePair(t, tc.self, tc.peer)
			cfg := nextTestConfig(t, dataDir)
			txn := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "the answer")

			// 1. the gate in front of the journal
			gateErr := routingReplyGate(mgr, replySelf)(txn)
			// 2. the last line of defence, reached without the gate
			delivered, delErr := deliverResponse(cfg, mgr, replySelf, txn)

			if tc.wantRefusal == nil {
				require.NoError(t, gateErr)
				require.NoError(t, delErr)
				assert.True(t, delivered)
				assert.Len(t, gateInboxFiles(t, dataDir, replyPeer), 1)
				return
			}
			require.ErrorIs(t, gateErr, tc.wantRefusal)
			require.ErrorIs(t, delErr, tc.wantRefusal, "deliverResponse must refuse on its own")
			assert.False(t, delivered)
			assert.Empty(t, gateInboxFiles(t, dataDir, replyPeer), "a refused reply writes NOTHING to the recipient")
		})
	}
}

// A forbidden reply through the verb's own path never becomes a journal, and the
// ask it was answering is left open: the answer was refused, not the question.
func TestReplyRouting_ForbiddenReplyLeavesNoJournalAndTheAskOpen(t *testing.T) {
	mgr, dataDir := plantRolePair(t, session.RoleObserver, session.RoleVal)
	cfg := nextTestConfig(t, dataDir)
	deliverAndNotify(t, mgr, dataDir, "msg-aaaaaaaaaaaa", "brief", time.Now().UTC())

	_, err := beginReply(mgr, replySelf, cliReplySelector(mgr, cfg, replySelf, []string{"the report"}, nopReader()), routingReplyGate(mgr, replySelf))
	require.ErrorIs(t, err, errReplyBlocked)
	require.ErrorIs(t, err, routing.ErrObserverCannotSend)

	_, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	assert.False(t, found)
	assert.FileExists(t, filepath.Join(dataDir, "sessions", replySelf, "inbox", "msg-aaaaaaaaaaaa.json"), "the ask is still open")
	assert.Empty(t, gateInboxFiles(t, dataDir, replyPeer))
}

// A pending journal written BEFORE the policy covered replies, towards a pair the
// policy now refuses: it is not delivered, not discarded and not overwritten.
func TestReplyRouting_RecoveredPendingJournalToAForbiddenPairIsBlockedAndKept(t *testing.T) {
	mgr, dataDir := plantRolePair(t, session.RoleEsc, session.RoleArchitect)
	old := newTxn(replySelf, []string{"msg-aaaaaaaaaaaa"}, "written long ago")
	require.NoError(t, mgr.WriteReplyTxn(replySelf, old))
	var called bool

	_, err := beginReply(mgr, replySelf, staticSelector(&called, newTxn(replySelf, []string{"msg-bbbbbbbbbbbb"}, "new")), routingReplyGate(mgr, replySelf))
	require.ErrorIs(t, err, errReplyBlocked)
	require.ErrorIs(t, err, routing.ErrOnlyValMayAddressArchitect)
	assert.False(t, called)

	onDisk, found, rerr := mgr.ReadReplyTxn(replySelf)
	require.NoError(t, rerr)
	require.True(t, found)
	assert.Equal(t, "written long ago", onDisk.Content)
	assert.Empty(t, gateInboxFiles(t, dataDir, replyPeer))
}

func nopReader() *strings.Reader { return strings.NewReader("") }
