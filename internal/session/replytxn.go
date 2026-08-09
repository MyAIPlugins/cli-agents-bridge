package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	// CloseIDs is the frozen, ordered set. An ask that arrives after the
	// snapshot is NOT here: it stays open for the next reply.
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

// WriteReplyTxn persists the journal atomically at 0600.
func (m *Manager) WriteReplyTxn(sessionID string, txn *ReplyTxn) error {
	txn.SchemaVersion = ReplyTxnSchemaVersion
	if err := transportfs.AtomicWriteJSON(m.replyTxnPath(sessionID), txn); err != nil {
		return fmt.Errorf("reply journal: write: %w", err)
	}
	return nil
}

// RemoveReplyTxn deletes the journal once the transaction is complete.
func (m *Manager) RemoveReplyTxn(sessionID string) error {
	if err := os.Remove(m.replyTxnPath(sessionID)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("reply journal: remove: %w", err)
	}
	return nil
}

// WithSessionLock runs fn while holding the cross-process session lock. It is
// how `reply` freezes its set, and the same fencing `cleanup` must respect so
// it cannot remove a session mid-transaction (§2.3).
func (m *Manager) WithSessionLock(sessionID string, fn func() error) error {
	release, err := AcquireLock(filepath.Join(m.sessionDir(sessionID), "lock"), false)
	if err != nil {
		return fmt.Errorf("session lock: %w", err)
	}
	defer func() { _ = release() }()
	return fn()
}
