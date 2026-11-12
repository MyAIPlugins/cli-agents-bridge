package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/myAIPlugins/cli-agents-bridge/internal/security"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// ReplyTxnSchemaVersion is the on-disk format of reply-txn.json.
const ReplyTxnSchemaVersion = 1

const replyTxnFile = "reply-txn.json"

// Reply transaction states.
const (
	// ReplyTxnPending means the set is frozen but the response is not known to
	// have been delivered yet.
	ReplyTxnPending = "pending"
	// ReplyTxnSent means delivery succeeded; only archiving may remain.
	ReplyTxnSent = "sent"
)

// ReplyTxn is the journal that makes `reply` logically exactly-once
// (DESIGN v0.8 §2.3).
//
// `reply` mutates two directories — the recipient's inbox and our own
// inbox/→processed/ — and there is no filesystem transaction spanning both
// (atomic.go guarantees one rename, not a pair). The journal closes the gap:
// the set is frozen ONCE, the response id is derived deterministically, and a
// retry completes what is missing WITHOUT sending again.
//
// Everything needed to rebuild the exact same response is frozen here —
// including Timestamp and Content. Without them a retry would produce a
// different payload under the same id, and the create-if-absent check could no
// longer tell "already delivered" from "a different message collided".
type ReplyTxn struct {
	SchemaVersion int    `json:"schemaVersion"`
	ResponseID    string `json:"responseId"`
	To            string `json:"to"`
	// Anchor is the oldest open ask of the set; it becomes the response's
	// inReplyTo, because the schema holds a single one.
	Anchor string `json:"anchor"`
	// CloseIDs is the frozen, ordered set: ONE delivery of open asks, chosen by
	// the caller (cmd/cab-bridge/verbs.go, oldestPage).
	//
	// It used to say "an ask that arrives after the snapshot is not here", which
	// was true and beside the point — the dangerous ask arrives BEFORE the
	// snapshot and after the agent stopped reading (F-109). What keeps it out is
	// belonging to a later delivery, not to a later snapshot.
	CloseIDs []string `json:"closeIds"`
	State    string   `json:"state"`
	// ArchivedIndex is how many CloseIDs have been archived. A crash between
	// two archives resumes here instead of restarting.
	ArchivedIndex int       `json:"archivedIndex"`
	Timestamp     time.Time `json:"timestamp"`
	Content       string    `json:"content"`
}

// DeterministicResponseID derives the response id from (responder, anchor), so
// every retry of the same logical reply reuses one id.
//
// This is what `--skip-duplicate` cannot do (ask.go:83-102): that one is
// optional, content- and time-window-dependent, and rides on a best-effort
// outbox — it does not identify the inbound transaction.
func DeterministicResponseID(responderSessionID, anchor string) string {
	sum := sha256.Sum256([]byte(responderSessionID + "\x00" + anchor))
	return "msg-" + hex.EncodeToString(sum[:6])
}

func (m *Manager) replyTxnPath(sessionID string) string {
	return filepath.Join(m.sessionDir(sessionID), replyTxnFile)
}

// ReadReplyTxn loads an in-flight reply transaction, if any.
//
// A corrupt journal is an ERROR, not an empty result — the opposite choice from
// the wake cursor, because the failure costs are opposite: replaying a cursor
// re-delivers (harmless), while ignoring a journal could send a second copy of
// a response that was already delivered. When in doubt, refuse to act.
//
// Does not validate sessionID — callers validate via security.ValidateSessionID.
func (m *Manager) ReadReplyTxn(sessionID string) (*ReplyTxn, bool, error) {
	data, err := security.ReadOwnedFile(m.replyTxnPath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, fmt.Errorf("reply journal: read: %w", err)
	}
	var txn ReplyTxn
	if err := json.Unmarshal(data, &txn); err != nil {
		return nil, false, fmt.Errorf("reply journal at %s is unreadable (%w) — inspect it by hand rather than risk a duplicate response", m.replyTxnPath(sessionID), err)
	}
	return &txn, true, nil
}

// WriteReplyTxn persists the journal atomically at 0600. It is for the CREATION of a journal,
// which happens once, under the session lock, in beginReply (cmd/cab-bridge/reply_core.go): it
// replaces whatever file is there, so a driver that already holds a transaction must never call
// it to record progress. UpdateReplyTxn and CompleteReplyTxn are the operations for that, and
// they check that the file is still the driver's own (F-176).
func (m *Manager) WriteReplyTxn(sessionID string, txn *ReplyTxn) error {
	txn.SchemaVersion = ReplyTxnSchemaVersion
	if err := transportfs.AtomicWriteJSON(m.replyTxnPath(sessionID), txn); err != nil {
		return fmt.Errorf("reply journal: write: %w", err)
	}
	return nil
}

// SameTransaction says whether o is the same transaction as t: the fields that were frozen when
// the journal was created. State and ArchivedIndex are progress, which two drivers of one
// transaction legitimately hold at different points, and SchemaVersion is the format; none of the
// three is identity.
//
// What this proves is that the two commit to the same INTENTION: which asks to close, and which
// answer (id, recipient, text, instant). It does not prove the same bytes on the wire, because
// deliverResponse also reads the current manifests when it builds the message, and the journal
// does not freeze those.
//
// Timestamp is compared as an instant: a zone or a monotonic reading is not a different
// transaction. Content is compared as a string, which is only sound because a text that is not
// valid UTF-8 never reaches a journal (the writers refuse it): JSON would store it as U+FFFD and
// the copy in memory would differ from the copy on disk.
func (t *ReplyTxn) SameTransaction(o *ReplyTxn) bool {
	return t.ResponseID == o.ResponseID &&
		t.To == o.To &&
		t.Anchor == o.Anchor &&
		slices.Equal(t.CloseIDs, o.CloseIDs) &&
		t.Timestamp.Equal(o.Timestamp) &&
		t.Content == o.Content
}

var (
	// ErrReplyTxnGone: the journal the driver is carrying is no longer on disk. The text is
	// about what THIS CHECK did; it does not say who removed the file, and it does not say the
	// command as a whole changed nothing, because the driver may already have delivered the
	// response and archived some asks before it got here.
	ErrReplyTxnGone = errors.New("the journal of this reply is no longer on disk, so this check wrote nothing and removed nothing. " +
		"The response may already have been delivered and the archiving may already have advanced, by this command before the check as well: " +
		"look at your asks with `next`, and at what you sent, before replying again")

	// ErrReplyTxnNotYours: the journal on disk is another transaction's. The check left it as
	// it found it. Same limit on what is claimed as ErrReplyTxnGone.
	ErrReplyTxnNotYours = errors.New("the journal on disk is not the one this command was completing, and this check left it as it was. " +
		"What this command was completing may already have been delivered and archived in part, before the check as well: " +
		"look at your asks with `next`, and at what you sent, before replying again")
)

// checkJournalIsMine reads the journal on disk and says whether it is the transaction the caller
// carries. It must be called under the session lock, in the same critical section as the write
// or the removal it guards: read and act under two locks would be a check and then a gamble.
func (m *Manager) checkJournalIsMine(sessionID string, mine *ReplyTxn) error {
	onDisk, found, err := m.ReadReplyTxn(sessionID)
	if err != nil {
		return err
	}
	if !found {
		return ErrReplyTxnGone
	}
	if !mine.SameTransaction(onDisk) {
		return fmt.Errorf("%w (the journal on disk is for response %s)", ErrReplyTxnNotYours, onDisk.ResponseID)
	}
	return nil
}

// UpdateReplyTxn records the progress of the transaction the caller carries, under the session
// lock and only if the journal on disk is still that transaction. A journal that is gone is not
// recreated, and another transaction's is not overwritten: the error says which, and the file is
// as it was.
func (m *Manager) UpdateReplyTxn(sessionID string, mine *ReplyTxn) error {
	return m.WithSessionLock(sessionID, func() error {
		if err := m.checkJournalIsMine(sessionID, mine); err != nil {
			return err
		}
		return m.WriteReplyTxn(sessionID, mine)
	})
}

// CompleteReplyTxn removes the journal once the transaction is complete, under the session lock
// and only if the journal on disk is still the caller's.
//
// The removal goes through security.RemoveFile, whose retry on Windows deletes by NAME. That is
// safe here for two reasons that are properties of this code, not of the helper: the removal runs
// under the exclusive lock between processes, and after the comparison in the same critical
// section, so no writer that follows the protocol can put another journal under the name between
// the check and the removal. The retry alone protects nothing.
func (m *Manager) CompleteReplyTxn(sessionID string, mine *ReplyTxn) error {
	return m.WithSessionLock(sessionID, func() error {
		if err := m.checkJournalIsMine(sessionID, mine); err != nil {
			return err
		}
		if err := security.RemoveFile(m.replyTxnPath(sessionID)); err != nil {
			return fmt.Errorf("reply journal: remove: %w", err)
		}
		return nil
	})
}

// WithSessionLock runs fn while holding the cross-process session lock. It is
// how `reply` freezes its set, and the same fencing `cleanup` must respect so
// it cannot remove a session mid-transaction (§2.3).
func (m *Manager) WithSessionLock(sessionID string, fn func() error) error {
	release, err := m.AcquireSessionLock(sessionID, m.LockWait)
	if err != nil {
		return fmt.Errorf("session lock: %w", err)
	}
	defer func() { _ = release() }()
	return fn()
}
