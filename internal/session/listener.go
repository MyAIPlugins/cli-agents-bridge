package session

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// ListenerOwner is the cross-process ownership record for the long-running
// LISTENER of a session (B-2), stored in sessions/<id>/listener.json — SEPARATE
// from manifest.json on purpose: manifestMu serializes the manifest RMW only
// IN-process (manager.go:39), so a stale heartbeat from an evicted listener in
// another process could load a pre-reclaim manifest and clobber a revocation
// written there. The listener record lives in its own file, mutated only under
// the session lock, so the revocation cannot be lost.
//
// Token is the ownership discriminant: every claim mints a fresh random token,
// and a reclaim writes a DIFFERENT one, so a previous holder's IsListenerCurrent
// immediately reads false. Generation is monotone (never decreases) — for
// observability and as a sanity floor, not for the fencing decision.
type ListenerOwner struct {
	Generation int       `json:"listenerGeneration"`
	Token      string    `json:"listenerToken"`
	PID        int       `json:"listenerPID"`
	ClaimedAt  time.Time `json:"listenerClaimedAt"`
}

// ReclaimInfo reports what a reclaim superseded, surfaced by register --resume.
type ReclaimInfo struct {
	PrevGeneration int
	NewGeneration  int
	PrevPID        int
	PrevToken      string
}

// listenerPath is the absolute path of a session's listener ownership file.
func (m *Manager) listenerPath(sessionID string) string {
	return filepath.Join(m.sessionDir(sessionID), "listener.json")
}

// generateListenerToken returns 16 hex chars (8 bytes) of randomness — ample to
// make two independent claims colliding effectively impossible.
func generateListenerToken() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate listener token: %w", err)
	}
	return hex.EncodeToString(b[:]), nil
}

// readListenerFile reads listener.json: (owner, true, nil) when present,
// (zero, false, nil) when absent, error on a genuine read/parse failure.
func (m *Manager) readListenerFile(sessionID string) (ListenerOwner, bool, error) {
	data, err := os.ReadFile(m.listenerPath(sessionID))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ListenerOwner{}, false, nil
		}
		return ListenerOwner{}, false, fmt.Errorf("read listener %s: %w", sessionID, err)
	}
	var o ListenerOwner
	if err := json.Unmarshal(data, &o); err != nil {
		return ListenerOwner{}, false, fmt.Errorf("parse listener %s: %w", sessionID, err)
	}
	return o, true, nil
}

// ReadListener is the pure read of the listener ownership record (observability
// for inspect/overview, and the basis of IsListenerCurrent). The bool is false
// when no listener has ever claimed the session.
func (m *Manager) ReadListener(sessionID string) (ListenerOwner, bool, error) {
	return m.readListenerFile(sessionID)
}

// IsListenerCurrent reports whether myToken is the session's CURRENT listener
// token — the ownerOK predicate the consume path and the heartbeat check against
// immediately before acting. It FAILS CLOSED: a missing file, a parse error, or
// a different token all read as "not current" (false), never an error — a
// fencing check must never let a doubtful holder consume.
func (m *Manager) IsListenerCurrent(sessionID, myToken string) bool {
	o, ok, err := m.readListenerFile(sessionID)
	if err != nil || !ok {
		return false
	}
	return o.Token == myToken
}

// claimListenerLocked is the lock-held core of ClaimListener: read the current
// generation (0 if absent), then write gen+1 with a fresh token and our PID. The
// caller MUST hold the session lock (it serializes this read-modify-write across
// processes — AtomicWriteJSON is atomic only at the file level, not across the
// read+write window).
func (m *Manager) claimListenerLocked(sessionID string) (ListenerOwner, error) {
	prev, _, err := m.readListenerFile(sessionID)
	if err != nil {
		return ListenerOwner{}, err
	}
	token, err := generateListenerToken()
	if err != nil {
		return ListenerOwner{}, err
	}
	o := ListenerOwner{
		Generation: prev.Generation + 1,
		Token:      token,
		PID:        os.Getpid(),
		ClaimedAt:  m.now(),
	}
	if err := transportfs.AtomicWriteJSON(m.listenerPath(sessionID), o); err != nil {
		return ListenerOwner{}, fmt.Errorf("claim listener %s: %w", sessionID, err)
	}
	return o, nil
}

// reclaimListenerLocked is the lock-held core of a reclaim: bump the generation
// and write a FRESH revocation token with PID=0 — a token no listener holds, so
// every previous holder's IsListenerCurrent goes false at once. PID=0 marks
// "reclaim-pending": revoked, but the next ClaimListener has not run yet. The
// caller MUST hold the session lock, so a reclaim and the following adopt are
// one atomic critical section (tryReuse).
func (m *Manager) reclaimListenerLocked(sessionID string) (ReclaimInfo, error) {
	prev, existed, err := m.readListenerFile(sessionID)
	if err != nil {
		return ReclaimInfo{}, err
	}
	token, err := generateListenerToken()
	if err != nil {
		return ReclaimInfo{}, err
	}
	o := ListenerOwner{
		Generation: prev.Generation + 1,
		Token:      token, // revocation marker — owned by no listener
		PID:        0,     // reclaim-pending until the next ClaimListener
		ClaimedAt:  m.now(),
	}
	if err := transportfs.AtomicWriteJSON(m.listenerPath(sessionID), o); err != nil {
		return ReclaimInfo{}, fmt.Errorf("reclaim listener %s: %w", sessionID, err)
	}
	info := ReclaimInfo{PrevGeneration: prev.Generation, NewGeneration: o.Generation}
	if existed {
		info.PrevPID = prev.PID
		info.PrevToken = prev.Token
	}
	return info, nil
}

// ClaimListener acquires the session lock and claims listener ownership for the
// current process, returning the new owner record. The caller (listen) keeps the
// returned Token for the lifetime of the listen and passes it to
// IsListenerCurrent. The lock is released before returning — a listener does not
// hold the lock for its lifetime (only the claim's read-modify-write is
// serialized), matching the register/reconnect convention.
func (m *Manager) ClaimListener(sessionID string) (ListenerOwner, error) {
	release, err := AcquireLock(filepath.Join(m.sessionDir(sessionID), "lock"), false)
	if err != nil {
		return ListenerOwner{}, fmt.Errorf("claim listener %s: %w", sessionID, err)
	}
	defer func() { _ = release() }()
	return m.claimListenerLocked(sessionID)
}

// ReclaimListener acquires the session lock and revokes the current listener
// ownership, returning what it superseded. Exposed for tests and a possible
// admin path; the register --resume reclaim instead uses reclaimListenerLocked
// under the lock it already holds, so revoke + adopt are atomic (tryReuse).
func (m *Manager) ReclaimListener(sessionID string) (ReclaimInfo, error) {
	release, err := AcquireLock(filepath.Join(m.sessionDir(sessionID), "lock"), false)
	if err != nil {
		return ReclaimInfo{}, fmt.Errorf("reclaim listener %s: %w", sessionID, err)
	}
	defer func() { _ = release() }()
	return m.reclaimListenerLocked(sessionID)
}
