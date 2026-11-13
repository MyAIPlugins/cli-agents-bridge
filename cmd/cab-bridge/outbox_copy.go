package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/myAIPlugins/cli-agents-bridge/internal/config"
	"github.com/myAIPlugins/cli-agents-bridge/internal/session"
	transportfs "github.com/myAIPlugins/cli-agents-bridge/internal/transport/fs"
)

// F-9 keeps a copy of what a session sends in its own outbox/, so that the agent can verify its
// sends (`sent`, outboxCount). It is a courtesy copy: the message was already delivered when it is
// written, and a copy that cannot be written does not undo the delivery.
//
// It is still a write into the folder of the SENDER's session, and `MkdirAll` creates that folder if
// it is not there: a send that crossed the retirement of its own session used to leave behind a
// folder with an outbox in it and no manifest, which no cleanup pass collects. So the copy is made
// under the sender's session lock, which refuses a session that no longer exists (ErrSessionGone)
// before anything is created (F-177).
//
// The sender's lock is taken AFTER the recipient's was let go of, never inside it: two sessions
// that send to each other would otherwise take the same two locks in opposite orders. The callers
// call this when the delivery section is over.

// outboxWrite writes the copy. Each caller keeps the primitive it always had: a send overwrites, a
// reply never rewrites a copy that is already there.
type outboxWrite func(path string, data []byte, perm os.FileMode) error

// writeOutboxOverwrite is the primitive of `send`.
func writeOutboxOverwrite(path string, data []byte, perm os.FileMode) error {
	return transportfs.AtomicWriteBytes(path, data, perm)
}

// writeOutboxIfAbsent is the primitive of `reply`: a response is written under a deterministic id,
// and a copy that is already there is the same bytes.
func writeOutboxIfAbsent(path string, data []byte, perm os.FileMode) error {
	_, err := transportfs.WriteIfAbsentBytes(path, data, perm)
	return err
}

// copyToOutbox writes the copy of message msgID into the outbox of session fromSID, under that
// session's lock. beforeLock is a test seam, nil in production: it runs right before the lock is
// asked for. The error says why the copy was not written (the lock did not come, the session is
// gone, the folder or the file could not be written); the caller decides what to say, and the
// delivery it follows is not undone.
func copyToOutbox(mgr *session.Manager, cfg config.Config, fromSID, msgID string, data []byte, write outboxWrite, beforeLock func()) error {
	if beforeLock != nil {
		beforeLock()
	}
	outbox := filepath.Join(cfg.DataDir, "sessions", fromSID, "outbox")
	return mgr.WithSessionLock(fromSID, func() error {
		if err := os.MkdirAll(outbox, 0o700); err != nil {
			return fmt.Errorf("mkdir sender outbox: %w", err)
		}
		return write(filepath.Join(outbox, msgID+".json"), data, 0o600)
	})
}
