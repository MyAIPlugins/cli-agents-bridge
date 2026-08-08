package session

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// WakeCursorSchemaVersion is the on-disk format of wake-cursor.json. Bump it
// only for a breaking change; readers tolerate a newer minor shape because an
// unknown field must never cost a redelivery.
const WakeCursorSchemaVersion = 1

// wakeCursorFile is the per-session cursor recording which messages have been
// handed to a `next`. It lives beside manifest.json rather than inside it, for
// the same reason listener.json does (see listener.go:17-28): manifestMu only
// serialises writers inside one process, while this record is read-modified by
// separate processes and must be fenced by the cross-process session lock.
const wakeCursorFile = "wake-cursor.json"

// WakeCursor is the NOTIFIED set: message ids already delivered by a `next`,
// each with the LOCAL time it was delivered.
//
// It stores exact ids, not a "last seen" watermark: message ids carry no
// ordering, and paging emits non-contiguous subsets, so a watermark would
// silently skip whatever fell outside the emitted page.
//
// NotifiedAt is local on purpose — the sender's timestamp is not trusted, and
// the retention rules of DESIGN §2.3 are expressed against local delivery time.
type WakeCursor struct {
	SchemaVersion int                  `json:"schemaVersion"`
	Notified      map[string]time.Time `json:"notified"`
	// Replayed holds ids that were moved back to UNREAD by a join, so the next
	// delivery can SAY they are a re-delivery. Without it ForgetNotified erased
	// them without a trace and a replayed message was indistinguishable from a
	// fresh one — the marker §2.3 asks for could not be produced at all.
	// Cleared as soon as the message is emitted again.
	Replayed map[string]bool `json:"replayed,omitempty"`
}

// WasReplayed reports whether id is being delivered again after a join replay.
func (c *WakeCursor) WasReplayed(id string) bool {
	return c != nil && c.Replayed[id]
}

// IsNotified reports whether id has already been delivered to a `next`.
func (c *WakeCursor) IsNotified(id string) bool {
	if c == nil || c.Notified == nil {
		return false
	}
	_, ok := c.Notified[id]
	return ok
}

func (m *Manager) wakeCursorPath(sessionID string) string {
	return filepath.Join(m.sessionDir(sessionID), wakeCursorFile)
}

// ReadWakeCursor loads the cursor. A missing or unreadable cursor yields an
// EMPTY cursor plus a human-readable warning, never an error.
//
// This is a deliberate divergence from notify-watch's state file, which fails
// hard on malformed JSON (notify_watch_state.go:53). The two have opposite
// failure costs: there, replaying re-fires side-effecting hooks; here, an empty
// cursor only re-delivers messages, and the model is at-least-once. Treating a
// damaged cursor as "everything was already notified" would silently swallow
// live mail — the one outcome the design forbids.
// Does not validate sessionID — callers validate via security.ValidateSessionID
// (SC-4), matching the sessionDir convention.
func (m *Manager) ReadWakeCursor(sessionID string) (*WakeCursor, string, error) {
	empty := &WakeCursor{SchemaVersion: WakeCursorSchemaVersion, Notified: map[string]time.Time{}}

	data, err := os.ReadFile(m.wakeCursorPath(sessionID))
	if err != nil {
		if os.IsNotExist(err) {
			return empty, "", nil
		}
		return empty, fmt.Sprintf("wake cursor unreadable (%v) — replaying: some messages may be delivered twice; treat them normally", err), nil
	}

	var c WakeCursor
	if err := json.Unmarshal(data, &c); err != nil {
		return empty, fmt.Sprintf("wake cursor corrupt (%v) — replaying: some messages may be delivered twice; treat them normally", err), nil
	}
	if c.Notified == nil {
		c.Notified = map[string]time.Time{}
	}
	if c.SchemaVersion > WakeCursorSchemaVersion {
		// Forward-compat: a newer writer may know more, but its ids are still
		// ids. Honour them rather than replaying the whole mailbox.
		return &c, fmt.Sprintf("wake cursor schemaVersion %d is newer than %d — honouring known ids", c.SchemaVersion, WakeCursorSchemaVersion), nil
	}
	return &c, "", nil
}

// CommitWakeCursor adds ids to the NOTIFIED set under the cross-process session
// lock, re-checking ownership INSIDE the critical section before writing.
//
// The re-check is the point of the whole function: a `next` that lost the wait
// ownership while emitting must not persist a delivery on behalf of the
// instance that replaced it, or those messages would be lost for the new owner.
// Same shape as touchHeartbeatOwned (manager.go:486-492).
//
// prune, when non-nil, reports which ids still exist in the mailbox; ids absent
// from it are dropped, so the cursor cannot grow forever as messages are
// archived out from under it.
//
// Returns evicted=true when ownership was lost — the caller has already emitted
// and must report the delivery as unconfirmed, not silently succeed.
func (m *Manager) CommitWakeCursor(sessionID string, ids []string, now time.Time, ownerOK func() bool, prune map[string]bool) (evicted bool, err error) {
	release, err := AcquireLock(filepath.Join(m.sessionDir(sessionID), "lock"), false)
	if err != nil {
		return false, fmt.Errorf("wake cursor: acquire session lock: %w", err)
	}
	defer func() { _ = release() }()

	if ownerOK != nil && !ownerOK() {
		return true, nil
	}

	cursor, _, err := m.ReadWakeCursor(sessionID)
	if err != nil {
		return false, err
	}
	for _, id := range ids {
		cursor.Notified[id] = now.UTC()
		// Delivered again: the replay marker has served its purpose.
		delete(cursor.Replayed, id)
	}
	if prune != nil {
		for id := range cursor.Notified {
			if !prune[id] {
				delete(cursor.Notified, id)
			}
		}
		// Prune BOTH halves. The invariant is declared on the cursor, so
		// honouring it on one field only left `replayed` holding ghost ids
		// forever — a message can vanish from the mailbox between the join that
		// armed the replay and the next that would have consumed the marker.
		for id := range cursor.Replayed {
			if !prune[id] {
				delete(cursor.Replayed, id)
			}
		}
	}
	cursor.SchemaVersion = WakeCursorSchemaVersion

	if err := transportfs.AtomicWriteJSON(m.wakeCursorPath(sessionID), cursor); err != nil {
		return false, fmt.Errorf("wake cursor: write: %w", err)
	}
	return false, nil
}

// ForgetNotified removes ids from the NOTIFIED set under the session lock, so
// they become UNREAD again and the next `next` re-delivers them.
//
// This is the redelivery half of the state machine (DESIGN §2.3), and without
// it the cursor only ever grows: a page of `next` lost to a compact left its
// asks NOTIFIED forever, no later `next` showed them again, and the next reply
// to that sender CLOSED them without this life having ever seen them.
//
// Applied to ASKS ONLY by the caller — a query stays actionable until answered,
// while `tell` and `response` are one-shot per cursor and must not be woken
// again days later.
func (m *Manager) ForgetNotified(sessionID string, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	release, err := AcquireLock(filepath.Join(m.sessionDir(sessionID), "lock"), false)
	if err != nil {
		return fmt.Errorf("wake cursor: acquire session lock: %w", err)
	}
	defer func() { _ = release() }()

	cursor, _, err := m.ReadWakeCursor(sessionID)
	if err != nil {
		return err
	}
	changed := false
	for _, id := range ids {
		if _, ok := cursor.Notified[id]; ok {
			delete(cursor.Notified, id)
			if cursor.Replayed == nil {
				cursor.Replayed = map[string]bool{}
			}
			cursor.Replayed[id] = true
			changed = true
		}
	}
	if !changed {
		return nil
	}
	cursor.SchemaVersion = WakeCursorSchemaVersion
	if err := transportfs.AtomicWriteJSON(m.wakeCursorPath(sessionID), cursor); err != nil {
		return fmt.Errorf("wake cursor: write: %w", err)
	}
	return nil
}
